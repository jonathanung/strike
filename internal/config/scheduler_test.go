package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/scheduler"
)

func TestLoadSchedulerLayering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"scheduler": {
			"limits": { "process": 8, "build": 2, "model": 3 },
			"commands": [
				{ "pattern": "go *", "class": "build" },
				{ "pattern": "make *", "class": "build" }
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"scheduler": {
			"limits": { "build": 4, "test": 2 },
			"commands": [
				{ "pattern": "go test *", "class": "test" }
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}

	// Project overrides build; process/model preserved; test added.
	if cfg.Scheduler.Limits[scheduler.PoolProcess] != 8 {
		t.Fatalf("process=%d", cfg.Scheduler.Limits[scheduler.PoolProcess])
	}
	if cfg.Scheduler.Limits[scheduler.PoolBuild] != 4 {
		t.Fatalf("build=%d", cfg.Scheduler.Limits[scheduler.PoolBuild])
	}
	if cfg.Scheduler.Limits[scheduler.PoolModel] != 3 {
		t.Fatalf("model=%d", cfg.Scheduler.Limits[scheduler.PoolModel])
	}
	if cfg.Scheduler.Limits[scheduler.PoolTest] != 2 {
		t.Fatalf("test=%d", cfg.Scheduler.Limits[scheduler.PoolTest])
	}
	if _, ok := cfg.Scheduler.Limits[scheduler.PoolContainer]; ok {
		t.Fatal("container should remain omitted (unlimited)")
	}

	if len(cfg.Scheduler.Commands) != 3 {
		t.Fatalf("commands=%d want 3: %+v", len(cfg.Scheduler.Commands), cfg.Scheduler.Commands)
	}
	// Provenance stamped with layer paths.
	if cfg.Scheduler.Commands[0].Source != global {
		t.Fatalf("cmd0 source=%q want %q", cfg.Scheduler.Commands[0].Source, global)
	}
	if cfg.Scheduler.Commands[2].Source != project {
		t.Fatalf("cmd2 source=%q want %q", cfg.Scheduler.Commands[2].Source, project)
	}

	eff, err := cfg.SchedulerEffective()
	if err != nil {
		t.Fatal(err)
	}
	if eff.Classify("go build .") != scheduler.ClassBuild {
		t.Fatal("go build")
	}
	if eff.Classify("go test ./...") != scheduler.ClassTest {
		t.Fatal("go test last-match from project")
	}
	if eff.Classify("make all") != scheduler.ClassBuild {
		t.Fatal("make")
	}
	if eff.Classify("ls") != scheduler.ClassGeneral {
		t.Fatal("default general")
	}

	rep := eff.Report()
	if !strings.Contains(rep, "process: 8") || !strings.Contains(rep, "container: unlimited") {
		t.Fatalf("report:\n%s", rep)
	}
	if !strings.Contains(rep, project) {
		t.Fatalf("report missing project source:\n%s", rep)
	}
}

func TestLoadSchedulerOmittedUnlimited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scheduler.Limits) != 0 || len(cfg.Scheduler.Commands) != 0 {
		t.Fatalf("want empty scheduler, got %+v", cfg.Scheduler)
	}
	eff, err := cfg.SchedulerEffective()
	if err != nil {
		t.Fatal(err)
	}
	s, err := scheduler.New(eff.SchedulerConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, p := range s.Snapshot().Pools {
		if !p.Unlimited {
			t.Fatalf("pool %s should be unlimited", p.Name)
		}
	}
}

func TestLoadSchedulerInvalidLimitFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"scheduler": { "limits": { "process": 0 } }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(work)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), project) {
		t.Fatalf("err should name file: %v", err)
	}
	if !strings.Contains(err.Error(), "positive") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadSchedulerInvalidRuleFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"scheduler": {
			"commands": [
				{ "pattern": "go *", "class": "build" },
				{ "pattern": "bad", "class": "deploy" }
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(work)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), global) {
		t.Fatalf("err should name source: %v", err)
	}
	if !strings.Contains(err.Error(), "commands[1]") {
		t.Fatalf("err should name index: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid class") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadSchedulerUnknownPoolFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"scheduler": { "limits": { "gpu": 1 } }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(work)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("err=%v", err)
	}
}

func TestMergeSchedulerUnit(t *testing.T) {
	base := SchedulerConfig{
		Presets:  []string{"cargo"},
		Limits:   scheduler.Limits{scheduler.PoolProcess: 4},
		Commands: []scheduler.CommandRule{{Pattern: "a *", Class: scheduler.ClassBuild, Source: "g"}},
	}
	layer := SchedulerConfig{
		Presets:  []string{"cargo", "npm"},
		Limits:   scheduler.Limits{scheduler.PoolProcess: 8, scheduler.PoolTest: 1},
		Commands: []scheduler.CommandRule{{Pattern: "b *", Class: scheduler.ClassTest, Source: "p"}},
	}
	got := mergeScheduler(base, layer)
	if got.Limits[scheduler.PoolProcess] != 8 || got.Limits[scheduler.PoolTest] != 1 {
		t.Fatalf("limits=%v", got.Limits)
	}
	if len(got.Commands) != 2 {
		t.Fatalf("commands=%v", got.Commands)
	}
	if len(got.Presets) != 2 || got.Presets[0] != "cargo" || got.Presets[1] != "npm" {
		t.Fatalf("presets=%v", got.Presets)
	}
	// base not mutated via shared slice header growth issues
	if len(base.Commands) != 1 {
		t.Fatalf("base commands mutated: %v", base.Commands)
	}
	if len(base.Presets) != 1 {
		t.Fatalf("base presets mutated: %v", base.Presets)
	}
}

func TestLoadSchedulerPresetsExpand(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"scheduler": {
			"presets": ["cargo"],
			"limits": { "process": 6 },
			"commands": [
				{ "pattern": "cargo bench *", "class": "build" }
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"scheduler": {
			"presets": ["npm"],
			"limits": { "build": 1 },
			"commands": [
				{ "pattern": "cargo test *", "class": "general" }
			]
		}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Scheduler.Presets) != 2 || cfg.Scheduler.Presets[0] != "cargo" || cfg.Scheduler.Presets[1] != "npm" {
		t.Fatalf("presets=%v", cfg.Scheduler.Presets)
	}

	eff, err := cfg.SchedulerEffective()
	if err != nil {
		t.Fatal(err)
	}
	// User process=6; cargo/npm suggest build=2 then project build=1 overlays.
	if eff.Limits[scheduler.PoolProcess] != 6 {
		t.Fatalf("process=%d", eff.Limits[scheduler.PoolProcess])
	}
	if eff.Limits[scheduler.PoolBuild] != 1 {
		t.Fatalf("build=%d want project overlay 1", eff.Limits[scheduler.PoolBuild])
	}
	if eff.Classify("cargo build") != scheduler.ClassBuild {
		t.Fatal("cargo build from preset")
	}
	if eff.Classify("cargo test --all") != scheduler.ClassGeneral {
		t.Fatal("project rule should override preset cargo test")
	}
	if eff.Classify("npm test") != scheduler.ClassTest {
		t.Fatal("npm preset test")
	}
	if eff.Classify("cargo bench x") != scheduler.ClassBuild {
		t.Fatal("global user command")
	}
	rep := eff.Report()
	if !strings.Contains(rep, "preset:cargo@v") || !strings.Contains(rep, "preset:npm@v") {
		t.Fatalf("report missing preset sources:\n%s", rep)
	}
	if !strings.Contains(rep, project) {
		t.Fatalf("report missing project source:\n%s", rep)
	}
}

func TestLoadSchedulerUnknownPresetFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	project := filepath.Join(work, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(project), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(project, []byte(`{
		"scheduler": { "presets": ["msbuild"] }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(work)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), project) {
		t.Fatalf("err should name file: %v", err)
	}
	if !strings.Contains(err.Error(), "unknown preset") {
		t.Fatalf("err=%v", err)
	}
}

func TestLoadSchedulerDuplicatePresetFails(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()

	global := filepath.Join(home, ".strike", "config")
	if err := os.MkdirAll(filepath.Dir(global), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(global, []byte(`{
		"scheduler": { "presets": ["cargo", "cargo"] }
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(work)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("err=%v", err)
	}
}
