package local_test

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/host/local"
	"github.com/jonathanung/strike-cli/internal/permission"
)

func TestPermissionsExplainBaseLayers(t *testing.T) {
	layers := []permission.Ruleset{
		permission.Defaults(),
		{{Permission: "bash", Pattern: "git *", Action: permission.Allow}},
	}
	p := local.NewPermissions(layers, []string{permission.LayerDefaults, permission.LayerConfig})
	ex := p.Explain("bash", "git status")
	if ex.Action != "allow" {
		t.Fatalf("action = %q, want allow", ex.Action)
	}
	if ex.Layer != permission.LayerConfig {
		t.Fatalf("layer = %q, want %s", ex.Layer, permission.LayerConfig)
	}
	if !strings.Contains(ex.Summary, "allow") {
		t.Fatalf("summary missing allow: %s", ex.Summary)
	}
	if ex.EvalPath == "" {
		t.Fatalf("expected eval path in explain, summary=%s", ex.Summary)
	}
}

func TestPermissionsExplainLive(t *testing.T) {
	p := local.NewPermissions(nil, nil)
	p.SetLive(func(perm, pat string) permission.DetailedExplanation {
		return permission.DetailedExplanation{
			Explanation: permission.Explanation{
				Permission: perm,
				Pattern:    pat,
				Action:     permission.Deny,
				Matched: &permission.Match{
					Layer:      permission.LayerAgent,
					Permission: perm,
					Pattern:    "*",
					Action:     permission.Deny,
				},
			},
			EvalPath: permission.EvalPathPattern,
		}
	})
	ex := p.Explain("bash", "rm -rf /")
	if ex.Action != "deny" || ex.Layer != permission.LayerAgent {
		t.Fatalf("got action=%q layer=%q", ex.Action, ex.Layer)
	}
}

func TestPermissionsPresets(t *testing.T) {
	p := local.NewPermissions(nil, nil)
	list := p.Presets()
	if len(list) < 2 {
		t.Fatalf("presets = %d, want >= 2", len(list))
	}
	ids := map[string]bool{}
	for _, pr := range list {
		ids[pr.ID] = true
		if pr.Name == "" || pr.Description == "" {
			t.Fatalf("preset %#v missing name/description", pr)
		}
	}
	if !ids[permission.PresetIDReadOnly] || !ids[permission.PresetIDDev] {
		t.Fatalf("missing shipped presets: %v", ids)
	}
}
