package swebench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonathanung/strike-cli/internal/scheduler"
)

// Config configures a subset run.
type Config struct {
	// Instances to run (already filtered to the desired subset).
	Instances []Instance
	// RunID labels the run (directories + report).
	RunID string
	// WorkRoot holds per-instance scratch (default DefaultWorkRoot).
	WorkRoot string
	// OutDir receives report.json + predictions.jsonl (required for Write).
	OutDir string
	// Provider / Model / Effort for strike exec.
	Provider string
	Model    string
	Effort   string
	// Strike binary path (optional).
	StrikeBin string
	// AgentTimeout per instance (default 30m).
	AgentTimeout time.Duration
	// Grader name: docker|harness|none (default docker).
	Grader GraderName
	// PullImages pulls SWE-bench images before use (default true).
	PullImages bool
	// Limit runs at most N instances (0 = all). Useful for smoke.
	Limit int
	// InstanceFilter when non-empty restricts to these ids (must be in Instances).
	InstanceFilter []string
	// StrikeVersion stamped into the report.
	StrikeVersion string
	// KeepWorkspace leaves host repo checkouts after each instance.
	KeepWorkspace bool
	// DryRun materializes nothing and does not call docker/agent; writes a
	// skipped report for wiring checks.
	DryRun bool
	// ExtraExecArgs are appended to strike exec before the prompt (config
	// sweeps / sandbox flags).
	ExtraExecArgs []string
	// ProjectConfig is raw JSON written to workDir/.strike/config before the
	// agent runs (parameter sweeps — compaction/leanCode/deferTools/…).
	ProjectConfig []byte
}

// Runner executes the SWE-bench subset.
type Runner struct {
	RT    Runtime
	Agent AgentDriver
	Grade Grader
	Cost  CostEstimator
	// Sched, when non-nil, acquires scheduler.PoolContainer for the full
	// instance lifecycle (materialize → agent → grade → cleanup). Waiting
	// instances do not create containers before admission (E12.10 / #592).
	// Release always runs even if cleanup fails so capacity is not leaked.
	Sched *scheduler.Scheduler
	// Now for timestamps (tests).
	Now func() time.Time
	// Materialize overrides workspace extraction (tests).
	Materialize func(ctx context.Context, instanceID, hostDir string, pull bool) (MaterializeResult, error)
	// ExtractPatch overrides patch extraction (tests).
	ExtractPatch func(workDir string) (string, error)
}

// Run executes all configured instances and returns the report (also written
// when cfg.OutDir is set).
func (r *Runner) Run(ctx context.Context, cfg Config) (Report, error) {
	if err := cfg.validate(); err != nil {
		return Report{}, err
	}
	nowFn := r.Now
	if nowFn == nil {
		nowFn = func() time.Time { return time.Now().UTC() }
	}
	runID := cfg.RunID
	if runID == "" {
		runID = DefaultRunID(nowFn())
	}
	workRoot := cfg.WorkRoot
	if workRoot == "" {
		workRoot = DefaultWorkRoot()
	}
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		return Report{}, err
	}

	instances := cfg.Instances
	if len(cfg.InstanceFilter) > 0 {
		filtered, err := FilterSubset(instances, cfg.InstanceFilter)
		if err != nil {
			return Report{}, err
		}
		instances = filtered
	}
	if cfg.Limit > 0 && len(instances) > cfg.Limit {
		instances = instances[:cfg.Limit]
	}

	pull := cfg.PullImages
	if cfg.DryRun {
		pull = false
	}

	if !cfg.DryRun && r.RT != nil {
		if err := r.RT.Available(ctx); err != nil {
			return Report{}, err
		}
	}

	grader := r.Grade
	if grader == nil && !cfg.DryRun {
		g, err := NewGrader(cfg.Grader, r.RT, workRoot, runID)
		if err != nil {
			return Report{}, err
		}
		grader = g
	}
	agent := r.Agent
	if agent == nil {
		agent = &StrikeExec{}
	}
	costEst := r.Cost
	if costEst == nil {
		costEst = &CatalogCost{}
	}
	extract := r.ExtractPatch
	if extract == nil {
		extract = ExtractPatch
	}
	materialize := r.Materialize
	if materialize == nil {
		materialize = func(ctx context.Context, instanceID, hostDir string, pull bool) (MaterializeResult, error) {
			return MaterializeWorkspace(ctx, r.RT, instanceID, hostDir, pull)
		}
	}

	results := make([]InstanceResult, 0, len(instances))
	preds := make([]Prediction, 0, len(instances))
	modelLabel := cfg.Model
	if modelLabel == "" {
		modelLabel = "strike"
	}

	for _, in := range instances {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		res, pred := r.runOne(ctx, cfg, runID, workRoot, in, agent, grader, costEst, extract, materialize, pull, modelLabel, nowFn)
		results = append(results, res)
		if pred.InstanceID != "" {
			preds = append(preds, pred)
		}
	}

	rep := BuildReport(runID, results, ReportMeta{
		Provider:      cfg.Provider,
		Model:         cfg.Model,
		Grader:        string(cfg.Grader),
		StrikeVersion: cfg.StrikeVersion,
	}, nowFn())

	if cfg.OutDir != "" {
		if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
			return rep, err
		}
		reportPath := filepath.Join(cfg.OutDir, "report.json")
		if err := WriteReport(reportPath, rep); err != nil {
			return rep, err
		}
		predPath := filepath.Join(cfg.OutDir, "predictions.jsonl")
		if err := WritePredictionsJSONL(predPath, preds); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (r *Runner) runOne(
	ctx context.Context,
	cfg Config,
	runID, workRoot string,
	in Instance,
	agent AgentDriver,
	grader Grader,
	costEst CostEstimator,
	extract func(string) (string, error),
	materialize func(context.Context, string, string, bool) (MaterializeResult, error),
	pull bool,
	modelLabel string,
	nowFn func() time.Time,
) (InstanceResult, Prediction) {
	start := nowFn()
	row := InstanceResult{
		InstanceID: in.InstanceID,
		Repo:       in.Repo,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		StartedAt:  start,
		Image:      DockerImageName(in.InstanceID),
	}
	emptyPred := Prediction{}

	finish := func(status InstanceStatus, errMsg string) (InstanceResult, Prediction) {
		end := nowFn()
		row.FinishedAt = end
		row.WallClockMs = end.Sub(start).Milliseconds()
		row.Status = status
		if errMsg != "" {
			row.Error = errMsg
		}
		return row, emptyPred
	}

	if cfg.DryRun {
		row.Status = StatusSkipped
		row.FinishedAt = nowFn()
		row.WallClockMs = row.FinishedAt.Sub(start).Milliseconds()
		row.GradeDetail = "dry-run"
		return row, emptyPred
	}

	// Admit into the in-process container pool before any docker create/pull.
	if r.Sched != nil {
		lease, err := r.Sched.Acquire(ctx, scheduler.PoolContainer)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return finish(StatusSkipped, "container pool canceled: "+err.Error())
			}
			return finish(StatusError, "container pool: "+err.Error())
		}
		// Always release — cleanup failure must not permanently consume capacity.
		defer lease.Release()
	}

	instDir := InstanceRunDir(workRoot, runID, in.InstanceID)
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		return finish(StatusError, err.Error())
	}

	mat, err := materialize(ctx, in.InstanceID, instDir, pull)
	if err != nil {
		return finish(StatusError, "materialize: "+err.Error())
	}
	row.Image = mat.Image
	if !cfg.KeepWorkspace {
		defer os.RemoveAll(instDir)
	}

	if len(cfg.ProjectConfig) > 0 {
		if err := writeProjectConfig(mat.WorkDir, cfg.ProjectConfig); err != nil {
			return finish(StatusError, "project config: "+err.Error())
		}
	}

	prompt := BuildAgentPrompt(in)
	agentTimeout := cfg.AgentTimeout
	if agentTimeout <= 0 {
		agentTimeout = 30 * time.Minute
	}
	agentStart := nowFn()
	execRes, agentErr := agent.Run(ctx, mat.WorkDir, prompt, AgentOpts{
		Strike:    cfg.StrikeBin,
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		Effort:    cfg.Effort,
		Timeout:   agentTimeout,
		ExtraArgs: cfg.ExtraExecArgs,
	})
	row.AgentMs = nowFn().Sub(agentStart).Milliseconds()
	row.SessionID = execRes.SessionID
	if execRes.Provider != "" {
		row.Provider = execRes.Provider
	}
	if execRes.Model != "" {
		row.Model = execRes.Model
	}
	if execRes.Usage != nil {
		row.Usage = execRes.Usage
		row.TokensIn = execRes.Usage.Input
		row.TokensOut = execRes.Usage.Output
		row.TokensCache = execRes.Usage.CacheRead + execRes.Usage.CacheCreation
		row.CostUSD = costEst.Estimate(row.Provider, row.Model, *execRes.Usage)
	}
	if agentErr != nil && execRes.Usage == nil {
		return finish(StatusError, "agent: "+agentErr.Error())
	}

	patch, err := extract(mat.WorkDir)
	if err != nil {
		msg := "patch: " + err.Error()
		if agentErr != nil {
			msg = msg + "; agent: " + agentErr.Error()
		}
		return finish(StatusError, msg)
	}
	patch = NormalizePatch(patch)
	row.PatchBytes = len(patch)
	patchPath := filepath.Join(instDir, "patch.diff")
	_ = WritePatch(patchPath, patch)

	pred := Prediction{
		InstanceID:      in.InstanceID,
		ModelNameOrPath: modelLabel,
		ModelPatch:      patch,
	}

	if grader == nil {
		return finish(StatusError, "nil grader")
	}
	gradeStart := nowFn()
	gr, err := grader.Grade(ctx, in, patch, mat.WorkDir)
	row.GradeMs = nowFn().Sub(gradeStart).Milliseconds()
	if err != nil {
		row.GradeDetail = err.Error()
		end := nowFn()
		row.FinishedAt = end
		row.WallClockMs = end.Sub(start).Milliseconds()
		row.Status = StatusError
		row.Error = "grade: " + err.Error()
		return row, pred
	}
	row.GradeDetail = gr.Detail
	row.FailToPassOK = gr.FailToPassOK
	row.FailToPassN = gr.FailToPassN
	row.PassToPassOK = gr.PassToPassOK
	row.PassToPassN = gr.PassToPassN

	end := nowFn()
	row.FinishedAt = end
	row.WallClockMs = end.Sub(start).Milliseconds()

	if gr.Skipped {
		row.Status = StatusSkipped
		// Still record agent failure as error when no grade.
		if agentErr != nil && patch == "" {
			row.Status = StatusError
			row.Error = agentErr.Error()
		}
		return row, pred
	}
	resolved := gr.Resolved
	row.Resolved = &resolved
	if resolved {
		row.Status = StatusResolved
	} else {
		row.Status = StatusUnresolved
	}
	if agentErr != nil && row.Error == "" {
		// Agent failed but we still graded whatever patch existed.
		row.GradeDetail = joinDetail(row.GradeDetail, "agent: "+agentErr.Error())
	}
	return row, pred
}

func joinDetail(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func (c Config) validate() error {
	if len(c.Instances) == 0 && len(c.InstanceFilter) == 0 && !c.DryRun {
		// DryRun may still want empty for wiring — allow empty only with DryRun.
		return fmt.Errorf("swebench: no instances configured")
	}
	if c.DryRun && len(c.Instances) == 0 {
		// Allow empty dry-run only when filter also empty — still ok for smoke.
		return nil
	}
	if len(c.Instances) == 0 {
		return fmt.Errorf("swebench: no instances configured")
	}
	switch c.Grader {
	case "", GraderDocker, GraderHarness, GraderNone:
	default:
		return fmt.Errorf("swebench: invalid grader %q", c.Grader)
	}
	return nil
}

// ResolveInstances loads dataset from path or HF and filters to the default
// subset (or explicit ids).
func ResolveInstances(ctx context.Context, datasetPath string, ids []string, client *DatasetClient) ([]Instance, error) {
	if len(ids) == 0 {
		ids = DefaultSubsetIDs()
	}
	var all []Instance
	var err error
	if datasetPath != "" {
		all, err = LoadInstancesJSONL(datasetPath)
	} else {
		if client == nil {
			client = &DatasetClient{}
		}
		all, err = client.FetchInstances(ctx)
	}
	if err != nil {
		return nil, err
	}
	return FilterSubset(all, ids)
}

// writeProjectConfig materializes a project-layer .strike/config for dial overrides.
func writeProjectConfig(workDir string, raw []byte) error {
	if workDir == "" {
		return fmt.Errorf("swebench: empty workDir")
	}
	if len(raw) == 0 {
		return nil
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("swebench: project config json: %w", err)
	}
	dir := filepath.Join(workDir, ".strike")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out := append([]byte(nil), raw...)
	if len(out) > 0 && out[len(out)-1] != '\n' {
		out = append(out, '\n')
	}
	return os.WriteFile(filepath.Join(dir, "config"), out, 0o644)
}
