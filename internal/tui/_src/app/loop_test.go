package tui

import (
	"strings"
	"testing"
	"time"
)

func TestParseLoopInterval(t *testing.T) {
	t.Parallel()
	tests := []struct {
		raw     string
		want    time.Duration
		wantErr string
	}{
		{raw: "15m", want: 15 * time.Minute},
		{raw: "2h", want: 2 * time.Hour},
		{raw: "30s", want: 30 * time.Second},
		{raw: "1h30m", want: 90 * time.Minute},
		{raw: "15", want: 15 * time.Minute},
		{raw: "1ms", wantErr: "too short"},
		{raw: "0s", wantErr: "too short"},
		{raw: "bogus", wantErr: "invalid interval"},
		{raw: "", wantErr: "empty interval"},
		{raw: "200h", wantErr: "too long"},
	}
	for _, tt := range tests {
		got, err := parseLoopInterval(tt.raw)
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("parseLoopInterval(%q) err=%v, want containing %q", tt.raw, err, tt.wantErr)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseLoopInterval(%q) err=%v", tt.raw, err)
			continue
		}
		if got != tt.want {
			t.Errorf("parseLoopInterval(%q)=%v, want %v", tt.raw, got, tt.want)
		}
	}
}

func TestParseLoopStart(t *testing.T) {
	t.Parallel()
	d, job, err := parseLoopStart("/loop 15m check pipeline status", []string{"15m", "check", "pipeline", "status"})
	if err != nil {
		t.Fatal(err)
	}
	if d != 15*time.Minute {
		t.Fatalf("interval=%v", d)
	}
	if job != "check pipeline status" {
		t.Fatalf("job=%q", job)
	}
	if _, _, err := parseLoopStart("/loop 15m", []string{"15m"}); err == nil {
		t.Fatal("expected job required")
	}
	if _, _, err := parseLoopStart("/loop notadur job", []string{"notadur", "job"}); err == nil {
		t.Fatal("expected invalid interval")
	}
}

func TestLoopSlashStartListStop(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.modelName = "echo"

	next, cmd := m.handleCommand("/loop 15m check pipeline")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("start should arm a tick")
	}
	if len(m.loops) != 1 || m.loops[0].id != "l1" {
		t.Fatalf("loops=%+v", m.loops)
	}
	if m.loops[0].interval != 15*time.Minute || m.loops[0].job != "check pipeline" {
		t.Fatalf("loop=%+v", m.loops[0])
	}
	if !strings.Contains(m.notice, "started l1") {
		t.Fatalf("notice=%q", m.notice)
	}
	gen := m.loops[0].gen

	next, _ = m.handleCommand("/loop list")
	m = next.(Model)
	if !strings.Contains(m.notice, "l1 every") || !strings.Contains(m.notice, "check pipeline") {
		t.Fatalf("list notice=%q", m.notice)
	}

	next, _ = m.handleCommand("/loop stop l1")
	m = next.(Model)
	if len(m.loops) != 0 {
		t.Fatalf("after stop loops=%+v", m.loops)
	}
	if !strings.Contains(m.notice, "stopped l1") {
		t.Fatalf("stop notice=%q", m.notice)
	}

	// Stale tick after stop must not submit.
	next, staleCmd := m.Update(loopTickMsg{id: "l1", gen: gen})
	m = next.(Model)
	_ = m
	if staleCmd != nil {
		t.Fatalf("stale tick should be ignored, got non-nil cmd")
	}
	assertNoAppOp(t, ops)
}

func TestLoopTickFiresUserInput(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	// Millisecond interval only for re-arm cmd under test (below min for /loop parse).
	m.loops = []scheduledLoop{{
		id: "l1", interval: time.Millisecond, job: "check pipeline", gen: 1,
	}}

	next, batch := m.Update(loopTickMsg{id: "l1", gen: 1})
	m = next.(Model)
	if len(m.loops) != 1 || m.loops[0].runs != 1 {
		t.Fatalf("loops=%+v", m.loops)
	}
	// Batch is submit + 1ms re-arm; both complete quickly.
	for _, msg := range runAllAppCmds(t, batch) {
		if msg == nil {
			continue
		}
		if _, isTick := msg.(loopTickMsg); isTick {
			continue
		}
		if _, isHist := msg.(historyAddedMsg); isHist {
			continue
		}
	}
	assertUserInputText(t, receiveAppOp(t, ops), "check pipeline")
}

func TestLoopStopAllAndInvalid(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"

	next, _ := m.handleCommand("/loop 1m job one")
	m = next.(Model)
	next, _ = m.handleCommand("/loop 2m job two")
	m = next.(Model)
	if len(m.loops) != 2 {
		t.Fatalf("loops=%d", len(m.loops))
	}

	next, _ = m.handleCommand("/loop stop")
	m = next.(Model)
	if len(m.loops) != 0 {
		t.Fatalf("stop all left %d", len(m.loops))
	}
	if !strings.Contains(m.notice, "stopped all") {
		t.Fatalf("notice=%q", m.notice)
	}

	next, _ = m.handleCommand("/loop xyz check")
	m = next.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "invalid interval") {
		t.Fatalf("notice=%q err=%v", m.notice, m.noticeErr)
	}

	next, _ = m.handleCommand("/loop 1ms too short job")
	m = next.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "too short") {
		t.Fatalf("notice=%q err=%v", m.notice, m.noticeErr)
	}

	next, _ = m.handleCommand("/loop stop missing")
	m = next.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "no active") {
		t.Fatalf("notice=%q", m.notice)
	}
}

func TestLoopRequiresProvider(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	next, cmd := m.handleCommand("/loop 1m check stuff")
	m = next.(Model)
	if cmd != nil {
		t.Fatal("should not schedule without provider")
	}
	if !m.noticeErr {
		t.Fatalf("notice=%q", m.notice)
	}
	assertNoAppOp(t, ops)
}

func TestLoopTickEnqueuesWhileTurnRunning(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.providerName = "echo"
	m.turnRunning = true
	m.loops = []scheduledLoop{{id: "l1", interval: time.Millisecond, job: "queued job", gen: 1}}

	next, cmd := m.Update(loopTickMsg{id: "l1", gen: 1})
	m = next.(Model)
	if m.loops[0].runs != 1 {
		t.Fatalf("runs=%d", m.loops[0].runs)
	}
	if len(m.inputQueue) != 1 || m.inputQueue[0].modelText != "queued job" {
		t.Fatalf("queue=%+v", m.inputQueue)
	}
	if cmd == nil {
		t.Fatal("expected re-arm tick cmd")
	}
	// Drain 1ms re-arm only; enqueue path does not emit an op yet.
	for _, msg := range runAllAppCmds(t, cmd) {
		_ = msg
	}
	assertNoAppOp(t, ops)
}

func TestLoopUnknownStopID(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.providerName = "echo"
	next, _ := m.handleCommand("/loop 1m keep going")
	m = next.(Model)
	next, _ = m.handleCommand("/loop stop nope")
	m = next.(Model)
	if !m.noticeErr || !strings.Contains(m.notice, "unknown id") {
		t.Fatalf("notice=%q", m.notice)
	}
	if len(m.loops) != 1 {
		t.Fatalf("loops=%d", len(m.loops))
	}
}

func TestFormatLoopInterval(t *testing.T) {
	t.Parallel()
	if got := formatLoopInterval(15 * time.Minute); got != "15m" {
		t.Fatalf("got %q", got)
	}
	if got := formatLoopInterval(2 * time.Hour); got != "2h" {
		t.Fatalf("got %q", got)
	}
	if got := formatLoopInterval(30 * time.Second); got != "30s" {
		t.Fatalf("got %q", got)
	}
}
