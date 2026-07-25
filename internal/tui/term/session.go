// Package term runs interactive programs on a PTY and emulates their screen
// with vt10x so the strike TUI can embed editors (nvim/vim) in a pane or overlay.
package term

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/hinshun/vt10x"
)

// Session is a running PTY-backed process with a virtual terminal screen.
type Session struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	ptmx    *os.File
	term    vt10x.Terminal
	cols    int
	rows    int
	done    chan struct{}
	waitErr error
	closed  bool

	// notify is closed-or-signaled when the screen changes or the process exits.
	// Receivers should also watch Done().
	notify chan struct{}
}

// Start launches cmd attached to a PTY of the given size and begins pumping
// output into the VT emulator. cols/rows must be positive.
func Start(cmd *exec.Cmd, cols, rows int) (*Session, error) {
	if cols < 1 {
		cols = 80
	}
	if rows < 1 {
		rows = 24
	}
	if cmd == nil {
		return nil, fmt.Errorf("term: nil command")
	}
	// Ensure the child sees a useful TERM for curses UIs.
	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	cmd.Env = append(cmd.Env, "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Rows: uint16(rows),
		Cols: uint16(cols),
	})
	if err != nil {
		return nil, err
	}
	term := vt10x.New(vt10x.WithSize(cols, rows))
	s := &Session{
		cmd:    cmd,
		ptmx:   ptmx,
		term:   term,
		cols:   cols,
		rows:   rows,
		done:   make(chan struct{}),
		notify: make(chan struct{}, 1),
	}
	go s.readLoop()
	go s.waitLoop()
	return s, nil
}

// Done is closed when the child process has exited and the reader has finished.
func (s *Session) Done() <-chan struct{} { return s.done }

// Notify is a level-triggered redraw signal (buffered 1).
func (s *Session) Notify() <-chan struct{} { return s.notify }

// WaitErr is the process exit error after Done is closed (nil on success).
func (s *Session) WaitErr() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

// Size returns the current PTY dimensions.
func (s *Session) Size() (cols, rows int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cols, s.rows
}

// Write sends bytes to the PTY (keystrokes).
func (s *Session) Write(p []byte) (int, error) {
	s.mu.Lock()
	ptmx := s.ptmx
	closed := s.closed
	s.mu.Unlock()
	if closed || ptmx == nil {
		return 0, io.ErrClosedPipe
	}
	return ptmx.Write(p)
}

// Resize updates the VT grid and PTY window size.
func (s *Session) Resize(cols, rows int) error {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.ptmx == nil {
		return io.ErrClosedPipe
	}
	if cols == s.cols && rows == s.rows {
		return nil
	}
	s.term.Resize(cols, rows)
	if err := pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		return err
	}
	s.cols, s.rows = cols, rows
	s.ping()
	return nil
}

// Close terminates the child (if still running) and releases the PTY.
func (s *Session) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cmd := s.cmd
	ptmx := s.ptmx
	s.mu.Unlock()

	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Signal(os.Interrupt)
		// Give the editor a moment to exit cleanly, then force-kill.
		timer := time.AfterFunc(400*time.Millisecond, func() {
			_ = cmd.Process.Kill()
		})
		defer timer.Stop()
	}
	if ptmx != nil {
		_ = ptmx.Close()
	}
	<-s.done
	return s.WaitErr()
}

// Terminal returns the VT view for rendering. Callers must Lock/Unlock.
func (s *Session) Terminal() vt10x.Terminal {
	return s.term
}

// Lock locks the VT state for Cell/Cursor inspection.
func (s *Session) Lock() { s.term.Lock() }

// Unlock unlocks the VT state.
func (s *Session) Unlock() { s.term.Unlock() }

func (s *Session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 {
			// term.Write locks VT state itself — do not hold s.mu here.
			_, _ = s.term.Write(buf[:n])
			s.ping()
		}
		if err != nil {
			return
		}
	}
}

func (s *Session) waitLoop() {
	err := s.cmd.Wait()
	s.mu.Lock()
	s.waitErr = err
	// Close PTY so readLoop unblocks if it hasn't already.
	if s.ptmx != nil && !s.closed {
		_ = s.ptmx.Close()
	}
	s.closed = true
	s.mu.Unlock()
	s.ping()
	close(s.done)
}

func (s *Session) ping() {
	select {
	case s.notify <- struct{}{}:
	default:
	}
}
