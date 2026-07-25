package engine_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/engine"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/echo"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestRestoreUserAssistantRoundTrip(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1", ProviderRequestID: "r1"}
	events := []protocol.Event{
		protocol.ModelSelected{Correlation: protocol.Correlation{SessionID: "s1"}, Provider: "echo", Model: "echo"},
		protocol.AgentSelected{Correlation: protocol.Correlation{SessionID: "s1"}, Name: "build"},
		protocol.UserMessage{Correlation: corr, Text: "hello"},
		protocol.TurnStarted{Correlation: corr},
		protocol.TextDelta{Correlation: corr, Text: "hi "},
		protocol.TextDelta{Correlation: corr, Text: "there"},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	}
	got := engine.Restore(events)
	want := []provider.Message{
		{Role: provider.RoleUser, Text: "hello"},
		{Role: provider.RoleAssistant, Text: "hi there"},
	}
	if !reflect.DeepEqual(got.Messages, want) {
		t.Fatalf("Messages = %#v, want %#v", got.Messages, want)
	}
	if got.Provider != "echo" || got.Model != "echo" || got.Agent != "build" {
		t.Fatalf("selections = %+v", got)
	}
}

func TestRestoreToolCallPair(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1", ProviderRequestID: "r1"}
	args := json.RawMessage(`{"command":"echo hi"}`)
	events := []protocol.Event{
		protocol.UserMessage{Correlation: corr, Text: "run it"},
		protocol.TextDelta{Correlation: corr, Text: "sure"},
		protocol.ToolCallBegin{Correlation: corr, CallID: "c1", Name: "bash", Args: args},
		protocol.ToolCallEnd{Correlation: corr, CallID: "c1", Title: "echo hi", Output: "hi\n"},
		protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
	}
	got := engine.Restore(events)
	if len(got.Messages) != 3 {
		t.Fatalf("len(Messages) = %d, want 3: %#v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != provider.RoleUser || got.Messages[0].Text != "run it" {
		t.Fatalf("user = %#v", got.Messages[0])
	}
	asst := got.Messages[1]
	if asst.Role != provider.RoleAssistant || asst.Text != "sure" || len(asst.ToolCalls) != 1 {
		t.Fatalf("assistant = %#v", asst)
	}
	if asst.ToolCalls[0].ID != "c1" || asst.ToolCalls[0].Name != "bash" {
		t.Fatalf("tool call = %#v", asst.ToolCalls[0])
	}
	tr := got.Messages[2]
	if tr.Role != provider.RoleTool || tr.ToolResult == nil || tr.ToolResult.CallID != "c1" || tr.ToolResult.Output != "hi\n" {
		t.Fatalf("tool result = %#v", tr)
	}
}

func TestRestoreSkipsChildLineage(t *testing.T) {
	root := protocol.Correlation{SessionID: "root", TurnID: "t1", ProviderRequestID: "r1"}
	child := protocol.Correlation{SessionID: "child", ParentSessionID: "root", Depth: 1, TurnID: "ct", ProviderRequestID: "cr"}
	events := []protocol.Event{
		protocol.UserMessage{Correlation: root, Text: "parent"},
		protocol.TextDelta{Correlation: root, Text: "ok"},
		protocol.UserMessage{Correlation: child, Text: "child prompt"},
		protocol.TextDelta{Correlation: child, Text: "child answer"},
		protocol.TurnCompleted{Correlation: root, StopReason: "end_turn"},
	}
	got := engine.Restore(events)
	want := []provider.Message{
		{Role: provider.RoleUser, Text: "parent"},
		{Role: provider.RoleAssistant, Text: "ok"},
	}
	if !reflect.DeepEqual(got.Messages, want) {
		t.Fatalf("Messages = %#v, want %#v", got.Messages, want)
	}
}

func TestRestoreCompactionRecord(t *testing.T) {
	// Build three user turns then a compaction that keeps the last two.
	var events []protocol.Event
	for i, text := range []string{"one", "two", "three"} {
		corr := protocol.Correlation{SessionID: "s", TurnID: "t" + string(rune('1'+i)), ProviderRequestID: "r" + string(rune('1'+i))}
		events = append(events,
			protocol.UserMessage{Correlation: corr, Text: text},
			protocol.TextDelta{Correlation: corr, Text: "a" + text},
			protocol.TurnCompleted{Correlation: corr, StopReason: "end_turn"},
		)
	}
	// Pre-compaction history: u,a,u,a,u,a (6 msgs). Compact with keep=2 user turns
	// drops first 2 messages (u+a for "one"), keeps 4, marker makes kept=5.
	events = append(events, protocol.CompactionCompleted{
		Correlation: protocol.Correlation{SessionID: "s"},
		Reason:      protocol.CompactionReasonManual,
		Removed:     2,
		Kept:        5,
	})
	got := engine.Restore(events)
	if len(got.Messages) != 5 {
		t.Fatalf("len = %d, want 5: %#v", len(got.Messages), got.Messages)
	}
	if got.Messages[0].Role != provider.RoleUser || !strings.Contains(got.Messages[0].Text, "compacted") {
		t.Fatalf("marker = %#v", got.Messages[0])
	}
	if got.Messages[1].Text != "two" || got.Messages[3].Text != "three" {
		t.Fatalf("tail = %#v", got.Messages)
	}
}

func TestRestoreIncompleteToolGetsSyntheticResult(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s", TurnID: "t", ProviderRequestID: "r"}
	events := []protocol.Event{
		protocol.UserMessage{Correlation: corr, Text: "go"},
		protocol.ToolCallBegin{Correlation: corr, CallID: "c1", Name: "bash", Args: json.RawMessage(`{}`)},
		// no ToolCallEnd — interrupted
		protocol.TurnCompleted{Correlation: corr, StopReason: "canceled"},
	}
	got := engine.Restore(events)
	if len(got.Messages) != 3 {
		t.Fatalf("len = %d, want 3: %#v", len(got.Messages), got.Messages)
	}
	tr := got.Messages[2]
	if tr.ToolResult == nil || !tr.ToolResult.IsError || tr.ToolResult.CallID != "c1" {
		t.Fatalf("synthetic result = %#v", tr)
	}
}

func TestRestorePhaseAutonomyAndAlwaysGrants(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1"}
	events := []protocol.Event{
		protocol.AutonomySelected{Correlation: corr, Mode: protocol.AutonomyAgent},
		protocol.AgentSelected{Correlation: corr, Name: "build"},
		protocol.PhaseChanged{
			Correlation: corr,
			Workflow:    "plan-implement",
			Phase:       "implement",
			Index:       1,
			Gate:        "agent",
		},
		protocol.PermissionAsked{
			Correlation: corr,
			RequestID:   "s1:perm_1",
			Permission:  "bash",
			Patterns:    []string{"git status"},
			Always:      []string{"git *"},
		},
		protocol.PermissionResolved{
			Correlation: corr,
			RequestID:   "s1:perm_1",
			Decision:    protocol.DecisionAlways,
		},
		protocol.PermissionAsked{
			Correlation: corr,
			RequestID:   "s1:perm_2",
			Permission:  "edit",
			Patterns:    []string{"foo.go"},
			Always:      []string{"*"},
		},
		protocol.PermissionResolved{
			Correlation: corr,
			RequestID:   "s1:perm_2",
			Decision:    protocol.DecisionOnce, // not session-scoped
		},
	}
	got := engine.Restore(events)
	if got.Autonomy != protocol.AutonomyAgent {
		t.Fatalf("Autonomy = %q, want agent", got.Autonomy)
	}
	if got.PhaseWorkflow != "plan-implement" || got.PhaseName != "implement" || got.PhaseIndex != 1 {
		t.Fatalf("phase = %q/%q/%d", got.PhaseWorkflow, got.PhaseName, got.PhaseIndex)
	}
	if len(got.AlwaysGrants) != 1 {
		t.Fatalf("AlwaysGrants = %#v, want one bash grant", got.AlwaysGrants)
	}
	g := got.AlwaysGrants[0]
	if g.Permission != "bash" || g.Pattern != "git *" || g.Action != permission.Allow {
		t.Fatalf("grant = %#v", g)
	}
}

func TestRestoreAlwaysGrantsClearedOnAgentSwitch(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1"}
	events := []protocol.Event{
		protocol.AgentSelected{Correlation: corr, Name: "build"},
		protocol.PermissionAsked{
			Correlation: corr, RequestID: "p1", Permission: "bash",
			Patterns: []string{"ls"}, Always: []string{"ls *"},
		},
		protocol.PermissionResolved{Correlation: corr, RequestID: "p1", Decision: protocol.DecisionAlways},
		protocol.AgentSelected{Correlation: corr, Name: "plan"},
	}
	got := engine.Restore(events)
	if got.Agent != "plan" {
		t.Fatalf("Agent = %q", got.Agent)
	}
	if len(got.AlwaysGrants) != 0 {
		t.Fatalf("AlwaysGrants after agent switch = %#v, want empty", got.AlwaysGrants)
	}
}

func TestRestoreClearedPhase(t *testing.T) {
	corr := protocol.Correlation{SessionID: "s1"}
	events := []protocol.Event{
		protocol.PhaseChanged{Correlation: corr, Workflow: "plan-implement", Phase: "plan", Index: 0},
		protocol.PhaseChanged{Correlation: corr}, // clear
	}
	got := engine.Restore(events)
	if got.PhaseName != "" || got.PhaseWorkflow != "" {
		t.Fatalf("phase after clear = %q/%q", got.PhaseWorkflow, got.PhaseName)
	}
}

func TestRestoreThenResumedModelRequest(t *testing.T) {
	// Live turn → capture events → Restore → new engine with InitialMessages →
	// one more turn; the provider must see prior history (no tool re-run).
	reg := tool.NewRegistry(tool.NewBash())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var liveEvents []protocol.Event
	eng1 := engine.New(engine.Options{
		SessionID:       "sess-resume",
		Select:          func(string) (provider.Provider, string, error) { return echo.New(), "echo", nil },
		Registry:        reg,
		InitialProvider: "echo",
		InitialModel:    "echo",
		Agents:          []engine.Agent{{Name: "build"}},
	})
	go eng1.Run(ctx)
	// Drain startup selections.
	deadline := time.After(2 * time.Second)
drain:
	for {
		select {
		case ev := <-eng1.Events():
			liveEvents = append(liveEvents, ev)
			if _, ok := ev.(protocol.AgentSelected); ok {
				break drain
			}
		case <-deadline:
			t.Fatal("timeout waiting for startup")
		}
	}
	eng1.Ops() <- protocol.UserInput{Text: "hello resume"}
	for {
		select {
		case ev := <-eng1.Events():
			liveEvents = append(liveEvents, ev)
			if _, ok := ev.(protocol.TurnCompleted); ok {
				goto restored
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout first turn")
		}
	}
restored:
	cancel()
	for range eng1.Events() {
	}

	restored := engine.Restore(liveEvents)
	if len(restored.Messages) < 2 {
		t.Fatalf("restored messages = %#v", restored.Messages)
	}

	var seen []provider.Message
	stub := &captureProvider{inner: echo.New(), onRequest: func(req provider.Request) {
		seen = append([]provider.Message(nil), req.Messages...)
	}}
	eng2 := engine.New(engine.Options{
		SessionID:       "sess-resume",
		Select:          func(string) (provider.Provider, string, error) { return stub, "echo", nil },
		Registry:        reg,
		InitialProvider: "echo",
		InitialModel:    "echo",
		InitialMessages: restored.Messages,
		InitialTitled:   restored.Titled,
		Agents:          []engine.Agent{{Name: "build"}},
	})
	if got := eng2.Messages(); !reflect.DeepEqual(got, restored.Messages) {
		t.Fatalf("seeded Messages = %#v, want %#v", got, restored.Messages)
	}

	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	go eng2.Run(ctx2)
	// Drain startup.
	for {
		select {
		case ev := <-eng2.Events():
			if _, ok := ev.(protocol.AgentSelected); ok {
				goto second
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout eng2 startup")
		}
	}
second:
	eng2.Ops() <- protocol.UserInput{Text: "and again"}
	for {
		select {
		case ev := <-eng2.Events():
			if _, ok := ev.(protocol.TurnCompleted); ok {
				goto check
			}
		case <-time.After(2 * time.Second):
			t.Fatal("timeout second turn")
		}
	}
check:
	cancel2()
	for range eng2.Events() {
	}
	if len(seen) < 3 {
		t.Fatalf("resumed request messages = %#v, want prior history + new user", seen)
	}
	if seen[0].Text != "hello resume" {
		t.Fatalf("first message = %#v, want restored user turn", seen[0])
	}
	last := seen[len(seen)-1]
	if last.Role != provider.RoleUser || last.Text != "and again" {
		t.Fatalf("last message = %#v, want new user input", last)
	}
}

type captureProvider struct {
	inner     provider.Provider
	onRequest func(provider.Request)
}

func (c *captureProvider) Name() string { return c.inner.Name() }

func (c *captureProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	if c.onRequest != nil {
		c.onRequest(req)
	}
	return c.inner.Stream(ctx, req)
}

func TestRestoreInitialPhaseAndAlwaysGrantsOnRun(t *testing.T) {
	// Resume snapshot: implement phase + session bash always-grant.
	// Engine must re-enter phase and honor the grant without prompting.
	dir := t.TempDir()
	args, _ := json.Marshal(map[string]any{"command": "git status"})
	call := provider.ToolCall{ID: "c1", Name: "bash", Args: args}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("ok"),
	)
	var asked int
	// Count PermissionAsked via event drain.
	eng := engine.New(engine.Options{
		SessionID:       "resume-phase-grants",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewBash()),
		WorkDir:         dir,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		InitialAgent:         "build",
		InitialAutonomy:      protocol.AutonomyAgent,
		InitialPhaseWorkflow: "plan-implement",
		InitialPhaseIndex:    1,
		InitialAlwaysGrants: permission.Ruleset{
			{Permission: "bash", Pattern: "git *", Action: permission.Allow},
		},
		Workflows: []config.Workflow{config.BuiltinPlanImplement()},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	var sawPhase, sawAutonomy bool
	deadline := time.After(5 * time.Second)
	for !sawPhase || !sawAutonomy {
		select {
		case <-deadline:
			t.Fatalf("timeout phase=%v autonomy=%v", sawPhase, sawAutonomy)
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.PhaseChanged:
				if e.Phase == "implement" && e.Workflow == "plan-implement" {
					sawPhase = true
				}
			case protocol.AutonomySelected:
				if e.Mode == protocol.AutonomyAgent {
					sawAutonomy = true
				}
			}
		}
	}

	eng.Ops() <- protocol.UserInput{Text: "run git"}
	deadline = time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout waiting for turn")
		case ev := <-eng.Events():
			switch ev.(type) {
			case protocol.PermissionAsked:
				asked++
			case protocol.TurnCompleted:
				if asked != 0 {
					t.Fatalf("PermissionAsked count = %d, want 0 (always grant restored)", asked)
				}
				return
			}
		}
	}
}

func TestRestorePlanPhaseDeniesWriteOnRun(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")
	if err := os.WriteFile(target, []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]any{"filePath": "out.txt", "content": "pwned\n"})
	call := provider.ToolCall{ID: "w1", Name: "write", Args: args}
	prov := newScriptedProvider(
		toolCallStep(call),
		completedStep("done"),
	)
	baseAllow := permission.Ruleset{
		{Permission: "write", Pattern: "*", Action: permission.Allow},
		{Permission: "edit", Pattern: "*", Action: permission.Allow},
		{Permission: "read", Pattern: "*", Action: permission.Allow},
	}
	eng := engine.New(engine.Options{
		SessionID:       "resume-plan-deny",
		Select:          func(string) (provider.Provider, string, error) { return prov, "model", nil },
		InitialProvider: "scripted",
		Registry:        tool.NewRegistry(tool.NewWrite()),
		WorkDir:         dir,
		Agents: []engine.Agent{
			{Name: "build"},
			{Name: "plan"},
		},
		// Intentionally start as build (as if agent switch lagged phase) and
		// restore plan phase — write must still be hard-denied.
		InitialAgent:         "build",
		InitialPhaseWorkflow: "plan-implement",
		InitialPhaseIndex:    0,
		Workflows:            []config.Workflow{config.BuiltinPlanImplement()},
		Rules:                []permission.Ruleset{permission.Defaults(), baseAllow},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	waitForEvent(t, eng, func(ev protocol.Event) bool {
		p, ok := ev.(protocol.PhaseChanged)
		return ok && p.Phase == "plan"
	})

	eng.Ops() <- protocol.UserInput{Text: "write it"}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		case ev := <-eng.Events():
			if end, ok := ev.(protocol.ToolCallEnd); ok {
				if !end.IsError {
					t.Fatalf("write succeeded under restored plan phase: %#v", end)
				}
				got, _ := os.ReadFile(target)
				if string(got) != "original\n" {
					t.Fatalf("file mutated: %q", got)
				}
				return
			}
		}
	}
}
