package workspace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreAddListActivateRemove(t *testing.T) {
	dir := t.TempDir()
	wsA := filepath.Join(dir, "alpha")
	wsB := filepath.Join(dir, "beta")
	for _, p := range []string{wsA, wsB} {
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.Add(wsA, "")
	if err != nil {
		t.Fatal(err)
	}
	if a.Name != "alpha" || a.ID == "" {
		t.Fatalf("add alpha: %+v", a)
	}
	b, err := s.Add(wsB, "Beta")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Add(wsA, "ignored")
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != a.ID {
		t.Fatalf("re-add not idempotent: %q vs %q", again.ID, a.ID)
	}
	list, active := s.List()
	if len(list) != 2 || active != a.ID {
		t.Fatalf("list=%+v active=%q", list, active)
	}
	if _, err := s.Activate(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, active := s.List(); active != b.ID {
		t.Fatalf("active after activate = %q, want %q", active, b.ID)
	}
	// Reopen persists.
	s2, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if list, _ := s2.List(); len(list) != 2 {
		t.Fatalf("reopened list = %+v", list)
	}
	if err := s.Remove(a.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 1 {
		t.Fatalf("after remove: %+v", list)
	}
	if err := s.Remove(a.ID); err == nil {
		t.Fatal("remove missing id: want error")
	}
	if _, err := s.Activate("nope"); err == nil {
		t.Fatal("activate missing id: want error")
	}
}

func TestStoreAddRejectsNonDir(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add(f, ""); err == nil {
		t.Fatal("add file: want error")
	}
	if _, err := s.Add(filepath.Join(dir, "missing"), ""); err == nil {
		t.Fatal("add missing: want error")
	}
}
