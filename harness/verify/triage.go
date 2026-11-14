package verify

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// MaxFocusedAttempts caps focused reruns per cmd gate across verification
// runs. The budget resets only when the full gate passes, so repeated
// failures produce a bounded history instead of an unbounded retry loop.
const MaxFocusedAttempts = 3

const (
	maxTriagePackages = 4
	maxTriageTests    = 8
	maxAttemptOutput  = 1024
)

var (
	failedTestRE = regexp.MustCompile(`^\s*--- FAIL: (\S+)`)
	failedPkgRE  = regexp.MustCompile(`^FAIL\s+(\S+)(?:\s|$)`)
	okPkgRE      = regexp.MustCompile(`^ok\s+\S+`)
	sourceRE     = regexp.MustCompile(`^\s*([^\s:]+\.go):(\d+)(?::\d+)?:`)
	importPathRE = regexp.MustCompile(`^[A-Za-z0-9._~/-]+$`)
	testNameRE   = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Failure is one failing Go package (and its failing top-level tests when
// reported) parsed from go test output.
type Failure struct {
	// Package is the import path printed by go test.
	Package string `json:"package"`
	// Dir is the module-relative directory used for focused reruns
	// ("./harness/verify"). Empty when the package is outside the workspace.
	Dir string `json:"dir,omitempty"`
	// Tests are failing top-level test names (subtests collapse to parent).
	Tests []string `json:"tests,omitempty"`
	// Source is the first file:line reported for this package.
	Source string `json:"source,omitempty"`
}

// TriageAttempt is one focused rerun in a gate's bounded history.
type TriageAttempt struct {
	Command  string `json:"command"`
	Passed   bool   `json:"passed"`
	ExitCode int    `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
	Error    string `json:"error,omitempty"`
}

// TriageReport is automatic failure triage for one cmd gate. The gate's own
// Value is the exact original command; OriginalOutput keeps the last full
// gate failure output even when a later run skipped the full gate.
type TriageReport struct {
	Failures       []Failure `json:"failures,omitempty"`
	FocusedCommand string    `json:"focused_command,omitempty"`
	OriginalOutput string    `json:"original_output,omitempty"`
	// Attempts is the focused rerun history since the gate last passed
	// (at most MaxFocusedAttempts).
	Attempts []TriageAttempt `json:"attempts,omitempty"`
	// FullGateSkipped is true when the focused rerun still failed, so the
	// broader gate was not rerun this time.
	FullGateSkipped bool `json:"full_gate_skipped,omitempty"`
	// Exhausted is true once the focused rerun budget is spent; later runs
	// go straight to the full gate until it passes.
	Exhausted bool `json:"exhausted,omitempty"`
}

// Triage carries per-gate failure triage state across Runner.Run calls
// (one per engine). Safe for concurrent use. The zero value is ready.
type Triage struct {
	mu    sync.Mutex
	gates map[string]TriageReport
	// keyMu serializes runs of the same gate key so concurrent child
	// verifications cannot lose attempt-history updates.
	keyMu map[string]*sync.Mutex
}

// lockKey holds the per-key run lock; call the returned func to release.
func (t *Triage) lockKey(key string) func() {
	t.mu.Lock()
	if t.keyMu == nil {
		t.keyMu = make(map[string]*sync.Mutex)
	}
	m := t.keyMu[key]
	if m == nil {
		m = &sync.Mutex{}
		t.keyMu[key] = m
	}
	t.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func (t *Triage) load(key string) (TriageReport, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	rep, ok := t.gates[key]
	return cloneTriage(rep), ok
}

func (t *Triage) store(key string, rep TriageReport) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gates == nil {
		t.gates = make(map[string]TriageReport)
	}
	t.gates[key] = cloneTriage(rep)
}

func (t *Triage) clear(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.gates, key)
}

func cloneTriage(rep TriageReport) TriageReport {
	rep.Failures = append([]Failure(nil), rep.Failures...)
	for i := range rep.Failures {
		rep.Failures[i].Tests = append([]string(nil), rep.Failures[i].Tests...)
	}
	rep.Attempts = append([]TriageAttempt(nil), rep.Attempts...)
	return rep
}

// ParseGoTestFailures extracts failing packages, their failing top-level
// tests, and the first source location from standard (non-JSON) go test
// output. Packages are returned in output order, at most maxTriagePackages.
func ParseGoTestFailures(output string) []Failure {
	var (
		out     []Failure
		seen    = map[string]bool{}
		tests   []string
		source  string
		scanner = bufio.NewScanner(strings.NewReader(output))
	)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	reset := func() { tests, source = nil, "" }
	for scanner.Scan() {
		line := scanner.Text()
		if m := failedTestRE.FindStringSubmatch(line); m != nil {
			name, _, _ := strings.Cut(m[1], "/")
			if testNameRE.MatchString(name) && !containsString(tests, name) {
				tests = append(tests, name)
			}
			continue
		}
		if m := failedPkgRE.FindStringSubmatch(line); m != nil {
			pkg := strings.TrimSuffix(m[1], ".test")
			if importPathRE.MatchString(pkg) && !seen[pkg] && len(out) < maxTriagePackages {
				seen[pkg] = true
				out = append(out, Failure{Package: pkg, Tests: tests, Source: source})
			}
			reset()
			continue
		}
		if okPkgRE.MatchString(line) {
			reset()
			continue
		}
		if source == "" {
			if m := sourceRE.FindStringSubmatch(line); m != nil {
				source = m[1] + ":" + m[2]
			}
		}
	}
	return out
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// FocusedCommand returns one go test command covering the failing packages
// that resolved to a workspace directory, filtered to their failing tests.
// Empty when nothing is focusable. Every interpolated value is validated and
// single-quoted.
func FocusedCommand(failures []Failure) string {
	var dirs, tests []string
	for _, f := range failures {
		if f.Dir == "" || !importPathRE.MatchString(f.Dir) || strings.Contains(f.Dir, "..") {
			continue
		}
		if !containsString(dirs, f.Dir) {
			dirs = append(dirs, f.Dir)
		}
		for _, name := range f.Tests {
			if testNameRE.MatchString(name) && !containsString(tests, name) && len(tests) < maxTriageTests {
				tests = append(tests, name)
			}
		}
	}
	if len(dirs) == 0 {
		return ""
	}
	parts := []string{"go", "test"}
	for _, d := range dirs {
		parts = append(parts, shellQuote(d))
	}
	if len(tests) > 0 {
		parts = append(parts, "-run", shellQuote("^("+strings.Join(tests, "|")+")$"))
	}
	parts = append(parts, "-count=1")
	return strings.Join(parts, " ")
}

func shellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

type goModule struct {
	path string // import path
	dir  string // workDir-relative, slash-separated ("." for root)
}

// workspaceModules reads go.mod (and go.work use entries) under workDir.
// Returns nil when workDir is not a Go module root, which disables triage:
// focused reruns only use the repository's own go test workflow.
func workspaceModules(workDir string) []goModule {
	if workDir == "" {
		workDir = "."
	}
	dirs := []string{"."}
	if b, err := os.ReadFile(filepath.Join(workDir, "go.work")); err == nil {
		dirs = append(dirs, parseGoWorkUse(string(b))...)
	}
	var mods []goModule
	seen := map[string]bool{}
	for _, d := range dirs {
		d = path.Clean(filepath.ToSlash(d))
		if seen[d] || strings.HasPrefix(d, "..") || path.IsAbs(d) {
			continue
		}
		seen[d] = true
		b, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(d), "go.mod"))
		if err != nil {
			continue
		}
		if mp := parseModulePath(string(b)); mp != "" {
			mods = append(mods, goModule{path: mp, dir: d})
		}
	}
	// Longest import path first so nested modules win.
	sort.SliceStable(mods, func(i, j int) bool { return len(mods[i].path) > len(mods[j].path) })
	return mods
}

func parseModulePath(gomod string) string {
	for _, line := range strings.Split(gomod, "\n") {
		line = strings.TrimSpace(stripLineComment(line))
		if rest, ok := strings.CutPrefix(line, "module"); ok && rest != "" && (rest[0] == ' ' || rest[0] == '\t') {
			return strings.Trim(strings.TrimSpace(rest), `"`)
		}
	}
	return ""
}

func parseGoWorkUse(gowork string) []string {
	var out []string
	inBlock := false
	for _, line := range strings.Split(gowork, "\n") {
		line = strings.TrimSpace(stripLineComment(line))
		switch {
		case inBlock && line == ")":
			inBlock = false
		case inBlock && line != "":
			out = append(out, strings.Trim(line, `"`))
		case line == "use (":
			inBlock = true
		case strings.HasPrefix(line, "use "):
			out = append(out, strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "use ")), `"`))
		}
	}
	return out
}

func stripLineComment(line string) string {
	if i := strings.Index(line, "//"); i >= 0 {
		return line[:i]
	}
	return line
}

// resolveDirs fills Failure.Dir for packages inside a workspace module.
func resolveDirs(failures []Failure, mods []goModule) {
	for i := range failures {
		pkg := failures[i].Package
		for _, m := range mods {
			rest, ok := strings.CutPrefix(pkg, m.path)
			if !ok || (rest != "" && rest[0] != '/') {
				continue
			}
			rel := path.Clean(path.Join(m.dir, strings.TrimPrefix(rest, "/")))
			if rel == "." {
				failures[i].Dir = "."
			} else {
				failures[i].Dir = "./" + rel
			}
			break
		}
	}
}

func triageKey(workDir, command string) string {
	return workDir + "\x00" + command
}

// runTriagedCmd runs a cmd gate with focused-rerun triage. When a previous
// run left a focused command and budget remains, that narrow target runs
// first: still failing with a go test failure skips the broad gate; passing
// (or an unrecognized failure) falls through to the full gate (the required
// verification). A full-gate failure is parsed and
// reproduced once with the focused command while budget remains.
func (r *Runner) runTriagedCmd(ctx context.Context, cr *CheckResult, command string) {
	key := triageKey(r.WorkDir, command)
	defer r.Triage.lockKey(key)()
	rep, had := r.Triage.load(key)
	if had && rep.FocusedCommand != "" && len(rep.Attempts) < MaxFocusedAttempts {
		att, raw := r.focusedAttempt(ctx, rep.FocusedCommand)
		rep.Attempts = append(rep.Attempts, att)
		// Skip the broad gate only on a recognizable go test failure; an
		// environmental failure (missing go, spawn error) falls through.
		if !att.Passed && att.ExitCode > 0 && len(ParseGoTestFailures(raw)) > 0 {
			rep.FullGateSkipped = true
			rep.Exhausted = len(rep.Attempts) >= MaxFocusedAttempts
			r.Triage.store(key, rep)
			cr.ExitCode = att.ExitCode
			cr.Output = att.Output
			cr.Error = "focused rerun still failing; full gate skipped"
			out := cloneTriage(rep)
			cr.Triage = &out
			return
		}
	}
	rep.FullGateSkipped = false

	code, output, err := r.runCmd(ctx, command)
	cr.Output = trimOutput(output)
	cr.ExitCode = code
	switch {
	case err != nil:
		cr.Error = err.Error()
	case code == 0:
		cr.Passed = true
	default:
		cr.Error = fmt.Sprintf("exit %d", code)
	}
	if cr.Passed {
		r.Triage.clear(key)
		if had {
			// Report the history that led to this pass, then reset.
			rep.Exhausted = false
			out := cloneTriage(rep)
			cr.Triage = &out
		}
		return
	}
	if err != nil {
		// Spawn/timeout failure: nothing to parse; keep any prior history
		// (including this run's focused attempt, so it counts toward the cap).
		if had {
			r.Triage.store(key, rep)
			out := cloneTriage(rep)
			cr.Triage = &out
		}
		return
	}

	failures := ParseGoTestFailures(output)
	resolveDirs(failures, workspaceModules(r.WorkDir))
	rep.Failures = failures
	rep.FocusedCommand = FocusedCommand(failures)
	rep.OriginalOutput = cr.Output
	if rep.FocusedCommand == "" && len(rep.Attempts) == 0 {
		// Not a focusable Go test failure: no triage noise.
		r.Triage.clear(key)
		return
	}
	if rep.FocusedCommand != "" && len(rep.Attempts) < MaxFocusedAttempts {
		att, _ := r.focusedAttempt(ctx, rep.FocusedCommand)
		rep.Attempts = append(rep.Attempts, att)
	}
	rep.Exhausted = rep.FocusedCommand != "" && len(rep.Attempts) >= MaxFocusedAttempts
	r.Triage.store(key, rep)
	out := cloneTriage(rep)
	cr.Triage = &out
}

// focusedAttempt runs command and returns the trimmed attempt plus the raw
// output (for parsing).
func (r *Runner) focusedAttempt(ctx context.Context, command string) (TriageAttempt, string) {
	code, output, err := r.runCmd(ctx, command)
	att := TriageAttempt{Command: command, ExitCode: code, Output: trimTo(output, maxAttemptOutput)}
	switch {
	case err != nil:
		att.Error = err.Error()
	case code == 0:
		att.Passed = true
	default:
		att.Error = fmt.Sprintf("exit %d", code)
	}
	return att, output
}

// TriageLines summarizes a check's triage for blockers: the focused rerun
// target and a one-line-per-attempt history.
func TriageLines(c CheckResult) []string {
	t := c.Triage
	if t == nil {
		return nil
	}
	var out []string
	for _, f := range t.Failures {
		line := "failing package " + f.Package
		if len(f.Tests) > 0 {
			line += " tests " + strings.Join(f.Tests, ",")
		}
		if f.Source != "" {
			line += " at " + f.Source
		}
		out = append(out, line)
	}
	if t.FocusedCommand != "" {
		out = append(out, "focused rerun: "+t.FocusedCommand)
	}
	for i, a := range t.Attempts {
		st := "passed"
		if !a.Passed {
			st = "failed"
			if a.Error != "" {
				st += " (" + a.Error + ")"
			}
		}
		out = append(out, fmt.Sprintf("attempt %d/%d %s: %s", i+1, MaxFocusedAttempts, st, a.Command))
	}
	if t.FullGateSkipped {
		out = append(out, "full gate skipped until the focused rerun passes")
	}
	if t.Exhausted {
		out = append(out, "focused rerun budget exhausted; fix the failure, then the full gate reruns")
	}
	return out
}
