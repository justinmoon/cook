package gate

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type Gate struct {
	Name    string `json:"name" toml:"name"`
	Command string `json:"command" toml:"command"`
}

type GateRun struct {
	ID         int64      `json:"id"`
	BranchRepo string     `json:"branch_repo"`
	BranchName string     `json:"branch_name"`
	GateName   string     `json:"gate_name"`
	Rev        string     `json:"rev"`
	Status     string     `json:"status"` // pending, running, passed, failed
	StartedAt  *time.Time `json:"started_at,omitempty"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	LogPath    string     `json:"log_path,omitempty"`
}

// BranchFullName returns repo/name format
func (r *GateRun) BranchFullName() string {
	return r.BranchRepo + "/" + r.BranchName
}

const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusPassed  = "passed"
	StatusFailed  = "failed"
)

type Store struct {
	db      *db.DB
	dataDir string
}

func NewStore(database *db.DB, dataDir string) *Store {
	return &Store{db: database, dataDir: dataDir}
}

func (s *Store) CreateRun(run *GateRun) error {
	err := s.db.QueryRow(`
		INSERT INTO gate_runs (branch_repo, branch_name, gate_name, rev, status, started_at, log_path)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, run.BranchRepo, run.BranchName, run.GateName, run.Rev, run.Status, run.StartedAt, run.LogPath).Scan(&run.ID)
	if err != nil {
		return err
	}

	return nil
}

func (s *Store) UpdateRun(run *GateRun) error {
	_, err := s.db.Exec(`
		UPDATE gate_runs 
		SET status = $1, finished_at = $2, exit_code = $3
		WHERE id = $4
	`, run.Status, run.FinishedAt, run.ExitCode, run.ID)
	return err
}

func (s *Store) GetLatestRun(repo, branchName, gateName string) (*GateRun, error) {
	row := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, gate_name, rev, status, started_at, finished_at, exit_code, log_path
		FROM gate_runs 
		WHERE branch_repo = $1 AND branch_name = $2 AND gate_name = $3
		ORDER BY id DESC
		LIMIT 1
	`, repo, branchName, gateName)

	return scanGateRun(row)
}

func (s *Store) ListRuns(repo, branchName string) ([]GateRun, error) {
	rows, err := s.db.Query(`
		SELECT id, branch_repo, branch_name, gate_name, rev, status, started_at, finished_at, exit_code, log_path
		FROM gate_runs 
		WHERE branch_repo = $1 AND branch_name = $2
		ORDER BY id DESC
	`, repo, branchName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var runs []GateRun
	for rows.Next() {
		run, err := scanGateRunRows(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, *run)
	}

	return runs, rows.Err()
}

// RunGate executes a command gate and returns the result
func (s *Store) RunGate(gate Gate, repo, branchName, rev, checkoutPath string) (*GateRun, error) {
	// Create log directory
	logDir := filepath.Join(s.dataDir, "logs", repo, branchName)
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create log dir: %w", err)
	}

	logPath := filepath.Join(logDir, fmt.Sprintf("%s-%s.log", gate.Name, time.Now().Format("20060102-150405")))

	now := time.Now()
	run := &GateRun{
		BranchRepo: repo,
		BranchName: branchName,
		GateName:   gate.Name,
		Rev:        rev,
		Status:     StatusRunning,
		StartedAt:  &now,
		LogPath:    logPath,
	}

	if err := s.CreateRun(run); err != nil {
		return nil, err
	}

	// Open log file
	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create log file: %w", err)
	}
	defer logFile.Close()

	// Run the command
	cmd := exec.Command("sh", "-c", gate.Command)
	cmd.Dir = checkoutPath
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Run()

	finishedAt := time.Now()
	run.FinishedAt = &finishedAt

	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			code := exitErr.ExitCode()
			run.ExitCode = &code
		}
		run.Status = StatusFailed
	} else {
		code := 0
		run.ExitCode = &code
		run.Status = StatusPassed
	}

	if err := s.UpdateRun(run); err != nil {
		return run, fmt.Errorf("failed to update run: %w", err)
	}

	return run, nil
}

func scanGateRun(row *sql.Row) (*GateRun, error) {
	var run GateRun
	var startedAt, finishedAt sql.NullTime
	var exitCode sql.NullInt64
	var logPath sql.NullString

	err := row.Scan(
		&run.ID, &run.BranchRepo, &run.BranchName, &run.GateName, &run.Rev, &run.Status,
		&startedAt, &finishedAt, &exitCode, &logPath,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	if startedAt.Valid {
		run.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		run.FinishedAt = &finishedAt.Time
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		run.ExitCode = &code
	}
	if logPath.Valid {
		run.LogPath = logPath.String
	}

	return &run, nil
}

func scanGateRunRows(rows *sql.Rows) (*GateRun, error) {
	var run GateRun
	var startedAt, finishedAt sql.NullTime
	var exitCode sql.NullInt64
	var logPath sql.NullString

	err := rows.Scan(
		&run.ID, &run.BranchRepo, &run.BranchName, &run.GateName, &run.Rev, &run.Status,
		&startedAt, &finishedAt, &exitCode, &logPath,
	)
	if err != nil {
		return nil, err
	}

	if startedAt.Valid {
		run.StartedAt = &startedAt.Time
	}
	if finishedAt.Valid {
		run.FinishedAt = &finishedAt.Time
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		run.ExitCode = &code
	}
	if logPath.Valid {
		run.LogPath = logPath.String
	}

	return &run, nil
}
