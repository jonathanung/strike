package permission

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// GrantScope bounds a runtime approval so it cannot silently become a
// workspace-wide allow. Session grants end with the session (and optional TTL).
// Path-prefix grants match only ask patterns under a prefix. Tool grants cover
// one permission name for any pattern. Command-class grants expand a small
// named class (git, go, test, build) into bash patterns.
type GrantScope string

const (
	ScopeSession      GrantScope = "session"
	ScopePathPrefix   GrantScope = "path-prefix"
	ScopeTool         GrantScope = "tool"
	ScopeCommandClass GrantScope = "command-class"
)

// ValidGrantScope reports whether s is a known scope.
func ValidGrantScope(s GrantScope) bool {
	switch s {
	case ScopeSession, ScopePathPrefix, ScopeTool, ScopeCommandClass:
		return true
	}
	return false
}

// ScopedGrant is a runtime allow approval with optional wall-clock TTL.
// Zero ExpiresAt means no wall-clock expiry (still session-bound for
// ScopeSession; project persistence is a separate DecisionProject path).
type ScopedGrant struct {
	// ID is optional; assigned when empty on Grant().
	ID string
	// Permission is the tool permission name (e.g. "bash", "edit"). Required
	// except when Scope is command-class (implied bash).
	Permission string
	// Pattern depends on scope:
	//   session: doublestar pattern (default "*")
	//   path-prefix: directory/file prefix (e.g. "internal/" or "/tmp/work")
	//   tool: ignored (forced to "*")
	//   command-class: class name (git|go|test|build) or a custom bash glob
	Pattern string
	Scope   GrantScope
	// TTL is wall-clock lifetime from grant time. Zero means no wall TTL.
	// ExpiresAt, when set, wins over TTL (tests/clock injection).
	TTL       time.Duration
	ExpiresAt time.Time
}

// ErrGrantWidens is returned when a scoped grant would override a hard Deny
// from parent/baseline layers (regression guard for closed #49).
var ErrGrantWidens = fmt.Errorf("permission: scoped grant would widen a deny")

// commandClassPatterns maps ScopeCommandClass names to bash globs.
var commandClassPatterns = map[string][]string{
	"git":   {"git *"},
	"go":    {"go *"},
	"test":  {"go test *", "make test*", "npm test*", "npm run test*", "cargo test*"},
	"build": {"go build *", "make *", "npm run build*", "cargo build*"},
}

// CommandClassPatterns returns the bash patterns for a shipped class name.
// Unknown names are treated as a single literal bash glob (the name itself).
func CommandClassPatterns(class string) []string {
	class = strings.ToLower(strings.TrimSpace(class))
	if class == "" {
		return nil
	}
	if pats, ok := commandClassPatterns[class]; ok {
		out := make([]string, len(pats))
		copy(out, pats)
		return out
	}
	return []string{class}
}

// Grant records a scoped allow approval. Fails if the grant would make any
// covered permission/pattern more permissive than a baseline Deny (does not
// silently widen parent permissions). now is the grant clock (tests inject).
func (s *Service) Grant(g ScopedGrant, now time.Time) error {
	if s == nil {
		return fmt.Errorf("permission: nil service")
	}
	if now.IsZero() {
		now = time.Now()
	}
	normalized, err := normalizeScopedGrant(g, now)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)

	// Baseline without session always-grants or scoped grants: a Deny in
	// defaults/config/project/agent/modeLate/phase must not be widened.
	baseline := s.baselineForGrantLocked()
	for _, cand := range normalized.candidates {
		if Evaluate(cand.Permission, cand.Pattern, baseline...) == Deny {
			return fmt.Errorf("%w: %s %s is denied by parent policy",
				ErrGrantWidens, cand.Permission, cand.Pattern)
		}
	}

	s.scoped = append(s.scoped, normalized)
	s.nextGrant++
	return nil
}

// ActiveGrants returns a copy of non-expired scoped grants at now.
func (s *Service) ActiveGrants(now time.Time) []ScopedGrant {
	if s == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredLocked(now)
	out := make([]ScopedGrant, 0, len(s.scoped))
	for _, g := range s.scoped {
		out = append(out, g.public())
	}
	return out
}

// SetClock overrides the wall clock used for TTL expiry (tests). nil resets.
func (s *Service) SetClock(now func() time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.clock = now
	s.mu.Unlock()
}

func (s *Service) now() time.Time {
	if s.clock != nil {
		return s.clock()
	}
	return time.Now()
}

// scopedGrant is the internal runtime form.
type scopedGrant struct {
	id         string
	scope      GrantScope
	expiresAt  time.Time
	candidates []Rule // allow rules this grant contributes
}

func (g scopedGrant) public() ScopedGrant {
	pat := "*"
	perm := ""
	if len(g.candidates) > 0 {
		perm = g.candidates[0].Permission
		pat = g.candidates[0].Pattern
		if g.scope == ScopeCommandClass && len(g.candidates) > 1 {
			// Reconstruct class-ish pattern from first candidate only for display.
			pat = g.candidates[0].Pattern
		}
	}
	return ScopedGrant{
		ID:         g.id,
		Permission: perm,
		Pattern:    pat,
		Scope:      g.scope,
		ExpiresAt:  g.expiresAt,
	}
}

func (g scopedGrant) expired(now time.Time) bool {
	return !g.expiresAt.IsZero() && !now.Before(g.expiresAt)
}

func (s *Service) pruneExpiredLocked(now time.Time) {
	if len(s.scoped) == 0 {
		return
	}
	dst := s.scoped[:0]
	for _, g := range s.scoped {
		if g.expired(now) {
			continue
		}
		dst = append(dst, g)
	}
	// Clear trailing refs when shrinking.
	for i := len(dst); i < len(s.scoped); i++ {
		s.scoped[i] = scopedGrant{}
	}
	s.scoped = dst
}

func (s *Service) scopedRulesLocked() Ruleset {
	var out Ruleset
	for _, g := range s.scoped {
		out = append(out, g.candidates...)
	}
	return out
}

// baselineForGrantLocked is layers that define the non-widening ceiling for
// new scoped grants: everything except session always-grants and scoped grants.
func (s *Service) baselineForGrantLocked() []Ruleset {
	out := make([]Ruleset, 0, len(s.base)+4)
	out = append(out, s.base...)
	out = append(out, s.project, s.agent, s.modeLate, s.phase)
	return out
}

func normalizeScopedGrant(g ScopedGrant, now time.Time) (scopedGrant, error) {
	if !ValidGrantScope(g.Scope) {
		if g.Scope == "" {
			g.Scope = ScopeSession
		} else {
			return scopedGrant{}, fmt.Errorf("permission: unknown grant scope %q", g.Scope)
		}
	}
	exp := g.ExpiresAt
	if exp.IsZero() && g.TTL > 0 {
		exp = now.Add(g.TTL)
	}
	id := strings.TrimSpace(g.ID)

	var cands []Rule
	switch g.Scope {
	case ScopeTool:
		perm := strings.TrimSpace(g.Permission)
		if perm == "" {
			return scopedGrant{}, fmt.Errorf("permission: tool scope requires permission name")
		}
		cands = []Rule{{Permission: perm, Pattern: "*", Action: Allow}}
	case ScopePathPrefix:
		perm := strings.TrimSpace(g.Permission)
		if perm == "" {
			return scopedGrant{}, fmt.Errorf("permission: path-prefix scope requires permission name")
		}
		prefix := strings.TrimSpace(g.Pattern)
		if prefix == "" {
			return scopedGrant{}, fmt.Errorf("permission: path-prefix scope requires pattern prefix")
		}
		// Store as doublestar: prefix and prefix/** so files under the tree match.
		cands = pathPrefixRules(perm, prefix)
	case ScopeCommandClass:
		class := strings.TrimSpace(g.Pattern)
		if class == "" {
			class = strings.TrimSpace(g.Permission)
		}
		if class == "" {
			return scopedGrant{}, fmt.Errorf("permission: command-class scope requires class name or pattern")
		}
		for _, pat := range CommandClassPatterns(class) {
			cands = append(cands, Rule{Permission: "bash", Pattern: pat, Action: Allow})
		}
	default: // ScopeSession
		perm := strings.TrimSpace(g.Permission)
		if perm == "" {
			return scopedGrant{}, fmt.Errorf("permission: session scope requires permission name")
		}
		pat := g.Pattern
		if pat == "" {
			pat = "*"
		}
		cands = []Rule{{Permission: perm, Pattern: pat, Action: Allow}}
	}
	if id == "" {
		id = fmt.Sprintf("grant_%d", now.UnixNano())
	}
	return scopedGrant{id: id, scope: g.Scope, expiresAt: exp, candidates: cands}, nil
}

func pathPrefixRules(permission, prefix string) Ruleset {
	prefix = path.Clean(strings.TrimSpace(prefix))
	// Keep trailing semantics: "internal" and "internal/" both cover the tree.
	prefix = strings.TrimSuffix(prefix, "/")
	if prefix == "." || prefix == "" {
		return Ruleset{{Permission: permission, Pattern: "*", Action: Allow}}
	}
	// Exact path + descendants. Use doublestar-friendly forms.
	return Ruleset{
		{Permission: permission, Pattern: prefix, Action: Allow},
		{Permission: permission, Pattern: prefix + "/**", Action: Allow},
		{Permission: permission, Pattern: prefix + "/*", Action: Allow},
	}
}
