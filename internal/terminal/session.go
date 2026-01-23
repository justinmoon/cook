package terminal

import (
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	// DefaultReplayBufferBytes is how much terminal output we keep for replay on reconnect.
	// Needs to be big enough to reconstruct a TUI screen after a disconnect.
	DefaultReplayBufferBytes = 8 * 1024 * 1024

	// DefaultSubscriberBuffer is the per-subscriber channel buffer.
	// If a subscriber can't keep up, we drop output for that subscriber (read loop never blocks).
	DefaultSubscriberBuffer = 256
)

type Session struct {
	key string
	pty *PTY

	mu        sync.Mutex
	out       ringBuffer
	subs      map[int]chan []byte
	nextSubID int

	closeOnce sync.Once
	closed    bool
	closeErr  error
	closedAt  time.Time

	startedAt time.Time
}

func newSession(key string, pty *PTY) *Session {
	s := &Session{
		key:       key,
		pty:       pty,
		out:       newRingBuffer(DefaultReplayBufferBytes),
		subs:      make(map[int]chan []byte),
		startedAt: time.Now(),
	}
	s.startPumps()
	return s
}

func (s *Session) Key() string { return s.key }

func (s *Session) PID() int { return s.pty.PID() }

func (s *Session) StartedAt() time.Time { return s.startedAt }

func (s *Session) ClosedAt() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		return time.Time{}, false
	}
	return s.closedAt, true
}

func (s *Session) CloseErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeErr
}

func (s *Session) Snapshot() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.out.bytes()
}

// Subscribe returns a snapshot of buffered output and a channel that receives future output.
// The snapshot + stream together allow a disconnected client to reconstruct the current screen.
func (s *Session) Subscribe() (subID int, snapshot []byte, ch <-chan []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	snapshot = s.out.bytes()

	subID = s.nextSubID
	s.nextSubID++

	c := make(chan []byte, DefaultSubscriberBuffer)
	if s.closed {
		close(c)
		return subID, snapshot, c
	}
	s.subs[subID] = c
	return subID, snapshot, c
}

func (s *Session) Unsubscribe(subID int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, ok := s.subs[subID]
	if !ok {
		return
	}
	delete(s.subs, subID)
	close(c)
}

func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return 0, fmt.Errorf("terminal session closed")
	}
	return s.pty.Write(p)
}

func (s *Session) Resize(rows, cols uint16) error {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return fmt.Errorf("terminal session closed")
	}
	return s.pty.Resize(rows, cols)
}

func (s *Session) Close() error {
	// Close underlying PTY; pumps will observe EOF/error and finalize.
	if err := s.pty.Close(); err != nil {
		s.closeWithErr(err)
		return err
	}
	s.closeWithErr(nil)
	return nil
}

func (s *Session) startPumps() {
	go s.pumpOutput()
	go s.waitProcess()
}

func (s *Session) pumpOutput() {
	buf := make([]byte, 32*1024)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			chunk := append([]byte(nil), buf[:n]...)
			s.mu.Lock()
			s.out.appendBytes(chunk)
			for _, sub := range s.subs {
				select {
				case sub <- chunk:
				default:
					// Drop for slow subscriber. We keep full replay in s.out.
				}
			}
			s.mu.Unlock()
		}

		if err != nil {
			if err == io.EOF {
				s.closeWithErr(nil)
				return
			}
			s.closeWithErr(err)
			return
		}
	}
}

func (s *Session) waitProcess() {
	err := s.pty.Wait()
	// Ensure PTY is closed to stop reads if the process exited but the PTY is still open.
	s.pty.Close()
	s.closeWithErr(err)
}

func (s *Session) closeWithErr(err error) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.closedAt = time.Now()
		s.closeErr = err
		for id, sub := range s.subs {
			delete(s.subs, id)
			close(sub)
		}
		s.mu.Unlock()
	})

	// If we were closed without an error (e.g., PTY EOF), but we later learn the
	// process exit status, keep the first non-nil error for debugging.
	if err != nil {
		s.mu.Lock()
		if s.closeErr == nil {
			s.closeErr = err
		}
		s.mu.Unlock()
	}
}
