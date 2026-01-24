package auth

import (
	"testing"
	"time"

	"github.com/justinmoon/cook/internal/db"
)

// TestFetchRealProfile tests fetching a real profile from nostr relays
// Uses jb55's pubkey as a known good profile
func TestFetchRealProfile(t *testing.T) {
	// jb55's pubkey (npub1xtscya34g58tk0z605fvr788k263gsu6cy9x0mhnm87echrgufzsevkk5s)
	pubkey := "32e1827635450ebb3c5a7d12c1f8e7b2b514439ac10a67eef3d9fd9c5c68e245"

	// Create temp DB
	database, err := db.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	store := NewProfileStore(database)

	// Fetch profile
	t.Logf("Fetching profile for %s...", pubkey[:16])
	start := time.Now()
	profile, err := store.FetchAndCache(pubkey, nil)
	t.Logf("Fetch took %v", time.Since(start))

	if err != nil {
		t.Fatalf("Failed to fetch profile: %v", err)
	}

	if profile == nil {
		t.Fatal("Profile is nil")
	}

	t.Logf("Profile fetched:")
	t.Logf("  Pubkey: %s", profile.Pubkey)
	t.Logf("  Name: %s", profile.Name)
	t.Logf("  Picture: %s", profile.Picture)
	t.Logf("  FetchedAt: %v", profile.FetchedAt)

	// Verify we got some data
	if profile.Name == "" {
		t.Error("Expected profile to have a name")
	}

	// Verify cache works
	cached, err := store.Get(pubkey)
	if err != nil {
		t.Fatalf("Failed to get cached profile: %v", err)
	}
	if cached == nil {
		t.Fatal("Cached profile is nil")
	}
	if cached.Name != profile.Name {
		t.Errorf("Cached name mismatch: got %q, want %q", cached.Name, profile.Name)
	}
}

// TestDisplayName tests the DisplayName method
func TestDisplayName(t *testing.T) {
	tests := []struct {
		name    string
		profile *Profile
		want    string
	}{
		{"with name", &Profile{Name: "jb55"}, "jb55"},
		{"empty name", &Profile{Name: ""}, ""},
		{"nil profile", nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.profile.DisplayName()
			if got != tt.want {
				t.Errorf("DisplayName() = %q, want %q", got, tt.want)
			}
		})
	}
}
