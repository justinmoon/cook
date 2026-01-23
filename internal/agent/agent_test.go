package agent

import (
	"os"
	"testing"
)

func TestSpawnClaude(t *testing.T) {
	cmd, err := Spawn(AgentClaude, "/tmp", "test prompt")
	if err != nil {
		t.Fatalf("Failed to spawn claude: %v", err)
	}
	
	t.Logf("Command path: %s", cmd.Path)
	t.Logf("Command args: %v", cmd.Args)
	t.Logf("Command dir: %s", cmd.Dir)
	
	if cmd.Dir != "/tmp" {
		t.Errorf("Expected dir /tmp, got %s", cmd.Dir)
	}
	
	// Check that claude is in the command
	if cmd.Path == "" {
		t.Error("Command path is empty")
	}
}

func TestSpawnCodex(t *testing.T) {
	cmd, err := Spawn(AgentCodex, "/tmp", "test prompt")
	if err != nil {
		t.Fatalf("Failed to spawn codex: %v", err)
	}
	
	t.Logf("Command path: %s", cmd.Path)
	t.Logf("Command args: %v", cmd.Args)
}

func TestClaudeExecutable(t *testing.T) {
	// Check if claude is actually available
	path, err := os.Stat("/Users/justin/.bun/bin/claude")
	if err != nil {
		t.Skipf("Claude not found at expected path: %v", err)
	}
	t.Logf("Claude found: %s, mode: %v", path.Name(), path.Mode())
}
