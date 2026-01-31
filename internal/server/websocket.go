package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
	"github.com/justinmoon/cook/internal/agent"
	"github.com/justinmoon/cook/internal/auth"
	"github.com/justinmoon/cook/internal/branch"
	"github.com/justinmoon/cook/internal/env"
	"github.com/justinmoon/cook/internal/envagent"
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

	// Get branch to determine backend type
	branchStore := branch.NewStore(s.db, s.cfg.Server.DataDir)
	b, err := branchStore.Get(repoRef, branchName)
	if err != nil {
		http.Error(w, "Failed to get branch", http.StatusInternalServerError)
		return
	}
	if b == nil {
		http.Error(w, "Branch not found", http.StatusNotFound)
		return
	}
	if b.Environment.Path == "" {
		http.Error(w, "Branch has no checkout path", http.StatusBadRequest)
		return
	}

	// For Docker and Modal backends, use cook-agent protocol
	if b.Environment.Backend == "docker" || b.Environment.Backend == "modal" {
		s.handleRemoteTerminalWS(w, r, b, sessionKey, isAgentSession, initialRows, initialCols)
		return
	}

	// For local backend, use direct PTY management
	errNoShell := errors.New("no shell found")

	// Get existing session or create new one (with initial size from URL)
	sess, created, err := s.termMgr.GetOrCreate(sessionKey, func() (*exec.Cmd, error) {
		// Only check for agent session on the main (non-tab) terminal
		if isAgentSession {
			agentStore := agent.NewStore(s.db)
			agentSession, _ := agentStore.GetLatest(repoRef, branchName)
			if agentSession != nil {
				// Resume the agent session instead of creating a shell
				log.Printf("Resuming agent session for %s (type: %s)", sessionKey, agentSession.AgentType)
				return agent.SpawnResume(agentSession.AgentType, b.Environment.Path, repoRef, branchName)
			}
		}

		// Get backend to create shell command
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
		if errors.Is(err, errNoShell) {
			http.Error(w, "No shell found", http.StatusInternalServerError)
		} else {
			log.Printf("Failed to create terminal session: %v", err)
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

// handleRemoteTerminalWS handles terminal connections for Docker/Modal backends via cook-agent
func (s *Server) handleRemoteTerminalWS(w http.ResponseWriter, r *http.Request, b *branch.Branch, sessionKey string, isAgentSession bool, initialRows, initialCols uint16) {
	// Get the backend to find agent address
	backend, err := b.Backend()
	if err != nil {
		http.Error(w, "Failed to get backend: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Get agent address from backend (works for both Docker and Modal)
	var agentAddr string
	switch be := backend.(type) {
	case *env.DockerBackend:
		agentAddr = be.AgentAddr()
	case *env.ModalBackend:
		agentAddr = be.AgentAddr()
	default:
		http.Error(w, "Backend does not support cook-agent", http.StatusBadRequest)
		return
	}

	// Connect to cook-agent
	agentClient, err := envagent.Dial(agentAddr)
	if err != nil {
		log.Printf("Failed to connect to cook-agent at %s: %v", agentAddr, err)
		http.Error(w, "Failed to connect to container agent", http.StatusInternalServerError)
		return
	}
	defer agentClient.Close()

	// Determine the command to run
	var command string
	if isAgentSession {
		agentStore := agent.NewStore(s.db)
		agentSession, _ := agentStore.GetLatest(b.Repo, b.Name)
		if agentSession != nil {
			// Build command with prompt if available
			prompt := agentSession.Prompt
			switch agentSession.AgentType {
			case agent.AgentClaude:
				if prompt != "" {
					escapedPrompt := strings.ReplaceAll(prompt, "'", "'\"'\"'")
					command = fmt.Sprintf("claude --dangerously-skip-permissions '%s'", escapedPrompt)
				} else {
					command = "claude --dangerously-skip-permissions"
				}
			case agent.AgentCodex:
				if prompt != "" {
					escapedPrompt := strings.ReplaceAll(prompt, "'", "'\"'\"'")
					command = fmt.Sprintf("codex '%s'", escapedPrompt)
				} else {
					command = "codex"
				}
			default:
				command = "bash -l"
			}
		} else {
			command = "bash -l"
		}
	} else {
		command = "bash -l"
	}

	// Try to attach to existing session, or create new one
	sessionID := sessionKey
	err = agentClient.AttachSession(sessionID)
	if err != nil {
		// Session doesn't exist, create it
		log.Printf("Creating new agent session %s: %s", sessionID, command)
		err = agentClient.CreateSession(sessionID, command, "/workspace")
		if err != nil {
			log.Printf("Failed to create agent session: %v", err)
			http.Error(w, "Failed to create session in container", http.StatusInternalServerError)
			return
		}
	} else {
		log.Printf("Attached to existing agent session %s", sessionID)
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Set initial size
	if initialRows > 0 && initialCols > 0 {
		agentClient.Resize(sessionID, int(initialRows), int(initialCols))
	}

	// Forward agent output to WebSocket
	agentClient.SetOutputHandler(func(sid string, data []byte) {
		if sid == sessionID {
			if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
				log.Printf("WebSocket write error: %v", err)
			}
		}
	})

	// Start reading from agent in background
	go func() {
		if err := agentClient.ReadLoop(); err != nil {
			log.Printf("Agent read loop error: %v", err)
		}
		conn.Close()
	}()

	// Read from WebSocket and forward to agent
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
						agentClient.Resize(sessionID, int(resize.Rows), int(resize.Cols))
					}
				case "input":
					var input string
					if err := json.Unmarshal(msg.Data, &input); err == nil {
						agentClient.SendInput(sessionID, []byte(input))
					}
				}
			} else {
				// Raw input
				agentClient.SendInput(sessionID, data)
			}
		}
	}
}
