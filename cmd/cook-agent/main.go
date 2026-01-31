// cook-agent runs inside execution environments (containers, sandboxes)
// and manages persistent terminal sessions that survive client disconnects.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var (
	listenAddr = flag.String("listen", ":7422", "address to listen on")
	upgrader   = websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
	}
)

func main() {
	flag.Parse()

	mgr := NewSessionManager()

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("WebSocket upgrade error: %v", err)
			return
		}
		handleConnection(conn, mgr)
	})

	log.Printf("cook-agent listening on %s (WebSocket)", *listenAddr)
	if err := http.ListenAndServe(*listenAddr, nil); err != nil {
		log.Fatalf("Failed to listen on %s: %v", *listenAddr, err)
	}
}

// Message types for the protocol
type Message struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id,omitempty"`
	Command   string `json:"command,omitempty"`
	WorkDir   string `json:"workdir,omitempty"`
	Data      []byte `json:"data,omitempty"`
	Rows      int    `json:"rows,omitempty"`
	Cols      int    `json:"cols,omitempty"`
	Error     string `json:"error,omitempty"`
	Sessions  []string `json:"sessions,omitempty"`
}

const (
	MsgCreate  = "create"
	MsgAttach  = "attach"
	MsgDetach  = "detach"
	MsgInput   = "input"
	MsgOutput  = "output"
	MsgResize  = "resize"
	MsgList    = "list"
	MsgOK      = "ok"
	MsgError   = "error"
)

// Session represents a persistent terminal session
type Session struct {
	ID      string
	Command string
	WorkDir string
	Cmd     *exec.Cmd
	Pty     *os.File

	mu        sync.Mutex
	clients   map[*websocket.Conn]bool
	closeOnce sync.Once
}

func (s *Session) AddClient(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clients[conn] = true
}

func (s *Session) RemoveClient(conn *websocket.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.clients, conn)
}

func (s *Session) Broadcast(data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	msg := Message{Type: MsgOutput, SessionID: s.ID, Data: data}
	encoded, _ := json.Marshal(msg)

	for conn := range s.clients {
		conn.WriteMessage(websocket.TextMessage, encoded)
	}
}

func (s *Session) Resize(rows, cols int) error {
	if s.Pty == nil {
		return fmt.Errorf("no pty")
	}
	ws := struct {
		Row    uint16
		Col    uint16
		Xpixel uint16
		Ypixel uint16
	}{uint16(rows), uint16(cols), 0, 0}
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		s.Pty.Fd(),
		syscall.TIOCSWINSZ,
		uintptr(unsafe.Pointer(&ws)),
	)
	if errno != 0 {
		return errno
	}
	return nil
}

// SessionManager manages multiple sessions
type SessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*Session
}

func NewSessionManager() *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
	}
}

func (m *SessionManager) Create(id, command, workDir string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.sessions[id]; exists {
		return nil, fmt.Errorf("session %s already exists", id)
	}

	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to start pty: %w", err)
	}

	session := &Session{
		ID:      id,
		Command: command,
		WorkDir: workDir,
		Cmd:     cmd,
		Pty:     ptmx,
		clients: make(map[*websocket.Conn]bool),
	}

	m.sessions[id] = session

	// Read from PTY and broadcast to all clients
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if err != nil {
				if err != io.EOF {
					log.Printf("Session %s read error: %v", id, err)
				}
				break
			}
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				session.Broadcast(data)
			}
		}
		// Process ended, clean up
		m.mu.Lock()
		delete(m.sessions, id)
		m.mu.Unlock()
		log.Printf("Session %s ended", id)
	}()

	log.Printf("Created session %s: %s", id, command)
	return session, nil
}

func (m *SessionManager) Get(id string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.sessions[id]
}

func (m *SessionManager) List() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	return ids
}

func handleConnection(conn *websocket.Conn, mgr *SessionManager) {
	defer conn.Close()

	var attachedSession *Session

	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error: %v", err)
			}
			break
		}

		var msg Message
		if err := json.Unmarshal(data, &msg); err != nil {
			log.Printf("JSON decode error: %v", err)
			continue
		}

		switch msg.Type {
		case MsgCreate:
			session, err := mgr.Create(msg.SessionID, msg.Command, msg.WorkDir)
			if err != nil {
				sendError(conn, err.Error())
			} else {
				session.AddClient(conn)
				attachedSession = session
				sendOK(conn, msg.SessionID)
			}

		case MsgAttach:
			session := mgr.Get(msg.SessionID)
			if session == nil {
				sendError(conn, "session not found")
			} else {
				if attachedSession != nil {
					attachedSession.RemoveClient(conn)
				}
				session.AddClient(conn)
				attachedSession = session
				sendOK(conn, msg.SessionID)
			}

		case MsgDetach:
			if attachedSession != nil {
				attachedSession.RemoveClient(conn)
				attachedSession = nil
			}
			sendOK(conn, "")

		case MsgInput:
			session := mgr.Get(msg.SessionID)
			if session != nil && session.Pty != nil {
				session.Pty.Write(msg.Data)
			}

		case MsgResize:
			session := mgr.Get(msg.SessionID)
			if session != nil {
				if err := session.Resize(msg.Rows, msg.Cols); err != nil {
					log.Printf("Resize error: %v", err)
				}
			}

		case MsgList:
			sendList(conn, mgr.List())
		}
	}

	// Clean up on disconnect
	if attachedSession != nil {
		attachedSession.RemoveClient(conn)
	}
}

func sendError(conn *websocket.Conn, errMsg string) {
	msg := Message{Type: MsgError, Error: errMsg}
	encoded, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, encoded)
}

func sendOK(conn *websocket.Conn, sessionID string) {
	msg := Message{Type: MsgOK, SessionID: sessionID}
	encoded, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, encoded)
}

func sendList(conn *websocket.Conn, sessions []string) {
	msg := Message{Type: MsgList, Sessions: sessions}
	encoded, _ := json.Marshal(msg)
	conn.WriteMessage(websocket.TextMessage, encoded)
}
