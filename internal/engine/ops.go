package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/sandbox"
)

func (e *Engine) handleOp(ctx context.Context, op protocol.Op) {
	// If the worker already emitted terminal TurnCompleted, join it before
	// applying the op so a follow-up UserInput is not rejected as active-turn.
	e.joinFinishingTurn()
	switch op := op.(type) {
	case protocol.UserInput:
		if e.turnActive() {
			e.enqueueUserInput(op)
			return
		}
		if e.prov == nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "no model selected — use /provider <anthropic|openai|xai|google|kimi|deepseek|echo> [model]",
			})
			return
		}
		e.startTurn(ctx, op.Text, op.Images)
	case protocol.SelectModel:
		e.handleSelect(op)
	case protocol.SelectAgent:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot switch agents while a turn is running",
			})
			return
		}
		e.handleSelectAgent(op)
	case protocol.SetEffort:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change effort while a turn is running",
			})
			return
		}
		e.setEffort(op.Level)
	case protocol.SetAutonomy:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change autonomy while a turn is running",
			})
			return
		}
		e.setAutonomy(op.Mode)
	case protocol.SetPermissionMode:
		// Allowed mid-turn: posture applies to subsequent tool asks in the
		// same turn (pending asks are rejected by the permission service).
		e.setPermissionMode(op.Mode)
	case protocol.SetFast:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot change fast while a turn is running",
			})
			return
		}
		e.setFast(op.Enabled)
	case protocol.StartWorkflow:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot start a workflow while a turn is running",
			})
			return
		}
		if err := e.startWorkflow(op.Name); err != nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     err.Error(),
			})
		}
	case protocol.StopWorkflow:
		if e.turnActive() {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     "cannot stop a workflow while a turn is running",
			})
			return
		}
		e.stopWorkflow()
	case protocol.PermissionReply:
		e.routePermissionReply(op)
	case protocol.QuestionReply:
		e.routeQuestionReply(op)
	case protocol.Interrupt:
		// Parent turn only — non-blocking children keep running until the
		// engine shuts down or they finish on their own.
		if e.turnCancel != nil {
			e.turnCancel()
		}
	case protocol.FilesChanged:
		e.handleFilesChanged(op)
	case protocol.Compact:
		e.handleCompact(ctx, op)
	case protocol.InspectEffectivePrompt:
		e.handleInspectEffectivePrompt()
	case protocol.SetContextControls:
		e.handleSetContextControls(op)
	case protocol.Rewind:
		e.handleRewind(op)
	}
}

// handleInspectEffectivePrompt emits the last Stream layer map when available,
// otherwise the current composition for the next request. When session pin/
// exclude controls changed since the last stream, prefer the current
// composition so inspect matches the next turn.
func (e *Engine) handleInspectEffectivePrompt() {
	snap := e.lastOrCurrentEffective()
	liveExcl := sortedKindKeys(e.excludedKinds)
	livePin := sortedKindKeys(e.pinnedKinds)
	if !stringSlicesEqual(liveExcl, snap.ExcludedKinds) || !stringSlicesEqual(livePin, snap.PinnedKinds) {
		snap = e.currentEffectiveSnapshot()
	}
	e.emit(protocol.EffectivePrompt{
		Correlation:    e.sessionCorr(),
		Layers:         snap.Layers,
		SystemChars:    snap.SystemChars,
		MessageCount:   snap.MessageCount,
		FromLastStream: snap.FromLastStream,
		Attribution:    snap.Attribution,
		ExcludedKinds:  snap.ExcludedKinds,
		PinnedKinds:    snap.PinnedKinds,
		ShedKinds:      snap.ShedKinds,
	})
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// handleFilesChanged invalidates read snapshots for the reported paths and
// emits FilesInvalidated so the session log and TUI observe the change.
// Accepted while a turn is running: external edits can land mid-turn.
func (e *Engine) handleFilesChanged(op protocol.FilesChanged) {
	abs := make([]string, 0, len(op.Paths))
	seen := make(map[string]struct{}, len(op.Paths))
	for _, p := range op.Paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.opts.WorkDir, p)
		}
		p = filepath.Clean(p)
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		abs = append(abs, p)
	}
	if len(abs) == 0 {
		return
	}
	e.files.MarkDirty(abs...)
	display := make([]string, len(abs))
	for i, p := range abs {
		display[i] = relDisplayPath(e.opts.WorkDir, p)
	}
	e.emit(protocol.FilesInvalidated{
		Correlation: e.sessionCorr(),
		Paths:       display,
		Reason:      op.Reason,
	})
}

func relDisplayPath(workDir, abs string) string {
	if workDir == "" {
		return abs
	}
	if rel, err := filepath.Rel(workDir, abs); err == nil && !strings.HasPrefix(rel, "..") {
		return rel
	}
	return abs
}

// splitProviderModel parses "provider/model" form. Both sides of the first
// slash must be non-empty. Only the first slash is considered so model ids
// that themselves contain slashes remain in the model part.
func splitProviderModel(s string) (provider, model string, ok bool) {
	provider, model, found := strings.Cut(s, "/")
	if !found || provider == "" || model == "" {
		return "", "", false
	}
	return provider, model, true
}

// stripMatchingProviderPrefixes repeatedly strips a leading "provider/"
// segment (EqualFold) until none remain. Caps iterations so a pathological
// input cannot loop forever.
//
//	openai + "openai/openai/gpt-5.6-sol" → "gpt-5.6-sol"
//	openai + "openai/gpt-5.6-sol"        → "gpt-5.6-sol"
//	openai + "gpt-5.6-sol"               → "gpt-5.6-sol"
func stripMatchingProviderPrefixes(providerName, model string) string {
	if providerName == "" || model == "" {
		return model
	}
	const maxStrip = 8
	for range maxStrip {
		prov, bare, ok := splitProviderModel(model)
		if !ok || config.CanonicalProviderID(prov) != config.CanonicalProviderID(providerName) {
			return model
		}
		model = bare
	}
	return model
}

// resolveSelectModel normalizes op.Model for a chosen provider: matching
// "provider/id" prefixes (including repeated ones) become bare ids; foreign
// prefixes are discarded so the caller falls back to the provider default;
// bare ids pass through.
func resolveSelectModel(providerName, model, defaultModel string) string {
	if prov, _, ok := splitProviderModel(model); ok {
		if config.CanonicalProviderID(prov) == config.CanonicalProviderID(providerName) {
			// First segment matches: strip all matching prefixes (handles
			// doubles like openai/openai/gpt-5.6-sol).
			model = stripMatchingProviderPrefixes(providerName, model)
		} else {
			model = ""
		}
	}
	if model == "" {
		return defaultModel
	}
	return model
}

func (e *Engine) handleSelect(op protocol.SelectModel) {
	if e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "cannot switch models while a turn is running",
		})
		return
	}
	if e.opts.Select == nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "provider selection is not available",
		})
		return
	}
	name := config.CanonicalProviderID(op.Provider)
	p, defaultModel, err := e.opts.Select(name)
	if err != nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     err.Error(),
		})
		return
	}
	model := resolveSelectModel(name, op.Model, defaultModel)
	e.setProvider(name, p, model)
}

func (e *Engine) setProvider(name string, p provider.Provider, model string) {
	name = config.CanonicalProviderID(name)
	// Chokepoint: never store a matching provider/ prefix (or doubles) on the
	// active model string. Callers may still pass already-prefixed ids.
	model = stripMatchingProviderPrefixes(name, model)
	e.prov, e.provName, e.model = p, name, model
	e.refreshContextWindow()
	e.emitSelected(protocol.ModelSelected{
		Correlation: e.sessionCorr(),
		Provider:    name,
		Model:       model,
	})
}

// setModel stores a bare model id for the current provider, stripping any
// matching provider/ prefixes first, and emits ModelSelected.
func (e *Engine) setModel(model string) {
	if e.provName != "" {
		model = stripMatchingProviderPrefixes(e.provName, model)
	}
	e.model = model
	e.refreshContextWindow()
	e.emitSelected(protocol.ModelSelected{
		Correlation: e.sessionCorr(),
		Provider:    e.provName,
		Model:       model,
	})
}

// setEffort records the reasoning dial and confirms it. An unrecognized level
// is rejected rather than silently forwarded to a provider that would 400 on
// it.
func (e *Engine) setEffort(level protocol.Effort) {
	parsed, ok := protocol.ParseEffort(string(level))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown effort %q (want %s)", level, effortNames()),
		})
		return
	}
	e.effort = parsed
	e.emitSelected(protocol.EffortSelected{
		Correlation: e.sessionCorr(),
		Level:       parsed,
	})
}

func effortNames() string {
	names := make([]string, 0, len(protocol.Efforts()))
	for _, level := range protocol.Efforts() {
		names = append(names, string(level))
	}
	return strings.Join(names, "|")
}

// setAutonomy records the session exit-gate policy and confirms it. Empty
// normalizes to supervised; unrecognized values are rejected. When a workflow
// phase is active, re-emits PhaseChanged so Gate matches the new dial.
func (e *Engine) setAutonomy(mode protocol.Autonomy) {
	parsed, ok := protocol.ParseAutonomy(string(mode))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown autonomy %q (want %s)", mode, autonomyNames()),
		})
		return
	}
	e.autonomy = parsed
	e.emitSelected(protocol.AutonomySelected{
		Correlation: e.sessionCorr(),
		Mode:        parsed,
	})
	if phase, ok := e.currentPhase(); ok {
		e.emitPhaseChanged(phase.Name, e.phaseIndex, "")
	}
}

func autonomyNames() string {
	names := make([]string, 0, len(protocol.Autonomies()))
	for _, mode := range protocol.Autonomies() {
		names = append(names, string(mode))
	}
	return strings.Join(names, "|")
}

// setPermissionMode records tool-permission posture, updates the permission
// service, and aligns plan posture with the plan-implement workflow. Empty
// normalizes to default; unrecognized values are rejected. Safe mid-turn:
// rules take effect for the next Ask; agent pins from plan enter/leave are
// queued when a turn is active (same as enterPhaseOpts).
func (e *Engine) setPermissionMode(mode protocol.PermissionMode) {
	e.applyPermissionMode(mode, true)
}

// applyPermissionMode is the shared implementation for startup and SetPermissionMode.
// When alignPlan is false (startup), only rules + confirm are applied; the caller
// enters the plan workflow after agent select. When true (user dial), plan is
// entered or left immediately (agent switch deferred mid-turn).
func (e *Engine) applyPermissionMode(mode protocol.PermissionMode, alignPlan bool) {
	parsed, ok := protocol.ParsePermissionMode(string(mode))
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown permission mode %q (want %s)", mode, permissionModeNames()),
		})
		return
	}
	if err := sandbox.CheckYoloSandbox(string(parsed), e.opts.SandboxMode, e.opts.AllowYoloWithoutSandbox); err != nil {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     err.Error(),
		})
		return
	}
	prev := e.permMode
	e.permMode = parsed
	e.perms.SetPermissionMode(parsed)
	if alignPlan {
		switch {
		case parsed == protocol.PermissionModePlan:
			_ = e.enterPlanPhase()
		case prev == protocol.PermissionModePlan && parsed != protocol.PermissionModePlan:
			// Leaving plan posture via the dial abandons plan workflow without
			// recording a plan handoff (cannot bypass unified approval).
			// When handoff already completed, keep implement phase if active.
			if phase, ok := e.currentPhase(); ok && phase.Name == "plan" {
				if !e.planHandoff.Active {
					e.clearPhase()
				}
				if e.agent.Name == "plan" {
					if _, ok := e.findAgent("build"); ok {
						if e.turnActive() {
							_ = e.queueSwitchAgent("build")
						} else {
							// Phase-aligned dial exit: keep session model.
							_ = e.applyAgent("build", true)
						}
					}
				}
			}
		}
	}
	e.emitSelected(protocol.PermissionModeSelected{
		Correlation: e.sessionCorr(),
		Mode:        parsed,
	})
}

func permissionModeNames() string {
	names := make([]string, 0, len(protocol.PermissionModes()))
	for _, mode := range protocol.PermissionModes() {
		names = append(names, string(mode))
	}
	return strings.Join(names, "|")
}

// providerEffort translates the frontend-facing dial into the provider
// vocabulary. The two ladders are kept in lockstep by TestProviderEffortCoversEveryLevel.
func providerEffort(level protocol.Effort) provider.Effort {
	switch level {
	case protocol.EffortOff:
		return provider.EffortOff
	case protocol.EffortLow:
		return provider.EffortLow
	case protocol.EffortMedium:
		return provider.EffortMedium
	case protocol.EffortHigh:
		return provider.EffortHigh
	case protocol.EffortXHigh:
		return provider.EffortXHigh
	case protocol.EffortMax:
		return provider.EffortMax
	default:
		return provider.EffortDefault
	}
}

func (e *Engine) setFast(enabled bool) {
	e.priority = enabled
	e.emitSelected(protocol.FastSelected{
		Correlation: e.sessionCorr(),
		Enabled:     enabled,
	})
}

func (e *Engine) findAgent(name string) (Agent, bool) {
	for _, a := range e.opts.Agents {
		if a.Name == name {
			return a, true
		}
	}
	return Agent{}, false
}

func agentNamesList(agents []Agent) string {
	if len(agents) == 0 {
		return "(none)"
	}
	names := make([]string, 0, len(agents))
	for _, a := range agents {
		if a.Name != "" {
			names = append(names, a.Name)
		}
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

// handleSelectAgent switches the active persona and syncs workflow phase
// when the user picks an agent (tab, /agent). Applies agent model pins.
func (e *Engine) handleSelectAgent(op protocol.SelectAgent) {
	if !e.applyAgent(op.Name, false) {
		return
	}
	// Root only: child sessions do not drive parent workflow phases.
	if e.opts.Depth == 0 {
		e.syncPhaseWithAgent(op.Name)
	}
}

// applyAgent switches persona and agent permission profile without touching
// workflow phase state. keepModel skips provider/model pins so phase and
// tool-driven agent-type transitions keep the session model; explicit user
// SelectAgent and startup pass keepModel=false so pins still apply. Effort
// pins always apply (unless LockEffort). Task-tool LockModel also blocks pins.
func (e *Engine) applyAgent(name string, keepModel bool) bool {
	agent, ok := e.findAgent(name)
	if !ok {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("unknown agent %q (available: %s)", name, agentNamesList(e.opts.Agents)),
		})
		return false
	}
	e.agent = agent
	if e.team != nil {
		e.team.SetPersona(e.opts.SessionID, agent.Name)
	}
	// Child sessions apply a filtered agent profile (AG3): denies always;
	// allows only when they do not override a parent Deny (Ask→Allow is OK so
	// personas like general keep bash). Root keeps the full profile (AG1/AG2).
	agentRules := agent.Permissions
	if e.opts.Depth > 0 {
		agentRules = permission.ChildAgentRules(e.opts.Rules, agent.Permissions)
	}
	e.perms.SetAgentRules(agentRules)
	e.emitSelected(protocol.AgentSelected{
		Correlation: e.sessionCorr(),
		Name:        agent.Name,
	})
	// Task-tool effort pins set LockEffort so agent defaults cannot override.
	if !e.opts.LockEffort && agent.Effort != protocol.EffortDefault {
		e.setEffort(agent.Effort)
	}

	// Model-only "provider/id" pins promote the prefix to a provider pin.
	// setProvider / resolveSelectModel strip matching prefixes (including
	// doubles) so we never store openai/openai/... on the active model.
	// Task-tool model pins set LockModel so agent defaults cannot override.
	// Phase/tool transitions pass keepModel so the session model sticks.
	if e.opts.LockModel || keepModel {
		return true
	}
	agentProvider, agentModel := agent.Provider, agent.Model
	if agentProvider == "" {
		if prov, _, ok := splitProviderModel(agent.Model); ok {
			agentProvider = prov
		}
	}

	switch {
	case agentProvider != "" && e.opts.Select != nil:
		p, defaultModel, err := e.opts.Select(agentProvider)
		if err != nil {
			e.emit(protocol.EngineError{
				Correlation: e.sessionCorr(),
				Message:     fmt.Sprintf("agent %s: %v", agent.Name, err),
			})
			return true
		}
		model := resolveSelectModel(agentProvider, agentModel, defaultModel)
		e.setProvider(agentProvider, p, model)
	case agentModel != "" && e.prov != nil:
		e.setModel(agentModel)
	}
	return true
}

// queueSwitchAgent validates name and queues it for application after the
// current tool batch (before the next provider Stream) or at turn end.
// Queued switches keep the session model (phase/tool transitions).
func (e *Engine) queueSwitchAgent(name string) error {
	if _, ok := e.findAgent(name); !ok {
		return fmt.Errorf("unknown agent %q (available: %s)", name, agentNamesList(e.opts.Agents))
	}
	e.pendingAgentMu.Lock()
	e.pendingAgent = name
	e.pendingAgentMu.Unlock()
	return nil
}

// applyPendingAgent applies a tool/phase-queued agent switch, if any.
// Keeps the session model; only explicit SelectAgent applies model pins.
func (e *Engine) applyPendingAgent() {
	e.pendingAgentMu.Lock()
	name := e.pendingAgent
	e.pendingAgent = ""
	e.pendingAgentMu.Unlock()
	if name == "" {
		return
	}
	if !e.applyAgent(name, true) {
		return
	}
	if e.opts.Depth == 0 {
		e.syncPhaseWithAgent(name)
	}
}
