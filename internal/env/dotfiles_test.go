package env

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalBackend_IsolatedHome(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	backend := NewLocalBackendFromPath(tmpDir)

	t.Run("HomeDir returns .home inside workdir", func(t *testing.T) {
		expected := filepath.Join(tmpDir, ".home")
		if backend.HomeDir() != expected {
			t.Errorf("HomeDir() = %q, want %q", backend.HomeDir(), expected)
		}
	})

	t.Run("SetupHome creates home directory", func(t *testing.T) {
		if err := backend.SetupHome(ctx); err != nil {
			t.Fatalf("SetupHome() error = %v", err)
		}

		if _, err := os.Stat(backend.HomeDir()); err != nil {
			t.Errorf("home directory should exist: %v", err)
		}
	})

	t.Run("Exec uses isolated HOME", func(t *testing.T) {
		output, err := backend.Exec(ctx, "echo $HOME")
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}

		got := strings.TrimSpace(string(output))
		if got != backend.HomeDir() {
			t.Errorf("Exec(echo $HOME) = %q, want %q", got, backend.HomeDir())
		}
	})

	t.Run("Command uses isolated HOME", func(t *testing.T) {
		cmd, err := backend.Command(ctx, "sh", "-c", "echo $HOME")
		if err != nil {
			t.Fatalf("Command() error = %v", err)
		}

		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("cmd.CombinedOutput() error = %v", err)
		}

		got := strings.TrimSpace(string(output))
		if got != backend.HomeDir() {
			t.Errorf("Command output = %q, want %q", got, backend.HomeDir())
		}
	})
}

func TestLocalBackend_DotfilesSymlink(t *testing.T) {
	ctx := context.Background()

	// Create temp workdir
	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a fake dotfiles directory (simulating cloned repo)
	dotfilesDir := filepath.Join(tmpDir, ".dotfiles")
	if err := os.MkdirAll(dotfilesDir, 0755); err != nil {
		t.Fatalf("failed to create dotfiles dir: %v", err)
	}

	// Create some dotfiles
	dotfiles := map[string]string{
		".bashrc":           "export FOO=bar",
		".vimrc":            "set number",
		".config/nvim/init.lua": "vim.opt.number = true",
		".gitconfig":        "[user]\n\tname = Test",
		".git/config":       "should be skipped",
		"flake.nix":         "should be skipped",
		"README.md":         "should be skipped",
	}

	for name, content := range dotfiles {
		path := filepath.Join(dotfilesDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("failed to create dir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", name, err)
		}
	}

	backend := NewLocalBackendFromPath(tmpDir)
	if err := backend.SetupHome(ctx); err != nil {
		t.Fatalf("SetupHome() error = %v", err)
	}

	// Manually call symlinkDotfiles since we're not cloning a real repo
	if err := backend.symlinkDotfiles(ctx, dotfilesDir); err != nil {
		t.Fatalf("symlinkDotfiles() error = %v", err)
	}

	homeDir := backend.HomeDir()

	t.Run("dotfiles are symlinked", func(t *testing.T) {
		shouldExist := []string{".bashrc", ".vimrc", ".gitconfig", ".config"}
		for _, name := range shouldExist {
			path := filepath.Join(homeDir, name)
			info, err := os.Lstat(path)
			if err != nil {
				t.Errorf("expected %s to exist: %v", name, err)
				continue
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("expected %s to be a symlink", name)
			}
		}
	})

	t.Run("skipped files are not symlinked", func(t *testing.T) {
		shouldNotExist := []string{".git", "flake.nix", "README.md"}
		for _, name := range shouldNotExist {
			path := filepath.Join(homeDir, name)
			if _, err := os.Lstat(path); err == nil {
				t.Errorf("expected %s to NOT be symlinked", name)
			}
		}
	})

	t.Run("symlinks point to dotfiles dir", func(t *testing.T) {
		link := filepath.Join(homeDir, ".bashrc")
		target, err := os.Readlink(link)
		if err != nil {
			t.Fatalf("Readlink() error = %v", err)
		}
		expected := filepath.Join(dotfilesDir, ".bashrc")
		if target != expected {
			t.Errorf("symlink target = %q, want %q", target, expected)
		}
	})

	t.Run("nested config directories are symlinked", func(t *testing.T) {
		// .config should be a symlink to the whole directory
		configLink := filepath.Join(homeDir, ".config")
		info, err := os.Lstat(configLink)
		if err != nil {
			t.Fatalf("expected .config to exist: %v", err)
		}
		if info.Mode()&os.ModeSymlink == 0 {
			t.Error("expected .config to be a symlink")
		}

		// Should be able to read nested file through symlink
		content, err := os.ReadFile(filepath.Join(homeDir, ".config", "nvim", "init.lua"))
		if err != nil {
			t.Fatalf("failed to read nested config: %v", err)
		}
		if string(content) != "vim.opt.number = true" {
			t.Errorf("unexpected content: %s", content)
		}
	})
}

func TestLocalBackend_ExecWithDotfiles(t *testing.T) {
	ctx := context.Background()

	tmpDir, err := os.MkdirTemp("", "cook-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create dotfiles with a .bashrc that sets a variable
	dotfilesDir := filepath.Join(tmpDir, ".dotfiles")
	os.MkdirAll(dotfilesDir, 0755)
	os.WriteFile(filepath.Join(dotfilesDir, ".test_marker"), []byte("dotfiles_loaded"), 0644)

	backend := NewLocalBackendFromPath(tmpDir)
	backend.SetupHome(ctx)
	backend.symlinkDotfiles(ctx, dotfilesDir)

	t.Run("can read dotfile via HOME", func(t *testing.T) {
		// Use bash to read the file from $HOME
		output, err := backend.Exec(ctx, "cat $HOME/.test_marker")
		if err != nil {
			t.Fatalf("Exec() error = %v", err)
		}

		if string(output) != "dotfiles_loaded" {
			t.Errorf("unexpected content: %s", output)
		}
	})
}
