package timeline_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

func TestBuildTurnToolProvider(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	corr := protocol.Correlation{SessionID: "sess-1", TurnID: "turn-abc"}
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: corr}},
		{Time: base.Add(10 * time.Millisecond), Event: protocol.UsageReported{
			Correlation: protocol.Correlation{
				SessionID: "sess-1", TurnID: "turn-abc",
				ProviderRequestID: "preq-1", Attempt: 1,
			},
			Input:  protocol.KnownTokens(100),
			Output: protocol.KnownTokens(20),
			Source: protocol.UsageSourceActual,
		}},
		{Time: base.Add(20 * time.Millisecond), Event: protocol.ToolCallBegin{
			Correlation: protocol.Correlation{
				SessionID: "sess-1", TurnID: "turn-abc",
				ProviderRequestID: "preq-1", Attempt: 1,
			},
			CallID: "call-1",
			Name:   "bash",
			Args:   json.RawMessage(`{"command":"echo hi"}`),
		}},
		{Time: base.Add(120 * time.Millisecond), Event: protocol.ToolCallEnd{
			Correlation: protocol.Correlation{
				SessionID: "sess-1", TurnID: "turn-abc",
				ProviderRequestID: "preq-1",
			},
			CallID: "call-1",
			Title:  "echo hi",
			Output: "hi\n",
		}},
		{Time: base.Add(200 * time.Millisecond), Event: protocol.TurnCompleted{
			Correlation: corr,
			StopReason:  "end_turn",
		}},
	}

	tr := timeline.Build(events, timeline.Options{
		SessionID: "sess-1",
		Clock:     func() time.Time { return base.Add(time.Second) },
	})

	if tr.SchemaVersion != timeline.SchemaVersion {
		t.Fatalf("schema = %q", tr.SchemaVersion)
	}
	if !tr.Redacted {
		t.Fatal("expected redacted=true")
	}
	if tr.Summary.Turns != 1 || tr.Summary.Tools != 1 || tr.Summary.Providers != 1 {
		t.Fatalf("summary = %+v", tr.Summary)
	}

	var turn, tool, prov *timeline.Entry
	for i := range tr.Entries {
		e := &tr.Entries[i]
		switch e.Kind {
		case timeline.KindTurn:
			turn = e
		case timeline.KindTool:
			tool = e
		case timeline.KindProvider:
			prov = e
		}
	}
	if turn == nil || tool == nil || prov == nil {
		t.Fatalf("missing entries: turn=%v tool=%v prov=%v entries=%+v", turn, tool, prov, tr.Entries)
	}
	if turn.State != timeline.StateCompleted || turn.TurnID != "turn-abc" {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.DurationMs == nil || *turn.DurationMs != 200 {
		t.Fatalf("turn duration = %v, want 200ms", turn.DurationMs)
	}
	if tool.State != timeline.StateCompleted || tool.Name != "bash" {
		t.Fatalf("tool = %+v", tool)
	}
	if tool.DurationMs == nil || *tool.DurationMs != 100 {
		t.Fatalf("tool duration = %v, want 100ms", tool.DurationMs)
	}
	if tool.ParentID != turn.ID {
		t.Fatalf("tool parent = %q, want turn %q", tool.ParentID, turn.ID)
	}
	if prov.InputTokens == nil || *prov.InputTokens != 100 {
		t.Fatalf("provider tokens = %+v", prov)
	}
}

func TestBuildChildAndCancel(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: protocol.Correlation{SessionID: "root", TurnID: "t1"}}},
		{Time: base.Add(time.Millisecond), Event: protocol.ChildStarted{
			Correlation: protocol.Correlation{
				SessionID: "child-1", ParentSessionID: "root", Depth: 1, TurnID: "t1",
			},
			Agent:  "explore",
			Name:   "scout",
			Prompt: "look around",
		}},
		{Time: base.Add(50 * time.Millisecond), Event: protocol.ToolCallBegin{
			Correlation: protocol.Correlation{SessionID: "root", TurnID: "t1"},
			CallID:      "c-cancel",
			Name:        "bash",
			Args:        json.RawMessage(`{}`),
		}},
		{Time: base.Add(60 * time.Millisecond), Event: protocol.ToolCallEnd{
			Correlation: protocol.Correlation{SessionID: "root", TurnID: "t1"},
			CallID:      "c-cancel",
			Output:      "canceled",
			IsError:     true,
		}},
		{Time: base.Add(100 * time.Millisecond), Event: protocol.ChildCompleted{
			Correlation: protocol.Correlation{
				SessionID: "child-1", ParentSessionID: "root", Depth: 1, TurnID: "t1",
			},
			Status:  protocol.ChildStatusCompleted,
			Summary: "done",
			Handoff: protocol.CompletionHandoff{Summary: "done"},
		}},
	}
	tr := timeline.Build(events, timeline.Options{SessionID: "root"})
	if tr.Summary.Children != 1 || tr.Summary.Canceled != 1 {
		t.Fatalf("summary = %+v", tr.Summary)
	}
	var child, tool *timeline.Entry
	for i := range tr.Entries {
		e := &tr.Entries[i]
		switch e.Kind {
		case timeline.KindChild:
			child = e
		case timeline.KindTool:
			tool = e
		}
	}
	if child == nil || child.State != timeline.StateCompleted || child.Name != "scout" {
		t.Fatalf("child = %+v", child)
	}
	if tool == nil || tool.State != timeline.StateCanceled {
		t.Fatalf("tool = %+v", tool)
	}
}

func TestExportRedactsSecrets(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	secret := "sk-ant-api03-LEAKEDVALUEFROMTOOL99"
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: protocol.Correlation{SessionID: "s", TurnID: "t"}}},
		{Time: base.Add(time.Millisecond), Event: protocol.ToolCallBegin{
			Correlation: protocol.Correlation{SessionID: "s", TurnID: "t"},
			CallID:      "c1",
			Name:        "bash",
			Args:        json.RawMessage(`{"command":"echo ` + secret + `"}`),
		}},
		{Time: base.Add(2 * time.Millisecond), Event: protocol.ToolCallEnd{
			Correlation: protocol.Correlation{SessionID: "s", TurnID: "t"},
			CallID:      "c1",
			// Nested JSON echo — redaction must still catch the key.
			Output: `{"stdout":"OPENAI_API_KEY=sk-proj-nested-secret-value-99\n` + secret + `"}`,
		}},
	}
	tr := timeline.Build(events, timeline.Options{SessionID: "s"})
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.json")
	if err := timeline.ExportJSON(path, tr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, banned := range []string{secret, "sk-proj-nested-secret-value-99"} {
		if strings.Contains(body, banned) {
			t.Errorf("export still contains %q\n%s", banned, body)
		}
	}
	if !strings.Contains(body, "REDACTED") {
		t.Fatalf("expected REDACTED marker in export:\n%s", body)
	}
	// Round-trip JSON.
	var got timeline.Trace
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != timeline.SchemaVersion {
		t.Fatalf("schema = %q", got.SchemaVersion)
	}
}

func TestExportJSONL(t *testing.T) {
	base := time.Now().UTC()
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: protocol.Correlation{SessionID: "s", TurnID: "t1"}}},
		{Time: base.Add(time.Millisecond), Event: protocol.TurnCompleted{Correlation: protocol.Correlation{SessionID: "s", TurnID: "t1"}}},
	}
	tr := timeline.Build(events, timeline.Options{SessionID: "s"})
	path := filepath.Join(t.TempDir(), "t.jsonl")
	if err := timeline.ExportJSONL(path, tr); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) < 2 {
		t.Fatalf("lines = %d, want >= 2", len(lines))
	}
	if !strings.Contains(lines[0], `"timeline.header"`) {
		t.Fatalf("header = %s", lines[0])
	}
}

func TestFormatCollapsed(t *testing.T) {
	entries := []timeline.Entry{
		{ID: "turn-1", Kind: timeline.KindTurn, State: timeline.StateCompleted, TurnID: "turn-abcdef", DurationMs: int64Ptr(1500)},
		{ID: "tool-1", Kind: timeline.KindTool, State: timeline.StateFailed, Name: "bash", CallID: "call-xyz", Error: "boom"},
	}
	got := timeline.FormatCollapsed(entries, 0)
	if !strings.Contains(got, "completed") || !strings.Contains(got, "turn") {
		t.Fatalf("collapsed missing turn: %q", got)
	}
	if !strings.Contains(got, "failed") || !strings.Contains(got, "bash") {
		t.Fatalf("collapsed missing tool: %q", got)
	}
}

func TestConcurrentObserveStableIDs(t *testing.T) {
	b := timeline.NewBuilder(timeline.Options{SessionID: "root"})
	const n = 50
	var wg sync.WaitGroup
	wg.Add(n)
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			turnID := "turn-" + string(rune('A'+i%26)) + "-" + itoa(i)
			corr := protocol.Correlation{SessionID: "root", TurnID: turnID}
			t0 := base.Add(time.Duration(i) * time.Millisecond)
			b.Observe(protocol.TurnStarted{Correlation: corr}, t0)
			b.Observe(protocol.ToolCallBegin{
				Correlation: corr,
				CallID:      "call-" + itoa(i),
				Name:        "read",
				Args:        json.RawMessage(`{}`),
			}, t0.Add(time.Millisecond))
			b.Observe(protocol.ToolCallEnd{
				Correlation: corr,
				CallID:      "call-" + itoa(i),
				Output:      "ok",
			}, t0.Add(2*time.Millisecond))
			b.Observe(protocol.TurnCompleted{Correlation: corr}, t0.Add(3*time.Millisecond))
		}()
	}
	wg.Wait()

	snap := b.Snapshot()
	ids := map[string]struct{}{}
	turnIDs := map[string]struct{}{}
	callIDs := map[string]struct{}{}
	for _, e := range snap {
		if _, ok := ids[e.ID]; ok {
			t.Fatalf("duplicate entry id %q", e.ID)
		}
		ids[e.ID] = struct{}{}
		if e.Kind == timeline.KindTurn {
			if e.TurnID == "" {
				t.Fatal("empty turn id")
			}
			if _, ok := turnIDs[e.TurnID]; ok {
				t.Fatalf("duplicate turn id %q", e.TurnID)
			}
			turnIDs[e.TurnID] = struct{}{}
		}
		if e.Kind == timeline.KindTool {
			if e.CallID == "" {
				t.Fatal("empty call id")
			}
			key := e.SessionID + "/" + e.CallID
			if _, ok := callIDs[key]; ok {
				t.Fatalf("duplicate call id %q", key)
			}
			callIDs[key] = struct{}{}
		}
	}
	if len(turnIDs) != n {
		t.Fatalf("turns = %d, want %d", len(turnIDs), n)
	}
	if len(callIDs) != n {
		t.Fatalf("tools = %d, want %d", len(callIDs), n)
	}
}

func TestEngineErrorFailsTurn(t *testing.T) {
	base := time.Now().UTC()
	corr := protocol.Correlation{SessionID: "s", TurnID: "t-fail"}
	tr := timeline.Build([]timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: corr}},
		{Time: base.Add(time.Millisecond), Event: protocol.EngineError{Correlation: corr, Message: "boom"}},
	}, timeline.Options{})
	if tr.Summary.Failed != 1 {
		t.Fatalf("failed = %d", tr.Summary.Failed)
	}
	for _, e := range tr.Entries {
		if e.Kind == timeline.KindTurn && e.State != timeline.StateFailed {
			t.Fatalf("turn state = %s", e.State)
		}
	}
}

func TestBuildVerificationSpan(t *testing.T) {
	base := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	corr := protocol.Correlation{SessionID: "s", TurnID: "t-v"}
	events := []timeline.TimedEvent{
		{Time: base, Event: protocol.TurnStarted{Correlation: corr}},
		{Time: base.Add(10 * time.Millisecond), Event: protocol.VerificationStarted{
			Correlation: corr,
			Scope:       protocol.VerificationScopeTurn,
			GateCount:   2,
		}},
		{Time: base.Add(50 * time.Millisecond), Event: protocol.VerificationCompleted{
			Correlation: corr,
			Scope:       protocol.VerificationScopeTurn,
			Report: protocol.VerificationReport{
				Passed:   false,
				Claimed:  true,
				Verified: false,
				Summary:  "verification failed: unit",
				Checks: []protocol.VerificationCheck{
					{Name: "unit", Kind: "cmd", Passed: false, Error: "exit 1"},
				},
			},
		}},
		{Time: base.Add(60 * time.Millisecond), Event: protocol.TurnCompleted{
			Correlation: corr,
			StopReason:  "end_turn",
			Verification: &protocol.VerificationReport{
				Passed: false, Claimed: true, Verified: false, Summary: "verification failed: unit",
			},
		}},
	}
	tr := timeline.Build(events, timeline.Options{SessionID: "s"})
	if tr.Summary.Verifies != 1 || tr.Summary.Failed < 1 {
		t.Fatalf("summary = %+v", tr.Summary)
	}
	var verify *timeline.Entry
	for i := range tr.Entries {
		if tr.Entries[i].Kind == timeline.KindVerify {
			verify = &tr.Entries[i]
			break
		}
	}
	if verify == nil {
		t.Fatal("missing verify entry")
	}
	if verify.State != timeline.StateFailed {
		t.Fatalf("state = %s", verify.State)
	}
	if verify.Name != protocol.VerificationScopeTurn {
		t.Fatalf("name = %q", verify.Name)
	}
	if verify.OutputPreview == "" || verify.Error == "" {
		t.Fatalf("preview/error empty: %+v", verify)
	}
	if verify.ParentID == "" {
		t.Fatalf("expected parent turn link: %+v", verify)
	}
}

func int64Ptr(v int64) *int64 { return &v }

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}
