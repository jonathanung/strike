package providers

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestNoNestedGoMod(t *testing.T) {
	if _, err := os.Stat("go.mod"); err == nil {
		t.Fatal("harness/providers must not have its own go.mod")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat go.mod: %v", err)
	}
}

func TestImportsAreProviderSeamOnly(t *testing.T) {
	cmd := exec.Command("go", "list", "-e", "-f", "{{.ImportPath}} {{join .Imports \" \"}}", "./...")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	var sawNetHTTP bool
	const (
		providerPkg  = "github.com/jonathanung/strike-cli/harness/provider"
		providersPkg = "github.com/jonathanung/strike-cli/harness/providers"
		enginePkg    = "github.com/jonathanung/strike-cli/harness/engine"
	)
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pkg, imports, _ := strings.Cut(line, " ")
		for _, imp := range strings.Fields(imports) {
			if imp == "net/http" {
				sawNetHTTP = true
			}
			if strings.Contains(imp, "/internal/") {
				t.Errorf("%s imports %s", pkg, imp)
			}
			if imp == enginePkg || strings.HasPrefix(imp, enginePkg+"/") {
				t.Errorf("%s imports %s (adapters must not import harness/engine)", pkg, imp)
			}
			if strings.HasPrefix(imp, "github.com/jonathanung/strike-cli/harness/") &&
				imp != providerPkg &&
				!strings.HasPrefix(imp, providerPkg+"/") &&
				imp != providersPkg &&
				!strings.HasPrefix(imp, providersPkg+"/") {
				t.Errorf("%s imports %s (adapters may only use harness/provider and sibling adapters)", pkg, imp)
			}
		}
	}
	if !sawNetHTTP {
		t.Fatal("harness adapters must use net/http (go list ./providers/...)")
	}
}
