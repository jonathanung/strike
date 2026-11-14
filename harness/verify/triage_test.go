package verify

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const goTestFailOutput = `--- FAIL: TestBroken (0.00s)
    verify_test.go:42: got false
--- FAIL: TestTable (0.00s)
    --- FAIL: TestTable/case_one (0.00s)
        verify_test.go:77: mismatch
FAIL
FAIL	example.com/mod/pkg/a	0.01s
ok  	example.com/mod/pkg/ok	0.02s
# example.com/mod/nested/b
nested/b/b.go:3:2: undefined: x
FAIL	example.com/mod/nested/b [build failed]
FAIL
`

func TestParseGoTestFailures(t *testing.T) {
	t.Parallel()
	got := ParseGoTestFailures(goTestFailOutput)
	want := []Failure{
		{Package: "example.com/mod/pkg/a", Tests: []string{"TestBroken", "TestTable"}, Source: "verify_test.go:42"},
		{Package: "example.com/mod/nested/b", Source: "nested/b/b.go:3"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestParseGoTestFailuresIgnoresNoiseAndInjection(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"empty":       "",
		"not go test": "error: lint failed\nFAILED 3 checks\n",
		"only ok":     "ok  \texample.com/mod/a\t0.1s\n",
		"shell meta":  "--- FAIL: Test$(rm -rf /) (0.00s)\nFAIL\texample.com/$(touch pwned)\t0.1s\n",
	}
	for name, out := range cases {
		if got := ParseGoTestFailures(out); len(got) != 0 {
			t.Errorf("%s: got %+v, want none", name, got)
		}
	}
}

func TestParseGoTestFailuresCapsPackages(t *testing.T) {
	t.Parallel()
	var b strings.Builder
	for i := 0; i < maxTriagePackages+3; i++ {
		b.WriteString("FAIL\texample.com/mod/p" + string(rune('a'+i)) + "\t0.1s\n")
	}
	if got := ParseGoTestFailures(b.String()); len(got) != maxTriagePackages {
		t.Fatalf("len=%d want %d", len(got), maxTriagePackages)
	}
}

func TestFocusedCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   []Failure
		want string
	}{
		{"none", nil, ""},
		{"unresolved package", []Failure{{Package: "other.com/x", Tests: []string{"TestX"}}}, ""},
		{"single test", []Failure{{Dir: "./pkg/a", Tests: []string{"TestBroken"}}},
			"go test './pkg/a' -run '^(TestBroken)$' -count=1"},
		{"build failure only", []Failure{{Dir: "./nested/b"}}, "go test './nested/b' -count=1"},
		{"multi package dedup", []Failure{
			{Dir: "./pkg/a", Tests: []string{"TestA", "TestB"}},
			{Dir: "./pkg/a", Tests: []string{"TestA"}},
			{Dir: "."},
		}, "go test './pkg/a' '.' -run '^(TestA|TestB)$' -count=1"},
		{"traversal rejected", []Failure{{Dir: "./../etc"}}, ""},
		{"bad test name dropped", []Failure{{Dir: "./a", Tests: []string{"Test;rm"}}}, "go test './a' -count=1"},
	}
	for _, tc := range tests {
		if got := FocusedCommand(tc.in); got != tc.want {
			t.Errorf("%s: got %q want %q", tc.name, got, tc.want)
		}
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func goWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "go.mod"), "// root\nmodule example.com/mod\n\ngo 1.26\n")
	writeFile(t, filepath.Join(dir, "go.work"), "go 1.26\n\nuse (\n\t. // root\n\t./nested\n\t../escape\n)\nuse ./single\n")
	writeFile(t, filepath.Join(dir, "nested", "go.mod"), "module \"example.com/mod/nested\"\n")
	writeFile(t, filepath.Join(dir, "single", "go.mod"), "module example.com/single\n")
	return dir
}

func TestResolveDirsWorkspace(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	failures := []Failure{
		{Package: "example.com/mod/pkg/a"},
		{Package: "example.com/mod/nested/b"},
		{Package: "example.com/mod"},
		{Package: "example.com/single/x"},
		{Package: "example.com/modextra/y"},
		{Package: "other.com/z"},
	}
	resolveDirs(failures, workspaceModules(dir))
	want := []string{"./pkg/a", "./nested/b", ".", "./single/x", "", ""}
	for i, f := range failures {
		if f.Dir != want[i] {
			t.Errorf("%s: dir=%q want %q", f.Package, f.Dir, want[i])
		}
	}
	if mods := workspaceModules(t.TempDir()); mods != nil {
		t.Fatalf("non-module dir: %+v", mods)
	}
}

// scriptedRunner fakes command execution: the full gate and the focused
// command each return the next scripted result; calls are recorded.
type scriptedRunner struct {
	full, focused []int
	calls         []string
}

func (s *scriptedRunner) run(_ context.Context, _, command string) (int, string, error) {
	s.calls = append(s.calls, command)
	if strings.HasPrefix(command, "go test '") {
		code := s.focused[0]
		s.focused = s.focused[1:]
		if code == 0 {
			return 0, "ok  \texample.com/mod/pkg/a\t0.01s\n", nil
		}
		return code, "--- FAIL: TestBroken (0.00s)\n    a_test.go:9: still broken\nFAIL\texample.com/mod/pkg/a\t0.01s\n", nil
	}
	code := s.full[0]
	s.full = s.full[1:]
	if code == 0 {
		return 0, "all ok\n", nil
	}
	return code, "--- FAIL: TestBroken (0.00s)\n    a_test.go:9: broken\nFAIL\texample.com/mod/pkg/a\t0.01s\nFAIL\n", nil
}

const focusedA = "go test './pkg/a' -run '^(TestBroken)$' -count=1"

func TestTriageFlowFocusedFirstThenFullGate(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	sr := &scriptedRunner{full: []int{2, 0}, focused: []int{1, 1, 0}}
	r := &Runner{WorkDir: dir, CmdRunner: sr.run, Triage: &Triage{}}
	gates := []Gate{{Kind: KindCmd, Value: "make test"}}
	in := Input{Claimed: true}

	// Run 1: full gate fails → parsed → one focused reproduction.
	res := r.Run(context.Background(), gates, in)
	c := res.Checks[0]
	if res.Passed || c.Triage == nil {
		t.Fatalf("run1: %+v", c)
	}
	if c.Value != "make test" || !strings.Contains(c.Output, "a_test.go:9: broken") || c.ExitCode != 2 {
		t.Fatalf("run1 lost original command/output: %+v", c)
	}
	tr := c.Triage
	if tr.FocusedCommand != focusedA || len(tr.Attempts) != 1 || tr.Attempts[0].Passed || tr.FullGateSkipped || tr.Exhausted {
		t.Fatalf("run1 triage: %+v", tr)
	}
	if tr.Failures[0].Dir != "./pkg/a" || tr.Failures[0].Source != "a_test.go:9" {
		t.Fatalf("run1 failures: %+v", tr.Failures)
	}
	if !reflect.DeepEqual(sr.calls, []string{"make test", focusedA}) {
		t.Fatalf("run1 calls: %q", sr.calls)
	}

	// Run 2: focused rerun still fails → full gate skipped, original kept.
	sr.calls = nil
	res = r.Run(context.Background(), gates, in)
	c = res.Checks[0]
	if res.Passed || !c.Triage.FullGateSkipped || len(c.Triage.Attempts) != 2 {
		t.Fatalf("run2: %+v triage=%+v", c, c.Triage)
	}
	if !strings.Contains(c.Triage.OriginalOutput, "a_test.go:9: broken") || !strings.Contains(c.Output, "still broken") {
		t.Fatalf("run2 outputs: output=%q original=%q", c.Output, c.Triage.OriginalOutput)
	}
	if !strings.Contains(c.Error, "full gate skipped") || c.ExitCode != 1 {
		t.Fatalf("run2 error: %+v", c)
	}
	if !reflect.DeepEqual(sr.calls, []string{focusedA}) {
		t.Fatalf("run2 calls: %q", sr.calls)
	}

	// Run 3: focused passes → required full gate runs and passes → reset.
	sr.calls = nil
	res = r.Run(context.Background(), gates, in)
	c = res.Checks[0]
	if !res.Passed || c.Triage == nil || len(c.Triage.Attempts) != 3 || !c.Triage.Attempts[2].Passed || c.Triage.FullGateSkipped {
		t.Fatalf("run3: passed=%v triage=%+v", res.Passed, c.Triage)
	}
	if !reflect.DeepEqual(sr.calls, []string{focusedA, "make test"}) {
		t.Fatalf("run3 calls: %q", sr.calls)
	}
	if _, ok := r.Triage.load(triageKey(dir, "make test")); ok {
		t.Fatal("history must reset after the full gate passes")
	}
}

func TestTriageBudgetExhaustedFallsBackToFullGate(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	sr := &scriptedRunner{full: []int{1, 1, 1, 1}, focused: []int{1, 0, 1}}
	r := &Runner{WorkDir: dir, CmdRunner: sr.run, Triage: &Triage{}}
	gates := []Gate{{Kind: KindCmd, Value: "go test ./..."}}
	var res Result
	for i := 0; i < 4; i++ {
		res = r.Run(context.Background(), gates, Input{Claimed: true})
	}
	// Run1: full+repro; run2: focused pass→full fail+repro; run3: budget
	// spent → full only; run4: full only. Attempts never exceed the cap.
	tr := res.Checks[0].Triage
	if tr == nil || len(tr.Attempts) != MaxFocusedAttempts || !tr.Exhausted {
		t.Fatalf("triage: %+v", tr)
	}
	want := []string{"go test ./...", focusedA, focusedA, "go test ./...", focusedA, "go test ./...", "go test ./..."}
	if !reflect.DeepEqual(sr.calls, want) {
		t.Fatalf("calls:\n got %q\nwant %q", sr.calls, want)
	}
	lines := strings.Join(FailedCheckLines(res), "\n")
	for _, s := range []string{"triage: focused rerun: " + focusedA, "triage: attempt 3/3 failed", "budget exhausted", "failing package example.com/mod/pkg/a tests TestBroken at a_test.go:9"} {
		if !strings.Contains(lines, s) {
			t.Errorf("FailedCheckLines missing %q:\n%s", s, lines)
		}
	}
}

func TestTriageSkipsNonGoFailuresAndDisabledRunner(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	calls := 0
	cmd := func(_ context.Context, _, _ string) (int, string, error) {
		calls++
		return 1, "lint: 3 issues", nil
	}
	r := &Runner{WorkDir: dir, CmdRunner: cmd, Triage: &Triage{}}
	res := r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "make lint"}}, Input{Claimed: true})
	if res.Checks[0].Triage != nil || calls != 1 {
		t.Fatalf("non-go failure triaged: calls=%d %+v", calls, res.Checks[0].Triage)
	}

	// Go failure without a Go module root: nothing focusable, no reruns.
	sr := &scriptedRunner{full: []int{1}}
	r = &Runner{WorkDir: t.TempDir(), CmdRunner: sr.run, Triage: &Triage{}}
	res = r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "go test ./..."}}, Input{Claimed: true})
	if res.Checks[0].Triage != nil || len(sr.calls) != 1 {
		t.Fatalf("no-module triage: calls=%q %+v", sr.calls, res.Checks[0].Triage)
	}

	// Triage nil: legacy single run.
	sr = &scriptedRunner{full: []int{1}}
	r = &Runner{WorkDir: dir, CmdRunner: sr.run}
	res = r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "go test ./..."}}, Input{Claimed: true})
	if res.Checks[0].Triage != nil || len(sr.calls) != 1 || res.Checks[0].Error != "exit 1" {
		t.Fatalf("disabled triage: calls=%q %+v", sr.calls, res.Checks[0])
	}
}

func TestTriageReportIsolatedFromState(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	sr := &scriptedRunner{full: []int{1}, focused: []int{1}}
	r := &Runner{WorkDir: dir, CmdRunner: sr.run, Triage: &Triage{}}
	res := r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "make test"}}, Input{Claimed: true})
	res.Checks[0].Triage.Attempts[0].Command = "mutated"
	res.Checks[0].Triage.Failures[0].Tests[0] = "mutated"
	st, _ := r.Triage.load(triageKey(dir, "make test"))
	if st.Attempts[0].Command != focusedA || st.Failures[0].Tests[0] != "TestBroken" {
		t.Fatalf("caller mutation leaked into state: %+v", st)
	}
}

func TestTriageCmdErrorKeepsHistory(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	tri := &Triage{}
	tri.store(triageKey(dir, "make test"), TriageReport{FocusedCommand: focusedA, Attempts: []TriageAttempt{{Command: focusedA}}})
	r := &Runner{WorkDir: dir, Triage: tri, CmdRunner: func(context.Context, string, string) (int, string, error) {
		return -1, "", context.DeadlineExceeded
	}}
	res := r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "make test"}}, Input{Claimed: true})
	c := res.Checks[0]
	// The focused pre-check errored (not a go test failure) so the full gate
	// ran and errored too; both runs' attempts persist toward the cap.
	if c.Passed || c.Triage == nil || len(c.Triage.Attempts) != 2 || !strings.Contains(c.Error, "deadline") {
		t.Fatalf("%+v triage=%+v", c, c.Triage)
	}
	if st, _ := tri.load(triageKey(dir, "make test")); len(st.Attempts) != 2 {
		t.Fatalf("pre-check attempt not persisted: %+v", st)
	}
}

func TestTriageConcurrentSameGateKeepsHistory(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	fail := func(_ context.Context, _, command string) (int, string, error) {
		return 1, "--- FAIL: TestBroken (0.00s)\nFAIL\texample.com/mod/pkg/a\t0.01s\n", nil
	}
	tri := &Triage{}
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			r := &Runner{WorkDir: dir, CmdRunner: fail, Triage: tri}
			r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "make test"}}, Input{Claimed: true})
		}()
	}
	for i := 0; i < 4; i++ {
		<-done
	}
	// Serialized per key: every run's attempt counts, capped at the budget.
	st, _ := tri.load(triageKey(dir, "make test"))
	if len(st.Attempts) != MaxFocusedAttempts || !st.Exhausted {
		t.Fatalf("attempts=%d exhausted=%v", len(st.Attempts), st.Exhausted)
	}
}

func TestTriageEnvironmentalFocusedFailureRunsFullGate(t *testing.T) {
	t.Parallel()
	dir := goWorkspace(t)
	tri := &Triage{}
	tri.store(triageKey(dir, "make test"), TriageReport{FocusedCommand: focusedA, Attempts: []TriageAttempt{{Command: focusedA}}})
	var calls []string
	r := &Runner{WorkDir: dir, Triage: tri, CmdRunner: func(_ context.Context, _, command string) (int, string, error) {
		calls = append(calls, command)
		if command == focusedA {
			return 127, "sh: go: not found", nil
		}
		return 0, "ok", nil
	}}
	res := r.Run(context.Background(), []Gate{{Kind: KindCmd, Value: "make test"}}, Input{Claimed: true})
	if !res.Passed || !reflect.DeepEqual(calls, []string{focusedA, "make test"}) {
		t.Fatalf("passed=%v calls=%q", res.Passed, calls)
	}
	if tr := res.Checks[0].Triage; tr == nil || len(tr.Attempts) != 2 || tr.Attempts[1].Passed || tr.FullGateSkipped {
		t.Fatalf("triage=%+v", tr)
	}
}
