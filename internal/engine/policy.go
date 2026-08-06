package engine

import (
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// Delegation policy modes (session.delegationPolicy.mode).
const (
	// PolicyOff disables worthiness checks (always allow spawn; reason recorded).
	PolicyOff = "off"
	// PolicyAdvise records the preferred action but always allows spawn.
	PolicyAdvise = "advise"
	// PolicyEnforce blocks soft-local and hard-deny decisions (default).
	PolicyEnforce = "enforce"
)

// Policy actions returned by EvaluateDelegationPolicy.
const (
	PolicyActionDelegate = "delegate"
	PolicyActionLocal    = "local"
	PolicyActionDeny     = "deny"
)

// Default policy thresholds when config leaves fields at zero.
const (
	DefaultPolicyTinyPromptRunes = 280
	DefaultPolicyMaxPathsLocal   = 1
	// DefaultPolicyMaxLiveChildren is used only when MaxLiveChildren is
	// explicitly enabled via a positive config value; zero means unlimited.
	DefaultPolicyMaxLiveChildren = 0
)

// DelegationPolicyConfig is the engine-side snapshot of session.delegationPolicy.
type DelegationPolicyConfig struct {
	// Mode is off|advise|enforce. Empty defaults to enforce.
	Mode string
	// TinyPromptRunes: bare prompts at or below this prefer local (0 → default).
	TinyPromptRunes int
	// MaxPathsLocal: bare tasks with at most this many scoped paths prefer local.
	// 0 → default 1. Negative disables the path-count soft rule.
	MaxPathsLocal int
	// MaxLiveChildren hard-denies when live children reach this count.
	// 0 = unlimited (no new ceiling beyond depth / MaxDelegations).
	MaxLiveChildren int
}

// NormalizeDelegationPolicy applies defaults and clamps mode.
// Empty Mode stays off so zero-value engine.Options keep legacy always-spawn
// behavior (tests and embedders). The CLI composition root sets mode=enforce
// when config omits session.delegationPolicy (product default).
func NormalizeDelegationPolicy(c DelegationPolicyConfig) DelegationPolicyConfig {
	mode := strings.ToLower(strings.TrimSpace(c.Mode))
	switch mode {
	case PolicyOff, PolicyAdvise, PolicyEnforce:
	case "on", "true", "yes":
		mode = PolicyEnforce
	case "":
		mode = PolicyOff
	default:
		mode = PolicyEnforce
	}
	c.Mode = mode
	if c.TinyPromptRunes == 0 {
		c.TinyPromptRunes = DefaultPolicyTinyPromptRunes
	}
	if c.TinyPromptRunes < 0 {
		c.TinyPromptRunes = 0
	}
	if c.MaxPathsLocal == 0 {
		c.MaxPathsLocal = DefaultPolicyMaxPathsLocal
	}
	if c.MaxLiveChildren < 0 {
		c.MaxLiveChildren = 0
	}
	return c
}

// DefaultDelegationPolicy is the product default when config omits the block.
func DefaultDelegationPolicy() DelegationPolicyConfig {
	return NormalizeDelegationPolicy(DelegationPolicyConfig{Mode: PolicyEnforce})
}

// PolicyInput is the pure-function input for delegation-worthiness (#876).
type PolicyInput struct {
	Config DelegationPolicyConfig
	// Force is an explicit override (task.force_delegate). Bypasses soft local
	// only — never hard safety ceilings.
	Force bool

	Prompt           string
	AgentPin         string
	Specialty        string
	Capabilities     []string
	Criteria         []string
	Deps             []string
	Verify           []tool.VerifyGate
	Paths            []string // allowed + required paths from context_bundle
	Depth            int
	MaxDepth         int
	LiveChildren     int
	TotalDelegations int
	// MaxDelegations hard ceiling (0 → MaxDelegations package const).
	MaxDelegations int
	// OverlapPaths are requested paths that conflict with other live agents.
	OverlapPaths []string
	// BudgetExhausted is true when the session outer cost/budget envelope is spent.
	BudgetExhausted bool
}

// PolicyDecision is the worthiness outcome (action + structured reason).
type PolicyDecision struct {
	// Action is delegate|local|deny.
	Action string
	// Reason is a concise structured explanation for logs/UI/metadata.
	Reason string
	// Hard is true when Force cannot override (safety ceiling).
	Hard bool
	// Codes are stable machine-readable tags (tiny, overlap, depth_ceiling, …).
	Codes []string
	// Preferred is the heuristic preference before mode/force adjustment
	// (delegate|local|deny). Useful for advise mode and metrics.
	Preferred string
	// Overridden is true when Force converted local → delegate.
	Overridden bool
}

// DelegationPolicyMetrics counts policy outcomes for cost/latency comparison.
type DelegationPolicyMetrics struct {
	Delegate  atomic.Int64
	Local     atomic.Int64
	Deny      atomic.Int64
	Override  atomic.Int64
	AdviseRun atomic.Int64 // advise-mode evaluations that preferred local but spawned
}

// Snapshot returns a plain copy of counters.
func (m *DelegationPolicyMetrics) Snapshot() (delegate, local, deny, override, adviseLocal int64) {
	if m == nil {
		return 0, 0, 0, 0, 0
	}
	return m.Delegate.Load(), m.Local.Load(), m.Deny.Load(), m.Override.Load(), m.AdviseRun.Load()
}

func (m *DelegationPolicyMetrics) record(d PolicyDecision, mode string) {
	if m == nil {
		return
	}
	switch d.Action {
	case PolicyActionDeny:
		m.Deny.Add(1)
	case PolicyActionLocal:
		m.Local.Add(1)
	default:
		m.Delegate.Add(1)
	}
	if d.Overridden {
		m.Override.Add(1)
	}
	if mode == PolicyAdvise && d.Preferred == PolicyActionLocal && d.Action == PolicyActionDelegate {
		m.AdviseRun.Add(1)
	}
}

// EvaluateDelegationPolicy decides whether a task/delegate spawn is worthwhile.
// Pure and deterministic: equal inputs always yield equal decisions.
//
// Order: hard ceilings → soft local heuristics → default delegate → force/mode.
func EvaluateDelegationPolicy(in PolicyInput) PolicyDecision {
	cfg := NormalizeDelegationPolicy(in.Config)
	maxDepth := in.MaxDepth
	if maxDepth <= 0 {
		maxDepth = 1
	}
	maxDeleg := in.MaxDelegations
	if maxDeleg <= 0 {
		maxDeleg = MaxDelegations
	}

	// --- Hard ceilings (never overridable) ---
	if in.Depth >= maxDepth {
		return PolicyDecision{
			Action:    PolicyActionDeny,
			Preferred: PolicyActionDeny,
			Hard:      true,
			Codes:     []string{"depth_ceiling"},
			Reason: fmt.Sprintf(
				"policy=%s action=deny hard=depth_ceiling depth=%d max=%d",
				cfg.Mode, in.Depth, maxDepth,
			),
		}
	}
	if cfg.MaxLiveChildren > 0 && in.LiveChildren >= cfg.MaxLiveChildren {
		return PolicyDecision{
			Action:    PolicyActionDeny,
			Preferred: PolicyActionDeny,
			Hard:      true,
			Codes:     []string{"child_count_ceiling"},
			Reason: fmt.Sprintf(
				"policy=%s action=deny hard=child_count_ceiling live=%d max=%d",
				cfg.Mode, in.LiveChildren, cfg.MaxLiveChildren,
			),
		}
	}
	if in.TotalDelegations >= maxDeleg {
		return PolicyDecision{
			Action:    PolicyActionDeny,
			Preferred: PolicyActionDeny,
			Hard:      true,
			Codes:     []string{"delegation_ceiling"},
			Reason: fmt.Sprintf(
				"policy=%s action=deny hard=delegation_ceiling total=%d max=%d",
				cfg.Mode, in.TotalDelegations, maxDeleg,
			),
		}
	}
	if in.BudgetExhausted {
		return PolicyDecision{
			Action:    PolicyActionDeny,
			Preferred: PolicyActionDeny,
			Hard:      true,
			Codes:     []string{"budget_exhausted"},
			Reason: fmt.Sprintf(
				"policy=%s action=deny hard=budget_exhausted",
				cfg.Mode,
			),
		}
	}

	// Mode off: skip soft heuristics.
	if cfg.Mode == PolicyOff {
		return PolicyDecision{
			Action:    PolicyActionDelegate,
			Preferred: PolicyActionDelegate,
			Codes:     []string{"off"},
			Reason:    "policy=off action=delegate",
		}
	}

	// --- Soft heuristics ---
	preferred, codes, detail := preferLocalOrDelegate(in, cfg)
	dec := PolicyDecision{
		Action:    preferred,
		Preferred: preferred,
		Hard:      false,
		Codes:     codes,
	}

	switch preferred {
	case PolicyActionLocal:
		dec.Reason = fmt.Sprintf("policy=%s action=local %s", cfg.Mode, detail)
	default:
		dec.Reason = fmt.Sprintf("policy=%s action=delegate %s", cfg.Mode, detail)
	}

	// Force override: soft local → delegate only.
	if in.Force && preferred == PolicyActionLocal {
		dec.Action = PolicyActionDelegate
		dec.Overridden = true
		dec.Codes = append(uniquePolicyCodes(dec.Codes), "override")
		dec.Reason = fmt.Sprintf(
			"policy=%s action=delegate override=force_delegate was=local %s",
			cfg.Mode, detail,
		)
		return dec
	}

	// Advise: always spawn, but keep preferred in reason/codes.
	if cfg.Mode == PolicyAdvise && preferred == PolicyActionLocal {
		dec.Action = PolicyActionDelegate
		dec.Codes = append(uniquePolicyCodes(dec.Codes), "advise_spawn")
		dec.Reason = fmt.Sprintf(
			"policy=advise action=delegate preferred=local %s",
			detail,
		)
		return dec
	}

	return dec
}

func preferLocalOrDelegate(in PolicyInput, cfg DelegationPolicyConfig) (action string, codes []string, detail string) {
	// Intentional delegation signals → prefer fan-out.
	intentional := strings.TrimSpace(in.AgentPin) != "" ||
		strings.TrimSpace(in.Specialty) != "" ||
		len(normalizeTags(in.Capabilities)) > 0 ||
		len(nonEmptyStrings(in.Criteria)) > 0 ||
		len(in.Deps) > 0 ||
		len(in.Verify) > 0

	paths := normalizePolicyPaths(in.Paths)
	promptRunes := utf8.RuneCountInString(strings.TrimSpace(in.Prompt))
	overlap := normalizePolicyPaths(in.OverlapPaths)

	// Overlap with other live agents: tightly coupled / conflict risk.
	if len(overlap) > 0 {
		codes = []string{"overlap"}
		detail = fmt.Sprintf("soft=overlap paths=%s", strings.Join(overlap, ","))
		if intentional {
			// Still prefer local when writable paths collide — coordination cost
			// dominates even for specialist work unless force_delegate.
			return PolicyActionLocal, codes, detail
		}
		return PolicyActionLocal, codes, detail
	}

	if intentional {
		codes = []string{"independent"}
		parts := make([]string, 0, 4)
		if strings.TrimSpace(in.AgentPin) != "" {
			parts = append(parts, "agent="+strings.TrimSpace(in.AgentPin))
		}
		if s := strings.TrimSpace(in.Specialty); s != "" {
			parts = append(parts, "specialty="+s)
		}
		if n := len(normalizeTags(in.Capabilities)); n > 0 {
			parts = append(parts, fmt.Sprintf("capabilities=%d", n))
		}
		if n := len(nonEmptyStrings(in.Criteria)); n > 0 {
			parts = append(parts, fmt.Sprintf("criteria=%d", n))
		}
		if len(in.Deps) > 0 {
			parts = append(parts, fmt.Sprintf("deps=%d", len(in.Deps)))
		}
		if len(in.Verify) > 0 {
			parts = append(parts, fmt.Sprintf("verify=%d", len(in.Verify)))
		}
		detail = "soft=independent " + strings.Join(parts, " ")
		return PolicyActionDelegate, codes, detail
	}

	// Bare tiny / single-path tasks: coordination overhead not worth it.
	maxPaths := cfg.MaxPathsLocal
	tiny := cfg.TinyPromptRunes > 0 && promptRunes > 0 && promptRunes <= cfg.TinyPromptRunes
	fewPaths := maxPaths >= 0 && len(paths) <= maxPaths
	if tiny && fewPaths {
		codes = []string{"tiny"}
		detail = fmt.Sprintf(
			"soft=tiny prompt_runes=%d paths=%d threshold=%d",
			promptRunes, len(paths), cfg.TinyPromptRunes,
		)
		return PolicyActionLocal, codes, detail
	}

	// Multi-path bare work without intentional pins still benefits from a child
	// when the prompt is substantial.
	if len(paths) > maxPaths && maxPaths >= 0 {
		codes = []string{"multi_path"}
		detail = fmt.Sprintf("soft=multi_path paths=%d", len(paths))
		return PolicyActionDelegate, codes, detail
	}

	// Default: substantial bare prompt → allow delegation (caller chose task).
	codes = []string{"default"}
	detail = fmt.Sprintf("soft=default prompt_runes=%d paths=%d", promptRunes, len(paths))
	return PolicyActionDelegate, codes, detail
}

func nonEmptyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, s)
		}
	}
	return out
}

func normalizePolicyPaths(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		// Normalize for stable compare (slash form).
		p = filepath.ToSlash(p)
		key := strings.ToLower(p)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}

func uniquePolicyCodes(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, c := range in {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if _, ok := seen[c]; ok {
			continue
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	return out
}

// policyPathsFromRequest collects path signals from a sealed context bundle.
func policyPathsFromRequest(req tool.TaskRequest) []string {
	b := req.ContextBundle
	out := make([]string, 0, len(b.AllowedPaths)+len(b.RequiredPaths))
	out = append(out, b.AllowedPaths...)
	out = append(out, b.RequiredPaths...)
	return normalizePolicyPaths(out)
}

// overlapPathsForPolicy returns requested paths that conflict with claims held
// by other active sessions (not the parent caller).
func (e *Engine) overlapPathsForPolicy(requested []string) []string {
	if e == nil || e.team == nil || len(requested) == 0 {
		return nil
	}
	own := e.team.Ownership()
	if own == nil {
		return nil
	}
	snap := own.Snapshot()
	if len(snap.Claims) == 0 {
		return nil
	}
	self := strings.TrimSpace(e.opts.SessionID)
	wd := strings.TrimSpace(e.opts.WorkDir)

	type holder struct {
		path string
	}
	var claims []holder
	for _, c := range snap.Claims {
		foreign := false
		for _, h := range c.Holders {
			if !h.Active {
				continue
			}
			sid := strings.TrimSpace(h.SessionID)
			if sid == "" || sid == self {
				continue
			}
			foreign = true
			break
		}
		if foreign {
			claims = append(claims, holder{path: c.Path})
		}
	}
	if len(claims) == 0 {
		return nil
	}

	var hit []string
	for _, raw := range requested {
		abs, display := resolveTeamOwnershipPath(wd, raw)
		for _, c := range claims {
			if pathsOverlapPolicy(abs, c.path) || pathsOverlapPolicy(display, c.path) {
				hit = append(hit, display)
				break
			}
		}
	}
	return normalizePolicyPaths(hit)
}

func pathsOverlapPolicy(a, b string) bool {
	a = filepath.Clean(strings.TrimSpace(a))
	b = filepath.Clean(strings.TrimSpace(b))
	if a == "" || b == "" || a == "." || b == "." {
		return false
	}
	if a == b {
		return true
	}
	// Prefix overlap (directory claim covers file, or vice versa).
	as := a + string(filepath.Separator)
	bs := b + string(filepath.Separator)
	return strings.HasPrefix(a, bs) || strings.HasPrefix(b, as)
}

func (e *Engine) liveChildCount() int {
	if e == nil {
		return 0
	}
	n := 0
	e.childMu.Lock()
	for _, h := range e.children {
		if h == nil {
			continue
		}
		select {
		case <-h.done:
		default:
			n++
		}
	}
	e.childMu.Unlock()
	return n
}

func (e *Engine) totalDelegationCount() int {
	if e == nil || e.team == nil {
		return 0
	}
	return len(e.team.Delegations())
}

// sessionBudgetExhausted reports whether an outer session cost envelope is spent.
// Wired via Options.SessionBudgetExhausted when session ceilings (#577) land;
// until then the hook is nil and fan-out is not blocked on cost.
func (e *Engine) sessionBudgetExhausted() bool {
	if e == nil || e.opts.SessionBudgetExhausted == nil {
		return false
	}
	return e.opts.SessionBudgetExhausted()
}

// PolicyMetricsSnapshot returns counters for delegation-worthiness decisions.
func (e *Engine) PolicyMetricsSnapshot() (delegate, local, deny, override, adviseLocal int64) {
	if e == nil {
		return 0, 0, 0, 0, 0
	}
	return e.policyMetrics.Snapshot()
}

// evaluateDelegationPolicy builds input from the engine + request and records metrics.
func (e *Engine) evaluateDelegationPolicy(req tool.TaskRequest) PolicyDecision {
	dec := EvaluateDelegationPolicy(e.policyInput(req))
	if e != nil {
		mode := NormalizeDelegationPolicy(e.opts.DelegationPolicy).Mode
		e.policyMetrics.record(dec, mode)
	}
	return dec
}

// evaluateDelegationPolicyHard re-checks only hard ceilings (used at deferred
// spawn after soft heuristics already passed at create). Soft rules are skipped
// by evaluating with mode=off after the hard-ceiling block.
func (e *Engine) evaluateDelegationPolicyHard(req tool.TaskRequest) PolicyDecision {
	in := e.policyInput(req)
	// Preserve configured ceilings (max live children) but skip soft heuristics.
	cfg := NormalizeDelegationPolicy(in.Config)
	cfg.Mode = PolicyOff
	in.Config = cfg
	dec := EvaluateDelegationPolicy(in)
	if e != nil && dec.Action == PolicyActionDeny {
		e.policyMetrics.record(dec, NormalizeDelegationPolicy(e.opts.DelegationPolicy).Mode)
	}
	// Annotate deferred hard re-check in the reason for observability.
	if dec.Action == PolicyActionDelegate && !strings.Contains(dec.Reason, "deferred") {
		dec.Reason = strings.TrimSpace(dec.Reason) + " deferred_hard_ok"
	}
	return dec
}

func (e *Engine) policyInput(req tool.TaskRequest) PolicyInput {
	if e == nil {
		return PolicyInput{Config: DelegationPolicyConfig{Mode: PolicyOff}}
	}
	paths := policyPathsFromRequest(req)
	maxDepth := e.opts.MaxChildDepth
	if maxDepth == 0 {
		maxDepth = 1
	}
	return PolicyInput{
		Config:           e.opts.DelegationPolicy,
		Force:            req.ForceDelegate,
		Prompt:           req.Prompt,
		AgentPin:         req.Agent,
		Specialty:        req.Specialty,
		Capabilities:     append([]string(nil), req.Capabilities...),
		Criteria:         append([]string(nil), req.Criteria...),
		Deps:             append([]string(nil), req.Deps...),
		Verify:           append([]tool.VerifyGate(nil), req.Verify...),
		Paths:            paths,
		Depth:            e.opts.Depth,
		MaxDepth:         maxDepth,
		LiveChildren:     e.liveChildCount(),
		TotalDelegations: e.totalDelegationCount(),
		MaxDelegations:   MaxDelegations,
		OverlapPaths:     e.overlapPathsForPolicy(paths),
		BudgetExhausted:  e.sessionBudgetExhausted(),
	}
}
