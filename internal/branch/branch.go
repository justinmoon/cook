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
	cfg := env.Config{
		WorkDir:  b.Environment.Path,
		Dotfiles: b.Environment.Dotfiles,
	}
	return env.NewBackend(env.Type(b.Environment.Backend), cfg)
}

type EnvironmentSpec struct {
	Backend  string `json:"backend"`  // "local" (future: "docker", "modal")
	Path     string `json:"path"`     // checkout path
	Image    string `json:"image"`    // reserved for future docker support
	Dotfiles string `json:"dotfiles"` // git URL for dotfiles repo (optional)
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

	result, err := s.db.Exec(`
		INSERT INTO branches (repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, b.Repo, b.Name, b.TaskRepo, b.TaskSlug, b.BaseRev, b.HeadRev, string(envJSON), b.Status)
	if err != nil {
		return err
	}

	id, _ := result.LastInsertId()
	b.ID = id
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

// RemoveCheckout removes the checkout directory for a branch
func (s *Store) RemoveCheckout(b *Branch) error {
	if b.Environment.Path == "" {
		return nil
	}

	// Just remove the directory
	return os.RemoveAll(b.Environment.Path)
}



func (s *Store) Get(repo, name string) (*Branch, error) {
	row := s.db.QueryRow(`
		SELECT id, repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status, created_at, merged_at
		FROM branches WHERE repo = ? AND name = ?
	`, repo, name)

	return scanBranch(row)
}

func (s *Store) List(repo, status string) ([]Branch, error) {
	query := `SELECT id, repo, name, task_repo, task_slug, base_rev, head_rev, environment_json, status, created_at, merged_at FROM branches WHERE 1=1`
	args := []interface{}{}

	if repo != "" {
		query += " AND repo = ?"
		args = append(args, repo)
	}
	if status != "" {
		query += " AND status = ?"
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
		UPDATE branches SET status = ?, merged_at = ? WHERE repo = ? AND name = ?
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
		UPDATE branches SET head_rev = ? WHERE repo = ? AND name = ?
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
		UPDATE branches SET base_rev = ? WHERE repo = ? AND name = ?
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
	result, err := s.db.Exec(`DELETE FROM branches WHERE repo = ? AND name = ?`, repo, name)
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
