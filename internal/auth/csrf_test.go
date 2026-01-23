package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCSRFMiddleware_SkipsAuthEndpoints(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	}))

	tests := []struct {
		name       string
		path       string
		method     string
		wantStatus int
	}{
		{"GET request passes", "/dashboard", "GET", http.StatusOK},
		{"POST to /auth/verify skips CSRF", "/auth/verify", "POST", http.StatusOK},
		{"POST to /auth/challenge skips CSRF", "/auth/challenge", "POST", http.StatusOK},
		{"POST to /dashboard blocked without CSRF", "/dashboard", "POST", http.StatusForbidden},
		{"POST to /repos blocked without CSRF", "/repos/owner/name/tasks", "POST", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestCSRFMiddleware_ValidatesToken(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Generate a token
	token, err := GenerateCSRFToken()
	if err != nil {
		t.Fatalf("failed to generate token: %v", err)
	}

	tests := []struct {
		name        string
		cookieToken string
		formToken   string
		wantStatus  int
	}{
		{"valid token in form", token, token, http.StatusOK},
		{"missing cookie", "", token, http.StatusForbidden},
		{"missing form token", token, "", http.StatusForbidden},
		{"mismatched tokens", token, "wrong-token", http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := "csrf_token=" + tt.formToken
			req := httptest.NewRequest("POST", "/dashboard/repos", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			if tt.cookieToken != "" {
				req.AddCookie(&http.Cookie{
					Name:  CSRFCookieName,
					Value: tt.cookieToken,
				})
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Errorf("got status %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}

func TestCSRFMiddleware_AcceptsHeaderToken(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token, _ := GenerateCSRFToken()

	req := httptest.NewRequest("POST", "/dashboard/repos", nil)
	req.Header.Set(CSRFHeaderName, token)
	req.AddCookie(&http.Cookie{
		Name:  CSRFCookieName,
		Value: token,
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("got status %d, want %d", rec.Code, http.StatusOK)
	}
}

func TestCSRFMiddleware_SkipsAPIWithAuth(t *testing.T) {
	handler := CSRFMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// API request with Authorization header should skip CSRF
	req := httptest.NewRequest("POST", "/api/v1/repos", nil)
	req.Header.Set("Authorization", "Bearer some-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("API with auth: got status %d, want %d", rec.Code, http.StatusOK)
	}

	// API request without Authorization header should require CSRF
	req2 := httptest.NewRequest("POST", "/api/v1/repos", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusForbidden {
		t.Errorf("API without auth: got status %d, want %d", rec2.Code, http.StatusForbidden)
	}
}
