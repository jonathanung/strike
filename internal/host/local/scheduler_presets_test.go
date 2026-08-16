package local

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/jonathanung/strike-cli/harness/scheduler"
	"github.com/jonathanung/strike-cli/internal/config"
)

func TestSchedulerPresetCatalog(t *testing.T) {
	cat := schedulerPresetCatalog{}
	list := cat.List()
	want := scheduler.KnownPresetIDs()
	if len(list) != len(want) {
		t.Fatalf("list=%d want %d", len(list), len(want))
	}
	for i, id := range want {
		if list[i].ID != id {
			t.Fatalf("list[%d]=%q want %q", i, list[i].ID, id)
		}
		p, ok := cat.Get(id)
		if !ok {
			t.Fatalf("Get(%q) miss", id)
		}
		if p.Name == "" || p.Rationale == "" || p.Version < 1 {
			t.Fatalf("Get(%q) incomplete: %+v", id, p)
		}
		if p.DefaultClass == "" || len(p.Rules) == 0 {
			t.Fatalf("Get(%q) missing class/rules: %+v", id, p)
		}
		// Host view should match scheduler catalog fields.
		src, ok := scheduler.Lookup(id)
		if !ok {
			t.Fatal(id)
		}
		if p.Name != src.Name || len(p.Rules) != len(src.Rules) {
			t.Fatalf("%s host/src mismatch", id)
		}
		if p.Rules[0].Pattern != src.Rules[0].Pattern || p.Rules[0].Class != string(src.Rules[0].Class) {
			t.Fatalf("%s rule0 mismatch", id)
		}
	}
	if _, ok := cat.Get("nope"); ok {
		t.Fatal("unknown should miss")
	}
}

func TestSchedulerPresetGlobalApplyRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cat := schedulerPresetCatalog{}
	st, err := cat.Global()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Presets) != 0 || len(st.Limits) != 0 || len(st.Commands) != 0 {
		t.Fatalf("empty home should be zero: %+v", st)
	}

	// Seed custom rules via raw write, then apply presets through host.
	path := config.GlobalPath()
	seed := config.Config{
		Scheduler: config.SchedulerConfig{
			Limits: scheduler.Limits{"process": 3},
			Commands: []scheduler.CommandRule{
				{Pattern: "custom *", Class: scheduler.ClassGeneral},
			},
		},
	}
	raw, err := json.MarshalIndent(seed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.GlobalRoot(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cat.ApplyGlobalPresets([]string{"npm", "cargo"}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	st, err = cat.Global()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Presets) != 2 || st.Presets[0] != "cargo" || st.Presets[1] != "npm" {
		// catalog order: cargo before npm
		t.Fatalf("presets=%v", st.Presets)
	}
	if st.Limits["process"] != 3 {
		t.Fatalf("limits=%v", st.Limits)
	}
	if len(st.Commands) != 1 || st.Commands[0].Pattern != "custom *" {
		t.Fatalf("commands=%+v", st.Commands)
	}

	// Re-apply same set is idempotent.
	if err := cat.ApplyGlobalPresets([]string{"cargo", "npm"}); err != nil {
		t.Fatal(err)
	}
	st2, err := cat.Global()
	if err != nil {
		t.Fatal(err)
	}
	if len(st2.Presets) != 2 {
		t.Fatalf("re-apply: %v", st2.Presets)
	}
}

func TestSchedulerPresetApplyUnknown(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	cat := schedulerPresetCatalog{}
	if err := cat.ApplyGlobalPresets([]string{"nope"}); err == nil {
		t.Fatal("want error")
	}
}
