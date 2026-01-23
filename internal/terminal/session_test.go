package terminal

import (
	"bytes"
	"os/exec"
	"testing"
	"time"
)

func TestSessionReplayAfterReconnect(t *testing.T) {
	mgr := NewManager()

	cmd := exec.Command("sh", "-c", "echo one; sleep 0.1; echo two; sleep 0.1; echo three; sleep 0.2")
	cmd.Dir = "/tmp"

	sess, err := mgr.Create("replay-session", cmd)
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}
	defer mgr.Remove("replay-session")

	// Attach briefly, then detach while the process continues writing output.
	subID, _, outCh := sess.Subscribe()
	select {
	case <-outCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for initial output")
	}
	sess.Unsubscribe(subID)

	// Wait for remaining output to be produced while nobody is subscribed.
	time.Sleep(400 * time.Millisecond)

	// Reconnect and ensure buffered output includes what happened while detached.
	_, snapshot, _ := sess.Subscribe()
	if !bytes.Contains(snapshot, []byte("two")) || !bytes.Contains(snapshot, []byte("three")) {
		t.Fatalf("expected snapshot to include output while detached; got %q", string(snapshot))
	}
}
