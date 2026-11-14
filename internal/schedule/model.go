// Package schedule is the strike serve clock: persisted cron, interval,
// one-shot, and heartbeat definitions that start a root session.
//
// The session itself is opened through rootstart.Starter so triggers can
// share that path. This package does not spawn engines.
package schedule

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike/harness/secretref"
	"github.com/jonathanung/strike/internal/rootstart"
)

const (
	KindCron      = "cron"
	KindInterval  = "interval"
	KindOnce      = "once"
	KindHeartbeat = "heartbeat"

	OverlapSkip  = "skip"
	OverlapQueue = "queue"
	OverlapAllow = "allow"

	MissedSkip = "skip"
	MissedOnce = "once"

	SourcePrompt   = "prompt"
	SourceGoal     = "goal"
	SourceWorkflow = "workflow"
	// SourceScript runs a shell command with no agent session. It is an
	// explicit opt-in: Replace never converts to or from it, and the fire
	// path refuses an entry that was not armed (see docs/design/script-schedules.md).
	SourceScript = "script"

	StatusStarted     = "started"
	StatusFinished    = "finished"
	StatusQuiet       = "quiet"
	StatusSkipped     = "skipped"
	StatusMissed      = "missed"
	StatusError       = "error"
	StatusInterrupted = "interrupted"
	StatusQueued      = "queued"
	// StatusFailed is a script run that exited non-zero or timed out.
	StatusFailed = "failed"
	// StatusDenied is a script run the permission layer or a hook refused.
	StatusDenied = "denied"

	// Decision values record what the permission layer said about a script run.
	DecisionAllow     = "allow"
	DecisionArmed     = "armed"
	DecisionDeny      = "deny"
	DecisionHookBlock = "hook_block"

	maxSchedules = 100
	maxRuns      = 50
	maxName      = 80
	maxPrompt    = 16 << 10
	maxCommand   = 16 << 10
	minEvery     = time.Minute
	maxEvery     = 366 * 24 * time.Hour

	defaultScriptTimeout = "30m"
	minScriptTimeout     = 10 * time.Second
	maxScriptTimeout     = 6 * time.Hour
)

var (
	ErrNotFound    = errors.New("schedule not found")
	ErrInvalid     = errors.New("invalid schedule")
	ErrBusy        = errors.New("previous run still active")
	ErrUnavailable = errors.New("live session unavailable")
	ErrLimit       = errors.New("too many schedules")
	errConflict    = errors.New("schedule changed")
)

// Schedule is one persisted clock entry. Runs is newest-first and capped.
type Schedule struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Enabled        bool      `json:"enabled"`
	Kind           string    `json:"kind"`
	Cron           string    `json:"cron,omitempty"`
	Every          string    `json:"every,omitempty"`
	At             time.Time `json:"at,omitempty"`
	Timezone       string    `json:"timezone"`
	Source         string    `json:"source"`
	Prompt         string    `json:"prompt,omitempty"`
	GoalID         string    `json:"goalId,omitempty"`
	Workflow       string    `json:"workflow,omitempty"`
	WorkspaceID    string    `json:"workspaceId,omitempty"`
	PermissionMode string    `json:"permissionMode,omitempty"`
	Overlap        string    `json:"overlap"`
	Missed         string    `json:"missed"`
	// Script-only payload. Command is stored verbatim (scrubbing happens on
	// read-side projections); Timeout is a Go duration string; SecretRefs
	// are canonical secret://env/NAME refs resolved only at exec. ArmedAt is
	// set by the cockpit's confirm step and CommandHash pins the command it
	// confirmed; a script schedule with a zero ArmedAt or a stale hash never fires.
	Command       string    `json:"command,omitempty"`
	Dir           string    `json:"dir,omitempty"`
	Timeout       string    `json:"timeout,omitempty"`
	SecretRefs    []string  `json:"secretRefs,omitempty"`
	ArmedAt       time.Time `json:"armedAt,omitempty"`
	CommandHash   string    `json:"commandHash,omitempty"`
	NextFire      time.Time `json:"nextFire,omitempty"`
	LastFire      time.Time `json:"lastFire,omitempty"`
	LastStatus    string    `json:"lastStatus,omitempty"`
	LastSessionID string    `json:"lastSessionId,omitempty"`
	LastError     string    `json:"lastError,omitempty"`
	Fingerprint   string    `json:"fingerprint,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	Runs          []Run     `json:"runs,omitempty"`
}

// Run is one fire, quiet check, skip, or miss.
type Run struct {
	ID             string    `json:"id"`
	ScheduleID     string    `json:"scheduleId"`
	FiredAt        time.Time `json:"firedAt"`
	ScheduledFor   time.Time `json:"scheduledFor,omitempty"`
	Status         string    `json:"status"`
	SessionID      string    `json:"sessionId,omitempty"`
	PermissionMode string    `json:"permissionMode,omitempty"`
	Error          string    `json:"error,omitempty"`
	// Script-run fields. FiredAt stays the tick time; StartedAt/EndedAt are
	// the process wall clock. ExitCode is nil when no process started.
	// OutputPreview is the scrubbed last 4 KiB; OutputRef names the sidecar
	// (see Store.WriteRunOutput). Decision is one of the Decision* values.
	StartedAt     time.Time `json:"startedAt,omitempty"`
	EndedAt       time.Time `json:"endedAt,omitempty"`
	ExitCode      *int      `json:"exitCode,omitempty"`
	OutputPreview string    `json:"outputPreview,omitempty"`
	OutputRef     string    `json:"outputRef,omitempty"`
	Decision      string    `json:"decision,omitempty"`
}

// Spec is a create or replace body. Enabled nil means enabled.
type Spec struct {
	Name           string
	Kind           string
	Cron           string
	Every          string
	At             time.Time
	Timezone       string
	Source         string
	Prompt         string
	GoalID         string
	Workflow       string
	WorkspaceID    string
	PermissionMode string
	Overlap        string
	Missed         string
	Enabled        *bool
	// Script-only. CommandHash is derived from Command by prepare, never
	// supplied. ArmedAt is set by the caller that confirmed the command.
	Command    string
	Dir        string
	Timeout    string
	SecretRefs []string
	ArmedAt    time.Time
}

// HashCommand is the sha256 hex pinned in Schedule.CommandHash at arming time.
func HashCommand(command string) string {
	sum := sha256.Sum256([]byte(command))
	return hex.EncodeToString(sum[:])
}

// GoalRef is the slice of a goal the clock needs to phrase a prompt.
type GoalRef struct {
	ID          string
	Description string
	Criteria    []string
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

func prepare(spec Spec, now time.Time, id string) (Schedule, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return Schedule{}, invalid("name is required")
	}
	if utf8.RuneCountInString(name) > maxName {
		return Schedule{}, invalid("name is too long")
	}
	kind := strings.ToLower(strings.TrimSpace(spec.Kind))
	switch kind {
	case KindCron, KindInterval, KindOnce, KindHeartbeat:
	default:
		return Schedule{}, invalid("kind must be cron, interval, once, or heartbeat")
	}
	tz := strings.TrimSpace(spec.Timezone)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := loadLoc(tz); err != nil {
		return Schedule{}, invalid("%s", err.Error())
	}
	source := strings.ToLower(strings.TrimSpace(spec.Source))
	switch source {
	case SourcePrompt, SourceGoal, SourceWorkflow, SourceScript:
	default:
		return Schedule{}, invalid("source must be prompt, goal, workflow, or script")
	}
	prompt := strings.TrimSpace(spec.Prompt)
	if utf8.RuneCountInString(prompt) > maxPrompt {
		return Schedule{}, invalid("prompt is too long")
	}
	goalID := strings.TrimSpace(spec.GoalID)
	workflow := strings.TrimSpace(spec.Workflow)
	var script scriptFields
	switch source {
	case SourcePrompt:
		if prompt == "" {
			return Schedule{}, invalid("prompt is required")
		}
	case SourceGoal:
		if goalID == "" {
			return Schedule{}, invalid("goalId is required")
		}
	case SourceWorkflow:
		if workflow == "" {
			return Schedule{}, invalid("workflow is required")
		}
	case SourceScript:
		if prompt != "" || goalID != "" || workflow != "" {
			return Schedule{}, invalid("script schedules take a command only; prompt, goalId, and workflow must be empty")
		}
		var err error
		if script, err = prepareScript(spec); err != nil {
			return Schedule{}, err
		}
	}
	if source != SourceScript && (strings.TrimSpace(spec.Command) != "" || strings.TrimSpace(spec.Dir) != "" ||
		strings.TrimSpace(spec.Timeout) != "" || len(spec.SecretRefs) > 0) {
		return Schedule{}, invalid("command, dir, timeout, and secretRefs apply to script schedules only")
	}
	overlap := strings.ToLower(strings.TrimSpace(spec.Overlap))
	if overlap == "" {
		overlap = OverlapSkip
	}
	switch overlap {
	case OverlapSkip:
	case OverlapQueue, OverlapAllow:
		if source == SourceScript {
			// Two instances of one script never run concurrently.
			return Schedule{}, invalid("overlap must be skip for script schedules")
		}
	default:
		return Schedule{}, invalid("overlap must be skip, queue, or allow")
	}
	missed := strings.ToLower(strings.TrimSpace(spec.Missed))
	if missed == "" {
		missed = MissedSkip
	}
	switch missed {
	case MissedSkip, MissedOnce:
	default:
		return Schedule{}, invalid("missed must be skip or once")
	}
	mode := strings.TrimSpace(spec.PermissionMode)
	if mode != "" {
		if _, err := rootstart.ResolveMode(mode, ""); err != nil {
			return Schedule{}, invalid("%s", err.Error())
		}
	}
	cronExpr := strings.TrimSpace(spec.Cron)
	every := strings.TrimSpace(spec.Every)
	switch kind {
	case KindCron:
		if _, err := parseCron(cronExpr); err != nil {
			return Schedule{}, invalid("%s", err.Error())
		}
	case KindInterval, KindHeartbeat:
		if _, err := parseEvery(every); err != nil {
			return Schedule{}, invalid("%s", err.Error())
		}
	case KindOnce:
		if spec.At.IsZero() {
			return Schedule{}, invalid("at is required")
		}
	}
	enabled := true
	if spec.Enabled != nil {
		enabled = *spec.Enabled
	}
	sch := Schedule{
		ID:             id,
		Name:           name,
		Enabled:        enabled,
		Kind:           kind,
		Cron:           cronExpr,
		Every:          every,
		At:             spec.At.UTC(),
		Timezone:       tz,
		Source:         source,
		Prompt:         prompt,
		GoalID:         goalID,
		Workflow:       workflow,
		WorkspaceID:    strings.TrimSpace(spec.WorkspaceID),
		PermissionMode: mode,
		Overlap:        overlap,
		Missed:         missed,
		Command:        script.command,
		Dir:            script.dir,
		Timeout:        script.timeout,
		SecretRefs:     script.secretRefs,
		ArmedAt:        script.armedAt,
		CommandHash:    script.commandHash,
	}
	next, err := initialNext(sch, now)
	if err != nil {
		return Schedule{}, err
	}
	sch.NextFire = next
	return sch, nil
}

// scriptFields is the validated script payload; zero for other sources.
type scriptFields struct {
	command     string
	dir         string
	timeout     string
	secretRefs  []string
	armedAt     time.Time
	commandHash string
}

func prepareScript(spec Spec) (scriptFields, error) {
	out := scriptFields{command: strings.TrimSpace(spec.Command)}
	if out.command == "" {
		return scriptFields{}, invalid("command is required")
	}
	if len(out.command) > maxCommand {
		return scriptFields{}, invalid("command is too long")
	}
	out.commandHash = HashCommand(out.command)
	out.timeout = strings.TrimSpace(spec.Timeout)
	if out.timeout == "" {
		out.timeout = defaultScriptTimeout
	}
	if _, err := parseScriptTimeout(out.timeout); err != nil {
		return scriptFields{}, invalid("%s", err.Error())
	}
	out.dir = strings.TrimSpace(spec.Dir)
	if out.dir != "" {
		if !filepath.IsAbs(out.dir) {
			return scriptFields{}, invalid("dir must be an absolute path")
		}
		info, err := os.Stat(out.dir)
		if err != nil {
			return scriptFields{}, invalid("dir: %s", err.Error())
		}
		if !info.IsDir() {
			return scriptFields{}, invalid("dir is not a directory")
		}
	}
	for _, raw := range spec.SecretRefs {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ref, ok := secretref.ParseRef(raw)
		if !ok {
			return scriptFields{}, invalid("secretRefs: %q is not a secret://env/NAME ref", raw)
		}
		out.secretRefs = append(out.secretRefs, ref.String())
	}
	if !spec.ArmedAt.IsZero() {
		out.armedAt = spec.ArmedAt.UTC()
	}
	return out, nil
}

// parseScriptTimeout bounds a script run: at least 10s, at most 6h.
func parseScriptTimeout(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("timeout: %w", err)
	}
	if d < minScriptTimeout {
		return 0, fmt.Errorf("timeout: minimum is 10s")
	}
	if d > maxScriptTimeout {
		return 0, fmt.Errorf("timeout: maximum is 6h")
	}
	return d, nil
}

func parseEvery(raw string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("every: %w", err)
	}
	if d < minEvery {
		return 0, fmt.Errorf("every: minimum is 1m")
	}
	if d > maxEvery {
		return 0, fmt.Errorf("every: maximum is 366 days")
	}
	return d, nil
}

func initialNext(sch Schedule, now time.Time) (time.Time, error) {
	switch sch.Kind {
	case KindOnce:
		return sch.At, nil
	case KindInterval, KindHeartbeat:
		every, err := parseEvery(sch.Every)
		if err != nil {
			return time.Time{}, invalid("%s", err.Error())
		}
		return now.Add(every), nil
	default:
		loc, err := loadLoc(sch.Timezone)
		if err != nil {
			return time.Time{}, invalid("%s", err.Error())
		}
		next, err := nextCron(sch.Cron, now, loc)
		if err != nil {
			return time.Time{}, invalid("%s", err.Error())
		}
		return next, nil
	}
}

// nextStrictlyAfter is the following slot after `after`. A zero time means
// the schedule has no further fire (a one-shot).
func nextStrictlyAfter(sch Schedule, after time.Time) (time.Time, error) {
	switch sch.Kind {
	case KindOnce:
		if sch.At.After(after) {
			return sch.At, nil
		}
		return time.Time{}, nil
	case KindInterval, KindHeartbeat:
		every, err := parseEvery(sch.Every)
		if err != nil {
			return time.Time{}, err
		}
		anchor := sch.NextFire
		if anchor.IsZero() || !anchor.After(after) {
			anchor = after
		} else {
			return anchor, nil
		}
		gaps := after.Sub(sch.NextFire) / every
		if sch.NextFire.IsZero() || gaps < 0 {
			return after.Add(every), nil
		}
		// First slot strictly after `after`, measured from the previous NextFire.
		next := sch.NextFire.Add(time.Duration(gaps+1) * every)
		if !next.After(after) {
			next = after.Add(every)
		}
		return next, nil
	default:
		loc, err := loadLoc(sch.Timezone)
		if err != nil {
			return time.Time{}, err
		}
		return nextCron(sch.Cron, after, loc)
	}
}

// dueCount reports how many slots are due at now: 0, 1, or 2.
// 2 means two or more.
func dueCount(sch Schedule, now time.Time) int {
	if !sch.Enabled || sch.NextFire.IsZero() || now.Before(sch.NextFire) {
		return 0
	}
	switch sch.Kind {
	case KindOnce:
		return 1
	case KindInterval, KindHeartbeat:
		every, err := parseEvery(sch.Every)
		if err != nil || every <= 0 {
			return 1
		}
		n := int(now.Sub(sch.NextFire)/every) + 1
		if n < 1 {
			return 1
		}
		if n > 2 {
			return 2
		}
		return n
	default:
		loc, err := loadLoc(sch.Timezone)
		if err != nil {
			return 1
		}
		nxt, err := nextCron(sch.Cron, sch.NextFire, loc)
		if err != nil {
			return 1
		}
		if nxt.After(now) {
			return 1
		}
		return 2
	}
}

func newID(prefix string) string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return prefix + hex.EncodeToString(b[:])
}

func cloneSchedule(s Schedule) Schedule {
	out := s
	if s.Runs != nil {
		out.Runs = append([]Run(nil), s.Runs...)
		for i := range out.Runs {
			if s.Runs[i].ExitCode != nil {
				code := *s.Runs[i].ExitCode
				out.Runs[i].ExitCode = &code
			}
		}
	}
	if s.SecretRefs != nil {
		out.SecretRefs = append([]string(nil), s.SecretRefs...)
	}
	return out
}

func addRun(sch *Schedule, run Run) {
	sch.Runs = append([]Run{run}, sch.Runs...)
	if len(sch.Runs) > maxRuns {
		sch.Runs = sch.Runs[:maxRuns]
	}
}
