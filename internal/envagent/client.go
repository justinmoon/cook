// Package envagent provides a client for communicating with cook-agent
// running inside execution environments.
package envagent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"sync"
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

// Client connects to a cook-agent instance
type Client struct {
	conn     net.Conn
	mu       sync.Mutex
	reader   *bufio.Reader
	onOutput func(sessionID string, data []byte)
}

// Dial connects to a cook-agent at the given address
func Dial(addr string) (*Client, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to cook-agent: %w", err)
	}

	c := &Client{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}

	return c, nil
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
	encoded = append(encoded, '\n')

	_, err = c.conn.Write(encoded)
	return err
}

// readMessage reads and parses a single message
func (c *Client) readMessage() (*Message, error) {
	line, err := c.reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var msg Message
	if err := json.Unmarshal(line, &msg); err != nil {
		return nil, err
	}

	return &msg, nil
}

// CreateSession creates a new session in the agent
func (c *Client) CreateSession(id, command, workDir string) error {
	if err := c.send(Message{
		Type:      MsgCreate,
		SessionID: id,
		Command:   command,
		WorkDir:   workDir,
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
			if err == io.EOF {
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

// Conn returns the underlying connection for advanced use cases
func (c *Client) Conn() net.Conn {
	return c.conn
}
