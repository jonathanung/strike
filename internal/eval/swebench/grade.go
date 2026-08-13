package swebench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GraderName selects how patches are tested.
type GraderName string

const (
	// GraderDocker applies the patch in the instance image and runs tests
	// (FAIL_TO_PASS must pass; PASS_TO_PASS must not regress).
	GraderDocker GraderName = "docker"
	// GraderHarness shells out to python -m swebench.harness.run_evaluation.
	GraderHarness GraderName = "harness"
	// GraderNone skips test execution (predictions + agent metrics only).
	GraderNone GraderName = "none"
)

// GradeResult is the outcome of test execution for one instance.
type GradeResult struct {
	Resolved     bool
	Detail       string
	FailToPassOK int
	FailToPassN  int
	PassToPassOK int
	PassToPassN  int
	Duration     time.Duration
	Skipped      bool
}

// Grader grades a model patch for one instance.
type Grader interface {
	Grade(ctx context.Context, in Instance, patch string, workDir string) (GradeResult, error)
}

// DockerGrader applies patch inside a fresh container from the instance image.
type DockerGrader struct {
	RT       Runtime
	Timeout  time.Duration // default 15m
	Pull     bool          // pull image before create
	WorkRoot string        // host dir for temp patch files
}

// Grade implements Grader.
func (g *DockerGrader) Grade(ctx context.Context, in Instance, patch string, _ string) (GradeResult, error) {
	start := time.Now()
	if strings.TrimSpace(patch) == "" {
		return GradeResult{
			Resolved: false,
			Detail:   "empty patch",
			Duration: time.Since(start),
		}, nil
	}
	if g.RT == nil {
		return GradeResult{}, fmt.Errorf("swebench: docker grader: nil runtime")
	}
	timeout := g.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	image := DockerImageName(in.InstanceID)
	if g.Pull {
		if err := g.RT.Pull(ctx, image); err != nil {
			return GradeResult{}, err
		}
	}

	// Long-lived container for apply + test.
	id, err := g.RT.Create(ctx, image, CreateOpts{
		WorkDir:    "/testbed",
		Entrypoint: []string{"sleep", "infinity"},
	})
	if err != nil {
		return GradeResult{}, err
	}
	defer func() { _ = g.RT.Remove(context.Background(), id) }()
	if err := g.RT.Start(ctx, id); err != nil {
		return GradeResult{}, err
	}

	root := g.WorkRoot
	if root == "" {
		root = os.TempDir()
	}
	tmpDir, err := os.MkdirTemp(root, "swe-grade-*")
	if err != nil {
		return GradeResult{}, err
	}
	defer os.RemoveAll(tmpDir)

	// Official harness order: apply the model patch, then run eval.sh which
	// restores tests, applies test_patch, and invokes the repo test runner.
	// Without eval_script we approximate: apply test_patch then model patch,
	// then a reconstructed command.
	useEval := strings.TrimSpace(in.EvalScript) != ""
	if !useEval {
		if tp := strings.TrimSpace(in.TestPatch); tp != "" {
			tpPath := filepath.Join(tmpDir, "test.patch")
			if err := os.WriteFile(tpPath, []byte(NormalizePatch(tp)), 0o600); err != nil {
				return GradeResult{}, err
			}
			if err := g.RT.CopyTo(ctx, id, tpPath, "/tmp/test.patch"); err != nil {
				return GradeResult{}, err
			}
			if err := g.applyInContainer(ctx, id, tmpDir, "/tmp/test.patch", "test_patch"); err != nil {
				return GradeResult{
					Resolved: false,
					Detail:   err.Error(),
					Duration: time.Since(start),
				}, nil
			}
		}
	}

	patchPath := filepath.Join(tmpDir, "patch.diff")
	if err := os.WriteFile(patchPath, []byte(NormalizePatch(patch)), 0o600); err != nil {
		return GradeResult{}, err
	}
	if err := g.RT.CopyTo(ctx, id, patchPath, "/tmp/patch.diff"); err != nil {
		return GradeResult{}, err
	}
	if err := g.applyInContainer(ctx, id, tmpDir, "/tmp/patch.diff", "model_patch"); err != nil {
		return GradeResult{
			Resolved: false,
			Detail:   err.Error(),
			Duration: time.Since(start),
		}, nil
	}

	f2p := in.FailToPass
	p2p := in.PassToPass
	var testScript string
	if useEval {
		testScript = strings.TrimSpace(in.EvalScript) + "\n"
	} else {
		testCmd := buildTestCommand(in.Repo, append(append([]string{}, f2p...), p2p...))
		testScript = `set -e
cd /testbed
# Activate common SWE-bench conda env when present.
if [ -f /opt/miniconda3/etc/profile.d/conda.sh ]; then
  . /opt/miniconda3/etc/profile.d/conda.sh
  conda activate testbed 2>/dev/null || true
elif [ -f /root/miniconda3/etc/profile.d/conda.sh ]; then
  . /root/miniconda3/etc/profile.d/conda.sh
  conda activate testbed 2>/dev/null || true
fi
` + testCmd + "\n"
	}

	testPath := filepath.Join(tmpDir, "test.sh")
	if err := os.WriteFile(testPath, []byte(testScript), 0o700); err != nil {
		return GradeResult{}, err
	}
	if err := g.RT.CopyTo(ctx, id, testPath, "/tmp/test.sh"); err != nil {
		return GradeResult{}, err
	}

	stdout, stderr, code, err := g.RT.Exec(ctx, id, []string{"bash", "/tmp/test.sh"}, ExecOpts{Timeout: timeout})
	if err != nil {
		return GradeResult{}, err
	}
	detail := strings.TrimSpace(stdout + "\n" + stderr)
	resolved := code == 0
	// Best-effort counts: without log parsers we treat whole-suite exit as all-or-nothing.
	gr := GradeResult{
		Resolved:    resolved,
		Detail:      truncate(detail, 800),
		FailToPassN: len(f2p),
		PassToPassN: len(p2p),
		Duration:    time.Since(start),
	}
	if resolved {
		gr.FailToPassOK = len(f2p)
		gr.PassToPassOK = len(p2p)
	}
	if !resolved && gr.Detail == "" {
		gr.Detail = fmt.Sprintf("tests exit %d", code)
	}
	return gr, nil
}

// applyInContainer applies a unified diff inside the container using the same
// fallback chain as the official SWE-bench harness.
func (g *DockerGrader) applyInContainer(ctx context.Context, id, tmpDir, containerPatch, label string) error {
	applyScript := `set -e
cd /testbed
git config --global --add safe.directory /testbed 2>/dev/null || true
PATCH_FILE=` + shellQuote(containerPatch) + `
if git apply --verbose "$PATCH_FILE"; then exit 0; fi
if git apply --verbose --reject "$PATCH_FILE"; then exit 0; fi
if patch --batch --fuzz=5 -p1 -i "$PATCH_FILE"; then exit 0; fi
echo "APPLY_PATCH_FAIL" >&2
exit 1
`
	applyPath := filepath.Join(tmpDir, "apply-"+label+".sh")
	if err := os.WriteFile(applyPath, []byte(applyScript), 0o700); err != nil {
		return err
	}
	dest := "/tmp/apply-" + label + ".sh"
	if err := g.RT.CopyTo(ctx, id, applyPath, dest); err != nil {
		return err
	}
	_, applyErrOut, applyCode, err := g.RT.Exec(ctx, id, []string{"bash", dest}, ExecOpts{Timeout: 2 * time.Minute})
	if err != nil {
		return err
	}
	if applyCode != 0 {
		return fmt.Errorf("%s apply failed: %s", label, truncate(applyErrOut, 400))
	}
	return nil
}

// buildTestCommand picks a repo-appropriate command for the given node ids/paths.
func buildTestCommand(repo string, tests []string) string {
	if len(tests) == 0 {
		return "echo 'no tests listed'; exit 1"
	}
	repo = strings.ToLower(repo)
	if strings.Contains(repo, "django") {
		tests = djangoRuntestsArgs(tests)
		if len(tests) == 0 {
			return "echo 'no tests listed'; exit 1"
		}
	}
	quoted := make([]string, len(tests))
	for i, t := range tests {
		quoted[i] = shellQuote(t)
	}
	joined := strings.Join(quoted, " ")
	switch {
	case strings.Contains(repo, "django"):
		return fmt.Sprintf(`if [ -f tests/runtests.py ]; then
  ./tests/runtests.py --verbosity 2 --settings=test_sqlite --parallel 1 %s
elif command -v pytest >/dev/null 2>&1; then
  pytest -xvs %s
else
  python -m pytest -xvs %s
fi`, joined, joined, joined)
	default:
		return fmt.Sprintf(`if command -v pytest >/dev/null 2>&1; then
  pytest -xvs %s
else
  python -m pytest -xvs %s
fi`, joined, joined)
	}
}

// djangoRuntestsArgs converts SWE-bench FAIL_TO_PASS names to runtests.py
// selectors. Dataset rows look like `test_foo (module.Class)` or a docstring;
// runtests.py wants `module.Class.test_foo`.
func djangoRuntestsArgs(tests []string) []string {
	out := make([]string, 0, len(tests))
	seen := make(map[string]struct{}, len(tests))
	for _, t := range tests {
		sel := djangoTestSelector(t)
		if sel == "" {
			continue
		}
		if _, ok := seen[sel]; ok {
			continue
		}
		seen[sel] = struct{}{}
		out = append(out, sel)
	}
	return out
}

func djangoTestSelector(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// "test_accent (dbshell.test_postgresql.PostgreSqlDbshellCommandTestCase)"
	open := strings.Index(s, " (")
	if open > 0 && strings.HasSuffix(s, ")") {
		name := s[:open]
		inner := strings.TrimSuffix(s[open+2:], ")")
		if inner != "" && !strings.ContainsAny(name, " \t") && (strings.HasPrefix(name, "test") || strings.HasPrefix(name, "app_")) {
			return inner + "." + name
		}
		return "" // docstring used as a test id; skip
	}
	// Already a dotted selector / path.
	if strings.Contains(s, " ") {
		return "" // prose / docstring
	}
	return s
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// NoneGrader skips grading.
type NoneGrader struct{}

// Grade implements Grader.
func (NoneGrader) Grade(context.Context, Instance, string, string) (GradeResult, error) {
	return GradeResult{Skipped: true, Detail: "grader=none"}, nil
}

// HarnessGrader shells out to the official SWE-bench evaluation harness.
type HarnessGrader struct {
	// Python is the python binary (default "python3").
	Python string
	// DatasetName passed to the harness (default DatasetName).
	DatasetName string
	// RunID for the harness log directory.
	RunID string
	// WorkDir is the host directory for predictions + harness cwd.
	WorkDir string
	// Timeout per instance (harness max_workers=1).
	Timeout time.Duration
	// Run injects command execution for tests.
	Run func(ctx context.Context, name string, args []string, dir string) (stdout, stderr string, code int, err error)
}

// Grade implements Grader. It writes a one-instance predictions file and runs
// the harness; then reads the produced report.json when present.
func (h *HarnessGrader) Grade(ctx context.Context, in Instance, patch string, _ string) (GradeResult, error) {
	start := time.Now()
	if strings.TrimSpace(patch) == "" {
		return GradeResult{Resolved: false, Detail: "empty patch", Duration: time.Since(start)}, nil
	}
	py := h.Python
	if py == "" {
		py = "python3"
	}
	ds := h.DatasetName
	if ds == "" {
		ds = DatasetName
	}
	runID := h.RunID
	if runID == "" {
		runID = "strike-" + in.InstanceID
	}
	dir := h.WorkDir
	if dir == "" {
		var err error
		dir, err = os.MkdirTemp("", "swe-harness-*")
		if err != nil {
			return GradeResult{}, err
		}
		defer os.RemoveAll(dir)
	}
	predPath := filepath.Join(dir, "preds.json")
	preds := map[string]Prediction{
		in.InstanceID: {
			InstanceID:      in.InstanceID,
			ModelNameOrPath: "strike",
			ModelPatch:      NormalizePatch(patch),
		},
	}
	raw, err := json.MarshalIndent(preds, "", "  ")
	if err != nil {
		return GradeResult{}, err
	}
	if err := os.WriteFile(predPath, raw, 0o600); err != nil {
		return GradeResult{}, err
	}

	args := []string{
		"-m", "swebench.harness.run_evaluation",
		"--dataset_name", ds,
		"--predictions_path", predPath,
		"--instance_ids", in.InstanceID,
		"--max_workers", "1",
		"--run_id", runID,
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var stdout, stderr string
	var code int
	if h.Run != nil {
		stdout, stderr, code, err = h.Run(runCtx, py, args, dir)
	} else {
		stdout, stderr, code, err = runCmd(runCtx, py, args, dir)
	}
	if err != nil {
		return GradeResult{}, fmt.Errorf("swebench harness: %w", err)
	}
	// Try to read report from standard harness layout.
	resolved, detail, found := readHarnessReport(dir, runID, in.InstanceID)
	if !found {
		detail = truncate(strings.TrimSpace(stdout+"\n"+stderr), 600)
		if code != 0 {
			return GradeResult{
				Resolved: false,
				Detail:   fmt.Sprintf("harness exit %d: %s", code, detail),
				Duration: time.Since(start),
			}, nil
		}
		return GradeResult{
			Resolved: false,
			Detail:   "harness finished but report.json not found: " + detail,
			Duration: time.Since(start),
		}, nil
	}
	return GradeResult{
		Resolved: resolved,
		Detail:   detail,
		Duration: time.Since(start),
	}, nil
}

func runCmd(ctx context.Context, name string, args []string, dir string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			return stdout.String(), stderr.String(), -1, err
		}
	}
	return stdout.String(), stderr.String(), code, nil
}

func readHarnessReport(workDir, runID, instanceID string) (resolved bool, detail string, ok bool) {
	// Harness writes under logs/run_evaluation/<run_id>/<model>/<instance>/report.json
	// Model folder varies; walk a shallow tree.
	root := filepath.Join(workDir, "logs", "run_evaluation", runID)
	var reportPath string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if d.Name() == "report.json" && strings.Contains(path, instanceID) {
			reportPath = path
			return filepath.SkipAll
		}
		return nil
	})
	if reportPath == "" {
		// Also check evaluation_results.
		alt := filepath.Join(workDir, "evaluation_results")
		_ = filepath.WalkDir(alt, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if d.Name() == "report.json" && strings.Contains(path, instanceID) {
				reportPath = path
				return filepath.SkipAll
			}
			return nil
		})
	}
	if reportPath == "" {
		return false, "", false
	}
	data, err := os.ReadFile(reportPath)
	if err != nil {
		return false, err.Error(), true
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		return false, string(data), true
	}
	// Shape: { "<instance_id>": { "resolved": true, ... } } or flat.
	if m, ok := generic[instanceID].(map[string]any); ok {
		if r, ok := m["resolved"].(bool); ok {
			return r, truncate(string(data), 400), true
		}
	}
	if r, ok := generic["resolved"].(bool); ok {
		return r, truncate(string(data), 400), true
	}
	return false, truncate(string(data), 400), true
}

// NewGrader returns a Grader for name. Empty name defaults to docker.
func NewGrader(name GraderName, rt Runtime, workRoot, runID string) (Grader, error) {
	if name == "" {
		name = GraderDocker
	}
	switch name {
	case GraderNone:
		return NoneGrader{}, nil
	case GraderDocker:
		return &DockerGrader{RT: rt, WorkRoot: workRoot, Pull: true}, nil
	case GraderHarness:
		return &HarnessGrader{WorkDir: workRoot, RunID: runID}, nil
	default:
		return nil, fmt.Errorf("swebench: unknown grader %q (want docker|harness|none)", name)
	}
}
