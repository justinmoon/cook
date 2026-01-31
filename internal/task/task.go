package task

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type Task struct {
	ID        int64     `json:"id"`
	Repo      string    `json:"repo"`
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Priority  int       `json:"priority"`
	Status    string    `json:"status"`
	DependsOn []string  `json:"depends_on"` // repo/slug references
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FullName returns repo/slug format
func (t *Task) FullName() string {
	return t.Repo + "/" + t.Slug
}

const (
	StatusOpen       = "open"
	StatusInProgress = "in_progress"
	StatusNeedsHuman = "needs_human"
	StatusClosed     = "closed"
)

// GenerateSlug creates a URL-safe slug from a title
func GenerateSlug(title string) string {
	slug := strings.ToLower(title)
	// Replace spaces with hyphens
	slug = strings.ReplaceAll(slug, " ", "-")
	// Remove non-alphanumeric characters except hyphens
	var result strings.Builder
	for _, r := range slug {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			result.WriteRune(r)
		}
	}
	slug = result.String()
	// Remove multiple consecutive hyphens
	for strings.Contains(slug, "--") {
		slug = strings.ReplaceAll(slug, "--", "-")
	}
	// Trim leading/trailing hyphens
	slug = strings.Trim(slug, "-")
	// Limit length
	if len(slug) > 50 {
		slug = slug[:50]
	}
	return slug
}

type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) Create(task *Task) error {
	// Validate slug doesn't contain /
	if strings.Contains(task.Slug, "/") {
		return fmt.Errorf("task slug cannot contain '/'")
	}

	if task.Status == "" {
		task.Status = StatusOpen
	}
	if task.Priority == 0 {
		task.Priority = 3
	}
	if task.DependsOn == nil {
		task.DependsOn = []string{}
	}

	depsJSON, err := json.Marshal(task.DependsOn)
	if err != nil {
		return err
	}

	err = s.db.QueryRow(`
		INSERT INTO tasks (repo, slug, title, body, priority, status, depends_on)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, task.Repo, task.Slug, task.Title, task.Body, task.Priority, task.Status, string(depsJSON)).Scan(&task.ID)
	if err != nil {
		return err
	}

	return nil
}

func (s *Store) Get(repo, slug string) (*Task, error) {
	row := s.db.QueryRow(`
		SELECT id, repo, slug, title, body, priority, status, depends_on, created_at, updated_at
		FROM tasks WHERE repo = $1 AND slug = $2
	`, repo, slug)

	return scanTask(row)
}

func (s *Store) List(repo, status string) ([]Task, error) {
	query := `SELECT id, repo, slug, title, body, priority, status, depends_on, created_at, updated_at FROM tasks WHERE 1=1`
	args := []interface{}{}

	if repo != "" {
		query += fmt.Sprintf(" AND repo = $%d", len(args)+1)
		args = append(args, repo)
	}
	if status != "" {
		query += fmt.Sprintf(" AND status = $%d", len(args)+1)
		args = append(args, status)
	}

	query += " ORDER BY priority DESC, created_at DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []Task
	for rows.Next() {
		task, err := scanTaskRows(rows)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, *task)
	}

	return tasks, rows.Err()
}

func (s *Store) Update(task *Task) error {
	depsJSON, err := json.Marshal(task.DependsOn)
	if err != nil {
		return err
	}

	result, err := s.db.Exec(`
		UPDATE tasks 
		SET title = $1, body = $2, priority = $3, status = $4, depends_on = $5, updated_at = NOW()
		WHERE repo = $6 AND slug = $7
	`, task.Title, task.Body, task.Priority, task.Status, string(depsJSON), task.Repo, task.Slug)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %q not found", task.FullName())
	}

	return nil
}

func (s *Store) UpdateStatus(repo, slug, status string) error {
	result, err := s.db.Exec(`
		UPDATE tasks SET status = $1, updated_at = NOW() WHERE repo = $2 AND slug = $3
	`, status, repo, slug)

	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %s/%s not found", repo, slug)
	}

	return nil
}

func (s *Store) Delete(repo, slug string) error {
	result, err := s.db.Exec(`DELETE FROM tasks WHERE repo = $1 AND slug = $2`, repo, slug)
	if err != nil {
		return err
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return fmt.Errorf("task %s/%s not found", repo, slug)
	}

	return nil
}

// IsBlocked checks if a task has unclosed dependencies
// DependsOn contains repo/slug references
func (s *Store) IsBlocked(t *Task) (bool, []string, error) {
	if len(t.DependsOn) == 0 {
		return false, nil, nil
	}

	var blockers []string
	for _, depRef := range t.DependsOn {
		// Parse repo/slug
		repo, slug := parseTaskRef(depRef)
		if repo == "" {
			// Assume same repo if no slash
			repo = t.Repo
			slug = depRef
		}

		dep, err := s.Get(repo, slug)
		if err != nil {
			return false, nil, err
		}
		if dep == nil {
			blockers = append(blockers, depRef+" (not found)")
			continue
		}
		if dep.Status != StatusClosed {
			blockers = append(blockers, dep.FullName())
		}
	}

	return len(blockers) > 0, blockers, nil
}

// parseTaskRef splits "owner/repo/slug" into (repoRef, slug).
// repoRef is "owner/repo", slug is the task slug.
// Returns ("", ref) if less than 2 slashes.
func parseTaskRef(ref string) (repoRef, slug string) {
	// Find the last slash to split repo ref from task slug
	lastSlash := -1
	for i := len(ref) - 1; i >= 0; i-- {
		if ref[i] == '/' {
			lastSlash = i
			break
		}
	}
	if lastSlash == -1 {
		return "", ref
	}
	repoRef = ref[:lastSlash]
	slug = ref[lastSlash+1:]
	// repoRef must contain at least one slash (owner/repo)
	hasSlash := false
	for _, c := range repoRef {
		if c == '/' {
			hasSlash = true
			break
		}
	}
	if !hasSlash {
		return "", ref
	}
	return repoRef, slug
}

type scanner interface {
	Scan(dest ...interface{}) error
}

func scanTask(row *sql.Row) (*Task, error) {
	var task Task
	var depsJSON string

	err := row.Scan(
		&task.ID, &task.Repo, &task.Slug, &task.Title, &task.Body,
		&task.Priority, &task.Status, &depsJSON,
		&task.CreatedAt, &task.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(depsJSON), &task.DependsOn); err != nil {
		task.DependsOn = []string{}
	}

	return &task, nil
}

func scanTaskRows(rows *sql.Rows) (*Task, error) {
	var task Task
	var depsJSON string

	err := rows.Scan(
		&task.ID, &task.Repo, &task.Slug, &task.Title, &task.Body,
		&task.Priority, &task.Status, &depsJSON,
		&task.CreatedAt, &task.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal([]byte(depsJSON), &task.DependsOn); err != nil {
		task.DependsOn = []string{}
	}

	return &task, nil
}
