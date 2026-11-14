package keepawake

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// stderrCap bounds the captured child stderr folded into Wait errors.
const stderrCap = 4096

// cappedBuffer keeps the first stderrCap bytes of child stderr.
type cappedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

// Write swallows (never short-writes) so io.Copy in os/exec keeps draining
// the child's stderr instead of closing the pipe and SIGPIPE-ing the child.
func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := stderrCap - b.buf.Len(); room > 0 {
		if len(p) > room {
			p = p[:room]
		}
		b.buf.Write(p)
	}
	return n, nil
}

func (b *cappedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// execProcess is the os/exec Process. argv is passed directly (no shell).
type execProcess struct {
	cmd    *exec.Cmd
	stderr *cappedBuffer
	done   chan struct{}
}

// execStart is the production Starter.
func execStart(path string, args []string) (Process, error) {
	cmd := exec.Command(path, args...)
	buf := &cappedBuffer{}
	cmd.Stderr = buf
	// The wrapped command (tail under systemd-inhibit) inherits the stderr
	// pipe. Without WaitDelay, a wrapper that dies on its own would leave Wait
	// blocked on the grandchild's pipe and Status stuck on a fake "on".
	cmd.WaitDelay = time.Second
	setProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &execProcess{cmd: cmd, stderr: buf, done: make(chan struct{})}, nil
}

func (p *execProcess) Wait() error {
	err := p.cmd.Wait()
	close(p.done)
	if err == nil {
		return nil
	}
	if msg := strings.TrimSpace(p.stderr.String()); msg != "" {
		return fmt.Errorf("%w: %s", err, msg)
	}
	return err
}

func (p *execProcess) Kill() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	return killTree(p.cmd)
}
