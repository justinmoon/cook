package dotfiles

import (
	"github.com/justinmoon/cook/internal/db"
)

type Dotfiles struct {
	ID        int64  `json:"id"`
	Pubkey    string `json:"pubkey"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	CreatedAt int64  `json:"created_at"`
}

type Store struct {
	db *db.DB
}

func NewStore(database *db.DB) *Store {
	return &Store{db: database}
}

func (s *Store) Create(d *Dotfiles) error {
	err := s.db.QueryRow(`
		INSERT INTO dotfiles (pubkey, name, url)
		VALUES ($1, $2, $3)
		RETURNING id
	`, d.Pubkey, d.Name, d.URL).Scan(&d.ID)
	if err != nil {
		return err
	}
	return nil
}

func (s *Store) List(pubkey string) ([]Dotfiles, error) {
	rows, err := s.db.Query(`
		SELECT id, pubkey, name, url, created_at
		FROM dotfiles
		WHERE pubkey = $1
		ORDER BY name ASC
	`, pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var list []Dotfiles
	for rows.Next() {
		var d Dotfiles
		if err := rows.Scan(&d.ID, &d.Pubkey, &d.Name, &d.URL, &d.CreatedAt); err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

func (s *Store) Get(pubkey string, name string) (*Dotfiles, error) {
	var d Dotfiles
	err := s.db.QueryRow(`
		SELECT id, pubkey, name, url, created_at
		FROM dotfiles
		WHERE pubkey = $1 AND name = $2
	`, pubkey, name).Scan(&d.ID, &d.Pubkey, &d.Name, &d.URL, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *Store) Delete(pubkey string, name string) error {
	_, err := s.db.Exec(`DELETE FROM dotfiles WHERE pubkey = $1 AND name = $2`, pubkey, name)
	return err
}
