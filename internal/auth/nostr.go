package auth

import (
	"fmt"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
)

const (
	// NIP-98 HTTP Auth event kind
	KindHTTPAuth = 27235
	// Max age for auth events (5 minutes)
	MaxEventAge = 5 * time.Minute
)

// VerifyAuthEvent verifies a signed Nostr event matches the expected challenge
func VerifyAuthEvent(event *nostr.Event, challenge string) error {
	// Verify signature
	valid, err := event.CheckSignature()
	if err != nil {
		return fmt.Errorf("signature check failed: %w", err)
	}
	if !valid {
		return fmt.Errorf("invalid signature")
	}

	// Verify event kind is 27235 (NIP-98 auth event)
	if event.Kind != KindHTTPAuth {
		return fmt.Errorf("invalid event kind %d, expected %d", event.Kind, KindHTTPAuth)
	}

	// Verify content matches challenge
	if event.Content != challenge {
		return fmt.Errorf("challenge mismatch")
	}

	// Verify event is recent
	eventTime := event.CreatedAt.Time()
	age := time.Since(eventTime)
	if age > MaxEventAge {
		return fmt.Errorf("event too old: %v", age)
	}
	if age < -time.Minute {
		return fmt.Errorf("event timestamp in future")
	}

	return nil
}

// PubkeyToNpub converts hex pubkey to npub format
func PubkeyToNpub(hexPubkey string) (string, error) {
	npub, err := nip19.EncodePublicKey(hexPubkey)
	if err != nil {
		return "", fmt.Errorf("invalid pubkey: %w", err)
	}
	return npub, nil
}

// NpubToPubkey converts npub to hex pubkey
func NpubToPubkey(npub string) (string, error) {
	prefix, data, err := nip19.Decode(npub)
	if err != nil {
		return "", fmt.Errorf("invalid npub: %w", err)
	}
	if prefix != "npub" {
		return "", fmt.Errorf("not an npub: %s", prefix)
	}
	return data.(string), nil
}

// IsValidPubkey checks if a string is a valid hex pubkey
func IsValidPubkey(pubkey string) bool {
	if len(pubkey) != 64 {
		return false
	}
	for _, c := range pubkey {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// ShortPubkey returns first 8 chars of hex pubkey for display
func ShortPubkey(pubkey string) string {
	if len(pubkey) >= 8 {
		return pubkey[:8]
	}
	return pubkey
}

// IsWhitelisted checks if pubkey is in the allowed list
// Empty whitelist means anyone can authenticate (first user claims instance)
func IsWhitelisted(pubkey string, allowedPubkeys []string) bool {
	if len(allowedPubkeys) == 0 {
		return true // empty whitelist = open registration
	}
	for _, allowed := range allowedPubkeys {
		// Support both hex and npub in whitelist
		allowedHex := allowed
		if len(allowed) > 4 && allowed[:4] == "npub" {
			if hex, err := NpubToPubkey(allowed); err == nil {
				allowedHex = hex
			}
		}
		if allowedHex == pubkey {
			return true
		}
	}
	return false
}

// GetPubkeyFromPrivkey derives the public key from a private key
func GetPubkeyFromPrivkey(privkey string) (string, error) {
	pubkey, err := nostr.GetPublicKey(privkey)
	if err != nil {
		return "", fmt.Errorf("failed to derive pubkey: %w", err)
	}
	return pubkey, nil
}
