package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
)

type contextKey string

const (
	// ContextKeyPubkey is the context key for the authenticated pubkey
	ContextKeyPubkey contextKey = "pubkey"
	// ContextKeySession is the context key for the session
	ContextKeySession contextKey = "session"
)

// GetPubkey returns the authenticated pubkey from context
func GetPubkey(ctx context.Context) string {
	if pubkey, ok := ctx.Value(ContextKeyPubkey).(string); ok {
		return pubkey
	}
	return ""
}

// GetSession returns the session from context
func GetSession(ctx context.Context) *Session {
	if session, ok := ctx.Value(ContextKeySession).(*Session); ok {
		return session
	}
	return nil
}

// Middleware creates an auth middleware that validates sessions
type Middleware struct {
	sessionStore   *SessionStore
	enabled        bool
	allowedPubkeys []string
	publicPaths    []string
}

// NewMiddleware creates a new auth middleware
func NewMiddleware(sessionStore *SessionStore, enabled bool, allowedPubkeys []string) *Middleware {
	return &Middleware{
		sessionStore:   sessionStore,
		enabled:        enabled,
		allowedPubkeys: allowedPubkeys,
		publicPaths: []string{
			"/health",
			"/login",
			"/auth/challenge",
			"/auth/verify",
			"/static/",
		},
	}
}

// Handler returns the middleware handler
func (m *Middleware) Handler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// If auth is disabled, pass through
		if !m.enabled {
			next.ServeHTTP(w, r)
			return
		}

		// Check if path is public
		for _, path := range m.publicPaths {
			if strings.HasPrefix(r.URL.Path, path) {
				next.ServeHTTP(w, r)
				return
			}
		}

		// Try NIP-98 auth first (for API requests)
		authHeader := r.Header.Get("Authorization")
		if strings.HasPrefix(authHeader, "Nostr ") {
			pubkey, err := m.validateNIP98(r, authHeader)
			if err != nil {
				http.Error(w, "NIP-98 auth failed: "+err.Error(), http.StatusUnauthorized)
				return
			}
			// Check whitelist
			if !IsWhitelisted(pubkey, m.allowedPubkeys) {
				http.Error(w, "Access denied: pubkey not whitelisted", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), ContextKeyPubkey, pubkey)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		// Try to get session from cookie
		sessionID := getSessionCookie(r)
		if sessionID == "" {
			// Try Bearer token
			sessionID = getBearerToken(r)
		}

		if sessionID == "" {
			redirectToLogin(w, r)
			return
		}

		// Validate session
		session, err := m.sessionStore.Validate(sessionID)
		if err != nil || session == nil {
			redirectToLogin(w, r)
			return
		}

		// Add pubkey and session to context
		ctx := context.WithValue(r.Context(), ContextKeyPubkey, session.Pubkey)
		ctx = context.WithValue(ctx, ContextKeySession, session)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validateNIP98 validates a NIP-98 Authorization header
func (m *Middleware) validateNIP98(r *http.Request, authHeader string) (string, error) {
	// Parse the event from header
	event, err := ParseNIP98Header(authHeader)
	if err != nil {
		return "", err
	}

	// Read body for verification (need to restore it for handler)
	var body []byte
	if r.Body != nil {
		body, err = io.ReadAll(r.Body)
		if err != nil {
			return "", err
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
		return "", err
	}

	return event.PubKey, nil
}

func getSessionCookie(r *http.Request) string {
	cookie, err := r.Cookie("cook_session")
	if err != nil {
		return ""
	}
	return cookie.Value
}

func getBearerToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return ""
}

func redirectToLogin(w http.ResponseWriter, r *http.Request) {
	// For API requests, return 401
	if strings.HasPrefix(r.URL.Path, "/api/") || r.Header.Get("Accept") == "application/json" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	// For browser requests, redirect to login
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// SetSessionCookie sets the session cookie on a response
func SetSessionCookie(w http.ResponseWriter, sessionID string, secure bool) {
	cookie := &http.Cookie{
		Name:     "cook_session",
		Value:    sessionID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(SessionDuration.Seconds()),
	}
	http.SetCookie(w, cookie)
}

// ClearSessionCookie clears the session cookie
func ClearSessionCookie(w http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:     "cook_session",
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	}
	http.SetCookie(w, cookie)
}
