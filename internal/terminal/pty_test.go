package terminal

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

func TestPTYCreateAndRetrieve(t *testing.T) {
	mgr := NewManager()

	// Create a simple command
	cmd := exec.Command("echo", "hello from PTY test")
	cmd.Dir = "/tmp"

	// Create PTY
	pty, err := mgr.Create("test-session", cmd)
	if err != nil {
		t.Fatalf("Failed to create PTY: %v", err)
	}

	if pty.PID() == 0 {
		t.Fatal("PTY has no PID")
	}
	t.Logf("Created PTY with PID %d", pty.PID())

	// Verify we can retrieve it
	pty2 := mgr.Get("test-session")
	if pty2 == nil {
		t.Fatal("Could not retrieve session")
	}

	// Wait briefly for output pump to capture echo output
	time.Sleep(150 * time.Millisecond)
	out := pty.Snapshot()
	if !bytes.Contains(out, []byte("hello from PTY test")) {
		t.Fatalf("expected output to contain %q, got %q", "hello from PTY test", string(out))
	}

	// Clean up
	mgr.Remove("test-session")

	// Verify it's gone
	if mgr.Get("test-session") != nil {
		t.Fatal("Session should be removed")
	}
}

func TestPTYWithInteractiveCommand(t *testing.T) {
	mgr := NewManager()

	// Use cat which will wait for input (simulates an agent)
	cmd := exec.Command("cat")
	cmd.Dir = "/tmp"

	pty, err := mgr.Create("interactive-session", cmd)
	if err != nil {
		t.Fatalf("Failed to create PTY: %v", err)
	}

	t.Logf("Created interactive PTY with PID %d", pty.PID())

	subID, _, outCh := pty.Subscribe()
	defer pty.Unsubscribe(subID)

	// Write to it
	_, err = pty.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Failed to write to PTY: %v", err)
	}

	// Read back (cat echoes input)
	select {
	case chunk := <-outCh:
		if !bytes.Contains(chunk, []byte("hello")) {
			t.Fatalf("expected echoed output to contain %q, got %q", "hello", string(chunk))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for PTY output")
	}

	// Clean up
	mgr.Remove("interactive-session")
}
