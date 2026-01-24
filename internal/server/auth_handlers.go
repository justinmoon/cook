package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/justinmoon/cook/internal/auth"
	"github.com/nbd-wtf/go-nostr"
)

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// If already authenticated, redirect to repos
	if session := auth.GetSession(r.Context()); session != nil {
		http.Redirect(w, r, "/repos", http.StatusSeeOther)
		return
	}

	// Render login page
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(loginPageHTML))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Get session and delete it
	if session := auth.GetSession(r.Context()); session != nil {
		s.sessionStore.Delete(session.ID)
	}

	// Clear cookie
	auth.ClearSessionCookie(w)

	// Redirect to login
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	ip := getClientIP(r)

	challenge, err := s.challengeStore.Create(ip)
	if err != nil {
		http.Error(w, err.Error(), http.StatusTooManyRequests)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"challenge": challenge,
	})
}

func (s *Server) handleAuthVerify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Challenge string       `json:"challenge"`
		Event     nostr.Event  `json:"event"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonError(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	ip := getClientIP(r)

	// Validate challenge
	if err := s.challengeStore.Validate(req.Challenge, ip); err != nil {
		jsonError(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Verify nostr event signature and content
	if err := auth.VerifyAuthEvent(&req.Event, req.Challenge); err != nil {
		jsonError(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// Check whitelist
	if !auth.IsWhitelisted(req.Event.PubKey, s.cfg.Server.AllowedPubkeys) {
		jsonError(w, "Access denied: pubkey not whitelisted", http.StatusForbidden)
		return
	}

	// First login claims instance ownership
	if !s.cfg.HasOwner() {
		if err := s.cfg.SetOwner(req.Event.PubKey); err != nil {
			// Non-fatal - log but continue
			// log.Printf("Warning: failed to set owner: %v", err)
		}
	}

	// Create session
	session, err := s.sessionStore.Create(req.Event.PubKey)
	if err != nil {
		jsonError(w, "Failed to create session", http.StatusInternalServerError)
		return
	}

	// Set cookie (secure only if not localhost)
	secure := !strings.HasPrefix(r.Host, "localhost") && !strings.HasPrefix(r.Host, "127.0.0.1")
	auth.SetSessionCookie(w, session.ID, secure)

	// Set CSRF cookie
	auth.SetCSRFCookie(w, r, secure)

	// Return success
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"pubkey":  req.Event.PubKey,
	})
}

func getClientIP(r *http.Request) string {
	// Check X-Forwarded-For header
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}

	// Check X-Real-IP header
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}

	// Fall back to RemoteAddr
	ip := r.RemoteAddr
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	return ip
}

func jsonError(w http.ResponseWriter, message string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}

const loginPageHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Login - Cook</title>
  <style>
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      line-height: 1.6;
      color: #333;
      background: #f5f5f5;
      display: flex;
      justify-content: center;
      align-items: center;
      min-height: 100vh;
    }
    .login-container {
      background: white;
      padding: 40px;
      border-radius: 8px;
      box-shadow: 0 2px 10px rgba(0,0,0,0.1);
      max-width: 400px;
      width: 100%;
    }
    h1 { font-size: 2em; margin-bottom: 10px; text-align: center; }
    .subtitle { text-align: center; color: #666; margin-bottom: 30px; }
    .button {
      display: block;
      width: 100%;
      padding: 12px 20px;
      background: #8b5cf6;
      color: white;
      border: none;
      border-radius: 5px;
      cursor: pointer;
      font-size: 1em;
      font-weight: 600;
    }
    .button:hover { background: #7c3aed; }
    .button:disabled { background: #ccc; cursor: not-allowed; }
    .error { background: #fee; color: #c33; padding: 12px; border-radius: 5px; margin-bottom: 20px; border: 1px solid #fcc; }
    .info { background: #e3f2fd; color: #1565c0; padding: 12px; border-radius: 5px; margin-top: 20px; font-size: 0.9em; border: 1px solid #90caf9; }
    .info a { color: #1565c0; }
    #status { text-align: center; margin-top: 15px; font-size: 0.9em; color: #666; }
  </style>
</head>
<body>
  <div class="login-container">
    <h1>🍳 Cook</h1>
    <p class="subtitle">AI-Native Software Factory</p>
    
    <button id="loginBtn" class="button">Login with Nostr</button>
    <div id="status"></div>
    
    <div class="info" id="extensionInfo" style="display: none;">
      You need a NIP-07 browser extension:
      <ul style="margin: 10px 0 0 20px;">
        <li><a href="https://getalby.com/" target="_blank">Alby</a></li>
        <li><a href="https://github.com/niclas/nos2x-fox" target="_blank">nos2x</a></li>
      </ul>
    </div>
  </div>

  <script>
    const loginBtn = document.getElementById('loginBtn');
    const status = document.getElementById('status');
    const extensionInfo = document.getElementById('extensionInfo');

    async function login() {
      try {
        if (!window.nostr) {
          extensionInfo.style.display = 'block';
          status.textContent = 'No Nostr extension found';
          return;
        }

        loginBtn.disabled = true;
        status.textContent = 'Requesting public key...';

        const pubkey = await window.nostr.getPublicKey();
        status.textContent = 'Fetching challenge...';

        const challengeRes = await fetch('/auth/challenge', { credentials: 'include' });
        if (!challengeRes.ok) throw new Error('Failed to get challenge');
        const { challenge } = await challengeRes.json();

        status.textContent = 'Please sign the request...';

        const event = {
          kind: 27235,
          created_at: Math.floor(Date.now() / 1000),
          tags: [],
          content: challenge
        };

        const signedEvent = await window.nostr.signEvent(event);
        status.textContent = 'Verifying...';

        const verifyRes = await fetch('/auth/verify', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          credentials: 'include',
          body: JSON.stringify({ event: signedEvent, challenge })
        });

        const result = await verifyRes.json();
        if (!verifyRes.ok) throw new Error(result.error || 'Authentication failed');

        status.textContent = 'Success! Redirecting...';
        setTimeout(() => window.location.href = '/repos', 500);
      } catch (error) {
        console.error('Login error:', error);
        status.textContent = 'Error: ' + error.message;
        loginBtn.disabled = false;
      }
    }

    loginBtn.addEventListener('click', login);
    if (!window.nostr) extensionInfo.style.display = 'block';
  </script>
</body>
</html>`
