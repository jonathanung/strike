package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/eval/swebench"
	"github.com/jonathanung/strike-cli/internal/eval/sweep"
	"github.com/jonathanung/strike-cli/internal/eval/tbench"
	"github.com/jonathanung/strike-cli/internal/version"
)

const evalUsage = `Run evaluation benchmarks (internal regression signal).

Usage:
  strike eval swebench [options]
  strike eval tbench [options]
  strike eval sweep [options]
  strike eval help

SWE-bench Verified subset (E3.3 / #561):
  Fixed 50-instance subset, Docker per instance, driven by strike exec --json.
  Writes report.json + predictions.jsonl for trend tracking.

  --dataset <path>     JSONL/JSON instances (default: fetch SWE-bench Verified)
  --out <dir>          output directory (default: evals/swebench/results/<run-id>)
  --run-id <id>        run label (default: UTC timestamp)
  --work-root <dir>    scratch root (default: ~/.strike/eval/swebench)
  --provider <name>    strike exec --provider
  --model <id>         strike exec --model
  --effort <level>     strike exec --effort
  --strike-bin <path>  strike binary for nested exec (default: argv0 or PATH)
  --grader <name>      docker (default) | harness | none
  --limit <n>          run at most n instances (smoke)
  --instance <id>      only this instance id (repeatable)
  --subset-only        print the 50 subset ids and exit
  --dry-run            skip docker/agent; write skipped rows (wiring check)
  --keep-workspace     keep per-instance host checkouts
  --no-pull            do not docker pull images
  --agent-timeout <d>  per-instance agent timeout (default 30m)
  --exec-arg <arg>     extra strike exec arg (repeatable; for config sweeps)
  --config-json <json> project .strike/config overlay written per instance

Terminal-Bench 2 subset (E3.4 / #562):
  Fixed 25-task subset, same harness shape as swebench (Docker + exec --json).
  Writes report.json for trend tracking. Requires a local task pack clone for
  grading (tests/test.sh).

  --tasks-dir <path>   clone of harbor-framework/terminal-bench-2 (required for real runs)
  --dataset <path>     optional JSONL instances (still pass --tasks-dir for tests/)
  --out <dir>          output directory (default: evals/tbench/results/<run-id>)
  --run-id <id>        run label (default: UTC timestamp)
  --work-root <dir>    scratch root (default: ~/.strike/eval/tbench)
  --provider <name>    strike exec --provider
  --model <id>         strike exec --model
  --effort <level>     strike exec --effort
  --strike-bin <path>  strike binary for nested exec (default: argv0 or PATH)
  --grader <name>      docker (default) | none
  --limit <n>          run at most n tasks (smoke)
  --instance <id>      only this task id (repeatable)
  --subset-only        print the 25 subset ids and exit
  --dry-run            skip docker/agent; write skipped rows (wiring check)
  --keep-workspace     keep per-instance host workspaces
  --no-pull            do not docker pull images
  --agent-timeout <d>  override per-task agent timeout
  --exec-arg <arg>     extra strike exec arg (repeatable; for config sweeps)
  --config-json <json> project .strike/config overlay written per instance

Parameter sweeps (E3.5 / #563):
  Run a builtin dial matrix against swebench or tbench. Each point writes a
  subset report under <out>/<point-id>/ and a comparison summary.json.

  --benchmark <name>   swebench (default) | tbench
  --matrix <name>      compaction | leanCode | deferTools | effort | all (default)
  --point <id>         only this matrix point id (repeatable)
  --list-points        print matrix point ids and exit
  --out <dir>          output root (default: evals/sweep/results/<run-id>)
  --run-id <id>        run label (default: UTC timestamp)
  --work-root <dir>    scratch root passed to the underlying runner
  --provider <name>    strike exec --provider
  --model <id>         strike exec --model
  --effort <level>     baseline effort when a point does not set one
  --strike-bin <path>  strike binary for nested exec
  --grader <name>      underlying grader (default: none for dry wiring; docker for real)
  --limit <n>          per-point instance limit (smoke)
  --instance <id>      only this instance/task id (repeatable)
  --dataset <path>     swebench/tbench dataset path
  --tasks-dir <path>   tbench task pack (required for real tbench runs)
  --dry-run            skip docker/agent on every point
  --keep-workspace     keep per-instance host workspaces
  --no-pull            do not docker pull images
  --agent-timeout <d>  per-instance agent timeout
  --exec-arg <arg>     extra strike exec arg on every point (repeatable)
  --continue-on-error  keep sweeping after a point fails

Exit codes: 0 ok, 1 run error, 2 usage.

Caveat: internal regression only — do not put pass rates in the README.
`

func runEvalCLI(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(stdout, evalUsage)
		return 0
	}
	switch args[0] {
	case "swebench", "swe-bench", "swe":
		return runEvalSWEBench(args[1:], stdout, stderr)
	case "tbench", "terminal-bench", "terminalbench", "tb":
		return runEvalTBench(args[1:], stdout, stderr)
	case "sweep", "sweeps", "params":
		return runEvalSweep(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "strike eval: unknown command %q\n", args[0])
		fmt.Fprint(stderr, evalUsage)
		return 2
	}
}

func runEvalSWEBench(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval swebench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dataset := fs.String("dataset", "", "")
	outDir := fs.String("out", "", "")
	runID := fs.String("run-id", "", "")
	workRoot := fs.String("work-root", "", "")
	provider := fs.String("provider", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	strikeBin := fs.String("strike-bin", "", "")
	grader := fs.String("grader", "docker", "")
	limit := fs.Int("limit", 0, "")
	subsetOnly := fs.Bool("subset-only", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	keepWS := fs.Bool("keep-workspace", false, "")
	noPull := fs.Bool("no-pull", false, "")
	agentTimeout := fs.Duration("agent-timeout", 30*time.Minute, "")
	configJSON := fs.String("config-json", "", "")
	var instances multiFlag
	var execArgs multiFlag
	fs.Var(&instances, "instance", "")
	fs.Var(&execArgs, "exec-arg", "")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "strike eval swebench:", err)
		fmt.Fprint(stderr, evalUsage)
		return 2
	}
	if *subsetOnly {
		for _, id := range swebench.DefaultSubsetIDs() {
			fmt.Fprintln(stdout, id)
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rid := *runID
	if rid == "" {
		rid = swebench.DefaultRunID(time.Now())
	}
	out := *outDir
	if out == "" {
		out = filepath.Join("evals", "swebench", "results", rid)
	}
	bin := *strikeBin
	if bin == "" {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		}
	}

	var inst []swebench.Instance
	if !*dryRun || *dataset != "" || len(instances) > 0 {
		var err error
		want := swebench.DefaultSubsetIDs()
		if len(instances) > 0 {
			want = append([]string{}, instances...)
		}
		inst, err = swebench.ResolveInstances(ctx, *dataset, want, nil)
		if err != nil {
			// Dry-run without dataset: synthesize skipped rows from ids only.
			if *dryRun && *dataset == "" {
				inst = make([]swebench.Instance, 0, len(want))
				for _, id := range want {
					inst = append(inst, swebench.Instance{
						InstanceID:       id,
						ProblemStatement: "(dry-run)",
					})
				}
			} else {
				fmt.Fprintln(stderr, "strike eval swebench:", err)
				return 1
			}
		}
	} else if *dryRun {
		for _, id := range swebench.DefaultSubsetIDs() {
			inst = append(inst, swebench.Instance{InstanceID: id, ProblemStatement: "(dry-run)"})
		}
	}

	cfg := swebench.Config{
		Instances:     inst,
		RunID:         rid,
		WorkRoot:      *workRoot,
		OutDir:        out,
		Provider:      *provider,
		Model:         *model,
		Effort:        *effort,
		StrikeBin:     bin,
		Grader:        swebench.GraderName(*grader),
		PullImages:    !*noPull,
		Limit:         *limit,
		StrikeVersion: version.String(),
		KeepWorkspace: *keepWS,
		DryRun:        *dryRun,
		AgentTimeout:  *agentTimeout,
		ExtraExecArgs: append([]string{}, execArgs...),
		ProjectConfig: configJSONBytes(*configJSON),
	}
	if cfg.Grader == "" {
		cfg.Grader = swebench.GraderDocker
	}

	runner := &swebench.Runner{
		RT:    &swebench.CLIRuntime{},
		Agent: &swebench.StrikeExec{},
		Cost:  &swebench.CatalogCost{},
	}
	// Attach grader explicitly so "none" works without docker available.
	if g, err := swebench.NewGrader(cfg.Grader, runner.RT, cfg.WorkRoot, cfg.RunID); err != nil {
		fmt.Fprintln(stderr, "strike eval swebench:", err)
		return 2
	} else {
		runner.Grade = g
	}

	rep, err := runner.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "strike eval swebench:", err)
		return 1
	}
	fmt.Fprint(stdout, swebench.FormatReport(rep))
	fmt.Fprintf(stdout, "\nwrote %s\n", filepath.Join(out, "report.json"))
	fmt.Fprintf(stdout, "wrote %s\n", filepath.Join(out, "predictions.jsonl"))
	return 0
}

func runEvalTBench(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval tbench", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	tasksDir := fs.String("tasks-dir", "", "")
	dataset := fs.String("dataset", "", "")
	outDir := fs.String("out", "", "")
	runID := fs.String("run-id", "", "")
	workRoot := fs.String("work-root", "", "")
	provider := fs.String("provider", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	strikeBin := fs.String("strike-bin", "", "")
	grader := fs.String("grader", "docker", "")
	limit := fs.Int("limit", 0, "")
	subsetOnly := fs.Bool("subset-only", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	keepWS := fs.Bool("keep-workspace", false, "")
	noPull := fs.Bool("no-pull", false, "")
	agentTimeout := fs.Duration("agent-timeout", 0, "")
	configJSON := fs.String("config-json", "", "")
	var instances multiFlag
	var execArgs multiFlag
	fs.Var(&instances, "instance", "")
	fs.Var(&execArgs, "exec-arg", "")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "strike eval tbench:", err)
		fmt.Fprint(stderr, evalUsage)
		return 2
	}
	if *subsetOnly {
		for _, id := range tbench.DefaultSubsetIDs() {
			fmt.Fprintln(stdout, id)
		}
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rid := *runID
	if rid == "" {
		rid = tbench.DefaultRunID(time.Now())
	}
	out := *outDir
	if out == "" {
		out = filepath.Join("evals", "tbench", "results", rid)
	}
	bin := *strikeBin
	if bin == "" {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		}
	}

	want := tbench.DefaultSubsetIDs()
	if len(instances) > 0 {
		want = append([]string{}, instances...)
	}

	var inst []tbench.Instance
	if *dryRun && *tasksDir == "" && *dataset == "" {
		for _, id := range want {
			inst = append(inst, tbench.Instance{
				InstanceID:  id,
				Instruction: "(dry-run)",
				DockerImage: tbench.DefaultImage(id),
			})
		}
	} else {
		var err error
		inst, err = tbench.ResolveInstances(*tasksDir, *dataset, want)
		if err != nil {
			if *dryRun && *dataset == "" && *tasksDir == "" {
				inst = nil
				for _, id := range want {
					inst = append(inst, tbench.Instance{
						InstanceID:  id,
						Instruction: "(dry-run)",
						DockerImage: tbench.DefaultImage(id),
					})
				}
			} else {
				fmt.Fprintln(stderr, "strike eval tbench:", err)
				return 1
			}
		}
		// When dataset JSONL is used without task dirs, try to attach TaskDir
		// from --tasks-dir for grading.
		if *tasksDir != "" {
			for i := range inst {
				if inst[i].TaskDir == "" {
					cand := filepath.Join(*tasksDir, inst[i].InstanceID)
					if st, err := os.Stat(cand); err == nil && st.IsDir() {
						inst[i].TaskDir = cand
					}
				}
			}
		}
	}

	cfg := tbench.Config{
		Instances:     inst,
		RunID:         rid,
		WorkRoot:      *workRoot,
		OutDir:        out,
		Provider:      *provider,
		Model:         *model,
		Effort:        *effort,
		StrikeBin:     bin,
		Grader:        tbench.GraderName(*grader),
		PullImages:    !*noPull,
		Limit:         *limit,
		StrikeVersion: version.String(),
		KeepWorkspace: *keepWS,
		DryRun:        *dryRun,
		AgentTimeout:  *agentTimeout,
		ExtraExecArgs: append([]string{}, execArgs...),
		ProjectConfig: configJSONBytes(*configJSON),
	}
	if cfg.Grader == "" {
		cfg.Grader = tbench.GraderDocker
	}

	runner := &tbench.Runner{
		RT:    &swebench.CLIRuntime{},
		Agent: &swebench.StrikeExec{},
		Cost:  &swebench.CatalogCost{},
	}
	if g, err := tbench.NewGrader(cfg.Grader, runner.RT, cfg.WorkRoot, cfg.PullImages); err != nil {
		fmt.Fprintln(stderr, "strike eval tbench:", err)
		return 2
	} else {
		runner.Grade = g
	}

	rep, err := runner.Run(ctx, cfg)
	if err != nil {
		fmt.Fprintln(stderr, "strike eval tbench:", err)
		return 1
	}
	fmt.Fprint(stdout, tbench.FormatReport(rep))
	fmt.Fprintf(stdout, "\nwrote %s\n", filepath.Join(out, "report.json"))
	return 0
}

func runEvalSweep(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("eval sweep", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	benchmark := fs.String("benchmark", sweep.BenchmarkSWEBench, "")
	matrixName := fs.String("matrix", sweep.MatrixAll, "")
	outDir := fs.String("out", "", "")
	runID := fs.String("run-id", "", "")
	workRoot := fs.String("work-root", "", "")
	provider := fs.String("provider", "", "")
	model := fs.String("model", "", "")
	effort := fs.String("effort", "", "")
	strikeBin := fs.String("strike-bin", "", "")
	grader := fs.String("grader", "", "")
	limit := fs.Int("limit", 0, "")
	listPoints := fs.Bool("list-points", false, "")
	dryRun := fs.Bool("dry-run", false, "")
	keepWS := fs.Bool("keep-workspace", false, "")
	noPull := fs.Bool("no-pull", false, "")
	agentTimeout := fs.Duration("agent-timeout", 0, "")
	dataset := fs.String("dataset", "", "")
	tasksDir := fs.String("tasks-dir", "", "")
	continueOnErr := fs.Bool("continue-on-error", false, "")
	var instances multiFlag
	var points multiFlag
	var execArgs multiFlag
	fs.Var(&instances, "instance", "")
	fs.Var(&points, "point", "")
	fs.Var(&execArgs, "exec-arg", "")

	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, "strike eval sweep:", err)
		fmt.Fprint(stderr, evalUsage)
		return 2
	}

	matrix, err := sweep.ResolveMatrix(*matrixName)
	if err != nil {
		fmt.Fprintln(stderr, "strike eval sweep:", err)
		return 2
	}
	matrix, err = matrix.FilterByIDs(points)
	if err != nil {
		fmt.Fprintln(stderr, "strike eval sweep:", err)
		return 2
	}
	if err := matrix.Validate(); err != nil {
		fmt.Fprintln(stderr, "strike eval sweep:", err)
		return 2
	}
	if *listPoints {
		for _, p := range matrix {
			fmt.Fprintln(stdout, p.ID)
		}
		return 0
	}

	bench := strings.ToLower(strings.TrimSpace(*benchmark))
	switch bench {
	case sweep.BenchmarkSWEBench, "swe", "swe-bench":
		bench = sweep.BenchmarkSWEBench
	case sweep.BenchmarkTBench, "terminal-bench", "tb":
		bench = sweep.BenchmarkTBench
	default:
		fmt.Fprintf(stderr, "strike eval sweep: unknown benchmark %q\n", *benchmark)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rid := *runID
	if rid == "" {
		rid = sweep.DefaultRunID(time.Now())
	}
	out := *outDir
	if out == "" {
		out = filepath.Join("evals", "sweep", "results", rid)
	}
	bin := *strikeBin
	if bin == "" {
		if exe, err := os.Executable(); err == nil {
			bin = exe
		}
	}
	gradeName := strings.TrimSpace(*grader)
	if gradeName == "" {
		if *dryRun {
			gradeName = "none"
		} else {
			gradeName = "docker"
		}
	}

	// Resolve instances once; reuse across points.
	var sweInst []swebench.Instance
	var tbInst []tbench.Instance
	switch bench {
	case sweep.BenchmarkSWEBench:
		sweInst, err = resolveSweepSWEInstances(ctx, *dataset, instances, *dryRun)
		if err != nil {
			fmt.Fprintln(stderr, "strike eval sweep:", err)
			return 1
		}
	case sweep.BenchmarkTBench:
		tbInst, err = resolveSweepTBInstances(*tasksDir, *dataset, instances, *dryRun)
		if err != nil {
			fmt.Fprintln(stderr, "strike eval sweep:", err)
			return 1
		}
	}

	if err := os.MkdirAll(out, 0o755); err != nil {
		fmt.Fprintln(stderr, "strike eval sweep:", err)
		return 1
	}

	results := make([]sweep.PointResult, 0, len(matrix))
	var pointErrs int
	for _, pt := range matrix {
		if err := ctx.Err(); err != nil {
			fmt.Fprintln(stderr, "strike eval sweep:", err)
			return 1
		}
		pointOut := filepath.Join(out, pt.ID)
		pointStart := time.Now()
		pr := sweep.PointResult{Point: pt, OutDir: pointOut}

		overlayJSON, err := pt.Overlay.Marshal()
		if err != nil {
			pr.Error = err.Error()
			pointErrs++
			results = append(results, pr)
			if !*continueOnErr {
				break
			}
			continue
		}
		// Empty overlay still valid (effort-only points write effort in overlay).
		if pt.Overlay.IsZero() {
			overlayJSON = nil
		}

		pointEffort := *effort
		if pt.Effort != "" {
			pointEffort = pt.Effort
		}

		switch bench {
		case sweep.BenchmarkSWEBench:
			cfg := swebench.Config{
				Instances:     sweInst,
				RunID:         rid + "-" + pt.ID,
				WorkRoot:      *workRoot,
				OutDir:        pointOut,
				Provider:      *provider,
				Model:         *model,
				Effort:        pointEffort,
				StrikeBin:     bin,
				Grader:        swebench.GraderName(gradeName),
				PullImages:    !*noPull,
				Limit:         *limit,
				StrikeVersion: version.String(),
				KeepWorkspace: *keepWS,
				DryRun:        *dryRun,
				AgentTimeout:  *agentTimeout,
				ExtraExecArgs: append([]string{}, execArgs...),
				ProjectConfig: overlayJSON,
			}
			if cfg.AgentTimeout <= 0 {
				cfg.AgentTimeout = 30 * time.Minute
			}
			if cfg.Grader == "" {
				cfg.Grader = swebench.GraderNone
			}
			runner := &swebench.Runner{
				RT:    &swebench.CLIRuntime{},
				Agent: &swebench.StrikeExec{},
				Cost:  &swebench.CatalogCost{},
			}
			if g, gerr := swebench.NewGrader(cfg.Grader, runner.RT, cfg.WorkRoot, cfg.RunID); gerr != nil {
				pr.Error = gerr.Error()
			} else {
				runner.Grade = g
				rep, rerr := runner.Run(ctx, cfg)
				if rerr != nil {
					pr.Error = rerr.Error()
				} else {
					pr.Metrics = sweep.MetricsFromAggregate(
						rep.Attempted, rep.Resolved, rep.Unresolved, rep.Errors, rep.Skipped,
						rep.PassRate, rep.TotalTokensIn, rep.TotalTokensOut, rep.TotalCostUSD, rep.TotalWallMs,
					)
					pr.ReportPath = filepath.Join(pointOut, "report.json")
				}
			}
		case sweep.BenchmarkTBench:
			cfg := tbench.Config{
				Instances:     tbInst,
				RunID:         rid + "-" + pt.ID,
				WorkRoot:      *workRoot,
				OutDir:        pointOut,
				Provider:      *provider,
				Model:         *model,
				Effort:        pointEffort,
				StrikeBin:     bin,
				Grader:        tbench.GraderName(gradeName),
				PullImages:    !*noPull,
				Limit:         *limit,
				StrikeVersion: version.String(),
				KeepWorkspace: *keepWS,
				DryRun:        *dryRun,
				AgentTimeout:  *agentTimeout,
				ExtraExecArgs: append([]string{}, execArgs...),
				ProjectConfig: overlayJSON,
			}
			if cfg.Grader == "" {
				cfg.Grader = tbench.GraderNone
			}
			runner := &tbench.Runner{
				RT:    &swebench.CLIRuntime{},
				Agent: &swebench.StrikeExec{},
				Cost:  &swebench.CatalogCost{},
			}
			if g, gerr := tbench.NewGrader(cfg.Grader, runner.RT, cfg.WorkRoot, cfg.PullImages); gerr != nil {
				pr.Error = gerr.Error()
			} else {
				runner.Grade = g
				rep, rerr := runner.Run(ctx, cfg)
				if rerr != nil {
					pr.Error = rerr.Error()
				} else {
					pr.Metrics = sweep.MetricsFromAggregate(
						rep.Attempted, rep.Resolved, rep.Unresolved, rep.Errors, rep.Skipped,
						rep.PassRate, rep.TotalTokensIn, rep.TotalTokensOut, rep.TotalCostUSD, rep.TotalWallMs,
					)
					pr.ReportPath = filepath.Join(pointOut, "report.json")
				}
			}
		}
		pr.DurationMs = time.Since(pointStart).Milliseconds()
		if pr.Error != "" {
			pointErrs++
			fmt.Fprintf(stderr, "strike eval sweep: point %s: %s\n", pt.ID, pr.Error)
		}
		results = append(results, pr)
		if pr.Error != "" && !*continueOnErr {
			break
		}
	}

	sum := sweep.BuildSummary(rid, results, sweep.SummaryMeta{
		Benchmark:     bench,
		Matrix:        *matrixName,
		Provider:      *provider,
		Model:         *model,
		Grader:        gradeName,
		StrikeVersion: version.String(),
		Limit:         *limit,
		DryRun:        *dryRun,
	}, time.Now().UTC())
	sumPath := filepath.Join(out, "summary.json")
	if err := sweep.WriteSummary(sumPath, sum); err != nil {
		fmt.Fprintln(stderr, "strike eval sweep:", err)
		return 1
	}
	fmt.Fprint(stdout, sweep.FormatSummary(sum))
	fmt.Fprintf(stdout, "\nwrote %s\n", sumPath)
	if pointErrs > 0 {
		return 1
	}
	return 0
}

func resolveSweepSWEInstances(ctx context.Context, dataset string, instances multiFlag, dryRun bool) ([]swebench.Instance, error) {
	want := swebench.DefaultSubsetIDs()
	if len(instances) > 0 {
		want = append([]string{}, instances...)
	}
	if dryRun && dataset == "" {
		out := make([]swebench.Instance, 0, len(want))
		for _, id := range want {
			out = append(out, swebench.Instance{InstanceID: id, ProblemStatement: "(dry-run)"})
		}
		return out, nil
	}
	inst, err := swebench.ResolveInstances(ctx, dataset, want, nil)
	if err != nil {
		if dryRun && dataset == "" {
			out := make([]swebench.Instance, 0, len(want))
			for _, id := range want {
				out = append(out, swebench.Instance{InstanceID: id, ProblemStatement: "(dry-run)"})
			}
			return out, nil
		}
		return nil, err
	}
	return inst, nil
}

func resolveSweepTBInstances(tasksDir, dataset string, instances multiFlag, dryRun bool) ([]tbench.Instance, error) {
	want := tbench.DefaultSubsetIDs()
	if len(instances) > 0 {
		want = append([]string{}, instances...)
	}
	if dryRun && tasksDir == "" && dataset == "" {
		out := make([]tbench.Instance, 0, len(want))
		for _, id := range want {
			out = append(out, tbench.Instance{
				InstanceID:  id,
				Instruction: "(dry-run)",
				DockerImage: tbench.DefaultImage(id),
			})
		}
		return out, nil
	}
	inst, err := tbench.ResolveInstances(tasksDir, dataset, want)
	if err != nil {
		if dryRun && dataset == "" && tasksDir == "" {
			out := make([]tbench.Instance, 0, len(want))
			for _, id := range want {
				out = append(out, tbench.Instance{
					InstanceID:  id,
					Instruction: "(dry-run)",
					DockerImage: tbench.DefaultImage(id),
				})
			}
			return out, nil
		}
		return nil, err
	}
	if tasksDir != "" {
		for i := range inst {
			if inst[i].TaskDir == "" {
				cand := filepath.Join(tasksDir, inst[i].InstanceID)
				if st, err := os.Stat(cand); err == nil && st.IsDir() {
					inst[i].TaskDir = cand
				}
			}
		}
	}
	return inst, nil
}

func configJSONBytes(s string) []byte {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []byte(s)
}

// multiFlag collects repeated flag values.
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error {
	v = strings.TrimSpace(v)
	if v == "" {
		return fmt.Errorf("empty value")
	}
	*m = append(*m, v)
	return nil
}
