package tui

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/jonathanung/strike-cli"

// TestArchitectureBoundaries enforces the frontend/backend dependency rules
// from the refactor spec by walking every production (.go, non-_test) file in
// the module with go/parser and checking its imports:
//
//   - internal/tui/** may import no internal/* package other than protocol,
//     host, and tui/... (so the frontend never touches auth/config/models/
//     history directly and can be developed against fakes).
//   - internal/host (the contract package, not host/local) imports the
//     standard library only.
//   - no backend package (internal/* except internal/tui/**) imports
//     internal/tui/**.
//
// A violation names the offending file and import so the boundary cannot be
// crossed silently.
func TestArchitectureBoundaries(t *testing.T) {
	root := moduleRoot(t)
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "vendor" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		pkgDir := filepath.ToSlash(filepath.Dir(rel))

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imp := strings.Trim(spec.Path.Value, `"`)
			if violation := boundaryViolation(pkgDir, imp); violation != "" {
				t.Errorf("%s imports %q: %s", rel, imp, violation)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking module: %v", err)
	}
}

// boundaryViolation returns a non-empty explanation when pkgDir importing imp
// breaks a dependency rule, or "" when the import is allowed.
func boundaryViolation(pkgDir, imp string) string {
	internal := modulePath + "/internal/"
	tuiPrefix := modulePath + "/internal/tui"

	switch {
	case isTUIDir(pkgDir):
		if !strings.HasPrefix(imp, internal) {
			return "" // stdlib or third-party is fine
		}
		suffix := strings.TrimPrefix(imp, internal)
		if suffix == "protocol" || suffix == "host" || suffix == "tui" || strings.HasPrefix(suffix, "tui/") {
			return ""
		}
		return "internal/tui may only import internal/{protocol,host,tui/...}"

	case pkgDir == "internal/host":
		if strings.Contains(imp, ".") {
			return "internal/host is a stdlib-only contract package"
		}
		return ""

	case strings.HasPrefix(pkgDir, "internal/") && !isTUIDir(pkgDir):
		if imp == tuiPrefix || strings.HasPrefix(imp, tuiPrefix+"/") {
			return "backend packages must not import internal/tui"
		}
		return ""
	}
	return ""
}

func isTUIDir(pkgDir string) bool {
	return pkgDir == "internal/tui" || strings.HasPrefix(pkgDir, "internal/tui/")
}

// moduleRoot walks up from the test's working directory to the directory
// holding go.mod.
func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from working directory")
		}
		dir = parent
	}
}
