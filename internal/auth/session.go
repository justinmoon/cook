package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

const (
	// SessionDuration is how long sessions are valid (1 year)
	SessionDuration = 365 * 24 * time.Hour
	// SessionTokenBytes is the size of session tokens
	SessionTokenBytes = 32
)

type Session struct {
	ID           string
	Pubkey       string
	CreatedAt    time.Time
	ExpiresAt    time.Time
	LastAccessed time.Time
}

type SessionStore struct {
	db *db.DB
}

func NewSessionStore(database *db.DB) *SessionStore {
	return &SessionStore{db: database}
}

// Create creates a new session for the given pubkey
func (s *SessionStore) Create(pubkey string) (*Session, error) {
	// Generate random session ID
	tokenBytes := make([]byte, SessionTokenBytes)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, err
	}
	sessionID := hex.EncodeToString(tokenBytes)

	now := time.Now()
	expiresAt := now.Add(SessionDuration)

	_, err := s.db.Exec(`
		INSERT INTO sessions (id, pubkey, created_at, expires_at, last_accessed)
		VALUES (?, ?, ?, ?, ?)
	`, sessionID, pubkey, now.Unix(), expiresAt.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}

	return &Session{
		ID:           sessionID,
		Pubkey:       pubkey,
		CreatedAt:    now,
		ExpiresAt:    expiresAt,
		LastAccessed: now,
	}, nil
}

// Validate validates a session and returns it if valid
func (s *SessionStore) Validate(sessionID string) (*Session, error) {
	row := s.db.QueryRow(`
		SELECT id, pubkey, created_at, expires_at, last_accessed
		FROM sessions WHERE id = ?
	`, sessionID)

	var session Session
	var createdAt, expiresAt, lastAccessed int64
	err := row.Scan(&session.ID, &session.Pubkey, &createdAt, &expiresAt, &lastAccessed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	session.CreatedAt = time.Unix(createdAt, 0)
	session.ExpiresAt = time.Unix(expiresAt, 0)
	session.LastAccessed = time.Unix(lastAccessed, 0)

	// Check expiration
	if time.Now().After(session.ExpiresAt) {
		s.Delete(sessionID)
		return nil, nil
	}

	// Update last accessed time
	now := time.Now()
	s.db.Exec(`UPDATE sessions SET last_accessed = ? WHERE id = ?`, now.Unix(), sessionID)
	session.LastAccessed = now

	return &session, nil
}

// Delete deletes a session
func (s *SessionStore) Delete(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, sessionID)
	return err
}

// DeleteByPubkey deletes all sessions for a pubkey
func (s *SessionStore) DeleteByPubkey(pubkey string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE pubkey = ?`, pubkey)
	return err
}

// CleanupExpired removes expired sessions
func (s *SessionStore) CleanupExpired() error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().Unix())
	return err
}

// ListByPubkey lists all sessions for a pubkey
func (s *SessionStore) ListByPubkey(pubkey string) ([]Session, error) {
	rows, err := s.db.Query(`
		SELECT id, pubkey, created_at, expires_at, last_accessed
		FROM sessions WHERE pubkey = ?
		ORDER BY created_at DESC
	`, pubkey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []Session
	for rows.Next() {
		var session Session
		var createdAt, expiresAt, lastAccessed int64
		err := rows.Scan(&session.ID, &session.Pubkey, &createdAt, &expiresAt, &lastAccessed)
		if err != nil {
			return nil, err
		}
		session.CreatedAt = time.Unix(createdAt, 0)
		session.ExpiresAt = time.Unix(expiresAt, 0)
		session.LastAccessed = time.Unix(lastAccessed, 0)
		sessions = append(sessions, session)
	}

	return sessions, rows.Err()
}
