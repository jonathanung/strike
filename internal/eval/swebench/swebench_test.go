package swebench

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultSubsetIDs(t *testing.T) {
	ids := DefaultSubsetIDs()
	if len(ids) != DefaultSubsetSize {
		t.Fatalf("subset size = %d, want %d", len(ids), DefaultSubsetSize)
	}
	// Sorted + unique.
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("subset not strictly sorted unique at %d: %q %q", i, ids[i-1], ids[i])
		}
	}
	// Spot-check known members from the committed list.
	want := map[string]bool{
		"django__django-10973": true,
		"sympy__sympy-24562":   true,
	}
	for _, id := range ids {
		delete(want, id)
	}
	if len(want) > 0 {
		t.Fatalf("missing expected ids: %v", want)
	}
}

func TestParseInstancesJSONLAndArray(t *testing.T) {
	path := filepath.Join("testdata", "instances_fixture.jsonl")
	all, err := LoadInstancesJSONL(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d instances", len(all))
	}
	if len(all[0].FailToPass) != 1 {
		t.Fatalf("FAIL_TO_PASS parse: %+v", all[0].FailToPass)
	}

	// JSON array form.
	raw, _ := json.Marshal(all)
	all2, err := ParseInstances(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(all2) != 2 {
		t.Fatalf("array parse: %d", len(all2))
	}
}

func TestDockerImageName(t *testing.T) {
	got := DockerImageName("django__django-10973")
	want := "docker.io/swebench/sweb.eval.x86_64.django_1776_django-10973:latest"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestParseExecJSON(t *testing.T) {
	raw := []byte(`{"type":"result","ok":true,"text":"done","sessionId":"s1","provider":"echo","model":"echo","usage":{"input":10,"output":5}}`)
	res, err := ParseExecJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.SessionID != "s1" || res.Usage == nil || res.Usage.Input != 10 {
		t.Fatalf("%+v", res)
	}
	// Last line wins.
	multi := []byte("noise\n" + string(raw) + "\n")
	res2, err := ParseExecJSON(multi)
	if err != nil || !res2.OK {
		t.Fatalf("multi: %v %+v", err, res2)
	}
}

func TestBuildAgentPrompt(t *testing.T) {
	p := BuildAgentPrompt(Instance{
		InstanceID:       "fixture__repo-1",
		Repo:             "fixture/repo",
		ProblemStatement: "Fix add()",
	})
	if !strings.Contains(p, "fixture__repo-1") || !strings.Contains(p, "Fix add()") {
		t.Fatalf("prompt: %s", p)
	}
}

func TestExtractPatch(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := execCommand(dir, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, out)
		}
	}
	run("git", "init")
	run("git", "config", "user.email", "t@example.com")
	run("git", "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("git", "add", "a.txt")
	run("git", "commit", "-m", "init")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("new\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	patch, err := ExtractPatch(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(patch, "a.txt") {
		t.Fatalf("expected a.txt in patch: %s", patch)
	}
	if !strings.Contains(patch, "b.txt") {
		t.Fatalf("expected untracked b.txt in patch: %s", patch)
	}
}

func execCommand(dir string, args ...string) *testCmd {
	return &testCmd{dir: dir, args: args}
}

type testCmd struct {
	dir  string
	args []string
}

func (c *testCmd) CombinedOutput() ([]byte, error) {
	// local helper using os/exec without importing name clash
	return combinedOutput(c.dir, c.args[0], c.args[1:]...)
}

func TestBuildReportAndWrite(t *testing.T) {
	resolved := true
	unresolved := false
	results := []InstanceResult{
		{InstanceID: "b", Status: StatusResolved, Resolved: &resolved, TokensIn: 100, TokensOut: 50, CostUSD: 0.01, WallClockMs: 1000},
		{InstanceID: "a", Status: StatusUnresolved, Resolved: &unresolved, TokensIn: 10, WallClockMs: 500},
		{InstanceID: "c", Status: StatusError, Error: "boom", WallClockMs: 100},
	}
	rep := BuildReport("run1", results, ReportMeta{Provider: "echo", Model: "echo", Grader: "none"}, time.Date(2026, 8, 6, 0, 0, 0, 0, time.UTC))
	if rep.Resolved != 1 || rep.Unresolved != 1 || rep.Errors != 1 {
		t.Fatalf("counts: %+v", rep)
	}
	if rep.PassRate != 0.5 {
		t.Fatalf("pass rate %v", rep.PassRate)
	}
	if rep.Results[0].InstanceID != "a" {
		t.Fatalf("sort: %v", rep.Results[0].InstanceID)
	}
	if rep.Note == "" || rep.SchemaVersion != ReportSchemaVersion {
		t.Fatalf("meta: %+v", rep)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	if err := WriteReport(path, rep); err != nil {
		t.Fatal(err)
	}
	got, err := LoadReport(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunID != "run1" || got.TotalTokensIn != 110 {
		t.Fatalf("%+v", got)
	}
	text := FormatReport(rep)
	if !strings.Contains(text, "pass_rate=") || !strings.Contains(text, "Internal regression") {
		t.Fatalf("format: %s", text)
	}
}

func TestFixedCost(t *testing.T) {
	// $1/M input, $2/M output
	c := FixedCost{InputPerM: 1, OutputPerM: 2}
	got := c.Estimate("p", "m", Usage{Input: 1_000_000, Output: 500_000})
	want := 1.0 + 1.0 // 1 + 0.5*2
	if got != want {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFilterSubset(t *testing.T) {
	all := []Instance{{InstanceID: "a"}, {InstanceID: "b"}, {InstanceID: "c"}}
	got, err := FilterSubset(all, []string{"c", "a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].InstanceID != "c" || got[1].InstanceID != "a" {
		t.Fatalf("%+v", got)
	}
	_, err = FilterSubset(all, []string{"a", "missing"})
	if err == nil {
		t.Fatal("expected missing error")
	}
}

func TestDatasetClientFetch(t *testing.T) {
	row := map[string]any{
		"instance_id":       "x__y-1",
		"repo":              "x/y",
		"base_commit":       "abc",
		"problem_statement": "fix it",
		"FAIL_TO_PASS":      `["t1"]`,
		"PASS_TO_PASS":      []string{},
	}
	rowJSON, _ := json.Marshal(row)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"rows":           []map[string]any{{"row": json.RawMessage(rowJSON)}},
			"num_rows_total": 1,
		})
	}))
	defer srv.Close()
	c := &DatasetClient{HTTP: srv.Client(), BaseURL: srv.URL}
	all, err := c.FetchInstances(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].InstanceID != "x__y-1" || len(all[0].FailToPass) != 1 {
		t.Fatalf("%+v", all)
	}
}

func TestRunnerDryRunAndMock(t *testing.T) {
	fixtures := loadFixture(t)
	out := t.TempDir()
	work := t.TempDir()

	// Dry-run
	r := &Runner{}
	rep, err := r.Run(context.Background(), Config{
		Instances: fixtures,
		RunID:     "dry1",
		OutDir:    filepath.Join(out, "dry"),
		WorkRoot:  work,
		DryRun:    true,
		Grader:    GraderNone,
		Provider:  "echo",
		Model:     "echo",
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Skipped != 2 || rep.Attempted != 0 {
		t.Fatalf("dry: %+v", rep)
	}

	// Full mock path: materialize + agent + patch + grade
	rt := &fakeRuntime{}
	agent := &fakeAgent{res: ExecResult{
		OK: true, Provider: "echo", Model: "echo", SessionID: "sess",
		Usage: &Usage{Input: 20, Output: 10},
	}}
	grader := &fakeGrader{res: GradeResult{Resolved: true, FailToPassOK: 1, FailToPassN: 1}}
	r2 := &Runner{
		RT:    rt,
		Agent: agent,
		Grade: grader,
		Cost:  FixedCost{InputPerM: 1, OutputPerM: 1},
		Now: func() time.Time {
			return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		},
		Materialize: func(ctx context.Context, instanceID, hostDir string, pull bool) (MaterializeResult, error) {
			repo := filepath.Join(hostDir, "repo")
			if err := os.MkdirAll(repo, 0o755); err != nil {
				return MaterializeResult{}, err
			}
			return MaterializeResult{WorkDir: repo, Image: DockerImageName(instanceID)}, nil
		},
		ExtractPatch: func(workDir string) (string, error) {
			return "diff --git a/x b/x\n+ok\n", nil
		},
	}
	rep2, err := r2.Run(context.Background(), Config{
		Instances:     fixtures[:1],
		RunID:         "mock1",
		OutDir:        filepath.Join(out, "mock"),
		WorkRoot:      work,
		Provider:      "echo",
		Model:         "echo",
		Grader:        GraderNone, // overridden by r2.Grade
		PullImages:    false,
		KeepWorkspace: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rep2.Resolved != 1 || rep2.PassRate != 1.0 {
		t.Fatalf("mock report: %+v", rep2)
	}
	if rep2.Results[0].TokensIn != 20 || rep2.Results[0].CostUSD <= 0 {
		t.Fatalf("metrics: %+v", rep2.Results[0])
	}
	// predictions written
	predPath := filepath.Join(out, "mock", "predictions.jsonl")
	data, err := os.ReadFile(predPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte("fixture__repo-1")) {
		t.Fatalf("preds: %s", data)
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls %d", agent.calls)
	}
}

func TestCLIRuntimeCreateArgs(t *testing.T) {
	var saw []string
	rt := &CLIRuntime{
		LookPath: func(string) (string, error) { return "/usr/bin/docker", nil },
		Run: func(ctx context.Context, name string, args ...string) (string, string, int, error) {
			saw = append([]string{name}, args...)
			if len(args) > 0 && args[0] == "create" {
				return "cid123\n", "", 0, nil
			}
			return "", "", 0, nil
		},
	}
	id, err := rt.Create(context.Background(), "img:latest", CreateOpts{WorkDir: "/testbed"})
	if err != nil {
		t.Fatal(err)
	}
	if id != "cid123" {
		t.Fatalf("id %q", id)
	}
	joined := strings.Join(saw, " ")
	if !strings.Contains(joined, "create") || !strings.Contains(joined, "img:latest") {
		t.Fatalf("args %v", saw)
	}
}

func TestBuildTestCommandDjango(t *testing.T) {
	cmd := buildTestCommand("django/django", []string{"test_foo"})
	if !strings.Contains(cmd, "runtests.py") {
		t.Fatalf("%s", cmd)
	}
}

func TestDockerGraderAppliesTestPatchFirst(t *testing.T) {
	var execScripts []string
	var copied []string
	rt := &recordingRuntime{
		onCopyTo: func(src, dst string) {
			copied = append(copied, dst)
		},
		onExec: func(cmd []string) (string, string, int, error) {
			if len(cmd) >= 2 && cmd[0] == "bash" {
				execScripts = append(execScripts, cmd[1])
			}
			return "ok", "", 0, nil
		},
	}
	g := &DockerGrader{RT: rt, WorkRoot: t.TempDir(), Pull: false, Timeout: time.Minute}
	in := Instance{
		InstanceID: "fixture__repo-1",
		Repo:       "fixture/repo",
		TestPatch:  "diff --git a/test_add.py b/test_add.py\n+def test_add():\n+  pass\n",
		FailToPass: []string{"test_add.py::test_add"},
	}
	gr, err := g.Grade(context.Background(), in, "diff --git a/x b/x\n+fix\n", "")
	if err != nil {
		t.Fatal(err)
	}
	if !gr.Resolved {
		t.Fatalf("expected resolved: %+v", gr)
	}
	// test.patch must be copied before model patch.diff
	ti, pi := -1, -1
	for i, c := range copied {
		if c == "/tmp/test.patch" {
			ti = i
		}
		if c == "/tmp/patch.diff" {
			pi = i
		}
	}
	if ti < 0 || pi < 0 || ti > pi {
		t.Fatalf("copy order test=%d model=%d all=%v", ti, pi, copied)
	}
	// apply scripts: test_patch then model_patch
	if len(execScripts) < 2 {
		t.Fatalf("execs %v", execScripts)
	}
	if !strings.Contains(execScripts[0], "test_patch") || !strings.Contains(execScripts[1], "model_patch") {
		t.Fatalf("apply order %v", execScripts)
	}
}

type recordingRuntime struct {
	onCopyTo func(src, dst string)
	onExec   func(cmd []string) (string, string, int, error)
}

func (r *recordingRuntime) Available(context.Context) error { return nil }
func (r *recordingRuntime) Pull(context.Context, string) error {
	return nil
}
func (r *recordingRuntime) Create(context.Context, string, CreateOpts) (string, error) {
	return "cid", nil
}
func (r *recordingRuntime) Start(context.Context, string) error { return nil }
func (r *recordingRuntime) CopyFrom(context.Context, string, string, string) error {
	return nil
}
func (r *recordingRuntime) CopyTo(_ context.Context, _ string, src, dst string) error {
	if r.onCopyTo != nil {
		r.onCopyTo(src, dst)
	}
	return nil
}
func (r *recordingRuntime) Exec(_ context.Context, _ string, cmd []string, _ ExecOpts) (string, string, int, error) {
	if r.onExec != nil {
		return r.onExec(cmd)
	}
	return "", "", 0, nil
}
func (r *recordingRuntime) Remove(context.Context, string) error { return nil }

func loadFixture(t *testing.T) []Instance {
	t.Helper()
	all, err := LoadInstancesJSONL(filepath.Join("testdata", "instances_fixture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return all
}

// --- fakes ---

type fakeRuntime struct{}

func (fakeRuntime) Available(context.Context) error { return nil }
func (fakeRuntime) Pull(context.Context, string) error {
	return nil
}
func (fakeRuntime) Create(context.Context, string, CreateOpts) (string, error) {
	return "c", nil
}
func (fakeRuntime) Start(context.Context, string) error { return nil }
func (fakeRuntime) CopyFrom(context.Context, string, string, string) error {
	return nil
}
func (fakeRuntime) CopyTo(context.Context, string, string, string) error { return nil }
func (fakeRuntime) Exec(context.Context, string, []string, ExecOpts) (string, string, int, error) {
	return "", "", 0, nil
}
func (fakeRuntime) Remove(context.Context, string) error { return nil }

type fakeAgent struct {
	res   ExecResult
	err   error
	calls int
}

func (f *fakeAgent) Run(context.Context, string, string, AgentOpts) (ExecResult, error) {
	f.calls++
	return f.res, f.err
}

type fakeGrader struct {
	res GradeResult
	err error
}

func (f *fakeGrader) Grade(context.Context, Instance, string, string) (GradeResult, error) {
	return f.res, f.err
}

func combinedOutput(dir, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}
