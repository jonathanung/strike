package goal

import (
	"testing"
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
