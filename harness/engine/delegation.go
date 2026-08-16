package engine

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Delegation lifecycle limits.
const (
	MaxDelegationCriteria      = 32
	MaxDelegationCriteriaRunes = 2 * 1024
	MaxDelegationDeps          = 32
	MaxDelegations             = 256
	MaxDelegationSubscribe     = 8
)

// Delegation is a first-class orchestration object wrapping a task spawn with
// acceptance criteria, dependencies, subscriptions, and a validated state
// machine. Stored on the lead-scoped Team (like the shared board).
//
// States: queued → working → blocked → review → done (+ failed / canceled).
// Plain task spawns create a working delegation linked to the child session.
type Delegation struct {
	ID             string                   `json:"id"`
	Prompt         string                   `json:"prompt"`
	Criteria       []string                 `json:"criteria,omitempty"`
	Deps           []string                 `json:"deps,omitempty"`
	Subscribe      []string                 `json:"subscribe,omitempty"` // blocked|review|done|failed|canceled
	OwnerSessionID string                   `json:"owner_session_id"`
	Assignee       string                   `json:"assignee,omitempty"` // optional display / future assign
	Agent          string                   `json:"agent,omitempty"`
	Model          string                   `json:"model,omitempty"`
	Effort         string                   `json:"effort,omitempty"`
	Name           string                   `json:"name,omitempty"`
	SessionID      string                   `json:"session_id,omitempty"`
	State          protocol.DelegationState `json:"state"`
	Version        int                      `json:"version"`
	BlockReason    string                   `json:"block_reason,omitempty"`
	// RouteReason records the capability-routing decision at create (#778).
	RouteReason string `json:"route_reason,omitempty"`
	// SpawnPending is true when deps are satisfied and the owner engine should
	// start the child (or a deferred release is in flight).
	SpawnPending bool `json:"spawn_pending,omitempty"`
	// Verify holds independent completion gates declared at create (for deferred spawn).
	Verify []tool.VerifyGate `json:"verify,omitempty"`
	// Budget is the per-child limit snapshot captured at create (for deferred spawn).
	Budget tool.AgentBudgetLimits `json:"budget,omitempty"`
	// ContextBundle is the sealed context package for deferred/immediate spawn.
	ContextBundle tool.ContextBundle `json:"context_bundle,omitempty"`
	CreatedAt     time.Time          `json:"created_at,omitempty"`
	UpdatedAt     time.Time          `json:"updated_at,omitempty"`
}

// DelegationConflictError is returned when CAS version or ownership fails.
type DelegationConflictError struct {
	Reason string
	Item   Delegation
}

func (e *DelegationConflictError) Error() string {
	if e == nil {
		return "delegation conflict"
	}
	if e.Reason == "" {
		return "delegation conflict"
	}
	return e.Reason
}

// DelegationTransitionError is an illegal state-machine move.
type DelegationTransitionError struct {
	From   protocol.DelegationState
	To     protocol.DelegationState
	Detail string
}

func (e *DelegationTransitionError) Error() string {
	if e == nil {
		return "illegal delegation transition"
	}
	if e.Detail != "" {
		return e.Detail
	}
	return fmt.Sprintf("illegal delegation transition %s → %s", e.From, e.To)
}

// ValidDelegationTransition reports whether from→to is allowed.
func ValidDelegationTransition(from, to protocol.DelegationState) bool {
	if from == to {
		return true // no-op / idempotent
	}
	switch from {
	case protocol.DelegationQueued:
		switch to {
		case protocol.DelegationWorking, protocol.DelegationBlocked,
			protocol.DelegationCanceled, protocol.DelegationFailed:
			return true
		}
	case protocol.DelegationWorking:
		switch to {
		case protocol.DelegationBlocked, protocol.DelegationReview, protocol.DelegationDone,
			protocol.DelegationFailed, protocol.DelegationCanceled:
			return true
		}
	case protocol.DelegationBlocked:
		switch to {
		case protocol.DelegationWorking, protocol.DelegationQueued, protocol.DelegationReview,
			protocol.DelegationFailed, protocol.DelegationCanceled:
			return true
		}
	case protocol.DelegationReview:
		switch to {
		case protocol.DelegationDone, protocol.DelegationWorking, protocol.DelegationBlocked,
			protocol.DelegationFailed, protocol.DelegationCanceled:
			return true
		}
	case protocol.DelegationDone, protocol.DelegationFailed, protocol.DelegationCanceled:
		return false
	}
	return false
}

// IsTerminalDelegation reports terminal lifecycle states.
func IsTerminalDelegation(s protocol.DelegationState) bool {
	switch s {
	case protocol.DelegationDone, protocol.DelegationFailed, protocol.DelegationCanceled:
		return true
	default:
		return false
	}
}

// NormalizeSubscribeKinds validates subscription event kinds.
func NormalizeSubscribeKinds(kinds []string) ([]string, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	if len(kinds) > MaxDelegationSubscribe {
		return nil, fmt.Errorf("subscribe exceeds %d kinds", MaxDelegationSubscribe)
	}
	seen := make(map[string]struct{}, len(kinds))
	out := make([]string, 0, len(kinds))
	for _, k := range kinds {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		switch k {
		case "blocked", "review", "done", "failed", "canceled", "working", "queued":
		default:
			return nil, fmt.Errorf("unknown subscribe kind %q (want blocked|review|done|failed|canceled|working|queued)", k)
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out, nil
}

// NormalizeCriteria trims and bounds acceptance criteria lines.
func NormalizeCriteria(lines []string) ([]string, error) {
	if len(lines) == 0 {
		return nil, nil
	}
	if len(lines) > MaxDelegationCriteria {
		return nil, fmt.Errorf("criteria exceeds %d items", MaxDelegationCriteria)
	}
	out := make([]string, 0, len(lines))
	for _, c := range lines {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if n := utf8.RuneCountInString(c); n > MaxDelegationCriteriaRunes {
			return nil, fmt.Errorf("criteria item exceeds %d runes (%d)", MaxDelegationCriteriaRunes, n)
		}
		out = append(out, c)
	}
	return out, nil
}

// NormalizeDeps trims dependency ids (delegation id or linked session id).
func NormalizeDeps(deps []string) ([]string, error) {
	if len(deps) == 0 {
		return nil, nil
	}
	if len(deps) > MaxDelegationDeps {
		return nil, fmt.Errorf("deps exceeds %d items", MaxDelegationDeps)
	}
	seen := make(map[string]struct{}, len(deps))
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		if _, ok := seen[d]; ok {
			continue
		}
		seen[d] = struct{}{}
		out = append(out, d)
	}
	return out, nil
}

// CreateDelegationSpec is the input for Team.CreateDelegation.
type CreateDelegationSpec struct {
	Prompt         string
	Criteria       []string
	Deps           []string
	Subscribe      []string
	OwnerSessionID string
	Assignee       string
	Agent          string
	Model          string
	Effort         string
	Name           string
	// RouteReason is the structured routing decision at create (#778).
	RouteReason string
	// SessionID when already spawning (immediate start).
	SessionID string
	// StartState overrides initial state when non-empty (working when session set).
	StartState protocol.DelegationState
	// Verify gates to attach when the child eventually spawns.
	Verify []tool.VerifyGate
	// Budget is the merged per-child limit for deferred spawn.
	Budget tool.AgentBudgetLimits
	// ContextBundle is the sealed context package for the child.
	ContextBundle tool.ContextBundle
}

// CreateDelegation appends a new lifecycle object. Initial state is queued when
// deps are unmet; working when StartState/SessionID indicate an immediate spawn;
// otherwise queued (caller will spawn and transition).
func (t *Team) CreateDelegation(spec CreateDelegationSpec) (Delegation, error) {
	if t == nil {
		return Delegation{}, fmt.Errorf("no team")
	}
	prompt := strings.TrimSpace(spec.Prompt)
	owner := strings.TrimSpace(spec.OwnerSessionID)
	if prompt == "" {
		return Delegation{}, fmt.Errorf("prompt is required")
	}
	if owner == "" {
		return Delegation{}, fmt.Errorf("owner is required")
	}
	criteria, err := NormalizeCriteria(spec.Criteria)
	if err != nil {
		return Delegation{}, err
	}
	deps, err := NormalizeDeps(spec.Deps)
	if err != nil {
		return Delegation{}, err
	}
	subs, err := NormalizeSubscribeKinds(spec.Subscribe)
	if err != nil {
		return Delegation{}, err
	}
	name, err := ValidateMemberName(spec.Name)
	if err != nil {
		return Delegation{}, err
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return Delegation{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[owner]; !ok {
		return Delegation{}, fmt.Errorf("owner is not on this team")
	}
	if len(t.delegations) >= MaxDelegations {
		return Delegation{}, fmt.Errorf("delegation registry is full (%d)", MaxDelegations)
	}

	// Validate deps exist (or allow session ids of known members / future dN).
	for _, dep := range deps {
		if _, ok := t.lookupDelegationLocked(dep); !ok {
			return Delegation{}, fmt.Errorf("unknown dependency %q", dep)
		}
	}
	// Self-dep / cycles: simple check — dep graph edges only to existing ids.
	// Full cycle detection when adding edges from new node.
	if err := detectDelegationCycleLocked(t, "", deps); err != nil {
		return Delegation{}, err
	}

	unmet := unmetDepsLocked(t, deps)
	state := protocol.DelegationQueued
	spawnPending := false
	switch {
	case spec.StartState != "":
		state = spec.StartState
	case strings.TrimSpace(spec.SessionID) != "":
		state = protocol.DelegationWorking
	case len(unmet) == 0 && len(deps) == 0:
		// Immediate spawn path — caller sets working after child starts.
		// Stay queued with spawnPending so engine can start atomically.
		state = protocol.DelegationQueued
		spawnPending = true
	case len(unmet) == 0 && len(deps) > 0:
		state = protocol.DelegationQueued
		spawnPending = true
	default:
		state = protocol.DelegationQueued
		spawnPending = false
	}

	t.delegSeq++
	id := fmt.Sprintf("d%d", t.delegSeq)
	now := time.Now().UTC()
	item := Delegation{
		ID:             id,
		Prompt:         prompt,
		Criteria:       criteria,
		Deps:           deps,
		Subscribe:      subs,
		OwnerSessionID: owner,
		Assignee:       strings.TrimSpace(spec.Assignee),
		Agent:          strings.TrimSpace(spec.Agent),
		Model:          strings.TrimSpace(spec.Model),
		Effort:         strings.TrimSpace(spec.Effort),
		Name:           name,
		RouteReason:    strings.TrimSpace(spec.RouteReason),
		SessionID:      strings.TrimSpace(spec.SessionID),
		State:          state,
		Version:        1,
		SpawnPending:   spawnPending,
		Verify:         append([]tool.VerifyGate(nil), spec.Verify...),
		Budget:         NormalizeAgentBudget(spec.Budget),
		ContextBundle:  spec.ContextBundle.Clone(),
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if item.SessionID != "" && item.State == protocol.DelegationQueued && spec.StartState == "" {
		item.State = protocol.DelegationWorking
		item.SpawnPending = false
	}
	if t.delegations == nil {
		t.delegations = make(map[string]Delegation, 8)
	}
	t.delegations[id] = item
	return item, nil
}

// Delegations returns a stable snapshot (id ascending).
func (t *Team) Delegations() []Delegation {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.delegationSnapshotLocked()
}

func (t *Team) delegationSnapshotLocked() []Delegation {
	if len(t.delegations) == 0 {
		return []Delegation{}
	}
	out := make([]Delegation, 0, len(t.delegations))
	for _, d := range t.delegations {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// GetDelegation resolves by delegation id or linked session id / name.
func (t *Team) GetDelegation(ref string) (Delegation, bool) {
	if t == nil {
		return Delegation{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.lookupDelegationLocked(ref)
}

func (t *Team) lookupDelegationLocked(ref string) (Delegation, bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" || len(t.delegations) == 0 {
		return Delegation{}, false
	}
	if d, ok := t.delegations[ref]; ok {
		return d, true
	}
	for _, d := range t.delegations {
		if d.SessionID == ref {
			return d, true
		}
		if d.Name != "" && d.Name == ref {
			return d, true
		}
	}
	return Delegation{}, false
}

// LinkDelegationSession attaches a child session id after spawn.
func (t *Team) LinkDelegationSession(id, sessionID string) (Delegation, error) {
	if t == nil {
		return Delegation{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	sessionID = strings.TrimSpace(sessionID)
	if id == "" || sessionID == "" {
		return Delegation{}, fmt.Errorf("id and session_id are required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.delegations[id]
	if !ok {
		return Delegation{}, fmt.Errorf("delegation %q not found", id)
	}
	d.SessionID = sessionID
	d.SpawnPending = false
	if d.State == protocol.DelegationQueued {
		d.State = protocol.DelegationWorking
	}
	d.Version++
	d.UpdatedAt = time.Now().UTC()
	t.delegations[id] = d
	return d, nil
}

// TransitionDelegation applies a validated state change with optional CAS.
// actor must be the owner or the lead. expectedVersion > 0 enables CAS.
// Returns the updated item; on CAS/ownership failure returns DelegationConflictError.
func (t *Team) TransitionDelegation(id, actor string, to protocol.DelegationState, reason string, expectedVersion int) (Delegation, error) {
	if t == nil {
		return Delegation{}, fmt.Errorf("no team")
	}
	id = strings.TrimSpace(id)
	actor = strings.TrimSpace(actor)
	if id == "" {
		return Delegation{}, fmt.Errorf("id is required")
	}
	if actor == "" {
		return Delegation{}, fmt.Errorf("actor is required")
	}
	if to == "" {
		return Delegation{}, fmt.Errorf("state is required")
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return Delegation{}, fmt.Errorf("team is dissolved")
	}
	if _, ok := t.members[actor]; !ok {
		return Delegation{}, fmt.Errorf("actor is not on this team")
	}
	d, ok := t.lookupDelegationLocked(id)
	if !ok {
		return Delegation{}, fmt.Errorf("delegation %q not found", id)
	}
	// Normalize id to canonical key.
	id = d.ID
	if expectedVersion > 0 && d.Version != expectedVersion {
		return Delegation{}, &DelegationConflictError{
			Reason: fmt.Sprintf("version conflict: have %d, expected %d", d.Version, expectedVersion),
			Item:   d,
		}
	}
	// Ownership: owner or lead may transition; assignee session may self-report
	// blocked/review/working when linked.
	if !delegationActorAllowedLocked(t, d, actor, to) {
		return Delegation{}, &DelegationConflictError{
			Reason: fmt.Sprintf("delegation %q is owned by %s", id, d.OwnerSessionID),
			Item:   d,
		}
	}
	if d.State == to {
		// Idempotent — still bump nothing; return current.
		return d, nil
	}
	if !ValidDelegationTransition(d.State, to) {
		return Delegation{}, &DelegationTransitionError{
			From:   d.State,
			To:     to,
			Detail: fmt.Sprintf("illegal transition %s → %s for %s", d.State, to, id),
		}
	}
	// Cannot enter working while deps unmet (unless explicit bypass via reason "bypass_deps").
	if to == protocol.DelegationWorking && strings.TrimSpace(reason) != "bypass_deps" {
		if unmet := unmetDepsLocked(t, d.Deps); len(unmet) > 0 {
			return Delegation{}, &DelegationTransitionError{
				From:   d.State,
				To:     to,
				Detail: fmt.Sprintf("dependencies not done: %s", strings.Join(unmet, ", ")),
			}
		}
	}

	prev := d.State
	d.State = to
	if r := strings.TrimSpace(reason); r != "" && r != "bypass_deps" {
		if to == protocol.DelegationBlocked {
			d.BlockReason = r
		}
	}
	if to != protocol.DelegationBlocked {
		d.BlockReason = ""
	}
	if to == protocol.DelegationWorking && d.SessionID == "" {
		d.SpawnPending = true
	}
	if IsTerminalDelegation(to) {
		d.SpawnPending = false
	}
	d.Version++
	d.UpdatedAt = time.Now().UTC()
	t.delegations[id] = d

	// When reaching done, mark dependents that are fully unblocked as spawn-ready.
	if to == protocol.DelegationDone {
		t.markDependentsReadyLocked(id)
	}
	// Failed/canceled deps block dependents still queued.
	if to == protocol.DelegationFailed || to == protocol.DelegationCanceled {
		t.blockDependentsLocked(id, fmt.Sprintf("dependency %s is %s", id, to))
	}
	_ = prev
	return d, nil
}

func delegationActorAllowedLocked(t *Team, d Delegation, actor string, to protocol.DelegationState) bool {
	if actor == t.leadID || actor == d.OwnerSessionID {
		return true
	}
	// Linked child may self-report progress states.
	if d.SessionID != "" && actor == d.SessionID {
		switch to {
		case protocol.DelegationBlocked, protocol.DelegationReview,
			protocol.DelegationWorking, protocol.DelegationDone:
			return true
		}
	}
	return false
}

func unmetDepsLocked(t *Team, deps []string) []string {
	if len(deps) == 0 {
		return nil
	}
	var unmet []string
	for _, dep := range deps {
		d, ok := t.lookupDelegationLocked(dep)
		if !ok {
			unmet = append(unmet, dep)
			continue
		}
		if d.State != protocol.DelegationDone {
			unmet = append(unmet, d.ID)
		}
	}
	return unmet
}

func (t *Team) markDependentsReadyLocked(doneID string) {
	for id, d := range t.delegations {
		if d.State != protocol.DelegationQueued || d.SessionID != "" {
			continue
		}
		if !dependsOnLocked(d, doneID) {
			continue
		}
		if len(unmetDepsLocked(t, d.Deps)) > 0 {
			continue
		}
		d.SpawnPending = true
		d.Version++
		d.UpdatedAt = time.Now().UTC()
		t.delegations[id] = d
	}
}

func (t *Team) blockDependentsLocked(failedID, reason string) {
	for id, d := range t.delegations {
		if IsTerminalDelegation(d.State) {
			continue
		}
		if d.State != protocol.DelegationQueued && d.State != protocol.DelegationWorking {
			continue
		}
		if !dependsOnLocked(d, failedID) {
			continue
		}
		// Only block if still waiting (queued) or not yet terminal.
		if d.State == protocol.DelegationQueued {
			d.State = protocol.DelegationBlocked
			d.BlockReason = reason
			d.SpawnPending = false
			d.Version++
			d.UpdatedAt = time.Now().UTC()
			t.delegations[id] = d
		}
	}
}

func dependsOnLocked(d Delegation, depID string) bool {
	for _, dep := range d.Deps {
		if dep == depID {
			return true
		}
		// Also match if dep was given as session id equal to done's session — handled via lookup at create.
	}
	return false
}

// TakeSpawnPending returns and clears spawn-pending delegations owned by owner
// (or all when owner is empty). Caller should spawn children and link sessions.
func (t *Team) TakeSpawnPending(owner string) []Delegation {
	if t == nil {
		return nil
	}
	owner = strings.TrimSpace(owner)
	t.mu.Lock()
	defer t.mu.Unlock()
	var out []Delegation
	for id, d := range t.delegations {
		if !d.SpawnPending {
			continue
		}
		if owner != "" && d.OwnerSessionID != owner {
			continue
		}
		if d.SessionID != "" {
			d.SpawnPending = false
			t.delegations[id] = d
			continue
		}
		if len(unmetDepsLocked(t, d.Deps)) > 0 {
			d.SpawnPending = false
			t.delegations[id] = d
			continue
		}
		// Leave SpawnPending true until LinkDelegationSession / failed spawn clears it.
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ClearSpawnPending marks a delegation as no longer waiting to spawn (e.g. spawn failed).
func (t *Team) ClearSpawnPending(id string) {
	if t == nil {
		return
	}
	id = strings.TrimSpace(id)
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.delegations[id]
	if !ok {
		return
	}
	d.SpawnPending = false
	t.delegations[id] = d
}

// BindDelegationOnChildCompleted updates lifecycle from a terminal child status.
// completed + criteria → review; completed + no criteria → done; failed/canceled map 1:1.
func (t *Team) BindDelegationOnChildCompleted(sessionID string, status protocol.ChildStatus) (Delegation, bool) {
	if t == nil {
		return Delegation{}, false
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Delegation{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.lookupDelegationLocked(sessionID)
	if !ok {
		return Delegation{}, false
	}
	if IsTerminalDelegation(d.State) && d.State != protocol.DelegationReview {
		// Already terminal (canceled via interrupt etc.).
		return d, true
	}
	var to protocol.DelegationState
	switch status {
	case protocol.ChildStatusCompleted:
		if len(d.Criteria) > 0 {
			to = protocol.DelegationReview
		} else {
			to = protocol.DelegationDone
		}
	case protocol.ChildStatusBlocked:
		// Gate failure or explicit block — not terminal done.
		to = protocol.DelegationBlocked
	case protocol.ChildStatusFailed:
		to = protocol.DelegationFailed
	case protocol.ChildStatusCanceled:
		to = protocol.DelegationCanceled
	default:
		to = protocol.DelegationFailed
	}
	if d.State == to {
		return d, true
	}
	if !ValidDelegationTransition(d.State, to) {
		// Force terminal from working/blocked/review even if odd.
		if IsTerminalDelegation(to) || to == protocol.DelegationReview {
			// allow review from working/blocked
		} else {
			return d, true
		}
	}
	// Apply without ownership check (engine-driven).
	id := d.ID
	d.State = to
	d.SpawnPending = false
	d.BlockReason = ""
	d.Version++
	d.UpdatedAt = time.Now().UTC()
	t.delegations[id] = d
	if to == protocol.DelegationDone {
		t.markDependentsReadyLocked(id)
	}
	if to == protocol.DelegationFailed || to == protocol.DelegationCanceled {
		t.blockDependentsLocked(id, fmt.Sprintf("dependency %s is %s", id, to))
	}
	return d, true
}

// SetDelegationBlocked marks a live delegation blocked (e.g. needs_attention).
func (t *Team) SetDelegationBlocked(sessionID, reason string) (Delegation, bool) {
	if t == nil {
		return Delegation{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.lookupDelegationLocked(sessionID)
	if !ok || IsTerminalDelegation(d.State) || d.State == protocol.DelegationReview {
		return d, ok
	}
	if d.State == protocol.DelegationBlocked {
		return d, true
	}
	if !ValidDelegationTransition(d.State, protocol.DelegationBlocked) {
		return d, true
	}
	d.State = protocol.DelegationBlocked
	d.BlockReason = strings.TrimSpace(reason)
	d.Version++
	d.UpdatedAt = time.Now().UTC()
	t.delegations[d.ID] = d
	return d, true
}

// SetDelegationWorking clears blocked back to working when attention resolves.
func (t *Team) SetDelegationWorking(sessionID string) (Delegation, bool) {
	if t == nil {
		return Delegation{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d, ok := t.lookupDelegationLocked(sessionID)
	if !ok || d.State != protocol.DelegationBlocked {
		return d, ok
	}
	d.State = protocol.DelegationWorking
	d.BlockReason = ""
	d.Version++
	d.UpdatedAt = time.Now().UTC()
	t.delegations[d.ID] = d
	return d, true
}

func detectDelegationCycleLocked(t *Team, newID string, deps []string) error {
	// DFS from each dep; if we ever reach newID (when non-empty) or revisit, cycle.
	// For create, newID is empty — only check that deps' graphs are finite (always true)
	// and that we're not adding a self-reference.
	for _, dep := range deps {
		if newID != "" && dep == newID {
			return fmt.Errorf("delegation cannot depend on itself")
		}
		if seenCycleLocked(t, dep, newID, map[string]struct{}{}) {
			return fmt.Errorf("dependency cycle involving %q", dep)
		}
	}
	return nil
}

func seenCycleLocked(t *Team, id, target string, stack map[string]struct{}) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if target != "" && id == target {
		return true
	}
	if _, ok := stack[id]; ok {
		return true
	}
	d, ok := t.lookupDelegationLocked(id)
	if !ok {
		return false
	}
	stack[id] = struct{}{}
	for _, dep := range d.Deps {
		if seenCycleLocked(t, dep, target, stack) {
			return true
		}
	}
	delete(stack, id)
	return false
}

// clearDelegationsLocked drops all lifecycle objects (team GC).
func (t *Team) clearDelegationsLocked() {
	t.delegations = make(map[string]Delegation)
	t.delegSeq = 0
}
