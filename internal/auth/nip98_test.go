package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func createTestEvent(method, url string, body []byte, createdAt time.Time) *nostr.Event {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)

	tags := nostr.Tags{
		{"u", url},
		{"method", method},
	}

	if len(body) > 0 {
		hash := sha256.Sum256(body)
		tags = append(tags, nostr.Tag{"payload", hex.EncodeToString(hash[:])})
	}

	event := &nostr.Event{
		Kind:      NIP98Kind,
		PubKey:    pk,
		CreatedAt: nostr.Timestamp(createdAt.Unix()),
		Tags:      tags,
		Content:   "",
	}
	event.Sign(sk)
	return event
}

func TestVerifyNIP98_ValidRequest(t *testing.T) {
	method := "GET"
	url := "http://localhost:8080/api/v1/repos"
	event := createTestEvent(method, url, nil, time.Now())

	err := VerifyNIP98(event, method, url, nil)
	if err != nil {
		t.Errorf("VerifyNIP98 failed for valid request: %v", err)
	}
}

func TestVerifyNIP98_ValidRequestWithBody(t *testing.T) {
	method := "POST"
	url := "http://localhost:8080/api/v1/tasks"
	body := []byte(`{"repo":"owner/repo","slug":"task-1","title":"Test"}`)
	event := createTestEvent(method, url, body, time.Now())

	err := VerifyNIP98(event, method, url, body)
	if err != nil {
		t.Errorf("VerifyNIP98 failed for valid POST request: %v", err)
	}
}

func TestVerifyNIP98_MissingPayloadForBodyRequest(t *testing.T) {
	method := "POST"
	url := "http://localhost:8080/api/v1/tasks"
	body := []byte(`{"repo":"owner/repo","slug":"task-1","title":"Test"}`)

	// Create event without payload tag
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	event := &nostr.Event{
		Kind:      NIP98Kind,
		PubKey:    pk,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"u", url},
			{"method", method},
			// No payload tag
		},
		Content: "",
	}
	event.Sign(sk)

	err := VerifyNIP98(event, method, url, body)
	if err == nil {
		t.Error("VerifyNIP98 should reject POST with body but no payload tag")
	}
}

func TestVerifyNIP98_WrongPayloadHash(t *testing.T) {
	method := "POST"
	url := "http://localhost:8080/api/v1/tasks"
	body := []byte(`{"repo":"owner/repo","slug":"task-1","title":"Test"}`)

	// Create event with wrong payload hash
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	event := &nostr.Event{
		Kind:      NIP98Kind,
		PubKey:    pk,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"u", url},
			{"method", method},
			{"payload", "0000000000000000000000000000000000000000000000000000000000000000"},
		},
		Content: "",
	}
	event.Sign(sk)

	err := VerifyNIP98(event, method, url, body)
	if err == nil {
		t.Error("VerifyNIP98 should reject wrong payload hash")
	}
}

func TestVerifyNIP98_ExpiredEvent(t *testing.T) {
	method := "GET"
	url := "http://localhost:8080/api/v1/repos"
	// Event from 2 minutes ago (past NIP98MaxAge)
	event := createTestEvent(method, url, nil, time.Now().Add(-2*time.Minute))

	err := VerifyNIP98(event, method, url, nil)
	if err == nil {
		t.Error("VerifyNIP98 should reject expired event")
	}
}

func TestVerifyNIP98_FutureEvent(t *testing.T) {
	method := "GET"
	url := "http://localhost:8080/api/v1/repos"
	// Event 2 minutes in future
	event := createTestEvent(method, url, nil, time.Now().Add(2*time.Minute))

	err := VerifyNIP98(event, method, url, nil)
	if err == nil {
		t.Error("VerifyNIP98 should reject future event")
	}
}

func TestVerifyNIP98_URLMismatch(t *testing.T) {
	method := "GET"
	eventURL := "http://localhost:8080/api/v1/repos"
	requestURL := "http://localhost:8080/api/v1/tasks"
	event := createTestEvent(method, eventURL, nil, time.Now())

	err := VerifyNIP98(event, method, requestURL, nil)
	if err == nil {
		t.Error("VerifyNIP98 should reject URL mismatch")
	}
}

func TestVerifyNIP98_MethodMismatch(t *testing.T) {
	eventMethod := "GET"
	requestMethod := "POST"
	url := "http://localhost:8080/api/v1/repos"
	event := createTestEvent(eventMethod, url, nil, time.Now())

	err := VerifyNIP98(event, requestMethod, url, nil)
	if err == nil {
		t.Error("VerifyNIP98 should reject method mismatch")
	}
}

func TestVerifyNIP98_WrongKind(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	pk, _ := nostr.GetPublicKey(sk)
	event := &nostr.Event{
		Kind:      1, // Wrong kind (should be 27235)
		PubKey:    pk,
		CreatedAt: nostr.Timestamp(time.Now().Unix()),
		Tags: nostr.Tags{
			{"u", "http://localhost:8080/api"},
			{"method", "GET"},
		},
		Content: "",
	}
	event.Sign(sk)

	err := VerifyNIP98(event, "GET", "http://localhost:8080/api", nil)
	if err == nil {
		t.Error("VerifyNIP98 should reject wrong event kind")
	}
}
