package keepawake

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeProc is an in-memory Process: the test decides when Wait returns.
type fakeProc struct {
	exit   chan error
	killed chan struct{}
	once   sync.Once
}

func newFakeProc() *fakeProc {
	return &fakeProc{exit: make(chan error, 1), killed: make(chan struct{})}
}

func (p *fakeProc) Wait() error { return <-p.exit }
func (p *fakeProc) Kill() error {
	p.once.Do(func() { close(p.killed) })
	return nil
}

// fakeStarter records launches and hands out procs in order.
type fakeStarter struct {
	mu    sync.Mutex
	paths []string
	args  [][]string
	procs []*fakeProc
	err   error
}

func (f *fakeStarter) start(path string, args []string) (Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, path)
	f.args = append(f.args, args)
	if f.err != nil {
		return nil, f.err
	}
	p := newFakeProc()
	f.procs = append(f.procs, p)
	return p, nil
}

func (f *fakeStarter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func testBackend() *Backend {
	return &Backend{
		Name:   "fake-inhibit",
		Detail: "fake detail",
		Args:   func(pid int) []string { return []string{"--pid", strconv.Itoa(pid)} },
	}
}

func found(string) (string, error) { return "/usr/bin/fake-inhibit", nil }

func newTestService(t *testing.T, fs *fakeStarter) *Service {
	t.Helper()
	return New(Options{
		Backend:  testBackend(),
		Start:    fs.start,
		LookPath: found,
		PID:      7,
		Grace:    50 * time.Millisecond,
	})
}

func waitState(t *testing.T, s *Service, want State) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		st := s.Status()
		if st.State == want {
			return st
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %+v, want %s", st, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestBackendForPerOS(t *testing.T) {
	linux, reason := backendFor("linux")
	if reason != "" || linux.Name != "systemd-inhibit" {
		t.Fatalf("linux = %+v %q", linux, reason)
	}
	args := strings.Join(linux.Args(4242), " ")
	for _, want := range []string{"--what=sleep:idle", "--mode=block", "--who=strike", "tail --pid=4242 -f /dev/null"} {
		if !strings.Contains(args, want) {
			t.Errorf("linux args %q missing %q", args, want)
		}
	}

	darwin, reason := backendFor("darwin")
	if reason != "" || darwin.Name != "caffeinate" {
		t.Fatalf("darwin = %+v %q", darwin, reason)
	}
	if got := strings.Join(darwin.Args(99), " "); got != "-s -w 99" {
		t.Errorf("darwin args = %q", got)
	}

	for _, goos := range []string{"windows", "plan9"} {
		b, reason := backendFor(goos)
		if b.Name != "" || reason == "" {
			t.Errorf("%s: backend = %+v reason = %q, want unsupported", goos, b, reason)
		}
	}
}

func TestUnsupportedWhenBinaryMissing(t *testing.T) {
	fs := &fakeStarter{}
	s := New(Options{
		Backend:  testBackend(),
		Start:    fs.start,
		LookPath: func(string) (string, error) { return "", errors.New("not found") },
	})
	st := s.Status()
	if st.Supported || st.State != StateUnsupported || !strings.Contains(st.Reason, "not found in PATH") {
		t.Fatalf("status = %+v", st)
	}
	st = s.SetEnabled(true)
	if !st.Enabled || st.State != StateUnsupported || fs.count() != 0 {
		t.Fatalf("enabled on unsupported = %+v starts=%d", st, fs.count())
	}
}

func TestUnsupportedWhenNoBackend(t *testing.T) {
	s := New(Options{Backend: &Backend{}, Unsupported: "nope here"})
	if st := s.Status(); st.State != StateUnsupported || st.Reason != "nope here" || st.Supported {
		t.Fatalf("status = %+v", st)
	}
}

func TestEnableDisableHoldsAndReleases(t *testing.T) {
	fs := &fakeStarter{}
	s := newTestService(t, fs)

	if st := s.Status(); st.State != StateOff || !st.Supported || st.Enabled || st.Backend != "fake-inhibit" {
		t.Fatalf("initial = %+v", st)
	}
	st := s.SetEnabled(true)
	if st.State != StateOn || !st.Enabled || st.Reason != "fake detail" || st.Since == "" {
		t.Fatalf("on = %+v", st)
	}
	if _, err := time.Parse(time.RFC3339, st.Since); err != nil {
		t.Fatalf("since %q: %v", st.Since, err)
	}
	if fs.count() != 1 || fs.paths[0] != "fake-inhibit" || strings.Join(fs.args[0], " ") != "--pid 7" {
		t.Fatalf("start = %v %v", fs.paths, fs.args)
	}
	// Re-enabling while held is idempotent.
	if st := s.SetEnabled(true); st.State != StateOn || fs.count() != 1 {
		t.Fatalf("idempotent = %+v starts=%d", st, fs.count())
	}
	raw, _ := json.Marshal(st)
	for _, want := range []string{`"enabled":true`, `"supported":true`, `"state":"on"`, `"backend":"fake-inhibit"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("json %s missing %s", raw, want)
		}
	}

	st = s.SetEnabled(false)
	if st.State != StateOff || st.Enabled || st.Since != "" || st.Reason != "" {
		t.Fatalf("off = %+v", st)
	}
	select {
	case <-fs.procs[0].killed:
	default:
		t.Fatal("child not killed on disable")
	}
	// Close with nothing held is a no-op.
	s.Close()
	s.Close()
}

func TestCloseReleasesButKeepsPreference(t *testing.T) {
	fs := &fakeStarter{}
	s := newTestService(t, fs)
	s.SetEnabled(true)
	s.Close()
	select {
	case <-fs.procs[0].killed:
	default:
		t.Fatal("child not killed on close")
	}
	if st := s.Status(); !st.Enabled {
		t.Fatalf("close cleared preference: %+v", st)
	}
}

func TestBlockedWhenStartFails(t *testing.T) {
	fs := &fakeStarter{err: errors.New("permission denied")}
	s := newTestService(t, fs)
	st := s.SetEnabled(true)
	if st.State != StateBlocked || !strings.Contains(st.Reason, "permission denied") || !st.Enabled {
		t.Fatalf("blocked = %+v", st)
	}
	// Toggling off clears the failure; on retries.
	if st := s.SetEnabled(false); st.State != StateOff || st.Reason != "" {
		t.Fatalf("off after blocked = %+v", st)
	}
	fs.mu.Lock()
	fs.err = nil
	fs.mu.Unlock()
	if st := s.SetEnabled(true); st.State != StateOn {
		t.Fatalf("retry = %+v", st)
	}
}

func TestBlockedWhenChildExitsWithinGrace(t *testing.T) {
	fs := &fakeStarter{}
	s := newTestService(t, fs)
	// Pre-arm the exit so Wait returns as soon as the watcher starts.
	go func() {
		for fs.count() == 0 {
			time.Sleep(time.Millisecond)
		}
		fs.mu.Lock()
		p := fs.procs[0]
		fs.mu.Unlock()
		p.exit <- errors.New("exit status 1: Failed to inhibit: no logind")
	}()
	st := s.SetEnabled(true)
	if st.State != StateBlocked || !strings.Contains(st.Reason, "no logind") || !strings.Contains(st.Reason, "fake-inhibit exited") {
		t.Fatalf("blocked = %+v", st)
	}
}

func TestBlockedWhenChildExitsLater(t *testing.T) {
	fs := &fakeStarter{}
	s := newTestService(t, fs)
	if st := s.SetEnabled(true); st.State != StateOn {
		t.Fatalf("on = %+v", st)
	}
	fs.procs[0].exit <- nil
	st := waitState(t, s, StateBlocked)
	if st.Reason != "fake-inhibit exited" || !st.Enabled {
		t.Fatalf("blocked = %+v", st)
	}
}

func TestStaleExitAfterDisableDoesNotFlipState(t *testing.T) {
	fs := &fakeStarter{}
	s := newTestService(t, fs)
	s.SetEnabled(true)
	old := fs.procs[0]
	s.SetEnabled(false)
	if st := s.SetEnabled(true); st.State != StateOn || fs.count() != 2 {
		t.Fatalf("re-enable = %+v starts=%d", st, fs.count())
	}
	// The first child's Wait now returns; it must not touch the new assertion.
	old.exit <- errors.New("killed")
	time.Sleep(20 * time.Millisecond)
	if st := s.Status(); st.State != StateOn {
		t.Fatalf("stale exit flipped state: %+v", st)
	}
	s.Close()
}

func TestCappedBufferNeverShortWrites(t *testing.T) {
	b := &cappedBuffer{}
	big := strings.Repeat("x", stderrCap+100)
	n, err := b.Write([]byte(big))
	if err != nil || n != len(big) {
		t.Fatalf("Write = %d, %v; want %d, nil (short write would abort io.Copy)", n, err, len(big))
	}
	if got := len(b.String()); got != stderrCap {
		t.Fatalf("captured %d bytes, want cap %d", got, stderrCap)
	}
	// Writes past the cap are swallowed, still reporting full length.
	if n, err := b.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("overflow Write = %d, %v", n, err)
	}
}
