package local

import (
	"context"
	"testing"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/goal"
)

func TestGoalsWiredThrough(t *testing.T) {
	store, err := goal.Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc := New(nil, nil, nil, nil, nil, nil, nil, t.TempDir())
	if svc.Goals != nil {
		t.Fatal("Goals should be nil until NewGoals")
	}
	svc.Goals = NewGoals(store, t.TempDir())
	if svc.Goals == nil {
		t.Fatal("NewGoals returned nil")
	}

	g, err := svc.Goals.Set("pass check", []string{"cmd: true"}, host.GoalSetOptions{MaxIterations: 3})
	if err != nil {
		t.Fatal(err)
	}
	if g.Status != "pending" {
		t.Fatalf("status=%s", g.Status)
	}
	if _, err := svc.Goals.Set("bad", nil, host.GoalSetOptions{}); err == nil {
		t.Fatal("empty criteria should fail")
	}

	list, err := svc.Goals.List()
	if err != nil || len(list) != 1 {
		t.Fatalf("list=%v err=%v", list, err)
	}

	got, err := svc.Goals.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "done" {
		t.Fatalf("run status=%s reason=%s", got.Status, got.FailReason)
	}
	log, err := svc.Goals.Log(g.ID, 0)
	if err != nil || len(log) < 1 {
		t.Fatalf("log=%v err=%v", log, err)
	}
}

func TestGoalsPauseAbort(t *testing.T) {
	store, err := goal.Open(t.TempDir(), "p2")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gapi := NewGoals(store, "")
	g, err := gapi.Set("x", []string{"cmd: false"}, host.GoalSetOptions{MaxIterations: 10, MaxNoProgressIters: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gapi.Resume(g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := gapi.Pause(g.ID); err != nil {
		t.Fatal(err)
	}
	got, ok, err := gapi.Get(g.ID)
	if err != nil || !ok || got.Status != "paused" {
		t.Fatalf("got=%+v", got)
	}
	if _, err := gapi.Abort(g.ID); err != nil {
		t.Fatal(err)
	}
	got, _, _ = gapi.Get(g.ID)
	if got.Status != "aborted" {
		t.Fatalf("status=%s", got.Status)
	}
}
