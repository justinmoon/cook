package env

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

const (
	defaultDockerImage = "cook-env:latest"
	containerPrefix    = "cook-"
	agentPort          = 7422
)

// DockerBackend runs commands in a Docker container.
type DockerBackend struct {
	config      Config
	client      *client.Client
	containerID string
	workDir     string // path inside container
	hostWorkDir string // path on host (for bind mount)
	agentPort   int    // port cook-agent listens on inside container
	imageName   string // docker image to use
}

// NewDockerBackend creates a new Docker backend with the given config.
func NewDockerBackend(cfg Config) (*DockerBackend, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &DockerBackend{
		config:      cfg,
		client:      cli,
		workDir:     "/workspace",
		hostWorkDir: cfg.WorkDir,
		agentPort:   agentPort,
		imageName:   defaultDockerImage,
	}, nil
}

// NewDockerBackendFromExisting reconnects to an existing container.
func NewDockerBackendFromExisting(containerID, hostWorkDir string) (*DockerBackend, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &DockerBackend{
		client:      cli,
		containerID: containerID,
		workDir:     "/workspace",
		hostWorkDir: hostWorkDir,
		agentPort:   agentPort,
		imageName:   defaultDockerImage,
	}, nil
}

// Setup provisions the Docker container with the repo cloned.
func (b *DockerBackend) Setup(ctx context.Context) error {
	// Build or pull the Docker image
	if err := b.prepareImage(ctx); err != nil {
		return fmt.Errorf("failed to prepare image: %w", err)
	}

	// Create host work directory if it doesn't exist
	if err := os.MkdirAll(b.hostWorkDir, 0755); err != nil {
		return fmt.Errorf("failed to create host work dir: %w", err)
	}

	// Clone repo to host directory first (so it's available via bind mount)
	if b.config.RepoURL != "" {
		if err := b.cloneRepo(ctx); err != nil {
			return fmt.Errorf("failed to clone repo: %w", err)
		}
	}

	// Create and start container
	if err := b.createContainer(ctx); err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	// Copy and start cook-agent inside the container
	if err := b.setupAgent(ctx); err != nil {
		return fmt.Errorf("failed to setup agent: %w", err)
	}

	// Copy Claude auth files if they exist
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

// prepareImage ensures the Docker image exists (builds from Dockerfile if needed)
func (b *DockerBackend) prepareImage(ctx context.Context) error {
	// Check if image already exists
	_, _, err := b.client.ImageInspectWithRaw(ctx, b.imageName)
	if err == nil {
		fmt.Printf("Using existing image: %s\n", b.imageName)
		return nil
	}

	// Image doesn't exist, try to build it from Dockerfile.env
	fmt.Printf("Building Docker image: %s\n", b.imageName)
	return b.buildDefaultImage(ctx)
}

func (b *DockerBackend) buildDefaultImage(ctx context.Context) error {
	// Find Dockerfile.env - look relative to the cook binary
	dockerfilePath, err := findDockerfile()
	if err != nil {
		return fmt.Errorf("Dockerfile.env not found: %w", err)
	}

	// Build the image using docker CLI
	cmd := exec.CommandContext(ctx, "docker", "build", "-t", b.imageName, "-f", dockerfilePath, filepath.Dir(dockerfilePath))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker build failed: %w", err)
	}

	return nil
}

func findDockerfile() (string, error) {
	// Try common locations
	candidates := []string{
		"Dockerfile.env",
		"./Dockerfile.env",
		filepath.Join(filepath.Dir(os.Args[0]), "Dockerfile.env"),
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return filepath.Abs(path)
		}
	}

	return "", fmt.Errorf("Dockerfile.env not found")
}



func (b *DockerBackend) cloneRepo(ctx context.Context) error {
	// Clone to host directory (will be bind-mounted into container)
	cmd := exec.CommandContext(ctx, "git", "clone", b.config.RepoURL, b.hostWorkDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	// Create and checkout branch if specified
	if b.config.BranchName != "" {
		cmd = exec.CommandContext(ctx, "git", "-C", b.hostWorkDir, "checkout", "-b", b.config.BranchName)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git checkout -b failed: %s: %w", string(output), err)
		}
	}

	return nil
}

func (b *DockerBackend) createContainer(ctx context.Context) error {
	containerName := containerPrefix + b.config.Name

	// Check if container already exists
	containers, err := b.client.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return err
	}
	for _, c := range containers {
		for _, name := range c.Names {
			if name == "/"+containerName {
				// Container exists, try to start it if stopped
				if c.State != "running" {
					if err := b.client.ContainerStart(ctx, c.ID, container.StartOptions{}); err != nil {
						return fmt.Errorf("failed to start existing container: %w", err)
					}
				}
				b.containerID = c.ID
				return nil
			}
		}
	}

	// Build mounts list
	mounts := []mount.Mount{
		{
			Type:   mount.TypeBind,
			Source: b.hostWorkDir,
			Target: b.workDir,
		},
	}



	// Create new container with host network for easy port access
	resp, err := b.client.ContainerCreate(ctx, &container.Config{
		Image:        b.imageName,
		Cmd:          []string{"sleep", "infinity"},
		Tty:          true,
		WorkingDir:   b.workDir,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
	}, &container.HostConfig{
		NetworkMode: "host",
		Mounts:      mounts,
	}, nil, nil, containerName)
	if err != nil {
		return fmt.Errorf("failed to create container: %w", err)
	}

	b.containerID = resp.ID

	// Start container
	if err := b.client.ContainerStart(ctx, b.containerID, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container: %w", err)
	}

	return nil
}

func (b *DockerBackend) setupAgent(ctx context.Context) error {
	// Find cook-agent binary - look in same directory as cook binary first
	agentBinary, err := findAgentBinary()
	if err != nil {
		return fmt.Errorf("cook-agent binary not found: %w", err)
	}

	// Copy cook-agent into the container (use /tmp which always exists)
	copyCmd := exec.CommandContext(ctx, "docker", "cp", agentBinary, b.containerID+":/tmp/cook-agent")
	if output, err := copyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to copy cook-agent: %s: %w", string(output), err)
	}

	// Make it executable
	_, err = b.Exec(ctx, "chmod +x /tmp/cook-agent")
	if err != nil {
		return fmt.Errorf("failed to chmod cook-agent: %w", err)
	}

	// Start cook-agent in the background
	// Using nohup and redirecting to a log file so it persists
	_, err = b.Exec(ctx, fmt.Sprintf(
		"nohup /tmp/cook-agent -listen :%d > /tmp/cook-agent.log 2>&1 &",
		b.agentPort,
	))
	if err != nil {
		return fmt.Errorf("failed to start cook-agent: %w", err)
	}

	// Wait briefly for agent to start
	// TODO: proper health check
	exec.CommandContext(ctx, "sleep", "0.5").Run()

	return nil
}

// findAgentBinary locates the cook-agent binary
func findAgentBinary() (string, error) {
	// Try common locations
	candidates := []string{
		// Same directory as current executable
		filepath.Join(filepath.Dir(os.Args[0]), "cook-agent"),
		// Development path (from project root)
		"./cook-agent",
		"./cmd/cook-agent/cook-agent",
		// Development path (from internal/env during tests)
		"../../cook-agent",
		// System path
		"/usr/local/bin/cook-agent",
	}

	for _, path := range candidates {
		if _, err := os.Stat(path); err == nil {
			return filepath.Abs(path)
		}
	}

	// Try to find in PATH
	if path, err := exec.LookPath("cook-agent"); err == nil {
		return path, nil
	}

	return "", fmt.Errorf("cook-agent not found in any expected location")
}

// AgentAddr returns the address to connect to cook-agent.
// Since we use host networking, it's just localhost:port.
func (b *DockerBackend) AgentAddr() string {
	return fmt.Sprintf("localhost:%d", b.agentPort)
}

func (b *DockerBackend) copyClaudeAuth(ctx context.Context) error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return err
	}

	// Create .claude directory in container
	if _, err := b.Exec(ctx, "mkdir -p /home/dev/.claude"); err != nil {
		return fmt.Errorf("failed to create .claude dir: %w", err)
	}

	// Helper to copy a file to container, resolving symlinks
	copyFile := func(src, dst string) error {
		// Resolve symlink if needed
		realPath, err := filepath.EvalSymlinks(src)
		if err != nil {
			return err
		}

		// Read file content
		content, err := os.ReadFile(realPath)
		if err != nil {
			return err
		}
		hostSize := len(content)

		// Create temp file with resolved content
		tmpFile, err := os.CreateTemp("", "claude-auth-*")
		if err != nil {
			return err
		}
		defer os.Remove(tmpFile.Name())

		if _, err := tmpFile.Write(content); err != nil {
			return err
		}
		tmpFile.Close()

		// Copy to /tmp in container first (avoids permission issues)
		tmpDst := "/tmp/" + filepath.Base(dst)
		cmd := exec.CommandContext(ctx, "docker", "cp", tmpFile.Name(), b.containerID+":"+tmpDst)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("docker cp: %s: %w", string(output), err)
		}

		// Move to final location as root and fix ownership
		mvCmd := fmt.Sprintf("cp %s %s && chown dev:dev %s && chmod 600 %s && rm %s", tmpDst, dst, dst, dst, tmpDst)
		if _, err := b.ExecAsRoot(ctx, mvCmd); err != nil {
			return fmt.Errorf("move file: %w", err)
		}

		// Verify copy succeeded by comparing file sizes
		output, err := b.ExecAsRoot(ctx, fmt.Sprintf("wc -c < %s", dst))
		if err != nil {
			return fmt.Errorf("verify file size: %w", err)
		}
		var containerSize int
		fmt.Sscanf(strings.TrimSpace(string(output)), "%d", &containerSize)
		if containerSize != hostSize {
			return fmt.Errorf("file copy verification failed for %s: host=%d bytes, container=%d bytes", dst, hostSize, containerSize)
		}

		return nil
	}

	// Copy ~/.claude.json (contains oauthAccount)
	claudeJsonPath := filepath.Join(homeDir, ".claude.json")
	if err := copyFile(claudeJsonPath, "/home/dev/.claude.json"); err != nil {
		fmt.Printf("Note: ~/.claude.json not copied: %v\n", err)
	}

	// On macOS, OAuth token is stored in Keychain - extract and save to container
	// Linux Claude Code reads from ~/.claude/.credentials.json
	cmd := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "Claude Code-credentials", "-w")
	if keychainData, err := cmd.Output(); err == nil && len(keychainData) > 0 {
		// Write credentials to container
		credPath := "/home/dev/.claude/.credentials.json"
		escaped := strings.ReplaceAll(string(keychainData), "'", "'\"'\"'")
		if _, err := b.ExecAsRoot(ctx, fmt.Sprintf("echo '%s' > %s && chown dev:dev %s && chmod 600 %s", 
			strings.TrimSpace(escaped), credPath, credPath, credPath)); err != nil {
			fmt.Printf("Warning: failed to copy keychain credentials: %v\n", err)
		}
	}

	// Copy files from ~/.claude/
	claudeDir := filepath.Join(homeDir, ".claude")
	authFiles := []string{".credentials.json", "settings.json", "settings.local.json"}
	for _, filename := range authFiles {
		srcPath := filepath.Join(claudeDir, filename)
		dstPath := "/home/dev/.claude/" + filename
		if err := copyFile(srcPath, dstPath); err != nil {
			// Not an error - file might not exist
			continue
		}
	}

	// Fix ownership
	b.Exec(ctx, "chown -R dev:dev /home/dev/.claude /home/dev/.claude.json 2>/dev/null")

	return nil
}

func (b *DockerBackend) setupDotfiles(ctx context.Context) error {
	dotfilesDir := "/home/dev/.dotfiles"

	// Clone dotfiles repo inside container
	_, err := b.Exec(ctx, fmt.Sprintf("git clone %s %s", b.config.Dotfiles, dotfilesDir))
	if err != nil {
		return fmt.Errorf("failed to clone dotfiles: %w", err)
	}

	// Symlink dotfiles to home (excluding git and readme files)
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

// Exec runs a command in the container and returns combined output.
func (b *DockerBackend) Exec(ctx context.Context, cmdStr string) ([]byte, error) {
	return b.execWithUser(ctx, cmdStr, "")
}

// ExecAsRoot runs a command in the container as root.
func (b *DockerBackend) ExecAsRoot(ctx context.Context, cmdStr string) ([]byte, error) {
	return b.execWithUser(ctx, cmdStr, "root")
}

func (b *DockerBackend) execWithUser(ctx context.Context, cmdStr string, user string) ([]byte, error) {
	if b.containerID == "" {
		return nil, fmt.Errorf("container not initialized")
	}

	execConfig := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmdStr},
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   b.workDir,
		User:         user,
	}

	execID, err := b.client.ContainerExecCreate(ctx, b.containerID, execConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create exec: %w", err)
	}

	resp, err := b.client.ContainerExecAttach(ctx, execID.ID, container.ExecStartOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer resp.Close()

	// Read output - Docker multiplexes stdout/stderr
	var stdout, stderr bytes.Buffer
	_, err = stdcopy.StdCopy(&stdout, &stderr, resp.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read exec output: %w", err)
	}

	// Combine stdout and stderr
	combined := append(stdout.Bytes(), stderr.Bytes()...)
	return combined, nil
}

// Command returns an *exec.Cmd that would run in the container.
// For Docker, this returns a docker exec command.
func (b *DockerBackend) Command(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if b.containerID == "" {
		return nil, fmt.Errorf("container not initialized")
	}

	// Build docker exec command
	dockerArgs := []string{"exec", "-it", "-w", b.workDir, b.containerID, name}
	dockerArgs = append(dockerArgs, args...)

	cmd := exec.CommandContext(ctx, "docker", dockerArgs...)
	return cmd, nil
}

// ReadFile reads a file from the container (via bind mount on host).
func (b *DockerBackend) ReadFile(ctx context.Context, path string) ([]byte, error) {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(absPath)
}

// WriteFile writes a file to the container (via bind mount on host).
func (b *DockerBackend) WriteFile(ctx context.Context, path string, content []byte) error {
	absPath, err := b.resolvePath(path)
	if err != nil {
		return err
	}

	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(absPath), 0755); err != nil {
		return fmt.Errorf("failed to create parent directory: %w", err)
	}

	return os.WriteFile(absPath, content, 0644)
}

// ListFiles lists files in a directory.
func (b *DockerBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
	absDir := b.hostWorkDir
	if dir != "" && dir != "." {
		var err error
		absDir, err = b.resolvePath(dir)
		if err != nil {
			return nil, err
		}
	}

	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	var files []FileInfo
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		relPath := entry.Name()
		if dir != "" && dir != "." {
			relPath = filepath.Join(dir, entry.Name())
		}

		files = append(files, FileInfo{
			Name:  entry.Name(),
			Path:  relPath,
			IsDir: entry.IsDir(),
			Size:  info.Size(),
		})
	}

	return files, nil
}

// WorkDir returns the working directory path (inside container).
func (b *DockerBackend) WorkDir() string {
	return b.hostWorkDir
}

// ContainerID returns the Docker container ID.
func (b *DockerBackend) ContainerID() string {
	return b.containerID
}

// Status returns the container status.
func (b *DockerBackend) Status(ctx context.Context) (Status, error) {
	if b.containerID == "" {
		return Status{State: StateStopped, Message: "container not created"}, nil
	}

	info, err := b.client.ContainerInspect(ctx, b.containerID)
	if err != nil {
		return Status{State: StateError, Message: err.Error()}, nil
	}

	state := StateStopped
	switch {
	case info.State.Running:
		state = StateRunning
	case info.State.Restarting:
		state = StateStarting
	case info.State.Dead || info.State.OOMKilled:
		state = StateError
	}

	return Status{
		State:   state,
		Message: info.State.Status,
		ID:      b.containerID[:12],
	}, nil
}

// Teardown stops and removes the container.
func (b *DockerBackend) Teardown(ctx context.Context) error {
	if b.containerID == "" {
		return nil
	}

	// Stop container
	b.client.ContainerStop(ctx, b.containerID, container.StopOptions{})

	// Remove container
	if err := b.client.ContainerRemove(ctx, b.containerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("failed to remove container: %w", err)
	}

	// Remove host work directory
	if b.hostWorkDir != "" {
		os.RemoveAll(b.hostWorkDir)
	}

	b.containerID = ""
	return nil
}

// resolvePath resolves a relative path within the host work directory.
func (b *DockerBackend) resolvePath(path string) (string, error) {
	root, err := filepath.EvalSymlinks(b.hostWorkDir)
	if err != nil {
		return "", fmt.Errorf("invalid working directory: %w", err)
	}

	cleanPath := filepath.Clean("/" + path)
	absPath := filepath.Join(root, cleanPath)

	if !strings.HasPrefix(absPath, root+string(filepath.Separator)) && absPath != root {
		return "", fmt.Errorf("path escapes working directory: %s", path)
	}

	return absPath, nil
}

// Close releases resources.
func (b *DockerBackend) Close() error {
	if b.client != nil {
		return b.client.Close()
	}
	return nil
}

// Ensure DockerBackend implements Backend
var _ Backend = (*DockerBackend)(nil)
