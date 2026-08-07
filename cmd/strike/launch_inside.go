package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/container"
	"github.com/jonathanung/strike-cli/internal/project"
	"github.com/jonathanung/strike-cli/internal/version"
)

// maybeLaunchInsideContainer re-execs strike inside the managed container when
// --launch-inside-container is set or container.execution is "container".
// On successful handoff it exits the process with the inner strike exit code.
// Returns nil when the caller should continue on the host (local mode / already inside).
func maybeLaunchInsideContainer(opts cliOptions, stderr io.Writer) error {
	if container.InsideContainer() {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cwd)
	if err != nil {
		return err
	}
	want := opts.launchInsideContainer || strings.EqualFold(cfg.Container.Execution, "container")
	if !want {
		return nil
	}

	rtCfg := cfg.Container.ToRuntime(version.Version)
	if cfg.Container.Engine != "" {
		// engine override applied via CLI Binary
	}
	cli := container.NewCLI(cfg.Container.Engine)
	mgr, err := container.NewManager(cwd, rtCfg, cli)
	if err != nil {
		return err
	}

	hostPath := cwd
	// Mount session worktree when configured (not whole repo).
	mode := project.NormalizeWorktreeMode(cfg.Session.Worktree)
	if opts.worktree || mode == project.WorktreeAlways || mode == project.WorktreeAuto {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Stable id for the outer launch worktree (inner session may create its own).
		wt, werr := project.Add(ctx, cwd, "container-launch")
		if werr == nil {
			hostPath = wt.Path
			fmt.Fprintf(stderr, "strike: mounting worktree %s\n", hostPath)
		} else if !errors.Is(werr, project.ErrNotGitRepository) {
			return fmt.Errorf("session worktree for container: %w", werr)
		}
	}

	innerArgs := stripLaunchInsideArgs(os.Args[1:])
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err = mgr.LaunchInside(ctx, container.LaunchInsideOpts{
		Args:         innerArgs,
		WorkDirHost:  hostPath,
		Version:      version.Version,
		ForceRebuild: false,
		Replace:      true,
	})
	if err == nil {
		os.Exit(0)
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		os.Exit(ee.ExitCode())
	}
	// Surface preflight codes clearly.
	var pe *container.PreflightError
	if errors.As(err, &pe) {
		return fmt.Errorf("%s", pe.Error())
	}
	return err
}

func stripLaunchInsideArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--launch-inside-container":
			continue
		case "-launch-inside-container":
			continue
		default:
			if strings.HasPrefix(a, "--launch-inside-container=") {
				continue
			}
			out = append(out, a)
		}
	}
	return out
}

// resolveContainerWorkDir is used by tests.
func resolveContainerWorkDir(cwd string) string {
	return filepath.Clean(cwd)
}
