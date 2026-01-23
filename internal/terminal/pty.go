package terminal

import (
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"

	"github.com/creack/pty"
)

type PTY struct {
	cmd    *exec.Cmd
	pty    *os.File
	mu     sync.Mutex
	closed bool
}

// Start creates a new PTY and starts the command
func Start(cmd *exec.Cmd) (*PTY, error) {
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, err
	}

	return &PTY{
		cmd: cmd,
		pty: ptmx,
	}, nil
}

// Read reads from the PTY
func (p *PTY) Read(buf []byte) (int, error) {
	return p.pty.Read(buf)
}

// Write writes to the PTY
func (p *PTY) Write(data []byte) (int, error) {
	return p.pty.Write(data)
}

// Resize resizes the PTY
func (p *PTY) Resize(rows, cols uint16) error {
	return pty.Setsize(p.pty, &pty.Winsize{
		Rows: rows,
		Cols: cols,
	})
}

// Close closes the PTY and terminates the process
func (p *PTY) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		return nil
	}
	p.closed = true

	// Kill the process group
	if p.cmd.Process != nil {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
	}

	return p.pty.Close()
}

// Wait waits for the command to exit
func (p *PTY) Wait() error {
	return p.cmd.Wait()
}

// Fd returns the file descriptor of the PTY
func (p *PTY) Fd() uintptr {
	return p.pty.Fd()
}

// File returns the underlying file
func (p *PTY) File() *os.File {
	return p.pty
}

// Copy copies PTY output to a writer
func (p *PTY) Copy(w io.Writer) (int64, error) {
	return io.Copy(w, p.pty)
}

// PID returns the process ID
func (p *PTY) PID() int {
	if p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}
