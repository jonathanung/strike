package replay_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/replay"
	"github.com/jonathanung/strike-cli/internal/persist/session"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func sampleEvents() []protocol.Event {
	corr := protocol.Correlation{SessionID: "sess-1", TurnID: "turn-1"}
	return []protocol.Event{
		protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "sess-1"}, Provider: "echo", Model: "echo"},
		protocol.AgentSelected{Correlation: protocol.Correlation{SessionID: "sess-1"}, Name: "build"},
		protocol.AutonomySelected{Correlation: protocol.Correlation{SessionID: "sess-1"}, Mode: protocol.AutonomySupervised},
		protocol.PermissionModeSelected{Correlation: protocol.Correlation{SessionID: "sess-1"}, Mode: protocol.PermissionModeDefault},
		protocol.UserMessage{Correlation: corr, Text: "run echo hi"},
		protocol.TurnStarted{Correlation: corr},
		protocol.TextDelta{Correlation: corr, Text: "working…"},
		protocol.ToolCallBegin{Correlation: corr, CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo hi"}`)},
		protocol.ToolCallEnd{Correlation: corr, CallID: "c1", Title: "bash", Output: "hi\n"},
		protocol.ToolCallBegin{Correlation: corr, CallID: "c2", Name: "webfetch", Args: json.RawMessage(`{"url":"https://example.com"}`)},
		protocol.ToolCallEnd{Correlation: corr, CallID: "c2", Title: "webfetch", Output: "ok", IsError: false},
		protocol.UsageReported{
			Correlation: protocol.Correlation{SessionID: "sess-1", TurnID: "turn-1", ProviderRequestID: "preq-1", Attempt: 1},
			Input:       protocol.TokenCount{N: 10, Known: true},
			Output:      protocol.TokenCount{N: 5, Known: true},
			Source:      "estimated",
		},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn", Files: []protocol.TurnFileChange{{Path: "a.go", Kind: "update"}}},
		protocol.ChildCompleted{
			Correlation: protocol.Correlation{SessionID: "child-1", ParentSessionID: "sess-1", Depth: 1},
			Status:      protocol.ChildStatusCompleted,
			Summary:     "done",
			Handoff: protocol.CompletionHandoff{
				Summary:      "child done",
				FilesChanged: []string{"b.go"},
				Findings:     []string{"note"},
			},
			Verification: &protocol.VerificationReport{
				Passed:   true,
				Claimed:  true,
				Verified: true,
				Summary:  "gates ok",
				Checks: []protocol.VerificationCheck{
					{Name: "unit", Kind: "cmd", Value: "go test", Passed: true},
				},
			},
		},
	}
}

func TestBuildRecordingExtractsSettingsToolsMarkersHandoffs(t *testing.T) {
	fixed := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	rec := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{
		SessionID: "sess-1",
		Clock:     func() time.Time { return fixed },
	})
	if rec.SchemaVersion != replay.RecordingSchemaVersion {
		t.Fatalf("schema = %q", rec.SchemaVersion)
	}
	if !rec.Redacted || rec.SideEffectsReplayed {
		t.Fatalf("redacted=%v sideEffects=%v", rec.Redacted, rec.SideEffectsReplayed)
	}
	if rec.RecordedAt != fixed {
		t.Fatalf("recordedAt = %v", rec.RecordedAt)
	}
	if rec.Settings.Provider != "echo" || rec.Settings.Model != "echo" || rec.Settings.Agent != "build" {
		t.Fatalf("settings = %+v", rec.Settings)
	}
	if rec.Settings.ToolsDigest == "" {
		t.Fatal("expected toolsDigest")
	}
	if rec.ExitStatus != "end_turn" || rec.Turns != 1 {
		t.Fatalf("exit=%q turns=%d", rec.ExitStatus, rec.Turns)
	}
	if len(rec.ToolCalls) != 2 {
		t.Fatalf("tools = %+v", rec.ToolCalls)
	}
	if rec.ToolCalls[0].Name != "bash" || rec.ToolCalls[1].Name != "webfetch" {
		t.Fatalf("tool names = %+v", rec.ToolCalls)
	}
	if len(rec.ToolResults) != 2 || rec.ToolResults[0].OutputDigest == "" {
		t.Fatalf("tool results = %+v", rec.ToolResults)
	}
	// Nondeterministic: text.delta (model) + webfetch (network)
	var sawModel, sawNet bool
	for _, m := range rec.Markers {
		if m.Kind == replay.MarkerModel {
			sawModel = true
		}
		if m.Kind == replay.MarkerNetwork && m.ToolIndex == 1 {
			sawNet = true
		}
	}
	if !sawModel || !sawNet {
		t.Fatalf("markers = %+v (model=%v net=%v)", rec.Markers, sawModel, sawNet)
	}
	if len(rec.ProviderAttempts) != 1 || rec.ProviderAttempts[0].ProviderRequestID != "preq-1" {
		t.Fatalf("provider = %+v", rec.ProviderAttempts)
	}
	if len(rec.Handoffs) != 1 || rec.Handoffs[0].Summary != "child done" {
		t.Fatalf("handoffs = %+v", rec.Handoffs)
	}
	if len(rec.Verifications) != 1 || !rec.Verifications[0].Passed {
		t.Fatalf("verifications = %+v", rec.Verifications)
	}
	// files from turn + handoff
	joined := strings.Join(rec.FilesChanged, ",")
	if !strings.Contains(joined, "a.go") || !strings.Contains(joined, "b.go") {
		t.Fatalf("files = %v", rec.FilesChanged)
	}
}

func TestBuildRecordingRedactsSecrets(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s", TurnID: "t"}
	events := []protocol.Event{
		protocol.UserMessage{Correlation: corr, Text: "token sk-ant-api03-SECRETVALUE1234567890abcd"},
		protocol.ToolCallBegin{Correlation: corr, CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo sk-ant-api03-SECRETVALUE1234567890abcd"}`)},
		protocol.ToolCallEnd{Correlation: corr, CallID: "c1", Title: "bash", Output: "sk-ant-api03-SECRETVALUE1234567890abcd\n"},
	}
	rec := replay.BuildRecording(events, replay.RecordingOptions{SessionID: "s"})
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "SECRETVALUE") {
		t.Fatalf("secret leaked into recording: %s", raw)
	}
	if len(rec.UserInputs) != 1 || strings.Contains(rec.UserInputs[0], "SECRETVALUE") {
		t.Fatalf("user input not redacted: %q", rec.UserInputs)
	}
}

func TestWriteLoadRecordingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run.json")
	rec := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{
		SessionID: "sess-1",
		Clock:     func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err := replay.WriteRecording(path, rec); err != nil {
		t.Fatal(err)
	}
	got, err := replay.LoadRecording(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != rec.SessionID || got.Settings.Model != rec.Settings.Model {
		t.Fatalf("got %+v", got)
	}
	if len(got.ToolCalls) != len(rec.ToolCalls) {
		t.Fatalf("tools %d vs %d", len(got.ToolCalls), len(rec.ToolCalls))
	}
}

func TestBuildRecordingFromJSONLAndResult(t *testing.T) {
	// Golden corpus path
	path := filepath.Join("testdata", "bash-run.jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Skip(err)
	}
	rec, err := replay.BuildRecordingFromJSONL(path, replay.RecordingOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if rec.EventCount == 0 || len(rec.UserInputs) == 0 {
		t.Fatalf("empty recording from golden: %+v", rec)
	}
	if len(rec.ToolCalls) != 1 || rec.ToolCalls[0].Name != "bash" {
		t.Fatalf("tools = %+v", rec.ToolCalls)
	}

	res, err := replay.Run(context.Background(), rec.UserInputs, replay.Options{WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	fromRes := replay.BuildRecordingFromResult(res, replay.RecordingOptions{})
	if err := replay.DiffToolCalls(rec.ToolCalls, fromRes.ToolCalls); err != nil {
		t.Fatalf("result recording tools: %v", err)
	}
}

func TestCompareRecordingsEqualAndDiverge(t *testing.T) {
	a := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{SessionID: "a"})
	b := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{SessionID: "b"})
	// Session ids differ but compare ignores them for tools/settings content.
	rep := replay.CompareRecordings(a, b, replay.CompareOptions{})
	if !rep.Equal() {
		t.Fatalf("expected equal:\n%s", replay.FormatCompareReport(rep))
	}

	// Diverge tools
	b.ToolCalls = append([]replay.ToolCall{}, b.ToolCalls...)
	b.ToolCalls[0] = replay.ToolCall{Name: "read", Args: json.RawMessage(`{"path":"x"}`)}
	rep = replay.CompareRecordings(a, b, replay.CompareOptions{})
	if rep.Equal() {
		t.Fatal("expected tool divergence")
	}
	if rep.ToolSequence == "" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestCompareIgnoresNondeterministicTools(t *testing.T) {
	a := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{})
	b := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{})
	// Change only webfetch args (index 1, marked network).
	b.ToolCalls = append([]replay.ToolCall{}, a.ToolCalls...)
	b.ToolCalls[1] = replay.ToolCall{Name: "webfetch", Args: json.RawMessage(`{"url":"https://other.example"}`)}

	strict := replay.CompareRecordings(a, b, replay.CompareOptions{})
	if strict.Equal() {
		t.Fatal("strict should fail on webfetch args")
	}
	ignore := replay.CompareRecordings(a, b, replay.CompareOptions{IgnoreNondeterministic: true})
	if !ignore.Equal() {
		t.Fatalf("ignore nondeterministic should pass:\n%s", replay.FormatCompareReport(ignore))
	}

	// Drop webfetch entirely on got: toolsDigest differs but ignore mode must
	// still pass when remaining deterministic tools match.
	b2 := a
	b2.ToolCalls = a.ToolCalls[:1:1]
	b2.ToolResults = a.ToolResults[:1:1]
	b2.Settings.ToolsDigest = "different"
	// Keep markers so tool index 1 is still known nondeterministic on want.
	ignoreDrop := replay.CompareRecordings(a, b2, replay.CompareOptions{IgnoreNondeterministic: true})
	if !ignoreDrop.Equal() {
		t.Fatalf("ignore should pass when only nondeterministic tools differ:\n%s", replay.FormatCompareReport(ignoreDrop))
	}
}

func TestCompareHandoffAndGateFields(t *testing.T) {
	a := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{})
	b := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{})
	b.Handoffs = append([]replay.HandoffSnapshot{}, a.Handoffs...)
	b.Handoffs[0].Summary = "different"
	b.Verifications = append([]replay.VerificationSnapshot{}, a.Verifications...)
	b.Verifications[0].Passed = false

	rep := replay.CompareRecordings(a, b, replay.CompareOptions{IgnoreNondeterministic: true})
	if rep.Equal() {
		t.Fatal("expected handoff/gate divergence")
	}
	var sawHandoff, sawGate bool
	for _, d := range rep.Divergences {
		if strings.Contains(d.Path, "handoffs") {
			sawHandoff = true
		}
		if strings.Contains(d.Path, "verifications") {
			sawGate = true
		}
	}
	if !sawHandoff || !sawGate {
		t.Fatalf("divergences = %+v", rep.Divergences)
	}
}

func TestDetectReplayDivergenceGolden(t *testing.T) {
	path := filepath.Join("testdata", "bash-run.jsonl")
	events, err := replay.LoadJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := replay.DetectReplayDivergence(context.Background(), events, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Equal() {
		t.Fatalf("golden divergence:\n%s\nwant tools=%+v got=%+v",
			replay.FormatCompareReport(rep), rep.WantTools, rep.GotTools)
	}
}

func TestDetectReplayDivergenceCatchesToolDrift(t *testing.T) {
	// Golden expects bash; feed a plain-echo golden against bash-run inputs by
	// mutating the golden tool list expectation via a crafted event list.
	events := []protocol.Event{
		protocol.UserMessage{Text: "run echo hello-strike"},
		protocol.ToolCallBegin{Name: "read", Args: json.RawMessage(`{"path":"nope"}`)},
		protocol.TurnCompleted{StopReason: "end_turn"},
	}
	rep, err := replay.DetectReplayDivergence(context.Background(), events, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Equal() {
		t.Fatal("expected divergence when golden tools differ from echo replay")
	}
	if rep.ToolSequence == "" && len(rep.Divergences) == 0 {
		t.Fatalf("report = %+v", rep)
	}
}

func TestBranchFromEventNoSideEffects(t *testing.T) {
	dir := t.TempDir()
	m := session.NewManager(dir)
	root, err := m.Create(session.CreateOptions{Title: "src", ProjectKey: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	evs := sampleEvents()
	// Fix session ids on events for append consistency.
	for i := range evs {
		switch e := evs[i].(type) {
		case protocol.ModelSelected:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.UserMessage:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.TurnStarted:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.TurnCompleted:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.ToolCallBegin:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.ToolCallEnd:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.TextDelta:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.UsageReported:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.AgentSelected:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.AutonomySelected:
			e.SessionID = root.ID
			evs[i] = e
		case protocol.PermissionModeSelected:
			e.SessionID = root.ID
			evs[i] = e
		}
	}
	for _, ev := range evs {
		if err := m.Append(root.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	srcBefore, err := m.Replay(root.ID)
	if err != nil {
		t.Fatal(err)
	}

	// Branch after first 5 events (through user.message).
	br, err := replay.BranchFromEvent(m, root.ID, replay.BranchKeep(5))
	if err != nil {
		t.Fatal(err)
	}
	if br.SideEffectsReplayed {
		t.Fatal("side effects must not be replayed")
	}
	if br.KeepEvents != 5 || br.AtEventIndex != 4 {
		t.Fatalf("keep=%d at=%d", br.KeepEvents, br.AtEventIndex)
	}
	if br.Info.ID == root.ID {
		t.Fatal("fork id must differ")
	}
	meta, err := session.ReadMeta(dir, br.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.ForkedFrom != root.ID {
		t.Fatalf("ForkedFrom = %q", meta.ForkedFrom)
	}
	forkEvs, err := m.Replay(br.Info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(forkEvs) != 5 {
		t.Fatalf("fork events = %d", len(forkEvs))
	}
	srcAfter, err := m.Replay(root.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(srcAfter) != len(srcBefore) {
		t.Fatalf("source mutated: %d → %d", len(srcBefore), len(srcAfter))
	}
	if br.Recording.SideEffectsReplayed || br.Recording.EventCount != 5 {
		t.Fatalf("recording = %+v", br.Recording)
	}

	// Turn-based branch
	br2, err := replay.BranchFromEvent(m, root.ID, replay.BranchAtTurn("turn-1"))
	if err != nil {
		t.Fatal(err)
	}
	// turn-1 completes at TurnCompleted index in sampleEvents
	if br2.KeepEvents < 5 {
		t.Fatalf("turn branch keep=%d", br2.KeepEvents)
	}

	// Event ref
	keep, err := replay.ResolveBranchKeep(evs, replay.BranchRef("event:2"))
	if err != nil || keep != 3 {
		t.Fatalf("event:2 keep=%d err=%v", keep, err)
	}
	keep, err = replay.ResolveBranchKeep(evs, replay.BranchRef("keep:0"))
	if err != nil || keep != 0 {
		t.Fatalf("keep:0 = %d err=%v", keep, err)
	}

	if _, err := replay.BranchFromEvent(m, root.ID, replay.BranchSelector{}); err == nil {
		t.Fatal("empty selector should error")
	}
	if _, err := replay.BranchFromEvent(nil, root.ID, replay.BranchKeep(1)); err == nil {
		t.Fatal("nil manager should error")
	}
	if _, err := replay.BranchFromEvent(m, root.ID, replay.BranchAtEvent(999)); err == nil {
		t.Fatal("oob index should error")
	}
}

func TestResolveBranchKeepTurnMissing(t *testing.T) {
	_, err := replay.ResolveBranchKeep(sampleEvents(), replay.BranchAtTurn("nope"))
	if err == nil {
		t.Fatal("expected missing turn error")
	}
}

func TestFormatCompareReport(t *testing.T) {
	a := replay.BuildRecording(sampleEvents(), replay.RecordingOptions{})
	b := a
	b.ExitStatus = "error"
	rep := replay.CompareRecordings(a, b, replay.CompareOptions{IgnoreNondeterministic: true})
	s := replay.FormatCompareReport(rep)
	if !strings.Contains(s, "equal: false") || !strings.Contains(s, "exitStatus") {
		t.Fatalf("format = %q", s)
	}
	ok := replay.CompareRecordings(a, a, replay.CompareOptions{})
	if !strings.Contains(replay.FormatCompareReport(ok), "equal: true") {
		t.Fatal(replay.FormatCompareReport(ok))
	}
}
