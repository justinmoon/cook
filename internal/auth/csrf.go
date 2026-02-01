package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strings"
)

const (
	CSRFCookieName = "cook_csrf"
	CSRFHeaderName = "X-CSRF-Token"
	CSRFFormField  = "csrf_token"
	CSRFTokenLen   = 32
)

// GenerateCSRFToken generates a new CSRF token
func GenerateCSRFToken() (string, error) {
	b := make([]byte, CSRFTokenLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// SetCSRFCookie sets or refreshes the CSRF cookie
func SetCSRFCookie(w http.ResponseWriter, r *http.Request, secure bool) string {
	// Check if we already have a valid token
	if cookie, err := r.Cookie(CSRFCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}

	// Generate new token
	token, err := GenerateCSRFToken()
	if err != nil {
		return ""
	}

	cookie := &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: false, // Must be readable by JS if needed
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(SessionDuration.Seconds()),
	}
	http.SetCookie(w, cookie)
	return token
}

// ValidateCSRF validates the CSRF token from request
func ValidateCSRF(r *http.Request) bool {
	// Get cookie token
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	cookieToken := cookie.Value

	// Get submitted token (try header first, then form)
	submittedToken := r.Header.Get(CSRFHeaderName)
	if submittedToken == "" {
		submittedToken = r.FormValue(CSRFFormField)
	}

	if submittedToken == "" {
		return false
	}

	// Constant-time comparison
	return subtle.ConstantTimeCompare([]byte(cookieToken), []byte(submittedToken)) == 1
}

// CSRFMiddleware creates middleware that validates CSRF tokens for state-changing methods
func CSRFMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Only validate for state-changing methods
		if r.Method == "POST" || r.Method == "PUT" || r.Method == "DELETE" || r.Method == "PATCH" {
			// Skip CSRF for auth endpoints (login flow doesn't have CSRF token yet)
			if strings.HasPrefix(r.URL.Path, "/auth/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip CSRF for git HTTP backend (used by sandboxes)
			if strings.HasPrefix(r.URL.Path, "/git/") {
				next.ServeHTTP(w, r)
				return
			}

			// Skip CSRF for API routes with Authorization header (NIP-98 or Bearer)
			if strings.HasPrefix(r.URL.Path, "/api/") {
				authHeader := r.Header.Get("Authorization")
				if authHeader != "" {
					next.ServeHTTP(w, r)
					return
				}
			}

			// Skip for SSE endpoints
			if strings.HasPrefix(r.URL.Path, "/events") {
				next.ServeHTTP(w, r)
				return
			}

			// Validate CSRF
			if !ValidateCSRF(r) {
				http.Error(w, "CSRF validation failed", http.StatusForbidden)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
