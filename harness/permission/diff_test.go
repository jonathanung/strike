package permission

import (
	"strings"
	"testing"
)

func TestDiffPresetsReadOnlyVsDev(t *testing.T) {
	d, err := DiffPresets(PresetIDReadOnly, PresetIDDev)
	if err != nil {
		t.Fatal(err)
	}
	if d.LeftLabel != "preset:read-only" || d.RightLabel != "preset:dev" {
		t.Fatalf("labels = %q → %q", d.LeftLabel, d.RightLabel)
	}
	if len(d.Changes) == 0 {
		t.Fatal("expected rule changes between read-only and dev")
	}
	text := FormatDiff(d)
	if !strings.Contains(text, "permission diff") {
		t.Fatalf("format = %q", text)
	}
	// read-only denies bash *; dev allows go * — should see deltas.
	var kinds []string
	for _, c := range d.Changes {
		kinds = append(kinds, string(c.Kind))
	}
	joined := strings.Join(kinds, ",")
	if !strings.Contains(joined, "added") && !strings.Contains(joined, "removed") && !strings.Contains(joined, "changed") {
		t.Fatalf("unexpected kinds: %v", kinds)
	}
}

func TestDiffPresetsUnknown(t *testing.T) {
	_, err := DiffPresets("nope", PresetIDDev)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestDiffLabeledChangedAction(t *testing.T) {
	left := []LabeledLayer{{Name: LayerConfig, Rules: Ruleset{
		{Permission: "bash", Pattern: "rm *", Action: Ask},
	}}}
	right := []LabeledLayer{{Name: LayerConfig, Rules: Ruleset{
		{Permission: "bash", Pattern: "rm *", Action: Deny},
	}}}
	d := DiffLabeled(left, right, "a", "b")
	if len(d.Changes) != 1 || d.Changes[0].Kind != DiffChanged {
		t.Fatalf("changes = %+v", d.Changes)
	}
	if d.Changes[0].Before.Action != Ask || d.Changes[0].After.Action != Deny {
		t.Fatalf("delta = %+v", d.Changes[0])
	}
}

func TestExplainWithPresetDryRun(t *testing.T) {
	layers := []LabeledLayer{
		{Name: LayerDefaults, Rules: Defaults()},
		{Name: LayerConfig, Rules: nil},
	}
	// Without preset, bash is typically ask from defaults.
	base := ExplainLabeled("bash", "go test", layers)
	ex, err := ExplainWithPreset(layers, PresetIDDev, "bash", "go test ./...")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Action != Allow {
		t.Fatalf("dev preset dry-run action = %s, want allow (base was %s)", ex.Action, base.Action)
	}
	// Original layers unchanged — re-explain without preset.
	again := ExplainLabeled("bash", "go test ./...", layers)
	if again.Action != base.Action {
		t.Fatalf("layers mutated: %s vs %s", again.Action, base.Action)
	}
}

func TestExplainWithPresetReadOnlyDeniesWrite(t *testing.T) {
	layers := []LabeledLayer{
		{Name: LayerDefaults, Rules: Defaults()},
	}
	ex, err := ExplainWithPreset(layers, PresetIDReadOnly, "write", "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if ex.Action != Deny {
		t.Fatalf("action = %s, want deny", ex.Action)
	}
	if ex.Matched == nil || ex.Matched.Layer != LayerPreset {
		t.Fatalf("matched = %+v", ex.Matched)
	}
}

func TestInspectCeilingManagedBlocks(t *testing.T) {
	layers := []LabeledLayer{
		{Name: LayerDefaults, Rules: Defaults()},
		{Name: LayerConfig, Rules: Ruleset{
			{Permission: "bash", Pattern: "rm*", Action: Allow},
		}},
		{Name: LayerManaged, Rules: Ruleset{
			{Permission: "bash", Pattern: "rm*", Action: Deny},
		}},
	}
	info := InspectCeiling(layers, "bash", "rm -rf tmp")
	if !info.ManagedBlocks {
		t.Fatalf("expected managed block: %+v", info)
	}
	if info.WithoutManaged != Allow || info.WithManaged != Deny {
		t.Fatalf("without=%s with=%s", info.WithoutManaged, info.WithManaged)
	}
	if info.ManagedRule == nil || info.ManagedRule.Layer != LayerManaged {
		t.Fatalf("managed rule = %+v", info.ManagedRule)
	}
	if !strings.Contains(info.Summary, "managed ceiling blocks") {
		t.Fatalf("summary = %q", info.Summary)
	}
}

func TestInspectCeilingInactive(t *testing.T) {
	layers := []LabeledLayer{
		{Name: LayerDefaults, Rules: Defaults()},
		{Name: LayerManaged, Rules: Ruleset{
			{Permission: "write", Pattern: "**/.env", Action: Deny},
		}},
	}
	info := InspectCeiling(layers, "read", "main.go")
	if info.ManagedBlocks {
		t.Fatalf("read should not be blocked: %+v", info)
	}
}

func TestProvenanceOrderingLastMatchAndManaged(t *testing.T) {
	// defaults allow-ish ask, config allow, managed deny — last managed wins.
	layers := []LabeledLayer{
		{Name: LayerDefaults, Rules: Defaults()},
		{Name: LayerPreset, Rules: func() Ruleset {
			p, _ := PresetByID(PresetIDYoloSandbox)
			return p.Rules
		}()},
		{Name: LayerConfig, Rules: Ruleset{
			{Permission: "bash", Pattern: "*", Action: Allow},
		}},
		{Name: LayerManaged, Rules: Ruleset{
			{Permission: "bash", Pattern: "*", Action: Deny},
		}},
	}
	ex := ExplainLabeled("bash", "anything", layers)
	if ex.Action != Deny {
		t.Fatalf("action = %s, want deny", ex.Action)
	}
	if ex.Matched == nil || ex.Matched.Layer != LayerManaged {
		t.Fatalf("matched = %+v, want managed", ex.Matched)
	}
	// Trail must include earlier allow then managed deny.
	var sawAllow, sawManaged bool
	for _, m := range ex.Trail {
		if m.Action == Allow {
			sawAllow = true
		}
		if m.Layer == LayerManaged && m.Action == Deny {
			sawManaged = true
		}
	}
	if !sawAllow || !sawManaged {
		t.Fatalf("trail = %+v", ex.Trail)
	}
}

func TestFormatExplanationFull(t *testing.T) {
	ex := ExplainLabeled("bash", "ls", []LabeledLayer{
		{Name: LayerConfig, Rules: Ruleset{{Permission: "bash", Pattern: "*", Action: Allow}}},
	})
	ceil := InspectCeiling([]LabeledLayer{
		{Name: LayerConfig, Rules: Ruleset{{Permission: "bash", Pattern: "*", Action: Allow}}},
		{Name: LayerManaged, Rules: Ruleset{{Permission: "bash", Pattern: "*", Action: Deny}}},
	}, "bash", "ls")
	sb := &SandboxExplainBits{Mode: "workspace-write", NetworkAllow: []string{"github.com"}}
	s := FormatExplanationFull(ex, &ceil, sb)
	if !strings.Contains(s, "managed ceiling") || !strings.Contains(s, "sandbox=workspace-write") {
		t.Fatalf("full = %q", s)
	}
	if !strings.Contains(s, "network.allow") {
		t.Fatalf("missing network.allow: %q", s)
	}
}
