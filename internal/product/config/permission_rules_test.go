package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike/harness/permission"
)

func TestPermissionRulesConfirmAndScopes(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	view, err := ReadPermissionRules(work)
	if err != nil {
		t.Fatal(err)
	}
	if view.GlobalMode != "default" || view.EffectiveMode != "default" || len(view.Rules) != 0 {
		t.Fatalf("empty view = %+v", view)
	}
	if !strings.Contains(view.Note, "Deny rules still apply") || !strings.Contains(view.Note, "last matching rule") {
		t.Fatalf("note = %q", view.Note)
	}

	deny := permission.Rule{Permission: "bash", Pattern: "rm *", Action: permission.Deny}
	if err := AppendPermissionRule(work, "global", deny, false); err != nil {
		t.Fatal(err)
	}
	allow := permission.Rule{Permission: "bash", Pattern: "git *", Action: permission.Allow}
	if err := AppendPermissionRule(work, "global", allow, false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("allow without confirm = %v", err)
	}
	if err := AppendPermissionRule(work, "project", allow, true); err != nil {
		t.Fatal(err)
	}
	ask := permission.Rule{Permission: "edit", Pattern: "*.go", Action: permission.Ask}
	if err := AppendPermissionRule(work, "project", ask, false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("ask without confirm = %v", err)
	}

	view, err = ReadPermissionRules(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rules) != 2 {
		t.Fatalf("rules = %+v", view.Rules)
	}
	if view.Rules[0].Scope != "global" || view.Rules[0].Action != "deny" || view.Rules[0].Index != 0 {
		t.Fatalf("global rule = %+v", view.Rules[0])
	}
	if view.Rules[1].Scope != "project" || view.Rules[1].Pattern != "git *" {
		t.Fatalf("project rule = %+v", view.Rules[1])
	}

	if err := DeletePermissionRule(work, "global", 0, false); !errors.Is(err, ErrConfirmRequired) {
		t.Fatalf("delete deny without confirm = %v", err)
	}
	if err := DeletePermissionRule(work, "global", 0, true); err != nil {
		t.Fatal(err)
	}
	if err := DeletePermissionRule(work, "project", 0, false); err != nil {
		t.Fatalf("delete allow = %v", err)
	}
	view, err = ReadPermissionRules(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rules) != 0 {
		t.Fatalf("rules after delete = %+v", view.Rules)
	}

	bad := []permission.Rule{
		{Permission: "trust", Pattern: "*", Action: permission.Allow},
		{Permission: "bash", Pattern: "", Action: permission.Deny},
		{Permission: "bash", Pattern: "rm\n-rf", Action: permission.Deny},
		{Permission: "bash", Pattern: "*", Action: "yolo"},
	}
	for _, rule := range bad {
		if err := AppendPermissionRule(work, "global", rule, true); err == nil {
			t.Fatalf("accepted invalid rule %+v", rule)
		}
	}
	if err := AppendPermissionRule(work, "session", deny, true); err == nil {
		t.Fatal("accepted unknown scope")
	}

	global := filepath.Join(home, ".strike", "config")
	data, err := os.ReadFile(global)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("global config missing")
	}
}

func TestPermissionRulesRejectInvalidGlobPatterns(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	for _, pattern := range []string{"[a-", "{unclosed", "[r*"} {
		for _, action := range []permission.Action{permission.Deny, permission.Allow} {
			rule := permission.Rule{Permission: "bash", Pattern: pattern, Action: action}
			err := AppendPermissionRule(work, "global", rule, true)
			if err == nil {
				t.Fatalf("AppendPermissionRule(%+v) = nil, want invalid glob rejection", rule)
			}
			if !strings.Contains(err.Error(), pattern) || !strings.Contains(err.Error(), "invalid glob syntax") {
				t.Fatalf("AppendPermissionRule(%+v) error = %q, want it to name the pattern and the invalid glob syntax", rule, err)
			}
		}
		if err := AppendProjectPermission(work, permission.Rule{Permission: "bash", Pattern: pattern, Action: permission.Deny}); err == nil {
			t.Fatalf("AppendProjectPermission(pattern %q) = nil, want invalid glob rejection", pattern)
		}
	}
	view, err := ReadPermissionRules(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rules) != 0 {
		t.Fatalf("invalid rules were persisted: %+v", view.Rules)
	}

	valid := []string{"**", "[r]*", "[a-z]*", "{rm,git} *", "**/*.go", "rm *"}
	for _, pattern := range valid {
		rule := permission.Rule{Permission: "bash", Pattern: pattern, Action: permission.Deny}
		if err := AppendPermissionRule(work, "global", rule, false); err != nil {
			t.Fatalf("AppendPermissionRule(valid %q) = %v", pattern, err)
		}
	}
	view, err = ReadPermissionRules(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rules) != len(valid) {
		t.Fatalf("valid rules = %+v, want %d", view.Rules, len(valid))
	}
}

// TestStoredInvalidGlobSurfacesInRulesReadPath covers the load path for a
// hand-edited config file: Load drops the inert rule (daemon startup is not
// bricked and the effective ruleset validates), while ReadPermissionRules
// returns the validation error so the editor surfaces it.
func TestStoredInvalidGlobSurfacesInRulesReadPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := `{"permissions":[{"permission":"bash","pattern":"[r*","action":"deny"},{"permission":"bash","pattern":"ls *","action":"deny"}]}`
	if err := os.WriteFile(global, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := ReadPermissionRules(work); err == nil || !strings.Contains(err.Error(), "[r*") || !strings.Contains(err.Error(), "invalid glob syntax") {
		t.Fatalf("ReadPermissionRules = %v, want error naming the invalid pattern", err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatalf("Load with a hand-edited invalid glob = %v, want startup not bricked", err)
	}
	if len(cfg.Permissions) != 1 || cfg.Permissions[0].Pattern != "ls *" {
		t.Fatalf("Load permissions = %+v, want only the valid rule kept", cfg.Permissions)
	}
	if err := permission.ValidateRuleset(cfg.Permissions); err != nil {
		t.Fatalf("loaded ruleset does not validate: %v", err)
	}
}
