package local_test

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/permission"
	"github.com/jonathanung/strike/internal/frontend/host/local"
)

func TestPermissionsFolderGrants(t *testing.T) {
	p := local.NewPermissions(nil, nil)
	// No hooks bound: empty list, revoke reports false.
	if got := p.SessionFolderGrants(); len(got) != 0 {
		t.Fatalf("unbound grants = %+v, want empty", got)
	}
	if p.RevokeSessionFolderGrant(0) {
		t.Fatal("unbound revoke returned true")
	}

	grants := []permission.FolderGrant{
		{Permission: "write", Folder: "docs", Patterns: []string{"docs", "docs/*", "docs/**"}},
		{Permission: "edit", Folder: "a*b", Patterns: []string{`a\*b`, `a\*b/*`, `a\*b/**`}},
	}
	p.SetFolderGrants(func() []permission.FolderGrant { return grants }, func(index int) bool {
		if index < 0 || index >= len(grants) {
			return false
		}
		grants = append(grants[:index], grants[index+1:]...)
		return true
	})
	got := p.SessionFolderGrants()
	if len(got) != 2 {
		t.Fatalf("grants = %+v, want two", got)
	}
	if got[0].Index != 0 || got[0].Permission != "write" || got[0].Folder != "docs" || got[0].Pattern != "docs/**" {
		t.Fatalf("grant[0] = %+v", got[0])
	}
	// The display pattern uses the folder name verbatim, not the escaped glob.
	if got[1].Pattern != "a*b/**" {
		t.Fatalf("grant[1] pattern = %q, want a*b/**", got[1].Pattern)
	}
	if !p.RevokeSessionFolderGrant(0) {
		t.Fatal("revoke returned false")
	}
	if got := p.SessionFolderGrants(); len(got) != 1 || got[0].Folder != "a*b" || got[0].Index != 0 {
		t.Fatalf("after revoke grants = %+v", got)
	}
}

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

func TestPermissionsExplainPresetDryRun(t *testing.T) {
	layers := []permission.Ruleset{
		permission.Defaults(),
	}
	p := local.NewPermissions(layers, []string{permission.LayerDefaults})
	ex := p.ExplainPreset("write", "main.go", permission.PresetIDReadOnly)
	if ex.Action != "deny" {
		t.Fatalf("action = %q, want deny", ex.Action)
	}
	if ex.DryRunPreset != permission.PresetIDReadOnly {
		t.Fatalf("DryRunPreset = %q", ex.DryRunPreset)
	}
	if !strings.Contains(ex.Summary, "dry-run preset=") {
		t.Fatalf("summary = %s", ex.Summary)
	}
	// Live explain without preset should not be deny from read-only.
	base := p.Explain("write", "main.go")
	if base.Action == "deny" && base.Layer == permission.LayerPreset {
		t.Fatal("base layers mutated by dry-run")
	}
}

func TestPermissionsDiffPresets(t *testing.T) {
	p := local.NewPermissions(nil, nil)
	d, err := p.DiffPresets(permission.PresetIDReadOnly, permission.PresetIDDev)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Changes) == 0 {
		t.Fatal("expected changes")
	}
	if !strings.Contains(d.Summary, "permission diff") {
		t.Fatalf("summary = %s", d.Summary)
	}
}
