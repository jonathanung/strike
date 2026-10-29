package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestForNewEmptyRegistryIsUnbound(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	got := ForNew(s, dir, true)
	if got.Bound || got.ID != "" || len(got.Roots) != 0 {
		t.Fatalf("empty registry binding = %+v, want unbound", got)
	}
	if got.Dir == "" {
		t.Fatal("unbound dir is empty")
	}
	if ForNew(nil, dir, true).Bound {
		t.Fatal("nil store should be unbound")
	}
}

func TestForNewPreferActiveUsesWorkspaceDirAndRules(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "proj")
	other := filepath.Join(dir, "other")
	if err := os.Mkdir(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	rules := filepath.Join(ws, "RULES.md")
	if err := os.WriteFile(rules, []byte("be brief\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "escape")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(ws, "out")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.Add(ws, "Proj")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetPolicy(added.ID, &rules, strPtr("plan")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetExtraRoots(added.ID, []string{other, link, "/"}); err == nil {
		t.Fatal("filesystem root and escaping symlink should be rejected")
	}
	if _, err := s.SetExtraRoots(added.ID, []string{other}); err != nil {
		t.Fatal(err)
	}
	got := ForNew(s, dir, true)
	if !got.Bound || got.ID != added.ID {
		t.Fatalf("binding = %+v", got)
	}
	if got.Dir != canonicalDir(ws) {
		t.Fatalf("dir = %q, want workspace", got.Dir)
	}
	if got.PermissionMode != "plan" {
		t.Fatalf("mode = %q", got.PermissionMode)
	}
	if !strings.Contains(got.RulesBlock, "be brief") || !strings.Contains(got.RulesBlock, "Instructions from:") {
		t.Fatalf("rules = %q", got.RulesBlock)
	}
	if len(got.Roots) != 2 {
		t.Fatalf("roots = %#v", got.Roots)
	}
	// Launch inside the workspace, without preferring active, stays put.
	sub := filepath.Join(ws, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	stay := ForNew(s, sub, false)
	if !stay.Bound || stay.Dir != canonicalDir(sub) || stay.Root != canonicalDir(ws) {
		t.Fatalf("subdir binding = %+v", stay)
	}
	resumed := ForResume(s, added.ID, dir)
	if resumed.Dir != canonicalDir(ws) {
		t.Fatalf("resume dir = %q", resumed.Dir)
	}
	missing := ForResume(s, "gone", dir)
	if missing.Bound || len(missing.Roots) != 0 {
		t.Fatalf("missing workspace = %+v", missing)
	}
}

func TestBrowseRejectsEscapeAndMkdirStaysInside(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "allowed")
	outside := filepath.Join(dir, "outside")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "hop")); err != nil {
		t.Fatal(err)
	}
	s, err := Open(filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureBrowse([]string{root})
	if _, err := s.Add(outside, "nope"); err == nil {
		t.Fatal("add outside browse root should fail")
	}
	listed, err := s.Browse(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range listed.Entries {
		if e.Name == "hop" {
			t.Fatalf("symlink escape listed: %+v", listed.Entries)
		}
	}
	if _, err := s.Browse(outside); err == nil {
		t.Fatal("browse outside should fail")
	}
	if _, err := s.Browse(filepath.Join(root, "..", "outside")); err == nil {
		t.Fatal("browse .. should fail")
	}
	created, err := s.Mkdir(root, "fresh")
	if err != nil {
		t.Fatal(err)
	}
	if !withinDir(canonicalDir(root), created) {
		t.Fatalf("mkdir %q escaped %q", created, root)
	}
	if _, err := s.Mkdir(root, ".."); err == nil {
		t.Fatal("mkdir .. should fail")
	}
	if _, err := s.Mkdir(outside, "x"); err == nil {
		t.Fatal("mkdir outside should fail")
	}
	added, err := s.Add(created, "")
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := s.Rename(added.ID, "Fresh folder")
	if err != nil || renamed.Name != "Fresh folder" {
		t.Fatalf("rename = %+v %v", renamed, err)
	}
	if _, err := s.Rename(added.ID, "../x"); err == nil {
		t.Fatal("rename with a separator should fail")
	}
}

func strPtr(s string) *string { return &s }
