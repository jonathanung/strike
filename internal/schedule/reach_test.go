package schedule_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestNoAgentPathReachesScheduleService pins design rule 2 of
// docs/design/script-schedules.md: no tool, /v1/ops op, MCP bridge, or SDK
// call reaches schedule.Service. The only writer is the authenticated
// cockpit API (internal/frontend/server/schedules.go), wired by strike
// serve (cmd/strike/schedule_wire.go). A new importer must be justified
// here, not added silently.
func TestNoAgentPathReachesScheduleService(t *testing.T) {
	root := moduleRoot(t)
	const mod = "github.com/jonathanung/strike/"
	allowed := map[string]map[string]bool{
		mod + "internal/schedule": {
			"cmd/strike/schedule_wire.go":           true,
			"internal/frontend/server/schedules.go": true,
			"internal/frontend/server/server.go":    true,
		},
		// The server package holds the Service; only the serve binary may
		// construct it. Tools, engine, MCP, and SDK packages must not.
		mod + "internal/frontend/server": {},
	}
	allowedDirs := map[string]map[string]bool{
		mod + "internal/frontend/server": {"cmd/strike": true},
	}
	got := map[string][]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "target", "webui":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return nil // generated or build-tagged oddities: not an importer we can judge
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			files, watched := allowed[p]
			if !watched {
				continue
			}
			if files[rel] || allowedDirs[p][filepath.ToSlash(filepath.Dir(rel))] {
				continue
			}
			got[p] = append(got[p], rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for p, files := range got {
		sort.Strings(files)
		t.Errorf("%s is imported outside the cockpit API: %v", p, files)
	}
}

// TestNoProtocolOpNamesSchedules: the /v1/ops and WebSocket op set carries
// no schedule op, so an agent or SDK client cannot reach the clock that way.
func TestNoProtocolOpNamesSchedules(t *testing.T) {
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "pkg", "protocol", "op_codec.go"))
	if err != nil {
		t.Fatal(err)
	}
	cases := 0
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "case \"") {
			continue
		}
		cases++
		if strings.Contains(strings.ToLower(line), "schedul") {
			t.Errorf("protocol op decodes a schedule op: %s", line)
		}
	}
	if cases < 5 {
		t.Fatalf("found only %d op cases in op_codec.go; the enumeration is broken", cases)
	}
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
