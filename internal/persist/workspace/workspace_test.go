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

func TestSetPolicyRulesAndMode(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "proj")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(ws, "AGENTS.md")
	if err := os.WriteFile(rules, []byte("# rules\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "secret.md")
	if err := os.WriteFile(outside, []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "escape.md")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Add(ws, "proj")
	if err != nil {
		t.Fatal(err)
	}
	mode := "plan"
	rel := "AGENTS.md"
	got, err := s.SetPolicy(added.ID, &rel, &mode)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rules != rules || got.PermissionMode != "plan" {
		t.Fatalf("policy = %+v", got)
	}
	// Omitted fields stay. Empty rules clears.
	onlyMode := "yolo"
	got, err = s.SetPolicy(added.ID, nil, &onlyMode)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rules != rules || got.PermissionMode != "yolo" {
		t.Fatalf("partial update = %+v", got)
	}
	clear := ""
	got, err = s.SetPolicy(added.ID, &clear, &clear)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rules != "" || got.PermissionMode != "" {
		t.Fatalf("clear = %+v", got)
	}
	if _, err := s.SetPolicy(added.ID, &outside, nil); err == nil {
		t.Fatal("rules outside workspace: want error")
	}
	if _, err := s.SetPolicy(added.ID, &link, nil); err == nil {
		t.Fatal("symlink escape: want error")
	}
	missing := "missing.md"
	if _, err := s.SetPolicy(added.ID, &missing, nil); err == nil {
		t.Fatal("missing rules file: want error")
	}
	if _, err := s.SetPolicy("nope", &rel, nil); err == nil {
		t.Fatal("missing id: want error")
	}
	s2, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	list, _ := s2.List()
	if len(list) != 1 || list[0].PermissionMode != "" || list[0].Rules != "" {
		t.Fatalf("reopen = %+v", list)
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
