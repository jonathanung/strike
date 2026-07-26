// Package permission implements ordered allow/ask/deny rulesets with
// last-match-wins evaluation, and the ask service that suspends a tool call
// until the user replies. Used by internal/engine (the ask service),
// internal/config (Ruleset is a config field), and cmd/strike (layering
// CLI/config rules). internal/tui never imports it — a pending ask reaches
// the frontend only as a protocol.PermissionAsked event.
package permission

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/bmatcuk/doublestar/v4"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

type Action string

const (
	Allow Action = "allow"
	Ask   Action = "ask"
	Deny  Action = "deny"
)

type Rule struct {
	Permission string `json:"permission"` // e.g. "bash", "edit", or "*"
	Pattern    string `json:"pattern"`    // glob over the ask pattern, "*" for any
	Action     Action `json:"action"`
}

type Ruleset []Rule

// Known permission names tools actually Ask with, plus "*".
var knownPermissions = map[string]struct{}{
	"*": {}, "read": {}, "glob": {}, "grep": {}, "edit": {}, "write": {},
	"bash": {}, "task": {}, "task_status": {}, "task_read": {}, "task_message": {},
	"task_interrupt": {}, "webfetch": {}, "todowrite": {}, "todoread": {},
	"memory_write": {}, "memory_read": {}, "issue_write": {}, "issue_read": {},
	"sleep": {}, "skill": {}, "question": {}, "toolsearch": {}, "hook": {},
	"enter_plan_mode": {}, "exit_plan_mode": {}, "phase_done": {},
	"mcp": {},
}

func ValidAction(a Action) bool {
	switch a {
	case Allow, Ask, Deny:
		return true
	}
	return false
}

// ValidateRuleset rejects empty permission names, unknown permission
// names, and invalid actions. Empty pattern is allowed (matches as "*").
func ValidateRuleset(rs Ruleset) error {
	for i, r := range rs {
		if r.Permission == "" {
			return fmt.Errorf("rule %d: empty permission name", i)
		}
		if _, ok := knownPermissions[r.Permission]; !ok {
			return fmt.Errorf("rule %d: unknown permission %q", i, r.Permission)
		}
		if !ValidAction(r.Action) {
			return fmt.Errorf("rule %d: invalid action %q", i, r.Action)
		}
	}
	return nil
}

// Defaults: searching and reading are free; anything that mutates or
// executes asks. task is allowed so agents can spawn children while
// depth remains below MaxChildDepth; DeriveChildRules denies task only
// when the child cannot nest further.
func Defaults() Ruleset {
	return Ruleset{
		{Permission: "read", Pattern: "*", Action: Allow},
		{Permission: "glob", Pattern: "*", Action: Allow},
		{Permission: "grep", Pattern: "*", Action: Allow},
		{Permission: "edit", Pattern: "*", Action: Ask},
		{Permission: "write", Pattern: "*", Action: Ask},
		{Permission: "bash", Pattern: "*", Action: Ask},
		// Project-local shell hooks execute arbitrary code — gate first run.
		{Permission: "hook", Pattern: "*", Action: Ask},
		{Permission: "task", Pattern: "*", Action: Allow},
		{Permission: "task_status", Pattern: "*", Action: Allow},
		{Permission: "task_read", Pattern: "*", Action: Allow},
		{Permission: "task_message", Pattern: "*", Action: Allow},
		{Permission: "task_interrupt", Pattern: "*", Action: Allow},
		{Permission: "webfetch", Pattern: "*", Action: Ask},
		{Permission: "todowrite", Pattern: "*", Action: Allow},
		{Permission: "todoread", Pattern: "*", Action: Allow},
		{Permission: "memory_write", Pattern: "*", Action: Allow},
		{Permission: "memory_read", Pattern: "*", Action: Allow},
		{Permission: "issue_write", Pattern: "*", Action: Allow},
		{Permission: "issue_read", Pattern: "*", Action: Allow},
		{Permission: "sleep", Pattern: "*", Action: Allow},
		{Permission: "skill", Pattern: "*", Action: Allow},
		{Permission: "question", Pattern: "*", Action: Allow},
		{Permission: "enter_plan_mode", Pattern: "*", Action: Allow},
		{Permission: "exit_plan_mode", Pattern: "*", Action: Allow},
		{Permission: "phase_done", Pattern: "*", Action: Allow},
		{Permission: "toolsearch", Pattern: "*", Action: Allow},
		// External MCP tools can run arbitrary server-side code — always ask.
		{Permission: "mcp", Pattern: "*", Action: Ask},
	}
}

// DenyOnly returns a defensive copy of rs containing only Deny rules.
// Used so child agent profiles can further restrict without widening
// parent Deny/Ask via Allow (AG3).
func DenyOnly(rs Ruleset) Ruleset {
	var out Ruleset
	for _, rule := range rs {
		if rule.Action == Deny {
			out = append(out, rule)
		}
	}
	return out
}

// DeriveChildRules deep-copies parentLayers and appends only Deny rules from
// childExtra. When denyTask is true (child depth has reached MaxChildDepth),
// appends Deny task * so the child cannot spawn further. Does NOT copy parent
// session grants (caller passes opts.Rules plus the parent's active agent
// profile). Child Service.granted starts empty.
//
// Only Deny entries from childExtra are kept so a child cannot widen a
// parent Deny/Ask via Allow. Parent last-match-wins order is preserved,
// including parent allow-after-deny patterns.
func DeriveChildRules(parentLayers []Ruleset, denyTask bool, childExtra ...Ruleset) []Ruleset {
	out := make([]Ruleset, 0, len(parentLayers)+len(childExtra)+1)
	for _, layer := range parentLayers {
		out = append(out, append(Ruleset(nil), layer...))
	}
	for _, extra := range childExtra {
		if denies := DenyOnly(extra); len(denies) > 0 {
			out = append(out, denies)
		}
	}
	if denyTask {
		out = append(out, Ruleset{{Permission: "task", Pattern: "*", Action: Deny}})
	}
	return out
}

func (r Rule) matches(permission, pattern string) bool {
	if r.Permission != "*" && r.Permission != permission {
		return false
	}
	if r.Pattern == "*" || r.Pattern == "" {
		return true
	}
	ok, err := doublestar.Match(r.Pattern, pattern)
	return err == nil && ok
}

// Evaluate flattens the rulesets in order and returns the action of the last
// matching rule, so later layers (project config, session grants) override
// earlier ones. Default is Ask.
func Evaluate(permission, pattern string, sets ...Ruleset) Action {
	action := Ask
	for _, set := range sets {
		for _, rule := range set {
			if rule.matches(permission, pattern) {
				action = rule.Action
			}
		}
	}
	return action
}

// DeniedError is returned when a hard ruleset/profile deny blocks a tool
// call without prompting. Reason is optional detail for the model.
type DeniedError struct {
	Reason string
}

func (e *DeniedError) Error() string {
	return protocol.ToolFeedbackPermissionDenied(e.Reason)
}

// RejectedError is returned when the user rejects a permission ask.
// Message carries optional user feedback for the model.
type RejectedError struct {
	Message string
}

func (e *RejectedError) Error() string {
	return protocol.ToolFeedbackUserRejected(e.Message)
}

type pending struct {
	permission  string
	patterns    []string
	always      []string
	correlation protocol.Correlation
	ch          chan protocol.PermissionReply

	// announced is set after PermissionAsked emission returns. Reply queues
	// terminal PermissionResolved onto deferredDecision until then so a
	// blocked or reentrant emitter cannot publish Resolved first.
	announced        bool
	hasDeferred      bool
	deferredDecision protocol.Decision
}

// resolvedEmission is a PermissionResolved to publish outside the mutex.
type resolvedEmission struct {
	id          string
	correlation protocol.Correlation
	decision    protocol.Decision
}

// Service resolves asks against the configured rulesets, suspending on a
// channel when user input is needed. It is safe for concurrent use.
//
// Evaluation order (last-match-wins):
//
//	base layers (defaults → config → optional dangerous allow-all)
//	→ project runtime grants (DecisionProject; also persisted when a
//	  ProjectPersister is set)
//	→ active agent profile
//	→ session always grants (DecisionAlways)
//	→ mode late (plan hard-denies)
//	→ active workflow phase profile
//	→ mode ask-upgrade (yolo / accept-edits): Ask→Allow only; never widens Deny
//
// Agent is evaluated after the optional dangerous allow-all, so role denies
// still apply under --dangerously-skip-permissions (hard ceiling for personas).
// Mode late and phase are last among rulesets so plan / workflow hard-denies
// (e.g. write/edit) cannot be widened by session always-grants. Yolo and
// accept-edits only upgrade remaining Ask results to Allow so explicit Deny
// rules always hold.
type Service struct {
	emit func(protocol.Event)

	mu       sync.Mutex
	base     []Ruleset
	project  Ruleset // runtime project-scoped grants (DecisionProject)
	agent    Ruleset // active agent profile; evaluated after project, before granted
	granted  Ruleset // session-scoped "always" grants (DecisionAlways)
	modeLate Ruleset // permission-mode hard-denies (plan); after granted, before phase
	phase    Ruleset // active workflow phase profile; last ruleset, hard ceiling
	permMode protocol.PermissionMode
	pending  map[string]*pending
	nextID   int

	// persistProject, when set, is called outside the mutex after a project
	// grant is recorded in memory. Failures are ignored so the in-session
	// grant still applies; the caller may log them.
	persistProject func(Rule) error
}

// New creates a Service. emit publishes events toward the frontend; base
// rulesets are evaluated in order (later wins), then project grants, the
// active agent profile, session always grants, then the workflow phase profile.
func New(emit func(protocol.Event), base ...Ruleset) *Service {
	return &Service{emit: emit, base: base, pending: map[string]*pending{}}
}

// SetProjectPersister registers an optional hook invoked after a
// DecisionProject grant is applied in memory. fn may be nil to clear.
// Safe to call at any time; concurrent with Ask/Reply.
func (s *Service) SetProjectPersister(fn func(Rule) error) {
	s.mu.Lock()
	s.persistProject = fn
	s.mu.Unlock()
}

// SetAgentRules replaces the active agent profile and clears session
// always-grants so prior role grants cannot widen the new role. Project
// grants are kept (they are workspace policy, not role-scoped). An empty
// or nil ruleset clears the agent layer. Defensively copies rs.
//
// Also rejects any pending asks (API hygiene; production SelectAgent is
// blocked mid-turn so pendings are normally empty). Emit/reply outside lock
// like Reply's reject cascade.
func (s *Service) SetAgentRules(rs Ruleset) {
	s.replaceProfileLocked(func() {
		s.agent = append(Ruleset(nil), rs...)
		s.granted = nil
	}, "agent changed")
}

// SetPhaseRules replaces the active workflow phase permission profile.
// An empty or nil ruleset clears the phase layer. Defensively copies rs.
// Does not clear session grants; phase is evaluated last so its denies still
// win. Rejects any pending asks (same hygiene as SetAgentRules).
func (s *Service) SetPhaseRules(rs Ruleset) {
	s.replaceProfileLocked(func() {
		s.phase = append(Ruleset(nil), rs...)
	}, "phase changed")
}

// SetPermissionMode installs the tool-permission posture for mode.
// Plan places write/edit denies after session always-grants so they cannot be
// widened. Yolo and accept-edits upgrade Ask→Allow after full evaluation
// without overriding Deny. Default clears mode effects. Rejects pending asks
// (same hygiene as SetAgentRules).
func (s *Service) SetPermissionMode(mode protocol.PermissionMode) {
	mode = mode.Normalize()
	late := ModeLateRules(mode)
	s.replaceProfileLocked(func() {
		s.permMode = mode
		s.modeLate = late
	}, "permission mode changed")
}

// ModeLateRules returns post-grant hard-deny rules for a posture (plan only).
func ModeLateRules(mode protocol.PermissionMode) Ruleset {
	if mode.Normalize() == protocol.PermissionModePlan {
		return Ruleset{
			{Permission: "write", Pattern: "*", Action: Deny},
			{Permission: "edit", Pattern: "*", Action: Deny},
		}
	}
	return nil
}

// ApplyMode upgrades an evaluated action for yolo / accept-edits. Deny and
// Allow are unchanged; only Ask may become Allow.
func ApplyMode(mode protocol.PermissionMode, permission string, action Action) Action {
	if action != Ask {
		return action
	}
	switch mode.Normalize() {
	case protocol.PermissionModeYolo:
		return Allow
	case protocol.PermissionModeAcceptEdits:
		if permission == "edit" || permission == "write" {
			return Allow
		}
	}
	return action
}

// SeedAlwaysGrants replaces session-scoped DecisionAlways grants (resume).
// Defensively copies rs. Does not touch pending asks or other layers.
func (s *Service) SeedAlwaysGrants(rs Ruleset) {
	s.mu.Lock()
	s.granted = append(Ruleset(nil), rs...)
	s.mu.Unlock()
}

// replaceProfileLocked applies mut under the service lock, then rejects all
// pending asks with message outside the lock.
func (s *Service) replaceProfileLocked(mut func(), message string) {
	s.mu.Lock()
	mut()

	type cascaded struct {
		id string
		p  *pending
	}
	var rejected []cascaded
	for id, p := range s.pending {
		delete(s.pending, id)
		rejected = append(rejected, cascaded{id: id, p: p})
	}

	var emitNow []resolvedEmission
	for _, other := range rejected {
		if other.p.announced {
			emitNow = append(emitNow, resolvedEmission{
				id:          other.id,
				correlation: other.p.correlation,
				decision:    protocol.DecisionReject,
			})
			continue
		}
		other.p.hasDeferred = true
		other.p.deferredDecision = protocol.DecisionReject
	}
	s.mu.Unlock()

	for _, em := range emitNow {
		s.emit(protocol.PermissionResolved{
			Correlation: em.correlation,
			RequestID:   em.id,
			Decision:    em.decision,
		})
	}
	reply := protocol.PermissionReply{
		Decision: protocol.DecisionReject,
		Message:  message,
	}
	for _, other := range rejected {
		other.p.ch <- reply
	}
}

// Ask implements tool.Context.Ask. It blocks until the permission resolves.
func (s *Service) Ask(ctx context.Context, req tool.AskRequest) error {
	return s.ask(ctx, req, protocol.Correlation{})
}

// AskWithCorrelation is Ask with protocol correlation attached to the
// PermissionAsked/PermissionResolved events for this pending request.
func (s *Service) AskWithCorrelation(ctx context.Context, req tool.AskRequest, corr protocol.Correlation) error {
	return s.ask(ctx, req, corr)
}

// ask is the shared implementation for Ask and AskWithCorrelation.
func (s *Service) ask(ctx context.Context, req tool.AskRequest, corr protocol.Correlation) error {
	s.mu.Lock()
	action := s.evaluateLocked(req.Permission, req.Patterns)
	switch action {
	case Allow:
		s.mu.Unlock()
		return nil
	case Deny:
		s.mu.Unlock()
		return &DeniedError{Reason: "a permission rule matched"}
	}
	s.nextID++
	// Session-scope IDs so concurrent parent/child engines never collide when
	// replies fan out across services.
	id := fmt.Sprintf("perm_%d", s.nextID)
	if sid := strings.TrimSpace(corr.SessionID); sid != "" {
		id = sid + ":" + id
	}
	p := &pending{
		permission:  req.Permission,
		patterns:    req.Patterns,
		always:      req.Always,
		correlation: corr,
		ch:          make(chan protocol.PermissionReply, 1),
	}
	s.pending[id] = p
	s.mu.Unlock()

	s.emit(protocol.PermissionAsked{
		Correlation: corr,
		RequestID:   id,
		Permission:  req.Permission,
		Patterns:    req.Patterns,
		Always:      req.Always,
		Metadata:    req.Metadata,
	})

	// Mark announced only after PermissionAsked returns. Any Reply that ran
	// while unannounced left a deferred resolution for us to emit now.
	s.mu.Lock()
	p.announced = true
	var deferred *resolvedEmission
	if p.hasDeferred {
		deferred = &resolvedEmission{
			id:          id,
			correlation: p.correlation,
			decision:    p.deferredDecision,
		}
		p.hasDeferred = false
	}
	s.mu.Unlock()
	if deferred != nil {
		s.emit(protocol.PermissionResolved{
			Correlation: deferred.correlation,
			RequestID:   deferred.id,
			Decision:    deferred.decision,
		})
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return ctx.Err()
	case reply := <-p.ch:
		if reply.Decision == protocol.DecisionReject {
			return &RejectedError{Message: reply.Message}
		}
		return nil
	}
}

// Reply resolves a pending ask. Session (always) and project grants record
// rules and retroactively resolve other pending asks that now evaluate to
// allow; a reject cascades to all sibling pending asks so a batch of parallel
// calls doesn't produce a prompt storm after one rejection.
//
// PermissionResolved is emitted immediately only for asks whose
// PermissionAsked has already finished. Otherwise the resolution is queued on
// the pending entry and emitted by ask after the opening event returns. All
// emitter calls run outside the mutex; Reply never waits on announcement.
func (s *Service) Reply(r protocol.PermissionReply) {
	s.mu.Lock()
	p, ok := s.pending[r.RequestID]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.pending, r.RequestID)

	type cascaded struct {
		id string
		p  *pending
	}
	var resolved []cascaded
	var resolvedReply protocol.PermissionReply
	var projectRules Ruleset
	persist := s.persistProject
	switch r.Decision {
	case protocol.DecisionAlways, protocol.DecisionProject:
		patterns := p.always
		if len(patterns) == 0 {
			patterns = p.patterns
		}
		for _, pat := range patterns {
			rule := Rule{Permission: p.permission, Pattern: pat, Action: Allow}
			if r.Decision == protocol.DecisionProject {
				s.project = append(s.project, rule)
				projectRules = append(projectRules, rule)
			} else {
				s.granted = append(s.granted, rule)
			}
		}
		for id, other := range s.pending {
			if s.evaluateLocked(other.permission, other.patterns) == Allow {
				delete(s.pending, id)
				resolved = append(resolved, cascaded{id: id, p: other})
			}
		}
		resolvedReply = protocol.PermissionReply{Decision: protocol.DecisionOnce}
	case protocol.DecisionReject:
		for id, other := range s.pending {
			delete(s.pending, id)
			resolved = append(resolved, cascaded{id: id, p: other})
		}
		resolvedReply = protocol.PermissionReply{Decision: protocol.DecisionReject}
	}

	var emitNow []resolvedEmission
	collect := func(id string, pend *pending, decision protocol.Decision) {
		if pend.announced {
			emitNow = append(emitNow, resolvedEmission{
				id:          id,
				correlation: pend.correlation,
				decision:    decision,
			})
			return
		}
		pend.hasDeferred = true
		pend.deferredDecision = decision
	}
	collect(r.RequestID, p, r.Decision)
	for _, other := range resolved {
		collect(other.id, other.p, resolvedReply.Decision)
	}
	s.mu.Unlock()

	// Emit all announced resolutions before waking any waiter so consumers
	// observe PermissionResolved before Ask returns.
	for _, em := range emitNow {
		s.emit(protocol.PermissionResolved{
			Correlation: em.correlation,
			RequestID:   em.id,
			Decision:    em.decision,
		})
	}
	if persist != nil {
		for _, rule := range projectRules {
			_ = persist(rule)
		}
	}
	p.ch <- r
	for _, other := range resolved {
		other.p.ch <- resolvedReply
	}
}

// evaluateLocked returns the worst-case action across the ask's patterns:
// any deny denies, any ask asks, otherwise allow. Permission mode may then
// upgrade Ask→Allow (yolo / accept-edits) without widening Deny.
func (s *Service) evaluateLocked(permission string, patterns []string) Action {
	sets := append([]Ruleset{}, s.base...)
	sets = append(sets, s.project, s.agent, s.granted, s.modeLate, s.phase)
	worst := Allow
	if len(patterns) == 0 {
		patterns = []string{"*"}
	}
	for _, pat := range patterns {
		switch Evaluate(permission, pat, sets...) {
		case Deny:
			return Deny
		case Ask:
			worst = Ask
		}
	}
	return ApplyMode(s.permMode, permission, worst)
}
