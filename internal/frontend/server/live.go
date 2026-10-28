package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike/internal/protocol"
)

// RootSummary is a public response payload for one active root.
type RootSummary struct {
	ID                string `json:"id"`
	Title             string `json:"title,omitempty"`
	Agent             string `json:"agent,omitempty"`
	Busy              bool   `json:"busy"`
	ActiveAt          int64  `json:"activeAt,omitempty"` // unix millis of last activity
	CreatedAt         int64  `json:"createdAt,omitempty"`
	HasRecentEvent    bool   `json:"hasRecentEvent"`
	PermissionPending bool   `json:"permissionPending,omitempty"`
	QuestionPending   bool   `json:"questionPending,omitempty"`
	// Status is the canonical id from protocol.ClassifyLive. Empty on a raw
	// hub list; HTTP handlers fill it.
	Status string `json:"status,omitempty"`
	// StatusSince is unix milliseconds when Status last changed.
	StatusSince int64 `json:"statusSince,omitempty"`
	// LastActivityAt is unix milliseconds of the last user-visible event.
	LastActivityAt int64 `json:"lastActivityAt,omitempty"`
	// Unread is true when that event is newer than the daemon seen map.
	Unread bool `json:"unread"`
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

	spawn      RootSpawnFunc
	resume     RootResumeFunc
	unattended RootSpawnFunc

	// onRemove, when set, runs after a root leaves the hub so the owner
	// can stop whatever the hub does not own (the root's engine and its
	// session-manager handle). Set via SetOnRemove before use.
	onRemove func(id string)
}

// SetOnRemove registers the callback invoked after a root is dropped
// from the hub (Remove or RemoveForSession). The hub never owned the
// root's engine: without this hook a closed root keeps its session open
// in the manager, and a later session delete reports it open and
// demands force.
func (h *LiveHub) SetOnRemove(fn func(id string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onRemove = fn
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
	entry := h.removeLocked(id)
	onRemove := h.onRemove
	h.mu.Unlock()
	if entry == nil {
		return
	}
	if entry.live != nil {
		entry.live.Close()
	}
	if onRemove != nil {
		onRemove(id)
	}
}

// removeLocked deletes id's entry and fixes the active id, with h.mu
// already held. It returns the removed entry, or nil when id was not
// registered. It does NOT close the entry's Live or fire onRemove: the
// caller does both after dropping the lock (Live.Close is idempotent
// and onRemove may call back into session/engine teardown).
func (h *LiveHub) removeLocked(id string) *rootEntry {
	entry, ok := h.entries[id]
	if !ok {
		return nil
	}
	delete(h.entries, id)
	if h.active == id {
		h.active = ""
		for rid := range h.entries {
			h.active = rid
			break
		}
	}
	return entry
}

// RemoveForSession deregisters the live root serving sessionID after that
// session's durable log has been deleted (#1399). It returns false and
// leaves the hub untouched when no entry serves sessionID, or when another
// live entry still references the same session or the same Live bridge —
// a root another live session still uses is never deregistered. The entry
// is normally keyed by the session id itself; an entry whose Live reports
// sessionID under a different key is also matched when it is the only one.
func (h *LiveHub) RemoveForSession(sessionID string) bool {
	if h == nil {
		return false
	}
	sessionID = trimRootID(sessionID)
	if sessionID == "" {
		return false
	}
	h.mu.Lock()
	// The candidate is normally the entry keyed by the session id. When no
	// such entry exists, fall back to the unique entry whose Live serves
	// this session, if there is exactly one.
	key := sessionID
	entry, ok := h.entries[key]
	if !ok {
		key = ""
		for rid, e := range h.entries {
			if e.live != nil && e.live.SessionID() == sessionID {
				if key != "" {
					h.mu.Unlock()
					return false // shared by multiple roots: keep all
				}
				key, entry = rid, e
			}
		}
		if key == "" {
			h.mu.Unlock()
			return false
		}
	}
	// The candidate must actually serve this session: a root serving a
	// different session is not this session's root.
	if entry.live != nil {
		if sid := entry.live.SessionID(); sid != "" && sid != sessionID {
			h.mu.Unlock()
			return false
		}
	}
	// Shared-root guard: another entry referencing the same Live bridge or
	// the same session id still uses this root, so it stays registered.
	for rid, other := range h.entries {
		if rid == key || other.live == nil {
			continue
		}
		if other.live == entry.live || other.live.SessionID() == sessionID {
			h.mu.Unlock()
			return false
		}
	}
	removed := h.removeLocked(key)
	onRemove := h.onRemove
	h.mu.Unlock()
	if removed == nil {
		return false
	}
	if removed.live != nil {
		removed.live.Close()
	}
	if onRemove != nil {
		onRemove(key)
	}
	return true
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
			ID:                id,
			Title:             e.title,
			Agent:             s.Agent,
			Busy:              s.Busy,
			ActiveAt:          e.activeAt.UnixMilli(),
			CreatedAt:         e.created.UnixMilli(),
			HasRecentEvent:    time.Since(e.activeAt) < 5*time.Minute,
			PermissionPending: s.PermissionPending,
			QuestionPending:   s.QuestionPending,
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
	res, err := h.Spawn(ctx)
	if err != nil {
		return RootCreateResult{}, err
	}
	h.mu.Lock()
	if h.entries[res.ID] != nil {
		h.active = res.ID
	}
	h.mu.Unlock()
	return res, nil
}

// SetUnattendedSpawn installs the spawn used by clocks and triggers.
// That callback must not carry the process --auto allow-all layer.
// When unset, SpawnUnattended uses Spawn.
func (h *LiveHub) SetUnattendedSpawn(fn RootSpawnFunc) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.unattended = fn
	h.mu.Unlock()
}

// Spawn registers a new root without changing the cockpit's active session.
func (h *LiveHub) Spawn(ctx context.Context) (RootCreateResult, error) {
	return h.spawnWith(ctx, false)
}

// SpawnUnattended registers a background root. Serve wires this to a spawn
// that starts in plan and omits the process permission bypass.
func (h *LiveHub) SpawnUnattended(ctx context.Context) (RootCreateResult, error) {
	return h.spawnWith(ctx, true)
}

func (h *LiveHub) spawnWith(ctx context.Context, unattended bool) (RootCreateResult, error) {
	if h == nil {
		return RootCreateResult{}, errors.New("root creation unavailable")
	}
	h.mu.RLock()
	fn := h.spawn
	if unattended && h.unattended != nil {
		fn = h.unattended
	}
	h.mu.RUnlock()
	if fn == nil {
		return RootCreateResult{}, errors.New("root creation unavailable")
	}
	rootID, err := fn(ctx)
	if err != nil {
		return RootCreateResult{}, err
	}
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
//
// Token/cost session totals are accumulated client-side from usage.reported
// events (including WS/SSE replay). ContextUsed is last-request occupancy from
// UsageReported.Used when known — not a session sum.
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
	// ContextFit is the latest context.fit_warning level (warn|critical).
	// Empty after compaction, or after a usage report falls under 80% of
	// ContextLimit (the engine's fitWarnRatio). It is not a second limit.
	ContextFit string `json:"contextFit,omitempty"`
	// Sandbox is the active OS process sandbox dial for this live session
	// (off|read-only|workspace-write). Distinct from permissionMode.
	Sandbox string `json:"sandbox,omitempty"`
	// SandboxBackend is bwrap|sandbox-exec when the OS launcher is available.
	SandboxBackend string `json:"sandboxBackend,omitempty"`
	// SandboxAvailable reports whether the OS sandbox backend can apply isolation.
	SandboxAvailable bool `json:"sandboxAvailable,omitempty"`
	// NetworkAllow is the config network.allow summary (empty = unrestricted public).
	NetworkAllow      []string `json:"networkAllow,omitempty"`
	PermissionPending bool     `json:"permissionPending,omitempty"`
	QuestionPending   bool     `json:"questionPending,omitempty"`
	// StopReason is the last TurnCompleted reason. Empty after a new turn starts.
	StopReason string `json:"stopReason,omitempty"`
	// TurnSince is unix milliseconds when the current turn state began.
	TurnSince int64 `json:"turnSince,omitempty"`
	// LastEventAt is unix milliseconds of the last user-visible event.
	// Reads and status polls do not bump it.
	LastEventAt int64 `json:"lastEventAt,omitempty"`
	// TurnState is the canonical status id for this root, including child
	// attention. Computed by Status; not stored.
	TurnState string `json:"turnState,omitempty"`
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

// TeamMemberSnapshot is one observe-only roster row for GET /v1/team.
type TeamMemberSnapshot struct {
	SessionID       string         `json:"sessionId"`
	Name            string         `json:"name,omitempty"`
	Agent           string         `json:"agent,omitempty"`
	State           string         `json:"state"`
	Role            string         `json:"role,omitempty"`
	ParentSessionID string         `json:"parentSessionId,omitempty"`
	TerminalSummary string         `json:"terminalSummary,omitempty"`
	Objective       string         `json:"objective,omitempty"`
	BlockReason     string         `json:"blockReason,omitempty"`
	Budget          map[string]any `json:"budget,omitempty"`
	Terminal        bool           `json:"terminal,omitempty"`
}

// DelegationSnapshot is one delegation/task board row.
type DelegationSnapshot struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	Prev           string `json:"prev,omitempty"`
	Version        int    `json:"version,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	Name           string `json:"name,omitempty"`
	Reason         string `json:"reason,omitempty"`
	OwnerSessionID string `json:"ownerSessionId,omitempty"`
}

// TeamMessageSnapshot is a bounded mailbox note for late join.
type TeamMessageSnapshot struct {
	MessageID string `json:"messageId,omitempty"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	Body      string `json:"body,omitempty"`
	Summary   string `json:"summary,omitempty"`
	TaskID    string `json:"taskId,omitempty"`
	Urgency   string `json:"urgency,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Time      string `json:"time,omitempty"`
}

// TeamSnapshot is the root-scoped observe-only team projection for reload/late join.
type TeamSnapshot struct {
	Available         bool                          `json:"available"`
	UnavailableReason string                        `json:"unavailableReason,omitempty"`
	LeadID            string                        `json:"leadId,omitempty"`
	Members           map[string]TeamMemberSnapshot `json:"members"`
	Delegations       map[string]DelegationSnapshot `json:"delegations"`
	Messages          []TeamMessageSnapshot         `json:"messages"`
	PathOverlaps      []map[string]any              `json:"pathOverlaps"`
	Artifacts         map[string]map[string]any     `json:"artifacts"`
	Ledger            map[string]map[string]any     `json:"ledger"`
	Verifications     []map[string]any              `json:"verifications"`
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
	// team is the observe-only multi-agent projection (WEBUI.13).
	team    TeamSnapshot
	subs    map[chan protocol.Event]struct{}
	closed  bool
	closeCh chan struct{}

	memoryModeGet func() string
	memoryModeSet func(string) error
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
		team: emptyTeamSnapshot(),
	}
	return l
}

func emptyTeamSnapshot() TeamSnapshot {
	return TeamSnapshot{
		Available:     true,
		Members:       map[string]TeamMemberSnapshot{},
		Delegations:   map[string]DelegationSnapshot{},
		Messages:      nil,
		PathOverlaps:  nil,
		Artifacts:     map[string]map[string]any{},
		Ledger:        map[string]map[string]any{},
		Verifications: nil,
	}
}

// BindMemoryMode attaches the session flag getter and setter. set persists
// the mode and enforces it on the engine. A nil binding reports unwired.
func (l *Live) BindMemoryMode(get func() string, set func(string) error) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.memoryModeGet = get
	l.memoryModeSet = set
	l.mu.Unlock()
}

// MemoryMode returns the bound session mode. ok is false when unwired.
func (l *Live) MemoryMode() (string, bool) {
	if l == nil {
		return memoryModeNormal, false
	}
	l.mu.RLock()
	get := l.memoryModeGet
	l.mu.RUnlock()
	if get == nil {
		return memoryModeNormal, false
	}
	mode := strings.TrimSpace(get())
	if mode == "" {
		mode = memoryModeNormal
	}
	return mode, true
}

// SetMemoryMode updates the bound session flag.
func (l *Live) SetMemoryMode(mode string) error {
	if l == nil || l.memoryModeSet == nil {
		return errors.New("memory mode is not wired")
	}
	l.mu.RLock()
	set := l.memoryModeSet
	l.mu.RUnlock()
	if set == nil {
		return errors.New("memory mode is not wired")
	}
	return set(mode)
}

const memoryModeNormal = "normal"

// Team returns a deep-ish copy of the observe-only team snapshot for this root.
func (l *Live) Team() TeamSnapshot {
	if l == nil {
		return TeamSnapshot{Available: false, UnavailableReason: "live session unavailable", Members: map[string]TeamMemberSnapshot{}, Delegations: map[string]DelegationSnapshot{}, Artifacts: map[string]map[string]any{}, Ledger: map[string]map[string]any{}}
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return cloneTeamSnapshot(l.team)
}

func cloneTeamSnapshot(in TeamSnapshot) TeamSnapshot {
	out := emptyTeamSnapshot()
	out.Available = in.Available
	out.UnavailableReason = in.UnavailableReason
	out.LeadID = in.LeadID
	for k, v := range in.Members {
		out.Members[k] = v
	}
	for k, v := range in.Delegations {
		out.Delegations[k] = v
	}
	if len(in.Messages) > 0 {
		out.Messages = append([]TeamMessageSnapshot(nil), in.Messages...)
	}
	if len(in.PathOverlaps) > 0 {
		out.PathOverlaps = append([]map[string]any(nil), in.PathOverlaps...)
	}
	for k, v := range in.Artifacts {
		out.Artifacts[k] = v
	}
	for k, v := range in.Ledger {
		out.Ledger[k] = v
	}
	if len(in.Verifications) > 0 {
		out.Verifications = append([]map[string]any(nil), in.Verifications...)
	}
	return out
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
	running, waiting, blocked := childAttention(l.team)
	out.TurnState = protocol.ClassifyLive(protocol.LiveStatusInput{
		Busy:              out.Busy,
		PermissionPending: out.PermissionPending,
		QuestionPending:   out.QuestionPending,
		StopReason:        out.StopReason,
		ChildRunning:      running,
		ChildWaiting:      waiting,
		ChildBlocked:      blocked,
	})
	return out
}

func childAttention(team TeamSnapshot) (running, waiting, blocked bool) {
	for _, m := range team.Members {
		if strings.EqualFold(m.Role, "lead") {
			continue
		}
		if !m.Terminal {
			if stall, _ := m.Budget["stall"].(bool); stall {
				waiting = true
				continue
			}
		}
		switch protocol.NormalizeStatus(m.State) {
		case protocol.StatusRunning:
			if !m.Terminal {
				running = true
			}
		case protocol.StatusWaiting:
			waiting = true
		case protocol.StatusBlocked:
			blocked = true
		}
	}
	return running, waiting, blocked
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

// RequestDiagnostic submits inspect.diagnostic and waits for the matching
// DiagnosticBundle event. Subscribe before Submit so the response is not missed.
func (l *Live) RequestDiagnostic(ctx context.Context) (protocol.DiagnosticBundle, error) {
	var zero protocol.DiagnosticBundle
	if l == nil {
		return zero, errors.New("live session unavailable")
	}
	sub := l.Subscribe(ctx)
	if err := l.Submit(ctx, protocol.InspectDiagnosticBundle{}); err != nil {
		return zero, err
	}
	for {
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case ev, ok := <-sub:
			if !ok {
				return zero, errors.New("live session closed")
			}
			if b, ok := ev.(protocol.DiagnosticBundle); ok {
				return b, nil
			}
		}
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

// Closed fires when Close has been called. Serve uses it to stop the
// engine for that root: LiveHub.Remove only closes the bridge, and a root
// started on the process context would otherwise stay open.
func (l *Live) Closed() <-chan struct{} {
	if l == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return l.closeCh
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
	case protocol.UserMessage, protocol.TextDelta, protocol.ReasoningDelta, protocol.ToolCallBegin, protocol.ToolCallEnd:
		l.noteActivityLocked()
	case protocol.TurnStarted:
		l.status.Busy = true
		l.status.StopReason = ""
		l.status.TurnSince = time.Now().UnixMilli()
		l.noteActivityLocked()
	case protocol.TurnCompleted:
		l.status.Busy = false
		l.status.StopReason = e.StopReason
		l.status.TurnSince = time.Now().UnixMilli()
		l.noteActivityLocked()
	case protocol.PermissionAsked:
		l.status.PermissionPending = true
		l.status.TurnSince = time.Now().UnixMilli()
		l.noteActivityLocked()
	case protocol.PermissionResolved:
		l.status.PermissionPending = false
		l.status.TurnSince = time.Now().UnixMilli()
		l.noteActivityLocked()
	case protocol.QuestionAsked:
		l.status.QuestionPending = true
		l.status.TurnSince = time.Now().UnixMilli()
		l.noteActivityLocked()
	case protocol.QuestionResolved:
		l.status.QuestionPending = false
		l.status.TurnSince = time.Now().UnixMilli()
		l.noteActivityLocked()
	case protocol.UsageReported:
		if e.Used.Known {
			l.status.ContextUsed = e.Used.N
			l.relaxContextFit()
		}
	case protocol.ContextFitWarning:
		if e.ContextLimit > 0 {
			l.status.ContextLimit = e.ContextLimit
		}
		if e.EstimatedTokens > 0 && l.status.ContextUsed == 0 {
			l.status.ContextUsed = e.EstimatedTokens
		}
		switch e.Level {
		case protocol.ContextFitWarn, protocol.ContextFitCritical:
			l.status.ContextFit = e.Level
		}
	case protocol.CompactionCompleted:
		// Occupancy from before the compact is no longer the window.
		// The next usage.reported fills it in. Keep the limit (window size).
		l.status.ContextUsed = 0
		l.status.ContextFit = ""
	case protocol.TeamRoster:
		l.applyTeamRoster(e)
	case protocol.ChildStarted:
		l.noteActivityLocked()
		l.applyChildStarted(e)
	case protocol.ChildCompleted:
		l.noteActivityLocked()
		l.applyChildCompleted(e)
	case protocol.DelegationChanged:
		l.applyDelegation(e)
	case protocol.AgentMessage:
		l.applyAgentMessage(e)
	case protocol.PathOverlap:
		l.applyPathOverlap(e)
	case protocol.ArtifactUpdated:
		l.applyArtifact(e)
	case protocol.LedgerUpdated:
		l.applyLedger(e)
	}
}

// relaxContextFit drops a stale fit warning once measured occupancy is
// back under the engine's warn ratio (0.80). A known zero does not clear
// it: omitempty can omit n, and that must not wipe a critical flag.
func (l *Live) relaxContextFit() {
	if l.status.ContextFit == "" || l.status.ContextLimit <= 0 || l.status.ContextUsed <= 0 {
		return
	}
	if int64(l.status.ContextUsed)*5 < int64(l.status.ContextLimit)*4 {
		l.status.ContextFit = ""
	}
}

func (l *Live) noteActivityLocked() {
	l.status.LastEventAt = time.Now().UnixMilli()
}

func teamTerminal(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "canceled", "cancelled", "blocked":
		return true
	default:
		return false
	}
}

func (l *Live) upsertMember(m TeamMemberSnapshot) {
	if l.team.Members == nil {
		l.team.Members = map[string]TeamMemberSnapshot{}
	}
	prev, ok := l.team.Members[m.SessionID]
	if ok && (prev.Terminal || teamTerminal(prev.State)) && !teamTerminal(m.State) {
		// Never revive terminal agents from late roster rows.
		m.State = prev.State
		m.Terminal = true
		if m.TerminalSummary == "" {
			m.TerminalSummary = prev.TerminalSummary
		}
	}
	if teamTerminal(m.State) {
		m.Terminal = true
	}
	if m.Name == "" {
		m.Name = prev.Name
	}
	if m.Agent == "" {
		m.Agent = prev.Agent
	}
	l.team.Members[m.SessionID] = m
	l.team.Available = true
	l.team.UnavailableReason = ""
}

func (l *Live) applyTeamRoster(e protocol.TeamRoster) {
	if e.LeadID != "" {
		l.team.LeadID = e.LeadID
	}
	for _, m := range e.Members {
		sid := strings.TrimSpace(m.SessionID)
		if sid == "" {
			continue
		}
		row := TeamMemberSnapshot{
			SessionID:       sid,
			Name:            m.Name,
			Agent:           m.Agent,
			State:           m.State,
			Role:            m.Role,
			ParentSessionID: m.ParentSessionID,
			TerminalSummary: m.TerminalSummary,
			Objective:       m.Objective,
			BlockReason:     m.BlockReason,
		}
		if m.Budget != nil {
			row.Budget = map[string]any{
				"tokensUsed":  m.Budget.TokensUsed,
				"costUsdUsed": m.Budget.CostUSDUsed,
				"toolCalls":   m.Budget.ToolCalls,
				"elapsedS":    m.Budget.ElapsedS,
				"stall":       m.Budget.Stall,
				"loop":        m.Budget.Loop,
			}
		}
		l.upsertMember(row)
	}
}

func (l *Live) applyChildStarted(e protocol.ChildStarted) {
	sid := strings.TrimSpace(e.SessionID)
	if sid == "" {
		return
	}
	l.upsertMember(TeamMemberSnapshot{
		SessionID: sid,
		Name:      e.Name,
		Agent:     e.Agent,
		State:     "running",
		Objective: e.Prompt,
	})
}

func (l *Live) applyChildCompleted(e protocol.ChildCompleted) {
	sid := strings.TrimSpace(e.SessionID)
	if sid == "" {
		return
	}
	state := string(e.Status)
	if state == "" {
		state = "completed"
	}
	summary := e.Summary
	if summary == "" {
		summary = e.Handoff.Summary
	}
	l.upsertMember(TeamMemberSnapshot{
		SessionID:       sid,
		Name:            e.Name,
		State:           state,
		TerminalSummary: summary,
		Terminal:        true,
	})
	if e.Verification != nil {
		l.team.Verifications = append(l.team.Verifications, map[string]any{
			"sessionId": sid,
			"passed":    e.Verification.Passed,
			"claimed":   e.Verification.Claimed,
			"verified":  e.Verification.Verified,
			"summary":   e.Verification.Summary,
		})
		if len(l.team.Verifications) > 40 {
			l.team.Verifications = l.team.Verifications[len(l.team.Verifications)-40:]
		}
	}
}

func (l *Live) applyDelegation(e protocol.DelegationChanged) {
	id := strings.TrimSpace(e.ID)
	if id == "" {
		return
	}
	if l.team.Delegations == nil {
		l.team.Delegations = map[string]DelegationSnapshot{}
	}
	l.team.Delegations[id] = DelegationSnapshot{
		ID:             id,
		State:          string(e.State),
		Prev:           string(e.Prev),
		Version:        e.Version,
		SessionID:      e.SessionID,
		Name:           e.Name,
		Reason:         e.Reason,
		OwnerSessionID: e.OwnerSessionID,
	}
	l.team.Available = true
}

func (l *Live) applyAgentMessage(e protocol.AgentMessage) {
	msg := TeamMessageSnapshot{
		MessageID: e.MessageID,
		From:      e.From,
		To:        e.To,
		Body:      e.Body,
		Summary:   e.Summary,
		TaskID:    e.TaskID,
		Urgency:   e.Urgency,
		Kind:      e.Kind,
	}
	l.team.Messages = append(l.team.Messages, msg)
	if len(l.team.Messages) > 100 {
		l.team.Messages = l.team.Messages[len(l.team.Messages)-100:]
	}
}

func (l *Live) applyPathOverlap(e protocol.PathOverlap) {
	row := map[string]any{"path": e.Path, "policy": e.Policy, "blocked": e.Blocked}
	if len(e.Holders) > 0 {
		sessions := make([]string, 0, len(e.Holders))
		for _, h := range e.Holders {
			if sid := strings.TrimSpace(h.SessionID); sid != "" {
				sessions = append(sessions, sid)
			}
		}
		if len(sessions) > 0 {
			row["sessions"] = sessions
		}
	}
	l.team.PathOverlaps = append(l.team.PathOverlaps, row)
	if len(l.team.PathOverlaps) > 50 {
		l.team.PathOverlaps = l.team.PathOverlaps[len(l.team.PathOverlaps)-50:]
	}
}

func (l *Live) applyArtifact(e protocol.ArtifactUpdated) {
	id := strings.TrimSpace(e.ID)
	if id == "" {
		return
	}
	if l.team.Artifacts == nil {
		l.team.Artifacts = map[string]map[string]any{}
	}
	l.team.Artifacts[id] = map[string]any{
		"id": id, "type": e.Type, "version": e.Version, "title": e.Title, "op": e.Op, "scope": e.Scope,
	}
}

func (l *Live) applyLedger(e protocol.LedgerUpdated) {
	id := strings.TrimSpace(e.ID)
	if id == "" {
		return
	}
	if l.team.Ledger == nil {
		l.team.Ledger = map[string]map[string]any{}
	}
	// Bound statement length; full history remains tool-backed.
	stmt := e.Statement
	if len(stmt) > 2000 {
		stmt = stmt[:2000]
	}
	l.team.Ledger[id] = map[string]any{
		"id": id, "kind": e.Kind, "status": e.Status, "op": e.Op, "statement": stmt,
	}
}
