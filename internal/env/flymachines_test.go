package env

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/justinmoon/cook/internal/envagent"
)

func TestFlyMachinesBackend_Integration(t *testing.T) {
	if os.Getenv("FLY_API_TOKEN") == "" && os.Getenv("FLY_TOKEN") == "" {
		t.Skip("FLY_API_TOKEN not set, skipping Fly Machines integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	cfg := Config{
		Name:       "test-fly-machines",
		RepoURL:    "https://github.com/octocat/Hello-World.git",
		BranchName: "master",
	}

	backend, err := NewFlyMachinesBackend(cfg)
	if err != nil {
		t.Fatalf("Failed to create Fly Machines backend: %v", err)
	}
	defer backend.Teardown(context.Background())

	if err := backend.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	output, err := backend.Exec(ctx, "echo hello")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	if string(output) == "" {
		t.Fatalf("Exec output empty")
	}

	status, err := backend.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	if status.State == StateError {
		t.Fatalf("Status returned error state: %+v", status)
	}

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

	if cfg, ok := tailnetConfigFromEnv(t); ok {
		runTailnetAuto(t, ctx, backend.Exec, "fly-machines", cfg)
	}
}
