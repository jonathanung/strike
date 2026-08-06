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
	"github.com/jonathanung/strike-cli/internal/eval/tbench"
	"github.com/jonathanung/strike-cli/internal/version"
)

const evalUsage = `Run evaluation benchmarks (internal regression signal).

Usage:
  strike eval swebench [options]
  strike eval tbench [options]
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
	var instances multiFlag
	fs.Var(&instances, "instance", "")

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
