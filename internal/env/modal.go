package env

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/modal-labs/libmodal/modal-go"
)

const (
	modalAppName   = "cook-sandbox"
	modalAgentPort = 7422
)

// ModalBackend runs commands in a Modal sandbox.
type ModalBackend struct {
	config       Config
	client       *modal.Client
	app          *modal.App
	sandbox      *modal.Sandbox
	sandboxID    string
	workDir      string
	agentTunnel  string // Tunnel URL for cook-agent
	tailnetProxy string
}

// NewModalBackend creates a new Modal backend with the given config.
func NewModalBackend(cfg Config) (*ModalBackend, error) {
	client, err := modal.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create modal client: %w", err)
	}

	return &ModalBackend{
		config:  cfg,
		client:  client,
		workDir: "/workspace",
	}, nil
}

// NewModalBackendFromSandboxID reconnects to an existing Modal sandbox.
func NewModalBackendFromSandboxID(sandboxID string, hostWorkDir string) (*ModalBackend, error) {
	client, err := modal.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create modal client: %w", err)
	}

	ctx := context.Background()
	app, err := client.Apps.FromName(ctx, modalAppName, &modal.AppFromNameParams{CreateIfMissing: true})
	if err != nil {
		return nil, fmt.Errorf("failed to get modal app: %w", err)
	}

	sandbox, err := client.Sandboxes.FromID(ctx, sandboxID)
	if err != nil {
		return nil, fmt.Errorf("failed to reconnect to sandbox %s: %w", sandboxID, err)
	}

	tunnels, err := sandbox.Tunnels(ctx, 30*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to get tunnels: %w", err)
	}
	var agentTunnel string
	if tunnel, ok := tunnels[modalAgentPort]; ok {
		agentTunnel = tunnel.URL()
	}
	if agentTunnel == "" {
		return nil, fmt.Errorf("no tunnel found for port %d", modalAgentPort)
	}

	return &ModalBackend{
		client:      client,
		app:         app,
		sandbox:     sandbox,
		sandboxID:   sandboxID,
		workDir:     "/workspace",
		agentTunnel: agentTunnel,
	}, nil
}

// Setup provisions the Modal sandbox with the repo cloned.
func (b *ModalBackend) Setup(ctx context.Context) error {
	// Get or create the Modal app
	app, err := b.client.Apps.FromName(ctx, modalAppName, &modal.AppFromNameParams{CreateIfMissing: true})
	if err != nil {
		return fmt.Errorf("failed to create modal app: %w", err)
	}
	b.app = app

	// Create sandbox with pre-built image (tools already installed via nix)
	image := b.client.Images.FromRegistry("ghcr.io/justinmoon/cook-sandbox:v3", nil)

	sandboxName := b.config.SandboxName
	if sandboxName == "" {
		sandboxName = b.config.Name
	}

	start := time.Now()
	fmt.Printf("Creating Modal sandbox...\n")
	sandbox, err := b.client.Sandboxes.Create(ctx, app, image, &modal.SandboxCreateParams{
		Name: sandboxName,
		// Add environment variables
		Env: map[string]string{
			"HOME":    "/root",
			"TERM":    "xterm-256color",
			"WORKDIR": b.workDir,
		},
		// Expose cook-agent port via tunnel
		EncryptedPorts: []int{modalAgentPort},
		// Set reasonable timeout (1 hour)
		Timeout: time.Hour,
	})
	if err != nil {
		return fmt.Errorf("failed to create sandbox: %w", err)
	}
	b.sandbox = sandbox
	b.sandboxID = sandbox.SandboxID
	fmt.Printf("Modal sandbox created: %s (took %v)\n", b.sandboxID, time.Since(start))

	// Create workspace directory
	start = time.Now()
	if _, err := b.Exec(ctx, "mkdir -p "+b.workDir); err != nil {
		return fmt.Errorf("failed to create workspace: %w", err)
	}
	fmt.Printf("Created workspace (took %v)\n", time.Since(start))

	if err := b.setupTailnet(ctx); err != nil {
		return fmt.Errorf("failed to setup tailnet: %w", err)
	}

	// Clone the repo
	start = time.Now()
	if err := b.cloneRepo(ctx); err != nil {
		return fmt.Errorf("failed to clone repo: %w", err)
	}
	fmt.Printf("Cloned repo (took %v)\n", time.Since(start))

	// Copy and start cook-agent
	start = time.Now()
	if err := b.setupAgent(ctx); err != nil {
		return fmt.Errorf("failed to setup agent: %w", err)
	}
	fmt.Printf("Setup agent (took %v)\n", time.Since(start))

	// Copy Claude auth if available
	if err := b.copyClaudeAuth(ctx); err != nil {
		fmt.Printf("Warning: failed to copy Claude auth: %v\n", err)
	}

	// Setup dotfiles if specified
	if b.config.Dotfiles != "" {
		if err := b.setupDotfiles(ctx); err != nil {
			return fmt.Errorf("failed to setup dotfiles: %w", err)
		}
	}

	return nil
}

func (b *ModalBackend) installTools(ctx context.Context) error {
	// Install git, curl, and other basic tools
	cmds := []string{
		"apt-get update",
		"apt-get install -y git curl wget vim neovim ripgrep jq procps netcat-openbsd",
		"npm install -g @anthropic-ai/claude-code",
		"rm -rf /var/lib/apt/lists/*",
	}
	for _, cmd := range cmds {
		if _, err := b.Exec(ctx, cmd); err != nil {
			return fmt.Errorf("failed to run %q: %w", cmd, err)
		}
	}
	return nil
}

func (b *ModalBackend) setupTailnet(ctx context.Context) error {
	authKey := strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
	if authKey == "" {
		return nil
	}

	if _, err := b.Exec(ctx, "if [ ! -x /usr/bin/env ]; then mkdir -p /usr/bin && ln -s /bin/env /usr/bin/env; fi"); err != nil {
		return fmt.Errorf("failed to ensure /usr/bin/env: %w", err)
	}

	if err := b.ensureCookTSUp(ctx); err != nil {
		return err
	}

	hostname := strings.TrimSpace(os.Getenv("TS_HOSTNAME"))
	if hostname == "" {
		hostname = "modal-" + b.sandboxID
	}
	hostname = sanitizeSpriteName(hostname)

	forceUserspace := strings.TrimSpace(os.Getenv("TS_FORCE_USERSPACE"))
	if forceUserspace == "" {
		forceUserspace = "1"
	}
	cmd := fmt.Sprintf(
		"TS_AUTHKEY=%s TS_HOSTNAME=%s TS_FORCE_USERSPACE=%s cook-ts-up",
		shellEscape(authKey),
		shellEscape(hostname),
		shellEscape(forceUserspace),
	)
	output, err := b.Exec(ctx, cmd)
	if err != nil {
		return fmt.Errorf("cook-ts-up failed: %w: %s", err, string(output))
	}

	if strings.Contains(string(output), "tailscale mode: userspace") {
		b.tailnetProxy = "socks5h://127.0.0.1:1055"
	}

	return nil
}

func (b *ModalBackend) ensureCookTSUp(ctx context.Context) error {
	if _, err := b.Exec(ctx, "command -v cook-ts-up >/dev/null 2>&1 && grep -q 'tailscale_bin' \"$(command -v cook-ts-up)\""); err == nil {
		return nil
	}

	scriptPath := filepath.Join("scripts", "tailscale", "fly-up.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		return fmt.Errorf("cook-ts-up missing and failed to read %s: %w", scriptPath, err)
	}

	encoded := encodeBase64(script)
	installCmd := fmt.Sprintf("echo -n '%s' | base64 -d > /bin/cook-ts-up && chmod +x /bin/cook-ts-up", encoded)
	if _, err := b.Exec(ctx, installCmd); err != nil {
		return fmt.Errorf("failed to install cook-ts-up: %w", err)
	}

	return nil
}

func (b *ModalBackend) cloneRepo(ctx context.Context) error {
	cmd := fmt.Sprintf("%sgit clone %s %s", b.proxyEnvPrefix(), b.config.RepoURL, b.workDir)
	if _, err := b.Exec(ctx, cmd); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	if b.config.BranchName != "" {
		branch := b.config.BranchName
		checkoutCmd := fmt.Sprintf(
			"cd %s && if git show-ref --verify --quiet refs/remotes/origin/%[2]s; then git checkout -B %[2]s origin/%[2]s; else git checkout -b %[2]s; fi",
			b.workDir,
			branch,
		)
		if _, err := b.Exec(ctx, checkoutCmd); err != nil {
			return fmt.Errorf("git checkout failed: %w", err)
		}
	}
	return nil
}

func (b *ModalBackend) setupAgent(ctx context.Context) error {
	agentCmd := "cook-agent"
	if _, err := b.Exec(ctx, "command -v cook-agent"); err != nil {
		agentPath, err := findAgentBinary()
		if err != nil {
			return fmt.Errorf("cook-agent binary not found: %w", err)
		}

		agentData, err := os.ReadFile(agentPath)
		if err != nil {
			return fmt.Errorf("failed to read cook-agent: %w", err)
		}

		encoded := encodeBase64(agentData)

		const chunkSize = 50000
		tmpPath := "/tmp/cook-agent.b64"

		b.Exec(ctx, fmt.Sprintf("rm -f %s", tmpPath))

		for i := 0; i < len(encoded); i += chunkSize {
			end := i + chunkSize
			if end > len(encoded) {
				end = len(encoded)
			}
			chunk := encoded[i:end]
			if _, err := b.Exec(ctx, fmt.Sprintf("echo -n '%s' >> %s", chunk, tmpPath)); err != nil {
				return fmt.Errorf("failed to write agent chunk: %w", err)
			}
		}

		if _, err := b.Exec(ctx, fmt.Sprintf("base64 -d %s > /tmp/cook-agent && chmod +x /tmp/cook-agent && rm %s", tmpPath, tmpPath)); err != nil {
			return fmt.Errorf("failed to decode agent: %w", err)
		}

		agentCmd = "/tmp/cook-agent"
	}

	_, err := b.Exec(ctx, fmt.Sprintf("nohup %s > /tmp/cook-agent.log 2>&1 &", agentCmd))
	if err != nil {
		return fmt.Errorf("failed to start agent: %w", err)
	}

	// Wait for agent to be ready
	for i := 0; i < 10; i++ {
		output, _ := b.Exec(ctx, fmt.Sprintf("nc -z localhost %d && echo OK || echo FAIL", modalAgentPort))
		if strings.Contains(string(output), "OK") {
			fmt.Printf("cook-agent started on port %d\n", modalAgentPort)

			// Get the tunnel URL for the agent port
			tunnels, err := b.sandbox.Tunnels(ctx, 30*time.Second)
			if err != nil {
				return fmt.Errorf("failed to get tunnels: %w", err)
			}
			if tunnel, ok := tunnels[modalAgentPort]; ok {
				b.agentTunnel = tunnel.URL()
				fmt.Printf("Agent tunnel URL: %s\n", b.agentTunnel)
			} else {
				return fmt.Errorf("no tunnel found for port %d", modalAgentPort)
			}

			return nil
		}
		// Small delay - exec another command
		b.Exec(ctx, "sleep 0.5")
	}

	// Check logs if failed
	logs, _ := b.Exec(ctx, "cat /tmp/cook-agent.log")
	return fmt.Errorf("agent failed to start, logs: %s", string(logs))
}

func encodeBase64(data []byte) string {
	const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var result strings.Builder
	for i := 0; i < len(data); i += 3 {
		var n uint32
		remaining := len(data) - i
		if remaining >= 3 {
			n = uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
			result.WriteByte(base64Chars[n>>18&63])
			result.WriteByte(base64Chars[n>>12&63])
			result.WriteByte(base64Chars[n>>6&63])
			result.WriteByte(base64Chars[n&63])
		} else if remaining == 2 {
			n = uint32(data[i])<<16 | uint32(data[i+1])<<8
			result.WriteByte(base64Chars[n>>18&63])
			result.WriteByte(base64Chars[n>>12&63])
			result.WriteByte(base64Chars[n>>6&63])
			result.WriteByte('=')
		} else {
			n = uint32(data[i]) << 16
			result.WriteByte(base64Chars[n>>18&63])
			result.WriteByte(base64Chars[n>>12&63])
			result.WriteByte('=')
			result.WriteByte('=')
		}
	}
	return result.String()
}

func (b *ModalBackend) copyClaudeAuth(ctx context.Context) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Create .claude directory
	if _, err := b.Exec(ctx, "mkdir -p /root/.claude"); err != nil {
		return err
	}

	// Copy ~/.claude.json
	claudeJsonPath := filepath.Join(homeDir, ".claude.json")
	if err := b.copyFileToSandbox(ctx, claudeJsonPath, "/root/.claude.json"); err != nil {
		fmt.Printf("Note: ~/.claude.json not copied: %v\n", err)
	}

	// On macOS, OAuth token is in Keychain
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	if keychainData, err := cmd.Output(); err == nil && len(keychainData) > 0 {
		credPath := "/root/.claude/.credentials.json"
		escaped := strings.ReplaceAll(string(keychainData), "'", "'\"'\"'")
		if _, err := b.Exec(ctx, fmt.Sprintf("echo '%s' > %s && chmod 600 %s",
			strings.TrimSpace(escaped), credPath, credPath)); err != nil {
			fmt.Printf("Warning: failed to copy keychain credentials: %v\n", err)
		}
	}

	return nil
}

func (b *ModalBackend) copyFileToSandbox(ctx context.Context, srcPath, dstPath string) error {
	// Resolve symlinks
	realPath, err := filepath.EvalSymlinks(srcPath)
	if err != nil {
		return err
	}

	content, err := os.ReadFile(realPath)
	if err != nil {
		return err
	}

	// Escape for shell
	escaped := strings.ReplaceAll(string(content), "'", "'\"'\"'")
	_, err = b.Exec(ctx, fmt.Sprintf("echo '%s' > %s", escaped, dstPath))
	return err
}

func (b *ModalBackend) setupDotfiles(ctx context.Context) error {
	dotfilesDir := "/root/.dotfiles"

	// Clone dotfiles repo
	_, err := b.Exec(ctx, fmt.Sprintf("%sgit clone %s %s", b.proxyEnvPrefix(), b.config.Dotfiles, dotfilesDir))
	if err != nil {
		return fmt.Errorf("failed to clone dotfiles: %w", err)
	}

	// Symlink dotfiles to home
	_, err = b.Exec(ctx, fmt.Sprintf(`
		cd %s
		for f in $(ls -A | grep -v -E '^(\.git|README\.md|LICENSE)$'); do
			rm -rf ~/"$f" 2>/dev/null || true
			ln -sf "%s/$f" ~/"$f"
		done
	`, dotfilesDir, dotfilesDir))
	if err != nil {
		return fmt.Errorf("failed to symlink dotfiles: %w", err)
	}

	return nil
}

func (b *ModalBackend) proxyEnvPrefix() string {
	if b.tailnetProxy == "" {
		return ""
	}
	proxy := shellEscape(b.tailnetProxy)
	return fmt.Sprintf("ALL_PROXY=%s HTTP_PROXY=%s HTTPS_PROXY=%s http_proxy=%s https_proxy=%s ",
		proxy, proxy, proxy, proxy, proxy)
}

// Exec runs a command in the sandbox and returns combined output.
func (b *ModalBackend) Exec(ctx context.Context, cmdStr string) ([]byte, error) {
	if b.sandbox == nil {
		return nil, fmt.Errorf("sandbox not initialized")
	}

	proc, err := b.sandbox.Exec(ctx, []string{"sh", "-c", cmdStr}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to exec: %w", err)
	}

	stdout, err := io.ReadAll(proc.Stdout)
	if err != nil {
		return nil, fmt.Errorf("failed to read stdout: %w", err)
	}

	stderr, err := io.ReadAll(proc.Stderr)
	if err != nil {
		return nil, fmt.Errorf("failed to read stderr: %w", err)
	}

	output := append(stdout, stderr...)
	exitCode, err := proc.Wait(ctx)
	if err != nil {
		return output, fmt.Errorf("exec wait failed: %w", err)
	}
	if exitCode != 0 {
		return output, fmt.Errorf("exec failed with exit code %d", exitCode)
	}

	return output, nil
}

// Command is not directly supported for Modal - use cook-agent instead.
func (b *ModalBackend) Command(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("Command() not supported for Modal backend - use cook-agent for PTY")
}

// ReadFile reads a file from the sandbox.
func (b *ModalBackend) ReadFile(ctx context.Context, path string) ([]byte, error) {
	output, err := b.Exec(ctx, fmt.Sprintf("cat '%s'", path))
	if err != nil {
		return nil, err
	}
	return output, nil
}

// WriteFile writes a file to the sandbox.
func (b *ModalBackend) WriteFile(ctx context.Context, path string, content []byte) error {
	escaped := strings.ReplaceAll(string(content), "'", "'\"'\"'")
	_, err := b.Exec(ctx, fmt.Sprintf("echo '%s' > '%s'", escaped, path))
	return err
}

// ListFiles lists files in a directory.
func (b *ModalBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
	output, err := b.Exec(ctx, fmt.Sprintf("ls -la '%s'", dir))
	if err != nil {
		return nil, err
	}

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
		files = append(files, FileInfo{
			Name:  name,
			Path:  filepath.Join(dir, name),
			IsDir: isDir,
		})
	}
	return files, nil
}

// WorkDir returns the working directory path.
func (b *ModalBackend) WorkDir() string {
	return b.workDir
}

// Status returns the sandbox status.
func (b *ModalBackend) Status(ctx context.Context) (Status, error) {
	if b.sandbox == nil {
		return Status{State: StateStopped}, nil
	}
	// Modal SDK doesn't have a direct status check, assume running if we have a sandbox
	return Status{
		State: StateRunning,
		ID:    b.sandboxID,
	}, nil
}

// Teardown terminates the sandbox.
func (b *ModalBackend) Teardown(ctx context.Context) error {
	if b.sandbox == nil {
		return nil
	}
	return b.sandbox.Terminate(ctx)
}

// SandboxID returns the Modal sandbox ID.
func (b *ModalBackend) SandboxID() string {
	return b.sandboxID
}

// AgentAddr returns the tunnel URL to connect to cook-agent.
func (b *ModalBackend) AgentAddr() string {
	return b.agentTunnel
}
