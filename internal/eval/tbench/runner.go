package tbench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/swebench"
)

// Config configures a Terminal-Bench subset run.
type Config struct {
	Instances      []Instance
	RunID          string
	WorkRoot       string
	OutDir         string
	Provider       string
	Model          string
	Effort         string
	StrikeBin      string
	AgentTimeout   time.Duration // 0 = per-task / 30m default
	Grader         GraderName
	PullImages     bool
	Limit          int
	InstanceFilter []string
	StrikeVersion  string
	KeepWorkspace  bool
	DryRun         bool
	// ExtraExecArgs are appended to strike exec before the prompt (config
	// overrides for sweeps — e.g. future --config flags).
	ExtraExecArgs []string
	// ProjectConfig is raw JSON written to workDir/.strike/config before the
	// agent runs (parameter sweeps).
	ProjectConfig []byte
}

// Runner executes the Terminal-Bench subset.
type Runner struct {
	RT    swebench.Runtime
	Agent swebench.AgentDriver
	Grade Grader
	Cost  swebench.CostEstimator
	Now   func() time.Time
	// Materialize overrides workspace extraction (tests).
	Materialize func(ctx context.Context, image, hostDir string, pull bool) (MaterializeResult, error)
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
		g, err := NewGrader(cfg.Grader, r.RT, workRoot, pull)
		if err != nil {
			return Report{}, err
		}
		grader = g
	}
	agent := r.Agent
	if agent == nil {
		agent = &swebench.StrikeExec{}
	}
	costEst := r.Cost
	if costEst == nil {
		costEst = &swebench.CatalogCost{}
	}
	materialize := r.Materialize
	if materialize == nil {
		materialize = func(ctx context.Context, image, hostDir string, pull bool) (MaterializeResult, error) {
			return MaterializeWorkspace(ctx, r.RT, image, hostDir, pull)
		}
	}

	results := make([]InstanceResult, 0, len(instances))
	for _, in := range instances {
		if err := ctx.Err(); err != nil {
			return Report{}, err
		}
		res := r.runOne(ctx, cfg, runID, workRoot, in, agent, grader, costEst, materialize, pull, nowFn)
		results = append(results, res)
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
	}
	return rep, nil
}

func (r *Runner) runOne(
	ctx context.Context,
	cfg Config,
	runID, workRoot string,
	in Instance,
	agent swebench.AgentDriver,
	grader Grader,
	costEst swebench.CostEstimator,
	materialize func(context.Context, string, string, bool) (MaterializeResult, error),
	pull bool,
	nowFn func() time.Time,
) InstanceResult {
	start := nowFn()
	image := in.DockerImage
	if image == "" {
		image = DefaultImage(in.InstanceID)
	}
	row := InstanceResult{
		InstanceID: in.InstanceID,
		Category:   in.Category,
		Difficulty: in.Difficulty,
		Provider:   cfg.Provider,
		Model:      cfg.Model,
		StartedAt:  start,
		Image:      image,
	}

	finish := func(status InstanceStatus, errMsg string) InstanceResult {
		end := nowFn()
		row.FinishedAt = end
		row.WallClockMs = end.Sub(start).Milliseconds()
		row.Status = status
		if errMsg != "" {
			row.Error = errMsg
		}
		return row
	}

	if cfg.DryRun {
		row.Status = StatusSkipped
		row.FinishedAt = nowFn()
		row.WallClockMs = row.FinishedAt.Sub(start).Milliseconds()
		row.GradeDetail = "dry-run"
		return row
	}

	instDir := InstanceRunDir(workRoot, runID, in.InstanceID)
	if err := os.MkdirAll(instDir, 0o755); err != nil {
		return finish(StatusError, err.Error())
	}
	if !cfg.KeepWorkspace {
		defer os.RemoveAll(instDir)
	}

	mat, err := materialize(ctx, image, instDir, pull)
	if err != nil {
		return finish(StatusError, "materialize: "+err.Error())
	}
	row.Image = mat.Image
	if mat.ContainerID != "" && r.RT != nil {
		cid := mat.ContainerID
		defer func() { _ = r.RT.Remove(context.Background(), cid) }()
	}

	isoCfg, isoErr := swebench.MergeEvalIsolation(cfg.ProjectConfig)
	if isoErr != nil {
		return finish(StatusError, "project config: "+isoErr.Error())
	}
	if err := writeProjectConfig(mat.WorkDir, isoCfg); err != nil {
		return finish(StatusError, "project config: "+err.Error())
	}

	prompt := FormatAgentPrompt(in, mat.ContainerID)
	agentTimeout := AgentTimeout(in, cfg.AgentTimeout)
	agentStart := nowFn()
	var env []string
	if mat.ContainerID != "" {
		if err := WriteEvalExecHelper(instDir); err != nil {
			return finish(StatusError, "eval-exec helper: "+err.Error())
		}
		env = append(env,
			"STRIKE_EVAL_CONTAINER="+mat.ContainerID,
			"STRIKE_EVAL_WORKDIR="+WorkDirInContainer,
		)
		if p := os.Getenv("PATH"); p != "" {
			env = append(env, "PATH="+instDir+string(os.PathListSeparator)+p)
		} else {
			env = append(env, "PATH="+instDir)
		}
	}
	execRes, agentErr := agent.Run(ctx, mat.WorkDir, prompt, swebench.AgentOpts{
		Strike:    cfg.StrikeBin,
		Provider:  cfg.Provider,
		Model:     cfg.Model,
		Effort:    cfg.Effort,
		Timeout:   agentTimeout,
		ExtraArgs: swebench.WithEvalExecDefaults(cfg.ExtraExecArgs),
		Env:       env,
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
		row.Usage = fromSWEUsage(execRes.Usage)
		row.TokensIn = execRes.Usage.Input
		row.TokensOut = execRes.Usage.Output
		row.TokensCache = execRes.Usage.CacheRead + execRes.Usage.CacheCreation
		row.CostUSD = costEst.Estimate(row.Provider, row.Model, *execRes.Usage)
	}
	if agentErr != nil && execRes.Usage == nil {
		return finish(StatusError, "agent: "+agentErr.Error())
	}

	reclaimWorkspaceOwner(ctx, r.RT, mat.ContainerID)

	if grader == nil {
		return finish(StatusError, "nil grader")
	}
	if dg, ok := grader.(*DockerGrader); ok {
		dg.LiveContainer = mat.ContainerID
	}
	gradeStart := nowFn()
	gr, err := grader.Grade(ctx, in, mat.WorkDir)
	row.GradeMs = nowFn().Sub(gradeStart).Milliseconds()
	if err != nil {
		row.GradeDetail = err.Error()
		end := nowFn()
		row.FinishedAt = end
		row.WallClockMs = end.Sub(start).Milliseconds()
		row.Status = StatusError
		row.Error = "grade: " + err.Error()
		return row
	}
	row.GradeDetail = gr.Detail
	row.Reward = gr.Reward

	end := nowFn()
	row.FinishedAt = end
	row.WallClockMs = end.Sub(start).Milliseconds()

	if gr.Skipped {
		row.Status = StatusSkipped
		if agentErr != nil {
			row.Status = StatusError
			row.Error = agentErr.Error()
		}
		return row
	}
	resolved := gr.Resolved
	row.Resolved = &resolved
	if resolved {
		row.Status = StatusResolved
	} else {
		row.Status = StatusUnresolved
	}
	if agentErr != nil && row.Error == "" {
		row.GradeDetail = joinDetail(row.GradeDetail, "agent: "+agentErr.Error())
	}
	return row
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
	if len(c.Instances) == 0 && !c.DryRun {
		return fmt.Errorf("tbench: no instances configured")
	}
	if c.DryRun && len(c.Instances) == 0 {
		return nil
	}
	if len(c.Instances) == 0 {
		return fmt.Errorf("tbench: no instances configured")
	}
	switch c.Grader {
	case "", GraderDocker, GraderNone:
	default:
		return fmt.Errorf("tbench: invalid grader %q", c.Grader)
	}
	return nil
}

// ResolveInstances loads a task pack directory or JSONL and filters to ids
// (default: embedded subset).
func ResolveInstances(tasksDir, datasetPath string, ids []string) ([]Instance, error) {
	if len(ids) == 0 {
		ids = DefaultSubsetIDs()
	}
	if datasetPath != "" {
		all, err := LoadInstancesJSONL(datasetPath)
		if err != nil {
			return nil, err
		}
		return FilterSubset(all, ids)
	}
	if tasksDir == "" {
		return nil, fmt.Errorf("tbench: provide --tasks-dir (clone of %s) or --dataset JSONL", DatasetRepo)
	}
	return LoadTaskPack(tasksDir, ids)
}

// writeProjectConfig materializes a project-layer .strike/config for dial overrides.
func writeProjectConfig(workDir string, raw []byte) error {
	if workDir == "" {
		return fmt.Errorf("tbench: empty workDir")
	}
	if len(raw) == 0 {
		return nil
	}
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return fmt.Errorf("tbench: project config json: %w", err)
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
