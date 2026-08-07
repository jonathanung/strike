package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/project"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/session"
	"github.com/jonathanung/strike-cli/internal/tui"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/update"
	"github.com/jonathanung/strike-cli/pkg/timeline"
)

// sessionStore is the narrow persistence surface runSession needs from a
// session event log.
type sessionStore interface {
	Append(protocol.Event) error
	Close() error
}

// runSession owns the engine/frontend/session lifecycle: it starts the engine
// and an event tee, runs the frontend, then on frontend return signals
// frontend-done and cancels the engine. The tee appends every engine event
// before optional frontend delivery; after frontend-done it stops forwarding
// but keeps draining and persisting until engine events close. Store.Close
// runs only after the engine and tee have both finished.
func runSession(
	parent context.Context,
	engineRun func(context.Context),
	engineEvents <-chan protocol.Event,
	store sessionStore,
	frontend func(<-chan protocol.Event) error,
) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	frontendEvents := make(chan protocol.Event, 256)
	frontendDone := make(chan struct{})

	var appendErr error
	teeDone := make(chan struct{})
	go func() {
		defer close(teeDone)
		var closeFrontendOnce sync.Once
		closeFrontend := func() {
			closeFrontendOnce.Do(func() { close(frontendEvents) })
		}
		defer closeFrontend()

		forwarding := true
		for ev := range engineEvents {
			if err := store.Append(ev); err != nil && appendErr == nil {
				appendErr = err
			}
			// Best-effort security audit sink (never fails the session path).
			if obs, ok := store.(interface{ ObserveAudit(protocol.Event) }); ok {
				obs.ObserveAudit(ev)
			}
			if !forwarding {
				continue
			}
			select {
			case <-frontendDone:
				forwarding = false
				closeFrontend()
				continue
			default:
			}
			select {
			case frontendEvents <- ev:
			case <-frontendDone:
				forwarding = false
				closeFrontend()
			}
		}
	}()

	engineDone := make(chan struct{})
	go func() {
		defer close(engineDone)
		engineRun(ctx)
	}()

	frontendErr := frontend(frontendEvents)
	close(frontendDone)
	cancel()

	<-engineDone
	<-teeDone

	var out error
	if frontendErr != nil {
		out = errors.Join(out, fmt.Errorf("frontend: %w", frontendErr))
	}
	if appendErr != nil {
		out = errors.Join(out, fmt.Errorf("append: %w", appendErr))
	}
	if closeErr := store.Close(); closeErr != nil {
		out = errors.Join(out, fmt.Errorf("close: %w", closeErr))
	}
	return out
}

// worktreeNotGitNotice is shown when session.worktree / --worktree wants a
// git worktree but the launch path is not a repository. App continues on cwd.
const worktreeNotGitNotice = "Session worktree was not created because no git repository was detected. Continuing in the launch directory."

// bindSessionWorktree resolves the tool CWD for a root session. On resume it
// reuses a durable worktree path when still present. On create it may add a
// git worktree under <main>/.strike/worktrees/<id>/ when mode/force says so.
// Returns a cleanup func (possibly nil) for worktreeCleanup=delete.
// When the path is not a git repo, soft-fails: toolDir stays launchDir, notice
// explains why, and err is nil so the app can launch normally.
func bindSessionWorktree(
	sessions *session.Manager,
	sessionID, launchDir string,
	cfg config.Config,
	force bool,
	resuming bool,
	openRootsBefore int,
) (toolDir string, cleanup func() error, notice string, err error) {
	toolDir = launchDir
	info, err := sessions.Get(sessionID)
	if err != nil {
		return launchDir, nil, "", fmt.Errorf("session worktree: %w", err)
	}

	if resuming {
		if info.WorktreePath != "" {
			st, statErr := os.Stat(info.WorktreePath)
			if statErr == nil && st.IsDir() {
				toolDir = info.WorktreePath
				cleanup = worktreeCleanupFunc(cfg, info.WorktreePath, info.WorktreeBranch, launchDir)
				return toolDir, cleanup, "", nil
			}
		}
		// Missing worktree on resume: stay on launch cwd (no half-bind).
		return launchDir, nil, "", nil
	}

	if !project.WantWorktree(cfg.Session.Worktree, force, openRootsBefore) {
		return launchDir, nil, "", nil
	}

	wt, err := project.Add(context.Background(), launchDir, sessionID)
	if err != nil {
		if errors.Is(err, project.ErrNotGitRepository) {
			// always / --worktree outside a repo: stay on launch cwd.
			return launchDir, nil, worktreeNotGitNotice, nil
		}
		return launchDir, nil, "", fmt.Errorf("session worktree: %w", err)
	}
	if err := sessions.SetWorktree(sessionID, wt.Path, wt.Branch); err != nil {
		_ = project.Remove(context.Background(), wt.RepoRoot, wt.Path, wt.Branch)
		return launchDir, nil, "", fmt.Errorf("session worktree: binding meta: %w", err)
	}
	cleanup = worktreeCleanupFunc(cfg, wt.Path, wt.Branch, wt.RepoRoot)
	return wt.Path, cleanup, "", nil
}

func worktreeCleanupFunc(cfg config.Config, path, branch, repoRoot string) func() error {
	if project.NormalizeCleanup(cfg.Session.WorktreeCleanup) != project.CleanupDelete {
		return nil
	}
	return func() error {
		return project.Remove(context.Background(), repoRoot, path, branch)
	}
}

// resumeOpened is the product of openResumeSession.
type resumeOpened struct {
	id     string
	bound  session.Bound
	replay []protocol.Event
}

// openResumeSession opens an existing root session, binds it, and loads the
// event log for engine.Restore + TUI replay. Child (subagent) sessions are
// rejected — resume is for root transcripts only.
func openResumeSession(sessions *session.Manager, id string) (resumeOpened, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return resumeOpened{}, fmt.Errorf("session: id is empty")
	}
	info, err := sessions.Get(id)
	if err != nil {
		return resumeOpened{}, fmt.Errorf("session: %w", err)
	}
	if info.ParentSessionID != "" {
		return resumeOpened{}, fmt.Errorf("session %q is a subagent transcript; resume a root session", id)
	}
	if _, err := sessions.Open(id); err != nil {
		return resumeOpened{}, fmt.Errorf("session: opening: %w", err)
	}
	bound, err := sessions.Bind(id)
	if err != nil {
		_ = sessions.Close(id)
		return resumeOpened{}, fmt.Errorf("session: binding: %w", err)
	}
	replay, err := sessions.Replay(id)
	if err != nil {
		_ = bound.Close()
		return resumeOpened{}, fmt.Errorf("session: replaying: %w", err)
	}
	return resumeOpened{id: id, bound: bound, replay: replay}, nil
}

// run is the interactive composition root: assemble backend, start a multi-root
// hub (concurrent parent engines), and hand host.Services to the TUI. Spawn /
// Activate keep additional roots live in-process. PendingResume remains a
// fallback when Roots cannot open a past session (should be rare).
func run(opts cliOptions, stdout, stderr io.Writer) (runErr error) {
	if err := maybeLaunchInsideContainer(opts, stderr); err != nil {
		return err
	}
	// If launch-inside re-exec'd successfully it never returns; if skipped, continue.
	warnedDangerous := false
	for {
		a, err := assemble(opts, false)
		if err != nil {
			return err
		}
		if !warnedDangerous {
			writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)
			warnedDangerous = true
		}

		hub := newMultiRootHub(a.firstSlot, a.spawnRoot, a.services.Files, a.services.Shell)
		a.services.Roots = hub
		// Hub owns firstSlot bind + worktree cleanup and engine Run/tee.
		hubOwned := true
		closeAssembled := func() {
			if hubOwned {
				if cerr := hub.Close(); cerr != nil && runErr == nil {
					runErr = fmt.Errorf("closing session hub: %w", cerr)
				}
				hubOwned = false
			} else {
				if cerr := a.store.Close(); cerr != nil && runErr == nil {
					runErr = fmt.Errorf("closing session store: %w", cerr)
				}
				if a.worktreeClose != nil {
					if err := a.worktreeClose(); err != nil && runErr == nil {
						runErr = fmt.Errorf("removing session worktree: %w", err)
					}
				}
			}
			if a.sessions != nil {
				_ = a.sessions.CloseAll()
			}
			if a.mcpClose != nil {
				if err := a.mcpClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing mcp servers: %w", err)
				}
			}
			if a.lspClose != nil {
				if err := a.lspClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing language servers: %w", err)
				}
			}
			if a.harnessClose != nil {
				if err := a.harnessClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing harness workers: %w", err)
				}
			}
			if a.schedulerClose != nil {
				a.schedulerClose()
			}
			if a.plansClose != nil {
				if err := a.plansClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing project plans: %w", err)
				}
			}
			if a.artifactsClose != nil {
				if err := a.artifactsClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing project artifacts: %w", err)
				}
			}
			if a.ledgerClose != nil {
				if err := a.ledgerClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing project ledger: %w", err)
				}
			}
			if a.goalsClose != nil {
				if err := a.goalsClose(); err != nil && runErr == nil {
					runErr = fmt.Errorf("closing project goals: %w", err)
				}
			}
			if err := a.issuesClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project issues: %w", err)
			}
			if err := a.memoryClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project memory: %w", err)
			}
			if err := a.historyClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing prompt history: %w", err)
			}
		}

		var pendingResume string
		var pendingUpgrade bool
		sessionPath := a.store.Path()

		restore := tui.EnableEnhancedKeys(stdout)
		vimMode := tui.VimModePane
		if mode, ok := tui.ParseVimMode(a.cfg.VimMode); ok {
			vimMode = mode
		}
		nanoMode := tui.VimModePane
		if mode, ok := tui.ParseNanoMode(a.cfg.NanoMode); ok {
			nanoMode = mode
		}
		mdReadMode := tui.PresentationEmbedded
		if mode, ok := tui.ParseSurfacePresentation(a.cfg.MdReadMode); ok {
			mdReadMode = mode
		}
		notifyMode := tui.NotifyUnfocusedOnly
		if mode, ok := tui.ParseNotifyMode(a.cfg.Notify); ok {
			notifyMode = mode
		}
		themeID := theme.BuiltinID
		var themePtr *theme.Theme
		if a.cfg.Theme != "" {
			if entry, ok := theme.Lookup(theme.Catalog(a.workDir), a.cfg.Theme); ok {
				th := entry.Theme
				themePtr = &th
				themeID = entry.ID
			}
		}
		// Alt screen, mouse cell motion, and focus reporting are declared on
		// Model.View (Bubble Tea v2). Mouse cell motion keeps chrome from being
		// natively selected; the TUI owns drag-highlight in transcript/prompt.
		isolation := resolveIsolationPosture(opts, a)
		// Inject real posture at launch (E12.7); do not infer from /.dockerenv.
		_ = os.Setenv(protocol.IsolationEnvKey, isolation)
		// Persist posture on the session for E3 reproducibility.
		_ = a.store.Append(protocol.SessionMeta{Isolation: isolation})
		program := tea.NewProgram(tui.New(hub.Ops(), hub.Events(), a.services, tui.Options{
			DangerouslySkipPermissions:   opts.dangerouslySkipPermissions,
			Theme:                        themePtr,
			ThemeID:                      themeID,
			SessionID:                    a.sessionID,
			WorkDir:                      a.workDir,
			StartupAlert:                 a.worktreeNotice,
			VimMode:                      vimMode,
			NanoMode:                     nanoMode,
			MdReadMode:                   mdReadMode,
			NotifyMode:                   notifyMode,
			CheckUpdate:                  autoupdateCheckFn(a.cfg.Autoupdate),
			SandboxMode:                  a.sandboxMode,
			SandboxBackend:               sandbox.BackendName(),
			SandboxAvailable:             sandbox.Available(),
			SandboxExplain:               a.sandboxExplain,
			Isolation:                    isolation,
			PermissionAutoApproveSeconds: a.cfg.PermissionAutoApproveSeconds,
			PermissionAutoApproveExclude: a.cfg.PermissionAutoApproveExclude,
			Replay:                       a.replay,
			Keybinds:                     config.KeybindsMap(a.cfg.Keybinds),
			Telemetry:                    opts.telemetry,
			Timeline:                     timelineOptionsFromConfig(a.cfg, a.sessionID),
			TimelineSet:                  true,
		}), tea.WithOutput(stdout), tea.WithInput(tui.WrapInput(os.Stdin)))
		final, runProgErr := program.Run()
		restore()
		if m, ok := final.(tui.Model); ok {
			pendingResume = m.PendingResume()
			pendingUpgrade = m.PendingUpgrade()
		}
		err = runProgErr
		closeAssembled()
		if err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if pendingUpgrade {
			// Alt screen is gone; run self-update and re-exec. Session JSONL under
			// ~/.strike is never touched.
			_, uerr := update.Upgrade(context.Background(), update.Options{Stdout: stdout})
			return uerr
		}
		if pendingResume == "" {
			fmt.Fprintln(stdout, "session log:", sessionPath)
			return nil
		}
		// Fallback restart when in-process Open was not used.
		opts.continueSession = false
		opts.sessionID = pendingResume
	}
}

// runExec is the headless one-shot composition root: same engine and session
// log as the TUI, but streams assistant text (or JSON envelopes) to stdout and
// exits after one turn.
func runExec(opts cliOptions, prompt string, format execOutputFormat, stdout, stderr io.Writer, extra ...headlessExtra) error {
	var ex headlessExtra
	if len(extra) > 0 {
		ex = extra[0]
	}
	return runExecContext(context.Background(), opts, prompt, format, stdout, stderr, ex)
}

// runExecContext is runExec with a cancelable parent context (MCP host disconnect,
// server shutdown). Cancel interrupts the in-flight turn.
func runExecContext(ctx context.Context, opts cliOptions, prompt string, format execOutputFormat, stdout, stderr io.Writer, extra ...headlessExtra) (runErr error) {
	var ex headlessExtra
	if len(extra) > 0 {
		ex = extra[0]
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a, err := assemble(opts, true)
	if err != nil {
		return err
	}
	if a.worktreeNotice != "" {
		fmt.Fprintln(stderr, a.worktreeNotice)
	}
	defer func() {
		if a.mcpClose != nil {
			if err := a.mcpClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing mcp servers: %w", err)
			}
		}
		if a.lspClose != nil {
			if err := a.lspClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing language servers: %w", err)
			}
		}
		if a.harnessClose != nil {
			if err := a.harnessClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing harness workers: %w", err)
			}
		}
		if a.schedulerClose != nil {
			a.schedulerClose()
		}
		if a.worktreeClose != nil {
			if err := a.worktreeClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("removing session worktree: %w", err)
			}
		}
		if a.plansClose != nil {
			if err := a.plansClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project plans: %w", err)
			}
		}
		if a.artifactsClose != nil {
			if err := a.artifactsClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project artifacts: %w", err)
			}
		}
		if a.ledgerClose != nil {
			if err := a.ledgerClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project ledger: %w", err)
			}
		}
		if a.goalsClose != nil {
			if err := a.goalsClose(); err != nil && runErr == nil {
				runErr = fmt.Errorf("closing project goals: %w", err)
			}
		}
		if err := a.issuesClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project issues: %w", err)
		}
		if err := a.memoryClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing project memory: %w", err)
		}
		if err := a.historyClose(); err != nil && runErr == nil {
			runErr = fmt.Errorf("closing prompt history: %w", err)
		}
	}()

	storeOwned := false
	defer func() {
		if !storeOwned {
			if cerr := a.store.Close(); cerr != nil && runErr == nil {
				runErr = fmt.Errorf("closing session store: %w", cerr)
			}
		}
	}()

	writeDangerousPermissionsWarning(stderr, opts.dangerouslySkipPermissions)

	storeOwned = true
	hopts := headlessOpts{
		Format:          format,
		SessionID:       a.sessionID,
		ApprovalControl: ex.ApprovalControl,
		ApprovalTimeout: ex.ApprovalTimeout,
	}
	return runSession(ctx, a.eng.Run, a.eng.Events(), bindAudit(a.store, a.cfg), func(events <-chan protocol.Event) error {
		return runHeadlessFrontend(ctx, a.eng.Ops(), events, prompt, stdout, stderr, hopts)
	})
}

// timelineOptionsFromConfig maps session.* timeline/trace knobs (#810) onto
// pkg/timeline.Options for the live TUI builder.
func timelineOptionsFromConfig(cfg config.Config, sessionID string) timeline.Options {
	opts := timeline.Options{SessionID: sessionID}
	if cfg.Session.TimelineMaxEntries != 0 {
		opts.MaxEntries = cfg.Session.TimelineMaxEntries
	}
	if cfg.Session.TimelineArgsPreviewMax > 0 {
		opts.ArgsPreviewMax = cfg.Session.TimelineArgsPreviewMax
	}
	if cfg.Session.TimelineOutputPreviewMax > 0 {
		opts.OutputPreviewMax = cfg.Session.TimelineOutputPreviewMax
	}
	if cfg.Session.TimelineBlobSpill {
		// Traces root only — TUI expands to <root>/<sessionID>/blobs on build/reset
		// so root switches do not keep a stale session path.
		opts.BlobDir = session.DefaultTracesDir()
	}
	return opts
}

// autoupdateCheckFn returns a TUI CheckUpdate callback for config.autoupdate.
// off → nil (no probe). notify|auto (default notify) → StartupProbe with a
// short timeout; failures stay silent.
func autoupdateCheckFn(rawMode string) func(context.Context) string {
	mode := config.EffectiveAutoupdate(rawMode)
	if !update.ShouldProbe(mode) {
		return nil
	}
	return func(ctx context.Context) string {
		res, err := update.StartupProbe(ctx, update.ProbeOptions{Mode: mode})
		if err != nil || res.Skipped {
			return ""
		}
		return res.Message
	}
}

// resolveIsolationPosture picks the E12.7 ladder label for this process.
// Prefers an existing STRIKE_ISOLATION (container launch) over recomputing.
func resolveIsolationPosture(opts cliOptions, a *assembled) string {
	if p, ok := protocol.ParseIsolationEnv(os.Getenv(protocol.IsolationEnvKey)); ok {
		return p
	}
	perm := protocol.PermissionModeDefault
	if opts.dangerouslySkipPermissions {
		perm = protocol.PermissionModeYolo
	} else if a != nil && a.cfg.PermissionMode != "" {
		perm = a.cfg.PermissionMode.Normalize()
	}
	sb := ""
	if a != nil {
		sb = a.sandboxMode
	}
	return protocol.ComputeIsolation(false, false, perm, sb)
}
