package server

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/justinmoon/cook/internal/agent"
	"github.com/justinmoon/cook/internal/terminal"
)

func TestAgentCreatesFile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "agent-test-*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	mgr := terminal.NewManager()

	cmd, err := agent.Spawn(agent.AgentClaude, tmpDir, "create a file called hello.txt with content 'test passed' then exit")
	if err != nil {
		t.Fatalf("Failed to spawn: %v", err)
	}
	t.Logf("Command: %v", cmd.Args)

	pty, err := mgr.Create("test-session", cmd)
	if err != nil {
		t.Fatalf("Failed to create PTY: %v", err)
	}
	defer mgr.Remove("test-session")

	pty.Resize(24, 80)
	t.Logf("Agent started with PID: %d", pty.PID())

	// Wait for hello.txt to appear
	targetFile := filepath.Join(tmpDir, "hello.txt")
	deadline := time.Now().Add(30 * time.Second)

	for time.Now().Before(deadline) {
		if _, err := os.Stat(targetFile); err == nil {
			content, _ := os.ReadFile(targetFile)
			t.Logf("SUCCESS: Agent created file with content: %q", string(content))
			return
		}

		// Check if process died
		if err := exec.Command("ps", "-p", fmt.Sprintf("%d", pty.PID())).Run(); err != nil {
			t.Log("Agent process exited")
			break
		}

		time.Sleep(1 * time.Second)
	}

	t.Fatal("Agent did not create hello.txt - agent is not working correctly")
}
