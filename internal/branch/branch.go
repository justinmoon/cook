package branch

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinmoon/cook/internal/db"
	"github.com/justinmoon/cook/internal/env"
)

type Branch struct {
	ID          int64           `json:"id"`
	Repo        string          `json:"repo"`
	Name        string          `json:"name"`
	TaskRepo    *string         `json:"task_repo,omitempty"`
	TaskSlug    *string         `json:"task_slug,omitempty"`
	BaseRev     string          `json:"base_rev"`
	HeadRev     string          `json:"head_rev"`
	Environment EnvironmentSpec `json:"environment"`
	Status      string          `json:"status"`
	CreatedAt   time.Time       `json:"created_at"`
	MergedAt    *time.Time      `json:"merged_at,omitempty"`
}

// FullName returns repo/name format
func (b *Branch) FullName() string {
	return b.Repo + "/" + b.Name
}

// TaskFullName returns the linked task's repo/slug or empty string
func (b *Branch) TaskFullName() string {
	if b.TaskRepo != nil && b.TaskSlug != nil {
		return *b.TaskRepo + "/" + *b.TaskSlug
	}
	return ""
}

// Backend returns a Backend for this branch's environment.
// Returns an error if the branch has no environment configured.
func (b *Branch) Backend() (env.Backend, error) {
	if b.Environment.Path == "" {
		return nil, fmt.Errorf("branch has no checkout path")
	}

	// For Docker backend, reconnect using container ID
	if b.Environment.Backend == "docker" {
		if b.Environment.ContainerID == "" {
			return nil, fmt.Errorf("docker backend has no container ID")
		}
		return env.NewDockerBackendFromContainerID(b.Environment.ContainerID, b.Environment.Path)
	}

	// For Modal backend, reconnect using sandbox ID
	if b.Environment.Backend == "modal" {
		if b.Environment.SandboxID == "" {
			return nil, fmt.Errorf("modal backend has no sandbox ID")
		}
		return env.NewModalBackendFromSandbox(b.Environment.SandboxID, b.Environment.Path)
	}

	cfg := env.Config{
		WorkDir:  b.Environment.Path,
		Dotfiles: b.Environment.Dotfiles,
	}
	return env.NewBackend(env.Type(b.Environment.Backend), cfg)
}

type EnvironmentSpec struct {
	Backend     string `json:"backend"`                // "local", "docker", "modal"
	Path        string `json:"path"`                   // checkout path (host path for docker)
	Image       string `json:"image,omitempty"`        // docker image (optional)
	Dotfiles    string `json:"dotfiles,omitempty"`     // git URL for dotfiles repo (optional)
	ContainerID string `json:"container_id,omitempty"` // docker container ID
	SandboxID   string `json:"sandbox_id,omitempty"`   // modal sandbox ID
}

const (
	StatusActive    = "active"
	StatusMerged    = "merged"
	StatusAbandoned = "abandoned"
)

type Store struct {
	db      *db.DB
	dataDir string
}

func NewStore(database *db.DB, dataDir string) *Store {
	return &Store{db: database, dataDir: dataDir}
}

func (s *Store) Create(b *Branch) error {
	// Validate branch name doesn't contain /
	if strings.Contains(b.Name, "/") {
		return fmt.Errorf("branch name cannot contain '/'")
	}

	if b.Status == "" {
		b.Status = StatusActive
	}

	envJSON, err := json.Marshal(b.Environment)
	if err != nil {
		return err
	}

	err = s.db.QueryRow(`
		INSERT INTO branches (repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id
	`, b.Repo, b.Name, b.TaskRepo, b.TaskSlug, b.BaseRev, b.HeadRev, string(envJSON), b.Status).Scan(&b.ID)
	if err != nil {
		return err
	}
	return nil
}

// CreateWithCheckout creates a branch with a cloned checkout directory
func (s *Store) CreateWithCheckout(b *Branch, bareRepoPath string, dotfiles string) error {
	// Validate branch name
	if strings.Contains(b.Name, "/") {
		return fmt.Errorf("branch name cannot contain '/'")
	}

	// Get base rev from master
	baseRev, err := getHeadRev(bareRepoPath, "master")
	if err != nil {
		return fmt.Errorf("failed to get master HEAD: %w (does the repo have commits?)", err)
	}
	b.BaseRev = baseRev
	b.HeadRev = baseRev

	// Create checkout path: dataDir/checkouts/repo/branch
	checkoutPath := filepath.Join(s.dataDir, "checkouts", b.Repo, b.Name)

	// Remove if exists (clean slate)
	os.RemoveAll(checkoutPath)

	if err := os.MkdirAll(filepath.Dir(checkoutPath), 0755); err != nil {
		return fmt.Errorf("failed to create checkout parent dir: %w", err)
	}

	// Clone the bare repo
	cmd := exec.Command("git", "clone", bareRepoPath, checkoutPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	// Create and checkout the branch
	cmd = exec.Command("git", "-C", checkoutPath, "checkout", "-b", b.Name)
	if output, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(checkoutPath)
		return fmt.Errorf("git checkout -b failed: %s: %w", string(output), err)
	}

	b.Environment = EnvironmentSpec{
		Backend:  "local",
		Path:     checkoutPath,
		Dotfiles: dotfiles,
	}
	b.Status = StatusActive

	// Set up the isolated home environment (and dotfiles if specified)
	backend, err := b.Backend()
	if err != nil {
		os.RemoveAll(checkoutPath)
		return fmt.Errorf("failed to create backend: %w", err)
	}
	lb := backend.(*env.LocalBackend)
	if err := lb.SetupHome(context.Background()); err != nil {
		os.RemoveAll(checkoutPath)
		return fmt.Errorf("failed to setup home: %w", err)
	}

	// Save to DB
	if err := s.Create(b); err != nil {
		os.RemoveAll(checkoutPath)
		return err
	}

	return nil
}

// CreateWithDockerCheckout creates a branch with a Docker container environment
func (s *Store) CreateWithDockerCheckout(b *Branch, bareRepoPath string, dotfiles string) error {
	// Validate branch name
	if strings.Contains(b.Name, "/") {
		return fmt.Errorf("branch name cannot contain '/'")
	}

	// Get base rev from master
	baseRev, err := getHeadRev(bareRepoPath, "master")
	if err != nil {
		return fmt.Errorf("failed to get master HEAD: %w (does the repo have commits?)", err)
	}
	b.BaseRev = baseRev
	b.HeadRev = baseRev

	// Create checkout path on host: dataDir/checkouts/repo/branch
	checkoutPath := filepath.Join(s.dataDir, "checkouts", b.Repo, b.Name)

	// Remove if exists (clean slate)
	os.RemoveAll(checkoutPath)

	// Create Docker backend config
	containerName := strings.ReplaceAll(b.Repo, "/", "-") + "-" + b.Name
	cfg := env.Config{
		Name:       containerName,
		RepoURL:    bareRepoPath,
		BranchName: b.Name,
		WorkDir:    checkoutPath,
		Dotfiles:   dotfiles,
	}

	backend, err := env.NewDockerBackend(cfg)
	if err != nil {
		return fmt.Errorf("failed to create docker backend: %w", err)
	}

	// Setup the container (this clones repo, starts container, sets up dotfiles)
	if err := backend.Setup(context.Background()); err != nil {
		backend.Teardown(context.Background())
		return fmt.Errorf("failed to setup docker environment: %w", err)
	}

	b.Environment = EnvironmentSpec{
		Backend:     "docker",
		Path:        checkoutPath,
		Dotfiles:    dotfiles,
		ContainerID: backend.ContainerID(),
	}
	b.Status = StatusActive

	// Save to DB
	if err := s.Create(b); err != nil {
		backend.Teardown(context.Background())
		return err
	}

	return nil
}

// CreateWithModalCheckout creates a branch with a Modal sandbox environment
func (s *Store) CreateWithModalCheckout(b *Branch, bareRepoPath string, dotfiles string) error {
	// Validate branch name
	if strings.Contains(b.Name, "/") {
		return fmt.Errorf("branch name cannot contain '/'")
	}

	// Get base rev from master
	baseRev, err := getHeadRev(bareRepoPath, "master")
	if err != nil {
		return fmt.Errorf("failed to get master HEAD: %w (does the repo have commits?)", err)
	}
	b.BaseRev = baseRev
	b.HeadRev = baseRev

	// Create a "virtual" path - Modal doesn't use host filesystem
	checkoutPath := filepath.Join(s.dataDir, "checkouts", b.Repo, b.Name)

	// Create Modal backend config
	sandboxName := strings.ReplaceAll(b.Repo, "/", "-") + "-" + b.Name
	cfg := env.Config{
		Name:       sandboxName,
		RepoURL:    bareRepoPath,
		BranchName: b.Name,
		WorkDir:    checkoutPath,
		Dotfiles:   dotfiles,
	}

	backend, err := env.NewModalBackend(cfg)
	if err != nil {
		return fmt.Errorf("failed to create modal backend: %w", err)
	}

	// Setup the sandbox (this clones repo, starts cook-agent, sets up dotfiles)
	if err := backend.Setup(context.Background()); err != nil {
		backend.Teardown(context.Background())
		return fmt.Errorf("failed to setup modal environment: %w", err)
	}

	b.Environment = EnvironmentSpec{
		Backend:   "modal",
		Path:      checkoutPath,
		Dotfiles:  dotfiles,
		SandboxID: backend.SandboxID(),
	}
	b.Status = StatusActive

	// Save to DB
	if err := s.Create(b); err != nil {
		backend.Teardown(context.Background())
		return err
	}

	return nil
}

// RemoveCheckout removes the checkout directory for a branch.
// For Docker/Modal branches, this also tears down the container/sandbox.
func (s *Store) RemoveCheckout(b *Branch) error {
	if b.Environment.Path == "" {
		return nil
	}

	// For Docker backend, teardown the container first
	if b.Environment.Backend == "docker" && b.Environment.ContainerID != "" {
		backend, err := b.Backend()
		if err == nil {
			backend.Teardown(context.Background())
		}
	}

	// For Modal backend, teardown the sandbox
	if b.Environment.Backend == "modal" && b.Environment.SandboxID != "" {
		backend, err := b.Backend()
		if err == nil {
			backend.Teardown(context.Background())
		}
	}

	// Remove the directory (for local/docker) - Modal doesn't have local files
	if b.Environment.Backend != "modal" {
		return os.RemoveAll(b.Environment.Path)
	}
	return nil
}

func (s *Store) Get(repo, name string) (*Branch, error) {
	row := s.db.QueryRow(`
		SELECT id, repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status, created_at, merged_at
		FROM branches WHERE repo = $1 AND name = $2
	`, repo, name)

	return scanBranch(row)
}

func (s *Store) List(repo, status string) ([]Branch, error) {
	query := `SELECT id, repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status, created_at, merged_at FROM branches WHERE 1=1`
	args := []interface{}{}

	if repo != "" {
		query += fmt.Sprintf(" AND repo = $%d", len(args)+1)
		args = append(args, repo)
	}
	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", len(args)+1)
		args = append(args, status)
	}

	query += " ORDER BY created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var branches []Branch
	for rows.Next() {
		b, err := scanBranchRows(rows)
		if err != nil {
			return nil, err
		}
		branches = append(branches, *b)
	}

	return branches, rows.Err()
}

func (s *Store) UpdateStatus(repo, name, status string) error {
	var mergedAt interface{}
	if status == StatusMerged {
		mergedAt = time.Now()
	}

	result, err := s.db.Exec(`
		UPDATE branches SET status = $1, merged_at = $2 WHERE repo = $3 AND name = $4
	`, status, mergedAt, repo, name)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("branch %s/%s not found", repo, name)
	}

	return nil
}

func (s *Store) UpdateHeadRev(repo, name, rev string) error {
	result, err := s.db.Exec(`
		UPDATE branches SET head_rev = $1 WHERE repo = $2 AND name = $3
	`, rev, repo, name)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("branch %s/%s not found", repo, name)
	}

	return nil
}

func (s *Store) UpdateBaseRev(repo, name, rev string) error {
	result, err := s.db.Exec(`
		UPDATE branches SET base_rev = $1 WHERE repo = $2 AND name = $3
	`, rev, repo, name)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("branch %s/%s not found", repo, name)
	}

	return nil
}

func (s *Store) Delete(repo, name string) error {
	result, err := s.db.Exec(`DELETE FROM branches WHERE repo = $1 AND name = $2`, repo, name)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("branch %s/%s not found", repo, name)
	}

	return nil
}

// CreateLocalCheckout creates a local checkout of a branch
func (s *Store) CreateLocalCheckout(repoPath, branchName, checkoutPath string) error {
	// Get current HEAD of master (or create initial commit if empty)
	baseRev, err := getHeadRev(repoPath, "master")
	if err != nil {
		// Repo might be empty, that's ok for now
		baseRev = ""
	}

	// Clone from bare repo to checkout path
	if err := os.MkdirAll(filepath.Dir(checkoutPath), 0755); err != nil {
		return fmt.Errorf("failed to create checkout parent dir: %w", err)
	}

	cmd := exec.Command("git", "clone", repoPath, checkoutPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git clone failed: %s: %w", string(output), err)
	}

	// Create and checkout the branch
	cmd = exec.Command("git", "-C", checkoutPath, "checkout", "-b", branchName)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git checkout -b failed: %s: %w", string(output), err)
	}

	// If we have a base rev, the branch is already based on it from the clone
	// If not (empty repo), we need to create an initial commit
	if baseRev == "" {
		// Create an empty initial commit
		cmd = exec.Command("git", "-C", checkoutPath, "commit", "--allow-empty", "-m", "Initial commit")
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git commit failed: %s: %w", string(output), err)
		}
	}

	return nil
}

// RemoveLocalCheckout removes a local checkout directory
func (s *Store) RemoveLocalCheckout(checkoutPath string) error {
	return os.RemoveAll(checkoutPath)
}

func getHeadRev(repoPath, ref string) (string, error) {
	cmd := exec.Command("git", "-C", repoPath, "rev-parse", ref)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output[:len(output)-1]), nil // trim newline
}

func scanBranch(row *sql.Row) (*Branch, error) {
	var b Branch
	var envJSON string
	var taskRepo, taskSlug sql.NullString
	var mergedAt sql.NullTime

	err := row.Scan(
		&b.ID, &b.Repo, &b.Name, &taskRepo, &taskSlug, &b.BaseRev, &b.HeadRev,
		&envJSON, &b.Status, &b.CreatedAt, &mergedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if taskRepo.Valid {
		b.TaskRepo = &taskRepo.String
	}
	if taskSlug.Valid {
		b.TaskSlug = &taskSlug.String
	}
	if mergedAt.Valid {
		b.MergedAt = &mergedAt.Time
	}

	if err := json.Unmarshal([]byte(envJSON), &b.Environment); err != nil {
		return nil, err
	}

	return &b, nil
}

func scanBranchRows(rows *sql.Rows) (*Branch, error) {
	var b Branch
	var envJSON string
	var taskRepo, taskSlug sql.NullString
	var mergedAt sql.NullTime

	err := rows.Scan(
		&b.ID, &b.Repo, &b.Name, &taskRepo, &taskSlug, &b.BaseRev, &b.HeadRev,
		&envJSON, &b.Status, &b.CreatedAt, &mergedAt,
	)
	if err != nil {
		return nil, err
	}

	if taskRepo.Valid {
		b.TaskRepo = &taskRepo.String
	}
	if taskSlug.Valid {
		b.TaskSlug = &taskSlug.String
	}
	if mergedAt.Valid {
		b.MergedAt = &mergedAt.Time
	}

	if err := json.Unmarshal([]byte(envJSON), &b.Environment); err != nil {
		return nil, err
	}

	return &b, nil
}
