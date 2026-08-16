package tui

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Leaf workspace modules extracted so the harness module can import
// protocol/redact without a cycle through the root module (#1204, #1208, #1232).
var leafModules = []struct {
	dir    string
	module string
	// stdlib is true when the leaf go.mod must have no require/replace.
	stdlib bool
}{
	{"pkg/protocol", modulePath + "/pkg/protocol", true},
	{"pkg/redact", modulePath + "/pkg/redact", true},
	{"harness", modulePath + "/harness", false},
}

// Packages that must stay in the root module until a later extract wave.
var rootOnlyPkgDirs = []string{
	"pkg/sdk",
	"pkg/timeline",
	"pkg/diag",
	"pkg/telemetry",
}

func TestWorkspaceModules(t *testing.T) {
	root := moduleRoot(t)

	work := readRepoFile(t, filepath.Join(root, "go.work"))
	uses := parseGoWorkUse(work)
	wantUses := []string{".", "./pkg/protocol", "./pkg/redact", "./harness"}
	for _, want := range wantUses {
		if !uses[want] {
			t.Errorf("go.work missing use %q", want)
		}
	}
	if len(uses) != len(wantUses) {
		t.Errorf("go.work use set = %v, want exactly %v", keys(uses), wantUses)
	}

	rootMod := readRepoFile(t, filepath.Join(root, "go.mod"))
	if got := parseGoModModule(rootMod); got != modulePath {
		t.Errorf("root go.mod module = %q, want %q", got, modulePath)
	}
	requires := parseGoModRequires(rootMod)
	replaces := parseGoModReplaces(rootMod)
	for _, leaf := range leafModules {
		if !requires[leaf.module] {
			t.Errorf("root go.mod missing require %s", leaf.module)
		}
		if got := replaces[leaf.module]; got != "./"+leaf.dir {
			t.Errorf("root go.mod replace %s = %q, want %q", leaf.module, got, "./"+leaf.dir)
		}

		mod := readRepoFile(t, filepath.Join(root, leaf.dir, "go.mod"))
		if got := parseGoModModule(mod); got != leaf.module {
			t.Errorf("%s/go.mod module = %q, want %q", leaf.dir, got, leaf.module)
		}
		if leaf.stdlib {
			if extra := parseGoModRequires(mod); len(extra) > 0 {
				t.Errorf("%s/go.mod must stay stdlib-only; unexpected require %v", leaf.dir, keys(extra))
			}
			if extra := parseGoModReplaces(mod); len(extra) > 0 {
				t.Errorf("%s/go.mod must not replace; got %v", leaf.dir, extra)
			}
			continue
		}
		reqs := parseGoModRequires(mod)
		switch leaf.dir {
		case "harness":
			for _, want := range []string{modulePath + "/pkg/protocol", modulePath + "/pkg/redact"} {
				if !reqs[want] {
					t.Errorf("%s/go.mod missing require %s", leaf.dir, want)
				}
			}
			if strings.Contains(mod, "/internal/") {
				t.Errorf("%s/go.mod must not mention internal packages", leaf.dir)
			}
		}
	}

	for _, dir := range rootOnlyPkgDirs {
		path := filepath.Join(root, dir, "go.mod")
		if _, err := os.Stat(path); err == nil {
			t.Errorf("%s/go.mod exists; that package is not extracted yet", dir)
		} else if !os.IsNotExist(err) {
			t.Errorf("stat %s: %v", path, err)
		}
	}

	if _, err := os.Stat(filepath.Join(root, "providers", "go.mod")); err == nil {
		t.Error("providers/go.mod exists; adapters live in the harness module")
	} else if !os.IsNotExist(err) {
		t.Errorf("stat providers/go.mod: %v", err)
	}
	if requires[modulePath+"/providers"] {
		t.Error("root go.mod still requires github.com/jonathanung/strike-cli/providers")
	}
	if _, ok := replaces[modulePath+"/providers"]; ok {
		t.Error("root go.mod still replaces github.com/jonathanung/strike-cli/providers")
	}
}

func readRepoFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func parseGoWorkUse(src string) map[string]bool {
	out := map[string]bool{}
	inUse := false
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" {
			continue
		}
		switch {
		case strings.HasPrefix(line, "use ("):
			inUse = true
		case line == ")" && inUse:
			inUse = false
		case inUse:
			out[line] = true
		case strings.HasPrefix(line, "use "):
			out[strings.TrimSpace(strings.TrimPrefix(line, "use "))] = true
		}
	}
	return out
}

func parseGoModModule(src string) string {
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "module ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "module "))
		}
	}
	return ""
}

func parseGoModRequires(src string) map[string]bool {
	out := map[string]bool{}
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
		case line == ")" && inBlock:
			inBlock = false
		case inBlock:
			if fields := strings.Fields(line); len(fields) >= 1 {
				out[fields[0]] = true
			}
		case strings.HasPrefix(line, "require "):
			if fields := strings.Fields(strings.TrimPrefix(line, "require ")); len(fields) >= 1 {
				out[fields[0]] = true
			}
		}
	}
	return out
}

func parseGoModReplaces(src string) map[string]string {
	out := map[string]string{}
	inBlock := false
	sc := bufio.NewScanner(strings.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.Index(line, "//"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case strings.HasPrefix(line, "replace ("):
			inBlock = true
		case line == ")" && inBlock:
			inBlock = false
		case inBlock:
			addReplace(out, line)
		case strings.HasPrefix(line, "replace "):
			addReplace(out, strings.TrimSpace(strings.TrimPrefix(line, "replace ")))
		}
	}
	return out
}

func addReplace(out map[string]string, line string) {
	old, neu, ok := strings.Cut(line, "=>")
	if !ok {
		return
	}
	of := strings.Fields(old)
	nf := strings.Fields(neu)
	if len(of) >= 1 && len(nf) >= 1 {
		out[of[0]] = nf[0]
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestParseGoWorkUse(t *testing.T) {
	src := `go 1.26.2

use (
	.
	./pkg/protocol
	./pkg/redact
)
`
	got := parseGoWorkUse(src)
	for _, want := range []string{".", "./pkg/protocol", "./pkg/redact"} {
		if !got[want] {
			t.Errorf("missing %q in %v", want, keys(got))
		}
	}
	if len(got) != 3 {
		t.Errorf("got %v, want 3 entries", keys(got))
	}

	single := parseGoWorkUse("use ./pkg/protocol\n")
	if !single["./pkg/protocol"] {
		t.Errorf("single-line use = %v", keys(single))
	}
}

func TestParseGoModModuleAndRequires(t *testing.T) {
	src := `module github.com/jonathanung/strike-cli

require (
	github.com/jonathanung/strike-cli/pkg/protocol v0.0.0
	github.com/other/dep v1.2.3 // indirect
)

require github.com/single/line v0.1.0
`
	if got := parseGoModModule(src); got != modulePath {
		t.Errorf("module = %q", got)
	}
	got := parseGoModRequires(src)
	for _, want := range []string{
		modulePath + "/pkg/protocol",
		"github.com/other/dep",
		"github.com/single/line",
	} {
		if !got[want] {
			t.Errorf("missing require %q in %v", want, keys(got))
		}
	}
}

func TestParseGoModReplaces(t *testing.T) {
	src := `
replace github.com/jonathanung/strike-cli/pkg/protocol => ./pkg/protocol

replace github.com/jonathanung/strike-cli/pkg/redact => ./pkg/redact
`
	got := parseGoModReplaces(src)
	if got[modulePath+"/pkg/protocol"] != "./pkg/protocol" {
		t.Errorf("protocol replace = %q", got[modulePath+"/pkg/protocol"])
	}
	if got[modulePath+"/pkg/redact"] != "./pkg/redact" {
		t.Errorf("redact replace = %q", got[modulePath+"/pkg/redact"])
	}
}
