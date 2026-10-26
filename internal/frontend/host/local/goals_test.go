package local

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jonathanung/strike/internal/frontend/host"
	"github.com/jonathanung/strike/internal/product/goal"
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

func TestGoalsAutopilotAndVerifyStop(t *testing.T) {
	store, err := goal.Open(t.TempDir(), "auto")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gapi := NewGoals(store, t.TempDir())
	if _, err := gapi.Set("x", []string{"cmd: true"}, host.GoalSetOptions{Autopilot: true}); err == nil {
		t.Fatal("autopilot without an allowlist must fail")
	}
	g, err := gapi.Set("nope", []string{"cmd: false"}, host.GoalSetOptions{
		MaxIterations: 20, MaxNoProgressIters: 20, MaxVerifyFailures: 3, MaxTokens: 200_000,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := gapi.Run(context.Background(), g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "blocked" || got.Escalation != "human" || got.LastIteration != 3 {
		t.Fatalf("got=%+v", got)
	}
	log, err := gapi.Log(g.ID, 0)
	if err != nil || len(log) != 3 || log[2].Verified {
		t.Fatalf("log=%+v err=%v", log, err)
	}
}

func TestPauseCancelsInFlightCheck(t *testing.T) {
	dir := t.TempDir()
	store, err := goal.Open(dir, "cancel")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gapi := NewGoals(store, dir)
	pidfile := filepath.Join(dir, "pid")
	cmd := "cmd: echo $$ > '" + pidfile + "'; exec sleep 30"
	g, err := gapi.Set("hold", []string{cmd}, host.GoalSetOptions{
		MaxIterations: 5, MaxNoProgressIters: 5, MaxVerifyFailures: 5, MaxWallClockS: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		_, _ = gapi.Run(context.Background(), g.ID)
		close(done)
	}()
	var pid int
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) && pid == 0 {
		b, err := os.ReadFile(pidfile)
		if err == nil {
			pid, _ = strconv.Atoi(strings.TrimSpace(string(b)))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if pid <= 0 {
		t.Fatal("verification command did not start")
	}
	if _, err := gapi.Pause(g.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("run did not return after pause")
	}
	if err := syscall.Kill(pid, 0); err == nil {
		t.Fatalf("pid %d still running after pause", pid)
	}
	got, ok, err := gapi.Get(g.ID)
	if err != nil || !ok || got.Status != "paused" {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestCanceledRunMarksActiveGoalFailed(t *testing.T) {
	dir := t.TempDir()
	store, err := goal.Open(dir, "failrun")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gapi := NewGoals(store, dir)
	g, err := gapi.Set("hold", []string{"cmd: exec sleep 30"}, host.GoalSetOptions{
		MaxIterations: 5, MaxNoProgressIters: 5, MaxVerifyFailures: 5, MaxWallClockS: 120,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()
	started := time.Now()
	_, runErr := gapi.Run(ctx, g.ID)
	if runErr == nil {
		t.Fatal("expected run error")
	}
	if time.Since(started) > 3*time.Second {
		got, _, _ := gapi.Get(g.ID)
		t.Fatalf("cancel took %s status=%s reason=%s err=%v", time.Since(started).Round(time.Millisecond), got.Status, got.FailReason, runErr)
	}
	got, ok, err := gapi.Get(g.ID)
	if err != nil || !ok || got.Status != "failed" || got.FailReason == "" {
		t.Fatalf("got=%+v ok=%v err=%v runErr=%v", got, ok, err, runErr)
	}
}
