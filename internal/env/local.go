package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LocalBackend runs commands in a local filesystem checkout.
type LocalBackend struct {
	config  Config
	workDir string
}

// NewLocalBackend creates a new local backend with the given config.
// If config.WorkDir is set, it will be used directly.
// Otherwise, Setup() must be called to clone and configure the environment.
func NewLocalBackend(cfg Config) *LocalBackend {
	return &LocalBackend{
		config:  cfg,
		workDir: cfg.WorkDir,
	}
}

// NewLocalBackendFromPath creates a LocalBackend for an existing checkout directory.
// This is useful for wrapping existing checkouts without calling Setup().
func NewLocalBackendFromPath(workDir string) *LocalBackend {
	return &LocalBackend{
		workDir: workDir,
	}
}

// Setup clones the repo and creates the branch checkout.
// If workDir is already set (from config or NewLocalBackendFromPath), this is a no-op.
func (b *LocalBackend) Setup(ctx context.Context) error {
	if b.workDir != "" {
		// Already have a working directory
		if _, err := os.Stat(b.workDir); err == nil {
			return nil
		}
		// Directory doesn't exist, need to create it
	}

	if b.config.RepoURL == "" {
		return fmt.Errorf("RepoURL is required for Setup")
	}

	// Create checkout directory
	if err := os.MkdirAll(filepath.Dir(b.workDir), 0755); err != nil {
		return fmt.Errorf("failed to create checkout parent dir: %w", err)
	}

	// Clone the repo
	cmd := exec.CommandContext(ctx, "git", "clone", b.config.RepoURL, b.workDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	// Create and checkout the branch
	if b.config.BranchName != "" {
		cmd = exec.CommandContext(ctx, "git", "-C", b.workDir, "checkout", "-b", b.config.BranchName)
		if output, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(b.workDir)
			return fmt.Errorf("git checkout -b failed: %s: %w", string(output), err)
		}
	}

	return nil
}

// Exec runs a command in the working directory and returns combined output.
func (b *LocalBackend) Exec(ctx context.Context, cmdStr string) ([]byte, error) {
	if b.workDir == "" {
		return nil, fmt.Errorf("backend not initialized: call Setup() first")
	}

	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Dir = b.workDir
	return cmd.CombinedOutput()
}

// Command returns an *exec.Cmd configured to run in the working directory.
func (b *LocalBackend) Command(ctx context.Context, name string, args ...string) (*exec.Cmd, error) {
	if b.workDir == "" {
		return nil, fmt.Errorf("backend not initialized: call Setup() first")
	}

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = b.workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	return cmd, nil
}

// ReadFile reads a file from the working directory.
func (b *LocalBackend) ReadFile(ctx context.Context, path string) ([]byte, error) {
	if b.workDir == "" {
		return nil, fmt.Errorf("backend not initialized: call Setup() first")
	}

	absPath, err := b.resolvePath(path)
	if err != nil {
		return nil, err
	}

	return os.ReadFile(absPath)
}

// WriteFile writes a file to the working directory.
func (b *LocalBackend) WriteFile(ctx context.Context, path string, content []byte) error {
	if b.workDir == "" {
		return fmt.Errorf("backend not initialized: call Setup() first")
	}

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

// ListFiles lists files in a directory within the working directory.
func (b *LocalBackend) ListFiles(ctx context.Context, dir string) ([]FileInfo, error) {
	if b.workDir == "" {
		return nil, fmt.Errorf("backend not initialized: call Setup() first")
	}

	absDir := b.workDir
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

// WorkDir returns the working directory path.
func (b *LocalBackend) WorkDir() string {
	return b.workDir
}

// Status returns the backend status.
func (b *LocalBackend) Status(ctx context.Context) (Status, error) {
	if b.workDir == "" {
		return Status{State: StateStopped, Message: "not initialized"}, nil
	}

	if _, err := os.Stat(b.workDir); os.IsNotExist(err) {
		return Status{State: StateStopped, Message: "directory does not exist"}, nil
	} else if err != nil {
		return Status{State: StateError, Message: err.Error()}, nil
	}

	return Status{State: StateRunning}, nil
}

// Teardown removes the working directory.
func (b *LocalBackend) Teardown(ctx context.Context) error {
	if b.workDir == "" {
		return nil
	}
	return os.RemoveAll(b.workDir)
}

// resolvePath resolves a relative path within the working directory,
// ensuring it doesn't escape the working directory (path traversal protection).
func (b *LocalBackend) resolvePath(path string) (string, error) {
	// Evaluate symlinks in workDir first
	root, err := filepath.EvalSymlinks(b.workDir)
	if err != nil {
		return "", fmt.Errorf("invalid working directory: %w", err)
	}

	// Clean the path and join with root
	// filepath.Clean("/"+path) normalizes path traversal attempts
	cleanPath := filepath.Clean("/" + path)
	absPath := filepath.Join(root, cleanPath)

	// After joining, verify the result is still within root.
	// We need to check that absPath starts with root (accounting for the separator).
	// filepath.Join with a cleaned path starting with "/" may still escape
	// if path contains enough ".." sequences after the initial "/".
	// Example: filepath.Join("/root", filepath.Clean("/../../etc")) = "/etc"
	if !strings.HasPrefix(absPath, root+string(filepath.Separator)) && absPath != root {
		return "", fmt.Errorf("path escapes working directory: %s", path)
	}

	return absPath, nil
}

// Ensure LocalBackend implements Backend
var _ Backend = (*LocalBackend)(nil)
