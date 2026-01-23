package editor

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type Tab struct {
	ID         string          `json:"id"`
	BranchRepo string          `json:"branch_repo"`
	BranchName string          `json:"branch_name"`
	Name       string          `json:"name"`
	Path       string          `json:"path"`
	ViewState  json.RawMessage `json:"view_state_json"`
	CreatedAt  time.Time       `json:"created_at"`
}

type TabStore struct {
	db *db.DB
}

func NewTabStore(d *db.DB) *TabStore {
	return &TabStore{db: d}
}

func (s *TabStore) Create(tab *Tab) error {
	viewState := tab.ViewState
	if len(viewState) == 0 {
		viewState = json.RawMessage(`{}`)
	}
	_, err := s.db.Exec(`
		INSERT INTO editor_tabs (id, branch_repo, branch_name, name, path, view_state_json)
		VALUES (?, ?, ?, ?, ?, ?)
	`, tab.ID, tab.BranchRepo, tab.BranchName, tab.Name, tab.Path, string(viewState))
	return err
}

func (s *TabStore) Update(tab *Tab) error {
	viewState := tab.ViewState
	if len(viewState) == 0 {
		viewState = json.RawMessage(`{}`)
	}
	_, err := s.db.Exec(`
		UPDATE editor_tabs
		SET name = ?, path = ?, view_state_json = ?
		WHERE id = ?
	`, tab.Name, tab.Path, string(viewState), tab.ID)
	return err
}

func (s *TabStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM editor_tabs WHERE id = ?`, id)
	return err
}

func (s *TabStore) ListByBranch(repo, branchName string) ([]Tab, error) {
	rows, err := s.db.Query(`
		SELECT id, branch_repo, branch_name, name, path, view_state_json, created_at
		FROM editor_tabs
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
		var viewStateJSON string
		if err := rows.Scan(&t.ID, &t.BranchRepo, &t.BranchName, &t.Name, &t.Path, &viewStateJSON, &t.CreatedAt); err != nil {
			return nil, err
		}
		if viewStateJSON == "" {
			t.ViewState = json.RawMessage(`{}`)
		} else {
			t.ViewState = json.RawMessage(viewStateJSON)
		}
		tabs = append(tabs, t)
	}
	return tabs, rows.Err()
}

func (s *TabStore) Get(id string) (*Tab, error) {
	var t Tab
	var viewStateJSON string
	err := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, name, path, view_state_json, created_at
		FROM editor_tabs
		WHERE id = ?
	`, id).Scan(&t.ID, &t.BranchRepo, &t.BranchName, &t.Name, &t.Path, &viewStateJSON, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if viewStateJSON == "" {
		t.ViewState = json.RawMessage(`{}`)
	} else {
		t.ViewState = json.RawMessage(viewStateJSON)
	}
	return &t, nil
}

