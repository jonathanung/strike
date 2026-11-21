package session

import (
	"strings"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

func writeSession(t *testing.T, m *Manager, opts CreateOptions, events ...protocol.Event) Info {
	t.Helper()
	info, err := m.Create(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range events {
		if err := m.Append(info.ID, ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Close(info.ID); err != nil {
		t.Fatal(err)
	}
	return info
}

func TestRestorePackageKeepsIDAndTeamLinks(t *testing.T) {
	src := NewManager(t.TempDir())
	writeSession(t, src, CreateOptions{ID: "root-1", Title: "lead"}, protocol.UserMessage{Text: "plan"})
	child := writeSession(t, src, CreateOptions{ID: "child-1", ParentSessionID: "root-1", LeadSessionID: "root-1", Title: "worker", ProjectKey: "/repo"},
		protocol.UserMessage{Text: "do it"}, protocol.TextDelta{Text: "done"})
	pkg, err := ExportPackage(src.Dir(), child.ID)
	if err != nil {
		t.Fatal(err)
	}

	dst := NewManager(t.TempDir())
	got, err := dst.RestorePackage(pkg)
	if err != nil {
		t.Fatalf("RestorePackage: %v", err)
	}
	if got.ID != "child-1" || got.ParentSessionID != "root-1" || got.LeadSessionID != "root-1" {
		t.Fatalf("restored info = %+v", got)
	}
	if got.Title != "worker" || got.ProjectKey != "/repo" {
		t.Fatalf("restored title/project = %q %q", got.Title, got.ProjectKey)
	}
	back, err := ExportPackage(dst.Dir(), "child-1")
	if err != nil {
		t.Fatal(err)
	}
	if !SameEvents(pkg, back) {
		t.Fatalf("restored events differ:\n%+v\n%+v", pkg.Events, back.Events)
	}

	if _, err := dst.RestorePackage(pkg); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second restore err = %v, want already exists", err)
	}
}

func TestRestorePackageRejectsUnsafeIDs(t *testing.T) {
	dst := NewManager(t.TempDir())
	for _, id := range []string{"", "../escape", "a/b", `a\b`} {
		pkg := Package{Format: PackageFormat, Version: PackageVersion, SourceID: id}
		if _, err := dst.RestorePackage(pkg); err == nil {
			t.Fatalf("RestorePackage(%q) accepted an unsafe id", id)
		}
	}
	list, err := dst.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("unsafe restores created sessions: %+v", list)
	}
}

func TestImportPackageTitledWinsOverLoggedTitle(t *testing.T) {
	src := NewManager(t.TempDir())
	info := writeSession(t, src, CreateOptions{Title: "original"}, protocol.SessionTitled{Title: "from log"})
	pkg, err := ExportPackage(src.Dir(), info.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := src.ImportPackageTitled(pkg, "from log (restored)")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == info.ID {
		t.Fatal("titled import must mint a new id")
	}
	if got.Title != "from log (restored)" {
		t.Fatalf("title = %q", got.Title)
	}
	meta, err := ReadMeta(src.Dir(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "from log (restored)" || meta.ParentSessionID != "" {
		t.Fatalf("meta = %+v", meta)
	}
}

func TestSameEvents(t *testing.T) {
	m := NewManager(t.TempDir())
	a := writeSession(t, m, CreateOptions{}, protocol.UserMessage{Text: "x"}, protocol.TextDelta{Text: "y"})
	b := writeSession(t, m, CreateOptions{}, protocol.UserMessage{Text: "x"}, protocol.TextDelta{Text: "y"})
	c := writeSession(t, m, CreateOptions{}, protocol.UserMessage{Text: "x"}, protocol.TextDelta{Text: "z"})
	d := writeSession(t, m, CreateOptions{}, protocol.UserMessage{Text: "x"})
	pkg := func(id string) Package {
		p, err := ExportPackage(m.Dir(), id)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name string
		x, y Package
		want bool
	}{
		{"same content, different sessions", pkg(a.ID), pkg(b.ID), true},
		{"different payload", pkg(a.ID), pkg(c.ID), false},
		{"different length", pkg(a.ID), pkg(d.ID), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SameEvents(tc.x, tc.y); got != tc.want {
				t.Fatalf("SameEvents = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidatePackage(t *testing.T) {
	cases := []struct {
		name string
		pkg  Package
		ok   bool
	}{
		{"current", Package{Format: PackageFormat, Version: PackageVersion, SchemaVersion: LogSchemaVersion}, true},
		{"wrong format", Package{Format: "x", Version: PackageVersion}, false},
		{"wrong version", Package{Format: PackageFormat, Version: PackageVersion + 1}, false},
		{"future schema", Package{Format: PackageFormat, Version: PackageVersion, SchemaVersion: LogSchemaVersion + 1}, false},
		{"negative schema", Package{Format: PackageFormat, Version: PackageVersion, SchemaVersion: -1}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidatePackage(tc.pkg); (err == nil) != tc.ok {
				t.Fatalf("ValidatePackage err = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
