package terminal

import (
	"fmt"
	"os/exec"
	"sync"
)

// Manager manages terminal sessions keyed by an arbitrary string (e.g. "owner/repo/branch").
type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
}

func NewManager() *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
	}
}

// Create starts a new terminal session. Errors if a session already exists for key.
func (m *Manager) Create(key string, cmd *exec.Cmd) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[key]; exists {
		return nil, fmt.Errorf("session already exists for %s", key)
	}

	p, err := Start(cmd)
	if err != nil {
		return nil, err
	}

	sess := newSession(key, p)
	m.sessions[key] = sess
	return sess, nil
}

// Get returns the session for key, or nil.
func (m *Manager) Get(key string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[key]
}

// GetOrCreate returns the existing session for key, or creates one using cmdFactory.
// created is true if a new session was started.
// initialRows/initialCols set the PTY size at creation (0 means use default).
func (m *Manager) GetOrCreate(key string, cmdFactory func() (*exec.Cmd, error), initialRows, initialCols uint16) (sess *Session, created bool, err error) {
	m.mu.Lock()
	if existing := m.sessions[key]; existing != nil {
		m.mu.Unlock()
		return existing, false, nil
	}

	cmd, err := cmdFactory()
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	p, err := Start(cmd)
	if err != nil {
		m.mu.Unlock()
		return nil, false, err
	}
	
	// Set initial size if provided
	if initialRows > 0 && initialCols > 0 {
		p.Resize(initialRows, initialCols)
	}
	
	sess = newSession(key, p)
	m.sessions[key] = sess
	m.mu.Unlock()
	return sess, true, nil
}

// Remove removes and closes a session (if present).
func (m *Manager) Remove(key string) error {
	m.mu.Lock()
	sess, exists := m.sessions[key]
	if exists {
		delete(m.sessions, key)
	}
	m.mu.Unlock()

	if !exists {
		return nil
	}
	return sess.Close()
}

// List returns all active session keys.
func (m *Manager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	keys := make([]string, 0, len(m.sessions))
	for k := range m.sessions {
		keys = append(keys, k)
	}
	return keys
}

// CloseAll closes and removes all sessions.
func (m *Manager) CloseAll() {
	m.mu.Lock()
	sessions := m.sessions
	m.sessions = make(map[string]*Session)
	m.mu.Unlock()

	for _, sess := range sessions {
		sess.Close()
	}
}
