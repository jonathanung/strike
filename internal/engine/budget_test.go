package engine

import (
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestMergeAgentBudgetSpawnWins(t *testing.T) {
	def := tool.AgentBudgetLimits{MaxTokens: 100, MaxToolCalls: 10, StallAfterS: 60}
	spawn := tool.AgentBudgetLimits{MaxTokens: 50, MaxWallClockS: 30}
	got := MergeAgentBudget(def, spawn)
	if got.MaxTokens != 50 {
		t.Fatalf("MaxTokens=%d want 50", got.MaxTokens)
	}
	if got.MaxToolCalls != 10 {
		t.Fatalf("MaxToolCalls=%d want 10 (inherit)", got.MaxToolCalls)
	}
	if got.MaxWallClockS != 30 {
		t.Fatalf("MaxWallClockS=%d want 30", got.MaxWallClockS)
	}
	if got.StallAfterS != 60 {
		t.Fatalf("StallAfterS=%d want 60", got.StallAfterS)
	}
}

func TestChildBudgetToolCallTrip(t *testing.T) {
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{MaxToolCalls: 3}, "do work", now)
	b.noteTool("read", now)
	b.noteTool("read", now.Add(time.Second))
	if trip, _, _, _ := b.evaluate(now.Add(2*time.Second), now); trip {
		t.Fatal("should not trip before limit")
	}
	b.noteTool("bash", now.Add(3*time.Second))
	trip, kind, reason, terminal := b.evaluate(now.Add(4*time.Second), now)
	if !trip || kind != "tool_calls" {
		t.Fatalf("trip=%v kind=%q want tool_calls", trip, kind)
	}
	if terminal != protocol.ChildStatusFailed {
		t.Fatalf("terminal=%s want failed", terminal)
	}
	if reason == "" {
		t.Fatal("empty reason")
	}
	if !b.markEscalatedLocked(kind, reason, terminal) {
		t.Fatal("markEscalated should succeed once")
	}
	if b.markEscalatedLocked(kind, reason, terminal) {
		t.Fatal("second mark should be false")
	}
}

func TestChildBudgetDangerousTools(t *testing.T) {
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{MaxDangerousTools: 2}, "x", now)
	b.noteTool("read", now) // not dangerous
	b.noteTool("bash", now)
	if trip, _, _, _ := b.evaluate(now, now); trip {
		t.Fatal("one dangerous should not trip")
	}
	b.noteTool("edit", now)
	trip, kind, _, _ := b.evaluate(now, now)
	if !trip || kind != "dangerous_tools" {
		t.Fatalf("trip=%v kind=%q", trip, kind)
	}
}

func TestChildBudgetTokens(t *testing.T) {
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{MaxTokens: 100}, "x", now)
	b.noteUsage(40, now)
	b.noteUsage(60, now)
	trip, kind, _, term := b.evaluate(now, now)
	if !trip || kind != "tokens" || term != protocol.ChildStatusFailed {
		t.Fatalf("trip=%v kind=%q term=%s", trip, kind, term)
	}
}

func TestChildBudgetWallClock(t *testing.T) {
	start := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{MaxWallClockS: 2}, "x", start)
	if trip, _, _, _ := b.evaluate(start.Add(time.Second), start); trip {
		t.Fatal("early wall clock trip")
	}
	trip, kind, _, _ := b.evaluate(start.Add(3*time.Second), start)
	if !trip || kind != "wall_clock" {
		t.Fatalf("trip=%v kind=%q", trip, kind)
	}
}

func TestChildBudgetHardStall(t *testing.T) {
	start := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{StallAfterS: 5}, "x", start)
	// Progress at start; idle past threshold.
	trip, kind, _, term := b.evaluate(start.Add(6*time.Second), start)
	if !trip || kind != "stall" || term != protocol.ChildStatusBlocked {
		t.Fatalf("trip=%v kind=%q term=%s", trip, kind, term)
	}
}

func TestChildBudgetSoftStallNoHardTrip(t *testing.T) {
	start := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{}, "x", start) // no hard stall
	// Past default soft stall (300s).
	trip, _, _, _ := b.evaluate(start.Add(301*time.Second), start)
	if trip {
		t.Fatal("soft stall must not hard-trip")
	}
	if !b.softStall {
		t.Fatal("expected softStall true")
	}
	snap := b.snapshot(start.Add(301*time.Second), start)
	if !snap.Stall {
		t.Fatal("snapshot.Stall want true")
	}
}

func TestChildBudgetLoopHardAndSoft(t *testing.T) {
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{LoopDetectN: 4}, "x", now)
	for i := 0; i < 3; i++ {
		b.noteTool("read", now.Add(time.Duration(i)*time.Second))
	}
	if trip, _, _, _ := b.evaluate(now, now); trip {
		t.Fatal("3 < 4 should not hard trip")
	}
	if !b.softLoop {
		// soft default is 6; with only 3 tools soft may be false
	}
	b.noteTool("read", now.Add(4*time.Second))
	trip, kind, _, term := b.evaluate(now, now)
	if !trip || kind != "loop" || term != protocol.ChildStatusBlocked {
		t.Fatalf("trip=%v kind=%q term=%s", trip, kind, term)
	}
}

func TestChildBudgetSoftLoopDefault(t *testing.T) {
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{}, "x", now)
	for i := 0; i < defaultSoftLoopN; i++ {
		b.noteTool("grep", now.Add(time.Duration(i)*time.Millisecond))
	}
	trip, _, _, _ := b.evaluate(now, now)
	if trip {
		t.Fatal("soft loop must not hard-trip")
	}
	if !b.softLoop {
		t.Fatal("expected softLoop")
	}
}

func TestChildBudgetSnapshotRemaining(t *testing.T) {
	start := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{
		MaxWallClockS: 100,
		MaxTokens:     1000,
		MaxToolCalls:  10,
	}, "obj", start)
	b.noteUsage(200, start.Add(10*time.Second))
	b.noteTool("read", start.Add(10*time.Second))
	snap := b.snapshot(start.Add(10*time.Second), start)
	if snap.TokensUsed != 200 {
		t.Fatalf("tokens=%d", snap.TokensUsed)
	}
	if snap.TokensRemaining == nil || *snap.TokensRemaining != 800 {
		t.Fatalf("tokens remaining=%v", snap.TokensRemaining)
	}
	if snap.ToolCallsRemaining == nil || *snap.ToolCallsRemaining != 9 {
		t.Fatalf("tool remaining=%v", snap.ToolCallsRemaining)
	}
	if snap.WallClockRemainingS == nil || *snap.WallClockRemainingS != 90 {
		t.Fatalf("wall remaining=%v", snap.WallClockRemainingS)
	}
	if b.objective != "obj" {
		t.Fatalf("objective=%q", b.objective)
	}
}

func TestChildBudgetEvaluateRace(t *testing.T) {
	// Concurrent noteTool + evaluate must not race (run with -race).
	now := time.Now()
	b := newChildBudget(tool.AgentBudgetLimits{MaxToolCalls: 1000}, "x", now)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				mu.Lock()
				b.noteTool("read", now.Add(time.Duration(i*50+j)*time.Millisecond))
				_, _, _, _ = b.evaluate(now.Add(time.Second), now)
				_ = b.snapshot(now.Add(time.Second), now)
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
}

func TestIdenticalTail(t *testing.T) {
	if identicalTail([]string{"a", "a"}, 2) != true {
		t.Fatal("want true")
	}
	if identicalTail([]string{"a", "b", "a"}, 2) != false {
		t.Fatal("want false")
	}
	if identicalTail([]string{"a"}, 2) != false {
		t.Fatal("want false for short")
	}
}
