package server

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/justinmoon/cook/internal/branch"
	"github.com/justinmoon/cook/internal/config"
	"github.com/justinmoon/cook/internal/db"
	"github.com/justinmoon/cook/internal/gate"
	"github.com/justinmoon/cook/internal/repo"
	"github.com/justinmoon/cook/internal/task"
)

// TestGUIWorkflow tests the complete GUI workflow:
// template repo → task → branch → gate → merge
func TestGUIWorkflow(t *testing.T) {
	// Create temp directory for test data
	tmpDir, err := os.MkdirTemp("", "cook-workflow-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Initialize database
	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	// Create stores
	owner := "testuser123"
	repoName := "test-repo"
	repoRef := owner + "/" + repoName

	repoStore := repo.NewStore(tmpDir)
	taskStore := task.NewStore(database)
	branchStore := branch.NewStore(database, tmpDir)
	gateStore := gate.NewStore(database, tmpDir)

	// Step 1: Create repo with template commit
	t.Log("Step 1: Creating repo with template commit")
	rp, err := repoStore.Create(owner, repoName)
	if err != nil {
		t.Fatalf("failed to create repo: %v", err)
	}

	// Verify master exists
	cmd := exec.Command("git", "-C", rp.Path, "rev-parse", "master")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("master branch does not exist: %s: %v", string(output), err)
	}

	// Verify template files exist (check by cloning)
	verifyTmpDir, err := os.MkdirTemp("", "cook-verify-*")
	if err != nil {
		t.Fatalf("failed to create verify temp dir: %v", err)
	}
	defer os.RemoveAll(verifyTmpDir)

	cmd = exec.Command("git", "clone", rp.Path, verifyTmpDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to clone for verification: %s: %v", string(output), err)
	}

	if _, err := os.Stat(filepath.Join(verifyTmpDir, "README.md")); os.IsNotExist(err) {
		t.Fatal("README.md not found in template")
	}
	if _, err := os.Stat(filepath.Join(verifyTmpDir, "cook.toml")); os.IsNotExist(err) {
		t.Fatal("cook.toml not found in template")
	}
	t.Log("Step 1: PASSED - Repo created with template commit")

	// Step 2: Create task
	t.Log("Step 2: Creating task")
	tk := &task.Task{
		Repo:  repoRef,
		Slug:  "fix-bug",
		Title: "Fix the bug",
		Body:  "There is a bug that needs fixing",
	}
	if err := taskStore.Create(tk); err != nil {
		t.Fatalf("failed to create task: %v", err)
	}

	// Verify task exists
	createdTask, err := taskStore.Get(repoRef, "fix-bug")
	if err != nil || createdTask == nil {
		t.Fatalf("failed to get created task: %v", err)
	}
	if createdTask.Status != task.StatusOpen {
		t.Fatalf("expected task status 'open', got '%s'", createdTask.Status)
	}
	t.Log("Step 2: PASSED - Task created")

	// Step 3: Create branch with checkout
	t.Log("Step 3: Creating branch with checkout")
	b := &branch.Branch{
		Repo:     repoRef,
		Name:     "fix-bug",
		TaskRepo: &repoRef,
		TaskSlug: &tk.Slug,
	}
	if err := branchStore.CreateWithCheckout(b, rp.Path, ""); err != nil {
		t.Fatalf("failed to create branch with checkout: %v", err)
	}

	// Verify checkout exists
	if _, err := os.Stat(b.Environment.Path); os.IsNotExist(err) {
		t.Fatal("checkout path does not exist")
	}

	// Verify cook.toml exists in checkout
	if _, err := os.Stat(filepath.Join(b.Environment.Path, "cook.toml")); os.IsNotExist(err) {
		t.Fatal("cook.toml not found in checkout")
	}

	// Update task status to in_progress (as the handler would)
	taskStore.UpdateStatus(repoRef, tk.Slug, task.StatusInProgress)
	t.Log("Step 3: PASSED - Branch with checkout created")

	// Step 4: Make a change and commit
	t.Log("Step 4: Making a change")
	testFile := filepath.Join(b.Environment.Path, "FIXED.txt")
	if err := os.WriteFile(testFile, []byte("Bug is fixed!\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cmd = exec.Command("git", "-C", b.Environment.Path, "add", "FIXED.txt")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add failed: %s: %v", string(output), err)
	}

	cmd = exec.Command("git", "-C", b.Environment.Path, "commit", "-m", "Fixed the bug", "--author", "Test <test@test.com>")
	cmd.Env = append(os.Environ(), "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@test.com")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit failed: %s: %v", string(output), err)
	}
	t.Log("Step 4: PASSED - Change committed")

	// Step 5: Run gates
	t.Log("Step 5: Running gates")
	cfg, err := gate.LoadRepoConfig(b.Environment.Path)
	if err != nil {
		t.Fatalf("failed to load gate config: %v", err)
	}

	if len(cfg.Gates) == 0 {
		t.Fatal("no gates configured")
	}

	// Get current HEAD for gate run
	cmd = exec.Command("git", "-C", b.Environment.Path, "rev-parse", "HEAD")
	headOutput, err := cmd.Output()
	if err != nil {
		t.Fatalf("failed to get HEAD: %v", err)
	}
	headRev := string(headOutput[:len(headOutput)-1])

	// Run the gate
	run, err := gateStore.RunGate(cfg.Gates[0], repoRef, b.Name, headRev, b.Environment.Path)
	if err != nil {
		t.Fatalf("failed to run gate: %v", err)
	}

	if run.Status != gate.StatusPassed {
		t.Fatalf("expected gate status 'passed', got '%s'", run.Status)
	}
	t.Log("Step 5: PASSED - Gate run succeeded")

	// Step 6: Merge (fast-forward)
	t.Log("Step 6: Merging branch")
	// Get master HEAD before merge
	masterBefore, _ := getBareRepoHead(rp.Path, "master")

	// Verify base rev matches (fast-forward possible)
	if b.BaseRev != masterBefore {
		t.Fatalf("base rev mismatch: branch base %s, master %s", b.BaseRev, masterBefore)
	}

	// Perform merge
	if err := mergeBranchFastForward(rp.Path, b); err != nil {
		t.Fatalf("merge failed: %v", err)
	}

	// Verify master updated
	masterAfter, _ := getBareRepoHead(rp.Path, "master")
	if masterAfter == masterBefore {
		t.Fatal("master did not change after merge")
	}
	if masterAfter != headRev {
		t.Fatalf("master should point to branch HEAD; expected %s, got %s", headRev, masterAfter)
	}

	// Update branch status
	branchStore.UpdateStatus(repoRef, b.Name, branch.StatusMerged)

	// Close task
	taskStore.UpdateStatus(repoRef, tk.Slug, task.StatusClosed)

	// Verify task closed
	closedTask, _ := taskStore.Get(repoRef, tk.Slug)
	if closedTask.Status != task.StatusClosed {
		t.Fatalf("expected task status 'closed', got '%s'", closedTask.Status)
	}
	t.Log("Step 6: PASSED - Branch merged, task closed")

	// Step 7: Cleanup checkout
	t.Log("Step 7: Cleaning up checkout")
	if err := branchStore.RemoveCheckout(b); err != nil {
		t.Fatalf("failed to remove checkout: %v", err)
	}

	// Verify checkout removed
	if _, err := os.Stat(b.Environment.Path); !os.IsNotExist(err) {
		t.Fatal("checkout path should not exist after removal")
	}
	t.Log("Step 7: PASSED - Checkout cleaned up")

	// Final verification: clone and check that the fix is in master
	t.Log("Final verification: checking merged changes")
	finalVerifyDir, err := os.MkdirTemp("", "cook-final-verify-*")
	if err != nil {
		t.Fatalf("failed to create final verify temp dir: %v", err)
	}
	defer os.RemoveAll(finalVerifyDir)

	cmd = exec.Command("git", "clone", rp.Path, finalVerifyDir)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to clone for final verification: %s: %v", string(output), err)
	}

	if _, err := os.Stat(filepath.Join(finalVerifyDir, "FIXED.txt")); os.IsNotExist(err) {
		t.Fatal("FIXED.txt not found in master after merge")
	}

	t.Log("=== GUI Workflow Test PASSED ===")
}

// TestServer is a minimal test to ensure the server can be created
func TestServerNew(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "cook-server-test-*")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	database, err := db.Open(tmpDir)
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	cfg := &config.Config{
		Server: config.ServerConfig{
			Host:    "localhost",
			Port:    8080,
			DataDir: tmpDir,
		},
	}

	srv, err := New(cfg, database)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}

	if srv == nil {
		t.Fatal("server should not be nil")
	}
}
