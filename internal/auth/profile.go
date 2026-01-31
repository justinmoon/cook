package auth

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/justinmoon/cook/internal/db"
	"github.com/nbd-wtf/go-nostr"
)

const (
	// ProfileCacheDuration is how long to cache profiles
	ProfileCacheDuration = 24 * time.Hour
	// ProfileFetchTimeout is how long to wait for relay responses
	ProfileFetchTimeout = 5 * time.Second
)

// Default relays to query for profiles
var DefaultRelays = []string{
	"wss://relay.damus.io",
	"wss://nos.lol",
	"wss://relay.nostr.band",
}

type Profile struct {
	Pubkey    string
	Name      string
	Picture   string
	FetchedAt time.Time
}

// DisplayName returns the best display name for a profile
func (p *Profile) DisplayName() string {
	if p != nil && p.Name != "" {
		return p.Name
	}
	return ""
}

type ProfileStore struct {
	db *db.DB
}

func NewProfileStore(database *db.DB) *ProfileStore {
	return &ProfileStore{db: database}
}

// Get returns a cached profile if fresh enough, otherwise nil
func (s *ProfileStore) Get(pubkey string) (*Profile, error) {
	row := s.db.QueryRow(`
		SELECT pubkey, name, picture, fetched_at
		FROM profiles WHERE pubkey = $1
	`, pubkey)

	var profile Profile
	var fetchedAt int64
	var name, picture sql.NullString

	err := row.Scan(&profile.Pubkey, &name, &picture, &fetchedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	profile.FetchedAt = time.Unix(fetchedAt, 0)
	if name.Valid {
		profile.Name = name.String
	}
	if picture.Valid {
		profile.Picture = picture.String
	}

	// Check if cache is stale
	if time.Since(profile.FetchedAt) > ProfileCacheDuration {
		return nil, nil
	}

	return &profile, nil
}

// Save saves a profile to the cache
func (s *ProfileStore) Save(profile *Profile) error {
	_, err := s.db.Exec(`
		INSERT INTO profiles (pubkey, name, picture, fetched_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (pubkey) DO UPDATE
		SET name = EXCLUDED.name,
			picture = EXCLUDED.picture,
			fetched_at = EXCLUDED.fetched_at
	`, profile.Pubkey, profile.Name, profile.Picture, profile.FetchedAt.Unix())
	return err
}

// FetchAndCache fetches a profile from relays and caches it
func (s *ProfileStore) FetchAndCache(pubkey string, relays []string) (*Profile, error) {
	if len(relays) == 0 {
		relays = DefaultRelays
	}

	ctx, cancel := context.WithTimeout(context.Background(), ProfileFetchTimeout)
	defer cancel()

	// Query relays for kind 0 (metadata) events
	filter := nostr.Filter{
		Authors: []string{pubkey},
		Kinds:   []int{0}, // Metadata
		Limit:   1,
	}

	var latestEvent *nostr.Event

	for _, relayURL := range relays {
		relay, err := nostr.RelayConnect(ctx, relayURL)
		if err != nil {
			continue
		}

		sub, err := relay.Subscribe(ctx, []nostr.Filter{filter})
		if err != nil {
			relay.Close()
			continue
		}

		// Get events
		for {
			select {
			case event := <-sub.Events:
				if event != nil {
					if latestEvent == nil || event.CreatedAt > latestEvent.CreatedAt {
						latestEvent = event
					}
				}
			case <-sub.EndOfStoredEvents:
				goto done
			case <-ctx.Done():
				goto done
			}
		}
	done:
		relay.Close()

		if latestEvent != nil {
			break
		}
	}

	profile := &Profile{
		Pubkey:    pubkey,
		FetchedAt: time.Now(),
	}

	if latestEvent != nil {
		// Parse metadata JSON
		var metadata struct {
			Name        string `json:"name"`
			DisplayName string `json:"display_name"`
			Picture     string `json:"picture"`
		}
		if err := json.Unmarshal([]byte(latestEvent.Content), &metadata); err == nil {
			if metadata.DisplayName != "" {
				profile.Name = metadata.DisplayName
			} else {
				profile.Name = metadata.Name
			}
			profile.Picture = metadata.Picture
		}
	}

	// Cache the profile (even if empty, to avoid repeated failed lookups)
	if err := s.Save(profile); err != nil {
		return profile, err
	}

	return profile, nil
}

// GetOrFetch returns a cached profile or fetches from relays
func (s *ProfileStore) GetOrFetch(pubkey string, relays []string) (*Profile, error) {
	// Try cache first
	profile, err := s.Get(pubkey)
	if err != nil {
		return nil, err
	}
	if profile != nil {
		return profile, nil
	}

	// Fetch from relays
	return s.FetchAndCache(pubkey, relays)
}

// DisplayNameForPubkey returns a display name for a pubkey
// Falls back to short pubkey if no profile name is available
func (s *ProfileStore) DisplayNameForPubkey(pubkey string) string {
	profile, _ := s.Get(pubkey)
	if profile != nil && profile.Name != "" {
		return profile.Name
	}
	return ShortPubkey(pubkey)
}
