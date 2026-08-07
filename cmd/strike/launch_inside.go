package main

import (
	"bufio"
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
//
// Session model (E12.6): one container per repo (ContainerName); additional
// sessions attach to the live container. Stale config prompts attach/rebuild/cancel.
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

	launchOpts := container.LaunchInsideOpts{
		Args:         innerArgs,
		WorkDirHost:  hostPath,
		Version:      version.Version,
		ForceRebuild: opts.containerRebuild,
		Replace:      opts.containerRebuild,
		AttachStale:  opts.containerAttachStale,
		OnResult: func(res container.LaunchResult) {
			printLaunchResult(stderr, res)
		},
	}

	err = mgr.LaunchInside(ctx, launchOpts)
	if err != nil {
		var stale *container.StaleContainerError
		if errors.As(err, &stale) {
			choice, cerr := resolveStaleContainerChoice(opts, stale, os.Stdin, stderr)
			if cerr != nil {
				return cerr
			}
			switch choice {
			case "attach":
				launchOpts.AttachStale = true
				launchOpts.Replace = false
			case "rebuild":
				launchOpts.Replace = true
				launchOpts.ForceRebuild = true
				launchOpts.AttachStale = false
			case "cancel":
				return container.ErrLaunchCancelled
			}
			err = mgr.LaunchInside(ctx, launchOpts)
		}
	}
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
	if errors.Is(err, container.ErrLaunchCancelled) {
		return err
	}
	return err
}

func printLaunchResult(w io.Writer, res container.LaunchResult) {
	if w == nil {
		return
	}
	name := res.Name
	if name == "" {
		name = res.ID
	}
	short := res.ID
	if len(short) > 12 {
		short = short[:12]
	}
	switch res.Mode {
	case container.LaunchModeAttached:
		fmt.Fprintf(w, "strike: attached to existing container %s (%s)\n", name, short)
	case container.LaunchModeRestarted:
		fmt.Fprintf(w, "strike: restarted container %s (%s)\n", name, short)
	case container.LaunchModeRebuilt:
		fmt.Fprintf(w, "strike: rebuilt and started container %s (%s)\n", name, short)
	default:
		fmt.Fprintf(w, "strike: started container %s (%s)\n", name, short)
	}
}

// resolveStaleContainerChoice returns attach|rebuild|cancel.
// Flags win; otherwise interactive prompt when stdin is a TTY.
func resolveStaleContainerChoice(opts cliOptions, stale *container.StaleContainerError, in io.Reader, errw io.Writer) (string, error) {
	if opts.containerAttachStale {
		return "attach", nil
	}
	if opts.containerRebuild {
		return "rebuild", nil
	}
	if opts.containerCancelStale {
		return "cancel", nil
	}
	fmt.Fprintf(errw, "strike: running container is stale: %s\n", stale.Reason)
	if stale.Name != "" {
		fmt.Fprintf(errw, "strike: container %s\n", stale.Name)
	}
	if stale.HaveHash != "" || stale.WantHash != "" {
		fmt.Fprintf(errw, "strike: have hash %s want %s\n", shortHash(stale.HaveHash), shortHash(stale.WantHash))
	}
	// Interactive only when reading the real TTY stdin (not a piped/test reader).
	if !readerIsInteractive(in) {
		return "", fmt.Errorf("%w (non-interactive: pass --container-attach-stale, --container-rebuild, or --container-cancel)", stale)
	}
	fmt.Fprintf(errw, "strike: attach anyway [a], rebuild [r], or cancel [c]? ")
	sc := bufio.NewScanner(in)
	if !sc.Scan() {
		return "cancel", container.ErrLaunchCancelled
	}
	ans := strings.ToLower(strings.TrimSpace(sc.Text()))
	switch ans {
	case "a", "attach", "y", "yes":
		return "attach", nil
	case "r", "rebuild":
		return "rebuild", nil
	case "c", "cancel", "n", "no", "":
		return "cancel", nil
	default:
		return "", fmt.Errorf("container: unknown choice %q (want attach/rebuild/cancel)", ans)
	}
}

// readerIsInteractive is true only for the process TTY stdin.
func readerIsInteractive(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok || f != os.Stdin {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func shortHash(h string) string {
	if h == "" {
		return "(none)"
	}
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func stripLaunchInsideArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--launch-inside-container", "-launch-inside-container",
			"--container-rebuild", "--container-attach-stale", "--container-cancel":
			continue
		default:
			if strings.HasPrefix(a, "--launch-inside-container=") ||
				strings.HasPrefix(a, "--container-rebuild=") ||
				strings.HasPrefix(a, "--container-attach-stale=") ||
				strings.HasPrefix(a, "--container-cancel=") {
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
