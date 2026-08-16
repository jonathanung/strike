package issue

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenCreateGetListClose(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	a, err := s.Create("fix auth", "login fails")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != 1 || a.Status != StatusOpen || a.Title != "fix auth" || a.Body != "login fails" {
		t.Fatalf("create a = %#v", a)
	}
	b, err := s.Create("docs", "")
	if err != nil {
		t.Fatal(err)
	}
	if b.ID != 2 {
		t.Fatalf("create b id = %d", b.ID)
	}

	got, ok, err := s.Get(1)
	if err != nil || !ok {
		t.Fatalf("Get 1: ok=%v err=%v", ok, err)
	}
	if got.Title != "fix auth" {
		t.Errorf("title = %q", got.Title)
	}

	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != 1 || all[1].ID != 2 {
		t.Fatalf("List all = %#v", all)
	}

	closed, err := s.CloseIssue(1)
	if err != nil {
		t.Fatal(err)
	}
	if closed.Status != StatusClosed {
		t.Fatalf("close status = %q", closed.Status)
	}
	openOnly, err := s.List(StatusOpen)
	if err != nil || len(openOnly) != 1 || openOnly[0].ID != 2 {
		t.Fatalf("List open = %#v err=%v", openOnly, err)
	}
	closedOnly, err := s.List(StatusClosed)
	if err != nil || len(closedOnly) != 1 || closedOnly[0].ID != 1 {
		t.Fatalf("List closed = %#v err=%v", closedOnly, err)
	}
}

func TestUpdate(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	iss, err := s.Create("old", "body")
	if err != nil {
		t.Fatal(err)
	}
	title := "new title"
	body := "new body"
	status := StatusClosed
	got, err := s.Update(iss.ID, &title, &body, &status)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != title || got.Body != body || got.Status != StatusClosed {
		t.Fatalf("updated = %#v", got)
	}
	if _, err := s.Update(99, &title, nil, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	bad := "nope"
	if _, err := s.Update(iss.ID, nil, nil, &bad); !errors.Is(err, errInvalidStatus) {
		t.Fatalf("bad status = %v", err)
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

	if _, err := a.Create("secret", ""); err != nil {
		t.Fatal(err)
	}
	list, err := b.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("project b saw a's issues: %#v", list)
	}
	if a.Path() == b.Path() {
		t.Fatal("projects share the same issues file path")
	}
}

func TestPersistAcrossOpen(t *testing.T) {
	root := t.TempDir()
	s1, err := Open(root, "key")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := s1.Create("keep", "me")
	if err != nil {
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
	got, ok, err := s2.Get(iss.ID)
	if err != nil || !ok {
		t.Fatalf("reload Get: ok=%v err=%v", ok, err)
	}
	if got.Title != "keep" || got.Body != "me" || got.Status != StatusOpen {
		t.Errorf("reloaded = %#v", got)
	}
	next, err := s2.Create("second", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != iss.ID+1 {
		t.Fatalf("next id = %d, want %d", next.ID, iss.ID+1)
	}
}

func TestCreateValidation(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if _, err := s.Create("  ", ""); !errors.Is(err, errEmptyTitle) {
		t.Fatalf("empty title = %v", err)
	}
	if _, err := s.Create(string(make([]byte, maxTitleLen+1)), ""); !errors.Is(err, errTitleTooLong) {
		t.Fatalf("title too long = %v", err)
	}
	if _, err := s.Create("ok", string(make([]byte, maxBodyLen+1))); !errors.Is(err, errBodyTooLong) {
		t.Fatalf("body too long = %v", err)
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
	if _, err := s.Create("x", ""); !errors.Is(err, errClosed) {
		t.Fatalf("Create after close = %v", err)
	}
	if _, _, err := s.Get(1); !errors.Is(err, errClosed) {
		t.Fatalf("Get after close = %v", err)
	}
	if _, err := s.List(""); !errors.Is(err, errClosed) {
		t.Fatalf("List after close = %v", err)
	}
}

func TestFileMode(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Create("k", "v"); err != nil {
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

func TestListInvalidStatus(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.List("wip"); !errors.Is(err, errInvalidStatus) {
		t.Fatalf("List bad status = %v", err)
	}
}

func TestExportImportRoundTrip(t *testing.T) {
	root := t.TempDir()
	src, err := Open(root, "src")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = src.Close() })
	a, err := src.Create("fix auth", "login fails")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.Create("docs", "write me"); err != nil {
		t.Fatal(err)
	}
	if _, err := src.CloseIssue(a.ID); err != nil {
		t.Fatal(err)
	}

	exportPath := filepath.Join(t.TempDir(), "issues.json")
	if err := src.Export(exportPath); err != nil {
		t.Fatal(err)
	}

	if _, err := src.ImportBytes([]byte(`{"format":"strike.issues","version":1,"next_id":1,"issues":[]}`), true); err != nil {
		t.Fatal(err)
	}
	if all, err := src.List(""); err != nil || len(all) != 0 {
		t.Fatalf("after wipe = %+v err=%v", all, err)
	}

	n, err := src.Import(exportPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("imported = %d, want 2", n)
	}
	got, ok, err := src.Get(1)
	if err != nil || !ok || got.Title != "fix auth" || got.Status != StatusClosed {
		t.Fatalf("restored #1 = %+v ok=%v err=%v", got, ok, err)
	}
	got2, ok, err := src.Get(2)
	if err != nil || !ok || got2.Title != "docs" {
		t.Fatalf("restored #2 = %+v ok=%v err=%v", got2, ok, err)
	}
	next, err := src.Create("third", "")
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != 3 {
		t.Fatalf("next id = %d, want 3", next.ID)
	}
}

func TestImportIntoOtherProject(t *testing.T) {
	root := t.TempDir()
	a, err := Open(root, "/proj/a")
	if err != nil {
		t.Fatal(err)
	}
	iss, err := a.Create("secret", "body")
	if err != nil {
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
	n, err := b.Import(path, false)
	if err != nil || n != 1 {
		t.Fatalf("import = %d err=%v", n, err)
	}
	got, ok, err := b.Get(iss.ID)
	if err != nil || !ok || got.Title != "secret" || got.Body != "body" {
		t.Fatalf("b got = %+v ok=%v err=%v", got, ok, err)
	}
}

func TestImportMergeAndReplace(t *testing.T) {
	s, err := Open(t.TempDir(), "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.Create("keep", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("gone", "x"); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{
  "format": "strike.issues",
  "version": 1,
  "next_id": 10,
  "issues": [
    {"id": 1, "title": "keep-new", "body": "n", "status": "closed"},
    {"id": 5, "title": "added", "status": "open"}
  ]
}`)
	n, err := s.ImportBytes(data, false)
	if err != nil || n != 2 {
		t.Fatalf("merge = %d err=%v", n, err)
	}
	if got, ok, _ := s.Get(1); !ok || got.Title != "keep-new" || got.Status != StatusClosed {
		t.Fatalf("merge #1 = %+v", got)
	}
	if _, ok, _ := s.Get(2); !ok {
		t.Fatal("merge should retain #2")
	}
	if _, ok, _ := s.Get(5); !ok {
		t.Fatal("merge missing #5")
	}

	n, err = s.ImportBytes(data, true)
	if err != nil || n != 2 {
		t.Fatalf("replace = %d err=%v", n, err)
	}
	if _, ok, _ := s.Get(2); ok {
		t.Fatal("replace should drop #2")
	}
	all, err := s.List("")
	if err != nil || len(all) != 2 {
		t.Fatalf("after replace = %+v err=%v", all, err)
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
		{name: "wrong format", data: `{"format":"other","version":1,"issues":[]}`, want: "unsupported format"},
		{name: "wrong version", data: `{"format":"strike.issues","version":99,"issues":[]}`, want: "unsupported version"},
		{name: "empty", data: "  ", want: "empty file"},
		{name: "bad id", data: `{"format":"strike.issues","version":1,"issues":[{"id":0,"title":"x","status":"open"}]}`, want: "invalid id"},
		{name: "dup id", data: `{"format":"strike.issues","version":1,"issues":[{"id":1,"title":"a","status":"open"},{"id":1,"title":"b","status":"open"}]}`, want: "duplicate id"},
		{name: "bad status", data: `{"format":"strike.issues","version":1,"issues":[{"id":1,"title":"a","status":"wip"}]}`, want: "status must be open or closed"},
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
