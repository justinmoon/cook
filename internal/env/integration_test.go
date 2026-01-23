package env

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestIntegration_BackendFileOperations tests that the Backend can perform
// file operations in a simulated branch checkout directory.
func TestIntegration_BackendFileOperations(t *testing.T) {
	ctx := context.Background()

	// Create a temp directory simulating a branch checkout
	tmpDir, err := os.MkdirTemp("", "cook-integration-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize a git repo to simulate a real checkout
	if err := initGitRepo(tmpDir); err != nil {
		t.Fatalf("failed to init git repo: %v", err)
	}

	// Create backend from path (simulating Branch.Backend())
	backend := NewLocalBackendFromPath(tmpDir)

	// Test write/read cycle
	t.Run("write and read source file", func(t *testing.T) {
		content := []byte(`package main

func main() {
	println("Hello, World!")
}
`)
		if err := backend.WriteFile(ctx, "main.go", content); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		got, err := backend.ReadFile(ctx, "main.go")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("content mismatch")
		}

		// Verify file exists on disk
		diskContent, err := os.ReadFile(filepath.Join(tmpDir, "main.go"))
		if err != nil {
			t.Fatalf("os.ReadFile() error = %v", err)
		}
		if string(diskContent) != string(content) {
			t.Errorf("disk content mismatch")
		}
	})

	// Test nested directory structure
	t.Run("create nested package structure", func(t *testing.T) {
		files := map[string]string{
			"pkg/util/helper.go": "package util\n\nfunc Helper() {}",
			"pkg/util/math.go":   "package util\n\nfunc Add(a, b int) int { return a + b }",
			"internal/config.go": "package internal\n\ntype Config struct{}",
		}

		for path, content := range files {
			if err := backend.WriteFile(ctx, path, []byte(content)); err != nil {
				t.Fatalf("WriteFile(%s) error = %v", path, err)
			}
		}

		// List pkg/util directory
		entries, err := backend.ListFiles(ctx, "pkg/util")
		if err != nil {
			t.Fatalf("ListFiles() error = %v", err)
		}
		if len(entries) != 2 {
			t.Errorf("ListFiles() returned %d entries, want 2", len(entries))
		}
	})

	// Test exec (git status in the repo)
	t.Run("exec git status", func(t *testing.T) {
		output, err := backend.Exec(ctx, "git status --short")
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		// Should show untracked files
		if len(output) == 0 {
			t.Error("Exec(git status) returned empty output")
		}
	})

	// Test status
	t.Run("status shows running", func(t *testing.T) {
		status, err := backend.Status(ctx)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.State != StateRunning {
			t.Errorf("State = %v, want %v", status.State, StateRunning)
		}
	})
}

func initGitRepo(dir string) error {
	backend := NewLocalBackendFromPath(dir)
	if _, err := backend.Exec(context.Background(), "git init"); err != nil {
		return err
	}
	if _, err := backend.Exec(context.Background(), "git config user.email test@test.com"); err != nil {
		return err
	}
	if _, err := backend.Exec(context.Background(), "git config user.name Test"); err != nil {
		return err
	}
	return nil
}
