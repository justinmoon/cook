package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

const (
	// NIP98Kind is the kind for HTTP Auth events
	NIP98Kind = 27235
	// NIP98MaxAge is maximum age of a NIP-98 event
	NIP98MaxAge = 60 * time.Second
)

// NIP98Event represents a NIP-98 HTTP Auth event
type NIP98Event struct {
	nostr.Event
}

// VerifyNIP98 verifies a NIP-98 event for the given request
func VerifyNIP98(event *nostr.Event, method, url string, body []byte) error {
	// Check kind
	if event.Kind != NIP98Kind {
		return fmt.Errorf("invalid event kind: expected %d, got %d", NIP98Kind, event.Kind)
	}

	// Verify signature
	ok, err := event.CheckSignature()
	if err != nil {
		return fmt.Errorf("signature check error: %w", err)
	}
	if !ok {
		return fmt.Errorf("invalid signature")
	}

	// Check timestamp
	eventTime := time.Unix(int64(event.CreatedAt), 0)
	if time.Since(eventTime) > NIP98MaxAge {
		return fmt.Errorf("event too old")
	}
	if time.Until(eventTime) > NIP98MaxAge {
		return fmt.Errorf("event timestamp in future")
	}

	// Check URL tag
	urlTag := getTag(event, "u")
	if urlTag == "" {
		return fmt.Errorf("missing 'u' (URL) tag")
	}
	// Normalize URLs for comparison (remove trailing slash)
	if strings.TrimSuffix(urlTag, "/") != strings.TrimSuffix(url, "/") {
		return fmt.Errorf("URL mismatch: expected %s, got %s", url, urlTag)
	}

	// Check method tag
	methodTag := getTag(event, "method")
	if methodTag == "" {
		return fmt.Errorf("missing 'method' tag")
	}
	if strings.ToUpper(methodTag) != strings.ToUpper(method) {
		return fmt.Errorf("method mismatch: expected %s, got %s", method, methodTag)
	}

	// Check payload tag for requests with body - REQUIRED when body present
	if len(body) > 0 {
		payloadTag := getTag(event, "payload")
		if payloadTag == "" {
			return fmt.Errorf("missing 'payload' tag for request with body")
		}
		hash := sha256.Sum256(body)
		expectedHash := hex.EncodeToString(hash[:])
		if payloadTag != expectedHash {
			return fmt.Errorf("payload hash mismatch")
		}
	}

	return nil
}

// getTag returns the first value of a tag by name
func getTag(event *nostr.Event, name string) string {
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == name {
			return tag[1]
		}
	}
	return ""
}

// ParseNIP98Header parses the Authorization header for NIP-98
func ParseNIP98Header(header string) (*nostr.Event, error) {
	// Header format: "Nostr <base64-encoded-event>"
	if !strings.HasPrefix(header, "Nostr ") {
		return nil, fmt.Errorf("invalid header format: expected 'Nostr <event>'")
	}

	encoded := strings.TrimPrefix(header, "Nostr ")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}

	var event nostr.Event
	if err := json.Unmarshal(decoded, &event); err != nil {
		return nil, fmt.Errorf("invalid event JSON: %w", err)
	}

	return &event, nil
}

// CreateNIP98Event creates a signed NIP-98 event for an HTTP request
func CreateNIP98Event(privateKey, method, url string, body []byte) (*nostr.Event, error) {
	tags := nostr.Tags{
		{"u", url},
		{"method", strings.ToUpper(method)},
	}

	if len(body) > 0 {
		hash := sha256.Sum256(body)
		tags = append(tags, nostr.Tag{"payload", hex.EncodeToString(hash[:])})
	}

	event := &nostr.Event{
		Kind:      NIP98Kind,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags:      tags,
		Content:   "",
	}

	if err := event.Sign(privateKey); err != nil {
		return nil, fmt.Errorf("failed to sign event: %w", err)
	}

	return event, nil
}

// EncodeNIP98Header encodes a NIP-98 event as an Authorization header value
func EncodeNIP98Header(event *nostr.Event) (string, error) {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	return "Nostr " + base64.StdEncoding.EncodeToString(eventJSON), nil
}

// NIP98Middleware creates middleware that validates NIP-98 Authorization headers
type NIP98Middleware struct {
	allowedPubkeys []string
}

// NewNIP98Middleware creates a new NIP-98 middleware
func NewNIP98Middleware(allowedPubkeys []string) *NIP98Middleware {
	return &NIP98Middleware{
		allowedPubkeys: allowedPubkeys,
	}
}

// Handler returns the middleware handler
func (m *NIP98Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" || !strings.HasPrefix(authHeader, "Nostr ") {
			http.Error(w, "Missing or invalid Authorization header", http.StatusUnauthorized)
			return
		}

		// Parse the event from header
		event, err := ParseNIP98Header(authHeader)
		if err != nil {
			http.Error(w, "Invalid NIP-98 header: "+err.Error(), http.StatusUnauthorized)
			return
		}

		// Read body for verification (need to restore it for handler)
		var body []byte
		if r.Body != nil {
			body, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "Failed to read request body", http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(strings.NewReader(string(body)))
		}

		// Build full URL for verification
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		if fwdProto := r.Header.Get("X-Forwarded-Proto"); fwdProto != "" {
			scheme = fwdProto
		}
		fullURL := scheme + "://" + r.Host + r.URL.RequestURI()

		// Verify the event
		if err := VerifyNIP98(event, r.Method, fullURL, body); err != nil {
			http.Error(w, "NIP-98 verification failed: "+err.Error(), http.StatusUnauthorized)
			return
		}

		// Check whitelist
		if !IsWhitelisted(event.PubKey, m.allowedPubkeys) {
			http.Error(w, "Access denied: pubkey not whitelisted", http.StatusForbidden)
			return
		}

		// Add pubkey to context
		ctx := context.WithValue(r.Context(), ContextKeyPubkey, event.PubKey)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
