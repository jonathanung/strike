package sessionloop

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"
)

type fakeTimer struct {
	d       time.Duration
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { t.stopped = true; return true }

type harness struct {
	mu     sync.Mutex
	timers []*fakeTimer
	sent   []string
	err    error
	now    time.Time
}

func newHarness(t *testing.T) (*Registry, *harness) {
	t.Helper()
	h := &harness{now: time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)}
	r := New(func(_ context.Context, job string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.err != nil {
			return h.err
		}
		h.sent = append(h.sent, job)
		return nil
	})
	r.now = func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now }
	r.after = func(d time.Duration, f func()) timer {
		h.mu.Lock()
		defer h.mu.Unlock()
		ft := &fakeTimer{d: d, f: f}
		h.timers = append(h.timers, ft)
		return ft
	}
	return r, h
}

// last returns the newest armed timer.
func (h *harness) last() *fakeTimer {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.timers[len(h.timers)-1]
}

func (h *harness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

func TestStartListFireRearm(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	l, err := r.Start("15m", "  check the pipeline ")
	if err != nil {
		t.Fatal(err)
	}
	if l.ID != "l1" || l.Interval != "15m" || l.IntervalMs != (15*time.Minute).Milliseconds() || l.Job != "check the pipeline" {
		t.Fatalf("loop = %+v", l)
	}
	if !l.NextAt.Equal(h.now.Add(15 * time.Minute)) {
		t.Fatalf("nextAt = %v", l.NextAt)
	}
	first := h.last()
	if first.d != 15*time.Minute {
		t.Fatalf("armed %v", first.d)
	}
	h.advance(15 * time.Minute)
	first.f()
	if len(h.sent) != 1 || h.sent[0] != "check the pipeline" {
		t.Fatalf("sent = %v", h.sent)
	}
	got := r.List()
	if len(got) != 1 || got[0].Runs != 1 || !got[0].LastRunAt.Equal(h.now) || !got[0].NextAt.Equal(h.now.Add(15*time.Minute)) {
		t.Fatalf("after fire = %+v", got)
	}
	if h.last() == first {
		t.Fatal("fire did not re-arm")
	}
	h.last().f()
	if len(h.sent) != 2 || r.List()[0].Runs != 2 {
		t.Fatalf("second fire: sent=%v list=%+v", h.sent, r.List())
	}
}

func TestStartRejectsGrammar(t *testing.T) {
	t.Parallel()
	r, _ := newHarness(t)
	for _, tc := range []struct{ interval, job string }{
		{"500ms", "sub-second"},
		{"169h", "over a week"},
		{"soon", "not a duration"},
		{"15m", "   "},
		{"15m", "bell\x07"},
	} {
		if _, err := r.Start(tc.interval, tc.job); !errors.Is(err, ErrInvalid) {
			t.Errorf("Start(%q,%q) err=%v, want ErrInvalid", tc.interval, tc.job, err)
		}
	}
	if r.Len() != 0 {
		t.Fatalf("rejected starts left %d loops", r.Len())
	}
}

func TestLimitSeventeenthRejected(t *testing.T) {
	t.Parallel()
	r, _ := newHarness(t)
	for i := 0; i < 16; i++ {
		if _, err := r.Start("1m", "job "+strconv.Itoa(i)); err != nil {
			t.Fatalf("start %d: %v", i, err)
		}
	}
	if _, err := r.Start("1m", "one too many"); !errors.Is(err, ErrLimit) {
		t.Fatalf("17th err=%v, want ErrLimit", err)
	}
	// Freeing a slot allows a new loop; ids are never reused.
	if err := r.Stop("l3"); err != nil {
		t.Fatal(err)
	}
	l, err := r.Start("1m", "after stop")
	if err != nil || l.ID != "l17" {
		t.Fatalf("restart = %+v %v", l, err)
	}
	list := r.List()
	if len(list) != 16 || list[0].ID != "l1" || list[15].ID != "l17" {
		t.Fatalf("order = %v..%v (%d)", list[0].ID, list[len(list)-1].ID, len(list))
	}
}

func TestStopInvalidatesInFlightTick(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	if _, err := r.Start("10s", "a"); err != nil {
		t.Fatal(err)
	}
	tm := h.last()
	if err := r.Stop("l1"); err != nil {
		t.Fatal(err)
	}
	if !tm.stopped {
		t.Fatal("timer not stopped")
	}
	tm.f() // a tick that raced Stop must not inject
	if len(h.sent) != 0 {
		t.Fatalf("stopped loop fired: %v", h.sent)
	}
	if err := r.Stop("l1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second stop err=%v", err)
	}
}

func TestStopAll(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	for _, j := range []string{"a", "b", "c"} {
		if _, err := r.Start("1m", j); err != nil {
			t.Fatal(err)
		}
	}
	if n := r.StopAll(); n != 3 {
		t.Fatalf("StopAll = %d", n)
	}
	for _, tm := range h.timers {
		if !tm.stopped {
			t.Fatal("timer left running")
		}
		tm.f()
	}
	if len(h.sent) != 0 || r.Len() != 0 {
		t.Fatalf("after StopAll sent=%v len=%d", h.sent, r.Len())
	}
	if n := r.StopAll(); n != 0 {
		t.Fatalf("empty StopAll = %d", n)
	}
}

func TestCloseStopsAndRejects(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	if _, err := r.Start("1m", "a"); err != nil {
		t.Fatal(err)
	}
	tm := h.last()
	r.Close()
	r.Close() // idempotent
	tm.f()
	if len(h.sent) != 0 || r.Len() != 0 {
		t.Fatalf("closed registry fired or kept loops: %v %d", h.sent, r.Len())
	}
	if _, err := r.Start("1m", "b"); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after close err=%v", err)
	}
	var nilReg *Registry
	nilReg.Close()
	if nilReg.Len() != 0 {
		t.Fatal("nil Len")
	}
}

func TestSubmitErrorKeepsScheduleAndRecords(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	if _, err := r.Start("1m", "a"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	h.err = errors.New("live session closed")
	h.mu.Unlock()
	first := h.last()
	first.f()
	got := r.List()[0]
	if got.Runs != 0 || got.LastError != "live session closed" {
		t.Fatalf("after failed fire = %+v", got)
	}
	if h.last() == first {
		t.Fatal("failed fire did not re-arm")
	}
	h.mu.Lock()
	h.err = nil
	h.mu.Unlock()
	h.last().f()
	if got := r.List()[0]; got.Runs != 1 || got.LastError != "" {
		t.Fatalf("recovered fire = %+v", got)
	}
}

func TestStopDuringSubmitDoesNotRearm(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	entered := make(chan struct{})
	r := New(func(context.Context, string) error {
		close(entered)
		<-release
		return nil
	})
	var timers []*fakeTimer
	var mu sync.Mutex
	r.after = func(d time.Duration, f func()) timer {
		mu.Lock()
		defer mu.Unlock()
		ft := &fakeTimer{d: d, f: f}
		timers = append(timers, ft)
		return ft
	}
	if _, err := r.Start("1s", "slow"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	tick := timers[0].f
	mu.Unlock()
	done := make(chan struct{})
	go func() { tick(); close(done) }()
	<-entered
	// List must not block on an in-flight submit.
	if r.Len() != 1 {
		t.Fatal("loop missing during submit")
	}
	if err := r.Stop("l1"); err != nil {
		t.Fatal(err)
	}
	close(release)
	<-done
	mu.Lock()
	defer mu.Unlock()
	if len(timers) != 1 {
		t.Fatalf("stopped loop re-armed (%d timers)", len(timers))
	}
}

func TestRealTimerFires(t *testing.T) {
	t.Parallel()
	got := make(chan string, 1)
	r := New(func(_ context.Context, job string) error {
		select {
		case got <- job:
		default:
		}
		return nil
	})
	defer r.Close()
	r.after = func(_ time.Duration, f func()) timer { return time.AfterFunc(time.Millisecond, f) }
	if _, err := r.Start("1s", "tick"); err != nil {
		t.Fatal(err)
	}
	select {
	case j := <-got:
		if j != "tick" {
			t.Fatalf("job = %q", j)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("real timer never fired")
	}
}
