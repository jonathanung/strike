package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/trust/admission"
)

func TestLoadAdmissionPresetAndAllowPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	proj := filepath.Join(work, ".strike")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
		"admission": {
			"preset": "strict",
			"allowPaths": ["~/trusted"]
		}
	}`
	if err := os.WriteFile(filepath.Join(proj, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(work)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Admission.Preset != "strict" {
		t.Fatalf("preset = %q", cfg.Admission.Preset)
	}
	if len(cfg.Admission.AllowPaths) != 1 {
		t.Fatalf("allowPaths = %#v", cfg.Admission.AllowPaths)
	}
	if !strings.HasPrefix(cfg.Admission.AllowPaths[0], home) {
		t.Fatalf("allow path not under home: %q", cfg.Admission.AllowPaths[0])
	}

	pol, err := config.ResolveAdmission(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if pol.Preset != admission.PresetStrict || !pol.FailClosed {
		t.Fatalf("policy = %+v", pol)
	}
}

func TestLoadRejectsBareRelativeAllowPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	proj := filepath.Join(work, ".strike")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"admission":{"allowPaths":[".strike/skills"]}}`
	if err := os.WriteFile(filepath.Join(proj, "config"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(work)
	if err == nil || !strings.Contains(err.Error(), "home-anchored") {
		t.Fatalf("err = %v", err)
	}
}

func TestFilterSkillsQuarantinesSpoof(t *testing.T) {
	home := t.TempDir()
	pol, err := admission.Resolve(admission.Config{Preset: admission.PresetDefault}, home)
	if err != nil {
		t.Fatal(err)
	}
	pol.Home = home
	evilPath := filepath.Join(home, "repo", "evil", ".strike", "skills", "pwn.md")
	skills := []config.Skill{
		{Name: "ok", Template: "hello", Path: filepath.Join(home, "skills", "ok.md")},
		{Name: "pwn", Template: "pwn", Path: evilPath},
		{Name: "ship", Template: "builtin", Builtin: true},
	}
	out, verdicts := config.FilterSkills(pol, skills, "")
	names := map[string]bool{}
	for _, s := range out {
		names[s.Name] = true
	}
	if !names["ok"] || !names["ship"] {
		t.Fatalf("admitted = %v", out)
	}
	if names["pwn"] {
		t.Fatal("spoof skill must not admit")
	}
	found := false
	for _, v := range verdicts {
		if v.Target == "pwn" && !v.BindsTools() {
			found = true
		}
	}
	if !found {
		t.Fatalf("verdicts = %+v", verdicts)
	}
}

func TestFilterSkillsAllowsProjectFirstPartyRoot(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()
	pol, err := admission.Resolve(admission.Config{Preset: admission.PresetDefault}, home)
	if err != nil {
		t.Fatal(err)
	}
	pol.Home = home
	legit := filepath.Join(work, ".strike", "skills", "local.md")
	skills := []config.Skill{
		{Name: "local", Template: "hello from project", Path: legit},
	}
	out, verdicts := config.FilterSkills(pol, skills, work)
	if len(out) != 1 || out[0].Name != "local" {
		t.Fatalf("admitted = %v verdicts=%v", out, verdicts)
	}
}
