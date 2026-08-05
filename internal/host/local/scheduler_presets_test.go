package local

import (
	"testing"

	"github.com/jonathanung/strike-cli/internal/scheduler"
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
