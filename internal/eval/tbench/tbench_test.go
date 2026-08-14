package tbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/swebench"
)

func TestDefaultSubsetIDs(t *testing.T) {
	ids := DefaultSubsetIDs()
	if len(ids) != DefaultSubsetSize {
		t.Fatalf("subset size = %d, want %d", len(ids), DefaultSubsetSize)
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Fatalf("subset not strictly sorted unique at %d: %q %q", i, ids[i-1], ids[i])
		}
	}
	want := map[string]bool{
		"crack-7z-hash":         true,
		"nginx-request-logging": true,
		"write-compressor":      true,
	}
	for _, id := range ids {
		delete(want, id)
	}
	if len(want) > 0 {
		t.Fatalf("missing expected ids: %v", want)
	}
}

func TestParseTaskTOML(t *testing.T) {
	raw := `
[task]
name = "terminal-bench/openssl-selfsigned-cert"

[metadata]
difficulty = "medium"
category = "security"

[verifier]
timeout_sec = 900.0

[agent]
timeout_sec = 900.0

[environment]
docker_image = "alexgshaw/openssl-selfsigned-cert:20251031"
`
	m := parseTaskTOML(raw)
	if m.DockerImage != "alexgshaw/openssl-selfsigned-cert:20251031" {
		t.Fatalf("image %q", m.DockerImage)
	}
	if m.Difficulty != "medium" || m.Category != "security" {
		t.Fatalf("%+v", m)
	}
	if m.AgentTimeout != 900 || m.VerifyTimeout != 900 {
		t.Fatalf("timeouts %+v", m)
	}
}

func TestLoadTaskDirAndPack(t *testing.T) {
	dir := filepath.Join("testdata", "fixture-task")
	in, err := LoadTaskDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if in.InstanceID != "fixture-task" {
		t.Fatalf("id %q", in.InstanceID)
	}
	if !strings.Contains(in.Instruction, "hello.txt") {
		t.Fatalf("instruction: %s", in.Instruction)
	}
	if in.DockerImage != "fixture/tbench-task:test" {
		t.Fatalf("image %q", in.DockerImage)
	}
	if in.Category != "file-operations" || in.Difficulty != "easy" {
		t.Fatalf("%+v", in)
	}
	if in.TaskDir == "" {
		t.Fatal("task dir empty")
	}

	// Pack root = testdata (contains fixture-task)
	root := "testdata"
	all, err := LoadTaskPack(root, []string{"fixture-task"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].InstanceID != "fixture-task" {
		t.Fatalf("%+v", all)
	}
}

func TestParseInstancesJSONL(t *testing.T) {
	all, err := LoadInstancesJSONL(filepath.Join("testdata", "instances_fixture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("got %d", len(all))
	}
}

func TestBuildAgentPrompt(t *testing.T) {
	p := BuildAgentPrompt(Instance{
		InstanceID:  "fixture-task",
		Instruction: "Do the thing",
		Category:    "file-operations",
	})
	if !strings.Contains(p, "fixture-task") || !strings.Contains(p, "Do the thing") {
		t.Fatalf("%s", p)
	}
	if strings.Contains(p, "eval-exec") {
		t.Fatalf("no-container prompt should omit eval-exec: %s", p)
	}
}

func TestFormatAgentPromptEvalContainer(t *testing.T) {
	p := FormatAgentPrompt(Instance{InstanceID: "x", Instruction: "y"}, "abc123")
	if !strings.Contains(p, "eval-exec") || !strings.Contains(p, "abc123") {
		t.Fatalf("container prompt: %s", p)
	}
	if !strings.Contains(p, "/app") {
		t.Fatalf("missing /app: %s", p)
	}
}

func TestWriteEvalExecHelper(t *testing.T) {
	dir := t.TempDir()
	if err := WriteEvalExecHelper(dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "eval-exec"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "STRIKE_EVAL_CONTAINER") || !strings.Contains(string(data), "docker exec") {
		t.Fatalf("helper: %s", data)
	}
}

func TestBuildReportAndWrite(t *testing.T) {
	resolved := true
	unresolved := false
	results := []InstanceResult{
		{InstanceID: "b", Status: StatusResolved, Resolved: &resolved, TokensIn: 100, TokensOut: 50, CostUSD: 0.01, WallClockMs: 1000, Reward: 1},
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
	if rep.Benchmark != BenchmarkName || rep.SchemaVersion != ReportSchemaVersion {
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

func TestRunnerDryRunAndMock(t *testing.T) {
	fixtures, err := LoadInstancesJSONL(filepath.Join("testdata", "instances_fixture.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// Attach task dir for grading path.
	fixtures[0].TaskDir = filepath.Join("testdata", "fixture-task")

	out := t.TempDir()
	work := t.TempDir()

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

	agent := &fakeAgent{res: swebench.ExecResult{
		OK: true, Provider: "echo", Model: "echo", SessionID: "sess",
		Usage: &swebench.Usage{Input: 20, Output: 10},
	}}
	grader := &fakeGrader{res: GradeResult{Resolved: true, Reward: 1, Detail: "reward.txt=1"}}
	r2 := &Runner{
		RT:    fakeRuntime{},
		Agent: agent,
		Grade: grader,
		Cost:  swebench.FixedCost{InputPerM: 1, OutputPerM: 1},
		Now: func() time.Time {
			return time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
		},
		Materialize: func(ctx context.Context, image, hostDir string, pull bool) (MaterializeResult, error) {
			app := filepath.Join(hostDir, "app")
			if err := os.MkdirAll(app, 0o755); err != nil {
				return MaterializeResult{}, err
			}
			return MaterializeResult{WorkDir: app, Image: image, ContainerID: "cid-live"}, nil
		},
	}
	rep2, err := r2.Run(context.Background(), Config{
		Instances:     fixtures[:1],
		RunID:         "mock1",
		OutDir:        filepath.Join(out, "mock"),
		WorkRoot:      work,
		Provider:      "echo",
		Model:         "echo",
		Grader:        GraderNone,
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
	if rep2.Results[0].Reward != 1 {
		t.Fatalf("reward: %+v", rep2.Results[0])
	}
	if agent.calls != 1 {
		t.Fatalf("agent calls %d", agent.calls)
	}
	if !containsArg(agent.opts.ExtraArgs, "--sandbox=off") {
		t.Fatalf("expected --sandbox=off, got %v", agent.opts.ExtraArgs)
	}
	if !containsArg(agent.opts.Env, "STRIKE_EVAL_CONTAINER=cid-live") {
		t.Fatalf("env missing container: %v", agent.opts.Env)
	}
	if !containsArg(agent.opts.Env, "STRIKE_EVAL_WORKDIR=/app") {
		t.Fatalf("env missing workdir: %v", agent.opts.Env)
	}
	if !strings.Contains(agent.prompt, "eval-exec") {
		t.Fatalf("prompt missing eval-exec: %s", agent.prompt)
	}
	if _, err := os.Stat(filepath.Join(work, "mock1", "fixture-task", "eval-exec")); err != nil {
		t.Fatalf("eval-exec helper: %v", err)
	}
	// report written
	if _, err := os.Stat(filepath.Join(out, "mock", "report.json")); err != nil {
		t.Fatal(err)
	}
}

func TestMaterializeWorkspaceStartsLiveContainer(t *testing.T) {
	rt := &bindRecordingRuntime{}
	host := t.TempDir()
	mat, err := MaterializeWorkspace(context.Background(), rt, "img:tag", host, false)
	if err != nil {
		t.Fatal(err)
	}
	if mat.WorkDir == "" || mat.ContainerID == "" {
		t.Fatalf("result %+v", mat)
	}
	if len(rt.binds) == 0 {
		t.Fatal("expected live-container HostBinds")
	}
	found := false
	for _, b := range rt.binds {
		if strings.HasSuffix(b, ":/app") {
			found = true
		}
	}
	if !found {
		t.Fatalf("binds=%v", rt.binds)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

func TestDockerGraderReadsReward(t *testing.T) {
	var execCmds []string
	var copyDsts []string
	rt := &recordingRuntime{
		onCopyTo: func(src, dst string) {
			copyDsts = append(copyDsts, dst)
		},
		onExec: func(cmd []string) (string, string, int, error) {
			joined := strings.Join(cmd, " ")
			execCmds = append(execCmds, joined)
			if strings.Contains(joined, "cat /logs/verifier/reward.json") {
				return "", "missing", 1, nil
			}
			if strings.Contains(joined, "cat /logs/verifier/reward.txt") {
				return "1\n", "", 0, nil
			}
			// setup rm / test.sh run
			return "ok", "", 0, nil
		},
	}
	g := &DockerGrader{RT: rt, WorkRoot: t.TempDir(), Pull: false, Timeout: time.Minute}
	// Need a real tests dir on disk for CopyTo source.
	taskDir := filepath.Join("testdata", "fixture-task")
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "hello.txt"), []byte("hello terminal-bench\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gr, err := g.Grade(context.Background(), Instance{
		InstanceID:  "fixture-task",
		DockerImage: "fixture/tbench-task:test",
		TaskDir:     taskDir,
	}, work)
	if err != nil {
		t.Fatal(err)
	}
	if !gr.Resolved || gr.Reward != 1 {
		t.Fatalf("%+v cmds=%v", gr, execCmds)
	}
	// Destinations must be exact /app and /tests (not nested /tests/tests).
	wantDst := map[string]bool{"/app": false, "/tests": false}
	for _, d := range copyDsts {
		if _, ok := wantDst[d]; ok {
			wantDst[d] = true
		}
	}
	for d, ok := range wantDst {
		if !ok {
			t.Fatalf("missing copy dest %s in %v", d, copyDsts)
		}
	}
}

func TestDockerGraderLiveSkipsAppReplace(t *testing.T) {
	var execCmds []string
	var copyDsts []string
	rt := &recordingRuntime{
		onCopyTo: func(src, dst string) {
			copyDsts = append(copyDsts, dst)
		},
		onExec: func(cmd []string) (string, string, int, error) {
			joined := strings.Join(cmd, " ")
			execCmds = append(execCmds, joined)
			if strings.Contains(joined, "cat /logs/verifier/reward.json") {
				return "", "missing", 1, nil
			}
			if strings.Contains(joined, "cat /logs/verifier/reward.txt") {
				return "1\n", "", 0, nil
			}
			return "ok", "", 0, nil
		},
	}
	g := &DockerGrader{RT: rt, WorkRoot: t.TempDir(), Pull: false, Timeout: time.Minute, LiveContainer: "live-cid"}
	gr, err := g.Grade(context.Background(), Instance{
		InstanceID:  "fixture-task",
		DockerImage: "fixture/tbench-task:test",
		TaskDir:     filepath.Join("testdata", "fixture-task"),
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !gr.Resolved {
		t.Fatalf("%+v cmds=%v", gr, execCmds)
	}
	for _, d := range copyDsts {
		if d == "/app" {
			t.Fatalf("live grade must not replace /app: %v", copyDsts)
		}
	}
	foundTests := false
	for _, d := range copyDsts {
		if d == "/tests" {
			foundTests = true
		}
	}
	if !foundTests {
		t.Fatalf("missing /tests copy: %v", copyDsts)
	}
	for _, c := range execCmds {
		if strings.Contains(c, "rm -rf /app") {
			t.Fatalf("live grade must not delete /app: %v", execCmds)
		}
	}
}

func TestDockerGraderMissingRewardUnresolved(t *testing.T) {
	rt := &recordingRuntime{
		onExec: func(cmd []string) (string, string, int, error) {
			joined := strings.Join(cmd, " ")
			if strings.Contains(joined, "cat /logs/verifier/") {
				return "", "missing", 1, nil
			}
			return "ok", "", 0, nil // test.sh exit 0 but no reward
		},
	}
	g := &DockerGrader{RT: rt, WorkRoot: t.TempDir(), Pull: false, Timeout: time.Minute}
	gr, err := g.Grade(context.Background(), Instance{
		InstanceID:  "fixture-task",
		DockerImage: "fixture/tbench-task:test",
		TaskDir:     filepath.Join("testdata", "fixture-task"),
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if gr.Resolved || gr.Reward != 0 {
		t.Fatalf("expected unresolved without reward: %+v", gr)
	}
	if !strings.Contains(gr.Detail, "reward") {
		t.Fatalf("detail: %s", gr.Detail)
	}
}

func TestDefaultImage(t *testing.T) {
	got := DefaultImage("openssl-selfsigned-cert")
	want := "alexgshaw/openssl-selfsigned-cert:20251031"
	if got != want {
		t.Fatalf("%q != %q", got, want)
	}
}

func TestResolveInstancesRequiresTasksDir(t *testing.T) {
	_, err := ResolveInstances("", "", nil)
	if err == nil || !strings.Contains(err.Error(), "tasks-dir") {
		t.Fatalf("err=%v", err)
	}
}

func TestAsFloat(t *testing.T) {
	if f, ok := asFloat(1.5); !ok || f != 1.5 {
		t.Fatalf("%v %v", f, ok)
	}
	if f, ok := asFloat(true); !ok || f != 1 {
		t.Fatalf("%v %v", f, ok)
	}
	if f, ok := asFloat(json.Number("2")); !ok || f != 2 {
		t.Fatalf("%v %v", f, ok)
	}
}

// --- fakes ---

type fakeRuntime struct{}

func (fakeRuntime) Available(context.Context) error { return nil }
func (fakeRuntime) Pull(context.Context, string) error {
	return nil
}
func (fakeRuntime) Create(context.Context, string, swebench.CreateOpts) (string, error) {
	return "c", nil
}
func (fakeRuntime) Start(context.Context, string) error { return nil }
func (fakeRuntime) CopyFrom(context.Context, string, string, string) error {
	return nil
}
func (fakeRuntime) CopyTo(context.Context, string, string, string) error { return nil }
func (fakeRuntime) Exec(context.Context, string, []string, swebench.ExecOpts) (string, string, int, error) {
	return "", "", 0, nil
}
func (fakeRuntime) Remove(context.Context, string) error { return nil }

func TestReclaimWorkspaceOwner(t *testing.T) {
	var got []string
	rt := &recordingRuntime{
		onExec: func(cmd []string) (string, string, int, error) {
			got = append(got, strings.Join(cmd, " "))
			return "", "", 0, nil
		},
	}
	reclaimWorkspaceOwner(context.Background(), rt, "cid-live")
	if len(got) != 1 || !strings.Contains(got[0], "chown -R") || !strings.Contains(got[0], "/app") {
		t.Fatalf("exec=%v", got)
	}
	reclaimWorkspaceOwner(context.Background(), rt, "")
	if len(got) != 1 {
		t.Fatal("empty cid should be a no-op")
	}
}

type recordingRuntime struct {
	onExec   func(cmd []string) (string, string, int, error)
	onCopyTo func(src, dst string)
}

func (r *recordingRuntime) Available(context.Context) error { return nil }
func (r *recordingRuntime) Pull(context.Context, string) error {
	return nil
}
func (r *recordingRuntime) Create(context.Context, string, swebench.CreateOpts) (string, error) {
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
func (r *recordingRuntime) Exec(_ context.Context, _ string, cmd []string, _ swebench.ExecOpts) (string, string, int, error) {
	if r.onExec != nil {
		return r.onExec(cmd)
	}
	return "", "", 0, nil
}
func (r *recordingRuntime) Remove(context.Context, string) error { return nil }

type fakeAgent struct {
	res    swebench.ExecResult
	err    error
	calls  int
	opts   swebench.AgentOpts
	prompt string
}

func (f *fakeAgent) Run(_ context.Context, _ string, prompt string, opts swebench.AgentOpts) (swebench.ExecResult, error) {
	f.calls++
	f.opts = opts
	f.prompt = prompt
	return f.res, f.err
}

type bindRecordingRuntime struct {
	n     int
	binds []string
}

func (r *bindRecordingRuntime) Available(context.Context) error { return nil }
func (r *bindRecordingRuntime) Pull(context.Context, string) error {
	return nil
}
func (r *bindRecordingRuntime) Create(_ context.Context, _ string, opts swebench.CreateOpts) (string, error) {
	r.n++
	if len(opts.HostBinds) > 0 {
		r.binds = append(r.binds, opts.HostBinds...)
	}
	return fmt.Sprintf("c%d", r.n), nil
}
func (r *bindRecordingRuntime) Start(context.Context, string) error { return nil }
func (r *bindRecordingRuntime) CopyFrom(_ context.Context, _ string, _ string, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, "marker.txt"), []byte("ok\n"), 0o644)
}
func (r *bindRecordingRuntime) CopyTo(context.Context, string, string, string) error {
	return nil
}
func (r *bindRecordingRuntime) Exec(context.Context, string, []string, swebench.ExecOpts) (string, string, int, error) {
	return "", "", 0, nil
}
func (r *bindRecordingRuntime) Remove(context.Context, string) error { return nil }

type fakeGrader struct {
	res GradeResult
	err error
}

func (f *fakeGrader) Grade(context.Context, Instance, string) (GradeResult, error) {
	return f.res, f.err
}
