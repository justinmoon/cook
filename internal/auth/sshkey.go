package auth

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

type SSHKey struct {
	ID          int64
	Pubkey      string // nostr pubkey (owner)
	SSHPubkey   string // full SSH public key
	Fingerprint string // SHA256 fingerprint
	Name        string // user-provided label
	CreatedAt   time.Time
}

type SSHKeyStore struct {
	db      *db.DB
	dataDir string
}

func NewSSHKeyStore(database *db.DB, dataDir string) *SSHKeyStore {
	return &SSHKeyStore{db: database, dataDir: dataDir}
}

// Add adds an SSH key for a user
func (s *SSHKeyStore) Add(pubkey, sshPubkey, name string) (*SSHKey, error) {
	// Parse and validate SSH key
	sshPubkey = strings.TrimSpace(sshPubkey)
	fingerprint, err := SSHFingerprint(sshPubkey)
	if err != nil {
		return nil, fmt.Errorf("invalid SSH key: %w", err)
	}

	// Check for duplicate fingerprint
	existing, err := s.GetByFingerprint(fingerprint)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, fmt.Errorf("SSH key already registered")
	}

	var id int64
	err = s.db.QueryRow(`
		INSERT INTO ssh_keys (pubkey, ssh_pubkey, fingerprint, name)
		VALUES ($1, $2, $3, $4)
		RETURNING id
	`, pubkey, sshPubkey, fingerprint, name).Scan(&id)
	if err != nil {
		return nil, err
	}

	// Regenerate authorized_keys file
	if err := s.regenerateAuthorizedKeys(); err != nil {
		return nil, fmt.Errorf("failed to update authorized_keys: %w", err)
	}

	return &SSHKey{
		ID:          id,
		Pubkey:      pubkey,
		SSHPubkey:   sshPubkey,
		Fingerprint: fingerprint,
		Name:        name,
		CreatedAt:   time.Now(),
	}, nil
}

// List lists SSH keys for a user (or all if pubkey is empty)
func (s *SSHKeyStore) List(pubkey string) ([]SSHKey, error) {
	query := `SELECT id, pubkey, ssh_pubkey, fingerprint, name, created_at FROM ssh_keys`
	args := []interface{}{}

	if pubkey != "" {
		query += fmt.Sprintf(" WHERE pubkey = $%d", len(args)+1)
		args = append(args, pubkey)
	}
	query += ` ORDER BY created_at DESC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var keys []SSHKey
	for rows.Next() {
		var key SSHKey
		var createdAt int64
		err := rows.Scan(&key.ID, &key.Pubkey, &key.SSHPubkey, &key.Fingerprint, &key.Name, &createdAt)
		if err != nil {
			return nil, err
		}
		key.CreatedAt = time.Unix(createdAt, 0)
		keys = append(keys, key)
	}

	return keys, rows.Err()
}

// GetByFingerprint returns an SSH key by fingerprint
func (s *SSHKeyStore) GetByFingerprint(fingerprint string) (*SSHKey, error) {
	row := s.db.QueryRow(`
		SELECT id, pubkey, ssh_pubkey, fingerprint, name, created_at
		FROM ssh_keys WHERE fingerprint = $1
	`, fingerprint)

	var key SSHKey
	var createdAt int64
	err := row.Scan(&key.ID, &key.Pubkey, &key.SSHPubkey, &key.Fingerprint, &key.Name, &createdAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	key.CreatedAt = time.Unix(createdAt, 0)
	return &key, nil
}

// Remove removes an SSH key by fingerprint
func (s *SSHKeyStore) Remove(pubkey, fingerprint string) error {
	result, err := s.db.Exec(`
		DELETE FROM ssh_keys WHERE pubkey = $1 AND fingerprint = $2
	`, pubkey, fingerprint)
	if err != nil {
		return err
	}

	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("SSH key not found")
	}

	// Regenerate authorized_keys file
	return s.regenerateAuthorizedKeys()
}

// regenerateAuthorizedKeys regenerates the authorized_keys file
func (s *SSHKeyStore) regenerateAuthorizedKeys() error {
	keys, err := s.List("")
	if err != nil {
		return err
	}

	// Build authorized_keys content
	var lines []string
	for _, key := range keys {
		// Each line has: command="cook git-shell <pubkey>",options key
		line := fmt.Sprintf(
			`command="cook git-shell %s",no-port-forwarding,no-X11-forwarding,no-agent-forwarding,no-pty %s`,
			key.Pubkey,
			key.SSHPubkey,
		)
		lines = append(lines, line)
	}

	// Write to file
	sshDir := filepath.Join(s.dataDir, ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return err
	}

	authKeysPath := filepath.Join(sshDir, "authorized_keys")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}

	return os.WriteFile(authKeysPath, []byte(content), 0600)
}

// SSHFingerprint calculates the SHA256 fingerprint of an SSH public key
func SSHFingerprint(sshPubkey string) (string, error) {
	parts := strings.Fields(sshPubkey)
	if len(parts) < 2 {
		return "", fmt.Errorf("invalid SSH public key format")
	}

	// Validate key type
	keyType := parts[0]
	validTypes := []string{"ssh-rsa", "ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521"}
	valid := false
	for _, t := range validTypes {
		if keyType == t {
			valid = true
			break
		}
	}
	if !valid {
		return "", fmt.Errorf("unsupported key type: %s", keyType)
	}

	// Decode base64 key data
	keyData, err := base64.StdEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("invalid base64 in SSH key")
	}

	// Calculate SHA256 fingerprint
	hash := sha256.Sum256(keyData)
	fingerprint := base64.RawStdEncoding.EncodeToString(hash[:])

	return "SHA256:" + fingerprint, nil
}

// ReadSSHKeyFile reads an SSH public key from a file
func ReadSSHKeyFile(path string) (string, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(data)), nil
}
