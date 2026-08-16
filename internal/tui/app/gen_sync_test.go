package tui

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSrcFlattenInSync ensures _src/ is the edited tree and flattened copies match.
func TestSrcFlattenInSync(t *testing.T) {
	root := moduleRoot(t)
	tuiDir := filepath.Join(root, "internal", "tui", "app")
	// Copy current flattened snapshot.
	before := map[string][]byte{}
	ents, err := os.ReadDir(tuiDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if e.IsDir() || e.Name() == "doc.go" || e.Name() == "gen_src.go" || e.Name() == "gen_sync_test.go" {
			continue
		}
		if filepath.Ext(e.Name()) != ".go" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(tuiDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		before[e.Name()] = b
	}
	cmd := exec.Command("go", "run", "gen_src.go", ".")
	cmd.Dir = tuiDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run gen_src.go: %v\n%s", err, out)
	}
	for name, old := range before {
		neu, err := os.ReadFile(filepath.Join(tuiDir, name))
		if err != nil {
			t.Errorf("after generate missing %s: %v", name, err)
			continue
		}
		if !bytes.Equal(old, neu) {
			t.Errorf("%s differs from _src flatten; edit _src/ and run go generate ./internal/tui/app", name)
		}
	}
}

func TestGenerateRemovesLegacyParentFlatten(t *testing.T) {
	root := moduleRoot(t)
	parent := filepath.Join(root, "internal", "tui")
	leftover := filepath.Join(parent, "legacy_flatten.go")
	if err := os.WriteFile(leftover, []byte("package tui\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(leftover) })
	cmd := exec.Command("go", "run", "gen_src.go", ".")
	cmd.Dir = filepath.Join(parent, "app")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run gen_src.go: %v\n%s", err, out)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("legacy parent flatten still present: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "generate.go")); err != nil {
		t.Fatalf("parent generate shim missing after cleanup: %v", err)
	}
}

// TestTUIParentListsAppAndKit locks the #1209 layout: parent internal/tui
// lists only the app package and kit packages, not flattened app sources.
func TestTUIParentListsAppAndKit(t *testing.T) {
	root := moduleRoot(t)
	dir := filepath.Join(root, "internal", "tui")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"app": true, "common": true, "term": true, "theme": true, "ui": true}
	for _, e := range ents {
		if !e.IsDir() {
			if e.Name() == "generate.go" {
				continue
			}
			if strings.HasSuffix(e.Name(), ".go") {
				t.Errorf("parent internal/tui has Go file %s; flatten target is internal/tui/app", e.Name())
			}
			continue
		}
		if !want[e.Name()] {
			t.Errorf("unexpected directory internal/tui/%s", e.Name())
			continue
		}
		delete(want, e.Name())
	}
	for name := range want {
		t.Errorf("missing directory internal/tui/%s", name)
	}
}
