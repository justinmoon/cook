// Package env provides environment backend abstractions for running code
// in different execution contexts (local, docker, modal).
package env

import (
	"context"
	"io"
	"os/exec"
)

// Backend handles the lifecycle of an execution environment.
type Backend interface {
	// Setup provisions the environment (clone repo, install tools, etc.)
	Setup(ctx context.Context) error

	// Exec runs a command and returns combined output
	Exec(ctx context.Context, cmd string) ([]byte, error)

	// Command returns an *exec.Cmd configured to run in this environment.
	// The caller is responsible for starting and managing the command.
	// This is useful for PTY integration where the terminal manager needs the Cmd.
	Command(ctx context.Context, name string, args ...string) (*exec.Cmd, error)

	// ReadFile reads a file from the environment
	ReadFile(ctx context.Context, path string) ([]byte, error)

	// WriteFile writes a file to the environment
	WriteFile(ctx context.Context, path string, content []byte) error

	// ListFiles lists files in a directory
	ListFiles(ctx context.Context, dir string) ([]FileInfo, error)

	// WorkDir returns the root working directory path
	WorkDir() string

	// Status returns backend health
	Status(ctx context.Context) (Status, error)

	// Teardown destroys the environment
	Teardown(ctx context.Context) error
}

// FileInfo represents a file or directory
type FileInfo struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
	Size  int64  `json:"size"`
}

// Status represents the health status of a backend
type Status struct {
	State   State  `json:"state"`
	Message string `json:"message,omitempty"`
	ID      string `json:"id,omitempty"` // container ID, sandbox ID, etc.
}

// State represents the current state of a backend
type State string

const (
	StateRunning  State = "running"
	StateStarting State = "starting"
	StateStopped  State = "stopped"
	StateError    State = "error"
)

// Config contains configuration for creating a backend
type Config struct {
	// Name is a unique identifier (typically repo/branch)
	Name string

	// RepoURL is the bare repo URL to clone from
	RepoURL string

	// BranchName is the git branch to checkout
	BranchName string

	// WorkDir is the working directory path (for local backend)
	WorkDir string

	// Dotfiles is an optional git URL for dotfiles repo
	Dotfiles string

	// Secrets contains agent auth, API keys, etc.
	Secrets map[string]string
}

// PTYAttacher is an optional interface for backends that support PTY attachment.
// The local backend doesn't implement this because PTY is handled by the terminal package.
// Docker and Modal backends will implement this for their specific PTY mechanisms.
type PTYAttacher interface {
	// AttachPTY returns reader/writer for interactive terminal
	AttachPTY(ctx context.Context, rows, cols int) (io.ReadWriteCloser, error)

	// ResizePTY resizes the terminal
	ResizePTY(rows, cols int) error
}

// Type represents the backend type
type Type string

const (
	TypeLocal  Type = "local"
	TypeDocker Type = "docker"
	TypeModal  Type = "modal"
)
