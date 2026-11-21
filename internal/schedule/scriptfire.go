package schedule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/jonathanung/strike/pkg/redact"
)

// maxRunPreview bounds Run.OutputPreview: the scrubbed last 4 KiB.
const maxRunPreview = 4 << 10

// errScriptUnavailable is recorded on a due script schedule when the
// process has no script runner (attach-only, tests).
var errScriptUnavailable = errors.New("script runs unavailable")

// ScriptRequest is one script fire handed to Hooks.Script. Dir is resolved
// (explicit dir, else the workspace path, else the daemon cwd) and exists
// at hand-off. PermissionMode is already resolved through
// rootstart.ResolveMode. The runner never sees a session.
type ScriptRequest struct {
	ScheduleID     string
	RunID          string
	Name           string
	Command        string
	CommandHash    string
	Dir            string
	Timeout        time.Duration
	SecretRefs     []string
	WorkspaceID    string
	PermissionMode string
}

// ScriptStart is what the runner reports right before it execs: the
// permission decision and the sandbox it applied.
type ScriptStart struct {
	Decision  string
	Sandbox   string
	StartedAt time.Time
}

// ScriptResult is a script run's outcome. Status is StatusFinished,
// StatusFailed, StatusDenied, or StatusError. ExitCode is nil when no
// process started. Output is the raw combined tail; the clock scrubs it
// before it is stored or audited.
type ScriptResult struct {
	Status    string
	Decision  string
	Sandbox   string
	StartedAt time.Time
	EndedAt   time.Time
	ExitCode  *int
	Output    []byte
	Error     string
}

// ScriptRunner runs one script fire to completion. It must call started
// once, immediately before the process starts, and not at all when the
// run is refused before exec. ctx is the daemon context: cancelling it
// kills the process.
type ScriptRunner func(ctx context.Context, req ScriptRequest, started func(ScriptStart)) ScriptResult

// ScriptAudit is one script_run audit record as the clock sees it. The
// wiring maps it onto audit.ScriptRunPayload so this package stays free of
// the audit import; Command, Dir, OutputTail and Error are scrubbed there.
type ScriptAudit struct {
	ScheduleID  string
	RunID       string
	Status      string
	Decision    string
	Mode        string
	Sandbox     string
	Command     string
	CommandHash string
	Dir         string
	StartedAt   time.Time
	EndedAt     time.Time
	ExitCode    *int
	Timeout     string
	OutputTail  string
	Error       string
}

// scriptRunning reports whether id is in the in-flight set.
func (s *Service) scriptRunning(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running[id]
}

// claimScript adds id to the in-flight set; false when it is already there.
func (s *Service) claimScript(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running[id] {
		return false
	}
	s.running[id] = true
	return true
}

func (s *Service) releaseScript(id string) {
	s.mu.Lock()
	delete(s.running, id)
	s.mu.Unlock()
}

// scriptContext is the context a detached script run lives in: the daemon
// loop's context once Run has started, else req with its cancellation cut
// so an HTTP handler returning does not kill the process.
func (s *Service) scriptContext(req context.Context) context.Context {
	s.mu.Lock()
	daemon := s.daemonCtx
	s.mu.Unlock()
	if daemon != nil {
		return daemon
	}
	return context.WithoutCancel(req)
}

// WaitScripts blocks until every detached script run has recorded its
// outcome. Shutdown and tests use it.
func (s *Service) WaitScripts() {
	s.scripts.Wait()
}

// startScript records a planned script fire as started and detaches the
// run. A missing runner or working directory is recorded as an error run
// with no fire. ErrBusy when the id is already in flight.
func (s *Service) startScript(ctx context.Context, sch Schedule, res tickResult, now time.Time) (Run, error) {
	if s.hooks.Script == nil {
		failStart(&res, errScriptUnavailable.Error())
		run, err := s.recordScript(sch, res, now)
		if err != nil {
			return Run{}, err
		}
		return run, ErrUnavailable
	}
	req := *res.Script
	if req.Dir == "" {
		req.Dir = s.dirFor(sch.WorkspaceID)
	}
	if err := checkScriptDir(req.Dir); err != nil {
		failStart(&res, err.Error())
		run, rerr := s.recordScript(sch, res, now)
		if rerr != nil {
			return Run{}, rerr
		}
		return run, fmt.Errorf("%w: %s", ErrInvalid, err.Error())
	}
	if !s.claimScript(sch.ID) {
		return Run{}, ErrBusy
	}
	req.RunID = newID("run_")
	res.Run.ID = req.RunID
	saved, err := s.store.Mutate(sch.ID, func(cur *Schedule) error {
		applyResult(cur, res, now)
		return nil
	})
	if err != nil {
		s.releaseScript(sch.ID)
		return Run{}, err
	}
	s.scripts.Add(1)
	go func() {
		defer s.scripts.Done()
		defer s.releaseScript(sch.ID)
		s.runScript(ctx, saved, req)
	}()
	return saved.Runs[0], nil
}

// failStart turns a planned start into an error result with no fire.
func failStart(res *tickResult, errText string) {
	res.Action = "error"
	res.LastStatus = StatusError
	res.LastError = errText
	res.Script = nil
	if res.Run != nil {
		res.Run.Status = StatusError
		res.Run.Error = errText
	}
}

// checkScriptDir re-checks the working directory at fire time.
func checkScriptDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("dir: no working directory")
	}
	info, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("dir: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("dir: %s is not a directory", dir)
	}
	return nil
}

// recordScript persists a non-start result for a script schedule (error,
// skipped, missed, quiet) and writes its audit record.
func (s *Service) recordScript(sch Schedule, res tickResult, now time.Time) (Run, error) {
	if res.Run != nil && res.Run.ID == "" {
		res.Run.ID = newID("run_")
	}
	saved, err := s.store.Mutate(sch.ID, func(cur *Schedule) error {
		applyResult(cur, res, now)
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	if res.Run == nil || res.Action == "none" || res.Action == "queue" {
		return Run{}, nil
	}
	run := *res.Run
	s.audit(sch, ScriptAudit{RunID: run.ID, Status: run.Status, Mode: run.PermissionMode, Error: run.Error})
	if len(saved.Runs) > 0 {
		return saved.Runs[0], nil
	}
	return run, nil
}

// runScript drives one detached run: the runner reports started, then the
// outcome is written onto the run record, the sidecar, and the audit log.
func (s *Service) runScript(ctx context.Context, sch Schedule, req ScriptRequest) {
	started := func(st ScriptStart) {
		_, _ = s.store.Mutate(sch.ID, func(cur *Schedule) error {
			if run := findRun(cur, req.RunID); run != nil {
				run.StartedAt = st.StartedAt
				run.Decision = st.Decision
			}
			return nil
		})
		s.audit(sch, ScriptAudit{
			RunID: req.RunID, Status: StatusStarted, Decision: st.Decision, Mode: req.PermissionMode,
			Sandbox: st.Sandbox, Dir: req.Dir, StartedAt: st.StartedAt,
		})
	}
	out := s.callRunner(ctx, req, started)
	s.finishScript(sch, req, out)
}

// callRunner runs the hook and turns a runner panic into an error outcome
// so the in-flight entry is always released and the run always recorded.
func (s *Service) callRunner(ctx context.Context, req ScriptRequest, started func(ScriptStart)) (out ScriptResult) {
	defer func() {
		if r := recover(); r != nil {
			out = ScriptResult{Status: StatusError, Error: fmt.Sprintf("script runner failed: %v", r)}
		}
	}()
	return s.hooks.Script(ctx, req, started)
}

func (s *Service) finishScript(sch Schedule, req ScriptRequest, out ScriptResult) {
	switch out.Status {
	case StatusFinished, StatusFailed, StatusDenied, StatusError:
	default:
		out.Error = fmt.Sprintf("script runner returned status %q", out.Status)
		out.Status = StatusError
	}
	now := s.now()
	if out.EndedAt.IsZero() && !out.StartedAt.IsZero() {
		out.EndedAt = now
	}
	preview := tailOutput(redact.ScrubToolOutput(string(out.Output)), maxRunPreview)
	_, _ = s.store.Mutate(sch.ID, func(cur *Schedule) error {
		cur.LastStatus = out.Status
		cur.LastError = out.Error
		cur.UpdatedAt = now
		run := findRun(cur, req.RunID)
		if run == nil {
			// The run fell off the 50-run cap while it was in flight.
			return nil
		}
		run.Status = out.Status
		run.Error = out.Error
		if out.Decision != "" {
			run.Decision = out.Decision
		}
		if !out.StartedAt.IsZero() {
			run.StartedAt = out.StartedAt
		}
		run.EndedAt = out.EndedAt
		run.ExitCode = cloneInt(out.ExitCode)
		run.OutputPreview = preview
		if len(out.Output) > 0 {
			if ref, err := s.store.WriteRunOutput(sch.ID, req.RunID, out.Output); err == nil {
				run.OutputRef = ref
			}
		}
		return nil
	})
	s.audit(sch, ScriptAudit{
		RunID: req.RunID, Status: out.Status, Decision: out.Decision, Mode: req.PermissionMode,
		Sandbox: out.Sandbox, Dir: req.Dir, StartedAt: out.StartedAt, EndedAt: out.EndedAt,
		ExitCode: cloneInt(out.ExitCode), OutputTail: preview, Error: out.Error,
	})
}

// audit fills the schedule-level fields and hands rec to Hooks.Audit.
func (s *Service) audit(sch Schedule, rec ScriptAudit) {
	if s.hooks.Audit == nil {
		return
	}
	rec.ScheduleID = sch.ID
	rec.Command = sch.Command
	rec.CommandHash = sch.CommandHash
	rec.Timeout = sch.Timeout
	if rec.Dir == "" {
		rec.Dir = sch.Dir
	}
	s.hooks.Audit(rec)
}

func findRun(sch *Schedule, runID string) *Run {
	for i := range sch.Runs {
		if sch.Runs[i].ID == runID {
			return &sch.Runs[i]
		}
	}
	return nil
}

func cloneInt(p *int) *int {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}
