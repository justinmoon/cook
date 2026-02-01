package env

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sprites "github.com/superfly/sprites-go"
)

const (
	spritesAgentPort   = 7422
	spritesDefaultHome = "/root"
	spritesWorkDir     = "/workspace"
)

// SpritesBackend runs commands in a Fly Sprites sandbox.
type SpritesBackend struct {
	config       Config
	client       *sprites.Client
	sprite       *sprites.Sprite
	spriteName   string
	workDir      string
	agentProxy   *sprites.ProxySession
	agentAddr    string
	tailnetProxy string
}

// NewSpritesBackend creates a new Sprites backend with the given config.
func NewSpritesBackend(cfg Config) (*SpritesBackend, error) {
	client, err := newSpritesClient(cfg)
	if err != nil {
		return nil, err
	}

	name := sanitizeSpriteName(cfg.SandboxName)
	if name == "" {
		name = sanitizeSpriteName(cfg.Name)
	}
	if name == "" {
		name = sanitizeSpriteName(fmt.Sprintf("cook-%d", time.Now().UnixNano()))
	}

	return &SpritesBackend{
		config:     cfg,
		client:     client,
		spriteName: name,
		workDir:    spritesWorkDir,
	}, nil
}

// NewSpritesBackendFromSpriteName reconnects to an existing sprite.
func NewSpritesBackendFromSpriteName(spriteName, hostWorkDir string) (*SpritesBackend, error) {
	client, err := newSpritesClient(Config{})
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	sprite, err := client.GetSprite(ctx, spriteName)
	if err != nil {
		return nil, fmt.Errorf("failed to reconnect to sprite %s: %w", spriteName, err)
	}

	backend := &SpritesBackend{
		client:     client,
		sprite:     sprite,
		spriteName: spriteName,
		workDir:    spritesWorkDir,
		config: Config{
			WorkDir: hostWorkDir,
		},
	}

	if err := backend.ensureAgentProxy(ctx); err != nil {
		return nil, fmt.Errorf("failed to create agent proxy: %w", err)
	}
	if err := backend.ensureAgentRunning(ctx); err != nil {
		return nil, fmt.Errorf("failed to ensure agent running: %w", err)
	}

	return backend, nil
}

// Setup provisions the sprite with the repo cloned and tools installed.
func (b *SpritesBackend) Setup(ctx context.Context) error {
	if b.client == nil {
		return fmt.Errorf("sprites client not initialized")
	}

	if b.sprite == nil {
		sprite, err := b.client.CreateSprite(ctx, b.spriteName, spriteConfigFromEnv())
		if err != nil {
			return fmt.Errorf("failed to create sprite: %w", err)
		}
		b.sprite = sprite
	}

	if err := b.installSandbox(ctx); err != nil {
		return err
	}

	if _, err := b.execWithEnv(ctx, "/", "mkdir -p "+b.workDir); err != nil {
		return fmt.Errorf("failed to create workspace: %w", err)
	}

	// Mark workspace as safe for git (avoids "dubious ownership" errors)
	if _, err := b.execWithEnv(ctx, "/", "git config --global --add safe.directory "+b.workDir); err != nil {
		return fmt.Errorf("git config failed: %w", err)
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

	if err := b.ensureAgentProxy(ctx); err != nil {
		return fmt.Errorf("failed to create agent proxy: %w", err)
	}

	if shouldAutoCheckpoint() {
		if _, err := b.Checkpoint(ctx); err != nil {
			fmt.Printf("Warning: failed to checkpoint sprite: %v\n", err)
		}
	}

	return nil
}

func (b *SpritesBackend) installSandbox(ctx context.Context) error {
	if _, err := b.execRaw(ctx, "test -x /opt/sandbox/bin/bash"); err == nil {
		return nil
	}

	url := spritesTarballURL(b.config)
	if url == "" {
		return fmt.Errorf("SPRITES_TARBALL_URL not set")
	}

	downloadCmd := fmt.Sprintf(`set -e; rm -f /tmp/sandbox.tar.gz; if command -v curl >/dev/null 2>&1; then curl -fsSL --show-error --retry 3 --retry-delay 1 --retry-connrefused %s -o /tmp/sandbox.tar.gz || true; fi; if [ ! -s /tmp/sandbox.tar.gz ]; then if command -v wget >/dev/null 2>&1; then wget -q -O /tmp/sandbox.tar.gz %s || true; fi; fi; if [ ! -s /tmp/sandbox.tar.gz ]; then echo "failed to download sandbox tarball" >&2; exit 1; fi`,
		shellEscape(url), shellEscape(url))
	if output, err := b.execRaw(ctx, downloadCmd); err != nil {
		return fmt.Errorf("failed to download sandbox tarball: %w: %s", err, string(output))
	}

	extractCmd := `if command -v tar >/dev/null 2>&1; then tar -xzf /tmp/sandbox.tar.gz -C /; elif command -v busybox >/dev/null 2>&1; then busybox tar -xzf /tmp/sandbox.tar.gz -C /; else echo "missing tar" >&2; exit 1; fi`
	if output, err := b.execRaw(ctx, extractCmd); err != nil {
		if _, checkErr := b.execRaw(ctx, "test -x /opt/sandbox/bin/bash"); checkErr == nil {
			fmt.Printf("Warning: tar extraction reported errors: %s\n", string(output))
		} else {
			return fmt.Errorf("failed to extract sandbox tarball: %w: %s", err, string(output))
		}
	}

	b.execRaw(ctx, "rm -f /tmp/sandbox.tar.gz")

	return nil
}

func (b *SpritesBackend) setupTailnet(ctx context.Context) error {
	authKey := strings.TrimSpace(os.Getenv("TS_AUTHKEY"))
	if authKey == "" {
		return nil
	}

	hostname := strings.TrimSpace(os.Getenv("TS_HOSTNAME"))
	if hostname == "" {
		hostname = "sprites-" + b.spriteName
	}
	hostname = sanitizeSpriteName(hostname)

	socksPort := "1055"
	cmd := fmt.Sprintf(`
set -euo pipefail

TS_AUTHKEY=%s
TS_HOSTNAME=%s
state_dir=/tmp/tailscale
sock=/tmp/ts.sock
socks_port=%s

if [ -S "$sock" ]; then
  if tailscale --socket="$sock" status >/dev/null 2>&1; then
    exit 0
  fi
fi

mkdir -p "$state_dir"
pkill -x tailscaled >/dev/null 2>&1 || true
rm -f "$sock" >/dev/null 2>&1 || true

nohup tailscaled --tun=userspace-networking --state="$state_dir/tailscaled.state" --socket="$sock" --socks5-server="127.0.0.1:${socks_port}" >/tmp/tailscaled.log 2>&1 &
sleep 1

hostname_flag=""
if [ -n "$TS_HOSTNAME" ]; then
  hostname_flag="--hostname=$TS_HOSTNAME"
fi

tailscale --socket="$sock" up --authkey="$TS_AUTHKEY" $hostname_flag --accept-dns=false
tailscale --socket="$sock" status >/dev/null
`, shellEscape(authKey), shellEscape(hostname), shellEscape(socksPort))

	if output, err := b.execWithEnv(ctx, "/", cmd); err != nil {
		return fmt.Errorf("tailscale userspace failed: %w: %s", err, string(output))
	}

	b.tailnetProxy = "socks5h://127.0.0.1:1055"
	return nil
}

func (b *SpritesBackend) cloneRepo(ctx context.Context) error {
	branch := strings.TrimSpace(b.config.BranchName)
	repo := strings.TrimSpace(b.config.RepoURL)
	if repo == "" {
		return nil
	}

	cmd := fmt.Sprintf("%sgit clone %s %s", b.proxyEnvPrefix(), shellEscape(repo), shellEscape(b.workDir))
	if output, err := b.execWithEnv(ctx, "/", cmd); err != nil {
		return fmt.Errorf("git clone failed: %w: %s", err, string(output))
	}

	if branch != "" {
		checkoutCmd := fmt.Sprintf(
			"cd %s && if git show-ref --verify --quiet refs/remotes/origin/%[2]s; then git checkout -B %[2]s origin/%[2]s; else git checkout -b %[2]s; fi",
			shellEscape(b.workDir),
			branch,
		)
		if output, err := b.execWithEnv(ctx, "/", checkoutCmd); err != nil {
			return fmt.Errorf("git checkout failed: %w: %s", err, string(output))
		}
	}
	return nil
}

func (b *SpritesBackend) setupAgent(ctx context.Context) error {
	agentBinary, err := findAgentBinary()
	if err != nil {
		return fmt.Errorf("cook-agent binary not found: %w", err)
	}

	agentData, err := os.ReadFile(agentBinary)
	if err != nil {
		return fmt.Errorf("failed to read cook-agent: %w", err)
	}

	fsys := b.sprite.Filesystem()
	if err := fsys.WriteFile("/tmp/cook-agent", agentData, 0755); err != nil {
		return fmt.Errorf("failed to write cook-agent: %w", err)
	}

	startCmd := fmt.Sprintf("nohup /tmp/cook-agent -listen :%d > /tmp/cook-agent.log 2>&1 &", spritesAgentPort)
	if output, err := b.execWithEnv(ctx, "/", startCmd); err != nil {
		return fmt.Errorf("failed to start cook-agent: %w: %s", err, string(output))
	}

	if err := b.waitForAgent(ctx); err != nil {
		logs, _ := b.execWithEnv(ctx, "/", "cat /tmp/cook-agent.log")
		return fmt.Errorf("agent failed to start: %w: %s", err, string(logs))
	}

	return nil
}

func (b *SpritesBackend) waitForAgent(ctx context.Context) error {
	for i := 0; i < 20; i++ {
		output, _ := b.execWithEnv(ctx, "/", fmt.Sprintf("nc -z localhost %d && echo OK || echo FAIL", spritesAgentPort))
		if strings.Contains(string(output), "OK") {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("agent did not become ready")
}

func (b *SpritesBackend) copyClaudeAuth(ctx context.Context) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	fsys := b.sprite.Filesystem()

	claudeJSON := filepath.Join(homeDir, ".claude.json")
	if data, err := os.ReadFile(claudeJSON); err == nil {
		if err := fsys.WriteFile("/root/.claude.json", data, 0600); err != nil {
			return err
		}
	}

	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	if keychainData, err := cmd.Output(); err == nil && len(keychainData) > 0 {
		if err := fsys.WriteFile("/root/.claude/.credentials.json", bytesTrim(keychainData), 0600); err != nil {
			return err
		}
	}

	claudeDir := filepath.Join(homeDir, ".claude")
	authFiles := []string{".credentials.json", "settings.json", "settings.local.json"}
	for _, filename := range authFiles {
		srcPath := filepath.Join(claudeDir, filename)
		if data, err := os.ReadFile(srcPath); err == nil {
			if err := fsys.WriteFile(path.Join("/root/.claude", filename), data, 0600); err != nil {
				return err
			}
		}
	}

	return nil
}

func (b *SpritesBackend) setupDotfiles(ctx context.Context) error {
	dotfilesDir := "/root/.dotfiles"

	// Ensure /root exists with proper permissions and clean up any existing dotfiles
	setupCmd := fmt.Sprintf("mkdir -p /root && chmod 755 /root && rm -rf %s && ls -la /root/", shellEscape(dotfilesDir))
	output, err := b.execWithEnv(ctx, "/", setupCmd)
	if err != nil {
		return fmt.Errorf("failed to prepare dotfiles dir: %w: %s", err, string(output))
	}
	fmt.Printf("[dotfiles] /root contents: %s\n", string(output))

	// Don't use Tailscale proxy for public URLs (e.g., GitHub)
	proxyPrefix := ""
	if b.tailnetProxy != "" && !strings.Contains(b.config.Dotfiles, "github.com") {
		proxyPrefix = b.proxyEnvPrefix()
	}

	cloneCmd := fmt.Sprintf("%sgit clone %s %s", proxyPrefix, shellEscape(b.config.Dotfiles), shellEscape(dotfilesDir))
	if output, err := b.execWithEnv(ctx, "/", cloneCmd); err != nil {
		return fmt.Errorf("failed to clone dotfiles: %w: %s", err, string(output))
	}

	symlinkCmd := fmt.Sprintf(`
		cd %s
		for f in $(ls -A | grep -v -E '^(\.git|README\.md|LICENSE)$'); do
			rm -rf "$HOME/$f" 2>/dev/null || true
			ln -sf "%s/$f" "$HOME/$f"
		done
	`, dotfilesDir, dotfilesDir)
	if output, err := b.execWithEnv(ctx, "/", symlinkCmd); err != nil {
		return fmt.Errorf("failed to symlink dotfiles: %w: %s", err, string(output))
	}

	return nil
}

func (b *SpritesBackend) ensureAgentProxy(ctx context.Context) error {
	if b.agentProxy != nil {
		return nil
	}

	proxy, err := b.client.ProxyPort(context.Background(), b.spriteName, 0, spritesAgentPort)
	if err != nil {
		return err
	}

	addr := proxy.LocalAddr()
	if addr == nil {
		proxy.Close()
		return fmt.Errorf("proxy did not expose local address")
	}
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		proxy.Close()
		return fmt.Errorf("unexpected address type: %T", addr)
	}

	b.agentProxy = proxy
	b.agentAddr = net.JoinHostPort(tcpAddr.IP.String(), strconv.Itoa(tcpAddr.Port))
	return nil
}

func (b *SpritesBackend) ensureAgentRunning(ctx context.Context) error {
	if _, err := b.execWithEnv(ctx, "/", fmt.Sprintf("nc -z localhost %d", spritesAgentPort)); err == nil {
		return nil
	}
	return b.setupAgent(ctx)
}

// Exec runs a command in the sprite and returns combined output.
func (b *SpritesBackend) Exec(ctx context.Context, cmdStr string) ([]byte, error) {
	output, err := b.execWithEnv(ctx, b.workDir, cmdStr)
	if err != nil {
		return output, err
	}
	return output, nil
}

// Command is not supported for Sprites - use cook-agent for PTY.
func (b *SpritesBackend) Command(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	return nil, fmt.Errorf("Command() not supported for Sprites backend - use cook-agent for PTY")
}

// ReadFile reads a file from the sprite.
func (b *SpritesBackend) ReadFile(ctx context.Context, filePath string) ([]byte, error) {
	resolved := b.resolvePath(filePath)
	data, err := b.sprite.Filesystem().ReadFile(resolved)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// WriteFile writes a file to the sprite.
func (b *SpritesBackend) WriteFile(ctx context.Context, filePath string, content []byte) error {
	resolved := b.resolvePath(filePath)
	return b.sprite.Filesystem().WriteFile(resolved, content, 0644)
}

// ListFiles lists files in a directory.
func (b *SpritesBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
	resolved := b.resolvePath(dir)
	entries, err := b.sprite.Filesystem().ReadDir(resolved)
	if err != nil {
		return nil, err
	}

	files := make([]FileInfo, 0, len(entries))
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, FileInfo{
			Name:  entry.Name(),
			Path:  path.Join(resolved, entry.Name()),
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	return files, nil
}

// WorkDir returns the working directory path.
func (b *SpritesBackend) WorkDir() string {
	return b.workDir
}

// Status returns the sprite status.
func (b *SpritesBackend) Status(ctx context.Context) (Status, error) {
	if b.sprite == nil {
		return Status{State: StateStopped}, nil
	}

	info, err := b.client.GetSprite(ctx, b.spriteName)
	if err != nil {
		return Status{State: StateError, Message: err.Error(), ID: b.spriteName}, nil
	}

	state := StateRunning
	switch strings.ToLower(info.Status) {
	case "stopped", "stopping", "suspended":
		state = StateStopped
	case "starting", "provisioning":
		state = StateStarting
	case "error", "failed":
		state = StateError
	}

	return Status{
		State:   state,
		Message: info.Status,
		ID:      b.spriteName,
	}, nil
}

// Teardown terminates the sprite.
func (b *SpritesBackend) Teardown(ctx context.Context) error {
	if b.agentProxy != nil {
		b.agentProxy.Close()
	}
	if shouldKeepSprite() {
		return nil
	}
	if b.sprite == nil {
		return nil
	}
	return b.sprite.Delete(ctx)
}

// SpriteName returns the sprite name.
func (b *SpritesBackend) SpriteName() string {
	return b.spriteName
}

// AgentAddr returns the local proxy address for cook-agent.
func (b *SpritesBackend) AgentAddr() string {
	return b.agentAddr
}

// Checkpoint creates a checkpoint and returns its ID.
func (b *SpritesBackend) Checkpoint(ctx context.Context) (string, error) {
	comment := fmt.Sprintf("cook checkpoint %s", time.Now().UTC().Format(time.RFC3339Nano))
	stream, err := b.sprite.CreateCheckpointWithComment(ctx, comment)
	if err != nil {
		return "", err
	}

	if err := stream.ProcessAll(func(msg *sprites.StreamMessage) error {
		if msg.Type == "error" {
			return fmt.Errorf("checkpoint error: %s", msg.Error)
		}
		return nil
	}); err != nil {
		return "", err
	}

	checkpoints, err := b.sprite.ListCheckpointsWithOptions(ctx, sprites.ListCheckpointsOptions{IncludeAuto: true})
	if err != nil {
		return "", err
	}
	for _, checkpoint := range checkpoints {
		if checkpoint.Comment == comment {
			return checkpoint.ID, nil
		}
	}

	return "", fmt.Errorf("checkpoint created but ID not found")
}

// RestoreFromCheckpoint restores the sprite from a checkpoint.
func (b *SpritesBackend) RestoreFromCheckpoint(ctx context.Context, checkpointID string) error {
	stream, err := b.sprite.RestoreCheckpoint(ctx, checkpointID)
	if err != nil {
		return err
	}

	return stream.ProcessAll(func(msg *sprites.StreamMessage) error {
		if msg.Type == "error" {
			return fmt.Errorf("restore error: %s", msg.Error)
		}
		return nil
	})
}

func (b *SpritesBackend) execWithEnv(ctx context.Context, dir, cmdStr string) ([]byte, error) {
	if b.sprite == nil {
		return nil, fmt.Errorf("sprite not initialized")
	}

	cmd := b.sprite.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	cmd.Env = b.execEnv()
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
}

func (b *SpritesBackend) proxyEnvPrefix() string {
	if b.tailnetProxy == "" {
		return ""
	}
	proxy := shellEscape(b.tailnetProxy)
	return fmt.Sprintf("ALL_PROXY=%s HTTP_PROXY=%s HTTPS_PROXY=%s http_proxy=%s https_proxy=%s ",
		proxy, proxy, proxy, proxy, proxy)
}

func (b *SpritesBackend) execRaw(ctx context.Context, cmdStr string) ([]byte, error) {
	if b.sprite == nil {
		return nil, fmt.Errorf("sprite not initialized")
	}

	cmd := b.sprite.CommandContext(ctx, "/bin/sh", "-c", cmdStr)
	cmd.Dir = "/"
	return cmd.CombinedOutput()
}

func (b *SpritesBackend) execEnv() []string {
	return []string{
		"PATH=/opt/sandbox/bin:/opt/sandbox/sbin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"HOME=" + spritesDefaultHome,
		"TERM=xterm-256color",
		"SSL_CERT_FILE=/opt/sandbox/etc/ssl/certs/ca-bundle.crt",
		"NODE_PATH=/root/.npm-global/lib/node_modules",
	}
}

func (b *SpritesBackend) resolvePath(p string) string {
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

func newSpritesClient(cfg Config) (*sprites.Client, error) {
	token := spritesToken(cfg)
	if token == "" {
		return nil, fmt.Errorf("SPRITES_TOKEN not set")
	}

	opts := []sprites.Option{}
	if baseURL := spritesBaseURL(); baseURL != "" {
		opts = append(opts, sprites.WithBaseURL(baseURL))
	}

	return sprites.New(token, opts...), nil
}

func spritesToken(cfg Config) string {
	if cfg.Secrets != nil {
		if token := cfg.Secrets["SPRITES_TOKEN"]; token != "" {
			return token
		}
		if token := cfg.Secrets["SPRITE_TOKEN"]; token != "" {
			return token
		}
	}

	if token := os.Getenv("SPRITES_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("SPRITE_TOKEN"); token != "" {
		return token
	}
	if token := os.Getenv("COOK_SPRITES_TOKEN"); token != "" {
		return token
	}

	return ""
}

func spritesBaseURL() string {
	if baseURL := os.Getenv("SPRITES_API_URL"); baseURL != "" {
		return baseURL
	}
	if baseURL := os.Getenv("SPRITES_BASE_URL"); baseURL != "" {
		return baseURL
	}
	if baseURL := os.Getenv("COOK_SPRITES_API_URL"); baseURL != "" {
		return baseURL
	}
	return ""
}

func spritesTarballURL(cfg Config) string {
	if cfg.Secrets != nil {
		if url := cfg.Secrets["SPRITES_TARBALL_URL"]; url != "" {
			return url
		}
	}
	if url := os.Getenv("SPRITES_TARBALL_URL"); url != "" {
		return url
	}
	if url := os.Getenv("COOK_SPRITES_TARBALL_URL"); url != "" {
		return url
	}
	return ""
}

func spriteConfigFromEnv() *sprites.SpriteConfig {
	cfg := sprites.SpriteConfig{}
	set := false

	if v := envInt("SPRITES_RAM_MB", "COOK_SPRITES_RAM_MB"); v > 0 {
		cfg.RamMB = v
		set = true
	}
	if v := envInt("SPRITES_CPUS", "COOK_SPRITES_CPUS"); v > 0 {
		cfg.CPUs = v
		set = true
	}
	if v := envInt("SPRITES_STORAGE_GB", "COOK_SPRITES_STORAGE_GB"); v > 0 {
		cfg.StorageGB = v
		set = true
	}
	if v := os.Getenv("SPRITES_REGION"); v != "" {
		cfg.Region = v
		set = true
	}
	if v := os.Getenv("COOK_SPRITES_REGION"); v != "" {
		cfg.Region = v
		set = true
	}

	if !set {
		return nil
	}
	return &cfg
}

func shouldAutoCheckpoint() bool {
	return envBool("SPRITES_AUTO_CHECKPOINT") || envBool("COOK_SPRITES_AUTO_CHECKPOINT")
}

func shouldKeepSprite() bool {
	return envBool("SPRITES_KEEP") || envBool("COOK_SPRITES_KEEP")
}

func envBool(key string) bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	return val == "1" || val == "true" || val == "yes" || val == "y"
}

func envInt(keys ...string) int {
	for _, key := range keys {
		if val := strings.TrimSpace(os.Getenv(key)); val != "" {
			parsed, err := strconv.Atoi(val)
			if err == nil {
				return parsed
			}
		}
	}
	return 0
}

func sanitizeSpriteName(name string) string {
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
	return cleaned
}

func shellEscape(value string) string {
	if value == "" {
		return "''"
	}
	escaped := strings.ReplaceAll(value, "'", "'\"'\"'")
	return "'" + escaped + "'"
}

func bytesTrim(data []byte) []byte {
	return []byte(strings.TrimSpace(string(data)))
}

// Ensure SpritesBackend implements Backend
var _ Backend = (*SpritesBackend)(nil)
