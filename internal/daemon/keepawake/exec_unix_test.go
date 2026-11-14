//go:build !windows

package keepawake

import (
	"os/exec"
	"strings"
	"testing"
	"time"
)

// shBackend runs the test's own shell snippet instead of an inhibitor so the
// exec path (argv passthrough, stderr capture, process-group kill) is covered
// without touching host power state.
func shBackend(script string) *Backend {
	return &Backend{
		Name:   "sh",
		Detail: "sh",
		Args:   func(int) []string { return []string{"-c", script} },
	}
}

func TestExecStartReportsStderrOnExit(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	s := New(Options{Backend: shBackend("echo 'Failed to inhibit: boom' >&2; exit 3"), Grace: 2 * time.Second})
	st := s.SetEnabled(true)
	if st.State != StateBlocked || !strings.Contains(st.Reason, "boom") || !strings.Contains(st.Reason, "exit status 3") {
		t.Fatalf("blocked = %+v", st)
	}
}

func TestExecKillTerminatesProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh not available")
	}
	// The wrapper spawns a grandchild the same way systemd-inhibit wraps tail.
	s := New(Options{Backend: shBackend("sleep 30; exit 0"), Grace: 100 * time.Millisecond})
	st := s.SetEnabled(true)
	if st.State != StateOn {
		t.Fatalf("on = %+v", st)
	}
	s.mu.Lock()
	p := s.proc.(*execProcess)
	s.mu.Unlock()
	start := time.Now()
	s.SetEnabled(false)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("child did not exit after Kill")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("kill waited for sleep to finish")
	}
	if st := s.Status(); st.State != StateOff {
		t.Fatalf("off = %+v", st)
	}
	// Kill after exit is a no-op.
	if err := p.Kill(); err != nil {
		t.Fatalf("kill after exit: %v", err)
	}
}
