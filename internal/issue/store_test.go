package issue

import (
	"errors"
	"os"
	"path/filepath"
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
