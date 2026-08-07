package admission_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/admission"
	"github.com/jonathanung/strike-cli/internal/security"
)

func TestResolvePresets(t *testing.T) {
	home := t.TempDir()
	for _, id := range []string{"permissive", "default", "strict", ""} {
		pol, err := admission.Resolve(admission.Config{Preset: id}, home)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", id, err)
		}
		if pol.Matrix == nil {
			t.Fatalf("nil matrix for %q", id)
		}
	}
	if _, err := admission.Resolve(admission.Config{Preset: "nope"}, home); err == nil {
		t.Fatal("expected unknown preset error")
	}
}

func TestStrictFailClosedDefault(t *testing.T) {
	home := t.TempDir()
	pol, err := admission.Resolve(admission.Config{Preset: admission.PresetStrict}, home)
	if err != nil {
		t.Fatal(err)
	}
	if !pol.FailClosed {
		t.Fatal("strict should default fail-closed")
	}
	v := pol.OnScanError("mcp", "x", errString("boom"))
	if v.Action != admission.ActionBlock {
		t.Fatalf("fail-closed action = %s", v.Action)
	}

	pol2, err := admission.Resolve(admission.Config{Preset: admission.PresetPermissive}, home)
	if err != nil {
		t.Fatal(err)
	}
	if pol2.FailClosed {
		t.Fatal("permissive should default fail-open")
	}
	v2 := pol2.OnScanError("mcp", "x", errString("boom"))
	if v2.Action != admission.ActionWarn {
		t.Fatalf("fail-open action = %s", v2.Action)
	}
}

func TestFailClosedOverride(t *testing.T) {
	home := t.TempDir()
	f := false
	pol, err := admission.Resolve(admission.Config{Preset: admission.PresetStrict, FailClosed: &f}, home)
	if err != nil {
		t.Fatal(err)
	}
	if pol.FailClosed {
		t.Fatal("override should clear fail-closed")
	}
}

func TestDecideMatrixDefault(t *testing.T) {
	home := t.TempDir()
	pol, err := admission.Resolve(admission.Config{Preset: admission.PresetDefault}, home)
	if err != nil {
		t.Fatal(err)
	}
	// high → quarantine under default
	v := pol.Decide("mcp", "s", []security.Finding{{
		Rule: "mcp.network_tool", Severity: security.SeverityHigh, Message: "net",
	}})
	if v.Action != admission.ActionQuarantine {
		t.Fatalf("high → %s, want quarantine", v.Action)
	}
	// critical → block
	v = pol.Decide("mcp", "s", []security.Finding{{
		Rule: "mcp.shell_tool", Severity: security.SeverityCritical, Message: "shell",
	}})
	if v.Action != admission.ActionBlock {
		t.Fatalf("critical → %s, want block", v.Action)
	}
	if v.BindsTools() {
		t.Fatal("block should not bind tools")
	}
}

func TestDecideMatrixStrict(t *testing.T) {
	home := t.TempDir()
	pol, err := admission.Resolve(admission.Config{Preset: admission.PresetStrict}, home)
	if err != nil {
		t.Fatal(err)
	}
	v := pol.Decide("mcp", "s", []security.Finding{{
		Rule: "mcp.network_tool", Severity: security.SeverityHigh, Message: "net",
	}})
	if v.Action != admission.ActionBlock {
		t.Fatalf("strict high → %s, want block", v.Action)
	}
}

func TestAllowPathsHomeAnchoredOnly(t *testing.T) {
	home := t.TempDir()
	// Bare relative rejected (spoofable).
	_, err := admission.NormalizeAllowPaths([]string{".strike/skills"}, home)
	if err == nil || !strings.Contains(err.Error(), "home-anchored") {
		t.Fatalf("bare relative: err=%v", err)
	}
	// Outside home rejected.
	_, err = admission.NormalizeAllowPaths([]string{"/etc/strike"}, home)
	if err == nil {
		t.Fatal("expected outside-home error")
	}
	// ~/ and absolute under home OK.
	paths, err := admission.NormalizeAllowPaths([]string{
		"~/trusted",
		filepath.Join(home, "other"),
	}, home)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths=%v", paths)
	}
	if !admission.PathAllowed(paths, filepath.Join(home, "trusted", "a")) {
		t.Fatal("expected allow under ~/trusted")
	}
	// Spoof subdirectory must NOT match bare marker — we never accepted bare markers.
	evil := filepath.Join(home, "evil", ".strike", "skills", "x")
	if admission.PathAllowed(paths, evil) {
		t.Fatal("evil nested path must not be allowed via unrelated prefixes")
	}
}

func TestPathSpoofViaSubdirectory(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, ".strike", "skills")
	evil := filepath.Join(home, "project", "evil", ".strike", "skills", "pwn.md")
	if !admission.PathSpoofsFirstParty(evil, []string{real}, nil) {
		t.Fatal("expected spoof detection for nested .strike/skills")
	}
	legit := filepath.Join(real, "ok.md")
	if admission.PathSpoofsFirstParty(legit, []string{real}, nil) {
		t.Fatal("real first-party path must not spoof")
	}
	// Allow-list can exempt a nested tree when explicitly home-anchored.
	allow, err := admission.NormalizeAllowPaths([]string{
		filepath.Join(home, "project", "evil", ".strike", "skills"),
	}, home)
	if err != nil {
		t.Fatal(err)
	}
	if admission.PathSpoofsFirstParty(evil, []string{real}, allow) {
		t.Fatal("explicit allow-list should suppress spoof")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
