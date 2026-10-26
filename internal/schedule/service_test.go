package schedule

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/rootstart"
)

type recordingStarter struct {
	mu   sync.Mutex
	reqs []rootstart.Request
	err  error
	id   string
}

func (r *recordingStarter) StartRoot(ctx context.Context, req rootstart.Request) (rootstart.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
	if r.err != nil {
		return rootstart.Result{}, r.err
	}
	id := r.id
	if id == "" {
		id = "sess-1"
	}
	return rootstart.Result{SessionID: id}, nil
}

func (r *recordingStarter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.reqs)
}

func (r *recordingStarter) last() rootstart.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.reqs[len(r.reqs)-1]
}

func promptSpec(name, every string) Spec {
	return Spec{
		Name:   name,
		Kind:   KindInterval,
		Every:  every,
		Source: SourcePrompt,
		Prompt: "check the tree",
	}
}

func TestCreateDefaultsSafestAndPersists(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(promptSpec("Nightly", "1h"))
	if err != nil {
		t.Fatal(err)
	}
	if sch.Overlap != OverlapSkip || sch.Missed != MissedSkip || sch.Timezone != "UTC" {
		t.Fatalf("defaults %+v", sch)
	}
	if !sch.Enabled || !sch.NextFire.Equal(now.Add(time.Hour)) {
		t.Fatalf("next %s enabled %v", sch.NextFire, sch.Enabled)
	}
	info, err := os.Stat(filepath.Join(dir, "schedules.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %o", info.Mode().Perm())
	}
	reopen, err := Open(dir, Hooks{Now: func() time.Time { return now }}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, ok := reopen.Get(sch.ID)
	if !ok || got.Name != "Nightly" {
		t.Fatalf("reopen %+v %v", got, ok)
	}
}

func TestRestartMissedSkipDoesNotStart(t *testing.T) {
	dir := t.TempDir()
	start := &recordingStarter{}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }, Starter: start}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(promptSpec("late", "1h"))
	if err != nil {
		t.Fatal(err)
	}
	// Pretend the process died with a run in flight and a fire an hour overdue.
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.NextFire = now.Add(-2 * time.Hour)
		s.LastStatus = StatusStarted
		s.LastSessionID = "old"
		addRun(s, Run{ID: "run_old", ScheduleID: s.ID, Status: StatusStarted, FiredAt: now.Add(-3 * time.Hour)})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	reopen, err := Open(dir, Hooks{Now: func() time.Time { return now }, Starter: start}, dir)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := reopen.Get(sch.ID)
	if got.LastStatus != StatusInterrupted {
		t.Fatalf("restart status %q", got.LastStatus)
	}
	if len(got.Runs) == 0 || got.Runs[0].Status != StatusInterrupted {
		t.Fatalf("run not interrupted: %+v", got.Runs)
	}
	reopen.Tick(context.Background(), true)
	if start.count() != 0 {
		t.Fatalf("missed=skip started %d sessions", start.count())
	}
	after, _ := reopen.Get(sch.ID)
	if after.LastStatus != StatusMissed {
		t.Fatalf("status %q", after.LastStatus)
	}
	if !after.NextFire.After(now) {
		t.Fatalf("next %s not after now", after.NextFire)
	}
}

func TestRestartMissedOnceStartsOne(t *testing.T) {
	dir := t.TempDir()
	start := &recordingStarter{id: "caught"}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }, Starter: start}, dir)
	if err != nil {
		t.Fatal(err)
	}
	spec := promptSpec("catch", "30m")
	missed := MissedOnce
	spec.Missed = missed
	sch, err := svc.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.NextFire = now.Add(-3 * time.Hour)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(context.Background(), true)
	if start.count() != 1 {
		t.Fatalf("starts %d", start.count())
	}
	req := start.last()
	if req.PermissionMode != rootstart.SafestMode || !req.Unattended || req.Source != rootstart.SourceSchedule {
		t.Fatalf("request %+v", req)
	}
	if req.Prompt != "check the tree" {
		t.Fatalf("prompt %q", req.Prompt)
	}
	after, _ := svc.Get(sch.ID)
	if after.LastSessionID != "caught" || after.LastStatus != StatusStarted {
		t.Fatalf("after %+v", after)
	}
	if !after.NextFire.After(now) {
		t.Fatalf("catch-up left next due: %s", after.NextFire)
	}
	// A second startup tick must not fire the backlog again.
	svc.Tick(context.Background(), true)
	if start.count() != 1 {
		t.Fatalf("second startup starts %d", start.count())
	}
}

func TestOverlapPolicies(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	busy := true
	start := &recordingStarter{}
	svc, err := Open(dir, Hooks{
		Now:     func() time.Time { return now },
		Starter: start,
		Busy:    func(string) bool { return busy },
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	makeDue := func(name, overlap string) Schedule {
		t.Helper()
		spec := promptSpec(name, "1h")
		spec.Overlap = overlap
		sch, err := svc.Create(spec)
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.store.Mutate(sch.ID, func(s *Schedule) error {
			s.NextFire = now.Add(-time.Minute)
			s.LastStatus = StatusStarted
			s.LastSessionID = "live"
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	skipped := makeDue("skip", OverlapSkip)
	queued := makeDue("queue", OverlapQueue)
	allowed := makeDue("allow", OverlapAllow)

	svc.Tick(context.Background(), false)
	if start.count() != 1 || start.last().SourceID != allowed.ID {
		t.Fatalf("allow should be the only start, got %d %+v", start.count(), start.reqs)
	}
	sk, _ := svc.Get(skipped.ID)
	if sk.LastStatus != StatusSkipped || len(sk.Runs) != 1 {
		t.Fatalf("skip %+v runs %d", sk.LastStatus, len(sk.Runs))
	}
	if !sk.NextFire.After(now) {
		t.Fatalf("skip did not advance %s", sk.NextFire)
	}
	q, _ := svc.Get(queued.ID)
	if q.LastStatus != StatusQueued || len(q.Runs) != 0 || !q.NextFire.Equal(now.Add(-time.Minute)) {
		t.Fatalf("queue status %s runs %d next %s", q.LastStatus, len(q.Runs), q.NextFire)
	}

	busy = false
	svc.Tick(context.Background(), false)
	q, _ = svc.Get(queued.ID)
	if q.LastStatus != StatusStarted || q.LastSessionID == "" {
		t.Fatalf("queue did not start: %+v", q)
	}
}

func TestHeartbeatQuietUntilChange(t *testing.T) {
	dir := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	start := &recordingStarter{}
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }, Starter: start}, work)
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec{
		Name: "pulse", Kind: KindHeartbeat, Every: "1m",
		Source: SourcePrompt, Prompt: "look around",
	}
	sch, err := svc.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.NextFire = now.Add(-time.Second)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(context.Background(), false)
	if start.count() != 0 {
		t.Fatal("first heartbeat should baseline quietly")
	}
	got, _ := svc.Get(sch.ID)
	if got.LastStatus != StatusQuiet || got.Fingerprint == "" {
		t.Fatalf("baseline %+v", got)
	}
	// Make the next slot due without changing the tree.
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.NextFire = now.Add(-time.Second)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(context.Background(), false)
	if start.count() != 0 {
		t.Fatal("unchanged tree started a session")
	}
	if err := os.WriteFile(filepath.Join(work, "a.txt"), []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.NextFire = now.Add(-time.Second)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(context.Background(), false)
	if start.count() != 1 {
		t.Fatalf("change starts %d", start.count())
	}
	if start.last().PermissionMode != rootstart.SafestMode {
		t.Fatalf("mode %s", start.last().PermissionMode)
	}
}

func TestWorkspaceModeWinsAndRunNowBusy(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	start := &recordingStarter{id: "now"}
	busy := false
	svc, err := Open(dir, Hooks{
		Now:     func() time.Time { return now },
		Starter: start,
		Busy:    func(string) bool { return busy },
		WorkspaceMode: func(id string) (string, bool) {
			if id == "ws" {
				return "accept-edits", true
			}
			return "", false
		},
		Goal: func(id string) (GoalRef, bool, error) {
			if id == "g1" {
				return GoalRef{ID: "g1", Description: "ship it", Criteria: []string{"cmd: go test"}}, true, nil
			}
			return GoalRef{}, false, nil
		},
	}, dir)
	if err != nil {
		t.Fatal(err)
	}
	spec := promptSpec("ws", "1h")
	spec.WorkspaceID = "ws"
	spec.PermissionMode = "plan" // workspace wins
	sch, err := svc.Create(spec)
	if err != nil {
		t.Fatal(err)
	}
	run, err := svc.RunNow(context.Background(), sch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.PermissionMode != "accept-edits" || run.SessionID != "now" {
		t.Fatalf("run %+v", run)
	}
	if start.last().PermissionMode != "accept-edits" {
		t.Fatalf("req mode %s", start.last().PermissionMode)
	}
	// Calendar did not jump to now.
	got, _ := svc.Get(sch.ID)
	if !got.NextFire.Equal(now.Add(time.Hour)) {
		t.Fatalf("run now moved next to %s", got.NextFire)
	}
	busy = true
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.LastStatus = StatusStarted
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.RunNow(context.Background(), sch.ID)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("busy err %v", err)
	}

	goal, err := svc.Create(Spec{
		Name: "goal", Kind: KindInterval, Every: "1h",
		Source: SourceGoal, GoalID: "g1",
	})
	if err != nil {
		t.Fatal(err)
	}
	busy = false
	if _, err := svc.RunNow(context.Background(), goal.ID); err != nil {
		t.Fatal(err)
	}
	if start.last().GoalID != "g1" || start.last().Prompt == "" {
		t.Fatalf("goal req %+v", start.last())
	}
}

func TestOneShotCompletesAndBadCronRejected(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	start := &recordingStarter{}
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }, Starter: start}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Create(Spec{Name: "bad", Kind: KindCron, Cron: "nope", Source: SourcePrompt, Prompt: "x"}); err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad cron %v", err)
	}
	sch, err := svc.Create(Spec{
		Name: "once", Kind: KindOnce, At: now.Add(-time.Minute),
		Source: SourcePrompt, Prompt: "just once", Missed: MissedOnce,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Tick(context.Background(), true)
	if start.count() != 1 {
		t.Fatalf("starts %d", start.count())
	}
	got, _ := svc.Get(sch.ID)
	if got.Enabled || !got.NextFire.IsZero() {
		t.Fatalf("one-shot still armed: enabled %v next %s", got.Enabled, got.NextFire)
	}
}

func TestPauseResumeDoesNotCatchUp(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	start := &recordingStarter{}
	svc, err := Open(dir, Hooks{Now: func() time.Time { return now }, Starter: start}, dir)
	if err != nil {
		t.Fatal(err)
	}
	sch, err := svc.Create(promptSpec("p", "1h"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetEnabled(sch.ID, false); err != nil {
		t.Fatal(err)
	}
	_, err = svc.store.Mutate(sch.ID, func(s *Schedule) error {
		s.NextFire = now.Add(-2 * time.Hour)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := svc.SetEnabled(sch.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Enabled || !resumed.NextFire.After(now) {
		t.Fatalf("resume %+v", resumed)
	}
	svc.Tick(context.Background(), true)
	if start.count() != 0 {
		t.Fatal("resume caught up")
	}
}
