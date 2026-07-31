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
//
// Charm stack paths are checked separately by TestCharmImportPaths. When E13
// rewrites Bubble Tea / Lip Gloss / Bubbles / Glamour to charm.land/…, update
// charm path helpers (and style_boundary lipglossPath) in the same commit as
// the import rewrites — edit internal/tui/_src/ only, then go generate.
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

// TestCharmImportPaths enforces the Charm module path allowlist across every
// .go file (including tests and cmd/):
//
//   - v1 (current): github.com/charmbracelet/…
//   - v2 (E13):     charm.land/…  (vanity domain)
//
// github.com/charmbracelet/…/v2 is forbidden — that is not a valid Charm v2
// module path. When E13 migrates imports, keep this allowlist in the same
// commit as the rewrites.
func TestCharmImportPaths(t *testing.T) {
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
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		// Skip fixtures that intentionally embed bad import strings.
		if strings.Contains(filepath.ToSlash(rel), "/testdata/") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			imp := strings.Trim(spec.Path.Value, `"`)
			if violation := charmImportViolation(imp); violation != "" {
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
			return "" // stdlib or third-party is fine (Charm paths: TestCharmImportPaths)
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

// charmImportViolation returns a non-empty explanation when imp is a forbidden
// Charm module path. Allowlist:
//
//	github.com/charmbracelet/…   — v1 (current)
//	charm.land/…                 — v2 vanity (E13+)
//
// Forbidden: github.com/charmbracelet/…/v2 (upgrade guides require charm.land).
func charmImportViolation(imp string) string {
	switch {
	case strings.HasPrefix(imp, "charm.land/"):
		return ""
	case strings.HasPrefix(imp, "github.com/charmbracelet/"):
		for _, seg := range strings.Split(imp, "/") {
			if seg == "v2" {
				return "Charm v2 imports must use charm.land/..., not github.com/charmbracelet/.../v2"
			}
		}
		return ""
	default:
		return ""
	}
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

func TestCharmImportViolation(t *testing.T) {
	cases := []struct {
		imp  string
		want string // empty = allowed
	}{
		{"github.com/charmbracelet/bubbletea", ""},
		{"github.com/charmbracelet/lipgloss", ""},
		{"github.com/charmbracelet/bubbles/viewport", ""},
		{"github.com/charmbracelet/glamour/styles", ""},
		{"github.com/charmbracelet/x/ansi", ""},
		{"charm.land/bubbletea/v2", ""},
		{"charm.land/lipgloss/v2", ""},
		{"charm.land/lipgloss/v2/compat", ""},
		{"charm.land/bubbles/v2/viewport", ""},
		{"charm.land/glamour/v2", ""},
		{"github.com/charmbracelet/bubbletea/v2", "Charm v2 imports must use charm.land/..., not github.com/charmbracelet/.../v2"},
		{"github.com/charmbracelet/lipgloss/v2", "Charm v2 imports must use charm.land/..., not github.com/charmbracelet/.../v2"},
		{"github.com/charmbracelet/bubbles/v2", "Charm v2 imports must use charm.land/..., not github.com/charmbracelet/.../v2"},
		{"github.com/other/thing", ""},
		{"fmt", ""},
	}
	for _, tc := range cases {
		got := charmImportViolation(tc.imp)
		if got != tc.want {
			t.Errorf("charmImportViolation(%q) = %q, want %q", tc.imp, got, tc.want)
		}
	}
}
