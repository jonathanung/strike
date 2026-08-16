package providers_test

import (
	"context"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/harness/providers"
	"github.com/jonathanung/strike-cli/harness/providers/auth"
	"github.com/jonathanung/strike-cli/harness/providers/factory"
)

func TestImportConstructsAdapterWithoutInternal(t *testing.T) {
	p, err := providers.NewAnthropic("sk-test")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "anthropic" {
		t.Fatalf("name = %q", p.Name())
	}
	p = providers.NewOpenAI(func(context.Context) (string, error) { return "sk", nil })
	if p.Name() != "openai" {
		t.Fatalf("openai name = %q", p.Name())
	}
	got, model, err := providers.Select("echo", factory.Options{})
	if err != nil || got.Name() != "echo" || model != "echo" {
		t.Fatalf("select echo: p=%v model=%q err=%v", got, model, err)
	}
	_ = auth.TypeAPIKey
}

func TestNoInternalOrEngineImports(t *testing.T) {
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.Contains(p, "/internal/") {
				t.Errorf("%s imports %s", path, p)
			}
			if strings.Contains(p, "harness/engine") {
				t.Errorf("%s imports %s", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
