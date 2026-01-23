package auth

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

const (
	// ChallengeMaxAge is how long challenges are valid
	ChallengeMaxAge = 5 * time.Minute
	// ChallengeBytes is the size of challenge tokens
	ChallengeBytes = 32
	// MaxChallengesPerIP limits challenges per IP per minute
	MaxChallengesPerIP = 10
	// MaxTotalChallenges is the global challenge limit
	MaxTotalChallenges = 1000
)

type challengeEntry struct {
	issuedAt time.Time
	ip       string
}

// ChallengeStore manages auth challenges
type ChallengeStore struct {
	mu         sync.RWMutex
	challenges map[string]*challengeEntry
}

// NewChallengeStore creates a new challenge store
func NewChallengeStore() *ChallengeStore {
	cs := &ChallengeStore{
		challenges: make(map[string]*challengeEntry),
	}
	// Start cleanup goroutine
	go cs.cleanupLoop()
	return cs
}

// Create generates a new challenge for the given IP
func (cs *ChallengeStore) Create(ip string) (string, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	// Rate limit per IP
	count := 0
	oneMinuteAgo := time.Now().Add(-time.Minute)
	for _, entry := range cs.challenges {
		if entry.ip == ip && entry.issuedAt.After(oneMinuteAgo) {
			count++
		}
	}
	if count >= MaxChallengesPerIP {
		return "", ErrRateLimited
	}

	// Global limit
	if len(cs.challenges) >= MaxTotalChallenges {
		return "", ErrTooManyChallenges
	}

	// Generate challenge
	challengeBytes := make([]byte, ChallengeBytes)
	if _, err := rand.Read(challengeBytes); err != nil {
		return "", err
	}
	challenge := hex.EncodeToString(challengeBytes)

	cs.challenges[challenge] = &challengeEntry{
		issuedAt: time.Now(),
		ip:       ip,
	}

	return challenge, nil
}

// Validate checks if a challenge is valid and removes it (single use)
func (cs *ChallengeStore) Validate(challenge, ip string) error {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	entry, ok := cs.challenges[challenge]
	if !ok {
		return ErrInvalidChallenge
	}

	// Remove challenge (single use)
	delete(cs.challenges, challenge)

	// Check IP matches
	if entry.ip != ip {
		return ErrIPMismatch
	}

	// Check age
	if time.Since(entry.issuedAt) > ChallengeMaxAge {
		return ErrChallengeExpired
	}

	return nil
}

func (cs *ChallengeStore) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	for range ticker.C {
		cs.cleanup()
	}
}

func (cs *ChallengeStore) cleanup() {
	cs.mu.Lock()
	defer cs.mu.Unlock()

	now := time.Now()
	for challenge, entry := range cs.challenges {
		if now.Sub(entry.issuedAt) > ChallengeMaxAge {
			delete(cs.challenges, challenge)
		}
	}
}

// Errors
type AuthError string

func (e AuthError) Error() string { return string(e) }

const (
	ErrRateLimited       AuthError = "rate limited"
	ErrTooManyChallenges AuthError = "too many pending challenges"
	ErrInvalidChallenge  AuthError = "invalid or expired challenge"
	ErrIPMismatch        AuthError = "challenge IP mismatch"
	ErrChallengeExpired  AuthError = "challenge expired"
	ErrNotWhitelisted    AuthError = "pubkey not whitelisted"
)
