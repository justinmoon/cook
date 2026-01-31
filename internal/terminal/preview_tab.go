package terminal

import (
	"database/sql"
	"encoding/json"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type PreviewTab struct {
	ID           string    `json:"id"`
	BranchRepo   string    `json:"branch_repo"`
	BranchName   string    `json:"branch_name"`
	Name         string    `json:"name"`
	CurrentURL   string    `json:"current_url"`
	History      []string  `json:"history"`
	HistoryIndex int       `json:"history_index"`
	CreatedAt    time.Time `json:"created_at"`
}

type PreviewTabStore struct {
	db *db.DB
}

func NewPreviewTabStore(d *db.DB) *PreviewTabStore {
	return &PreviewTabStore{db: d}
}

func (s *PreviewTabStore) Create(tab *PreviewTab) error {
	historyJSON, _ := json.Marshal(tab.History)
	_, err := s.db.Exec(`
		INSERT INTO preview_tabs (id, branch_repo, branch_name, name, current_url, history_json, history_index)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, tab.ID, tab.BranchRepo, tab.BranchName, tab.Name, tab.CurrentURL, string(historyJSON), tab.HistoryIndex)
	return err
}

func (s *PreviewTabStore) Update(tab *PreviewTab) error {
	historyJSON, _ := json.Marshal(tab.History)
	_, err := s.db.Exec(`
		UPDATE preview_tabs 
		SET current_url = $1, history_json = $2, history_index = $3, name = $4
		WHERE id = $5
	`, tab.CurrentURL, string(historyJSON), tab.HistoryIndex, tab.Name, tab.ID)
	return err
}

func (s *PreviewTabStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM preview_tabs WHERE id = $1`, id)
	return err
}

func (s *PreviewTabStore) ListByBranch(repo, branchName string) ([]PreviewTab, error) {
	rows, err := s.db.Query(`
		SELECT id, branch_repo, branch_name, name, current_url, history_json, history_index, created_at
		FROM preview_tabs
		WHERE branch_repo = $1 AND branch_name = $2
		ORDER BY created_at ASC
	`, repo, branchName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tabs []PreviewTab
	for rows.Next() {
		var t PreviewTab
		var historyJSON string
		if err := rows.Scan(&t.ID, &t.BranchRepo, &t.BranchName, &t.Name, &t.CurrentURL, &historyJSON, &t.HistoryIndex, &t.CreatedAt); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(historyJSON), &t.History)
		if t.History == nil {
			t.History = []string{}
		}
		tabs = append(tabs, t)
	}
	return tabs, rows.Err()
}

func (s *PreviewTabStore) Get(id string) (*PreviewTab, error) {
	var t PreviewTab
	var historyJSON string
	err := s.db.QueryRow(`
		SELECT id, branch_repo, branch_name, name, current_url, history_json, history_index, created_at
		FROM preview_tabs WHERE id = $1
	`, id).Scan(&t.ID, &t.BranchRepo, &t.BranchName, &t.Name, &t.CurrentURL, &historyJSON, &t.HistoryIndex, &t.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	json.Unmarshal([]byte(historyJSON), &t.History)
	if t.History == nil {
		t.History = []string{}
	}
	return &t, nil
}
