package engine

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// TestProductionImportsAreKernelOnly locks #1206: engine production code may
// not import Strike product packages.
func TestProductionImportsAreKernelOnly(t *testing.T) {
	allowed := map[string]bool{
		"github.com/jonathanung/strike-cli/internal/fn":         true,
		"github.com/jonathanung/strike-cli/internal/permission": true,
		"github.com/jonathanung/strike-cli/internal/protocol":   true,
		"github.com/jonathanung/strike-cli/internal/provider":   true,
		"github.com/jonathanung/strike-cli/internal/question":   true,
		"github.com/jonathanung/strike-cli/internal/sandbox":    true,
		"github.com/jonathanung/strike-cli/internal/scheduler":  true,
		"github.com/jonathanung/strike-cli/internal/tool":       true,
		"github.com/jonathanung/strike-cli/internal/verify":     true,
		"github.com/jonathanung/strike-cli/pkg/redact":          true,
		"github.com/jonathanung/strike-cli/pkg/protocol":        true,
		"github.com/jonathanung/strike-cli/pkg/diag":            true,
	}
	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "testdata" {
				return fs.SkipDir
			}
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
			if !strings.HasPrefix(imp, "github.com/jonathanung/strike-cli/") {
				continue
			}
			if allowed[imp] {
				continue
			}
			t.Errorf("%s imports %q (product/non-kernel)", path, imp)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
