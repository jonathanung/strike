package tool

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// Product persist / integrate packages the kernel tool package must not import.
var forbiddenKernelImports = []string{
	"github.com/jonathanung/strike-cli/internal/memory",
	"github.com/jonathanung/strike-cli/internal/issue",
	"github.com/jonathanung/strike-cli/internal/plan",
	"github.com/jonathanung/strike-cli/internal/artifact",
	"github.com/jonathanung/strike-cli/internal/ledger",
	"github.com/jonathanung/strike-cli/internal/lsp",
	"github.com/jonathanung/strike-cli/internal/tools",
	"github.com/jonathanung/strike-cli/internal/secret",
}

func TestKernelToolImportBoundary(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imp := strings.Trim(spec.Path.Value, `"`)
			if strings.Contains(imp, "/internal/") {
				t.Errorf("%s imports forbidden %q", path, imp)
				continue
			}
			for _, forbid := range forbiddenKernelImports {
				if imp == forbid {
					t.Errorf("%s imports forbidden %q", path, imp)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
