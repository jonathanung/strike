package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/jonathanung/strike-cli/harness/scheduler"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// Route mode values accepted on task/delegate spawn.
const (
	RouteOff  = ""
	RouteAuto = "auto"
)

// DefaultRouteMaxConcurrent is the per-persona live-child limit used when
// route=auto and the request does not set MaxConcurrent. At or above this
// count the router skips to the next candidate (fallback).
const DefaultRouteMaxConcurrent = 1

// Cost class ranks for max_cost_class filtering (lower is cheaper).
const (
	costClassUnknown = 0
	costClassLow     = 1
	costClassMedium  = 2
	costClassHigh    = 3
)

// RouteAgent is one selectable persona for the capability router.
type RouteAgent struct {
	Name         string
	Capabilities []string
	Provider     string
	Model        string // optional agent model pin
	Effort       protocol.Effort
	// CostClass is low|medium|high|"" (unknown). Used when max_cost_class is set.
	CostClass string
}

// RouteLoad is a point-in-time load snapshot for fallback decisions.
type RouteLoad struct {
	// ActiveByAgent counts live (non-terminal) children per persona name.
	ActiveByAgent map[string]int
	// BudgetBlocked marks personas whose live children are escalated / hard-limited.
	BudgetBlocked map[string]bool
	// ModelPoolSaturated is true when the process model pool has no free slots.
	ModelPoolSaturated bool
}

// RouteInput is the pure-function input for capability-aware routing.
type RouteInput struct {
	// Mode is "" (off) or "auto".
	Mode string
	// Required capability/specialty tags (normalized lower-case). All must match
	// when non-empty under mode=auto (unless pins short-circuit).
	Required []string
	// MaxCostClass is low|medium|high|"" (no filter).
	MaxCostClass string
	// ModelAllow is an optional allow-list of bare or provider/model ids.
	ModelAllow []string
	// Explicit pins — always win when provided (AC: pin override).
	AgentPin  string
	ModelPin  string
	EffortPin string
	// ParentAgent is the caller's current persona (default when routing off / no pin).
	ParentAgent string
	// Agents is the catalog of selectable personas.
	Agents []RouteAgent
	// Load drives concurrency/budget fallback.
	Load RouteLoad
	// MaxConcurrent is live children per persona before fallback. 0 → DefaultRouteMaxConcurrent.
	MaxConcurrent int
}

// RouteDecision is the router output (chosen worker + structured reason).
type RouteDecision struct {
	Agent    string
	Model    string // empty → inherit parent model
	Effort   string // empty → inherit parent effort dial
	Reason   string
	Fallback bool
	// Mode echoes effective mode: off|pin|auto.
	Mode string
	// Skipped lists persona names skipped due to load/cost (debug).
	Skipped []string
}

// Route selects agent/model/effort for a task spawn. Pure and deterministic:
// equal inputs always yield equal decisions (stable sort by name on ties).
func Route(in RouteInput) RouteDecision {
	agentPin := strings.TrimSpace(in.AgentPin)
	modelPin := strings.TrimSpace(in.ModelPin)
	effortPin := strings.TrimSpace(in.EffortPin)
	mode := strings.ToLower(strings.TrimSpace(in.Mode))
	if mode == "off" {
		mode = RouteOff
	}

	// Explicit agent and/or model pins always win (routing may still be off).
	if agentPin != "" || modelPin != "" {
		agent := agentPin
		if agent == "" {
			agent = strings.TrimSpace(in.ParentAgent)
		}
		reason := "pin"
		if agentPin != "" && modelPin != "" {
			reason = fmt.Sprintf("pin agent=%s model=%s", agentPin, modelPin)
		} else if agentPin != "" {
			reason = fmt.Sprintf("pin agent=%s", agentPin)
		} else {
			reason = fmt.Sprintf("pin model=%s", modelPin)
		}
		if effortPin != "" {
			reason += " effort=" + effortPin
		}
		return RouteDecision{
			Agent:  agent,
			Model:  modelPin,
			Effort: effortPin,
			Reason: reason,
			Mode:   "pin",
		}
	}

	if mode != RouteAuto {
		// Legacy: inherit parent agent/model when routing is off.
		parent := strings.TrimSpace(in.ParentAgent)
		return RouteDecision{
			Agent:  parent,
			Model:  "",
			Effort: effortPin,
			Reason: "route=off inherit parent",
			Mode:   "off",
		}
	}

	required := normalizeTags(in.Required)
	if len(required) == 0 {
		parent := strings.TrimSpace(in.ParentAgent)
		return RouteDecision{
			Agent:  parent,
			Model:  "",
			Effort: effortPin,
			Reason: "route=auto without specialty; inherit parent",
			Mode:   "auto",
		}
	}

	maxConc := in.MaxConcurrent
	if maxConc <= 0 {
		maxConc = DefaultRouteMaxConcurrent
	}
	maxCost := parseCostClass(in.MaxCostClass)
	allow := normalizeModelAllow(in.ModelAllow)

	type ranked struct {
		agent   RouteAgent
		score   int // specialty quality only (higher better); load applied when walking
		active  int
		blocked bool
	}
	var (
		candidates []ranked
		skipped    []string
	)
	for _, a := range in.Agents {
		name := strings.TrimSpace(a.Name)
		if name == "" {
			continue
		}
		if !agentMatchesRequired(a, required) {
			continue
		}
		if maxCost > 0 {
			ac := parseCostClass(a.CostClass)
			if ac > 0 && ac > maxCost {
				skipped = append(skipped, name+":cost")
				continue
			}
		}
		if len(allow) > 0 {
			// Prefer agent model pin when present; empty model inherits and passes allow-list
			// only when the pin is empty (caller may still pin later).
			if m := strings.TrimSpace(a.Model); m != "" && !modelAllowed(m, a.Provider, allow) {
				skipped = append(skipped, name+":model_allow")
				continue
			}
		}
		active := 0
		if in.Load.ActiveByAgent != nil {
			active = in.Load.ActiveByAgent[name]
		}
		blocked := in.Load.BudgetBlocked != nil && in.Load.BudgetBlocked[name]
		// Specialty quality only — load does not reorder the ideal primary so
		// fallback_from stays meaningful when the top match is busy/blocked.
		overlap := capabilityOverlap(a, required)
		score := overlap * 1000
		candidates = append(candidates, ranked{agent: a, score: score, active: active, blocked: blocked})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		return candidates[i].agent.Name < candidates[j].agent.Name
	})

	// Ideal primary = first after specialty rank (ignore load).
	primary := ""
	if len(candidates) > 0 {
		primary = candidates[0].agent.Name
	}

	// Walk ranked list; skip busy / budget-blocked / saturated.
	var (
		chosen   *ranked
		fallback bool
	)
	for i := range candidates {
		c := &candidates[i]
		name := c.agent.Name
		if c.blocked {
			skipped = append(skipped, name+":budget")
			continue
		}
		if c.active >= maxConc {
			skipped = append(skipped, fmt.Sprintf("%s:concurrent(%d>=%d)", name, c.active, maxConc))
			continue
		}
		// Model pool saturation: still allow spawn (scheduler queues), but prefer
		// a free persona when this candidate already has live children.
		if in.Load.ModelPoolSaturated && c.active > 0 && len(candidates) > 1 {
			skipped = append(skipped, name+":model_pool")
			continue
		}
		chosen = c
		if name != primary {
			fallback = true
		}
		break
	}

	if chosen == nil {
		// Last-resort fallback chain: general → build → parent.
		for _, fb := range []string{"general", "build", strings.TrimSpace(in.ParentAgent)} {
			fb = strings.TrimSpace(fb)
			if fb == "" {
				continue
			}
			if a, ok := findRouteAgent(in.Agents, fb); ok {
				// Avoid infinite empty when already considered and blocked.
				if in.Load.BudgetBlocked != nil && in.Load.BudgetBlocked[fb] {
					continue
				}
				active := 0
				if in.Load.ActiveByAgent != nil {
					active = in.Load.ActiveByAgent[fb]
				}
				if active >= maxConc {
					continue
				}
				chosen = &ranked{agent: a, active: active}
				fallback = true
				primary = firstNonEmpty(primary, required[0])
				break
			}
		}
	}

	if chosen == nil {
		parent := strings.TrimSpace(in.ParentAgent)
		return RouteDecision{
			Agent:   parent,
			Model:   "",
			Effort:  effortPin,
			Reason:  fmt.Sprintf("route=auto specialty=%s; no available agent, inherit parent", strings.Join(required, ",")),
			Mode:    "auto",
			Skipped: uniqueStrings(skipped),
		}
	}

	agent := chosen.agent
	model := strings.TrimSpace(agent.Model) // agent pin; empty inherits parent
	if len(allow) > 0 && model != "" && !modelAllowed(model, agent.Provider, allow) {
		model = ""
	}
	effort := effortPin
	if effort == "" && agent.Effort != protocol.EffortDefault && agent.Effort != "" {
		effort = string(agent.Effort)
	}

	reason := fmt.Sprintf("route=auto specialty=%s agent=%s", strings.Join(required, ","), agent.Name)
	if fallback {
		reason += fmt.Sprintf(" fallback_from=%s", primary)
	} else {
		reason += " primary"
	}
	if model != "" {
		reason += " model=" + model
	} else {
		reason += " model=inherit"
	}
	if len(skipped) > 0 {
		reason += " skipped=" + strings.Join(uniqueStrings(skipped), ",")
	}

	return RouteDecision{
		Agent:    agent.Name,
		Model:    model,
		Effort:   effort,
		Reason:   reason,
		Fallback: fallback,
		Mode:     "auto",
		Skipped:  uniqueStrings(skipped),
	}
}

// applyRouteDecision writes router output onto a task request (mutates req).
func applyRouteDecision(req *tool.TaskRequest, d RouteDecision) {
	if req == nil {
		return
	}
	if d.Agent != "" {
		req.Agent = d.Agent
	}
	// Pins already on req win; router only fills empties for model/effort when
	// it chose them (agent model pin under auto). Explicit req pins were
	// handled inside Route and returned as decision fields.
	if strings.TrimSpace(req.Model) == "" && d.Model != "" {
		req.Model = d.Model
	}
	if strings.TrimSpace(req.Effort) == "" && d.Effort != "" {
		req.Effort = d.Effort
	}
}

func (e *Engine) routeTaskRequest(req tool.TaskRequest) (tool.TaskRequest, RouteDecision) {
	if e == nil {
		return req, RouteDecision{Reason: "no engine", Mode: "off"}
	}
	mode := strings.TrimSpace(req.Route)
	// Auto-enable routing when specialty/capabilities provided without explicit off.
	if mode == "" && (strings.TrimSpace(req.Specialty) != "" || len(req.Capabilities) > 0) {
		mode = RouteAuto
	}
	required := normalizeTags(req.Capabilities)
	if s := strings.TrimSpace(req.Specialty); s != "" {
		required = normalizeTags(append(required, s))
	}
	parent := ""
	if e.agent.Name != "" {
		parent = e.agent.Name
	}
	agents := make([]RouteAgent, 0, len(e.opts.Agents))
	for _, a := range e.opts.Agents {
		agents = append(agents, RouteAgent{
			Name:         a.Name,
			Capabilities: append([]string(nil), a.Capabilities...),
			Provider:     a.Provider,
			Model:        a.Model,
			Effort:       a.Effort,
		})
	}
	dec := Route(RouteInput{
		Mode:          mode,
		Required:      required,
		MaxCostClass:  req.MaxCostClass,
		ModelAllow:    append([]string(nil), req.Models...),
		AgentPin:      req.Agent,
		ModelPin:      req.Model,
		EffortPin:     req.Effort,
		ParentAgent:   parent,
		Agents:        agents,
		Load:          e.routeLoadSnapshot(),
		MaxConcurrent: req.MaxConcurrent,
	})
	out := req
	applyRouteDecision(&out, dec)
	return out, dec
}

func (e *Engine) routeLoadSnapshot() RouteLoad {
	load := RouteLoad{
		ActiveByAgent: make(map[string]int),
		BudgetBlocked: make(map[string]bool),
	}
	if e == nil {
		return load
	}
	// Collect live handles under childMu, then read budget under each handle's
	// mu (childBudget is guarded by childHandle.mu — never nest the reverse).
	type live struct {
		h       *childHandle
		persona string
	}
	var lives []live
	e.childMu.Lock()
	for _, h := range e.children {
		if h == nil {
			continue
		}
		// Live children only (done channel still open).
		select {
		case <-h.done:
			continue
		default:
		}
		persona := strings.TrimSpace(h.agent)
		if persona == "" {
			continue
		}
		lives = append(lives, live{h: h, persona: persona})
		load.ActiveByAgent[persona]++
	}
	e.childMu.Unlock()

	for _, l := range lives {
		l.h.mu.Lock()
		blocked := l.h.budget != nil && l.h.budget.escalated
		l.h.mu.Unlock()
		if blocked {
			load.BudgetBlocked[l.persona] = true
		}
	}
	if e.opts.Scheduler != nil {
		for _, p := range e.opts.Scheduler.Snapshot().Pools {
			if p.Name == scheduler.PoolModel && !p.Unlimited && p.Capacity > 0 && p.InUse >= p.Capacity {
				load.ModelPoolSaturated = true
				break
			}
		}
	}
	return load
}

func agentMatchesRequired(a RouteAgent, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := agentCapabilitySet(a)
	for _, r := range required {
		if _, ok := have[r]; !ok {
			return false
		}
	}
	return true
}

func capabilityOverlap(a RouteAgent, required []string) int {
	have := agentCapabilitySet(a)
	n := 0
	for _, r := range required {
		if _, ok := have[r]; ok {
			n++
		}
	}
	return n
}

func agentCapabilitySet(a RouteAgent) map[string]struct{} {
	out := make(map[string]struct{}, 4+len(a.Capabilities))
	if n := strings.ToLower(strings.TrimSpace(a.Name)); n != "" {
		out[n] = struct{}{}
	}
	for _, c := range a.Capabilities {
		c = strings.ToLower(strings.TrimSpace(c))
		if c != "" {
			out[c] = struct{}{}
		}
	}
	return out
}

func normalizeTags(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		for _, part := range strings.FieldsFunc(s, func(r rune) bool {
			return r == ',' || r == ';' || r == '|'
		}) {
			p := strings.ToLower(strings.TrimSpace(part))
			if p == "" {
				continue
			}
			if _, ok := seen[p]; ok {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

func parseCostClass(s string) int {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return costClassLow
	case "medium", "med":
		return costClassMedium
	case "high":
		return costClassHigh
	default:
		return costClassUnknown
	}
}

func normalizeModelAllow(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, m := range in {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		key := strings.ToLower(m)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, m)
	}
	return out
}

func modelAllowed(model, provider string, allow []string) bool {
	if len(allow) == 0 {
		return true
	}
	model = strings.TrimSpace(model)
	provider = strings.TrimSpace(provider)
	full := model
	if provider != "" && !strings.Contains(model, "/") {
		full = provider + "/" + model
	}
	ml := strings.ToLower(model)
	fl := strings.ToLower(full)
	for _, a := range allow {
		al := strings.ToLower(strings.TrimSpace(a))
		if al == ml || al == fl {
			return true
		}
		// allow bare id match against suffix of provider/model
		if _, bare, ok := strings.Cut(al, "/"); ok && bare == ml {
			return true
		}
	}
	return false
}

func findRouteAgent(agents []RouteAgent, name string) (RouteAgent, bool) {
	name = strings.TrimSpace(name)
	for _, a := range agents {
		if a.Name == name {
			return a, true
		}
	}
	return RouteAgent{}, false
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
