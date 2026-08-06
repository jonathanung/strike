// Package verify provides independent completion gates: machine-checkable
// conditions that the implementer model cannot self-certify past.
//
// Shared by delegation completion (#780) and solo/harness claim-vs-verified
// paths (#806). The runner never treats model-authored prose (including a
// handoff "verification" string) as evidence that a gate passed.
package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Gate kinds. Prefer cmd and schema for trust boundaries.
const (
	KindCmd    = "cmd"    // shell command; pass iff exit 0
	KindSchema = "schema" // structured payload validity (e.g. handoff)
	KindPath   = "path"   // path exists under WorkDir (relative or absolute)
)

// MaxGates caps completion conditions on one delegation/run.
const MaxGates = 16

// Gate is one machine-checkable completion condition.
type Gate struct {
	// Kind is cmd|schema|path.
	Kind string `json:"kind"`
	// Value is the command, schema name ("handoff"), or filesystem path.
	Value string `json:"value"`
	// Description is an optional human label for reports.
	Description string `json:"description,omitempty"`
}

// HandoffView is the structured completion payload used by schema gates.
// Distinct from any model self-report string about verification.
type HandoffView struct {
	Summary    string
	Incomplete bool
	// HasStructured is true when a model-supplied handoff object was parsed
	// (even if some fields are empty). When false and Incomplete is true,
	// the engine filled defaults only.
	HasStructured bool
}

// EnvMetadata is retained for audit/replay of a verification run.
type EnvMetadata struct {
	WorkDir    string `json:"work_dir,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	WorktreeID string `json:"worktree_id,omitempty"`
	ModelID    string `json:"model_id,omitempty"`
	StartedAt  string `json:"started_at,omitempty"`  // RFC3339
	FinishedAt string `json:"finished_at,omitempty"` // RFC3339
}

// CheckResult is one gate outcome inside a Result.
type CheckResult struct {
	Name       string `json:"name,omitempty"`
	Kind       string `json:"kind"`
	Value      string `json:"value,omitempty"`
	Passed     bool   `json:"passed"`
	ExitCode   int    `json:"exit_code,omitempty"`
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMs int64  `json:"duration_ms,omitempty"`
}

// Result is the harness-owned verification report.
//
// Claimed means the implementer reached a terminal "done" claim.
// Verified means every configured gate passed (independent of Claimed).
// Passed is the conjunction used by callers to promote to final done.
type Result struct {
	Passed     bool          `json:"passed"`
	Claimed    bool          `json:"claimed"`
	Verified   bool          `json:"verified"`
	Checks     []CheckResult `json:"checks"`
	Env        EnvMetadata   `json:"env"`
	Summary    string        `json:"summary,omitempty"`
	DurationMs int64         `json:"duration_ms,omitempty"`
}

// Input is the harness snapshot for one Run.
type Input struct {
	// Claimed is true when the implementer marked work complete.
	Claimed bool
	// Handoff is required for schema:handoff gates; ignored by cmd/path.
	Handoff *HandoffView
	// Env is copied into the result (timestamps filled by Runner).
	Env EnvMetadata
}

// Runner executes gates. CmdRunner overrides shell execution (tests).
type Runner struct {
	WorkDir string
	// Timeout bounds each cmd gate (default 120s).
	Timeout time.Duration
	// CmdRunner overrides command execution (tests).
	CmdRunner func(ctx context.Context, workDir, command string) (exitCode int, output string, err error)
	// Now overrides the clock (tests).
	Now func() time.Time
}

// ParseGate validates and normalizes one gate declaration.
func ParseGate(kind, value, description string) (Gate, error) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	value = strings.TrimSpace(value)
	description = strings.TrimSpace(description)
	if kind == "" {
		return Gate{}, fmt.Errorf("verify: gate kind is required")
	}
	switch kind {
	case KindCmd, KindSchema, KindPath:
		// ok
	default:
		return Gate{}, fmt.Errorf("verify: unknown gate kind %q (want cmd, schema, path)", kind)
	}
	if value == "" {
		return Gate{}, fmt.Errorf("verify: gate value is empty for kind %q", kind)
	}
	if kind == KindSchema {
		name := strings.ToLower(value)
		if name != "handoff" {
			return Gate{}, fmt.Errorf("verify: unknown schema %q (want handoff)", value)
		}
		value = "handoff"
	}
	return Gate{Kind: kind, Value: value, Description: description}, nil
}

// ParseGates validates a list of gates (max MaxGates). Empty input is valid.
func ParseGates(in []Gate) ([]Gate, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if len(in) > MaxGates {
		return nil, fmt.Errorf("verify: at most %d gates allowed (got %d)", MaxGates, len(in))
	}
	out := make([]Gate, 0, len(in))
	for i, g := range in {
		parsed, err := ParseGate(g.Kind, g.Value, g.Description)
		if err != nil {
			return nil, fmt.Errorf("verify: gate %d: %w", i+1, err)
		}
		out = append(out, parsed)
	}
	return out, nil
}

// Run executes every gate. With zero gates, returns Passed=Claimed and
// Verified=true (nothing to check). Model self-report is never consulted.
func (r *Runner) Run(ctx context.Context, gates []Gate, in Input) Result {
	start := r.now()
	env := in.Env
	if env.WorkDir == "" {
		env.WorkDir = r.WorkDir
	}
	if env.StartedAt == "" {
		env.StartedAt = start.UTC().Format(time.RFC3339)
	}

	res := Result{
		Claimed: in.Claimed,
		Checks:  make([]CheckResult, 0, len(gates)),
		Env:     env,
	}

	if len(gates) == 0 {
		// No independent gates: claimed completion is accepted as verified.
		res.Verified = true
		res.Passed = in.Claimed
		res.Summary = emptyGatesSummary(in.Claimed)
		res.Env.FinishedAt = r.now().UTC().Format(time.RFC3339)
		res.DurationMs = r.now().Sub(start).Milliseconds()
		return res
	}

	allPass := true
	for _, g := range gates {
		if ctx.Err() != nil {
			cr := CheckResult{
				Name:  gateName(g),
				Kind:  g.Kind,
				Value: g.Value,
				Error: ctx.Err().Error(),
			}
			res.Checks = append(res.Checks, cr)
			allPass = false
			break
		}
		cr := r.runOne(ctx, g, in)
		res.Checks = append(res.Checks, cr)
		if !cr.Passed {
			allPass = false
		}
	}

	res.Verified = allPass
	res.Passed = in.Claimed && allPass
	res.Summary = summarize(res)
	finished := r.now()
	res.Env.FinishedAt = finished.UTC().Format(time.RFC3339)
	res.DurationMs = finished.Sub(start).Milliseconds()
	return res
}

func (r *Runner) runOne(ctx context.Context, g Gate, in Input) CheckResult {
	cr := CheckResult{
		Name:  gateName(g),
		Kind:  g.Kind,
		Value: g.Value,
	}
	t0 := r.now()
	defer func() {
		cr.DurationMs = r.now().Sub(t0).Milliseconds()
	}()

	switch g.Kind {
	case KindCmd:
		code, output, err := r.runCmd(ctx, g.Value)
		cr.Output = trimOutput(output)
		cr.ExitCode = code
		if err != nil {
			cr.Error = err.Error()
			cr.Passed = false
			return cr
		}
		cr.Passed = code == 0
		if !cr.Passed {
			cr.Error = fmt.Sprintf("exit %d", code)
		}
		return cr
	case KindPath:
		ok, errMsg := r.checkPath(g.Value)
		cr.Passed = ok
		if !ok {
			cr.Error = errMsg
		}
		return cr
	case KindSchema:
		ok, errMsg := checkSchema(g.Value, in.Handoff)
		cr.Passed = ok
		if !ok {
			cr.Error = errMsg
		}
		return cr
	default:
		cr.Error = fmt.Sprintf("unknown gate kind %q", g.Kind)
		return cr
	}
}

func (r *Runner) runCmd(ctx context.Context, command string) (int, string, error) {
	if r.CmdRunner != nil {
		return r.CmdRunner(ctx, r.WorkDir, command)
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// #nosec G204 -- completion gates are operator/lead-authored conditions.
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if r.WorkDir != "" {
		cmd.Dir = r.WorkDir
	}
	out, err := cmd.CombinedOutput()
	output := string(out)
	if err == nil {
		return 0, output, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), output, nil
	}
	return -1, output, err
}

func (r *Runner) checkPath(raw string) (bool, string) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return false, "empty path"
	}
	if !filepath.IsAbs(p) {
		base := r.WorkDir
		if base == "" {
			base = "."
		}
		p = filepath.Join(base, p)
	}
	p = filepath.Clean(p)
	if _, err := os.Stat(p); err != nil {
		if os.IsNotExist(err) {
			return false, "path does not exist"
		}
		return false, err.Error()
	}
	return true, ""
}

func checkSchema(name string, h *HandoffView) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "handoff":
		if h == nil {
			return false, "handoff schema: no handoff payload"
		}
		if h.Incomplete || !h.HasStructured {
			return false, "handoff schema: structured handoff missing or incomplete"
		}
		if strings.TrimSpace(h.Summary) == "" {
			return false, "handoff schema: summary is required"
		}
		return true, ""
	default:
		return false, fmt.Sprintf("unknown schema %q", name)
	}
}

func gateName(g Gate) string {
	if d := strings.TrimSpace(g.Description); d != "" {
		return d
	}
	return g.Kind + ": " + g.Value
}

func emptyGatesSummary(claimed bool) string {
	if claimed {
		return "no gates configured; claimed completion accepted"
	}
	return "no gates configured"
}

func summarize(res Result) string {
	if res.Passed {
		return fmt.Sprintf("verified: %d/%d gates passed", len(res.Checks), len(res.Checks))
	}
	var failed []string
	for _, c := range res.Checks {
		if c.Passed {
			continue
		}
		msg := c.Name
		if c.Error != "" {
			msg = msg + ": " + c.Error
		}
		failed = append(failed, msg)
	}
	if len(failed) == 0 {
		if !res.Claimed {
			return "not claimed"
		}
		return "verification failed"
	}
	return "verification failed: " + strings.Join(failed, "; ")
}

func trimOutput(s string) string {
	s = strings.TrimSpace(s)
	const max = 4096
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

func (r *Runner) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// FailedCheckLines returns short actionable lines for blockers/notices.
func FailedCheckLines(res Result) []string {
	var out []string
	for _, c := range res.Checks {
		if c.Passed {
			continue
		}
		line := c.Name
		if c.Error != "" {
			line = line + ": " + c.Error
		}
		if c.Output != "" {
			// Keep one short line of output for the implementer.
			first := c.Output
			if i := strings.IndexByte(first, '\n'); i >= 0 {
				first = first[:i]
			}
			first = strings.TrimSpace(first)
			if len(first) > 200 {
				first = first[:200] + "…"
			}
			if first != "" {
				line = line + " (" + first + ")"
			}
		}
		out = append(out, line)
	}
	return out
}
