// Package timeline builds a structured, queryable run timeline from protocol
// events and exports a versioned, secret-redacted machine-readable trace.
//
// Relationship to other surfaces:
//   - Session JSONL (internal/session) remains the durable full event log.
//     Timeline is a derived, collapsed view — not a second transcript.
//   - Agent roster / budget live fields (#774) stay on team.roster and TUI
//     panes; timeline owns harness-wide turn/tool/provider/child spans.
//   - Secret scrubbing uses pkg/redact (shared with markdown export and
//     engine inspect). Full secret-ref resolution is #796.
package timeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/redact"
)

// SchemaVersion is the versioned export document schema (not the Op/Event wire).
const SchemaVersion = "1.0.0"

// Entry kinds on the timeline.
const (
	KindTurn     = "turn"
	KindTool     = "tool"
	KindProvider = "provider"
	KindChild    = "child"
)

// Lifecycle states (queued → running → waiting → terminal).
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateWaiting   = "waiting"
	StateCompleted = "completed"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
)

// Default payload preview limits for expandable fields in exports.
const (
	DefaultArgsPreviewMax    = 512
	DefaultOutputPreviewMax  = 2048
	DefaultErrorPreviewMax   = 512
	DefaultCollapsedMaxLines = 200
)

// TimedEvent pairs a protocol event with its envelope (or observe) timestamp.
type TimedEvent struct {
	Time  time.Time
	Event protocol.Event
}

// Entry is one span on the structured run timeline.
type Entry struct {
	ID                string `json:"id"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	SessionID         string `json:"sessionId,omitempty"`
	TurnID            string `json:"turnId,omitempty"`
	ParentID          string `json:"parentId,omitempty"` // parent entry id when nested
	Name              string `json:"name,omitempty"`     // tool name, agent, …
	CallID            string `json:"callId,omitempty"`
	ProviderRequestID string `json:"providerRequestId,omitempty"`
	ChildSessionID    string `json:"childSessionId,omitempty"`
	Attempt           int    `json:"attempt,omitempty"`
	StartedAt         string `json:"startedAt,omitempty"` // RFC3339Nano UTC
	EndedAt           string `json:"endedAt,omitempty"`
	DurationMs        *int64 `json:"durationMs,omitempty"`
	StopReason        string `json:"stopReason,omitempty"`
	ChildStatus       string `json:"childStatus,omitempty"`
	Error             string `json:"error,omitempty"`
	// Token fields when known (from usage.reported).
	InputTokens         *int   `json:"inputTokens,omitempty"`
	OutputTokens        *int   `json:"outputTokens,omitempty"`
	CacheReadTokens     *int   `json:"cacheReadTokens,omitempty"`
	CacheCreationTokens *int   `json:"cacheCreationTokens,omitempty"`
	UsageSource         string `json:"usageSource,omitempty"`
	// Redacted, size-limited expandable payloads (never raw secrets).
	ArgsPreview   string `json:"argsPreview,omitempty"`
	OutputPreview string `json:"outputPreview,omitempty"`
}

// Summary rolls up a trace for quick inspection.
type Summary struct {
	Turns      int   `json:"turns"`
	Tools      int   `json:"tools"`
	Providers  int   `json:"providers"`
	Children   int   `json:"children"`
	Failed     int   `json:"failed"`
	Canceled   int   `json:"canceled"`
	InputTok   int64 `json:"inputTokens,omitempty"`
	OutputTok  int64 `json:"outputTokens,omitempty"`
	DurationMs int64 `json:"durationMs,omitempty"`
}

// Trace is the versioned machine-readable export document.
type Trace struct {
	SchemaVersion string    `json:"schemaVersion"`
	SessionID     string    `json:"sessionId,omitempty"`
	ExportedAt    time.Time `json:"exportedAt"`
	Redacted      bool      `json:"redacted"`
	// Note documents relationship to session JSONL and #774.
	Note     string   `json:"note,omitempty"`
	Summary  Summary  `json:"summary"`
	Entries  []Entry  `json:"entries"`
	Warnings []string `json:"warnings,omitempty"`
}

// Options configure build/export limits.
type Options struct {
	ArgsPreviewMax   int
	OutputPreviewMax int
	ErrorPreviewMax  int
	// SessionID pins the root session id on the export when known.
	SessionID string
	// Clock overrides time.Now for tests (export timestamp only).
	Clock func() time.Time
}

func (o Options) withDefaults() Options {
	if o.ArgsPreviewMax <= 0 {
		o.ArgsPreviewMax = DefaultArgsPreviewMax
	}
	if o.OutputPreviewMax <= 0 {
		o.OutputPreviewMax = DefaultOutputPreviewMax
	}
	if o.ErrorPreviewMax <= 0 {
		o.ErrorPreviewMax = DefaultErrorPreviewMax
	}
	if o.Clock == nil {
		o.Clock = func() time.Time { return time.Now().UTC() }
	}
	return o
}

// Builder folds protocol events into timeline entries. Safe for concurrent use.
type Builder struct {
	mu   sync.Mutex
	opts Options

	// indexes
	turns     map[string]*Entry // turnID
	tools     map[string]*Entry // sessionID\0callID
	providers map[string]*Entry // providerRequestID
	children  map[string]*Entry // child sessionID

	order []string // entry ids in first-seen order
	byID  map[string]*Entry

	sessionID string
	seq       int
}

// NewBuilder returns an empty concurrent-safe timeline builder.
func NewBuilder(opts Options) *Builder {
	opts = opts.withDefaults()
	return &Builder{
		opts:      opts,
		turns:     make(map[string]*Entry),
		tools:     make(map[string]*Entry),
		providers: make(map[string]*Entry),
		children:  make(map[string]*Entry),
		byID:      make(map[string]*Entry),
		sessionID: opts.SessionID,
	}
}

// Observe records one event at time t (typically the JSONL envelope time).
// Unknown event types are ignored. Zero t uses opts.Clock().
func (b *Builder) Observe(ev protocol.Event, t time.Time) {
	if ev == nil {
		return
	}
	if t.IsZero() {
		t = b.opts.Clock()
	}
	t = t.UTC()

	b.mu.Lock()
	defer b.mu.Unlock()

	switch e := ev.(type) {
	case protocol.TurnStarted:
		b.noteSession(e.SessionID)
		ent := b.ensureTurn(e.TurnID, e.SessionID, t)
		ent.State = StateRunning
		if ent.StartedAt == "" {
			ent.StartedAt = formatTime(t)
		}
	case protocol.TurnCompleted:
		b.noteSession(e.SessionID)
		ent := b.ensureTurn(e.TurnID, e.SessionID, t)
		if ent.State == StateRunning || ent.State == StateWaiting || ent.State == StateQueued {
			ent.State = StateCompleted
		}
		ent.StopReason = e.StopReason
		b.finish(ent, t)
	case protocol.EngineError:
		b.noteSession(e.SessionID)
		if e.TurnID != "" {
			ent := b.ensureTurn(e.TurnID, e.SessionID, t)
			ent.State = StateFailed
			ent.Error = clip(redact.String(e.Message), b.opts.ErrorPreviewMax)
			b.finish(ent, t)
		}
	case protocol.ToolCallBegin:
		b.noteSession(e.SessionID)
		ent := b.ensureTool(e.CallID, e.SessionID, e.TurnID, t)
		ent.Name = e.Name
		ent.State = StateRunning
		ent.ProviderRequestID = e.ProviderRequestID
		ent.Attempt = e.Attempt
		if ent.StartedAt == "" {
			ent.StartedAt = formatTime(t)
		}
		ent.ArgsPreview = clip(redact.String(string(e.Args)), b.opts.ArgsPreviewMax)
		if e.TurnID != "" {
			if turn := b.turns[e.TurnID]; turn != nil {
				ent.ParentID = turn.ID
			}
		}
	case protocol.ToolCallEnd:
		b.noteSession(e.SessionID)
		ent := b.ensureTool(e.CallID, e.SessionID, e.TurnID, t)
		// Name comes from ToolCallBegin; Title is a UI label only.
		out := e.Output
		if e.IsError {
			if isCanceledOutput(out) {
				ent.State = StateCanceled
			} else {
				ent.State = StateFailed
				ent.Error = clip(redact.String(out), b.opts.ErrorPreviewMax)
			}
		} else {
			ent.State = StateCompleted
		}
		ent.OutputPreview = clip(redact.String(out), b.opts.OutputPreviewMax)
		b.finish(ent, t)
	case protocol.PermissionAsked:
		b.noteSession(e.SessionID)
		// Mark open tool as waiting when we can correlate by request metadata.
		// Permission asks suspend a tool; without call id we mark the turn.
		if e.TurnID != "" {
			if turn := b.turns[e.TurnID]; turn != nil && turn.State == StateRunning {
				turn.State = StateWaiting
			}
		}
	case protocol.PermissionResolved:
		b.noteSession(e.SessionID)
		if e.TurnID != "" {
			if turn := b.turns[e.TurnID]; turn != nil && turn.State == StateWaiting {
				turn.State = StateRunning
			}
		}
	case protocol.UsageReported:
		b.noteSession(e.SessionID)
		ent := b.ensureProvider(e.ProviderRequestID, e.SessionID, e.TurnID, e.Attempt, t)
		if ent.StartedAt == "" {
			ent.StartedAt = formatTime(t)
		}
		// Usage is end-of-stream; treat as completed span ending now.
		if e.Input.Known {
			n := e.Input.N
			ent.InputTokens = &n
		}
		if e.Output.Known {
			n := e.Output.N
			ent.OutputTokens = &n
		}
		if e.CacheRead.Known {
			n := e.CacheRead.N
			ent.CacheReadTokens = &n
		}
		if e.CacheCreation.Known {
			n := e.CacheCreation.N
			ent.CacheCreationTokens = &n
		}
		ent.UsageSource = e.Source
		if ent.State == StateQueued || ent.State == "" {
			ent.State = StateCompleted
		} else if ent.State == StateRunning {
			ent.State = StateCompleted
		}
		b.finish(ent, t)
	case protocol.ProviderRetrying:
		b.noteSession(e.SessionID)
		// Failed attempt identity is on Correlation; next attempt is announced.
		if e.ProviderRequestID != "" {
			ent := b.ensureProvider(e.ProviderRequestID, e.SessionID, e.TurnID, e.Attempt, t)
			ent.State = StateFailed
			ent.Error = clip(redact.String(e.Message), b.opts.ErrorPreviewMax)
			b.finish(ent, t)
		}
	case protocol.ChildStarted:
		b.noteSession(e.ParentSessionID)
		if e.SessionID == "" {
			return
		}
		ent := b.ensureChild(e.SessionID, e.ParentSessionID, e.TurnID, t)
		ent.Name = firstNonEmpty(e.Name, e.Agent)
		ent.State = StateRunning
		if ent.StartedAt == "" {
			ent.StartedAt = formatTime(t)
		}
		if e.Prompt != "" {
			ent.ArgsPreview = clip(redact.String(e.Prompt), b.opts.ArgsPreviewMax)
		}
	case protocol.ChildCompleted:
		b.noteSession(e.ParentSessionID)
		if e.SessionID == "" {
			return
		}
		ent := b.ensureChild(e.SessionID, e.ParentSessionID, e.TurnID, t)
		ent.ChildStatus = string(e.Status)
		switch e.Status {
		case protocol.ChildStatusCompleted:
			ent.State = StateCompleted
		case protocol.ChildStatusCanceled:
			ent.State = StateCanceled
		default:
			ent.State = StateFailed
		}
		if e.Summary != "" {
			ent.OutputPreview = clip(redact.String(e.Summary), b.opts.OutputPreviewMax)
		} else if e.Handoff.Summary != "" {
			ent.OutputPreview = clip(redact.String(e.Handoff.Summary), b.opts.OutputPreviewMax)
		}
		b.finish(ent, t)
	case protocol.ChildEscalated:
		// Budget/stall/loop trip (#774): annotate running child; terminal
		// still comes from ChildCompleted.
		b.noteSession(e.ParentSessionID)
		if e.SessionID == "" {
			return
		}
		ent := b.ensureChild(e.SessionID, e.ParentSessionID, e.TurnID, t)
		if e.Name != "" && ent.Name == "" {
			ent.Name = e.Name
		}
		if e.Reason != "" {
			ent.Error = clip(redact.String(e.Kind+": "+e.Reason), b.opts.ErrorPreviewMax)
		}
	case protocol.SchedulerQueued:
		b.noteSession(e.SessionID)
		if e.TurnID != "" {
			if turn := b.turns[e.TurnID]; turn != nil && turn.State == StateRunning {
				turn.State = StateQueued
			}
		}
	case protocol.SchedulerAdmitted:
		b.noteSession(e.SessionID)
		if e.TurnID != "" {
			if turn := b.turns[e.TurnID]; turn != nil && turn.State == StateQueued {
				turn.State = StateRunning
			}
		}
	}
}

// Snapshot returns a deep copy of current entries in first-seen order.
func (b *Builder) Snapshot() []Entry {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Entry, 0, len(b.order))
	for _, id := range b.order {
		if ent := b.byID[id]; ent != nil {
			out = append(out, *ent)
		}
	}
	return out
}

// Trace builds a versioned export document from the current snapshot.
func (b *Builder) Trace() Trace {
	b.mu.Lock()
	sessionID := b.sessionID
	if sessionID == "" {
		sessionID = b.opts.SessionID
	}
	entries := make([]Entry, 0, len(b.order))
	for _, id := range b.order {
		if ent := b.byID[id]; ent != nil {
			cp := *ent
			// Final redaction pass so nested mutations cannot bypass scrub.
			cp.Error = redact.String(cp.Error)
			cp.ArgsPreview = redact.String(cp.ArgsPreview)
			cp.OutputPreview = redact.String(cp.OutputPreview)
			cp.Name = redact.String(cp.Name)
			entries = append(entries, cp)
		}
	}
	clock := b.opts.Clock
	b.mu.Unlock()

	return Trace{
		SchemaVersion: SchemaVersion,
		SessionID:     sessionID,
		ExportedAt:    clock(),
		Redacted:      true,
		Note:          "Derived harness timeline (turns/tools/provider attempts/children). Complements session JSONL full transcript and #774 agent roster/budget fields; does not replace either.",
		Summary:       summarize(entries),
		Entries:       entries,
	}
}

// Build constructs a Trace from a slice of timed events (offline path).
func Build(events []TimedEvent, opts Options) Trace {
	b := NewBuilder(opts)
	// Stable order: sort by time then original index for equal timestamps.
	type indexed struct {
		i int
		e TimedEvent
	}
	items := make([]indexed, len(events))
	for i, e := range events {
		items[i] = indexed{i: i, e: e}
	}
	sort.SliceStable(items, func(i, j int) bool {
		ti, tj := items[i].e.Time, items[j].e.Time
		if ti.Equal(tj) {
			return items[i].i < items[j].i
		}
		return ti.Before(tj)
	})
	for _, it := range items {
		b.Observe(it.e.Event, it.e.Time)
	}
	return b.Trace()
}

// ExportJSON writes a redacted Trace as pretty JSON to path (atomic replace).
func ExportJSON(path string, tr Trace) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create timeline export directory: %w", err)
	}
	// Field-level redaction already ran in Trace(); do not scrub the raw JSON
	// bytes (pattern replace can break string escapes / structure).
	data, err := json.MarshalIndent(tr, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".strike-timeline-*.tmp")
	if err != nil {
		return fmt.Errorf("create timeline temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write timeline: %w", err)
	}
	if _, err := tmp.Write([]byte("\n")); err != nil {
		return fmt.Errorf("write timeline newline: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod timeline temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync timeline temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close timeline temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace timeline file: %w", err)
	}
	cleanup = false
	return nil
}

// ExportJSONL writes one redacted entry per line plus a header line.
// Line 0 is {"type":"timeline.header",...}; subsequent lines are entries.
func ExportJSONL(path string, tr Trace) error {
	path = filepath.Clean(path)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create timeline export directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".strike-timeline-*.tmp")
	if err != nil {
		return fmt.Errorf("create timeline temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	enc := json.NewEncoder(tmp)
	header := map[string]any{
		"type":          "timeline.header",
		"schemaVersion": tr.SchemaVersion,
		"sessionId":     tr.SessionID,
		"exportedAt":    tr.ExportedAt,
		"redacted":      true,
		"summary":       tr.Summary,
		"note":          tr.Note,
	}
	if err := enc.Encode(header); err != nil {
		return err
	}
	for _, ent := range tr.Entries {
		row := struct {
			Type string `json:"type"`
			Entry
		}{Type: "timeline.entry", Entry: ent}
		if err := enc.Encode(row); err != nil {
			return err
		}
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// FormatCollapsed returns a human-readable multi-line summary (for TUI/debug).
// Secrets are redacted. At most maxLines lines (0 = DefaultCollapsedMaxLines).
func FormatCollapsed(entries []Entry, maxLines int) string {
	if maxLines <= 0 {
		maxLines = DefaultCollapsedMaxLines
	}
	var b strings.Builder
	n := 0
	for _, e := range entries {
		if n >= maxLines {
			fmt.Fprintf(&b, "… %d more entries\n", len(entries)-n)
			break
		}
		line := formatEntryLine(e)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
		n++
	}
	return strings.TrimRight(b.String(), "\n")
}

func formatEntryLine(e Entry) string {
	dur := ""
	if e.DurationMs != nil {
		dur = fmt.Sprintf(" %s", formatDurationMs(*e.DurationMs))
	}
	label := e.Kind
	if e.Name != "" {
		label = e.Kind + ":" + e.Name
	}
	id := shortID(e.ID)
	switch e.Kind {
	case KindTurn:
		id = shortID(e.TurnID)
	case KindTool:
		id = shortID(e.CallID)
	case KindProvider:
		id = shortID(e.ProviderRequestID)
	case KindChild:
		id = shortID(e.ChildSessionID)
	}
	state := e.State
	if state == "" {
		state = "?"
	}
	extra := ""
	if e.InputTokens != nil || e.OutputTokens != nil {
		in, out := 0, 0
		if e.InputTokens != nil {
			in = *e.InputTokens
		}
		if e.OutputTokens != nil {
			out = *e.OutputTokens
		}
		extra = fmt.Sprintf(" tokens=%d/%d", in, out)
	}
	if e.Error != "" {
		extra += " err=" + clip(redact.String(e.Error), 40)
	}
	return fmt.Sprintf("%-8s %-24s %-10s%s%s", state, clip(label, 24), id, dur, extra)
}

func formatDurationMs(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	if ms < 60_000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	}
	return fmt.Sprintf("%.1fm", float64(ms)/60_000)
}

func shortID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "-"
	}
	if utf8.RuneCountInString(id) <= 10 {
		return id
	}
	return string([]rune(id)[:10])
}

// --- internal helpers ---

func (b *Builder) noteSession(id string) {
	if b.sessionID == "" && id != "" {
		b.sessionID = id
	}
}

func (b *Builder) nextID(prefix string) string {
	b.seq++
	return fmt.Sprintf("%s-%d", prefix, b.seq)
}

func (b *Builder) track(ent *Entry) {
	if _, ok := b.byID[ent.ID]; ok {
		return
	}
	b.byID[ent.ID] = ent
	b.order = append(b.order, ent.ID)
}

func (b *Builder) ensureTurn(turnID, sessionID string, t time.Time) *Entry {
	if turnID == "" {
		turnID = b.nextID("turn")
	}
	if ent, ok := b.turns[turnID]; ok {
		return ent
	}
	ent := &Entry{
		ID:        b.nextID("turn"),
		Kind:      KindTurn,
		State:     StateRunning,
		SessionID: sessionID,
		TurnID:    turnID,
		StartedAt: formatTime(t),
	}
	b.turns[turnID] = ent
	b.track(ent)
	return ent
}

func (b *Builder) ensureTool(callID, sessionID, turnID string, t time.Time) *Entry {
	key := sessionID + "\x00" + callID
	if callID == "" {
		key = b.nextID("toolkey")
	}
	if ent, ok := b.tools[key]; ok {
		return ent
	}
	ent := &Entry{
		ID:        b.nextID("tool"),
		Kind:      KindTool,
		State:     StateRunning,
		SessionID: sessionID,
		TurnID:    turnID,
		CallID:    callID,
		StartedAt: formatTime(t),
	}
	b.tools[key] = ent
	b.track(ent)
	return ent
}

func (b *Builder) ensureProvider(reqID, sessionID, turnID string, attempt int, t time.Time) *Entry {
	if reqID == "" {
		reqID = b.nextID("preq")
	}
	if ent, ok := b.providers[reqID]; ok {
		return ent
	}
	ent := &Entry{
		ID:                b.nextID("provider"),
		Kind:              KindProvider,
		State:             StateRunning,
		SessionID:         sessionID,
		TurnID:            turnID,
		ProviderRequestID: reqID,
		Attempt:           attempt,
		StartedAt:         formatTime(t),
		Name:              "stream",
	}
	b.providers[reqID] = ent
	b.track(ent)
	return ent
}

func (b *Builder) ensureChild(childID, parentSessionID, turnID string, t time.Time) *Entry {
	if ent, ok := b.children[childID]; ok {
		return ent
	}
	ent := &Entry{
		ID:             b.nextID("child"),
		Kind:           KindChild,
		State:          StateRunning,
		SessionID:      parentSessionID,
		TurnID:         turnID,
		ChildSessionID: childID,
		StartedAt:      formatTime(t),
	}
	b.children[childID] = ent
	b.track(ent)
	return ent
}

func (b *Builder) finish(ent *Entry, t time.Time) {
	if ent == nil {
		return
	}
	ent.EndedAt = formatTime(t)
	if ent.StartedAt != "" {
		if start, err := time.Parse(time.RFC3339Nano, ent.StartedAt); err == nil {
			ms := t.Sub(start).Milliseconds()
			if ms < 0 {
				ms = 0
			}
			ent.DurationMs = &ms
		}
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func clip(s string, max int) string {
	if max <= 0 || s == "" {
		return s
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

func isCanceledOutput(out string) bool {
	o := strings.ToLower(strings.TrimSpace(out))
	return strings.Contains(o, "canceled") || strings.Contains(o, "cancelled") || strings.Contains(o, "interrupted")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func summarize(entries []Entry) Summary {
	var s Summary
	var minStart, maxEnd time.Time
	for _, e := range entries {
		switch e.Kind {
		case KindTurn:
			s.Turns++
		case KindTool:
			s.Tools++
		case KindProvider:
			s.Providers++
		case KindChild:
			s.Children++
		}
		switch e.State {
		case StateFailed:
			s.Failed++
		case StateCanceled:
			s.Canceled++
		}
		if e.InputTokens != nil {
			s.InputTok += int64(*e.InputTokens)
		}
		if e.OutputTokens != nil {
			s.OutputTok += int64(*e.OutputTokens)
		}
		if e.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.StartedAt); err == nil {
				if minStart.IsZero() || t.Before(minStart) {
					minStart = t
				}
			}
		}
		if e.EndedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, e.EndedAt); err == nil {
				if maxEnd.IsZero() || t.After(maxEnd) {
					maxEnd = t
				}
			}
		}
	}
	if !minStart.IsZero() && !maxEnd.IsZero() && !maxEnd.Before(minStart) {
		s.DurationMs = maxEnd.Sub(minStart).Milliseconds()
	}
	return s
}
