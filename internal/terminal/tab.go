package terminal

import (
	"database/sql"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type Tab struct {
	ID         string    `json:"id"`
	BranchRepo string    `json:"branch_repo"`
	BranchName string    `json:"branch_name"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"created_at"`
}

type TabStore struct {
	db *db.DB
}

func NewTabStore(d *db.DB) *TabStore {
	return &TabStore{db: d}
}

func (s *TabStore) Create(tab *Tab) error {
	_, err := s.db.Exec(`
		INSERT INTO terminal_tabs (id, branch_repo, branch_name, name)
		VALUES (?, ?, ?, ?)
	`, tab.ID, tab.BranchRepo, tab.BranchName, tab.Name)
	return err
}

func (s *TabStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM terminal_tabs WHERE id = ?`, id)
	return err
}

func (s *TabStore) ListByBranch(repo, branchName string) ([]Tab, error) {
	rows, err := s.db.Query(`
		SELECT id, branch_repo, branch_name, name, created_at
		FROM terminal_tabs
		WHERE branch_repo = ? AND branch_name = ?
		ORDER BY created_at ASC
	`, repo, branchName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tabs []Tab
	for rows.Next() {
		var t Tab
		if err := rows.Scan(&t.ID, &t.BranchRepo, &t.BranchName, &t.Name, &t.CreatedAt); err != nil {
			return nil, err
		}
		tabs = append(tabs, t)
	}
	return tabs, rows.Err()
}

func (s *TabStore) Get(id string) (*Tab, error) {
	var t Tab
	err := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, name, created_at
		FROM terminal_tabs WHERE id = ?
	`, id).Scan(&t.ID, &t.BranchRepo, &t.BranchName, &t.Name, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}
