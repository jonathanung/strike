package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/jonathanung/strike/harness/debugmode"
	"github.com/jonathanung/strike/harness/engine"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/provider"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

// recordingEdit stands in for the edit builtin so tests can prove whether
// the phase gate let a call reach Execute.
type recordingEdit struct {
	mu    sync.Mutex
	calls []string
}

func (t *recordingEdit) Name() string            { return "edit" }
func (t *recordingEdit) Description() string     { return "test edit" }
func (t *recordingEdit) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *recordingEdit) Execute(_ context.Context, args json.RawMessage, _ *tool.Context) (tool.Result, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, string(args))
	return tool.Result{Output: "edited"}, nil
}

func (t *recordingEdit) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.calls)
}

func editCall(id, oldText, newText string) provider.ToolCall {
	args, _ := json.Marshal(map[string]string{"filePath": "a.go", "oldString": oldText, "newString": newText})
	return provider.ToolCall{ID: id, Name: "edit", Args: args}
}

func newDebugEngine(t *testing.T, workDir string, prov provider.Provider, initial *protocol.DebugUpdated, tools ...tool.Tool) *engine.Engine {
	t.Helper()
	return engine.New(engine.Options{
		Select:              func(string) (provider.Provider, string, error) { return prov, "test-model", nil },
		InitialProvider:     "scripted",
		Registry:            tool.NewRegistry(tools...),
		WorkDir:             workDir,
		Rules:               []permission.Ruleset{permission.Defaults()},
		SandboxAllowDegrade: true,
		InitialDebug:        initial,
	})
}

func debugOp(t *testing.T, eng *engine.Engine, op protocol.DebugControl) protocol.DebugUpdated {
	t.Helper()
	eng.Ops() <- op
	ev := receiveEvent(t, eng.Events(), func(ev protocol.Event) bool {
		_, ok := ev.(protocol.DebugUpdated)
		return ok
	})
	return ev.(protocol.DebugUpdated)
}

func toolEnds(events []protocol.Event) map[string]protocol.ToolCallEnd {
	out := map[string]protocol.ToolCallEnd{}
	for _, ev := range events {
		if end, ok := ev.(protocol.ToolCallEnd); ok {
			out[end.CallID] = end
		}
	}
	return out
}

func TestDebugGateDeniesUntaggedEditWithReason(t *testing.T) {
	fix := editCall("fix", "return a", "return b")
	probe := editCall("probe", "return a", "log(a) // "+debugmode.Marker+"\nreturn a")
	prov := newScriptedProvider(toolCallStep(fix, probe), completedStep("done"))
	edit := &recordingEdit{}
	eng := newDebugEngine(t, t.TempDir(), prov, nil, edit)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	if ev := debugOp(t, eng, protocol.DebugControl{Action: protocol.DebugActionStart}); ev.Error != "" || ev.Phase != string(debugmode.PhaseHypothesis) {
		t.Fatalf("start = %+v", ev)
	}
	eng.Ops() <- protocol.UserInput{Text: "find the bug"}
	_, events := collectThroughTurnCompleted(t, eng.Events())
	ends := toolEnds(events)

	denied := ends["fix"]
	if !denied.IsError || denied.ErrorCode != protocol.ErrorCodeBlocked {
		t.Fatalf("fix edit end = %+v, want blocked error", denied)
	}
	if !strings.Contains(denied.Output, "hypothesis phase allows only instrumentation") || !strings.Contains(denied.Output, "return b") {
		t.Fatalf("denial reason missing from transcript output: %q", denied.Output)
	}
	if allowed := ends["probe"]; allowed.IsError {
		t.Fatalf("tagged instrumentation edit was blocked: %+v", allowed)
	}
	if got := edit.count(); got != 1 {
		t.Fatalf("edit Execute calls = %d, want 1 (only the tagged probe)", got)
	}
}

func TestDebugLifecycleEndSweepsTaggedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("x\nprintln(1) // "+debugmode.Marker+"\ny\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng := newDebugEngine(t, dir, newScriptedProvider(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	start := debugOp(t, eng, protocol.DebugControl{Action: protocol.DebugActionStart})
	if len(start.Instrumentation) != 1 || start.Instrumentation[0].Path != "a.go" || start.Instrumentation[0].Line != 2 {
		t.Fatalf("start instrumentation = %+v", start.Instrumentation)
	}
	steps := []struct {
		op      protocol.DebugControl
		wantErr string
	}{
		{op: protocol.DebugControl{Action: protocol.DebugActionStart}, wantErr: "already active"},
		{op: protocol.DebugControl{Action: protocol.DebugActionUnlock}, wantErr: "no confirmed hypothesis"},
		{op: protocol.DebugControl{Action: protocol.DebugActionHypotheses, Hypotheses: []protocol.DebugHypothesis{
			{ID: "h1", Statement: "off by one"}, {ID: "h2", Statement: "stale cache"},
		}}},
		{op: protocol.DebugControl{Action: protocol.DebugActionMark, HypothesisID: "h1", Status: "confirmed"}, wantErr: "no evidence"},
		{op: protocol.DebugControl{Action: protocol.DebugActionEvidence, HypothesisID: "h1", Source: "repro", Text: "index 3 of 3"}},
		{op: protocol.DebugControl{Action: protocol.DebugActionMark, HypothesisID: "h1", Status: "confirmed"}},
		{op: protocol.DebugControl{Action: protocol.DebugActionEnd}, wantErr: "mark the bug fixed"},
		{op: protocol.DebugControl{Action: protocol.DebugActionUnlock}},
		{op: protocol.DebugControl{Action: protocol.DebugActionFixed}},
		{op: protocol.DebugControl{Action: "bogus"}, wantErr: "unknown action"},
	}
	for _, st := range steps {
		ev := debugOp(t, eng, st.op)
		if st.wantErr == "" && ev.Error != "" {
			t.Fatalf("%s: unexpected error %q", st.op.Action, ev.Error)
		}
		if st.wantErr != "" && !strings.Contains(ev.Error, st.wantErr) {
			t.Fatalf("%s: error = %q, want %q", st.op.Action, ev.Error, st.wantErr)
		}
	}

	end := debugOp(t, eng, protocol.DebugControl{Action: protocol.DebugActionEnd})
	if end.Error != "" || end.Phase != string(debugmode.PhaseClosed) {
		t.Fatalf("end = %+v", end)
	}
	if len(end.Removed) != 1 || end.Removed[0].Line != 2 || len(end.Instrumentation) != 0 {
		t.Fatalf("end removed=%+v remaining=%+v", end.Removed, end.Instrumentation)
	}
	if len(end.Hypotheses) != 2 || end.Hypotheses[0].Status != "confirmed" || len(end.Hypotheses[0].Evidence) != 1 {
		t.Fatalf("summary hypotheses = %+v", end.Hypotheses)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "x\ny\n" {
		t.Fatalf("file after sweep = %q", data)
	}
	if ev := debugOp(t, eng, protocol.DebugControl{Action: protocol.DebugActionScan}); !strings.Contains(ev.Error, "no debug session") {
		t.Fatalf("scan after end = %+v, want no-session error", ev)
	}
}

func TestDebugOverrideRecorded(t *testing.T) {
	eng := newDebugEngine(t, t.TempDir(), newScriptedProvider(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)
	debugOp(t, eng, protocol.DebugControl{Action: protocol.DebugActionStart})
	ev := debugOp(t, eng, protocol.DebugControl{Action: protocol.DebugActionUnlock, Text: "prod is down, ship it"})
	if ev.Error != "" || ev.Phase != string(debugmode.PhaseFix) || ev.Override != "prod is down, ship it" {
		t.Fatalf("unlock with override = %+v", ev)
	}
}

func TestRestoreDebugKeepsLastSuccessfulState(t *testing.T) {
	hyp := protocol.DebugUpdated{Action: protocol.DebugActionStart, Phase: "hypothesis"}
	refused := protocol.DebugUpdated{Action: protocol.DebugActionUnlock, Phase: "hypothesis", Error: "no confirmed hypothesis"}
	fix := protocol.DebugUpdated{Action: protocol.DebugActionUnlock, Phase: "fix", Override: "why"}
	tests := []struct {
		name   string
		events []protocol.Event
		want   *protocol.DebugUpdated
	}{
		{name: "none"},
		{name: "start", events: []protocol.Event{hyp}, want: &hyp},
		{name: "refusal ignored", events: []protocol.Event{hyp, refused}, want: &hyp},
		{name: "latest wins", events: []protocol.Event{hyp, fix, refused}, want: &fix},
		{name: "child ignored", events: []protocol.Event{protocol.DebugUpdated{Correlation: protocol.Correlation{ParentSessionID: "p", Depth: 1}, Phase: "fix"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := engine.Restore(tc.events).Debug
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("Debug = %+v, want nil", got)
			case tc.want != nil && (got == nil || got.Phase != tc.want.Phase || got.Override != tc.want.Override):
				t.Fatalf("Debug = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestInitialDebugResumesGate(t *testing.T) {
	tests := []struct {
		name       string
		initial    *protocol.DebugUpdated
		wantDenied bool
	}{
		{name: "hypothesis resumes gate", initial: &protocol.DebugUpdated{Phase: "hypothesis"}, wantDenied: true},
		{name: "fix phase allows edits", initial: &protocol.DebugUpdated{Phase: "fix"}},
		{name: "closed resumes nothing", initial: &protocol.DebugUpdated{Phase: "closed"}},
		{name: "no debug", initial: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			prov := newScriptedProvider(toolCallStep(editCall("e1", "a", "b")), completedStep("done"))
			edit := &recordingEdit{}
			eng := newDebugEngine(t, t.TempDir(), prov, tc.initial, edit)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go eng.Run(ctx)
			eng.Ops() <- protocol.UserInput{Text: "go"}
			_, events := collectThroughTurnCompleted(t, eng.Events())
			end := toolEnds(events)["e1"]
			if denied := end.ErrorCode == protocol.ErrorCodeBlocked; denied != tc.wantDenied {
				t.Fatalf("denied = %v, want %v (end %+v)", denied, tc.wantDenied, end)
			}
			if wantCalls := map[bool]int{true: 0, false: 1}[tc.wantDenied]; edit.count() != wantCalls {
				t.Fatalf("Execute calls = %d, want %d", edit.count(), wantCalls)
			}
		})
	}
}
