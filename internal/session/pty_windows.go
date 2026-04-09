//go:build windows

package session

import (
	"context"
	"io"
	"log"
	"sync"
	"time"

	"github.com/UserExistsError/conpty"
)

// PTY wraps a Windows ConPTY pseudo-console.
type PTY struct {
	cpty      *conpty.ConPty
	closeOnce sync.Once
	exitDone  chan struct{} // closed when child process exits
	exitCode  uint32
}

// NewPTY creates a ConPTY session running the given command.
// If workDir is non-empty, the child process starts in that directory.
func NewPTY(command string, cols, rows int, workDir string) (*PTY, error) {
	opts := []conpty.ConPtyOption{conpty.ConPtyDimensions(cols, rows)}
	if workDir != "" {
		opts = append(opts, conpty.ConPtyWorkDir(workDir))
	}
	cpty, err := conpty.Start(command, opts...)
	if err != nil {
		return nil, err
	}
	p := &PTY{
		cpty:     cpty,
		exitDone: make(chan struct{}),
	}
	// Watch for child process exit. When it exits, close the ConPTY
	// to unblock any pending Read() calls on the output pipe.
	go func() {
		code, _ := cpty.Wait(context.Background())
		p.exitCode = code
		close(p.exitDone)
		p.Close()
		log.Printf("[pty] child process exited (code %d)", code)
	}()
	return p, nil
}

// Reader returns a reader for the PTY output stream.
func (p *PTY) Reader() io.Reader {
	return p.cpty
}

// Write sends data to the PTY input.
func (p *PTY) Write(data []byte) (int, error) {
	return p.cpty.Write(data)
}

// Resize changes the PTY dimensions.
func (p *PTY) Resize(cols, rows int) error {
	return p.cpty.Resize(cols, rows)
}

// Close shuts down the ConPTY and child process with a timeout.
// If cpty.Close() blocks for more than 5 seconds (e.g. hung child process),
// we log a warning and return. Safe to call multiple times.
func (p *PTY) Close() error {
	var err error
	p.closeOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			err = p.cpty.Close()
			close(done)
		}()
		select {
		case <-done:
			// Normal close completed.
		case <-time.After(5 * time.Second):
			log.Printf("[pty] Close() timed out after 5s, child process may be hung")
			// The goroutine with cpty.Close() will eventually complete or leak.
			// This prevents Destroy() from blocking indefinitely.
		}
	})
	return err
}

// ExitCode returns the child process exit code. Only valid after exitDone is closed.
func (p *PTY) ExitCode() uint32 {
	return p.exitCode
}

// Done returns a channel that is closed when the child process exits.
func (p *PTY) Done() <-chan struct{} {
	return p.exitDone
}
