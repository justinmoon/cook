package branch

import (
	"os"
	"testing"

	"github.com/justinmoon/cook/internal/env"
)

func TestBranch_Backend(t *testing.T) {
	t.Run("local backend", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "cook-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		b := &Branch{
			Name: "test-branch",
			Repo: "owner/repo",
			Environment: EnvironmentSpec{
				Backend: "local",
				Path:    tmpDir,
			},
		}

		backend, err := b.Backend()
		if err != nil {
			t.Fatalf("Backend() error = %v", err)
		}

		if backend.WorkDir() != tmpDir {
			t.Errorf("WorkDir() = %q, want %q", backend.WorkDir(), tmpDir)
		}

		// Verify it's a LocalBackend
		_, ok := backend.(*env.LocalBackend)
		if !ok {
			t.Errorf("Backend() returned %T, want *env.LocalBackend", backend)
		}
	})

	t.Run("empty backend type defaults to local", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "cook-test-*")
		if err != nil {
			t.Fatalf("failed to create temp dir: %v", err)
		}
		defer os.RemoveAll(tmpDir)

		b := &Branch{
			Name: "test-branch",
			Repo: "owner/repo",
			Environment: EnvironmentSpec{
				Backend: "", // empty should default to local
				Path:    tmpDir,
			},
		}

		backend, err := b.Backend()
		if err != nil {
			t.Fatalf("Backend() error = %v", err)
		}

		_, ok := backend.(*env.LocalBackend)
		if !ok {
			t.Errorf("Backend() returned %T, want *env.LocalBackend", backend)
		}
	})

	t.Run("no checkout path", func(t *testing.T) {
		b := &Branch{
			Name: "test-branch",
			Repo: "owner/repo",
			Environment: EnvironmentSpec{
				Backend: "local",
				Path:    "",
			},
		}

		_, err := b.Backend()
		if err == nil {
			t.Error("Backend() expected error for empty path")
		}
	})
}
