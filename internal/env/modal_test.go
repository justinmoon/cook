package env

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/justinmoon/cook/internal/envagent"
)

func TestModalBackend_Integration(t *testing.T) {
	// Skip if no Modal credentials
	if os.Getenv("MODAL_TOKEN_ID") == "" {
		t.Skip("MODAL_TOKEN_ID not set, skipping Modal integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cfg := Config{
		Name:       "test-modal-backend",
		RepoURL:    "https://github.com/octocat/Hello-World.git",
		BranchName: "master",
	}

	backend, err := NewModalBackend(cfg)
	if err != nil {
		t.Fatalf("Failed to create Modal backend: %v", err)
	}
	defer backend.Teardown(context.Background())

	t.Log("Setting up Modal sandbox...")
	if err := backend.Setup(ctx); err != nil {
		t.Fatalf("Setup failed: %v", err)
	}

	t.Logf("Sandbox ID: %s", backend.SandboxID())

	// Test Exec
	t.Log("Testing Exec...")
	output, err := backend.Exec(ctx, "echo hello")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}
	t.Logf("Exec output: %s", string(output))

	// Test ReadFile
	t.Log("Testing ReadFile...")
	content, err := backend.ReadFile(ctx, "/workspace/README")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	t.Logf("README content (first 100 chars): %s", string(content)[:min(100, len(content))])

	// Test ListFiles
	t.Log("Testing ListFiles...")
	files, err := backend.ListFiles(ctx, "/workspace")
	if err != nil {
		t.Fatalf("ListFiles failed: %v", err)
	}
	t.Logf("Files in workspace: %v", files)

	// Test WriteFile
	t.Log("Testing WriteFile...")
	if err := backend.WriteFile(ctx, "/workspace/test.txt", []byte("hello from test")); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	readBack, err := backend.ReadFile(ctx, "/workspace/test.txt")
	if err != nil {
		t.Fatalf("ReadFile after WriteFile failed: %v", err)
	}
	t.Logf("Read back: %s", string(readBack))

	// Test Status
	status, err := backend.Status(ctx)
	if err != nil {
		t.Fatalf("Status failed: %v", err)
	}
	t.Logf("Status: %+v", status)

	// Test WebSocket terminal connection
	t.Log("Testing WebSocket terminal connection...")
	agentAddr := backend.AgentAddr()
	t.Logf("Agent address: %s", agentAddr)

	// Connect via envagent client
	client, err := envagent.Dial(agentAddr)
	if err != nil {
		t.Fatalf("Failed to connect to cook-agent via WebSocket: %v", err)
	}
	defer client.Close()

	// Create a terminal session
	sessionID := "test-session-1"
	if err := client.CreateSession(sessionID, "/bin/sh", "/workspace", 24, 80); err != nil {
		t.Fatalf("Failed to create session: %v", err)
	}
	t.Log("Terminal session created successfully!")

	// Set up output handler
	var lastOutput []byte
	client.SetOutputHandler(func(sid string, data []byte) {
		lastOutput = append(lastOutput, data...)
	})

	// Start read loop in goroutine
	go client.ReadLoop()

	// Send a command
	if err := client.SendInput(sessionID, []byte("echo TERMINAL_TEST_OK\n")); err != nil {
		t.Fatalf("Failed to send input: %v", err)
	}

	// Wait for output
	time.Sleep(2 * time.Second)

	t.Logf("Terminal output received: %d bytes", len(lastOutput))
	if len(lastOutput) > 0 {
		t.Logf("Output: %s", string(lastOutput))
	}

	if cfg, ok := tailnetConfigFromEnv(t); ok {
		runTailnetUserspace(t, ctx, backend.Exec, "modal", cfg)
	}

	t.Log("Modal backend test passed!")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
