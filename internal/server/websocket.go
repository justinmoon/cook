package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/justinmoon/cook/internal/agent"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/branch"
	"github.com/justinmoon/cook/internal/env"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// Allow same-origin requests only
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // No origin header (e.g., non-browser clients)
		}
		// Compare origin to request host
		return origin == "http://"+r.Host || origin == "https://"+r.Host
	},
}

type wsMessage struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data,omitempty"`
}

type resizeMsg struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	owner := chi.URLParam(r, "owner")
	repoName := chi.URLParam(r, "repo")
	branchName := chi.URLParam(r, "name")
	repoRef := owner + "/" + repoName
	
	// Support multiple terminal tabs via ?tab=xxx query param
	// No tab param = agent session, with tab param = separate shell session
	tabID := r.URL.Query().Get("tab")
	sessionKey := repoRef + "/" + branchName
	if tabID != "" {
		sessionKey = sessionKey + "/" + tabID
	}
	isAgentSession := tabID == ""
	
	// Parse initial terminal size from URL (so PTY is created at correct size)
	var initialRows, initialCols uint16
	if rows := r.URL.Query().Get("rows"); rows != "" {
		if v, err := strconv.ParseUint(rows, 10, 16); err == nil {
			initialRows = uint16(v)
		}
	}
	if cols := r.URL.Query().Get("cols"); cols != "" {
		if v, err := strconv.ParseUint(cols, 10, 16); err == nil {
			initialCols = uint16(v)
		}
	}

	// Terminal access requires ownership
	pubkey := auth.GetPubkey(r.Context())
	if pubkey == "" || owner != pubkey {
		http.Error(w, "Forbidden: terminal access requires ownership", http.StatusForbidden)
		return
	}

	errBranchNotFound := errors.New("branch not found")
	errNoCheckout := errors.New("branch has no checkout path")
	errNoShell := errors.New("no shell found")

	// Get existing session or create new one (with initial size from URL)
	sess, created, err := s.termMgr.GetOrCreate(sessionKey, func() (*exec.Cmd, error) {
		branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
		b, err := branchStore.Get(repoRef, branchName)
		if err != nil {
			return nil, err
		}
		if b == nil {
			return nil, errBranchNotFound
		}
		if b.Environment.Path == "" {
			return nil, errNoCheckout
		}

		// Only check for agent session on the main (non-tab) terminal
		if isAgentSession {
			agentStore := agent.NewStore(s.db)
			agentSession, _ := agentStore.GetLatest(repoRef, branchName)
			if agentSession != nil {
				// Resume the agent session instead of creating a shell
				log.Printf("Resuming agent session for %s (type: %s)", sessionKey, agentSession.AgentType)
				return agent.SpawnResume(agentSession.AgentType, b.Environment.Path)
			}
		}

		// Get backend to use its environment (isolated HOME, dotfiles, etc.)
		backend, err := b.Backend()
		if err != nil {
			return nil, err
		}
		lb := backend.(*env.LocalBackend)

		// Create a shell PTY using the backend's Command method (gets proper env with isolated HOME)
		shells := []string{"/bin/zsh", "/bin/bash", "/bin/sh"}
		for _, shell := range shells {
			if _, err := os.Stat(shell); err == nil {
				cmd, err := lb.Command(context.Background(), shell, "-l")
				if err != nil {
					return nil, err
				}
				return cmd, nil
			}
		}
		return nil, errNoShell
	}, initialRows, initialCols)
	if err != nil {
		switch {
		case errors.Is(err, errBranchNotFound):
			http.Error(w, "Branch not found", http.StatusNotFound)
		case errors.Is(err, errNoCheckout):
			http.Error(w, "Branch has no checkout path", http.StatusBadRequest)
		case errors.Is(err, errNoShell):
			http.Error(w, "No shell found", http.StatusInternalServerError)
		default:
			http.Error(w, "Failed to create terminal session", http.StatusInternalServerError)
		}
		return
	}
	if created {
		log.Printf("Created new terminal session for %s (initial size: %dx%d)", sessionKey, initialCols, initialRows)
	} else {
		log.Printf("Attaching to existing terminal session for %s (client size: %dx%d)", sessionKey, initialCols, initialRows)
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Subscribe to output stream
	subID, snapshot, outCh := sess.Subscribe()
	defer sess.Unsubscribe(subID)

	// Wait for first resize message before sending snapshot.
	// This ensures the terminal is properly sized so line wrapping is correct.
	snapshotSent := false

	// Stream output to WebSocket.
	go func() {
		for chunk := range outCh {
			if err := conn.WriteMessage(websocket.BinaryMessage, chunk); err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}
		}
	}()

	// Read from WebSocket and write to PTY
	for {
		messageType, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket read error: %v", err)
			}
			return
		}

		switch messageType {
		case websocket.BinaryMessage, websocket.TextMessage:
			// Check if it's a control message
			var msg wsMessage
			if err := json.Unmarshal(data, &msg); err == nil && msg.Type != "" {
				switch msg.Type {
				case "resize":
					var resize resizeMsg
					if err := json.Unmarshal(msg.Data, &resize); err == nil {
						if err := sess.Resize(resize.Rows, resize.Cols); err != nil {
							log.Printf("PTY resize error: %v", err)
						}
						// Send snapshot after first resize
						// Now that PTY is created at correct size, replay should work for all sessions
						if !snapshotSent && len(snapshot) > 0 {
							if err := conn.WriteMessage(websocket.BinaryMessage, snapshot); err != nil {
								log.Printf("WebSocket write error (snapshot): %v", err)
								return
							}
							snapshotSent = true
						}
					}
				case "input":
					var input string
					if err := json.Unmarshal(msg.Data, &input); err == nil {
						if _, err := sess.Write([]byte(input)); err != nil {
							log.Printf("PTY write error: %v", err)
						}
					}
				}
			} else {
				// Raw input
				if _, err := sess.Write(data); err != nil {
					log.Printf("PTY write error: %v", err)
				}
			}
		}
	}
}

// StartTerminalSession is no longer needed - PTY is created on WebSocket connect
