package engine

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/debugmode"
	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/harness/tool"
	"github.com/jonathanung/strike/pkg/protocol"
)

func newAgentDebugEngine(t *testing.T, reg *tool.Registry) *Engine {
	t.Helper()
	return New(Options{
		SessionID: "s1",
		WorkDir:   t.TempDir(),
		Registry:  reg,
		Agents:    []Agent{{Name: "build"}},
		Rules:     []permission.Ruleset{permission.Defaults()},
	})
}

func drainDebugUpdates(e *Engine) []protocol.DebugUpdated {
	var out []protocol.DebugUpdated
	for {
		select {
		case ev := <-e.events:
			if d, ok := ev.(protocol.DebugUpdated); ok {
				out = append(out, d)
			}
		default:
			return out
		}
	}
}

func twoHypotheses() tool.DebugOp {
	return tool.DebugOp{Action: protocol.DebugActionHypotheses, Hypotheses: []tool.DebugHypothesis{
		{ID: "h1", Statement: "off by one", Confirms: "index == len", Refutes: "index < len"},
		{ID: "h2", Statement: "stale cache"},
	}}
}

func TestAgentDebugControlNeedsActiveSession(t *testing.T) {
	e := newAgentDebugEngine(t, nil)
	_, err := e.agentDebugControl(protocol.Correlation{}, twoHypotheses())
	if err == nil || !strings.Contains(err.Error(), "no debug session is active") {
		t.Fatalf("err = %v", err)
	}
	if got := drainDebugUpdates(e); len(got) != 0 {
		t.Fatalf("emitted %+v without a session", got)
	}
}

func TestAgentDebugControlRefusesUserOnlyActions(t *testing.T) {
	e := newAgentDebugEngine(t, nil)
	s := debugmode.New()
	e.debug.Store(s)
	if err := s.SetHypotheses([]debugmode.Hypothesis{{ID: "h1", Statement: "a"}, {ID: "h2", Statement: "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Attach("h1", debugmode.Evidence{Text: "seen"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Mark("h1", debugmode.StatusConfirmed); err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{
		protocol.DebugActionStart, protocol.DebugActionUnlock, protocol.DebugActionFixed,
		protocol.DebugActionEnd, protocol.DebugActionScan, "", "bogus",
	} {
		t.Run(action, func(t *testing.T) {
			_, err := e.agentDebugControl(protocol.Correlation{}, tool.DebugOp{Action: action, Text: "override reason"})
			if err == nil || !strings.Contains(err.Error(), "not allowed") || !strings.Contains(err.Error(), "user's decisions") {
				t.Fatalf("err = %v, want refusal with reason", err)
			}
		})
	}
	if s.Phase() != debugmode.PhaseHypothesis {
		t.Fatalf("phase = %s, agent must not move the session", s.Phase())
	}
	if snap := s.Snapshot(); snap.Override != "" || snap.Fixed {
		t.Fatalf("snapshot = %+v, agent must not override or mark fixed", snap)
	}
	if got := drainDebugUpdates(e); len(got) != 0 {
		t.Fatalf("refusals emitted %+v", got)
	}
}

func TestAgentDebugControlDrivesHypothesesEvidenceMark(t *testing.T) {
	e := newAgentDebugEngine(t, nil)
	e.debug.Store(debugmode.New())
	corr := protocol.Correlation{SessionID: "s1", TurnID: "t1"}

	steps := []struct {
		op      tool.DebugOp
		wantErr string
		wantOut string
	}{
		{op: tool.DebugOp{Action: protocol.DebugActionHypotheses, Hypotheses: []tool.DebugHypothesis{{ID: "h1", Statement: "only one"}}}, wantErr: "need 2"},
		{op: twoHypotheses(), wantOut: "h1=open(0 evidence), h2=open(0 evidence)"},
		{op: tool.DebugOp{Action: protocol.DebugActionMark, HypothesisID: "h1", Status: "confirmed"}, wantErr: "no evidence"},
		{op: tool.DebugOp{Action: protocol.DebugActionEvidence, HypothesisID: "h1", Source: "repro", Text: "index 3 of 3"}, wantOut: "h1=open(1 evidence)"},
		{op: tool.DebugOp{Action: protocol.DebugActionMark, HypothesisID: "h1", Status: "confirmed"}, wantOut: "h1=confirmed(1 evidence)"},
		{op: tool.DebugOp{Action: protocol.DebugActionMark, HypothesisID: "h2", Status: "refuted"}, wantOut: "h2=refuted(0 evidence)"},
	}
	for i, st := range steps {
		out, err := e.agentDebugControl(corr, st.op)
		if st.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), st.wantErr) {
				t.Fatalf("step %d: err = %v, want %q", i, err, st.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if !strings.Contains(out, st.wantOut) || !strings.Contains(out, "Debug phase hypothesis") {
			t.Fatalf("step %d: out = %q, want %q", i, out, st.wantOut)
		}
	}

	got := drainDebugUpdates(e)
	if len(got) != 4 {
		t.Fatalf("debug.updated events = %d, want 4 (successes only): %+v", len(got), got)
	}
	first := got[0]
	if first.Action != protocol.DebugActionHypotheses || first.TurnID != "t1" || first.Error != "" ||
		len(first.Hypotheses) != 2 || first.Hypotheses[0].Rank != 1 || first.Hypotheses[0].Confirms != "index == len" {
		t.Fatalf("hypotheses event = %+v", first)
	}
	last := got[3]
	if last.Phase != string(debugmode.PhaseHypothesis) || last.Hypotheses[0].Status != "confirmed" || last.Hypotheses[1].Status != "refuted" {
		t.Fatalf("last event = %+v", last)
	}
	// Confirming never unlocks fix by itself: the user's Unlock stays explicit.
	if p := e.debug.Load().Phase(); p != debugmode.PhaseHypothesis {
		t.Fatalf("phase = %s after confirm, want hypothesis", p)
	}
}

func TestDebugPromptLayerFollowsPhase(t *testing.T) {
	e := newAgentDebugEngine(t, nil)
	if _, ok := e.debugPromptLayer(); ok {
		t.Fatal("no session: no debug layer")
	}
	for _, l := range e.composeSystemLayers() {
		if strings.HasPrefix(l.Source, "debug:") {
			t.Fatalf("debug layer without a session: %+v", l)
		}
	}
	s := debugmode.New()
	e.debug.Store(s)
	layer, ok := e.debugPromptLayer()
	if !ok || layer.Source != "debug:hypothesis" || layer.Kind != protocol.PromptLayerPhase {
		t.Fatalf("layer = %+v ok=%v", layer, ok)
	}
	for _, want := range []string{"phase: hypothesis", debugmode.Marker, "debug_session", "Unlock", "cannot unlock"} {
		if !strings.Contains(layer.Text, want) {
			t.Errorf("hypothesis note missing %q:\n%s", want, layer.Text)
		}
	}
	found := false
	for _, l := range e.composeSystemLayers() {
		if l.Source == "debug:hypothesis" {
			found = true
		}
	}
	if !found {
		t.Fatal("debug layer not composed into the system prompt")
	}

	if err := s.UnlockFix("user override"); err != nil {
		t.Fatal(err)
	}
	layer, ok = e.debugPromptLayer()
	if !ok || layer.Source != "debug:fix" || !strings.Contains(layer.Text, "phase: fix") || !strings.Contains(layer.Text, debugmode.Marker) {
		t.Fatalf("fix layer = %+v ok=%v", layer, ok)
	}
}

func TestDebugSessionActivationFollowsSession(t *testing.T) {
	reg := tool.NewRegistry(tool.NewRead(), namedStub{name: debugSessionTool})
	reg.SetDeferLoading(true)
	e := newAgentDebugEngine(t, reg)
	for _, f := range e.applyWorkflowToolActivation() {
		if f == activationFamilyDebug {
			t.Fatal("debug family active without a session")
		}
	}
	if reg.Discovered(debugSessionTool) {
		t.Fatal("debug_session discovered without a session")
	}
	e.debug.Store(debugmode.New())
	has := false
	for _, f := range e.applyWorkflowToolActivation() {
		has = has || f == activationFamilyDebug
	}
	if !has || !reg.Discovered(debugSessionTool) {
		t.Fatalf("debug family not activated (has=%v)", has)
	}
}
