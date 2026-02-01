package env

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/justinmoon/cook/internal/envagent"
)

func TestSpritesBackend_Integration(t *testing.T) {
	if os.Getenv("SPRITES_TOKEN") == "" && os.Getenv("SPRITE_TOKEN") == "" {
		t.Skip("SPRITES_TOKEN not set, skipping Sprites integration test")
	}
	if os.Getenv("SPRITES_TARBALL_URL") == "" && os.Getenv("COOK_SPRITES_TARBALL_URL") == "" {
		t.Skip("SPRITES_TARBALL_URL not set, skipping Sprites integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	cfg := Config{
		Name:       "test-sprites-backend",
		RepoURL:    "https://github.com/octocat/Hello-World.git",
		BranchName: "master",
	}

	backend, err := NewSpritesBackend(cfg)
	if err != nil {
		t.Fatalf("Failed to create Sprites backend: %v", err)
	}
	defer backend.Teardown(context.Background())

	if err := backend.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	// Test Exec
	output, err := backend.Exec(ctx, "echo hello")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if string(output) == "" {
		t.Fatalf("Exec output empty")
	}

	// Test WriteFile / ReadFile
	testPath := backend.WorkDir() + "/test.txt"
	if err := backend.WriteFile(ctx, testPath, []byte("hello from test")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	data, err := backend.ReadFile(ctx, testPath)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "hello from test" {
		t.Fatalf("ReadFile got %q", string(data))
	}

	// Test ListFiles
	files, err := backend.ListFiles(ctx, backend.WorkDir())
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("ListFiles returned no entries")
	}

	// Test Status
	status, err := backend.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.State == StateError {
		t.Fatalf("Status returned error state: %+v", status)
	}

	// Test WebSocket terminal connection
	agentAddr := backend.AgentAddr()
	if agentAddr == "" {
		t.Fatalf("AgentAddr is empty")
	}

	client, err := envagent.Dial(agentAddr)
	if err != nil {
		t.Fatalf("Failed to connect to cook-agent: %v", err)
	}
	defer client.Close()

	sessionID := "test-session-1"
	if err := client.CreateSession(sessionID, "/bin/sh", backend.WorkDir(), 24, 80); err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}

	go client.ReadLoop()
	if err := client.SendInput(sessionID, []byte("echo SPRITES_TEST_OK\n")); err != nil {
		t.Fatalf("Failed to send input: %v", err)
	}

	if cfg, ok := tailnetConfigFromEnv(t); ok {
		runTailnetUserspace(t, ctx, backend.Exec, "sprites", cfg)
	}

	if os.Getenv("SPRITES_CHECKPOINT_TEST") != "" {
		checkpointID, err := backend.Checkpoint(ctx)
		if err != nil {
			t.Fatalf("Checkpoint failed: %v", err)
		}
		if err := backend.RestoreFromCheckpoint(ctx, checkpointID); err != nil {
			t.Fatalf("Restore failed: %v", err)
		}
	}
}
