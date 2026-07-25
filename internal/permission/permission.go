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

// Defaults: searching and reading are free; anything that mutates or
// executes asks. task is allowed so the root agent can spawn foreground
// children; DeriveChildRules denies task on child sessions.
func Defaults() Ruleset {
	return Ruleset{
		{Permission: "read", Pattern: "*", Action: Allow},
		{Permission: "glob", Pattern: "*", Action: Allow},
		{Permission: "grep", Pattern: "*", Action: Allow},
		{Permission: "edit", Pattern: "*", Action: Ask},
		{Permission: "write", Pattern: "*", Action: Ask},
		{Permission: "bash", Pattern: "*", Action: Ask},
		{Permission: "task", Pattern: "*", Action: Allow},
		{Permission: "webfetch", Pattern: "*", Action: Ask},
		{Permission: "todowrite", Pattern: "*", Action: Allow},
		{Permission: "sleep", Pattern: "*", Action: Allow},
		{Permission: "skill", Pattern: "*", Action: Allow},
		{Permission: "toolsearch", Pattern: "*", Action: Allow},
	}
}

// DeriveChildRules deep-copies parentLayers, appends only Deny rules from
// childExtra, then appends Deny task *. Does NOT copy parent session grants
// (caller passes opts.Rules only). Child Service.granted starts empty.
//
// Only Deny entries from childExtra are kept so a child cannot widen a
// parent Deny/Ask via Allow. Parent last-match-wins order is preserved,
// including parent allow-after-deny patterns.
func DeriveChildRules(parentLayers []Ruleset, childExtra ...Ruleset) []Ruleset {
	out := make([]Ruleset, 0, len(parentLayers)+len(childExtra)+1)
	for _, layer := range parentLayers {
		out = append(out, append(Ruleset(nil), layer...))
	}
	for _, extra := range childExtra {
		var denies Ruleset
		for _, rule := range extra {
			if rule.Action == Deny {
				denies = append(denies, rule)
			}
		}
		if len(denies) > 0 {
			out = append(out, denies)
		}
	}
	out = append(out, Ruleset{{Permission: "task", Pattern: "*", Action: Deny}})
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

// RejectedError is returned from an ask when the user rejects (or a rule
// denies). Message carries optional user feedback for the model.
type RejectedError struct {
	Message string
}

func (e *RejectedError) Error() string {
	if e.Message != "" {
		return "The user rejected this tool call with feedback: " + e.Message
	}
	return "The user rejected this tool call."
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
type Service struct {
	emit func(protocol.Event)

	mu      sync.Mutex
	base    []Ruleset
	granted Ruleset // session-scoped "always" grants
	pending map[string]*pending
	nextID  int
}

// New creates a Service. emit publishes events toward the frontend; base
// rulesets are evaluated in order (later wins), with session grants last.
func New(emit func(protocol.Event), base ...Ruleset) *Service {
	return &Service{emit: emit, base: base, pending: map[string]*pending{}}
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
		return &RejectedError{Message: "denied by permission rule"}
	}
	s.nextID++
	id := fmt.Sprintf("perm_%d", s.nextID)
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

// Reply resolves a pending ask. An "always" grant records session rules and
// retroactively resolves other pending asks that now evaluate to allow; a
// reject cascades to all sibling pending asks so a batch of parallel calls
// doesn't produce a prompt storm after one rejection.
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
	switch r.Decision {
	case protocol.DecisionAlways:
		patterns := p.always
		if len(patterns) == 0 {
			patterns = p.patterns
		}
		for _, pat := range patterns {
			s.granted = append(s.granted, Rule{Permission: p.permission, Pattern: pat, Action: Allow})
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
	p.ch <- r
	for _, other := range resolved {
		other.p.ch <- resolvedReply
	}
}

// evaluateLocked returns the worst-case action across the ask's patterns:
// any deny denies, any ask asks, otherwise allow.
func (s *Service) evaluateLocked(permission string, patterns []string) Action {
	sets := append(append([]Ruleset{}, s.base...), s.granted)
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
	return worst
}
