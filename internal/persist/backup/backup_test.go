package backup

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

type rec struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

func sample(t *testing.T) *Archive {
	t.Helper()
	a := New(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC), "v1.2.3")
	if err := a.Add(SectionMemory, 2, []rec{{"a", "1"}, {"b", "2"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(SectionSessions, 0, []rec{}); err != nil {
		t.Fatal(err)
	}
	a.Exclude("provider credentials", ExcludeSecret, "never exported")
	return a
}

func encode(t *testing.T, a *Archive) []byte {
	t.Helper()
	data, err := a.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestRoundTripPreservesSectionsAndManifest(t *testing.T) {
	data := encode(t, sample(t))
	got, err := Decode(data)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if got.Format != Format || got.Version != Version || got.Encrypted {
		t.Fatalf("header = %q %d encrypted=%v", got.Format, got.Version, got.Encrypted)
	}
	if got.StrikeVersion != "v1.2.3" || !got.CreatedAt.Equal(time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("stamp = %q %v", got.StrikeVersion, got.CreatedAt)
	}
	var mem []rec
	ok, err := got.Section(SectionMemory, &mem)
	if err != nil || !ok {
		t.Fatalf("Section memory ok=%v err=%v", ok, err)
	}
	if len(mem) != 2 || mem[0] != (rec{"a", "1"}) || mem[1] != (rec{"b", "2"}) {
		t.Fatalf("memory = %#v", mem)
	}
	var none []rec
	if ok, err := got.Section(SectionSchedules, &none); ok || err != nil || none != nil {
		t.Fatalf("absent section ok=%v err=%v dst=%v", ok, err, none)
	}
	if len(got.Manifest.Excluded) != 1 || got.Manifest.Excluded[0].Kind != ExcludeSecret {
		t.Fatalf("excluded = %#v", got.Manifest.Excluded)
	}
	again := encode(t, got)
	if !bytes.Equal(again, data) {
		t.Fatalf("re-encode differs:\n%s\n%s", again, data)
	}
}

func TestDecodeAcceptsPrettyPrintedArchive(t *testing.T) {
	data := encode(t, sample(t))
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, data, "", "  "); err != nil {
		t.Fatal(err)
	}
	if _, err := Decode(pretty.Bytes()); err != nil {
		t.Fatalf("pretty archive rejected: %v", err)
	}
}

func TestDecodeRejectsTamperedOrMalformedArchives(t *testing.T) {
	good := string(encode(t, sample(t)))
	cases := []struct {
		name   string
		mutate func(string) string
		want   string
	}{
		{"empty", func(string) string { return "  " }, "empty file"},
		{"not json", func(string) string { return "{" }, "bad JSON"},
		{"wrong format", func(s string) string { return strings.Replace(s, Format, "strike.memory", 1) }, "format"},
		{"wrong version", func(s string) string { return strings.Replace(s, `"version":1`, `"version":2`, 1) }, "version 2"},
		{"encrypted", func(s string) string { return strings.Replace(s, `"encrypted":false`, `"encrypted":true`, 1) }, "encrypted"},
		{"tampered value", func(s string) string { return strings.Replace(s, `"value":"2"`, `"value":"evil"`, 1) }, "integrity check"},
		{"dropped item", func(s string) string { return strings.Replace(s, `,{"key":"b","value":"2"}`, ``, 1) }, "integrity check"},
		{"missing section", func(s string) string { return strings.Replace(s, `,"sessions":[]`, ``, 1) }, `"sessions" is missing`},
		{"unlisted section", func(s string) string {
			return strings.Replace(s, `"sections":{`, `"sections":{"schedules":[],`, 1)
		}, "not in the manifest"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bad := tc.mutate(good)
			if bad == good {
				t.Fatalf("mutation did not apply to %s", good)
			}
			_, err := Decode([]byte(bad))
			if err == nil {
				t.Fatal("Decode accepted a bad archive")
			}
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err %v is not ErrInvalid", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDecodeRejectsManifestInconsistencies(t *testing.T) {
	raw := json.RawMessage(`[{"key":"a","value":"1"}]`)
	cases := []struct {
		name string
		a    Archive
		want string
	}{
		{"unknown section", Archive{
			Manifest: Manifest{Sections: []SectionEntry{{Name: "secrets", Items: 1, SHA256: Hash(raw)}}},
			Sections: map[string]json.RawMessage{"secrets": raw},
		}, "unknown section"},
		{"listed twice", Archive{
			Manifest: Manifest{Sections: []SectionEntry{{Name: SectionMemory, Items: 1, SHA256: Hash(raw)}, {Name: SectionMemory, Items: 1, SHA256: Hash(raw)}}},
			Sections: map[string]json.RawMessage{SectionMemory: raw},
		}, "listed twice"},
		{"item count", Archive{
			Manifest: Manifest{Sections: []SectionEntry{{Name: SectionMemory, Items: 5, SHA256: Hash(raw)}}},
			Sections: map[string]json.RawMessage{SectionMemory: raw},
		}, "has 1 items, manifest says 5"},
		{"not an array", Archive{
			Manifest: Manifest{Sections: []SectionEntry{{Name: SectionMemory, Items: 1, SHA256: Hash([]byte(`{"a":1}`))}}},
			Sections: map[string]json.RawMessage{SectionMemory: json.RawMessage(`{"a":1}`)},
		}, "not a JSON array"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.a.Format, tc.a.Version = Format, Version
			data, err := json.Marshal(tc.a)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Decode(data)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestAddRejectsUnknownAndDuplicateSections(t *testing.T) {
	a := New(time.Now(), "")
	if err := a.Add("auth", 0, []rec{}); err == nil {
		t.Fatal("unknown section accepted")
	}
	if err := a.Add(SectionMemory, 0, []rec{}); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(SectionMemory, 0, []rec{}); err == nil {
		t.Fatal("duplicate section accepted")
	}
	if len(a.Manifest.Sections) != 1 {
		t.Fatalf("manifest = %#v", a.Manifest.Sections)
	}
}

func TestReportCountsByAction(t *testing.T) {
	a := sample(t)
	r := NewReport(a, true)
	r.Add(Item{Section: SectionMemory, ID: "a", Action: ActionCreate})
	r.Add(Item{Section: SectionMemory, ID: "b", Action: ActionSkip})
	r.Add(Item{Section: SectionMemory, ID: "c", Action: ActionSkip})
	if !r.DryRun || r.Counts[SectionMemory][ActionCreate] != 1 || r.Counts[SectionMemory][ActionSkip] != 2 {
		t.Fatalf("report = %#v", r)
	}
	if len(r.Excluded) != 1 {
		t.Fatalf("report excluded = %#v", r.Excluded)
	}
}

func TestCopyName(t *testing.T) {
	cases := []struct {
		name  string
		taken map[string]bool
		want  string
	}{
		{"free", nil, "notes (restored)"},
		{"first taken", map[string]bool{"notes (restored)": true}, "notes (restored 2)"},
		{"two taken", map[string]bool{"notes (restored)": true, "notes (restored 2)": true}, "notes (restored 3)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := CopyName("notes", func(s string) bool { return tc.taken[s] })
			if got != tc.want {
				t.Fatalf("CopyName = %q, want %q", got, tc.want)
			}
		})
	}
}
