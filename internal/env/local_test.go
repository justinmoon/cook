package env

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalBackendFromPath(t *testing.T) {
	// Create a temp directory
	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backend := NewLocalBackendFromPath(tmpDir)

	if backend.WorkDir() != tmpDir {
		t.Errorf("WorkDir() = %q, want %q", backend.WorkDir(), tmpDir)
	}
}

func TestLocalBackendStatus(t *testing.T) {
	ctx := context.Background()

	t.Run("not initialized", func(t *testing.T) {
		backend := &LocalBackend{}
		status, err := backend.Status(ctx)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.State != StateStopped {
			t.Errorf("State = %v, want %v", status.State, StateStopped)
		}
	})

	t.Run("directory exists", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "cook-test-*")
		defer os.RemoveAll(tmpDir)

		backend := NewLocalBackendFromPath(tmpDir)
		status, err := backend.Status(ctx)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.State != StateRunning {
			t.Errorf("State = %v, want %v", status.State, StateRunning)
		}
	})

	t.Run("directory does not exist", func(t *testing.T) {
		backend := NewLocalBackendFromPath("/nonexistent/path/xyz")
		status, err := backend.Status(ctx)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if status.State != StateStopped {
			t.Errorf("State = %v, want %v", status.State, StateStopped)
		}
	})
}

func TestLocalBackendExec(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backend := NewLocalBackendFromPath(tmpDir)

	t.Run("simple command", func(t *testing.T) {
		output, err := backend.Exec(ctx, "echo hello")
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		if got := strings.TrimSpace(string(output)); got != "hello" {
			t.Errorf("Exec() output = %q, want %q", got, "hello")
		}
	})

	t.Run("pwd is workdir", func(t *testing.T) {
		output, err := backend.Exec(ctx, "pwd")
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		// Resolve symlinks for comparison (macOS /var -> /private/var)
		expectedDir, _ := filepath.EvalSymlinks(tmpDir)
		gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(output)))
		if gotDir != expectedDir {
			t.Errorf("Exec(pwd) = %q, want %q", gotDir, expectedDir)
		}
	})

	t.Run("not initialized", func(t *testing.T) {
		backend := &LocalBackend{}
		_, err := backend.Exec(ctx, "echo hello")
		if err == nil {
			t.Error("Exec() expected error for uninitialized backend")
		}
	})
}

func TestLocalBackendCommand(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backend := NewLocalBackendFromPath(tmpDir)

	t.Run("command creation", func(t *testing.T) {
		cmd, err := backend.Command(ctx, "echo", "hello")
		if err != nil {
			t.Fatalf("Command() error = %v", err)
		}
		if cmd.Dir != tmpDir {
			t.Errorf("cmd.Dir = %q, want %q", cmd.Dir, tmpDir)
		}
		if cmd.Path == "" {
			t.Error("cmd.Path is empty")
		}
	})

	t.Run("not initialized", func(t *testing.T) {
		backend := &LocalBackend{}
		_, err := backend.Command(ctx, "echo", "hello")
		if err == nil {
			t.Error("Command() expected error for uninitialized backend")
		}
	})
}

func TestLocalBackendReadWriteFile(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backend := NewLocalBackendFromPath(tmpDir)

	t.Run("write and read", func(t *testing.T) {
		content := []byte("hello world")
		if err := backend.WriteFile(ctx, "test.txt", content); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		got, err := backend.ReadFile(ctx, "test.txt")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("ReadFile() = %q, want %q", got, content)
		}
	})

	t.Run("write with nested dir", func(t *testing.T) {
		content := []byte("nested content")
		if err := backend.WriteFile(ctx, "subdir/deep/test.txt", content); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		got, err := backend.ReadFile(ctx, "subdir/deep/test.txt")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("ReadFile() = %q, want %q", got, content)
		}
	})

	t.Run("read nonexistent", func(t *testing.T) {
		_, err := backend.ReadFile(ctx, "nonexistent.txt")
		if err == nil {
			t.Error("ReadFile() expected error for nonexistent file")
		}
	})
}

func TestLocalBackendPathTraversal(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backend := NewLocalBackendFromPath(tmpDir)

	t.Run("path traversal sandboxed", func(t *testing.T) {
		// Path traversal attempts should be sandboxed to workdir, not escape
		// Writing to "../../../tmp/evil.txt" should write to workdir/tmp/evil.txt
		content := []byte("test")
		err := backend.WriteFile(ctx, "../../../tmp/evil.txt", content)
		if err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		// Verify the file was created inside the workdir
		expectedPath := filepath.Join(tmpDir, "tmp", "evil.txt")
		if _, err := os.Stat(expectedPath); err != nil {
			t.Errorf("file should exist at %s, got error: %v", expectedPath, err)
		}

		// Verify no file was created outside workdir
		if _, err := os.Stat("/tmp/evil.txt"); err == nil {
			os.Remove("/tmp/evil.txt") // Clean up
			t.Error("file was created outside workdir at /tmp/evil.txt")
		}
	})

	t.Run("absolute path converted", func(t *testing.T) {
		// Absolute paths should be treated as relative to workdir
		content := []byte("test content")
		if err := backend.WriteFile(ctx, "/test.txt", content); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}

		// Should be readable as relative path
		got, err := backend.ReadFile(ctx, "test.txt")
		if err != nil {
			t.Fatalf("ReadFile() error = %v", err)
		}
		if string(got) != string(content) {
			t.Errorf("ReadFile() = %q, want %q", got, content)
		}
	})
}

func TestLocalBackendListFiles(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create some files and dirs
	os.WriteFile(filepath.Join(tmpDir, "file1.txt"), []byte("content1"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "file2.go"), []byte("content2"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "subdir", "nested.txt"), []byte("nested"), 0644)

	backend := NewLocalBackendFromPath(tmpDir)

	t.Run("list root", func(t *testing.T) {
		files, err := backend.ListFiles(ctx, "")
		if err != nil {
			t.Fatalf("ListFiles() error = %v", err)
		}
		if len(files) != 3 {
			t.Errorf("ListFiles() returned %d files, want 3", len(files))
		}

		// Check that we have expected files
		names := make(map[string]bool)
		for _, f := range files {
			names[f.Name] = true
		}
		if !names["file1.txt"] || !names["file2.go"] || !names["subdir"] {
			t.Errorf("missing expected files, got: %v", names)
		}
	})

	t.Run("list subdir", func(t *testing.T) {
		files, err := backend.ListFiles(ctx, "subdir")
		if err != nil {
			t.Fatalf("ListFiles() error = %v", err)
		}
		if len(files) != 1 {
			t.Errorf("ListFiles() returned %d files, want 1", len(files))
		}
		if files[0].Name != "nested.txt" {
			t.Errorf("ListFiles() got %q, want nested.txt", files[0].Name)
		}
		if files[0].Path != "subdir/nested.txt" {
			t.Errorf("ListFiles() path = %q, want subdir/nested.txt", files[0].Path)
		}
	})

	t.Run("list nonexistent", func(t *testing.T) {
		_, err := backend.ListFiles(ctx, "nonexistent")
		if err == nil {
			t.Error("ListFiles() expected error for nonexistent directory")
		}
	})
}

func TestLocalBackendTeardown(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	// Don't defer remove - Teardown should do it

	backend := NewLocalBackendFromPath(tmpDir)

	// Create a file
	os.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("content"), 0644)

	if err := backend.Teardown(ctx); err != nil {
		t.Fatalf("Teardown() error = %v", err)
	}

	// Verify directory is gone
	if _, err := os.Stat(tmpDir); !os.IsNotExist(err) {
		t.Errorf("directory still exists after Teardown")
		os.RemoveAll(tmpDir) // Clean up
	}
}

func TestLocalBackendSetup(t *testing.T) {
	ctx := context.Background()

	t.Run("existing workdir skips setup", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "cook-test-*")
		defer os.RemoveAll(tmpDir)

		backend := NewLocalBackendFromPath(tmpDir)
		if err := backend.Setup(ctx); err != nil {
			t.Fatalf("Setup() error = %v", err)
		}
		// Should succeed without doing anything
	})

	t.Run("missing repo URL", func(t *testing.T) {
		backend := NewLocalBackend(Config{
			WorkDir: "/tmp/nonexistent-xyz",
		})
		err := backend.Setup(ctx)
		if err == nil {
			t.Error("Setup() expected error without RepoURL")
		}
	})
}
