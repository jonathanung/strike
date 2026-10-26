package schedule

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/rootstart"
)

// Hooks are the daemon's view of the rest of the process. Nil funcs are
// skipped: a nil Starter means fires are recorded as unavailable, a nil
// Goal hook rejects goal schedules at create time.
type Hooks struct {
	Now           func() time.Time
	Busy          func(sessionID string) bool
	Fingerprint   func(dir string) (string, error)
	WorkDir       func(workspaceID string) string
	WorkspaceMode func(workspaceID string) (mode string, ok bool)
	Goal          func(id string) (GoalRef, bool, error)
	Workflow      func(name string) (found, valid bool)
	Starter       rootstart.Starter
}

// Service is the clock. Open reconciles runs that were in flight when the
// process died. Run ticks every 20s; tests call Tick directly.
type Service struct {
	store      *Store
	hooks      Hooks
	defaultDir string

	mu      sync.Mutex
	firing  map[string]bool
	ticking bool
}

// Open loads the registry in dir. defaultDir is the fingerprint root when
// a schedule names no workspace.
func Open(dir string, hooks Hooks, defaultDir string) (*Service, error) {
	st, err := OpenStore(dir)
	if err != nil {
		return nil, err
	}
	if hooks.Fingerprint == nil {
		hooks.Fingerprint = Fingerprint
	}
	return &Service{store: st, hooks: hooks, defaultDir: defaultDir, firing: map[string]bool{}}, nil
}

func (s *Service) now() time.Time {
	if s.hooks.Now != nil {
		return s.hooks.Now()
	}
	return time.Now()
}

func (s *Service) List() []Schedule {
	return s.store.List()
}

func (s *Service) Get(id string) (Schedule, bool) {
	return s.store.Get(id)
}

func (s *Service) History(id string) ([]Run, error) {
	sch, ok := s.store.Get(id)
	if !ok {
		return nil, ErrNotFound
	}
	if sch.Runs == nil {
		return []Run{}, nil
	}
	return sch.Runs, nil
}

func (s *Service) Create(spec Spec) (Schedule, error) {
	if err := s.checkSource(spec); err != nil {
		return Schedule{}, err
	}
	now := s.now()
	sch, err := prepare(spec, now, newID("sch_"))
	if err != nil {
		return Schedule{}, err
	}
	sch.CreatedAt = now
	sch.UpdatedAt = now
	if err := s.store.Insert(sch); err != nil {
		return Schedule{}, err
	}
	return sch, nil
}

// Replace rewrites the spec and keeps history. The next fire is recomputed
// when the clock expression changes; a past next fire moves forward without
// catching up.
func (s *Service) Replace(id string, spec Spec) (Schedule, error) {
	cur, ok := s.store.Get(id)
	if !ok {
		return Schedule{}, ErrNotFound
	}
	if err := s.checkSource(spec); err != nil {
		return Schedule{}, err
	}
	now := s.now()
	next, err := prepare(spec, now, id)
	if err != nil {
		return Schedule{}, err
	}
	next.Runs = cur.Runs
	next.CreatedAt = cur.CreatedAt
	next.Fingerprint = cur.Fingerprint
	next.LastFire = cur.LastFire
	next.LastStatus = cur.LastStatus
	next.LastSessionID = cur.LastSessionID
	next.LastError = cur.LastError
	next.UpdatedAt = now
	if sameClock(cur, next) {
		next.NextFire = cur.NextFire
	}
	if next.Enabled && (next.NextFire.IsZero() || !next.NextFire.After(now)) && next.Kind != KindOnce {
		moved, err := nextStrictlyAfter(next, now)
		if err != nil {
			return Schedule{}, err
		}
		next.NextFire = moved
	}
	return s.store.Mutate(id, func(sch *Schedule) error {
		*sch = next
		return nil
	})
}

func sameClock(a, b Schedule) bool {
	return a.Kind == b.Kind && a.Cron == b.Cron && a.Every == b.Every && a.At.Equal(b.At) && a.Timezone == b.Timezone
}

func (s *Service) Delete(id string) error {
	return s.store.Delete(id)
}

func (s *Service) SetEnabled(id string, enabled bool) (Schedule, error) {
	now := s.now()
	return s.store.Mutate(id, func(sch *Schedule) error {
		sch.UpdatedAt = now
		if !enabled {
			sch.Enabled = false
			return nil
		}
		sch.Enabled = true
		if sch.Kind == KindOnce {
			if !sch.At.IsZero() && sch.At.After(now) {
				sch.NextFire = sch.At
				return nil
			}
			// A one-shot whose time has passed stays disarmed. Resume does
			// not start a second session, and it does not invent a fire at now.
			sch.Enabled = false
			sch.NextFire = time.Time{}
			return nil
		}
		if sch.NextFire.IsZero() || !sch.NextFire.After(now) {
			moved, err := nextStrictlyAfter(*sch, now)
			if err != nil {
				return err
			}
			if moved.IsZero() {
				sch.NextFire = now
			} else {
				sch.NextFire = moved
			}
		}
		return nil
	})
}

// RunNow starts one root immediately. Overlap skip while the previous
// session is still busy returns ErrBusy. The calendar is not moved.
func (s *Service) RunNow(ctx context.Context, id string) (Run, error) {
	sch, ok := s.store.Get(id)
	if !ok {
		return Run{}, ErrNotFound
	}
	if s.hooks.Starter == nil {
		return Run{}, ErrUnavailable
	}
	in := s.observe(sch)
	if in.Busy && sch.Overlap == OverlapSkip {
		return Run{}, ErrBusy
	}
	if !s.begin(id) {
		return Run{}, ErrBusy
	}
	defer s.end(id)
	res := planRunNow(sch, in)
	var startErr error
	if res.Action == "start" && res.Req != nil {
		out, err := s.hooks.Starter.StartRoot(ctx, *res.Req)
		startErr = err
		if res.Run != nil {
			res.Run.SessionID = out.SessionID
		}
		if err != nil {
			res.Action = "error"
			res.LastStatus = StatusError
			res.LastError = err.Error()
			if res.Run != nil {
				res.Run.Status = StatusError
				res.Run.Error = err.Error()
			}
		}
	}
	saved, err := s.store.Mutate(id, func(cur *Schedule) error {
		applyResult(cur, res, in.Now)
		return nil
	})
	if err != nil {
		return Run{}, err
	}
	if len(saved.Runs) == 0 {
		if startErr != nil {
			return Run{}, startErr
		}
		if res.LastError != "" {
			return Run{}, fmt.Errorf("%w: %s", ErrInvalid, res.LastError)
		}
		return Run{}, ErrInvalid
	}
	if saved.Runs[0].Status == StatusError && saved.Runs[0].Error != "" {
		if startErr != nil {
			return saved.Runs[0], startErr
		}
		return saved.Runs[0], fmt.Errorf("%w: %s", ErrInvalid, saved.Runs[0].Error)
	}
	return saved.Runs[0], nil
}

// Tick evaluates every schedule once. startup applies the missed-run policy
// to fires that came due while this process was down.
func (s *Service) Tick(ctx context.Context, startup bool) {
	s.mu.Lock()
	if s.ticking {
		s.mu.Unlock()
		return
	}
	s.ticking = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.ticking = false
		s.mu.Unlock()
	}()
	for _, sch := range s.store.List() {
		s.tickOne(ctx, sch.ID, startup)
	}
}

// Run is the daemon loop. It returns when ctx is cancelled. A nil starter
// does not tick: attach-only can still edit the registry.
func (s *Service) Run(ctx context.Context) {
	if s == nil || s.hooks.Starter == nil {
		return
	}
	s.Tick(ctx, true)
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Tick(ctx, false)
		}
	}
}

func (s *Service) tickOne(ctx context.Context, id string, startup bool) {
	sch, ok := s.store.Get(id)
	if !ok {
		return
	}
	in := s.observe(sch)
	in.Startup = startup
	idle := !in.Busy && startedRun(sch)
	res := planTick(sch, in)
	if res.Action == "none" && !idle {
		return
	}
	if res.Action == "start" {
		if s.hooks.Starter == nil {
			res.Action = "error"
			res.LastStatus = StatusError
			res.LastError = ErrUnavailable.Error()
			if res.Run != nil {
				res.Run.Status = StatusError
				res.Run.Error = res.LastError
			}
		} else if !s.begin(id) {
			return
		} else {
			defer s.end(id)
			out, err := s.hooks.Starter.StartRoot(ctx, *res.Req)
			if res.Run != nil {
				res.Run.SessionID = out.SessionID
			}
			if err != nil {
				res.Action = "error"
				res.LastStatus = StatusError
				res.LastError = err.Error()
				if res.Run != nil {
					res.Run.Status = StatusError
					res.Run.Error = err.Error()
				}
			}
		}
	}
	_, _ = s.store.Mutate(id, func(cur *Schedule) error {
		if idle {
			noteIdle(cur)
		}
		if res.Action != "none" {
			applyResult(cur, res, in.Now)
		}
		return nil
	})
}

func (s *Service) observe(sch Schedule) tickInput {
	in := tickInput{Now: s.now()}
	if sch.LastSessionID != "" && s.hooks.Busy != nil {
		in.Busy = s.hooks.Busy(sch.LastSessionID)
	}
	if sch.WorkspaceID != "" && s.hooks.WorkspaceMode != nil {
		if mode, ok := s.hooks.WorkspaceMode(sch.WorkspaceID); ok {
			in.WorkspaceMode = mode
		}
	}
	if sch.Kind == KindHeartbeat {
		fp, err := s.hooks.Fingerprint(s.dirFor(sch.WorkspaceID))
		in.Fingerprint, in.FingerprintErr = fp, err
	}
	switch sch.Source {
	case SourceGoal:
		if s.hooks.Goal == nil {
			in.GoalErr = invalid("goals are unavailable")
			break
		}
		g, ok, err := s.hooks.Goal(sch.GoalID)
		in.GoalErr = err
		if err == nil && ok {
			in.Goal = &g
		}
	case SourceWorkflow:
		if s.hooks.Workflow == nil {
			break
		}
		found, valid := s.hooks.Workflow(sch.Workflow)
		in.WorkflowChecked = true
		in.WorkflowFound = found
		in.WorkflowValid = valid
	}
	return in
}

func (s *Service) dirFor(workspaceID string) string {
	if s.hooks.WorkDir != nil {
		if d := strings.TrimSpace(s.hooks.WorkDir(workspaceID)); d != "" {
			return d
		}
	}
	return s.defaultDir
}

func (s *Service) checkSource(spec Spec) error {
	switch strings.ToLower(strings.TrimSpace(spec.Source)) {
	case SourceGoal:
		if s.hooks.Goal == nil {
			return invalid("goals are unavailable")
		}
		_, ok, err := s.hooks.Goal(strings.TrimSpace(spec.GoalID))
		if err != nil {
			return err
		}
		if !ok {
			return invalid("goal %q not found", strings.TrimSpace(spec.GoalID))
		}
	case SourceWorkflow:
		if s.hooks.Workflow == nil {
			return invalid("workflows are unavailable")
		}
		found, valid := s.hooks.Workflow(strings.TrimSpace(spec.Workflow))
		if !found {
			return invalid("workflow %q not found", strings.TrimSpace(spec.Workflow))
		}
		if !valid {
			return invalid("workflow %q is invalid", strings.TrimSpace(spec.Workflow))
		}
	}
	return nil
}

func (s *Service) begin(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firing[id] {
		return false
	}
	s.firing[id] = true
	return true
}

func (s *Service) end(id string) {
	s.mu.Lock()
	delete(s.firing, id)
	s.mu.Unlock()
}

func startedRun(sch Schedule) bool {
	if sch.LastStatus == StatusStarted {
		return true
	}
	for _, run := range sch.Runs {
		if run.Status == StatusStarted {
			return true
		}
	}
	return false
}

func noteIdle(sch *Schedule) {
	if sch.LastStatus == StatusStarted {
		sch.LastStatus = StatusFinished
	}
	for i := range sch.Runs {
		if sch.Runs[i].Status == StatusStarted {
			sch.Runs[i].Status = StatusFinished
		}
	}
}

func applyResult(sch *Schedule, res tickResult, now time.Time) {
	if res.ClearNext {
		sch.NextFire = time.Time{}
	} else if !res.NextFire.IsZero() {
		sch.NextFire = res.NextFire
	}
	sch.Enabled = res.Enabled
	if res.Action == "quiet" || res.Action == "start" || res.Action == "missed" {
		sch.Fingerprint = res.Fingerprint
	}
	if res.Action == "none" {
		return
	}
	if res.LastStatus != "" {
		sch.LastStatus = res.LastStatus
	}
	sch.LastError = res.LastError
	if res.Action != "queue" {
		sch.LastFire = now
	}
	if res.Run != nil && res.Action != "queue" {
		run := *res.Run
		if run.ID == "" {
			run.ID = newID("run_")
		}
		if run.ScheduleID == "" {
			run.ScheduleID = sch.ID
		}
		addRun(sch, run)
		if run.SessionID != "" {
			sch.LastSessionID = run.SessionID
		}
	}
	sch.UpdatedAt = now
}
