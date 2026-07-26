package memory

import (
	"strings"
	"testing"
)

func TestHasAutoLoadTag(t *testing.T) {
	cases := []struct {
		tags []string
		want bool
	}{
		{nil, false},
		{[]string{"note"}, false},
		{[]string{"preference"}, true},
		{[]string{"instruction"}, true},
		{[]string{"project-convention"}, true},
		{[]string{"note", "preference"}, true},
	}
	for _, tt := range cases {
		if got := HasAutoLoadTag(tt.tags); got != tt.want {
			t.Errorf("HasAutoLoadTag(%v) = %v, want %v", tt.tags, got, tt.want)
		}
	}
}

func TestSelectAutoLoadFiltersAndSorts(t *testing.T) {
	entries := []Entry{
		{Key: "z-pref", Value: "prefer tests", Tags: []string{"preference"}},
		{Key: "a-note", Value: "scratch", Tags: nil},
		{Key: "m-conv", Value: "use make", Tags: []string{"project-convention"}},
		{Key: "other", Value: "x", Tags: []string{"config"}},
	}
	got, omitted := SelectAutoLoad(entries)
	if omitted != 0 {
		t.Fatalf("omitted = %d, want 0", omitted)
	}
	if len(got) != 2 {
		t.Fatalf("selected = %d, want 2: %#v", len(got), got)
	}
	if got[0].Key != "m-conv" || got[1].Key != "z-pref" {
		t.Fatalf("order = %q, %q", got[0].Key, got[1].Key)
	}
}

func TestSelectAutoLoadEntryCap(t *testing.T) {
	entries := make([]Entry, MaxAutoLoadEntries+3)
	for i := range entries {
		entries[i] = Entry{
			Key:   string(rune('a'+i%26)) + "-" + strings.Repeat("k", i%5),
			Value: "v",
			Tags:  []string{"instruction"},
		}
		// unique keys
		entries[i].Key = strings.Repeat("k", i+1)
	}
	got, omitted := SelectAutoLoad(entries)
	if len(got) != MaxAutoLoadEntries {
		t.Fatalf("selected = %d, want %d", len(got), MaxAutoLoadEntries)
	}
	if omitted != 3 {
		t.Fatalf("omitted = %d, want 3", omitted)
	}
}

func TestFormatAutoLoadLayerEmpty(t *testing.T) {
	if got := FormatAutoLoadLayer(nil, 0); got != "" {
		t.Fatalf("empty = %q", got)
	}
}

func TestFormatAutoLoadLayerContent(t *testing.T) {
	text := FormatAutoLoadLayer([]Entry{
		{Key: "test.priority", Value: "Always run make test first.", Tags: []string{"preference"}},
	}, 0)
	for _, want := range []string{
		"# Project memory (untrusted)",
		"## test.priority",
		"tags: preference",
		"Always run make test first.",
		"instruction, preference, or project-convention",
		"Issues are never auto-loaded",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in:\n%s", want, text)
		}
	}
	if strings.Contains(text, "omitted") {
		t.Error("unexpected omit notice")
	}
}

func TestFormatAutoLoadLayerOmitNotice(t *testing.T) {
	text := FormatAutoLoadLayer([]Entry{
		{Key: "a", Value: "v", Tags: []string{"instruction"}},
	}, 2)
	if !strings.Contains(text, "(2 eligible entries omitted by auto-load cap)") {
		t.Fatalf("omit notice missing:\n%s", text)
	}
}

func TestAutoLoadLayerFromStore(t *testing.T) {
	s, err := Open(t.TempDir(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Put("pref", "use table tests", []string{"preference"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("note", "scratch pad", nil); err != nil {
		t.Fatal(err)
	}

	text, omitted, err := AutoLoadLayer(s)
	if err != nil {
		t.Fatal(err)
	}
	if omitted != 0 {
		t.Fatalf("omitted = %d", omitted)
	}
	if !strings.Contains(text, "use table tests") {
		t.Fatalf("tagged value missing:\n%s", text)
	}
	if strings.Contains(text, "scratch pad") {
		t.Fatal("untagged entry must not auto-load")
	}
}

func TestAutoLoadLayerNilLister(t *testing.T) {
	text, omitted, err := AutoLoadLayer(nil)
	if err != nil || text != "" || omitted != 0 {
		t.Fatalf("nil = %q %d %v", text, omitted, err)
	}
}
