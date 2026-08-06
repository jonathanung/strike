package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// RootSummary is a public response payload for one active root.
type RootSummary struct {
	ID             string `json:"id"`
	Title          string `json:"title,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Busy           bool   `json:"busy"`
	ActiveAt       int64  `json:"activeAt,omitempty"` // unix millis of last activity
	CreatedAt      int64  `json:"createdAt,omitempty"`
	HasRecentEvent bool   `json:"hasRecentEvent"`
}

// RootCreateResult is returned after creating a new root.
type RootCreateResult struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
}

// RootResumeResult is returned after resuming a durable session.
type RootResumeResult struct {
	ID        string `json:"id"`
	SessionID string `json:"sessionId"`
	ResumedID string `json:"resumedId"` // the durable id that was resumed
	WasActive bool   `json:"wasActive"` // true when already live (just activated)
}

// RootSpawnFunc creates a new engine+session, returns its id. Called by LiveHub.Create.
type RootSpawnFunc func(ctx context.Context) (rootID string, err error)

// RootResumeFunc opens or activates a durable session. Called by LiveHub.Resume.
// sessionID is the durable session id to resume. Returns the Live id and a flag
// indicating whether it was already live (activate-only).
type RootResumeFunc func(ctx context.Context, sessionID string) (rootID string, wasActive bool, err error)

// rootEntry tracks one live root inside LiveHub.
type rootEntry struct {
	live     *Live
	title    string
	activeAt time.Time
	created  time.Time
}

// LiveHub owns multiple Live bridges, one per active root session, and scopes
// HTTP ops/status/events/ws to the root id in ?root=<id>.
type LiveHub struct {
	mu      sync.RWMutex
	active  string
	entries map[string]*rootEntry
	closeCh chan struct{}

	spawn  RootSpawnFunc
	resume RootResumeFunc
}

// NewLiveHub creates an empty hub. Callers wire lifecycle functions.
func NewLiveHub(spawn RootSpawnFunc, resume RootResumeFunc) *LiveHub {
	return &LiveHub{
		entries: make(map[string]*rootEntry),
		closeCh: make(chan struct{}),
		spawn:   spawn,
		resume:  resume,
	}
}

// Add registers a live bridge for id and sets it as active when it's the first.
func (h *LiveHub) Add(id string, live *Live) {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	h.entries[id] = &rootEntry{live: live, activeAt: now, created: now}
	if h.active == "" {
		h.active = id
	}
}

// Remove stops and removes id from the hub. If it was active, the first remaining
// entry becomes active.
func (h *LiveHub) Remove(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entry, ok := h.entries[id]
	if !ok {
		return
	}
	entry.live.Close()
	delete(h.entries, id)
	if h.active == id {
		h.active = ""
		for rid := range h.entries {
			h.active = rid
			break
		}
	}
}

// Active returns the currently active live bridge, or nil.
func (h *LiveHub) Active() *Live {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if entry, ok := h.entries[h.active]; ok {
		return entry.live
	}
	return nil
}

// LiveFor returns the live bridge for a given root id, or nil.
func (h *LiveHub) LiveFor(rootID string) *Live {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if rootID == "" {
		rootID = h.active
	}
	if entry, ok := h.entries[rootID]; ok {
		return entry.live
	}
	return nil
}

// ActiveID returns the active root id.
func (h *LiveHub) ActiveID() string {
	if h == nil {
		return ""
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.active
}

// List returns a snapshot of all active root sessions.
func (h *LiveHub) List() []RootSummary {
	if h == nil {
		return nil
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]RootSummary, 0, len(h.entries))
	for id, e := range h.entries {
		s := e.live.Status()
		out = append(out, RootSummary{
			ID:             id,
			Title:          e.title,
			Agent:          s.Agent,
			Busy:           s.Busy,
			ActiveAt:       e.activeAt.UnixMilli(),
			CreatedAt:      e.created.UnixMilli(),
			HasRecentEvent: time.Since(e.activeAt) < 5*time.Minute,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ActiveAt != out[j].ActiveAt {
			return out[i].ActiveAt > out[j].ActiveAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Create spawns a new empty root engine via the spawn callback and activates it.
func (h *LiveHub) Create(ctx context.Context) (RootCreateResult, error) {
	if h.spawn == nil {
		return RootCreateResult{}, errors.New("root creation unavailable")
	}
	rootID, err := h.spawn(ctx)
	if err != nil {
		return RootCreateResult{}, err
	}
	h.mu.Lock()
	if h.entries[rootID] != nil {
		h.active = rootID
	}
	h.mu.Unlock()
	return RootCreateResult{ID: rootID, SessionID: rootID}, nil
}

// Activate switches the active root to id. Returns an error when id is not live.
func (h *LiveHub) Activate(id string) error {
	id = trimRootID(id)
	if id == "" {
		return fmt.Errorf("root id is empty")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.entries[id]; !ok {
		return fmt.Errorf("root %q is not active", id)
	}
	h.active = id
	h.entries[id].activeAt = time.Now()
	return nil
}

// Resume restores a durable session as an active root via the resume callback.
// Rejects missing and child-session IDs.
func (h *LiveHub) Resume(ctx context.Context, sessionID string) (RootResumeResult, error) {
	sessionID = trimRootID(sessionID)
	if sessionID == "" {
		return RootResumeResult{}, fmt.Errorf("session id is empty")
	}
	if h.resume == nil {
		return RootResumeResult{}, errors.New("resume unavailable")
	}
	rootID, wasActive, err := h.resume(ctx, sessionID)
	if err != nil {
		return RootResumeResult{}, err
	}
	return RootResumeResult{
		ID:        rootID,
		SessionID: rootID,
		ResumedID: sessionID,
		WasActive: wasActive,
	}, nil
}

// Close stops every live bridge and unblocks subscribers.
func (h *LiveHub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, e := range h.entries {
		e.live.Close()
	}
	h.entries = nil
}

// MarkActive refreshes the activeAt timestamp for a root.
func (h *LiveHub) MarkActive(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry, ok := h.entries[id]; ok {
		entry.activeAt = time.Now()
	}
}

// SetTitle records a display title for a root.
func (h *LiveHub) SetTitle(id, title string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if entry, ok := h.entries[id]; ok {
		entry.title = title
	}
}

func trimRootID(id string) string {
	return strings.TrimSpace(id)
}

// AgentInfo is a selectable agent exposed to the web UI.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// StatusSnapshot is the live cockpit chrome state.
type StatusSnapshot struct {
	SessionID      string `json:"sessionId"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Autonomy       string `json:"autonomy,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Workflow       string `json:"workflow,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	Busy           bool   `json:"busy"`
	ContextUsed    int    `json:"contextUsed,omitempty"`
	ContextLimit   int    `json:"contextLimit,omitempty"`
	// Sandbox is the active OS process sandbox dial for this live session
	// (off|read-only|workspace-write). Distinct from permissionMode.
	Sandbox string `json:"sandbox,omitempty"`
	// SandboxBackend is bwrap|sandbox-exec when the OS launcher is available.
	SandboxBackend string `json:"sandboxBackend,omitempty"`
	// SandboxAvailable reports whether the OS sandbox backend can apply isolation.
	SandboxAvailable bool `json:"sandboxAvailable,omitempty"`
	// NetworkAllow is the config network.allow summary (empty = unrestricted public).
	NetworkAllow []string `json:"networkAllow,omitempty"`
}

// SandboxSnapshot is host-safe OS sandbox chrome for GET /v1/sandbox.
// Mode is the active live dial; DefaultMode is the persisted config default
// (may differ). Explain is the multi-line /sandbox explain profile text.
type SandboxSnapshot struct {
	Mode             string   `json:"mode"`
	Backend          string   `json:"backend,omitempty"`
	Available        bool     `json:"available"`
	NetworkAllow     []string `json:"networkAllow,omitempty"`
	Explain          string   `json:"explain,omitempty"`
	DefaultMode      string   `json:"defaultMode,omitempty"`
	PermissionMode   string   `json:"permissionMode,omitempty"`
	Note             string   `json:"note,omitempty"`
	Modes            []string `json:"modes,omitempty"`
	CanChangeDefault bool     `json:"canChangeDefault"`
}

// Live bridges a running engine to HTTP/WebSocket clients.
type Live struct {
	sessionID string
	cwd       string
	agents    []AgentInfo

	ops chan<- protocol.Op

	mu     sync.RWMutex
	status StatusSnapshot
	// sandboxExplain is the compiled profile text for GET /v1/sandbox.
	sandboxExplain string
	subs           map[chan protocol.Event]struct{}
	closed         bool
	closeCh        chan struct{}
}

// NewLive creates a live session bridge. ops must be the engine Ops channel.
func NewLive(sessionID, cwd string, agents []AgentInfo, ops chan<- protocol.Op) *Live {
	l := &Live{
		sessionID: sessionID,
		cwd:       cwd,
		agents:    append([]AgentInfo(nil), agents...),
		ops:       ops,
		subs:      make(map[chan protocol.Event]struct{}),
		closeCh:   make(chan struct{}),
		status: StatusSnapshot{
			SessionID: sessionID,
			CWD:       cwd,
		},
	}
	return l
}

// SetSandbox seeds OS sandbox chrome on the live status snapshot and stores
// explain text for GET /v1/sandbox. Safe to call once after NewLive.
func (l *Live) SetSandbox(mode, backend string, available bool, networkAllow []string, explain string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = "workspace-write"
	}
	l.status.Sandbox = mode
	l.status.SandboxBackend = strings.TrimSpace(backend)
	l.status.SandboxAvailable = available
	if len(networkAllow) > 0 {
		l.status.NetworkAllow = append([]string(nil), networkAllow...)
	} else {
		l.status.NetworkAllow = nil
	}
	l.sandboxExplain = explain
}

// SandboxExplain returns the compiled /sandbox explain body for this live root.
func (l *Live) SandboxExplain() string {
	if l == nil {
		return ""
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.sandboxExplain
}

// SessionID returns the live session id.
func (l *Live) SessionID() string { return l.sessionID }

// Agents returns selectable agents.
func (l *Live) Agents() []AgentInfo {
	out := make([]AgentInfo, len(l.agents))
	copy(out, l.agents)
	return out
}

// Status returns a copy of the current status snapshot.
func (l *Live) Status() StatusSnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := l.status
	if len(l.status.NetworkAllow) > 0 {
		out.NetworkAllow = append([]string(nil), l.status.NetworkAllow...)
	}
	return out
}

// Submit sends an op to the engine. Returns err if the live session is closed
// or the op channel blocks beyond a short timeout.
func (l *Live) Submit(ctx context.Context, op protocol.Op) error {
	if l == nil || l.ops == nil {
		return errors.New("live session unavailable")
	}
	l.mu.RLock()
	closed := l.closed
	l.mu.RUnlock()
	if closed {
		return errors.New("live session closed")
	}
	select {
	case l.ops <- op:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-l.closeCh:
		return errors.New("live session closed")
	case <-time.After(5 * time.Second):
		return errors.New("engine ops queue full")
	}
}

// Publish fans an engine event out to subscribers and updates status. A
// subscriber that cannot keep up is disconnected rather than blocking the
// engine event drain or other subscribers.
func (l *Live) Publish(ev protocol.Event) {
	if l == nil {
		return
	}
	l.applyStatus(ev)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	for ch := range l.subs {
		select {
		case ch <- ev:
		default:
			delete(l.subs, ch)
			close(ch)
		}
	}
	l.mu.Unlock()
}

// Subscribe receives live events until ctx is done or Live is closed.
// The returned channel is closed when unsubscribed.
func (l *Live) Subscribe(ctx context.Context) <-chan protocol.Event {
	ch := make(chan protocol.Event, 256)
	if l == nil {
		close(ch)
		return ch
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		close(ch)
		return ch
	}
	l.subs[ch] = struct{}{}
	l.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-l.closeCh:
		}
		l.mu.Lock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
		l.mu.Unlock()
	}()
	return ch
}

// Close marks the live session finished and unblocks subscribers.
func (l *Live) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	close(l.closeCh)
	for ch := range l.subs {
		delete(l.subs, ch)
		close(ch)
	}
}

func (l *Live) applyStatus(ev protocol.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch e := ev.(type) {
	case protocol.ModelSelected:
		l.status.Provider = e.Provider
		l.status.Model = e.Model
	case protocol.AgentSelected:
		l.status.Agent = e.Name
	case protocol.EffortSelected:
		l.status.Effort = string(e.Level)
	case protocol.AutonomySelected:
		l.status.Autonomy = string(e.Mode)
	case protocol.PermissionModeSelected:
		l.status.PermissionMode = string(e.Mode)
	case protocol.PhaseChanged:
		l.status.Phase = e.Phase
		l.status.Workflow = e.Workflow
	case protocol.TurnStarted:
		l.status.Busy = true
	case protocol.TurnCompleted:
		l.status.Busy = false
	case protocol.UsageReported:
		if e.Used.Known {
			l.status.ContextUsed = e.Used.N
		}
	}
}
