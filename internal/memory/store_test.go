package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenPutGetListDelete(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if err := s.Put("api.base", "https://example.com", []string{"config", "url"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("note", "hello", nil); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.Get("api.base")
	if err != nil || !ok {
		t.Fatalf("Get api.base: ok=%v err=%v", ok, err)
	}
	if got.Value != "https://example.com" {
		t.Errorf("value = %q", got.Value)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "config" || got.Tags[1] != "url" {
		t.Errorf("tags = %#v", got.Tags)
	}

	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("List all = %d, want 2", len(all))
	}
	if all[0].Key != "api.base" || all[1].Key != "note" {
		t.Errorf("order = %q, %q", all[0].Key, all[1].Key)
	}

	tagged, err := s.List("config")
	if err != nil {
		t.Fatal(err)
	}
	if len(tagged) != 1 || tagged[0].Key != "api.base" {
		t.Fatalf("List tag = %#v", tagged)
	}

	if err := s.Delete("note"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get("note"); err != nil || ok {
		t.Fatalf("Get after delete: ok=%v err=%v", ok, err)
	}
	if err := s.Delete("missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Delete missing = %v, want ErrNotFound", err)
	}
}

func TestProjectIsolation(t *testing.T) {
	root := t.TempDir()
	a, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Open(root, "/proj/b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	if err := a.Put("secret", "from-a", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := b.Get("secret"); err != nil || ok {
		t.Fatalf("project b saw a's key: ok=%v err=%v", ok, err)
	}
	if a.Path() == b.Path() {
		t.Fatal("projects share the same memory file path")
	}
}

func TestPersistAcrossOpen(t *testing.T) {
	root := t.TempDir()
	s1, err := Open(root, "key")
	if err != nil {
		t.Fatal(err)
	}
	if err := s1.Put("k", "v", []string{"t"}); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(root, "key")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s2.Close() })
	got, ok, err := s2.Get("k")
	if err != nil || !ok {
		t.Fatalf("reload Get: ok=%v err=%v", ok, err)
	}
	if got.Value != "v" || len(got.Tags) != 1 || got.Tags[0] != "t" {
		t.Errorf("reloaded = %#v", got)
	}
}

func TestPutValidation(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cases := []struct {
		name  string
		key   string
		value string
		tags  []string
		want  error
	}{
		{name: "empty key", key: "  ", want: errEmptyKey},
		{name: "key too long", key: string(make([]byte, maxKeyLen+1)), want: errKeyTooLong},
		{name: "value too long", key: "k", value: string(make([]byte, maxValueLen+1)), want: errValueTooLong},
		{name: "tag too long", key: "k", tags: []string{string(make([]byte, maxTagLen+1))}, want: errTagTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Put(tc.key, tc.value, tc.tags)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Put = %v, want %v", err, tc.want)
			}
		})
	}
	tooMany := make([]string, maxTags+1)
	for i := range tooMany {
		tooMany[i] = string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	if err := s.Put("k", "v", tooMany); !errors.Is(err, errTooManyTags) {
		t.Fatalf("too many tags = %v, want %v", err, errTooManyTags)
	}
}

func TestClosedStore(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("k", "v", nil); !errors.Is(err, errClosed) {
		t.Fatalf("Put after close = %v", err)
	}
	if _, _, err := s.Get("k"); !errors.Is(err, errClosed) {
		t.Fatalf("Get after close = %v", err)
	}
	if _, err := s.List(""); !errors.Is(err, errClosed) {
		t.Fatalf("List after close = %v", err)
	}
}

func TestReplaceUpdatesValue(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Put("k", "one", []string{"a"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("k", "two", []string{"b"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("k")
	if err != nil || !ok {
		t.Fatal(err)
	}
	if got.Value != "two" {
		t.Errorf("value = %q", got.Value)
	}
	if len(got.Tags) != 1 || got.Tags[0] != "b" {
		t.Errorf("tags = %#v", got.Tags)
	}
}

func TestFileMode(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Put("k", "v", nil); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file mode = %o, want 600", mode)
	}
	dirInfo, err := os.Stat(filepath.Dir(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if mode := dirInfo.Mode().Perm(); mode != 0o700 {
		t.Errorf("dir mode = %o, want 700", mode)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	root := t.TempDir()
	src, err := Open(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	if err := src.Put("api.base", "https://example.com", []string{"config"}); err != nil {
		t.Fatal(err)
	}
	if err := src.Put("note", "hello", nil); err != nil {
		t.Fatal(err)
	}

	exportPath := filepath.Join(t.TempDir(), "memory.json")
	if err := src.Export(exportPath); err != nil {
		t.Fatal(err)
	}

	// Wipe source via replace import of empty snapshot is not enough — clear by replace with empty entries file.
	empty := []byte(`{"format":"strike.memory","version":1,"entries":[]}` + "\n")
	if _, err := src.ImportBytes(empty, true); err != nil {
		t.Fatal(err)
	}
	if all, err := src.List(""); err != nil || len(all) != 0 {
		t.Fatalf("after wipe list = %+v err=%v", all, err)
	}

	n, err := src.Import(exportPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("imported = %d, want 2", n)
	}
	got, ok, err := src.Get("api.base")
	if err != nil || !ok || got.Value != "https://example.com" {
		t.Fatalf("restored api.base = %+v ok=%v err=%v", got, ok, err)
	}
	note, ok, err := src.Get("note")
	if err != nil || !ok || note.Value != "hello" {
		t.Fatalf("restored note = %+v ok=%v err=%v", note, ok, err)
	}
}

func TestImportIntoOtherProject(t *testing.T) {
	root := t.TempDir()
	a, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Put("shared", "from-a", []string{"t"}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "handoff.json")
	if err := a.Export(path); err != nil {
		t.Fatal(err)
	}
	_ = a.Close()

	b, err := Open(root, "/proj/b")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if _, ok, err := b.Get("shared"); err != nil || ok {
		t.Fatalf("b should start empty: ok=%v err=%v", ok, err)
	}
	n, err := b.Import(path, false)
	if err != nil || n != 1 {
		t.Fatalf("import = %d err=%v", n, err)
	}
	got, ok, err := b.Get("shared")
	if err != nil || !ok || got.Value != "from-a" {
		t.Fatalf("b got = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestImportMergeAndReplace(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.Put("keep", "old", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Put("gone", "x", nil); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{
  "format": "strike.memory",
  "version": 1,
  "entries": [
    {"key": "keep", "value": "new", "tags": ["t"]},
    {"key": "added", "value": "y"}
  ]
}`)
	n, err := s.ImportBytes(data, false)
	if err != nil || n != 2 {
		t.Fatalf("merge = %d err=%v", n, err)
	}
	if got, ok, _ := s.Get("keep"); !ok || got.Value != "new" {
		t.Fatalf("merge keep = %+v", got)
	}
	if _, ok, _ := s.Get("gone"); !ok {
		t.Fatal("merge should retain gone")
	}
	if _, ok, _ := s.Get("added"); !ok {
		t.Fatal("merge missing added")
	}

	n, err = s.ImportBytes(data, true)
	if err != nil || n != 2 {
		t.Fatalf("replace = %d err=%v", n, err)
	}
	if _, ok, _ := s.Get("gone"); ok {
		t.Fatal("replace should drop gone")
	}
	all, err := s.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("after replace list = %+v err=%v", all, err)
	}
}

func TestImportBadJSONAndVersion(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	cases := []struct {
		name string
		data string
		want string
	}{
		{name: "bad json", data: "{not json", want: "bad JSON"},
		{name: "wrong format", data: `{"format":"other","version":1,"entries":[]}`, want: "unsupported format"},
		{name: "wrong version", data: `{"format":"strike.memory","version":99,"entries":[]}`, want: "unsupported version"},
		{name: "empty", data: "   ", want: "empty file"},
		{name: "duplicate key", data: `{"format":"strike.memory","version":1,"entries":[{"key":"a","value":"1"},{"key":"a","value":"2"}]}`, want: "duplicate key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.ImportBytes([]byte(tc.data), true)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}
