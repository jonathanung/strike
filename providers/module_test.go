package providers

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestStandaloneModule(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	src := string(data)
	if !strings.Contains(src, "module github.com/jonathanung/strike-cli/providers\n") {
		t.Fatalf("go.mod missing module path:\n%s", src)
	}
	if strings.Contains(src, "/internal/") {
		t.Fatalf("providers must not require internal packages:\n%s", src)
	}
	if !strings.Contains(src, "github.com/jonathanung/strike-cli/harness") {
		t.Fatalf("providers must require the harness module (provider interface):\n%s", src)
	}
	if strings.Contains(src, "harness/engine") {
		t.Fatalf("providers must not require harness/engine:\n%s", src)
	}
}

func TestImportsAreProviderSeamOnly(t *testing.T) {
	cmd := exec.Command("go", "list", "-e", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
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
		for _, imp := range strings.Fields(imports) {
			if strings.Contains(imp, "/internal/") {
				t.Errorf("%s imports %s", pkg, imp)
			}
			if strings.HasPrefix(imp, "github.com/jonathanung/strike-cli/harness/") &&
				imp != "github.com/jonathanung/strike-cli/harness/provider" &&
				!strings.HasPrefix(imp, "github.com/jonathanung/strike-cli/harness/provider/") {
				t.Errorf("%s imports %s (providers may only use harness/provider)", pkg, imp)
			}
		}
	}
}
