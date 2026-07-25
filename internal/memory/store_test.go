package memory

import (
	"errors"
	"os"
	"path/filepath"
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
