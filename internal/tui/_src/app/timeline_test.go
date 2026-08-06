package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

func TestHandleTimelineCommandShowsModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "sess-tl"
	m.resetRunTimeline()
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	corr := protocol.Correlation{SessionID: "sess-tl", TurnID: "turn-1"}
	m.observeTimeline(protocol.TurnStarted{Correlation: corr}, base)
	m.observeTimeline(protocol.ToolCallBegin{
		Correlation: corr, CallID: "c1", Name: "bash",
		Args: json.RawMessage(`{}`),
	}, base.Add(time.Millisecond))
	m.observeTimeline(protocol.ToolCallEnd{
		Correlation: corr, CallID: "c1", Output: "ok",
	}, base.Add(2*time.Millisecond))
	m.observeTimeline(protocol.TurnCompleted{Correlation: corr}, base.Add(3*time.Millisecond))

	next, cmd := m.handleCommand("/timeline")
	if cmd != nil {
		t.Fatal("expected no cmd")
	}
	nm, ok := next.(Model)
	if !ok {
		t.Fatalf("next type %T", next)
	}
	if nm.modal == nil {
		t.Fatal("expected timeline modal")
	}
	if _, ok := nm.modal.(*timelineModal); !ok {
		t.Fatalf("modal type %T", nm.modal)
	}
	view := nm.modal.view(80, nm.th)
	if !strings.Contains(view, "Run timeline") && !strings.Contains(view, "timeline") {
		// Dialog title may be styled; body should mention turn/tool state.
		if !strings.Contains(view, "completed") && !strings.Contains(view, "turn") {
			t.Fatalf("modal view missing timeline content:\n%s", view)
		}
	}
}

func TestHandleTimelineExportRedacts(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "sess-exp"
	m.workDir = t.TempDir()
	m.resetRunTimeline()
	base := time.Now().UTC()
	corr := protocol.Correlation{SessionID: "sess-exp", TurnID: "t1"}
	secret := "sk-ant-api03-TUIEXPORTLEAKVALUE99"
	m.observeTimeline(protocol.TurnStarted{Correlation: corr}, base)
	m.observeTimeline(protocol.ToolCallEnd{
		Correlation: corr, CallID: "c1",
		Output:  "OPENAI_API_KEY=sk-proj-nested-tui-99\n" + secret,
		IsError: false,
	}, base.Add(time.Millisecond))

	path := filepath.Join(m.workDir, "out.json")
	next, cmd := m.handleCommand("/timeline export " + path)
	if cmd == nil {
		t.Fatal("expected export cmd")
	}
	msg := cmd()
	finished, ok := msg.(timelineFinishedMsg)
	if !ok {
		t.Fatalf("msg type %T", msg)
	}
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, banned := range []string{secret, "sk-proj-nested-tui-99"} {
		if strings.Contains(body, banned) {
			t.Errorf("export contains %q", banned)
		}
	}
	var tr timeline.Trace
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.SchemaVersion != timeline.SchemaVersion {
		t.Fatalf("schema = %q", tr.SchemaVersion)
	}
	// Apply finished notice path.
	_, _ = next.(Model).applyTimelineFinished(finished)
}

func TestTimedEventsFromJSONL(t *testing.T) {
	env, err := protocol.Wrap(protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "s", TurnID: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	events, err := timedEventsFromJSONL(append(b, '\n'))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("len = %d", len(events))
	}
	if _, ok := events[0].Event.(protocol.TurnStarted); !ok {
		t.Fatalf("event type %T", events[0].Event)
	}
}

func TestHandleTimelineEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, _ := m.handleCommand("/timeline")
	nm := next.(Model)
	if !nm.noticeErr || !strings.Contains(nm.notice, "empty") {
		t.Fatalf("notice = %q err=%v", nm.notice, nm.noticeErr)
	}
}
