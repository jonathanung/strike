package engine_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/harness/engine"
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/enginebind"
	"github.com/jonathanung/strike-cli/internal/memory"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestExcludeDropsMemoryLayer(t *testing.T) {
	store := openTestMemory(t)
	if err := store.Put("pref.one", "MEMORY_MARKER_EXCLUDE", []string{memory.TagPreference}); err != nil {
		t.Fatal(err)
	}
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "s-exclude",
		WorkDir:         t.TempDir(),
		Memory:          enginebind.Memory(store),
		Agents:          []engine.Agent{{Name: "build", Prompt: "PERSONA_OK"}},
		InitialProvider: "scripted",
		InitialModel:    "m",
		Registry:        tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	// Confirm memory is present before exclude.
	eng.Ops() <- protocol.InspectEffectivePrompt{}
	before := waitEffectivePrompt(t, eng)
	if !layerKindsContain(before.Layers, protocol.PromptLayerMemory) {
		t.Fatalf("memory layer missing before exclude: %+v", kindsOf(before.Layers))
	}

	eng.Ops() <- protocol.SetContextControls{
		ExcludeKinds: []string{protocol.PromptLayerMemory},
		SetExclude:   true,
	}
	waitContextControls(t, eng)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	req := waitStreamRequest(t, eng, prov)
	waitTurnCompleted(t, eng)
	if strings.Contains(req.System, "MEMORY_MARKER_EXCLUDE") {
		t.Fatal("excluded memory layer still present in Stream system")
	}

	eng.Ops() <- protocol.InspectEffectivePrompt{}
	after := waitEffectivePrompt(t, eng)
	if layerKindsContain(after.Layers, protocol.PromptLayerMemory) {
		t.Fatalf("memory still in inspect layers: %+v", kindsOf(after.Layers))
	}
	if !stringSliceContains(after.ExcludedKinds, protocol.PromptLayerMemory) {
		t.Fatalf("ExcludedKinds = %v, want project_memory", after.ExcludedKinds)
	}
	for _, layer := range after.Layers {
		if layer.EstTokens <= 0 && layer.Chars > 0 {
			t.Fatalf("layer %s missing EstTokens (chars=%d)", layer.Kind, layer.Chars)
		}
	}
}

func TestPinRetainsMemoryUnderFitPressure(t *testing.T) {
	store := openTestMemory(t)
	// Large memory so optional shed is meaningful.
	big := strings.Repeat("PINNED_MEMORY_BLOCK ", 400)
	if err := store.Put("pref.big", big, []string{memory.TagPreference}); err != nil {
		t.Fatal(err)
	}
	// Large instruction layer (also optional) that should shed when unpinned.
	hugeInst := "Instructions from: /tmp/HUGE.md\n" + strings.Repeat("NOISY_INSTRUCTION ", 2000)

	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn", Usage: &provider.Usage{InputTokens: 50, OutputTokens: 5}},
		},
	})
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "s-pin-fit",
		WorkDir:         t.TempDir(),
		Memory:          enginebind.Memory(store),
		Instructions:    []string{hugeInst},
		Agents:          []engine.Agent{{Name: "build", Prompt: "PERSONA_PIN"}},
		InitialProvider: "scripted",
		InitialModel:    "m",
		// Small window forces fit pressure + optional shed.
		ContextWindow: 2_000,
		Registry:      tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.SetContextControls{
		PinKinds: []string{protocol.PromptLayerMemory},
		SetPin:   true,
	}
	waitContextControls(t, eng)

	eng.Ops() <- protocol.UserInput{Text: "go"}
	var (
		req    provider.Request
		sawFit bool
		fitEv  protocol.ContextFitWarning
	)
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case req = <-prov.requests:
			// keep draining events until turn completes
		case ev := <-eng.Events():
			switch e := ev.(type) {
			case protocol.ContextFitWarning:
				sawFit = true
				fitEv = e
			case protocol.TurnCompleted:
				break loop
			case protocol.EngineError:
				t.Fatalf("engine error: %s", e.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for turn + fit warning")
		}
	}
	if req.System == "" {
		t.Fatal("no Stream request captured")
	}
	if !sawFit {
		t.Fatal("expected ContextFitWarning under small context window")
	}
	if fitEv.ContextLimit != 2_000 {
		t.Errorf("fit limit = %d", fitEv.ContextLimit)
	}
	if fitEv.Level != protocol.ContextFitWarn && fitEv.Level != protocol.ContextFitCritical {
		t.Errorf("fit level = %q", fitEv.Level)
	}
	if fitEv.Source != protocol.UsageSourceEstimated {
		t.Errorf("fit source = %q", fitEv.Source)
	}
	if !strings.Contains(req.System, "PINNED_MEMORY_BLOCK") {
		t.Fatal("pinned memory must survive fit-pressure shed")
	}
	if strings.Contains(req.System, "NOISY_INSTRUCTION") {
		t.Fatal("unpinned optional instruction should shed under fit pressure")
	}

	eng.Ops() <- protocol.InspectEffectivePrompt{}
	ev := waitEffectivePrompt(t, eng)
	if !stringSliceContains(ev.PinnedKinds, protocol.PromptLayerMemory) {
		t.Fatalf("PinnedKinds = %v", ev.PinnedKinds)
	}
	if !stringSliceContains(ev.ShedKinds, protocol.PromptLayerInstruction) {
		t.Fatalf("ShedKinds = %v, want instruction", ev.ShedKinds)
	}
	if layerKindsContain(ev.Layers, protocol.PromptLayerInstruction) {
		t.Fatal("shed instruction still listed in layers")
	}
	if !layerKindsContain(ev.Layers, protocol.PromptLayerMemory) {
		t.Fatal("pinned memory missing from layers")
	}
	for _, layer := range ev.Layers {
		if layer.Kind == protocol.PromptLayerMemory && !layer.Pinned {
			t.Fatal("memory layer Pinned flag false")
		}
	}
}

func TestInspectAfterExcludeUsesCurrentComposition(t *testing.T) {
	store := openTestMemory(t)
	if err := store.Put("pref.one", "MEMORY_AFTER_STREAM", []string{memory.TagPreference}); err != nil {
		t.Fatal(err)
	}
	prov := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "ok"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	eng := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "s-inspect-live",
		WorkDir:         t.TempDir(),
		Memory:          enginebind.Memory(store),
		Agents:          []engine.Agent{{Name: "build", Prompt: "PERSONA"}},
		InitialProvider: "scripted",
		InitialModel:    "m",
		Registry:        tool.NewRegistry(),
		Select: func(string) (provider.Provider, string, error) {
			return prov, "m", nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go eng.Run(ctx)

	eng.Ops() <- protocol.UserInput{Text: "hi"}
	_ = waitStreamRequest(t, eng, prov)
	waitTurnCompleted(t, eng)

	eng.Ops() <- protocol.SetContextControls{
		ExcludeKinds: []string{protocol.PromptLayerMemory},
		SetExclude:   true,
	}
	waitContextControls(t, eng)

	eng.Ops() <- protocol.InspectEffectivePrompt{}
	ev := waitEffectivePrompt(t, eng)
	if ev.FromLastStream {
		t.Fatal("inspect after control change should use current composition, not stale last stream")
	}
	if layerKindsContain(ev.Layers, protocol.PromptLayerMemory) {
		t.Fatalf("memory still in layers after exclude: %+v", kindsOf(ev.Layers))
	}
	if !stringSliceContains(ev.ExcludedKinds, protocol.PromptLayerMemory) {
		t.Fatalf("ExcludedKinds = %v", ev.ExcludedKinds)
	}
}

func TestChildSessionInspectsOwnEffectiveContext(t *testing.T) {
	// Child engines compose their own layers (depth>0 adds handoff guidance).
	// Inspect on the child must reflect child composition, not the parent.
	parentProv := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "parent"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	childProv := newScriptedProvider(streamStep{
		events: []provider.StreamEvent{
			{Type: provider.EventTextDelta, Text: "child-done"},
			{Type: provider.EventDone, StopReason: "end_turn"},
		},
	})
	// Use a single Select that returns parent first, then child provider for
	// nested engines — simpler: child inherits same Select but we inspect the
	// child engine via spawn path with echo-like scripted steps.
	var streamN int
	selectFn := func(string) (provider.Provider, string, error) {
		streamN++
		if streamN == 1 {
			return parentProv, "m", nil
		}
		return childProv, "m", nil
	}
	reg := tool.NewRegistry()
	// Minimal task tool surface is registered by engine wiring in child tests;
	// reuse harness from child_test patterns via UserInput that doesn't spawn.
	// Direct child engine construction mirrors spawnChild options.
	child := engine.New(engine.Options{
		BuildDiagnostic: enginebind.Diagnostic(),
		SessionID:       "child-inspect",
		ParentSessionID: "parent-inspect",
		Depth:           1,
		WorkDir:         t.TempDir(),
		Agents:          []engine.Agent{{Name: "explore", Prompt: "CHILD_PERSONA_ONLY"}},
		InitialAgent:    "explore",
		InitialProvider: "scripted",
		InitialModel:    "m",
		Registry:        reg,
		Select:          selectFn,
		TaskOneShot:     true,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go child.Run(ctx)

	child.Ops() <- protocol.InspectEffectivePrompt{}
	ev := waitEffectivePrompt(t, child)
	if ev.FromLastStream {
		t.Fatal("expected current composition before child stream")
	}
	found := false
	for _, layer := range ev.Layers {
		if layer.Kind == protocol.PromptLayerPersona && strings.Contains(layer.Source, "explore") {
			found = true
		}
		if layer.EstTokens < 0 {
			t.Fatalf("negative EstTokens on %s", layer.Kind)
		}
	}
	if !found {
		t.Fatalf("child persona missing: %+v", kindsOf(ev.Layers))
	}
	// Depth>0 engines include completion-handoff guidance under tools kind.
	// Presence is optional if registry empty; Correlation should stamp child id.
	if ev.SessionID != "child-inspect" {
		t.Fatalf("SessionID = %q, want child-inspect", ev.SessionID)
	}
	if ev.ParentSessionID != "parent-inspect" {
		t.Fatalf("ParentSessionID = %q", ev.ParentSessionID)
	}
	if ev.Depth != 1 {
		t.Fatalf("Depth = %d, want 1", ev.Depth)
	}
	_ = parentProv
}

func waitContextControls(t *testing.T, eng *engine.Engine) protocol.ContextControlsSelected {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case ev := <-eng.Events():
			if c, ok := ev.(protocol.ContextControlsSelected); ok {
				return c
			}
			if e, ok := ev.(protocol.EngineError); ok {
				t.Fatalf("engine error: %s", e.Message)
			}
		case <-deadline:
			t.Fatal("timeout waiting for ContextControlsSelected")
		}
	}
}

func layerKindsContain(layers []protocol.PromptLayerInfo, kind string) bool {
	for _, l := range layers {
		if l.Kind == kind {
			return true
		}
	}
	return false
}

func kindsOf(layers []protocol.PromptLayerInfo) []string {
	out := make([]string, len(layers))
	for i, l := range layers {
		out[i] = l.Kind
	}
	return out
}

func stringSliceContains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}
