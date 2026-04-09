//go:build !windows

package session

import (
	"io"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
)

// PTY wraps a Unix pseudo-terminal.
type PTY struct {
	ptmx      *os.File
	cmd       *exec.Cmd
	closeOnce sync.Once
	exitDone  chan struct{} // closed when child process exits
	exitCode  uint32
}

// NewPTY creates a PTY session running the given command.
// If workDir is non-empty, the child process starts in that directory.
func NewPTY(command string, cols, rows int, workDir string) (*PTY, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		parts = []string{"/bin/sh"}
	}
	cmd := exec.Command(parts[0], parts[1:]...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Env = os.Environ()

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return nil, err
	}

	p := &PTY{
		ptmx:     ptmx,
		cmd:      cmd,
		exitDone: make(chan struct{}),
	}

	// Watch for child process exit.
	go func() {
		state, _ := cmd.Process.Wait()
		if state != nil {
			p.exitCode = uint32(state.ExitCode())
		}
		close(p.exitDone)
		p.Close()
		log.Printf("[pty] child process exited (code %d)", p.exitCode)
	}()

	return p, nil
}

// Reader returns a reader for the PTY output stream.
func (p *PTY) Reader() io.Reader {
	return p.ptmx
}

// Write sends data to the PTY input.
func (p *PTY) Write(data []byte) (int, error) {
	return p.ptmx.Write(data)
}

// Resize changes the PTY dimensions.
func (p *PTY) Resize(cols, rows int) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

// Close shuts down the PTY and child process with a timeout.
func (p *PTY) Close() error {
	var err error
	p.closeOnce.Do(func() {
		done := make(chan struct{})
		go func() {
			// Try graceful termination first.
			if p.cmd.Process != nil {
				p.cmd.Process.Signal(syscall.SIGTERM)
			}
			err = p.ptmx.Close()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			log.Printf("[pty] Close() timed out after 5s, killing child process")
			if p.cmd.Process != nil {
				p.cmd.Process.Kill()
			}
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
