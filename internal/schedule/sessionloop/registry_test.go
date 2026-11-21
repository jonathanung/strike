package sessionloop

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
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
	r := New(func(_ context.Context, _, job string) error {
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

func TestLoopJSONOmitsUnsetFields(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	l, err := r.Start("1m", "a")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"lastRunAt", "lastError"} {
		if strings.Contains(string(raw), key) {
			t.Fatalf("unfired loop JSON has %s: %s", key, raw)
		}
	}
	h.last().f()
	raw, _ = json.Marshal(r.List()[0])
	if !strings.Contains(string(raw), "lastRunAt") {
		t.Fatalf("fired loop JSON lacks lastRunAt: %s", raw)
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
	r := New(func(context.Context, string, string) error {
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
	r := New(func(_ context.Context, _, job string) error {
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

// TestFireSubmitsLoopID covers #1827: each firing hands the session its own
// loop id so the transcript can badge it.
func TestFireSubmitsLoopID(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var got []string
	var timers []*fakeTimer
	r := New(func(_ context.Context, id, job string) error {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, id+":"+job)
		return nil
	})
	r.after = func(d time.Duration, f func()) timer {
		mu.Lock()
		defer mu.Unlock()
		ft := &fakeTimer{d: d, f: f}
		timers = append(timers, ft)
		return ft
	}
	for _, job := range []string{"first job", "second job"} {
		if _, err := r.Start("1m", job); err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	fire := []func(){timers[1].f, timers[0].f}
	mu.Unlock()
	for _, f := range fire {
		f()
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"l2:second job", "l1:first job"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("submitted = %v, want %v", got, want)
	}
}

// changeLog records SetOnChange reports as "reason id [ids]".
type changeLog struct {
	mu  sync.Mutex
	got []string
}

func (c *changeLog) hook(ch Change) {
	ids := make([]string, 0, len(ch.Loops))
	for _, l := range ch.Loops {
		ids = append(ids, l.ID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, ch.Reason+" "+ch.LoopID+" ["+strings.Join(ids, ",")+"]")
}

func (c *changeLog) take() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.got
	c.got = nil
	return out
}

// TestOnChangeReportsEveryChange (#1944): start, stop, a firing, a failed
// firing, stop-all, and close each report once with the list after the
// change; rejected calls and no-op stops report nothing.
func TestOnChangeReportsEveryChange(t *testing.T) {
	t.Parallel()
	mustStart := func(t *testing.T, r *Registry, interval, job string) {
		t.Helper()
		if _, err := r.Start(interval, job); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name string
		run  func(t *testing.T, r *Registry, h *harness)
		want []string
	}{
		{
			name: "start",
			run:  func(t *testing.T, r *Registry, _ *harness) { mustStart(t, r, "1m", "a"); mustStart(t, r, "2m", "b") },
			want: []string{"start l1 [l1]", "start l2 [l1,l2]"},
		},
		{
			name: "rejected start reports nothing",
			run: func(t *testing.T, r *Registry, _ *harness) {
				if _, err := r.Start("1ms", "a"); err == nil {
					t.Fatal("want grammar error")
				}
			},
			want: nil,
		},
		{
			name: "stop one",
			run: func(t *testing.T, r *Registry, _ *harness) {
				mustStart(t, r, "1m", "a")
				mustStart(t, r, "1m", "b")
				if err := r.Stop("l1"); err != nil {
					t.Fatal(err)
				}
				if err := r.Stop("l9"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("stop unknown = %v", err)
				}
			},
			want: []string{"start l1 [l1]", "start l2 [l1,l2]", "stop l1 [l2]"},
		},
		{
			name: "firing",
			run: func(t *testing.T, r *Registry, h *harness) {
				mustStart(t, r, "1m", "a")
				h.last().f()
			},
			want: []string{"start l1 [l1]", "fire l1 [l1]"},
		},
		{
			name: "failed firing",
			run: func(t *testing.T, r *Registry, h *harness) {
				mustStart(t, r, "1m", "a")
				h.mu.Lock()
				h.err = errors.New("queue full")
				h.mu.Unlock()
				h.last().f()
			},
			want: []string{"start l1 [l1]", "error l1 [l1]"},
		},
		{
			name: "stale tick reports nothing",
			run: func(t *testing.T, r *Registry, h *harness) {
				mustStart(t, r, "1m", "a")
				tick := h.last().f
				if err := r.Stop("l1"); err != nil {
					t.Fatal(err)
				}
				tick()
			},
			want: []string{"start l1 [l1]", "stop l1 []"},
		},
		{
			name: "stop all",
			run: func(t *testing.T, r *Registry, _ *harness) {
				mustStart(t, r, "1m", "a")
				mustStart(t, r, "1m", "b")
				if n := r.StopAll(); n != 2 {
					t.Fatalf("stopped %d", n)
				}
				if n := r.StopAll(); n != 0 {
					t.Fatalf("second stop-all stopped %d", n)
				}
			},
			want: []string{"start l1 [l1]", "start l2 [l1,l2]", "stop  []"},
		},
		{
			name: "close",
			run: func(t *testing.T, r *Registry, _ *harness) {
				mustStart(t, r, "1m", "a")
				r.Close()
				r.Close()
				if _, err := r.Start("1m", "b"); !errors.Is(err, ErrClosed) {
					t.Fatalf("start after close = %v", err)
				}
			},
			want: []string{"start l1 [l1]", "close  []"},
		},
		{
			name: "close with no loops reports nothing",
			run:  func(t *testing.T, r *Registry, _ *harness) { r.Close() },
			want: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, h := newHarness(t)
			log := &changeLog{}
			r.SetOnChange(log.hook)
			tc.run(t, r, h)
			got := log.take()
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("changes = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOnChangeMayReadTheRegistry: the hook runs outside the registry lock,
// so calling List or Len from it must not deadlock.
func TestOnChangeMayReadTheRegistry(t *testing.T) {
	t.Parallel()
	r, h := newHarness(t)
	lens := make(chan int, 4)
	r.SetOnChange(func(Change) { lens <- r.Len() + len(r.List()) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := r.Start("1m", "a"); err != nil {
			t.Error(err)
			return
		}
		h.last().f()
		_ = r.Stop("l1")
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("hook calling List deadlocked")
	}
	want := []int{2, 2, 0}
	for i, w := range want {
		if got := <-lens; got != w {
			t.Fatalf("report %d saw %d loops (Len+List), want %d", i, got, w)
		}
	}
}

// TestOnChangeLastReportIsNewest: racing changes may report in any order,
// but the final report always carries the final list.
func TestOnChangeLastReportIsNewest(t *testing.T) {
	t.Parallel()
	r, _ := newHarness(t)
	var mu sync.Mutex
	var last []Loop
	r.SetOnChange(func(c Change) {
		mu.Lock()
		last = c.Loops
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := r.Start("1m", "job"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(last) != 8 || len(r.List()) != 8 {
		t.Fatalf("last report has %d loops, registry %d", len(last), len(r.List()))
	}
}
