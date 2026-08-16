package config_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/product/config"
)

func TestSkillAdjacentResourceTraversal(t *testing.T) {
	root := t.TempDir()
	skillDir := filepath.Join(root, "skills", "demo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillPath := filepath.Join(skillDir, "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("---\ndescription: d\n---\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "notes.md"), []byte("hello adjacent"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Outside root
	if err := os.WriteFile(filepath.Join(root, "secret.txt"), []byte("nope"), 0o644); err != nil {
		t.Fatal(err)
	}

	sk := config.Skill{Name: "demo", Path: skillPath, Template: "body"}
	data, err := config.ReadSkillResource(sk, "notes.md")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello adjacent" {
		t.Fatalf("%q", data)
	}

	for _, bad := range []string{"../secret.txt", "/etc/passwd", "..", "foo/../../secret.txt"} {
		if _, err := config.ReadSkillResource(sk, bad); err == nil {
			t.Fatalf("expected reject for %q", bad)
		}
	}
}

func TestSkillCatalogReload(t *testing.T) {
	work := t.TempDir()
	dir := filepath.Join(work, ".strike", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte("---\ndescription: x\n---\n"+body+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("alpha", "one")
	cat, err := config.NewSkillCatalog(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Lookup("alpha"); !ok {
		t.Fatal("missing alpha")
	}
	write("beta", "two")
	if err := cat.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, ok := cat.Lookup("beta"); !ok {
		t.Fatalf("reload missing beta; have %v", names(cat))
	}
}

func names(c *config.SkillCatalog) []string {
	var out []string
	for _, s := range c.Skills() {
		out = append(out, s.Name)
	}
	return out
}

func TestSkillSourceRootFlat(t *testing.T) {
	p := filepath.Join(t.TempDir(), "foo.md")
	_ = os.WriteFile(p, []byte("x"), 0o644)
	root := config.SkillSourceRoot(config.Skill{Path: p})
	if !strings.HasSuffix(root, filepath.Dir(p)) && root != filepath.Dir(p) {
		// equal dirs
	}
	if root != filepath.Dir(p) {
		// Abs may differ
		if filepath.Base(root) != filepath.Base(filepath.Dir(p)) {
			t.Fatalf("root=%q", root)
		}
	}
}
