package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSleepShortSucceeds(t *testing.T) {
	start := time.Now()
	res, err := NewSleep().Execute(context.Background(), mustJSON(t, map[string]any{
		"seconds": 0.05,
	}), allowAll(t.TempDir()))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("returned too fast: %v", elapsed)
	}
	if !strings.Contains(res.Output, "Slept") {
		t.Errorf("output = %q", res.Output)
	}
	if !strings.Contains(res.Title, "slept") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestSleepRejectsBounds(t *testing.T) {
	tc := allowAll(t.TempDir())
	cases := []float64{0, -1, 300.1, 1000}
	for _, sec := range cases {
		_, err := NewSleep().Execute(context.Background(), mustJSON(t, map[string]any{
			"seconds": sec,
		}), tc)
		if err == nil {
			t.Errorf("seconds=%v: expected error", sec)
			continue
		}
		if !strings.Contains(err.Error(), "seconds") {
			t.Errorf("seconds=%v: got %v", sec, err)
		}
	}
	// 300 is allowed (inclusive max).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// Don't actually sleep 300s — just ensure validation accepts via a tiny value at boundary check.
	// Boundary 300 would take too long; unit-test the condition with 300 rejected above as 300.1
	// and accept that 300 is in range by checking error message for 0 only.
	_ = ctx
}

func TestSleepMaxInclusive(t *testing.T) {
	// Validation only: cancel immediately so we never wait 300s if validation passes.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewSleep().Execute(ctx, mustJSON(t, map[string]any{
		"seconds": 300,
	}), allowAll(t.TempDir()))
	// Either context canceled (validation passed) or unexpected validation error.
	if err == nil {
		t.Fatal("expected error from canceled context")
	}
	if strings.Contains(err.Error(), "seconds must") {
		t.Errorf("300 should be accepted, got validation error: %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
}

func TestSleepCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	// Cancel after a short delay so Execute is inside the select.
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err := NewSleep().Execute(ctx, mustJSON(t, map[string]any{
		"seconds": 30,
	}), allowAll(t.TempDir()))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancel error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want Canceled", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("cancel took too long: %v", elapsed)
	}
}

func TestSleepPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
	}
	_, err := NewSleep().Execute(context.Background(), mustJSON(t, map[string]any{
		"seconds": 0.01,
	}), tc)
	if err == nil {
		t.Fatal("expected deny")
	}
}

func TestSleepWakesOnChildChannel(t *testing.T) {
	wake := make(chan struct{})
	tc := allowAll(t.TempDir())
	tc.ChildWake = wake
	go func() {
		time.Sleep(30 * time.Millisecond)
		close(wake)
	}()
	start := time.Now()
	res, err := NewSleep().Execute(context.Background(), mustJSON(t, map[string]any{
		"seconds": 30,
	}), tc)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("wake took too long: %v", elapsed)
	}
	if !strings.Contains(res.Output, "child") {
		t.Errorf("output = %q, want child wake message", res.Output)
	}
	if !strings.Contains(res.Title, "woke") {
		t.Errorf("title = %q", res.Title)
	}
}

func TestSleepSkipsWhenChildNoticePending(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.HasChildNotice = func() bool { return true }
	start := time.Now()
	res, err := NewSleep().Execute(context.Background(), mustJSON(t, map[string]any{
		"seconds": 30,
	}), tc)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("should not sleep when notice pending: %v", elapsed)
	}
	if !strings.Contains(res.Output, "child") {
		t.Errorf("output = %q", res.Output)
	}
}

func TestSleepExplicitWithoutWakeStillSleeps(t *testing.T) {
	// No ChildWake / HasChildNotice: normal short sleep still works.
	start := time.Now()
	res, err := NewSleep().Execute(context.Background(), mustJSON(t, map[string]any{
		"seconds": 0.05,
	}), allowAll(t.TempDir()))
	elapsed := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("returned too fast: %v", elapsed)
	}
	if !strings.Contains(res.Output, "Slept") {
		t.Errorf("output = %q", res.Output)
	}
}
