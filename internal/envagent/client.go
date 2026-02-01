// Package envagent provides a client for communicating with cook-agent
// running inside execution environments.
package envagent

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// Message types - must match cook-agent
const (
	MsgCreate = "create"
	MsgAttach = "attach"
	MsgDetach = "detach"
	MsgInput  = "input"
	MsgOutput = "output"
	MsgResize = "resize"
	MsgList   = "list"
	MsgOK     = "ok"
	MsgError  = "error"
)

// Message is the wire format for agent communication
type Message struct {
	Type      string   `json:"type"`
	SessionID string   `json:"session_id,omitempty"`
	Command   string   `json:"command,omitempty"`
	WorkDir   string   `json:"workdir,omitempty"`
	Data      []byte   `json:"data,omitempty"`
	Rows      int      `json:"rows,omitempty"`
	Cols      int      `json:"cols,omitempty"`
	Error     string   `json:"error,omitempty"`
	Sessions  []string `json:"sessions,omitempty"`
}

// Client connects to a cook-agent instance via WebSocket
type Client struct {
	conn     *websocket.Conn
	mu       sync.Mutex
	onOutput func(sessionID string, data []byte)
}

// Dial connects to a cook-agent at the given address.
// The address can be:
//   - "host:port" (converted to ws://host:port)
//   - "ws://..." or "wss://..." (used as-is)
//   - "https://..." (converted to wss://...)
func Dial(addr string) (*Client, error) {
	// Convert address to WebSocket URL
	wsURL := addr
	if strings.HasPrefix(addr, "https://") {
		wsURL = "wss://" + strings.TrimPrefix(addr, "https://")
	} else if strings.HasPrefix(addr, "http://") {
		wsURL = "ws://" + strings.TrimPrefix(addr, "http://")
	} else if !strings.HasPrefix(addr, "ws://") && !strings.HasPrefix(addr, "wss://") {
		// Assume host:port format
		wsURL = "ws://" + addr
	}

	dialer := websocket.DefaultDialer
	tlsConfig := (*tls.Config)(nil)
	if isEnvTrue("COOK_AGENT_INSECURE") || isEnvTrue("COOK_FLY_AGENT_INSECURE") {
		tlsConfig = &tls.Config{InsecureSkipVerify: true}
	}

	if dnsServer := strings.TrimSpace(os.Getenv("COOK_AGENT_DNS_SERVER")); dnsServer != "" {
		resolver := &net.Resolver{
			PreferGo: true,
			Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "udp", dnsServer)
			},
		}
		netDialer := &net.Dialer{Resolver: resolver}
		dialer = &websocket.Dialer{
			Proxy:            websocket.DefaultDialer.Proxy,
			HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
			TLSClientConfig:  tlsConfig,
			NetDialContext:   netDialer.DialContext,
		}
	} else if tlsConfig != nil {
		dialer = &websocket.Dialer{
			Proxy:            websocket.DefaultDialer.Proxy,
			HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
			TLSClientConfig:  tlsConfig,
		}
	}

	conn, _, err := dialer.Dial(wsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to cook-agent at %s: %w", wsURL, err)
	}

	c := &Client{
		conn: conn,
	}

	return c, nil
}

func isEnvTrue(key string) bool {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return false
	}
	value = strings.ToLower(value)
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

// Close closes the connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// SetOutputHandler sets a callback for output messages
func (c *Client) SetOutputHandler(handler func(sessionID string, data []byte)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onOutput = handler
}

// send sends a message to the agent
func (c *Client) send(msg Message) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	encoded, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	return c.conn.WriteMessage(websocket.TextMessage, encoded)
}

// readMessage reads and parses a single message
func (c *Client) readMessage() (*Message, error) {
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

// CreateSession creates a new session in the agent
func (c *Client) CreateSession(id, command, workDir string, rows, cols int) error {
	if err := c.send(Message{
		Type:      MsgCreate,
		SessionID: id,
		Command:   command,
		WorkDir:   workDir,
		Rows:      rows,
		Cols:      cols,
	}); err != nil {
		return err
	}

	resp, err := c.readMessage()
	if err != nil {
		return err
	}

	if resp.Type == MsgError {
		return fmt.Errorf("agent error: %s", resp.Error)
	}

	return nil
}

// AttachSession attaches to an existing session
func (c *Client) AttachSession(id string) error {
	if err := c.send(Message{
		Type:      MsgAttach,
		SessionID: id,
	}); err != nil {
		return err
	}

	resp, err := c.readMessage()
	if err != nil {
		return err
	}

	if resp.Type == MsgError {
		return fmt.Errorf("agent error: %s", resp.Error)
	}

	return nil
}

// SendInput sends input to a session
func (c *Client) SendInput(sessionID string, data []byte) error {
	return c.send(Message{
		Type:      MsgInput,
		SessionID: sessionID,
		Data:      data,
	})
}

// Resize resizes a session's terminal
func (c *Client) Resize(sessionID string, rows, cols int) error {
	return c.send(Message{
		Type:      MsgResize,
		SessionID: sessionID,
		Rows:      rows,
		Cols:      cols,
	})
}

// ListSessions lists all active sessions
func (c *Client) ListSessions() ([]string, error) {
	if err := c.send(Message{Type: MsgList}); err != nil {
		return nil, err
	}

	resp, err := c.readMessage()
	if err != nil {
		return nil, err
	}

	if resp.Type == MsgError {
		return nil, fmt.Errorf("agent error: %s", resp.Error)
	}

	return resp.Sessions, nil
}

// ReadLoop reads messages from the agent and dispatches output.
// This should be called in a goroutine. It blocks until the connection is closed.
func (c *Client) ReadLoop() error {
	for {
		msg, err := c.readMessage()
		if err != nil {
			// WebSocket close is not an error
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return err
		}

		if msg.Type == MsgOutput {
			c.mu.Lock()
			handler := c.onOutput
			c.mu.Unlock()

			if handler != nil {
				handler(msg.SessionID, msg.Data)
			}
		}
	}
}
