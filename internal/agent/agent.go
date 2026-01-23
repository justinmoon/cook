package agent

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type AgentType string

const (
	AgentClaude   AgentType = "claude"
	AgentCodex    AgentType = "codex"
	AgentOpenCode AgentType = "opencode"
)

type SessionStatus string

const (
	StatusStarting  SessionStatus = "starting"
	StatusRunning   SessionStatus = "running"
	StatusCompleted SessionStatus = "completed"
	StatusFailed    SessionStatus = "failed"
	StatusNeedsHelp SessionStatus = "needs_help"
)

type Session struct {
	ID         int64         `json:"id"`
	BranchRepo string        `json:"branch_repo"`
	BranchName string        `json:"branch_name"`
	AgentType  AgentType     `json:"agent_type"`
	Prompt     string        `json:"prompt"`
	Status     SessionStatus `json:"status"`
	PID        *int          `json:"pid,omitempty"`
	ExitCode   *int          `json:"exit_code,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	EndedAt    *time.Time    `json:"ended_at,omitempty"`
}

// BranchFullName returns repo/name format
func (s *Session) BranchFullName() string {
	return s.BranchRepo + "/" + s.BranchName
}

type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) Create(session *Session) error {
	session.StartedAt = time.Now()
	session.Status = StatusStarting

	result, err := s.db.Exec(`
		INSERT INTO agent_sessions (branch_repo, branch_name, agent_type, prompt, status, started_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, session.BranchRepo, session.BranchName, session.AgentType, session.Prompt, session.Status, session.StartedAt)
	if err != nil {
		return err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return err
	}
	session.ID = id

	return nil
}

func (s *Store) Update(session *Session) error {
	_, err := s.db.Exec(`
		UPDATE agent_sessions 
		SET status = ?, pid = ?, exit_code = ?, ended_at = ?
		WHERE id = ?
	`, session.Status, session.PID, session.ExitCode, session.EndedAt, session.ID)
	return err
}

func (s *Store) Get(id int64) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, agent_type, prompt, status, pid, exit_code, started_at, ended_at
		FROM agent_sessions WHERE id = ?
	`, id)
	return scanSession(row)
}

func (s *Store) GetByBranch(repo, branchName string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, agent_type, prompt, status, pid, exit_code, started_at, ended_at
		FROM agent_sessions 
		WHERE branch_repo = ? AND branch_name = ? AND status IN ('starting', 'running', 'needs_help')
		ORDER BY id DESC
		LIMIT 1
	`, repo, branchName)
	return scanSession(row)
}

// GetLatest returns the most recent agent session for a branch, regardless of status.
// This is used to resume sessions after server restart.
func (s *Store) GetLatest(repo, branchName string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, agent_type, prompt, status, pid, exit_code, started_at, ended_at
		FROM agent_sessions 
		WHERE branch_repo = ? AND branch_name = ?
		ORDER BY id DESC
		LIMIT 1
	`, repo, branchName)
	return scanSession(row)
}

func (s *Store) List(repo, branchName string) ([]Session, error) {
	query := `
		SELECT id, branch_repo, branch_name, agent_type, prompt, status, pid, exit_code, started_at, ended_at
		FROM agent_sessions WHERE 1=1
	`
	args := []interface{}{}

	if repo != "" {
		query += " AND branch_repo = ?"
		args = append(args, repo)
	}
	if branchName != "" {
		query += " AND branch_name = ?"
		args = append(args, branchName)
	}

	query += " ORDER BY id DESC"

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		session, err := scanSessionRows(rows)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, *session)
	}

	return sessions, rows.Err()
}

func scanSession(row *sql.Row) (*Session, error) {
	var session Session
	var pid, exitCode sql.NullInt64
	var endedAt sql.NullTime

	err := row.Scan(
		&session.ID, &session.BranchRepo, &session.BranchName, &session.AgentType, &session.Prompt,
		&session.Status, &pid, &exitCode, &session.StartedAt, &endedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if pid.Valid {
		p := int(pid.Int64)
		session.PID = &p
	}
	if exitCode.Valid {
		c := int(exitCode.Int64)
		session.ExitCode = &c
	}
	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}

	return &session, nil
}

func scanSessionRows(rows *sql.Rows) (*Session, error) {
	var session Session
	var pid, exitCode sql.NullInt64
	var endedAt sql.NullTime

	err := rows.Scan(
		&session.ID, &session.BranchRepo, &session.BranchName, &session.AgentType, &session.Prompt,
		&session.Status, &pid, &exitCode, &session.StartedAt, &endedAt,
	)
	if err != nil {
		return nil, err
	}

	if pid.Valid {
		p := int(pid.Int64)
		session.PID = &p
	}
	if exitCode.Valid {
		c := int(exitCode.Int64)
		session.ExitCode = &c
	}
	if endedAt.Valid {
		session.EndedAt = &endedAt.Time
	}

	return &session, nil
}

// Spawn creates an agent command to run in the given checkout directory.
// Commands are wrapped in a shell to avoid macOS PTY permission issues.
func Spawn(agentType AgentType, checkoutPath, prompt string) (*exec.Cmd, error) {
	var shellCmd string

	switch agentType {
	case AgentClaude:
		// Run Claude interactively (no -p flag) so the TUI is visible
		// The prompt is passed as an argument to start the conversation
		if prompt != "" {
			escapedPrompt := strings.ReplaceAll(prompt, "'", "'\"'\"'")
			shellCmd = fmt.Sprintf("claude --dangerously-skip-permissions '%s'", escapedPrompt)
		} else {
			shellCmd = "claude --dangerously-skip-permissions"
		}

	case AgentCodex:
		if prompt != "" {
			escapedPrompt := strings.ReplaceAll(prompt, "'", "'\"'\"'")
			shellCmd = fmt.Sprintf("codex '%s'", escapedPrompt)
		} else {
			shellCmd = "codex"
		}

	case AgentOpenCode:
		shellCmd = "opencode"

	default:
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}

	// Wrap in shell to avoid macOS "operation not permitted" when creating PTY
	cmd := exec.Command("/bin/zsh", "-c", shellCmd)
	cmd.Dir = checkoutPath
	cmd.Env = append(os.Environ(),
		"COOK_BRANCH="+checkoutPath,
		"TERM=xterm-256color",
	)

	return cmd, nil
}

// SpawnResume creates an agent command that resumes a previous session.
// Uses --continue to resume the most recent session in the checkout directory.
func SpawnResume(agentType AgentType, checkoutPath string) (*exec.Cmd, error) {
	var shellCmd string

	switch agentType {
	case AgentClaude:
		shellCmd = "claude --dangerously-skip-permissions --continue"
	case AgentCodex:
		// Codex may not support resume - just start fresh
		shellCmd = "codex"
	case AgentOpenCode:
		shellCmd = "opencode"
	default:
		return nil, fmt.Errorf("unknown agent type: %s", agentType)
	}

	cmd := exec.Command("/bin/zsh", "-c", shellCmd)
	cmd.Dir = checkoutPath
	cmd.Env = append(os.Environ(),
		"COOK_BRANCH="+checkoutPath,
		"TERM=xterm-256color",
	)

	return cmd, nil
}

// IsRunning checks if a process is still running
func IsRunning(pid int) bool {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// Send signal 0 to check if process exists
	err = process.Signal(syscall.Signal(0))
	return err == nil
}

// Kill terminates an agent process
func Kill(pid int) error {
	// Kill the process group
	return syscall.Kill(-pid, syscall.SIGTERM)
}
