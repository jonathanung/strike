package harness_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestStandaloneModule(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "module github.com/jonathanung/strike-cli/harness\n") {
		t.Fatalf("go.mod missing module path:\n%s", src)
	}
	if strings.Contains(src, "/internal/") {
		t.Fatalf("harness must not require internal packages:\n%s", src)
	}
	for _, want := range []string{
		"github.com/jonathanung/strike-cli/pkg/protocol",
		"github.com/jonathanung/strike-cli/pkg/redact",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("harness must require %s:\n%s", want, src)
		}
	}
}

func TestProductionImportsHaveNoInternal(t *testing.T) {
	cmd := exec.Command("go", "list", "-e", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
	cmd.Dir = "."
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pkg, imports, _ := strings.Cut(line, " ")
		if strings.Contains(pkg, "/testdata/") {
			continue
		}
		for _, imp := range strings.Fields(imports) {
			if strings.Contains(imp, "/internal/") {
				t.Errorf("%s imports %s", pkg, imp)
			}
		}
	}
}

func TestNoNestedGoMod(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "testdata" || d.Name() == ".git") {
			return filepath.SkipDir
		}
		if path != "go.mod" && d.Name() == "go.mod" {
			t.Errorf("nested go.mod %s (harness is one module)", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
