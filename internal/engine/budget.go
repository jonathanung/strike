package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// Soft observability defaults when hard limits are unset (signal only; no kill).
// Stall doubles as stale-child detection (#517 folded into this design).
const (
	defaultSoftStallAfterS = 300 // 5 minutes without progress
	defaultSoftLoopN       = 6   // identical consecutive tool names
	budgetWatchInterval    = time.Second
	maxLoopHistory         = 32
)

// dangerousBudgetTools count toward MaxDangerousTools (mutating / high-impact).
var dangerousBudgetTools = map[string]struct{}{
	"bash":          {},
	"write":         {},
	"edit":          {},
	"apply_patch":   {},
	"notebook_edit": {},
}

// MergeAgentBudget overlays spawn limits onto session defaults.
// Positive / non-zero spawn fields win; zero means "inherit default".
func MergeAgentBudget(defaults, spawn tool.AgentBudgetLimits) tool.AgentBudgetLimits {
	out := defaults
	if spawn.MaxWallClockS > 0 {
		out.MaxWallClockS = spawn.MaxWallClockS
	}
	if spawn.MaxTokens > 0 {
		out.MaxTokens = spawn.MaxTokens
	}
	if spawn.MaxCostUSD > 0 {
		out.MaxCostUSD = spawn.MaxCostUSD
	}
	if spawn.MaxToolCalls > 0 {
		out.MaxToolCalls = spawn.MaxToolCalls
	}
	if spawn.MaxDangerousTools > 0 {
		out.MaxDangerousTools = spawn.MaxDangerousTools
	}
	if spawn.StallAfterS > 0 {
		out.StallAfterS = spawn.StallAfterS
	}
	if spawn.LoopDetectN > 0 {
		out.LoopDetectN = spawn.LoopDetectN
	}
	return NormalizeAgentBudget(out)
}

// NormalizeAgentBudget clamps negatives to zero.
func NormalizeAgentBudget(b tool.AgentBudgetLimits) tool.AgentBudgetLimits {
	if b.MaxWallClockS < 0 {
		b.MaxWallClockS = 0
	}
	if b.MaxTokens < 0 {
		b.MaxTokens = 0
	}
	if b.MaxCostUSD < 0 {
		b.MaxCostUSD = 0
	}
	if b.MaxToolCalls < 0 {
		b.MaxToolCalls = 0
	}
	if b.MaxDangerousTools < 0 {
		b.MaxDangerousTools = 0
	}
	if b.StallAfterS < 0 {
		b.StallAfterS = 0
	}
	if b.LoopDetectN < 0 {
		b.LoopDetectN = 0
	}
	return b
}

// childBudget tracks per-child usage and escalation state. Guarded by childHandle.mu.
type childBudget struct {
	limits tool.AgentBudgetLimits

	tokensUsed     int
	toolCalls      int
	dangerousTools int
	costUSD        float64 // reserved; stays 0 until session cost pricing (#577)

	lastProgressAt time.Time
	recentTools    []string // newest at end

	objective  string
	lastAction string

	softStall bool
	softLoop  bool
	// softStallSignaled is true after the rising-edge soft-stall notify
	// (ChildEscalated action=signaled). Cleared on progress so a later stall
	// can signal again.
	softStallSignaled bool

	escalated bool
	kind      string // wall_clock|tokens|cost_usd|tool_calls|dangerous_tools|stall|loop
	reason    string
	// terminal overrides ChildCompleted status when budget kill races interrupt.
	terminal protocol.ChildStatus

	// Soft-budget finalization (#879): one reserved handoff turn before hard stop.
	finalizing          bool
	finalizationDone    bool
	finalizationOutcome string // succeeded|failed|skipped_hard|none
}

func newChildBudget(limits tool.AgentBudgetLimits, objective string, now time.Time) *childBudget {
	return &childBudget{
		limits:         NormalizeAgentBudget(limits),
		lastProgressAt: now,
		objective:      strings.TrimSpace(objective),
	}
}

func (b *childBudget) noteProgress(now time.Time, action string) {
	if b == nil {
		return
	}
	b.lastProgressAt = now
	if a := strings.TrimSpace(action); a != "" {
		b.lastAction = a
	}
	// Progress clears soft stall and allows a future rising-edge signal.
	b.softStall = false
	b.softStallSignaled = false
}

func (b *childBudget) noteTool(name string, now time.Time) {
	if b == nil {
		return
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return
	}
	b.toolCalls++
	if _, ok := dangerousBudgetTools[name]; ok {
		b.dangerousTools++
	}
	b.recentTools = append(b.recentTools, name)
	if len(b.recentTools) > maxLoopHistory {
		b.recentTools = b.recentTools[len(b.recentTools)-maxLoopHistory:]
	}
	b.noteProgress(now, "tool "+name)
	b.softLoop = b.loopSoft()
}

func (b *childBudget) noteUsage(tokens int, now time.Time) {
	if b == nil || tokens <= 0 {
		return
	}
	// Sum per-stream Used (input+cache+output for that request). Matches
	// vendor billing (full prompt tokens each turn), not unique context size.
	b.tokensUsed += tokens
	b.noteProgress(now, b.lastAction)
}

func (b *childBudget) setObjective(text string) {
	if b == nil {
		return
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.objective = text
}

func (b *childBudget) loopSoft() bool {
	n := defaultSoftLoopN
	if b.limits.LoopDetectN > 0 {
		n = b.limits.LoopDetectN
	}
	return identicalTail(b.recentTools, n)
}

func (b *childBudget) loopHard() bool {
	if b == nil || b.limits.LoopDetectN < 2 {
		return false
	}
	return identicalTail(b.recentTools, b.limits.LoopDetectN)
}

func identicalTail(tools []string, n int) bool {
	if n < 2 || len(tools) < n {
		return false
	}
	tail := tools[len(tools)-n:]
	first := tail[0]
	if first == "" {
		return false
	}
	for _, t := range tail[1:] {
		if t != first {
			return false
		}
	}
	return true
}

// evaluate returns a hard trip when a configured limit is exceeded.
// Soft stall/loop flags are updated for observability even when no hard trip.
func (b *childBudget) evaluate(now time.Time, startedAt time.Time) (trip bool, kind, reason string, terminal protocol.ChildStatus) {
	if b == nil || b.escalated {
		return false, "", "", ""
	}
	elapsed := now.Sub(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}

	// Soft stall signal (stale-child / #517).
	stallAfter := time.Duration(defaultSoftStallAfterS) * time.Second
	if b.limits.StallAfterS > 0 {
		stallAfter = time.Duration(b.limits.StallAfterS) * time.Second
	}
	idle := now.Sub(b.lastProgressAt)
	if b.lastProgressAt.IsZero() {
		idle = elapsed
	}
	b.softStall = idle >= stallAfter
	b.softLoop = b.loopSoft()

	// Hard limits (configured only).
	if b.limits.MaxWallClockS > 0 && elapsed >= time.Duration(b.limits.MaxWallClockS)*time.Second {
		return true, "wall_clock",
			fmt.Sprintf("wall-clock budget exhausted (%s >= %ds)", elapsed.Round(time.Second), b.limits.MaxWallClockS),
			protocol.ChildStatusFailed
	}
	if b.limits.MaxTokens > 0 && b.tokensUsed >= b.limits.MaxTokens {
		return true, "tokens",
			fmt.Sprintf("token budget exhausted (%d/%d)", b.tokensUsed, b.limits.MaxTokens),
			protocol.ChildStatusFailed
	}
	// CostUSD reserved until session pricing (#577); enforce when both limit and usage > 0.
	if b.limits.MaxCostUSD > 0 && b.costUSD >= b.limits.MaxCostUSD {
		return true, "cost_usd",
			fmt.Sprintf("cost budget exhausted (%.4f/%.4f USD)", b.costUSD, b.limits.MaxCostUSD),
			protocol.ChildStatusFailed
	}
	// Count limits: Max N means N complete calls are allowed; trip on the (N+1)th.
	if b.limits.MaxToolCalls > 0 && b.toolCalls > b.limits.MaxToolCalls {
		return true, "tool_calls",
			fmt.Sprintf("tool-call budget exhausted (%d/%d)", b.toolCalls, b.limits.MaxToolCalls),
			protocol.ChildStatusFailed
	}
	if b.limits.MaxDangerousTools > 0 && b.dangerousTools > b.limits.MaxDangerousTools {
		return true, "dangerous_tools",
			fmt.Sprintf("dangerous-tool budget exhausted (%d/%d)", b.dangerousTools, b.limits.MaxDangerousTools),
			protocol.ChildStatusFailed
	}
	// Hard stall only when StallAfterS explicitly configured.
	if b.limits.StallAfterS > 0 && idle >= time.Duration(b.limits.StallAfterS)*time.Second {
		return true, "stall",
			fmt.Sprintf("stale/stall: no progress for %s (threshold %ds)", idle.Round(time.Second), b.limits.StallAfterS),
			protocol.ChildStatusBlocked
	}
	if b.loopHard() {
		n := b.limits.LoopDetectN
		return true, "loop",
			fmt.Sprintf("loop detected: tool %q repeated %d times", b.recentTools[len(b.recentTools)-1], n),
			protocol.ChildStatusBlocked
	}
	return false, "", "", ""
}

func (b *childBudget) stallThresholdS() int {
	if b != nil && b.limits.StallAfterS > 0 {
		return b.limits.StallAfterS
	}
	return defaultSoftStallAfterS
}

func (b *childBudget) idleDuration(now time.Time, startedAt time.Time) time.Duration {
	if b == nil {
		return 0
	}
	if !b.lastProgressAt.IsZero() {
		d := now.Sub(b.lastProgressAt)
		if d < 0 {
			return 0
		}
		return d
	}
	d := now.Sub(startedAt)
	if d < 0 {
		return 0
	}
	return d
}

func (b *childBudget) softStallReason(now time.Time, startedAt time.Time) string {
	idle := b.idleDuration(now, startedAt)
	th := b.stallThresholdS()
	return fmt.Sprintf("stale/stall: no progress for %s (soft threshold %ds)", idle.Round(time.Second), th)
}

func (b *childBudget) snapshot(now time.Time, startedAt time.Time) tool.AgentBudgetSnapshot {
	if b == nil {
		return tool.AgentBudgetSnapshot{}
	}
	elapsed := now.Sub(startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	elapsedS := int(elapsed.Seconds())
	idle := b.idleDuration(now, startedAt)
	idleS := int(idle.Seconds())
	if idleS < 0 {
		idleS = 0
	}
	snap := tool.AgentBudgetSnapshot{
		Limits:               b.limits,
		ElapsedS:             elapsedS,
		TokensUsed:           b.tokensUsed,
		CostUSDUsed:          b.costUSD,
		ToolCalls:            b.toolCalls,
		DangerousTools:       b.dangerousTools,
		Stall:                b.softStall,
		IdleS:                idleS,
		StallAfterSEffective: b.stallThresholdS(),
		Loop:                 b.softLoop,
		Escalated:            b.escalated,
		EscalateKind:         b.kind,
		EscalateReason:       b.reason,
	}
	if !b.lastProgressAt.IsZero() {
		snap.LastProgressAt = b.lastProgressAt.UTC().Format(time.RFC3339)
	}
	if b.limits.MaxWallClockS > 0 {
		rem := b.limits.MaxWallClockS - elapsedS
		if rem < 0 {
			rem = 0
		}
		snap.WallClockRemainingS = &rem
	}
	if b.limits.MaxTokens > 0 {
		rem := b.limits.MaxTokens - b.tokensUsed
		if rem < 0 {
			rem = 0
		}
		snap.TokensRemaining = &rem
	}
	if b.limits.MaxToolCalls > 0 {
		rem := b.limits.MaxToolCalls - b.toolCalls
		if rem < 0 {
			rem = 0
		}
		snap.ToolCallsRemaining = &rem
	}
	if b.limits.MaxDangerousTools > 0 {
		rem := b.limits.MaxDangerousTools - b.dangerousTools
		if rem < 0 {
			rem = 0
		}
		snap.DangerousRemaining = &rem
	}
	if b.limits.MaxCostUSD > 0 {
		rem := b.limits.MaxCostUSD - b.costUSD
		if rem < 0 {
			rem = 0
		}
		snap.CostUSDRemaining = &rem
	}
	return snap
}

// markEscalatedLocked records a hard trip. Caller holds childHandle.mu.
func (b *childBudget) markEscalatedLocked(kind, reason string, terminal protocol.ChildStatus) bool {
	if b == nil || b.escalated {
		return false
	}
	b.escalated = true
	b.kind = kind
	b.reason = reason
	b.terminal = terminal
	return true
}

// startChildBudgetWatch runs a 1 Hz ticker for wall-clock / stall hard limits.
func (e *Engine) startChildBudgetWatch(h *childHandle) {
	if e == nil || h == nil {
		return
	}
	parentLife := e.runCtx
	if parentLife == nil {
		parentLife = context.Background()
	}
	ctx, cancel := context.WithCancel(parentLife)
	h.budgetWatchCancel = cancel
	go func() {
		ticker := time.NewTicker(budgetWatchInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.done:
				return
			case now := <-ticker.C:
				e.pollChildBudget(h, now)
			}
		}
	}()
}

func (e *Engine) pollChildBudget(h *childHandle, now time.Time) {
	if e == nil || h == nil {
		return
	}
	h.mu.Lock()
	if h.budget == nil || h.budget.escalated {
		h.mu.Unlock()
		return
	}
	trip, kind, reason, terminal := h.budget.evaluate(now, h.startedAt)
	// Soft stall rising edge: parent-visible signal without kill (#517).
	if !trip && h.budget.softStall && !h.budget.softStallSignaled {
		h.budget.softStallSignaled = true
		id := h.id
		name := h.name
		softReason := h.budget.softStallReason(now, h.startedAt)
		snap := h.budget.snapshot(now, h.startedAt)
		h.mu.Unlock()
		e.signalSoftStall(id, name, softReason, snap)
		return
	}
	if !trip || !h.budget.markEscalatedLocked(kind, reason, terminal) {
		h.mu.Unlock()
		return
	}
	id := h.id
	name := h.name
	snap := h.budget.snapshot(now, h.startedAt)
	h.mu.Unlock()
	e.escalateChildBudget(id, name, kind, reason, terminal, snap, h)
}

// signalSoftStall emits child.escalated action=signaled + lead mailbox and
// wakes waiters on task.stale / task.blocked. Does not interrupt the child.
func (e *Engine) signalSoftStall(id, name, reason string, snap tool.AgentBudgetSnapshot) {
	if e == nil || id == "" {
		return
	}
	depth := e.opts.Depth + 1
	view := budgetSnapshotToProtocol(snap)
	ev := protocol.ChildEscalated{
		Correlation: protocol.Correlation{
			SessionID:       id,
			ParentSessionID: e.opts.SessionID,
			Depth:           depth,
		},
		Name:   name,
		Kind:   "stall",
		Reason: reason,
		Action: protocol.EscalateActionSignaled,
		Budget: view,
	}
	e.emit(ev)
	e.persistChildEvent(id, ev)

	body := fmt.Sprintf(
		"[child.stale] session=%s kind=stall action=signaled\n%s",
		id, reason,
	)
	if name != "" {
		body = fmt.Sprintf(
			"[child.stale] session=%s name=%s kind=stall action=signaled\n%s",
			id, name, reason,
		)
	}
	if e.team != nil {
		to := e.team.LeadID()
		if d, ok := e.team.GetDelegation(id); ok && d.OwnerSessionID != "" {
			to = d.OwnerSessionID
		}
		_ = e.team.Deliver(e.opts.SessionID, to, body)
	}

	e.notifyWaiters(waitSignal{
		Kind:      tool.WaitEventTaskStale,
		SessionID: id,
		Name:      name,
		Status:    "needs_attention",
		Summary:   reason,
	})
	// Also wake waiters subscribed only to task.blocked (needs_attention).
	e.notifyWaitersBlocked(id, name)
	e.emitTeamRoster()
}

// escalateChildBudget emits child.escalated, notifies the lead, marks
// delegation, and either starts soft finalization or hard-interrupts the child.
// Safe to call once per handle (escalated flag is set by the caller).
func (e *Engine) escalateChildBudget(id, name, kind, reason string, terminal protocol.ChildStatus, snap tool.AgentBudgetSnapshot, h *childHandle) {
	if e == nil || id == "" {
		return
	}
	depth := e.opts.Depth + 1
	if h != nil && h.eng != nil {
		depth = h.eng.opts.Depth
	}

	// Soft budgets: one reserved finalization turn when the child engine can
	// still accept ops. Hard path when finalization is unsafe/impossible.
	tryFinalize := softBudgetAllowsFinalization(kind) &&
		h != nil && h.eng != nil && h.ops != nil
	if tryFinalize {
		// Parent life already ending → hard skip (no extra model call).
		if e.runCtx != nil && e.runCtx.Err() != nil {
			tryFinalize = false
		}
	}

	action := protocol.EscalateActionInterrupted
	if tryFinalize {
		action = protocol.EscalateActionFinalizing
		h.mu.Lock()
		if h.budget != nil {
			h.budget.finalizing = true
			h.budget.finalizationOutcome = "" // pending
		}
		h.mu.Unlock()
	} else if h != nil {
		h.mu.Lock()
		if h.budget != nil {
			h.budget.finalizationOutcome = protocol.FinalizationSkippedHard
		}
		h.mu.Unlock()
	}

	view := budgetSnapshotToProtocol(snap)
	ev := protocol.ChildEscalated{
		Correlation: protocol.Correlation{
			SessionID:       id,
			ParentSessionID: e.opts.SessionID,
			Depth:           depth,
		},
		Name:           name,
		Kind:           kind,
		Reason:         reason,
		Action:         action,
		TerminalStatus: terminal,
		Budget:         view,
	}
	e.emit(ev)
	e.persistChildEvent(id, ev)

	// Structured lead/owner notify (not silent kill).
	body := fmt.Sprintf(
		"[child.escalated] session=%s kind=%s action=%s\n%s",
		id, kind, action, reason,
	)
	if name != "" {
		body = fmt.Sprintf(
			"[child.escalated] session=%s name=%s kind=%s action=%s\n%s",
			id, name, kind, action, reason,
		)
	}
	if e.team != nil {
		to := e.team.LeadID()
		if d, ok := e.team.GetDelegation(id); ok && d.OwnerSessionID != "" {
			to = d.OwnerSessionID
		}
		_ = e.team.Deliver(e.opts.SessionID, to, body)
		// Park/fail the lifecycle object with structured reason.
		switch terminal {
		case protocol.ChildStatusBlocked:
			if d, ok := e.team.SetDelegationBlocked(id, reason); ok {
				e.emitDelegationChanged(d, protocol.DelegationWorking, "budget:"+kind)
			}
		case protocol.ChildStatusFailed:
			actor := strings.TrimSpace(e.opts.SessionID)
			if cur, ok := e.team.GetDelegation(id); ok && !IsTerminalDelegation(cur.State) {
				prev := cur.State
				if item, err := e.team.TransitionDelegation(cur.ID, actor, protocol.DelegationFailed, reason, 0); err == nil {
					e.emitDelegationChanged(item, prev, "budget:"+kind)
				}
			}
		}
	}

	if tryFinalize {
		e.startBudgetFinalization(h, kind, reason)
	} else if h != nil {
		// Hard interrupt child (cancel + Interrupt op).
		h.cancel()
		select {
		case h.ops <- protocol.Interrupt{}:
		default:
		}
	}
	e.emitTeamRoster()
}

// startBudgetFinalization interrupts the in-flight turn, injects a no-tools
// handoff prompt, and arms a reserve watchdog. Does not cancel childCtx until
// finalization completes or the reserve elapses (#879).
func (e *Engine) startBudgetFinalization(h *childHandle, kind, reason string) {
	if e == nil || h == nil || h.eng == nil {
		return
	}
	// Stop tools mid-turn; pending UserInput survives Interrupt.
	select {
	case h.ops <- protocol.Interrupt{}:
	default:
	}
	h.eng.enterBudgetFinalization(kind, reason)
	prompt := budgetFinalizationPrompt(kind, reason)
	select {
	case h.ops <- protocol.UserInput{Text: prompt}:
	default:
		// Ops blocked — fall back to hard stop.
		h.mu.Lock()
		if h.budget != nil {
			h.budget.finalizing = false
			h.budget.finalizationDone = true
			h.budget.finalizationOutcome = protocol.FinalizationFailed
		}
		h.mu.Unlock()
		h.cancel()
		return
	}
	go e.watchBudgetFinalization(h)
}

// watchBudgetFinalization hard-stops the child when the finalization reserve
// elapses or the child engine reports the finalization turn finished.
func (e *Engine) watchBudgetFinalization(h *childHandle) {
	if e == nil || h == nil {
		return
	}
	deadline := time.NewTimer(budgetFinalizationReserve)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			return
		case <-deadline.C:
			h.mu.Lock()
			if h.budget != nil && !h.budget.finalizationDone {
				h.budget.finalizationDone = true
				h.budget.finalizing = false
				if h.budget.finalizationOutcome == "" {
					h.budget.finalizationOutcome = protocol.FinalizationFailed
				}
			}
			h.mu.Unlock()
			if h.eng != nil {
				h.eng.leaveBudgetFinalization()
			}
			h.cancel()
			select {
			case h.ops <- protocol.Interrupt{}:
			default:
			}
			return
		case <-tick.C:
			if h.eng == nil {
				continue
			}
			if !h.eng.budgetFinalizationTurnDone() {
				continue
			}
			// Finalization turn finished — end the child (TaskOneShot may
			// already be exiting; cancel ensures drain completes).
			h.mu.Lock()
			if h.budget != nil && !h.budget.finalizationDone {
				h.budget.finalizationDone = true
				h.budget.finalizing = false
				// Outcome refined at ChildCompleted from parse result when empty.
				if h.budget.finalizationOutcome == "" {
					h.budget.finalizationOutcome = protocol.FinalizationFailed
				}
			}
			h.mu.Unlock()
			h.cancel()
			select {
			case h.ops <- protocol.Interrupt{}:
			default:
			}
			return
		}
	}
}

func (h *childHandle) budgetTerminal() (protocol.ChildStatus, string) {
	if h == nil {
		return "", ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.budget == nil || !h.budget.escalated || h.budget.terminal == "" {
		return "", ""
	}
	return h.budget.terminal, h.budget.reason
}

// budgetTerminalMeta returns budget kind + finalization outcome for ChildCompleted.
func (h *childHandle) budgetTerminalMeta() (kind, finalization string) {
	if h == nil {
		return "", ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.budget == nil || !h.budget.escalated {
		return "", ""
	}
	kind = h.budget.kind
	finalization = h.budget.finalizationOutcome
	if finalization == "" {
		if h.budget.finalizing || h.budget.finalizationDone {
			finalization = protocol.FinalizationFailed
		} else {
			finalization = protocol.FinalizationSkippedHard
		}
	}
	return kind, finalization
}

// markFinalizationOutcomeLocked records whether the reserved handoff parse succeeded.
// Caller holds childHandle.mu.
func (b *childBudget) markFinalizationOutcomeLocked(outcome string) {
	if b == nil {
		return
	}
	outcome = strings.TrimSpace(outcome)
	if outcome == "" {
		return
	}
	b.finalizationOutcome = outcome
	b.finalizationDone = true
	b.finalizing = false
}

func (e *Engine) childFilesTouched(sessionID string) []string {
	if e == nil || e.team == nil {
		return nil
	}
	own := e.team.Ownership()
	if own == nil {
		return nil
	}
	abs := own.PathsForSession(sessionID)
	if len(abs) == 0 {
		return nil
	}
	wd := strings.TrimSpace(e.opts.WorkDir)
	out := make([]string, 0, len(abs))
	for _, p := range abs {
		display := p
		if wd != "" {
			if rel, err := filepath.Rel(wd, p); err == nil &&
				rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				display = rel
			}
		}
		out = append(out, display)
	}
	return out
}

func budgetSnapshotToProtocol(s tool.AgentBudgetSnapshot) *protocol.AgentBudgetView {
	v := &protocol.AgentBudgetView{
		MaxWallClockS:        s.Limits.MaxWallClockS,
		MaxTokens:            s.Limits.MaxTokens,
		MaxCostUSD:           s.Limits.MaxCostUSD,
		MaxToolCalls:         s.Limits.MaxToolCalls,
		MaxDangerousTools:    s.Limits.MaxDangerousTools,
		StallAfterS:          s.Limits.StallAfterS,
		LoopDetectN:          s.Limits.LoopDetectN,
		ElapsedS:             s.ElapsedS,
		TokensUsed:           s.TokensUsed,
		CostUSDUsed:          s.CostUSDUsed,
		ToolCalls:            s.ToolCalls,
		DangerousTools:       s.DangerousTools,
		WallClockRemainingS:  s.WallClockRemainingS,
		TokensRemaining:      s.TokensRemaining,
		ToolCallsRemaining:   s.ToolCallsRemaining,
		DangerousRemaining:   s.DangerousRemaining,
		CostUSDRemaining:     s.CostUSDRemaining,
		Stall:                s.Stall,
		IdleS:                s.IdleS,
		LastProgressAt:       s.LastProgressAt,
		StallAfterSEffective: s.StallAfterSEffective,
		Loop:                 s.Loop,
		Escalated:            s.Escalated,
		EscalateKind:         s.EscalateKind,
		EscalateReason:       s.EscalateReason,
	}
	return v
}
