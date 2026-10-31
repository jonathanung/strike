package goal

import (
	"testing"
	"time"
)

func TestStoreCreateGetListResume(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, err := Open(root, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	spec, err := ParseCheckSpec("cmd: true")
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.Create("do thing", []Criterion{{Description: "ok", Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != StatusPending {
		t.Fatalf("status=%s want pending", g.Status)
	}
	if g.ID == "" {
		t.Fatal("empty id")
	}

	got, ok, err := s.Get(g.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Description != "do thing" {
		t.Fatalf("desc=%q", got.Description)
	}

	list, err := s.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list: n=%d err=%v", len(list), err)
	}

	// Reopen — durable.
	s2, err := Open(root, "proj-a")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got2, ok, err := s2.Get(g.ID)
	if err != nil || !ok || got2.Description != "do thing" {
		t.Fatalf("reopen get: ok=%v err=%v got=%+v", ok, err, got2)
	}
}

func TestStoreCommitIterationAndIntents(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	g.Status = StatusActive
	key := IntentKey(g.ID, 1, 0)
	rec := IterationRecord{
		N:         1,
		Plan:      "p",
		StateHash: "abc",
		Actions: []ActionRecord{{
			Index: 0, Tool: "bash", IntentKey: key, Completed: true, OK: true,
		}},
		Evaluation: EvalRecord{AllSatisfied: true},
	}
	if err := s.CommitIteration(g, rec); err != nil {
		t.Fatal(err)
	}
	if !s.IntentDone(key) {
		t.Fatal("intent should be done after commit")
	}
	iters, err := s.ListIterations(g.ID)
	if err != nil || len(iters) != 1 || iters[0].N != 1 {
		t.Fatalf("iters=%+v err=%v", iters, err)
	}
}

// A commit carrying a stale Active snapshot must not resurrect a goal
// a concurrent control already paused (#1417): the stored pause —
// status and its frozen wall clock — wins, the iteration still records.
func TestStoreCommitIterationPreservesConcurrentPause(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(g.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	stale, ok, err := s.Get(g.ID)
	if err != nil || !ok || stale.Status != StatusActive {
		t.Fatalf("snapshot=%+v ok=%v err=%v", stale, ok, err)
	}
	paused, err := s.SetStatus(g.ID, StatusPaused, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CommitIteration(stale, IterationRecord{N: 1, StateHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(g.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if got.Status != StatusPaused {
		t.Fatalf("status=%s, want paused (commit overwrote the pause)", got.Status)
	}
	if !got.ActiveStartedAt.IsZero() || got.ActiveElapsed != paused.ActiveElapsed {
		t.Fatalf("pause wall-clock freeze lost: %+v", got)
	}
	if got.LastIteration != 1 {
		t.Fatalf("lastIteration=%d, want the committed iteration recorded", got.LastIteration)
	}
}

// A commit whose snapshot deliberately carries a terminal status (the
// loop's guard transition, stored goal still Active) must still apply.
func TestStoreCommitIterationAppliesGuardTransition(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(g.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	snap, ok, err := s.Get(g.ID)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	snap.Status = StatusDone
	if err := s.CommitIteration(snap, IterationRecord{N: 1, StateHash: "h1"}); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Get(g.ID)
	if err != nil || got.Status != StatusDone || got.LastIteration != 1 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestStoreMarkFailedIfActive(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(g.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(g.ID, StatusPaused, ""); err != nil {
		t.Fatal(err)
	}
	// Paused: the failure must not apply — a pause-caused cancellation
	// is not a failure (#1417).
	got, applied, err := s.MarkFailedIfActive(g.ID, "context canceled")
	if err != nil || applied || got.Status != StatusPaused || got.FailReason != "" {
		t.Fatalf("paused: got=%+v applied=%v err=%v", got, applied, err)
	}
	if _, err := s.SetStatus(g.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	got, applied, err = s.MarkFailedIfActive(g.ID, "boom")
	if err != nil || !applied || got.Status != StatusFailed || got.FailReason != "boom" {
		t.Fatalf("active: got=%+v applied=%v err=%v", got, applied, err)
	}
	if _, _, err := s.MarkFailedIfActive("missing", "boom"); err == nil {
		t.Fatal("missing goal should error")
	}
}

func TestStoreCommitIterationReopen(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	s, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	key := IntentKey(g.ID, 1, 0)
	g.Status = StatusActive
	g.LastIteration = 1
	if err := s.CommitIteration(g, IterationRecord{
		N: 1, StateHash: "h1",
		Actions: []ActionRecord{{Index: 0, Tool: "t", IntentKey: key, Completed: true, OK: true}},
	}); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s2, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if !s2.IntentDone(key) {
		t.Fatal("intent lost on reopen")
	}
	got, ok, _ := s2.Get(g.ID)
	if !ok || got.LastIteration != 1 {
		t.Fatalf("got=%+v", got)
	}
}

func TestStoreStatusTransitions(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	g, err = s.SetStatus(g.ID, StatusActive, "")
	if err != nil || g.Status != StatusActive {
		t.Fatalf("activate: %+v err=%v", g, err)
	}
	g, err = s.SetStatus(g.ID, StatusPaused, "")
	if err != nil || g.Status != StatusPaused {
		t.Fatalf("pause: %+v err=%v", g, err)
	}
	g, err = s.SetStatus(g.ID, StatusActive, "")
	if err != nil || g.Status != StatusActive {
		t.Fatalf("resume: %+v err=%v", g, err)
	}
	g, err = s.SetStatus(g.ID, StatusAborted, "stop")
	if err != nil || g.Status != StatusAborted {
		t.Fatalf("abort: %+v err=%v", g, err)
	}
}

func TestStoreRejectsEmptyCriteria(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Create("nope", nil, DefaultConstraints()); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestStoreRace(t *testing.T) {
	s, err := Open(t.TempDir(), "race")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_, _, _ = s.Get(g.ID)
			_, _ = s.List()
			_ = s.AppendEvent(Event{GoalID: g.ID, Type: "test"})
		}()
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestPauseFreezesWallClock(t *testing.T) {
	t.Parallel()
	s, err := Open(t.TempDir(), "clock")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	spec, _ := ParseCheckSpec("cmd: true")
	g, err := s.Create("g", []Criterion{{Check: spec}}, DefaultConstraints())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetStatus(g.ID, StatusActive, ""); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	paused, err := s.SetStatus(g.ID, StatusPaused, "")
	if err != nil {
		t.Fatal(err)
	}
	if !paused.ActiveStartedAt.IsZero() {
		t.Fatal("pause left the wall clock running")
	}
	if paused.ActiveElapsed < 20*time.Millisecond {
		t.Fatalf("elapsed=%s", paused.ActiveElapsed)
	}
	frozen := paused.ActiveElapsed
	time.Sleep(40 * time.Millisecond)
	resumed, err := s.SetStatus(g.ID, StatusActive, "")
	if err != nil {
		t.Fatal(err)
	}
	if resumed.ActiveElapsed != frozen {
		t.Fatalf("resume billed the wait: frozen=%s now=%s", frozen, resumed.ActiveElapsed)
	}
	if resumed.ActiveStartedAt.IsZero() {
		t.Fatal("resume did not start a new stretch")
	}
}
