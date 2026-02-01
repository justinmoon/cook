package env

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	fly "github.com/superfly/fly-go"
	"github.com/superfly/fly-go/flaps"
	"github.com/superfly/fly-go/tokens"
)

const (
	flyMachinesDefaultApp = "cook-sandbox"
	flyAgentPort          = 7422
	flyWorkDir            = "/workspace"
)

// FlyMachinesBackend runs commands in a Fly Machines VM.
type FlyMachinesBackend struct {
	config       Config
	flapsClient  *flaps.Client
	machine      *fly.Machine
	machineID    string
	workDir      string
	agentAddr    string
	appName      string
	tailnetProxy string
}

// NewFlyMachinesBackend creates a new Fly Machines backend with the given config.
func NewFlyMachinesBackend(cfg Config) (*FlyMachinesBackend, error) {
	client, appName, err := newFlyMachinesClient(cfg)
	if err != nil {
		return nil, err
	}

	backend := &FlyMachinesBackend{
		config:      cfg,
		flapsClient: client,
		workDir:     flyWorkDir,
		appName:     appName,
	}

	if flyMachinesReuseEnabled() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if machine, err := reuseFlyMachine(ctx, client, appName); err != nil {
			return nil, err
		} else if machine != nil {
			backend.machine = machine
			backend.machineID = machine.ID
		}
	}

	return backend, nil
}

// NewFlyMachinesBackendFromMachineID reconnects to an existing machine.
func NewFlyMachinesBackendFromMachineID(machineID, hostWorkDir string) (*FlyMachinesBackend, error) {
	client, appName, err := newFlyMachinesClient(Config{})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	machine, err := client.Get(ctx, appName, machineID)
	if err != nil {
		return nil, fmt.Errorf("failed to get machine %s: %w", machineID, err)
	}

	backend := &FlyMachinesBackend{
		flapsClient: client,
		machine:     machine,
		machineID:   machineID,
		workDir:     flyWorkDir,
		appName:     appName,
		config: Config{
			WorkDir: hostWorkDir,
		},
	}
	backend.agentAddr = flyMachinesAgentAddr(appName)

	return backend, nil
}

// Setup provisions the machine with the repo cloned and tools installed.
func (b *FlyMachinesBackend) Setup(ctx context.Context) error {
	if b.flapsClient == nil {
		return fmt.Errorf("fly machines client not initialized")
	}

	if b.machine == nil {
		config := b.buildMachineConfig()
		name := b.config.SandboxName
		if name == "" {
			name = b.config.Name
		}
		launchInput := fly.LaunchMachineInput{
			Config: config,
			Region: flyMachinesRegion(),
			Name:   sanitizeFlyMachineName(name),
		}

		machine, err := b.flapsClient.Launch(ctx, b.appName, launchInput)
		if err != nil {
			return fmt.Errorf("failed to launch machine: %w", err)
		}
		b.machine = machine
		b.machineID = machine.ID
	}

	if err := b.ensureStarted(ctx); err != nil {
		return err
	}

	if _, err := b.execWithDir(ctx, "/", "mkdir -p "+b.workDir); err != nil {
		return fmt.Errorf("failed to create workspace: %w", err)
	}

	if err := b.setupTailnet(ctx); err != nil {
		return fmt.Errorf("failed to setup tailnet: %w", err)
	}

	if b.config.RepoURL != "" {
		if err := b.cloneRepo(ctx); err != nil {
			return fmt.Errorf("failed to clone repo: %w", err)
		}
	}

	if err := b.setupAgent(ctx); err != nil {
		return fmt.Errorf("failed to setup agent: %w", err)
	}

	if err := b.copyClaudeAuth(ctx); err != nil {
		fmt.Printf("Warning: failed to copy Claude auth: %v\n", err)
	}

	if b.config.Dotfiles != "" {
		if err := b.setupDotfiles(ctx); err != nil {
			return fmt.Errorf("failed to setup dotfiles: %w", err)
		}
	}

	b.agentAddr = flyMachinesAgentAddr(b.appName)
	return nil
}

func (b *FlyMachinesBackend) buildMachineConfig() *fly.MachineConfig {
	config := &fly.MachineConfig{
		Image: flyMachinesImage(b.appName),
		Guest: &fly.MachineGuest{
			CPUKind:  flyMachinesCPUKind(),
			CPUs:     flyMachinesCPUs(),
			MemoryMB: flyMachinesMemoryMB(),
		},
		Env: map[string]string{
			"HOME":    "/root",
			"TERM":    "xterm-256color",
			"WORKDIR": b.workDir,
		},
		Init: fly.MachineInit{
			Cmd: []string{"/bin/sleep", "infinity"},
		},
		Services: []fly.MachineService{
			{
				Protocol:     "tcp",
				InternalPort: flyAgentPort,
				Ports: []fly.MachinePort{
					{
						Port:     fly.Pointer(flyAgentPort),
						Handlers: []string{"tls", "http"},
					},
				},
			},
		},
		AutoDestroy: flyMachinesAutoDestroy(),
		Restart: &fly.MachineRestart{
			Policy: fly.MachineRestartPolicyNo,
		},
	}

	return config
}

func (b *FlyMachinesBackend) ensureStarted(ctx context.Context) error {
	const startTimeout = 180 * time.Second
	const startAttempts = 5

	var lastErr error
	for attempt := 0; attempt < startAttempts; attempt++ {
		machine, err := b.flapsClient.Get(ctx, b.appName, b.machineID)
		if err != nil {
			return fmt.Errorf("failed to get machine status: %w", err)
		}
		b.machine = machine

		shouldRefresh := false
		switch machine.State {
		case fly.MachineStateStarted:
			return nil
		case fly.MachineStateStopped, fly.MachineStateSuspended:
			if _, err := b.flapsClient.Start(ctx, b.appName, b.machineID, ""); err != nil {
				msg := err.Error()
				if !strings.Contains(msg, "machine still starting") && !strings.Contains(msg, "already started") {
					return fmt.Errorf("failed to start machine: %w", err)
				}
			}
			shouldRefresh = true
		case fly.MachineStateCreated:
			// Launch should auto-start; just wait for the state to transition.
		default:
			// fallthrough to wait; state may transition during provision
		}

		if shouldRefresh {
			if refreshed, err := b.flapsClient.Get(ctx, b.appName, b.machineID); err == nil {
				machine = refreshed
				b.machine = refreshed
			}
		}

		if err := b.flapsClient.Wait(ctx, b.appName, machine, fly.MachineStateStarted, startTimeout); err == nil {
			return nil
		} else {
			lastErr = err
			// Re-check state; sometimes machines stay in "starting" but exec works.
			refreshed, getErr := b.flapsClient.Get(ctx, b.appName, b.machineID)
			if getErr == nil {
				b.machine = refreshed
				if refreshed.State == fly.MachineStateStarted {
					return nil
				}
				if refreshed.State == "starting" {
					if _, probeErr := b.execWithDir(ctx, "/", "true"); probeErr == nil {
						return nil
					}
				}
			}
		}
	}

	if lastErr != nil {
		return fmt.Errorf("machine failed to start: %w", lastErr)
	}
	return fmt.Errorf("machine failed to start")
}

func (b *FlyMachinesBackend) setupTailnet(ctx context.Context) error {
	authKey := strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
	if authKey == "" {
		return nil
	}

	if _, err := b.execWithDir(ctx, "/", "if [ ! -x /usr/bin/env ]; then mkdir -p /usr/bin && ln -s /bin/env /usr/bin/env; fi"); err != nil {
		return fmt.Errorf("failed to ensure /usr/bin/env: %w", err)
	}

	if err := b.ensureCookTSUp(ctx); err != nil {
		return err
	}

	hostname := strings.TrimSpace(os.Getenv("TS_HOSTNAME"))
	if hostname == "" && b.machineID != "" {
		hostname = fmt.Sprintf("fly-%s-%s", b.appName, b.machineID)
	}
	if hostname != "" {
		hostname = sanitizeSpriteName(hostname)
	}

	cmd := fmt.Sprintf("TS_AUTHKEY=%s TS_HOSTNAME=%s cook-ts-up", shellEscape(authKey), shellEscape(hostname))
	output, err := b.execWithDir(ctx, "/", cmd)
	if err != nil {
		return fmt.Errorf("cook-ts-up failed: %w: %s", err, string(output))
	}

	if strings.Contains(string(output), "tailscale mode: userspace") {
		b.tailnetProxy = "socks5h://127.0.0.1:1055"
	}

	return nil
}

func (b *FlyMachinesBackend) ensureCookTSUp(ctx context.Context) error {
	if _, err := b.execWithDir(ctx, "/", "command -v cook-ts-up >/dev/null 2>&1 && grep -q 'tailscale_bin' \"$(command -v cook-ts-up)\""); err == nil {
		return nil
	}

	scriptPath := filepath.Join("scripts", "tailscale", "fly-up.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("cook-ts-up missing and failed to read %s: %w", scriptPath, err)
	}

	encoded := base64.StdEncoding.EncodeToString(script)
	installCmd := fmt.Sprintf("echo -n %s | base64 -d > /bin/cook-ts-up && chmod +x /bin/cook-ts-up", shellEscape(encoded))
	if _, err := b.execWithDir(ctx, "/", installCmd); err != nil {
		return fmt.Errorf("failed to install cook-ts-up: %w", err)
	}

	return nil
}

func (b *FlyMachinesBackend) cloneRepo(ctx context.Context) error {
	branch := strings.TrimSpace(b.config.BranchName)
	repo := strings.TrimSpace(b.config.RepoURL)
	if repo == "" {
		return nil
	}

	if _, err := b.execWithDir(ctx, "/", fmt.Sprintf("rm -rf %s && mkdir -p %s", shellEscape(b.workDir), shellEscape(b.workDir))); err != nil {
		return fmt.Errorf("failed to reset workspace: %w", err)
	}

	cmd := fmt.Sprintf("%sgit clone %s %s", b.proxyEnvPrefix(), shellEscape(repo), shellEscape(b.workDir))
	if output, err := b.execWithDir(ctx, "/", cmd); err != nil {
		return fmt.Errorf("git clone failed: %w: %s", err, string(output))
	}

	if branch != "" {
		if _, err := b.execWithDir(ctx, "/", "git config --global --add safe.directory /workspace"); err != nil {
			return fmt.Errorf("git config failed: %w", err)
		}
		checkoutCmd := fmt.Sprintf(
			"cd %s && if git show-ref --verify --quiet refs/remotes/origin/%[2]s; then git checkout -B %[2]s origin/%[2]s; else git checkout -b %[2]s; fi",
			shellEscape(b.workDir),
			branch,
		)
		if output, err := b.execWithDir(ctx, "/", checkoutCmd); err != nil {
			return fmt.Errorf("git checkout failed: %w: %s", err, string(output))
		}
	}
	return nil
}

func (b *FlyMachinesBackend) setupAgent(ctx context.Context) error {
	agentCmd := "cook-agent"
	if _, err := b.execWithDir(ctx, "/", "command -v cook-agent"); err != nil {
		agentPath, err := findAgentBinary()
		if err != nil {
			return fmt.Errorf("cook-agent binary not found: %w", err)
		}

		agentData, err := os.ReadFile(agentPath)
		if err != nil {
			return fmt.Errorf("failed to read cook-agent: %w", err)
		}

		tmpPath := "/tmp/cook-agent.b64"
		if _, err := b.execWithDir(ctx, "/", fmt.Sprintf("rm -f %s", tmpPath)); err != nil {
			return err
		}

		const chunkSize = 32 * 1024
		for i := 0; i < len(agentData); i += chunkSize {
			end := i + chunkSize
			if end > len(agentData) {
				end = len(agentData)
			}
			chunk := agentData[i:end]
			encoded := base64.StdEncoding.EncodeToString(chunk)
			if _, err := b.execWithDir(ctx, "/", fmt.Sprintf("echo -n %s >> %s", shellEscape(encoded), tmpPath)); err != nil {
				return fmt.Errorf("failed to upload agent chunk: %w", err)
			}
		}

		decodeCmd := fmt.Sprintf("base64 -d %s > /tmp/cook-agent && chmod +x /tmp/cook-agent && rm %s", tmpPath, tmpPath)
		if _, err := b.execWithDir(ctx, "/", decodeCmd); err != nil {
			return fmt.Errorf("failed to decode agent: %w", err)
		}

		agentCmd = "/tmp/cook-agent"
	}

	startCmd := fmt.Sprintf("nohup %s -listen :%d > /tmp/cook-agent.log 2>&1 &", agentCmd, flyAgentPort)
	if _, err := b.execWithDir(ctx, "/", startCmd); err != nil {
		return fmt.Errorf("failed to start agent: %w", err)
	}

	if err := b.waitForAgent(ctx); err != nil {
		logs, _ := b.execWithDir(ctx, "/", "cat /tmp/cook-agent.log")
		return fmt.Errorf("agent failed to start: %w: %s", err, string(logs))
	}

	return nil
}

func (b *FlyMachinesBackend) waitForAgent(ctx context.Context) error {
	probeCmd := fmt.Sprintf(
		`if command -v nc >/dev/null 2>&1; then nc -z localhost %d; else bash -lc "echo > /dev/tcp/localhost/%d"; fi`,
		flyAgentPort,
		flyAgentPort,
	)
	for i := 0; i < 12; i++ {
		output, _ := b.execWithDir(ctx, "/", probeCmd+" && echo OK || echo FAIL")
		if strings.Contains(string(output), "OK") {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("agent did not become ready")
}

func (b *FlyMachinesBackend) copyClaudeAuth(ctx context.Context) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	if _, err := b.execWithDir(ctx, "/", "mkdir -p /root/.claude"); err != nil {
		return err
	}

	claudeJSON := filepath.Join(homeDir, ".claude.json")
	if data, err := os.ReadFile(claudeJSON); err == nil {
		if err := b.WriteFile(ctx, "/root/.claude.json", data); err != nil {
			return err
		}
	}

	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	if keychainData, err := cmd.Output(); err == nil && len(keychainData) > 0 {
		if err := b.WriteFile(ctx, "/root/.claude/.credentials.json", bytesTrim(keychainData)); err != nil {
			return err
		}
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	authFiles := []string{".credentials.json", "settings.json", "settings.local.json"}
	for _, filename := range authFiles {
		srcPath := filepath.Join(claudeDir, filename)
		if data, err := os.ReadFile(srcPath); err == nil {
			if err := b.WriteFile(ctx, path.Join("/root/.claude", filename), data); err != nil {
				return err
			}
		}
	}

	return nil
}

func (b *FlyMachinesBackend) setupDotfiles(ctx context.Context) error {
	dotfilesDir := "/root/.dotfiles"
	cloneCmd := fmt.Sprintf("%sgit clone %s %s", b.proxyEnvPrefix(), shellEscape(b.config.Dotfiles), shellEscape(dotfilesDir))
	if output, err := b.execWithDir(ctx, "/", cloneCmd); err != nil {
		return fmt.Errorf("failed to clone dotfiles: %w: %s", err, string(output))
	}

	symlinkCmd := fmt.Sprintf(`
		cd %s
		for f in $(ls -A | grep -v -E '^(\.git|README\.md|LICENSE)$'); do
			rm -rf ~\/"$f" 2>/dev/null || true
			ln -sf "%s/$f" ~\/"$f"
		done
	`, dotfilesDir, dotfilesDir)
	if output, err := b.execWithDir(ctx, "/", symlinkCmd); err != nil {
		return fmt.Errorf("failed to symlink dotfiles: %w: %s", err, string(output))
	}

	return nil
}

// Exec runs a command in the machine and returns combined output.
func (b *FlyMachinesBackend) Exec(ctx context.Context, cmdStr string) ([]byte, error) {
	return b.execWithDir(ctx, b.workDir, cmdStr)
}

// Command is not directly supported for Fly Machines - use cook-agent for PTY.
func (b *FlyMachinesBackend) Command(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("Command() not supported for Fly Machines backend - use cook-agent for PTY")
}

// ReadFile reads a file from the machine.
func (b *FlyMachinesBackend) ReadFile(ctx context.Context, filePath string) ([]byte, error) {
	resolved := b.resolvePath(filePath)
	output, err := b.execWithDir(ctx, "/", fmt.Sprintf("cat %s", shellEscape(resolved)))
	if err != nil {
		return nil, err
	}
	return output, nil
}

// WriteFile writes a file to the machine.
func (b *FlyMachinesBackend) WriteFile(ctx context.Context, filePath string, content []byte) error {
	resolved := b.resolvePath(filePath)
	dir := path.Dir(resolved)
	if _, err := b.execWithDir(ctx, "/", fmt.Sprintf("mkdir -p %s", shellEscape(dir))); err != nil {
		return err
	}

	if len(content) <= 128*1024 {
		encoded := base64.StdEncoding.EncodeToString(content)
		cmd := fmt.Sprintf("echo %s | base64 -d > %s", shellEscape(encoded), shellEscape(resolved))
		if _, err := b.execWithDir(ctx, "/", cmd); err != nil {
			return err
		}
		return nil
	}

	if _, err := b.execWithDir(ctx, "/", fmt.Sprintf("rm -f %s", shellEscape(resolved))); err != nil {
		return err
	}

	const chunkSize = 512 * 1024
	for i := 0; i < len(content); i += chunkSize {
		end := i + chunkSize
		if end > len(content) {
			end = len(content)
		}
		chunk := content[i:end]
		encoded := base64.StdEncoding.EncodeToString(chunk)
		cmd := fmt.Sprintf("echo -n %s | base64 -d >> %s", shellEscape(encoded), shellEscape(resolved))
		if _, err := b.execWithDir(ctx, "/", cmd); err != nil {
			return err
		}
	}
	return nil
}

// ListFiles lists files in a directory.
func (b *FlyMachinesBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
	resolved := b.resolvePath(dir)
	output, err := b.execWithDir(ctx, "/", fmt.Sprintf("ls -la %s", shellEscape(resolved)))
	if err != nil {
		return nil, err
	}

	return parseLsOutput(output, resolved), nil
}

// WorkDir returns the working directory path.
func (b *FlyMachinesBackend) WorkDir() string {
	return b.workDir
}

// Status returns the machine status.
func (b *FlyMachinesBackend) Status(ctx context.Context) (Status, error) {
	if b.machine == nil {
		return Status{State: StateStopped}, nil
	}

	machine, err := b.flapsClient.Get(ctx, b.appName, b.machineID)
	if err != nil {
		return Status{State: StateError, Message: err.Error(), ID: b.machineID}, nil
	}

	state := StateRunning
	switch machine.State {
	case fly.MachineStateStarted:
		state = StateRunning
	case fly.MachineStateStopped, fly.MachineStateSuspended, fly.MachineStateDestroyed:
		state = StateStopped
	case fly.MachineStateCreated, fly.MachineStateDestroying:
		state = StateStarting
	default:
		state = StateError
	}

	return Status{State: state, ID: b.machineID, Message: machine.State}, nil
}

// Teardown stops and destroys the machine.
func (b *FlyMachinesBackend) Teardown(ctx context.Context) error {
	if b.machineID == "" {
		return nil
	}

	_ = b.flapsClient.Stop(ctx, b.appName, fly.StopMachineInput{ID: b.machineID}, "")

	return b.flapsClient.Destroy(ctx, b.appName, fly.RemoveMachineInput{
		ID:   b.machineID,
		Kill: true,
	}, "")
}

// MachineID returns the machine ID.
func (b *FlyMachinesBackend) MachineID() string {
	return b.machineID
}

// AgentAddr returns the public agent URL.
func (b *FlyMachinesBackend) AgentAddr() string {
	return b.agentAddr
}

func (b *FlyMachinesBackend) proxyEnvPrefix() string {
	if b.tailnetProxy == "" {
		return ""
	}
	proxy := shellEscape(b.tailnetProxy)
	return fmt.Sprintf("ALL_PROXY=%s HTTP_PROXY=%s HTTPS_PROXY=%s http_proxy=%s https_proxy=%s ",
		proxy, proxy, proxy, proxy, proxy)
}

func (b *FlyMachinesBackend) execWithDir(ctx context.Context, dir, cmdStr string) ([]byte, error) {
	return b.execWithDirAndStdin(ctx, dir, cmdStr, "")
}

func (b *FlyMachinesBackend) execWithDirAndStdin(ctx context.Context, dir, cmdStr, stdin string) ([]byte, error) {
	if b.machineID == "" {
		return nil, fmt.Errorf("machine not initialized")
	}

	command := cmdStr
	if dir != "" {
		command = fmt.Sprintf("cd %s && %s", shellEscape(dir), cmdStr)
	}

	execReq := &fly.MachineExecRequest{
		Cmd:     fmt.Sprintf("sh -lc %s", shellEscape(command)),
		Timeout: 120,
		Stdin:   stdin,
	}

	resp, err := b.flapsClient.Exec(ctx, b.appName, b.machineID, execReq)
	if err != nil {
		return nil, fmt.Errorf("exec failed: %w", err)
	}

	combined := []byte(resp.StdOut + resp.StdErr)
	if resp.ExitCode != 0 {
		return combined, fmt.Errorf("command failed (exit %d): %s", resp.ExitCode, resp.StdErr)
	}

	return combined, nil
}

func (b *FlyMachinesBackend) resolvePath(p string) string {
	if p == "" {
		return p
	}

	if filepath.IsAbs(p) {
		if b.config.WorkDir != "" {
			base := filepath.Clean(b.config.WorkDir)
			target := filepath.Clean(p)
			if target == base || strings.HasPrefix(target, base+string(filepath.Separator)) {
				rel := strings.TrimPrefix(target, base)
				rel = strings.TrimPrefix(rel, string(filepath.Separator))
				if rel == "" {
					return b.workDir
				}
				return path.Join(b.workDir, filepath.ToSlash(rel))
			}
		}
		return filepath.ToSlash(p)
	}

	return path.Join(b.workDir, filepath.ToSlash(p))
}

func newFlyMachinesClient(cfg Config) (*flaps.Client, string, error) {
	token := flyMachinesToken(cfg)
	if token == "" {
		return nil, "", fmt.Errorf("FLY_API_TOKEN not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	client, err := flaps.NewWithOptions(ctx, flaps.NewClientOpts{
		Tokens: tokens.Parse(token),
	})
	if err != nil {
		return nil, "", err
	}

	return client, flyMachinesAppName(), nil
}

func flyMachinesToken(cfg Config) string {
	if cfg.Secrets != nil {
		if token := cfg.Secrets["FLY_API_TOKEN"]; token != "" {
			return token
		}
		if token := cfg.Secrets["FLY_TOKEN"]; token != "" {
			return token
		}
	}

	if token := os.Getenv("FLY_API_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("FLY_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("COOK_FLY_API_TOKEN"); token != "" {
		return token
	}

	return ""
}

func flyMachinesAppName() string {
	if app := os.Getenv("FLY_MACHINES_APP"); app != "" {
		return app
	}
	if app := os.Getenv("COOK_FLY_MACHINES_APP"); app != "" {
		return app
	}
	return flyMachinesDefaultApp
}

func flyMachinesAgentAddr(appName string) string {
	if addr := os.Getenv("FLY_MACHINES_AGENT_ADDR"); addr != "" {
		return addr
	}
	if addr := os.Getenv("COOK_FLY_MACHINES_AGENT_ADDR"); addr != "" {
		return addr
	}
	return fmt.Sprintf("https://%s.fly.dev:%d", appName, flyAgentPort)
}

func flyMachinesRegion() string {
	if region := os.Getenv("FLY_MACHINES_REGION"); region != "" {
		return region
	}
	if region := os.Getenv("COOK_FLY_MACHINES_REGION"); region != "" {
		return region
	}
	return "ord"
}

func flyMachinesImage(appName string) string {
	if image := os.Getenv("FLY_MACHINES_IMAGE"); image != "" {
		return image
	}
	if image := os.Getenv("COOK_FLY_MACHINES_IMAGE"); image != "" {
		return image
	}
	if appName != "" {
		return fmt.Sprintf("registry.fly.io/%s:cook-env", appName)
	}
	return "ghcr.io/justinmoon/cook-sandbox:latest"
}

func flyMachinesCPUKind() string {
	if kind := os.Getenv("FLY_MACHINES_CPU_KIND"); kind != "" {
		return kind
	}
	if kind := os.Getenv("COOK_FLY_MACHINES_CPU_KIND"); kind != "" {
		return kind
	}
	return "shared"
}

func flyMachinesCPUs() int {
	if cpus := envInt("FLY_MACHINES_CPUS", "COOK_FLY_MACHINES_CPUS"); cpus > 0 {
		return cpus
	}
	return 1
}

func flyMachinesMemoryMB() int {
	if mem := envInt("FLY_MACHINES_MEMORY_MB", "COOK_FLY_MACHINES_MEMORY_MB"); mem > 0 {
		return mem
	}
	return 1024
}

func flyMachinesAutoDestroy() bool {
	return envBool("FLY_MACHINES_AUTO_DESTROY") || envBool("COOK_FLY_MACHINES_AUTO_DESTROY")
}

func flyMachinesReuseEnabled() bool {
	return envBool("FLY_MACHINES_REUSE") || envBool("COOK_FLY_MACHINES_REUSE")
}

func reuseFlyMachine(ctx context.Context, client *flaps.Client, appName string) (*fly.Machine, error) {
	started, err := client.List(ctx, appName, fly.MachineStateStarted)
	if err == nil && len(started) > 0 {
		return started[0], nil
	}
	stopped, err := client.List(ctx, appName, fly.MachineStateStopped)
	if err == nil && len(stopped) > 0 {
		return stopped[0], nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list machines: %w", err)
	}
	return nil, nil
}

func parseLsOutput(output []byte, dir string) []FileInfo {
	var files []FileInfo
	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if line == "" || strings.HasPrefix(line, "total") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 9 {
			continue
		}
		name := fields[8]
		if name == "." || name == ".." {
			continue
		}
		isDir := strings.HasPrefix(fields[0], "d")
		var size int64
		if len(fields) > 4 {
			if parsed, err := strconv.ParseInt(fields[4], 10, 64); err == nil {
				size = parsed
			}
		}
		files = append(files, FileInfo{
			Name:  name,
			Path:  path.Join(dir, name),
			IsDir: isDir,
			Size:  size,
		})
	}
	return files
}

func sanitizeFlyMachineName(name string) string {
	if name == "" {
		return ""
	}

	name = strings.ToLower(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}

	cleaned := strings.Trim(b.String(), "-")
	for strings.Contains(cleaned, "--") {
		cleaned = strings.ReplaceAll(cleaned, "--", "-")
	}
	if len(cleaned) > 63 {
		cleaned = cleaned[:63]
		cleaned = strings.TrimRight(cleaned, "-")
	}
	if cleaned == "" {
		cleaned = "cook-machine"
	}
	return cleaned
}

// Ensure FlyMachinesBackend implements Backend
var _ Backend = (*FlyMachinesBackend)(nil)
