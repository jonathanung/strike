package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
	"github.com/jonathanung/strike-cli/internal/verify"
)

// childEventCap bounds in-memory transcript rows retained per child for task_read.
const childEventCap = 256

// childActivityCap bounds latest_activity lines for task_status.
const childActivityCap = 12

// leafTaskTools are stripped from registries that cannot nest further.
// Team tools (agent_roster, agent_ownership, agent_message, agent_broadcast,
// team_task, delegate) must NOT be listed here — depth-capped leaves still
// coordinate. task_message is parent-control and is stripped with task_*.
// delegate create/spawn is parent-side; list/get/transition stay available so
// leaves can self-report blocked/review (ownership-gated in engine).
var leafTaskTools = []string{
	"task", "task_status", "task_read", "task_message", "task_interrupt", "wait",
}

// childHandle tracks one non-blocking child engine while it runs.
type childHandle struct {
	id        string
	ops       chan<- protocol.Op
	cancel    context.CancelFunc
	done      chan struct{}
	permReply func(protocol.PermissionReply)
	qReply    func(protocol.QuestionReply)
	// eng is retained so the drain goroutine can read lastAssistantText.
	eng       *Engine
	startedAt time.Time
	agent     string
	prompt    string
	name      string // optional stable teammate alias
	// gates are independent completion conditions declared at spawn.
	gates []tool.VerifyGate
	// parent is the spawning engine (for budget escalation emit/notify).
	parent *Engine
	// budgetWatchCancel stops the per-child budget ticker.
	budgetWatchCancel context.CancelFunc

	mu           sync.Mutex
	currentTool  string
	awaitingPerm bool
	awaitingQ    bool
	turnRunning  bool
	// queue* tracks in-flight scheduler admission so task_status/roster can
	// identify the constrained pool instead of looking idle/working-generic.
	queueRequestID string
	queuePools     []string
	queueLabel     string
	activity       []string
	events         []tool.TaskTranscriptEntry // absolute index preserved in entry.Index
	nextEventIndex int
	// budget tracks per-child limits, usage, stall/loop, and escalation (#774).
	budget *childBudget
}

// childRecord retains terminal state + bounded transcript after a child exits
// so task_status/task_read still work without spawning a new child.
type childRecord struct {
	id           string
	startedAt    time.Time
	endedAt      time.Time
	agent        string
	prompt       string
	name         string
	status       protocol.ChildStatus
	summary      string
	handoff      protocol.CompletionHandoff
	verification *protocol.VerificationReport
	activity     []string
	events       []tool.TaskTranscriptEntry
	// Observability retained after exit (#774).
	objective    string
	lastAction   string
	filesTouched []string
	budgetSnap   tool.AgentBudgetSnapshot
	hasBudget    bool
}

// spawnChild starts a non-blocking child engine for the task tool and returns
// as soon as the child is running. The parent turn is not held open for the
// child's lifetime. Child Run and event drain each get their own goroutine
// under the parent engine's run context (not the parent turn context).
//
// Parent emits ChildStarted immediately and ChildCompleted when the child
// finishes. PermissionAsked/PermissionResolved, QuestionAsked/QuestionResolved,
// and nested ChildStarted/ChildCompleted are re-emitted from the child onto the
// parent event stream; optional ChildSession hooks persist the full child log.
//
// When the session team is present, a first-class delegation lifecycle object
// is created (criteria/deps/subscribe optional). Unmet deps return status
// "queued" without starting a child; dependents auto-spawn when deps reach done.
func (e *Engine) spawnChild(ctx context.Context, req tool.TaskRequest) (tool.TaskResult, error) {
	return e.spawnChildInner(ctx, req, "")
}

// spawnChildForDelegation starts a child for an existing queued delegation.
func (e *Engine) spawnChildForDelegation(ctx context.Context, d Delegation) (tool.TaskResult, error) {
	req := tool.TaskRequest{
		Prompt:    d.Prompt,
		Name:      d.Name,
		Agent:     d.Agent,
		Model:     d.Model,
		Effort:    d.Effort,
		Criteria:  d.Criteria,
		Deps:      d.Deps,
		Subscribe: d.Subscribe,
		Assignee:  d.Assignee,
		Verify:    append([]tool.VerifyGate(nil), d.Verify...),
		Budget:    d.Budget,
	}
	return e.spawnChildInner(ctx, req, d.ID)
}

func (e *Engine) spawnChildInner(ctx context.Context, req tool.TaskRequest, existingDelegationID string) (tool.TaskResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TaskResult{}, err
	}
	maxDepth := e.opts.MaxChildDepth
	if maxDepth == 0 {
		maxDepth = 1
	}
	if e.opts.Depth >= maxDepth {
		return tool.TaskResult{}, fmt.Errorf("task depth limit reached")
	}

	agentName := strings.TrimSpace(req.Agent)
	if agentName == "" {
		agentName = e.agent.Name
	}
	childAgent, ok := e.findAgent(agentName)
	if !ok {
		return tool.TaskResult{}, fmt.Errorf("unknown agent %q (available: %s)", agentName, agentNamesList(e.opts.Agents))
	}
	childHarness, childHarnessName, err := e.resolveHarness(childAgent)
	if err != nil {
		return tool.TaskResult{}, err
	}

	// Resolve optional model pin (catalog + Select) before opening a session
	// so invalid models / bad providers fail the tool with no child side effects.
	modelPin, err := e.resolveTaskModelPin(ctx, req.Model)
	if err != nil {
		return tool.TaskResult{}, err
	}
	// Resolve optional effort pin before opening a session so a bad level
	// fails the tool with no child side effects.
	effortPin, err := resolveTaskEffortPin(req.Effort)
	if err != nil {
		return tool.TaskResult{}, err
	}
	childEffort := e.effort
	if effortPin.lock {
		childEffort = effortPin.level
	}

	// Optional stable teammate alias: validate + uniqueness before side effects.
	memberName, err := ValidateMemberName(req.Name)
	if err != nil {
		return tool.TaskResult{}, err
	}
	if memberName != "" && e.team != nil {
		if owner, taken := e.team.NameOwner(memberName); taken {
			return tool.TaskResult{}, fmt.Errorf("name %q is already used by session %s", memberName, owner)
		}
	}

	// Delegation lifecycle: create (or reuse) before side effects so unmet deps
	// never open a child session.
	var (
		delegID string
		deleg   Delegation
	)
	if existingDelegationID != "" && e.team != nil {
		d, ok := e.team.GetDelegation(existingDelegationID)
		if !ok {
			return tool.TaskResult{}, fmt.Errorf("delegation %q not found", existingDelegationID)
		}
		if d.SessionID != "" {
			return tool.TaskResult{
				Output:       fmt.Sprintf("Delegation %s already linked to session %s", d.ID, d.SessionID),
				Status:       "started",
				SessionID:    d.SessionID,
				Name:         d.Name,
				DelegationID: d.ID,
				Lifecycle:    string(d.State),
			}, nil
		}
		// Ensure deps still satisfied.
		if unmet := unmetDepsFor(e.team, d.Deps); len(unmet) > 0 {
			return tool.TaskResult{
				Output:       fmt.Sprintf("Delegation %s still waiting on deps: %s", d.ID, strings.Join(unmet, ", ")),
				Status:       "queued",
				DelegationID: d.ID,
				Lifecycle:    string(protocol.DelegationQueued),
				Name:         d.Name,
			}, nil
		}
		delegID = d.ID
		deleg = d
	} else if e.team != nil {
		item, shouldSpawn, err := e.createDelegationForTask(req)
		if err != nil {
			return tool.TaskResult{}, err
		}
		delegID = item.ID
		deleg = item
		if !shouldSpawn {
			out := fmt.Sprintf(
				"Queued delegation %s (waiting on deps). No child started yet. Use delegate get/list or task_status with id %q; it will auto-start when dependencies reach done.",
				item.ID, item.ID,
			)
			if len(item.Deps) > 0 {
				out = fmt.Sprintf(
					"Queued delegation %s waiting on deps %v. No child started yet. It auto-starts when dependencies reach done; track via delegate get %s or task_status.",
					item.ID, item.Deps, item.ID,
				)
			}
			return tool.TaskResult{
				Output:       out,
				Status:       "queued",
				DelegationID: item.ID,
				Lifecycle:    string(item.State),
				Name:         item.Name,
			}, nil
		}
	}

	childID := rand.Text()
	title := briefAgentSessionTitle(agentName, childID)
	if e.opts.OpenChildSession != nil {
		id, err := e.opts.OpenChildSession(e.opts.SessionID, childID, title)
		if err != nil {
			e.failDelegationSpawn(delegID, "open child session: "+err.Error())
			return tool.TaskResult{}, fmt.Errorf("open child session: %w", err)
		}
		if strings.TrimSpace(id) != "" {
			childID = id
			// Re-derive after the host may have rewritten the id.
			title = briefAgentSessionTitle(agentName, childID)
		}
	}

	childDepth := e.opts.Depth + 1
	// Strip task only when the child cannot nest further (depth >= max).
	// Otherwise keep task so MaxChildDepth > 1 can actually nest; SpawnTask
	// is injected when Depth < MaxChildDepth (see executeTool).
	var childReg *tool.Registry
	if e.opts.Registry == nil {
		childReg = tool.NewRegistry()
	} else if childDepth >= maxDepth {
		childReg = e.opts.Registry.CloneWithout(leafTaskTools...)
	} else {
		childReg = e.opts.Registry.CloneWithout()
	}

	// Parent effective ceiling: configured layers plus the active parent
	// agent profile and approved workflow phase profile. Session always-grants
	// are intentionally omitted so the child starts with an empty granted set.
	// Child agent Allows that would override a parent Deny are dropped (AG3);
	// Ask→Allow is kept so personas like general (bash allow) work as task
	// subagents. Phase rules propagate so children cannot widen beyond the
	// parent’s approved phase ceiling.
	parentLayers := append([]permission.Ruleset(nil), e.opts.Rules...)
	if len(e.agent.Permissions) > 0 {
		parentLayers = append(parentLayers, append(permission.Ruleset(nil), e.agent.Permissions...))
	}
	if phaseRules := e.perms.PhaseRules(); len(phaseRules) > 0 {
		parentLayers = append(parentLayers, phaseRules)
	}
	child := New(Options{
		SessionID:                  childID,
		ParentSessionID:            e.opts.SessionID,
		RootSessionID:              e.rootSessionID(),
		Depth:                      childDepth,
		MaxChildDepth:              maxDepth,
		TaskOneShot:                true,
		Team:                       e.team, // share lead roster; nested enrolls on same team
		Select:                     e.opts.Select,
		Registry:                   childReg,
		WorkDir:                    e.opts.WorkDir,
		ProjectRoot:                e.opts.ProjectRoot,
		Instructions:               e.opts.Instructions,
		Memory:                     e.opts.Memory,
		SystemPrompt:               e.opts.SystemPrompt,
		LeanCode:                   e.opts.LeanCode,
		HarnessRegistry:            e.opts.HarnessRegistry,
		Scheduler:                  e.opts.Scheduler,          // share process-local pools
		SchedulerPolicy:            e.opts.SchedulerPolicy,    // bash classification rules
		FileSync:                   e.opts.FileSync,           // share LSP document sync
		CollectDiagnostics:         e.opts.CollectDiagnostics, // share LSP result injection
		Agents:                     e.opts.Agents,
		InitialAgent:               agentName,
		InitialProvider:            e.provName,
		InitialModel:               e.model,
		InitialEffort:              childEffort,
		InitialTitled:              title != "",
		SandboxMode:                e.opts.SandboxMode,
		NetworkAllow:               e.opts.NetworkAllow,
		AllowYoloWithoutSandbox:    e.opts.AllowYoloWithoutSandbox,
		MaxTokens:                  e.opts.MaxTokens,
		MaxStreamAttempts:          e.opts.MaxStreamAttempts,
		StreamRetryBackoff:         e.opts.StreamRetryBackoff,
		ContextWindow:              e.contextWindow(),
		LookupContextWindow:        e.opts.LookupContextWindow,
		ListModels:                 e.opts.ListModels,
		LockModel:                  modelPin.lock,
		LockEffort:                 effortPin.lock,
		CompactionThreshold:        e.opts.CompactionThreshold,
		CompactionBuffer:           e.opts.CompactionBuffer,
		KeepUserTurns:              e.opts.KeepUserTurns,
		PruneProtectTokens:         e.opts.PruneProtectTokens,
		PruneMinimumTokens:         e.opts.PruneMinimumTokens,
		PruneKeepUserTurns:         e.opts.PruneKeepUserTurns,
		PruneProtectTools:          e.opts.PruneProtectTools,
		CompactionStrategy:         e.opts.CompactionStrategy,
		CompactionModel:            e.opts.CompactionModel,
		Rules:                      permission.DeriveChildRules(parentLayers, childDepth >= maxDepth, childAgent.Permissions),
		Hooks:                      e.opts.Hooks,
		HookRules:                  e.opts.HookRules,
		PersistProjectRule:         e.opts.PersistProjectRule,
		DangerouslySkipPermissions: e.opts.DangerouslySkipPermissions,
		DefaultChildBudget:         e.opts.DefaultChildBudget,
	})
	child.taskHarness = childHarness
	child.taskHarnessName = childHarnessName

	// Inherit the parent's live provider/model/priority, then optionally apply
	// a task model pin. Clearing InitialProvider avoids Run's silent Select
	// failure leaving the child with no model while the parent is healthy.
	// Agent pins may still re-Select in handleSelectAgent unless LockModel.
	e.applyChildProviderModel(child, modelPin)

	// Child lifetime follows the parent engine, not the invoking turn.
	parentLife := e.runCtx
	if parentLife == nil {
		parentLife = context.Background()
	}
	childCtx, cancel := context.WithCancel(parentLife)

	childCorr := protocol.Correlation{
		SessionID:       childID,
		ParentSessionID: e.opts.SessionID,
		Depth:           childDepth,
	}

	startedAt := time.Now()
	// Prefer delegation-stored budget (already merged) when present.
	budgetLimits := MergeAgentBudget(e.opts.DefaultChildBudget, req.Budget)
	if delegID != "" && e.team != nil {
		if d, ok := e.team.GetDelegation(delegID); ok {
			// Non-zero stored budget wins as the full merged snapshot.
			if d.Budget != (tool.AgentBudgetLimits{}) {
				budgetLimits = NormalizeAgentBudget(d.Budget)
			}
		}
	}
	h := &childHandle{
		id:        childID,
		ops:       child.Ops(),
		cancel:    cancel,
		done:      make(chan struct{}),
		permReply: child.perms.Reply,
		qReply:    child.questions.Reply,
		eng:       child,
		startedAt: startedAt,
		agent:     agentName,
		prompt:    req.Prompt,
		name:      memberName,
		gates:     append([]tool.VerifyGate(nil), req.Verify...),
		parent:    e,
		budget:    newChildBudget(budgetLimits, req.Prompt, startedAt),
	}

	e.childMu.Lock()
	if e.children == nil {
		e.children = make(map[string]*childHandle)
	}
	e.children[childID] = h
	e.childMu.Unlock()

	// Auto-enroll on the lead's implicit team (no TeamCreate).
	if e.team != nil {
		if !e.team.Enroll(TeamMember{
			SessionID:       childID,
			Name:            memberName,
			Persona:         agentName,
			State:           protocol.TeamMemberRunning,
			ParentSessionID: e.opts.SessionID,
			Depth:           childDepth,
			StartedAt:       h.startedAt,
		}) {
			// Race: another spawn claimed the name between NameOwner and Enroll.
			e.childMu.Lock()
			delete(e.children, childID)
			e.childMu.Unlock()
			cancel()
			e.closeChildSession(childID)
			e.failDelegationSpawn(delegID, "failed to enroll child on team")
			if memberName != "" {
				return tool.TaskResult{}, fmt.Errorf("name %q is already used on this team", memberName)
			}
			return tool.TaskResult{}, fmt.Errorf("failed to enroll child on team")
		}
	}

	// Link delegation → session and move to working.
	if delegID != "" && e.team != nil {
		prev := deleg.State
		linked, err := e.team.LinkDelegationSession(delegID, childID)
		if err != nil {
			// Child already running — keep going but surface link failure in output later.
			deleg.SessionID = childID
		} else {
			deleg = linked
			e.emitDelegationChanged(deleg, prev, "spawned")
		}
	}

	startedEv := protocol.ChildStarted{
		Correlation: childCorr,
		Agent:       agentName,
		Prompt:      req.Prompt,
		Name:        memberName,
	}
	e.emit(startedEv)
	e.persistChildEvent(childID, startedEv)
	h.noteEvent(startedEv)
	e.emitTeamRoster()

	// stopReason is delivered once when the child turn ends. Buffer 1 so the
	// drain goroutine never blocks on a late reader.
	stopCh := make(chan string, 1)
	var (
		failMu  sync.Mutex
		failMsg string
	)
	go func() {
		defer close(h.done)
		defer e.closeChildSession(childID)
		// Run returns on TaskOneShot idle (or cancel); release childCtx after.
		defer cancel()

		for ev := range child.Events() {
			e.persistChildEvent(childID, ev)
			h.noteEvent(ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				e.emit(ev)
				e.notifyWaitersBlocked(childID, memberName)
			case protocol.PermissionResolved:
				e.emit(ev)
			case protocol.QuestionAsked:
				e.emit(ev)
				e.notifyWaitersBlocked(childID, memberName)
			case protocol.QuestionResolved:
				e.emit(ev)
			case protocol.ChildStarted:
				// Nested grandchildren: re-emit so root TUI/tree sees lineage.
				e.emit(ev)
			case protocol.ChildCompleted:
				e.emit(ev)
			case protocol.AgentMessage:
				// Peer mailbox traffic on a child: surface on the parent
				// stream for TUI/debug (recipient correlation retained).
				e.emit(ev)
			case protocol.TeamRoster:
				// Nested engines share the lead team; bubble roster snapshots.
				e.emit(ev)
			case protocol.PathOverlap:
				// Multi-agent path conflicts from child writers surface on the
				// parent stream for lead/UI visibility.
				e.emit(ev)
			case protocol.SchedulerQueued, protocol.SchedulerAdmitted, protocol.SchedulerCanceled:
				// Surface queue lifecycle so parent TUI/task_status can show
				// which pool a child is waiting on (not idle).
				e.emit(ev)
			case protocol.TurnCompleted:
				// One-shot task: record stop reason. Do not cancel here —
				// TaskOneShot Run exits when idle so nested grandchildren
				// (MaxChildDepth > 1) can finish under this engine.
				select {
				case stopCh <- ev.StopReason:
				default:
				}
			case protocol.EngineError:
				// Pre-turn failures (e.g. no provider) never emit TurnCompleted.
				// Mid-turn errors are followed by TurnCompleted{"error"}; the
				// first stopCh send wins either way.
				if msg := strings.TrimSpace(ev.Message); msg != "" {
					failMu.Lock()
					if failMsg == "" {
						failMsg = msg
					}
					failMu.Unlock()
				}
				select {
				case stopCh <- "error":
				default:
				}
				cancel()
			}
		}

		// Ensure Run exits even if Events closed without a terminal signal.
		cancel()

		stopReason := ""
		gotStop := false
		select {
		case stopReason = <-stopCh:
			gotStop = true
		default:
		}

		// If parent engine life ended without a child turn terminal, treat as cancel.
		assistantText := lastAssistantText(child.messages)
		var status protocol.ChildStatus
		var errText string
		// Budget escalation may race interrupt: prefer structured budget terminal.
		budgetStatus, budgetReason := h.budgetTerminal()
		switch {
		case budgetStatus != "":
			status = budgetStatus
			errText = budgetReason
		case (childCtx.Err() != nil && !gotStop) || (gotStop && stopReason == "interrupted"):
			status = protocol.ChildStatusCanceled
		case !gotStop || stopReason == "error":
			status = protocol.ChildStatusFailed
			failMu.Lock()
			errText = failMsg
			failMu.Unlock()
		default:
			status = protocol.ChildStatusCompleted
		}
		// Structured handoff: parse model JSON from assistant text first (before
		// appending engine error suffixes), then merge engine file tracking.
		handoff := buildCompletionHandoff(status, assistantText, child.mutatedPathsSnapshot())
		if errText != "" {
			if strings.TrimSpace(handoff.Summary) == "" || handoff.Summary == defaultHandoffSummary(status) {
				handoff.Summary = errText
			} else if !strings.Contains(handoff.Summary, errText) {
				handoff.Summary = handoff.Summary + "\n\nError: " + errText
			}
			if len(handoff.Blockers) == 0 {
				handoff.Blockers = []string{errText}
			} else {
				// Keep model blockers; ensure error is visible.
				found := false
				for _, b := range handoff.Blockers {
					if strings.Contains(b, errText) {
						found = true
						break
					}
				}
				if !found {
					handoff.Blockers = append(handoff.Blockers, errText)
				}
			}
		}
		if strings.TrimSpace(handoff.Summary) == "" {
			handoff.Summary = defaultHandoffSummary(status)
		}

		// Independent verification gates: implementer-done ≠ harness-verified.
		// Only when the child claimed a successful completion and gates were
		// configured at spawn. Model handoff.Verification text is never evidence.
		var verification *protocol.VerificationReport
		if status == protocol.ChildStatusCompleted && len(h.gates) > 0 {
			verification = e.runChildVerification(h, child, handoff)
			if verification != nil && !verification.Passed {
				status = protocol.ChildStatusBlocked
				// Surface gate failures as actionable blockers for the lead.
				for _, line := range verify.FailedCheckLines(protocolReportToVerifyResult(verification)) {
					handoff.Blockers = appendUniqueString(handoff.Blockers, line)
				}
				if verification.Summary != "" {
					// Keep model summary; prefix blocked reason for terminal Summary.
					if strings.TrimSpace(handoff.Summary) == "" || handoff.Summary == defaultHandoffSummary(protocol.ChildStatusCompleted) {
						handoff.Summary = verification.Summary
					}
				}
			}
		}

		completed := protocol.ChildCompleted{
			Correlation:  childCorr,
			Status:       status,
			Summary:      handoff.Summary,
			Name:         memberName,
			Handoff:      handoff,
			DelegationID: delegID,
			Verification: verification,
		}
		// Prefer verification summary on blocked terminal Summary field.
		if status == protocol.ChildStatusBlocked && verification != nil && verification.Summary != "" {
			completed.Summary = verification.Summary
		}
		e.emit(completed)
		e.persistChildEvent(childID, completed)
		h.noteEvent(completed)
		e.finishChild(h, completed)
		e.onChildDelegationTerminal(childID, status)
		// Wake wait-tool subscribers before the model-facing notice path so
		// mid-turn wait + complete races resolve without busy-polling.
		e.notifyWaitersFromCompleted(completed)
		// Wake Run so the parent can inject a model-visible summary (and
		// auto-nudge when idle). Non-blocking: drop if Run is shutting down.
		select {
		case e.childDone <- completed:
		default:
		}
	}()

	go child.Run(childCtx)
	e.startChildBudgetWatch(h)

	// Deliver the subtask prompt unless the parent turn context is already done.
	select {
	case <-ctx.Done():
		cancel()
		select {
		case child.Ops() <- protocol.Interrupt{}:
		default:
		}
		return tool.TaskResult{}, ctx.Err()
	case child.Ops() <- protocol.UserInput{Text: req.Prompt}:
	}

	var out string
	if memberName != "" {
		out = fmt.Sprintf(
			"Started child session %s (name %s, agent %s). It runs independently and does not block this turn. Continue other work if useful; a [child.completed] message will deliver the terminal summary automatically — do not sleep-poll for it. Address this teammate by name %q or session id.",
			childID, memberName, agentName, memberName,
		)
	} else {
		out = fmt.Sprintf(
			"Started child session %s (agent %s). It runs independently and does not block this turn. Continue other work if useful; a [child.completed] message will deliver the terminal summary automatically — do not sleep-poll for it.",
			childID, agentName,
		)
	}
	if delegID != "" {
		out += fmt.Sprintf(" Delegation %s lifecycle=%s.", delegID, deleg.State)
	}
	lifecycle := string(deleg.State)
	if lifecycle == "" && delegID != "" {
		lifecycle = string(protocol.DelegationWorking)
	}
	return tool.TaskResult{
		Output:       out,
		Status:       "started",
		SessionID:    childID,
		Name:         memberName,
		DelegationID: delegID,
		Lifecycle:    lifecycle,
	}, nil
}

// unmetDepsFor returns dependency ids that are not yet done (exported helper for spawn).
func unmetDepsFor(t *Team, deps []string) []string {
	if t == nil || len(deps) == 0 {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return unmetDepsLocked(t, deps)
}

// finishChild moves a live handle into childHistory with terminal state.
func (e *Engine) finishChild(h *childHandle, completed protocol.ChildCompleted) {
	if h == nil {
		return
	}
	if h.budgetWatchCancel != nil {
		h.budgetWatchCancel()
	}
	now := time.Now()
	h.mu.Lock()
	rec := &childRecord{
		id:           h.id,
		startedAt:    h.startedAt,
		endedAt:      now,
		agent:        h.agent,
		prompt:       h.prompt,
		name:         h.name,
		status:       completed.Status,
		summary:      completed.Summary,
		handoff:      completed.Handoff,
		verification: completed.Verification,
		activity:     append([]string(nil), h.activity...),
		events:       append([]tool.TaskTranscriptEntry(nil), h.events...),
	}
	if h.budget != nil {
		rec.objective = h.budget.objective
		rec.lastAction = h.budget.lastAction
		rec.budgetSnap = h.budget.snapshot(now, h.startedAt)
		rec.hasBudget = true
	}
	h.mu.Unlock()
	rec.filesTouched = e.childFilesTouched(h.id)

	// Stop accepting peer mail before the handle leaves the live map.
	if e.team != nil {
		e.team.DetachMailbox(h.id)
	}

	e.childMu.Lock()
	delete(e.children, h.id)
	if e.childHistory == nil {
		e.childHistory = make(map[string]*childRecord)
	}
	e.childHistory[h.id] = rec
	// Terminal members remain listable on the team until lead Dissolve.
	if e.team != nil {
		e.team.SetTerminal(h.id, protocol.TeamMemberStateFromChild(completed.Status), completed.Summary)
		// Merge structured handoff files_changed into the ownership graph,
		// then deactivate so finished children no longer cause overlap.
		if own := e.team.Ownership(); own != nil {
			e.RecordChildFilesChanged(h.id, h.name, completed.Handoff.FilesChanged)
			own.DeactivateSession(h.id)
		}
	}
	e.childMu.Unlock()
	e.emitTeamRoster()
}

// dissolveTeamIfLead clears the implicit team when this engine is the lead.
// Nested engines share the pointer and must not dissolve the lead's roster.
// No team.roster event here: session end already tears down the UI, and
// emitting would re-append to the JSONL on every quiet resume exit.
func (e *Engine) dissolveTeamIfLead() {
	if e == nil || e.team == nil {
		return
	}
	if e.team.LeadID() != e.opts.SessionID {
		return
	}
	e.team.Dissolve()
}

func (h *childHandle) noteEvent(ev protocol.Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	switch ev := ev.(type) {
	case protocol.TurnStarted:
		h.turnRunning = true
		h.awaitingPerm = false
		h.awaitingQ = false
		h.currentTool = ""
		h.pushActivityLocked("turn started")
		if h.budget != nil {
			h.budget.noteProgress(now, "turn started")
		}
	case protocol.TurnCompleted:
		h.turnRunning = false
		h.awaitingPerm = false
		h.awaitingQ = false
		h.currentTool = ""
		h.pushActivityLocked("turn completed (" + ev.StopReason + ")")
		if h.budget != nil {
			h.budget.noteProgress(now, "turn completed")
		}
	case protocol.ToolCallBegin:
		h.currentTool = ev.Name
		h.pushActivityLocked("tool " + ev.Name)
		if h.budget != nil {
			h.budget.noteTool(ev.Name, now)
		}
	case protocol.ToolCallEnd:
		prev := h.currentTool
		h.currentTool = ""
		label := "tool end"
		if prev != "" {
			label = "tool end " + prev
		} else if t := strings.TrimSpace(ev.Title); t != "" {
			label = "tool end " + t
		}
		if ev.IsError {
			label += " (error)"
		}
		h.pushActivityLocked(label)
		if h.budget != nil {
			h.budget.noteProgress(now, label)
		}
	case protocol.UsageReported:
		// Accumulate stream usage toward token budget (#774).
		tokens := 0
		if ev.Used.Known {
			tokens = ev.Used.N
		} else {
			if ev.Input.Known {
				tokens += ev.Input.N
			}
			if ev.Output.Known {
				tokens += ev.Output.N
			}
			if ev.CacheRead.Known {
				tokens += ev.CacheRead.N
			}
			if ev.CacheCreation.Known {
				tokens += ev.CacheCreation.N
			}
		}
		if h.budget != nil && tokens > 0 {
			h.budget.noteUsage(tokens, now)
		}
	case protocol.PermissionAsked:
		h.awaitingPerm = true
		h.pushActivityLocked("needs permission: " + ev.Permission)
	case protocol.PermissionResolved:
		h.awaitingPerm = false
		h.pushActivityLocked("permission resolved")
		if h.budget != nil {
			h.budget.noteProgress(now, "permission resolved")
		}
	case protocol.QuestionAsked:
		h.awaitingQ = true
		h.pushActivityLocked("needs user question")
	case protocol.QuestionResolved:
		h.awaitingQ = false
		h.pushActivityLocked("question resolved")
		if h.budget != nil {
			h.budget.noteProgress(now, "question resolved")
		}
	case protocol.ChildStarted:
		h.pushActivityLocked("started")
	case protocol.ChildCompleted:
		h.pushActivityLocked("completed (" + string(ev.Status) + ")")
	case protocol.UserMessage:
		if t := strings.TrimSpace(ev.Text); t != "" {
			h.pushActivityLocked(truncateRunes(t, 80))
			if h.budget != nil {
				h.budget.noteProgress(now, "user message")
			}
		}
	case protocol.TextDelta:
		// skip high-frequency deltas in activity
	case protocol.HarnessProgress:
		summary := strings.TrimSpace(ev.Name + ": " + string(ev.Payload))
		h.pushActivityLocked(truncateRunes(strings.TrimSuffix(summary, ":"), 80))
		if h.budget != nil {
			h.budget.noteProgress(now, "harness progress")
		}
	case protocol.EngineError:
		if msg := strings.TrimSpace(ev.Message); msg != "" {
			h.pushActivityLocked("error: " + truncateRunes(msg, 80))
		}
	case protocol.SchedulerQueued:
		h.queueRequestID = ev.RequestID
		h.queuePools = append([]string(nil), ev.Pools...)
		h.queueLabel = ev.Label
		h.pushActivityLocked(queueActivityLine("queued", ev.Label, ev.Pools))
	case protocol.SchedulerAdmitted:
		if h.queueRequestID == "" || h.queueRequestID == ev.RequestID {
			h.queueRequestID = ""
			h.queuePools = nil
			h.queueLabel = ""
		}
		h.pushActivityLocked(queueActivityLine("admitted", ev.Label, ev.Pools))
	case protocol.SchedulerCanceled:
		if h.queueRequestID == "" || h.queueRequestID == ev.RequestID {
			h.queueRequestID = ""
			h.queuePools = nil
			h.queueLabel = ""
		}
		h.pushActivityLocked(queueActivityLine("queue canceled", ev.Label, ev.Pools))
	}

	// Hard budget check after usage/tool notes (same lock). Defer parent-side
	// escalate until after we finish locked bookkeeping so we never double-Unlock.
	var (
		escParent   *Engine
		escID       string
		escName     string
		escKind     string
		escReason   string
		escTerminal protocol.ChildStatus
		escSnap     tool.AgentBudgetSnapshot
		doEscalate  bool
	)
	if h.budget != nil && !h.budget.escalated {
		if trip, kind, reason, terminal := h.budget.evaluate(now, h.startedAt); trip {
			if h.budget.markEscalatedLocked(kind, reason, terminal) {
				h.pushActivityLocked("escalated: " + kind)
				escParent = h.parent
				escID = h.id
				escName = h.name
				escKind = kind
				escReason = reason
				escTerminal = terminal
				escSnap = h.budget.snapshot(now, h.startedAt)
				doEscalate = true
			}
		}
	}

	if entry, ok := summarizeChildEvent(h.nextEventIndex, ev); ok {
		h.events = append(h.events, entry)
		if len(h.events) > childEventCap {
			h.events = h.events[len(h.events)-childEventCap:]
		}
		h.nextEventIndex++
	}

	if doEscalate && escParent != nil {
		// Unlock for emit/interrupt; re-lock so deferred Unlock is balanced.
		h.mu.Unlock()
		escParent.escalateChildBudget(escID, escName, escKind, escReason, escTerminal, escSnap, h)
		h.mu.Lock()
	}
}

func (h *childHandle) pushActivityLocked(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	h.activity = append(h.activity, line)
	if len(h.activity) > childActivityCap {
		h.activity = h.activity[len(h.activity)-childActivityCap:]
	}
}

func summarizeChildEvent(index int, ev protocol.Event) (tool.TaskTranscriptEntry, bool) {
	switch ev := ev.(type) {
	case protocol.ChildStarted:
		return tool.TaskTranscriptEntry{Index: index, Kind: "child.started", Summary: "agent=" + ev.Agent}, true
	case protocol.ChildCompleted:
		return tool.TaskTranscriptEntry{Index: index, Kind: "child.completed", Summary: string(ev.Status) + ": " + truncateRunes(ev.Summary, 200)}, true
	case protocol.SchedulerQueued:
		return tool.TaskTranscriptEntry{Index: index, Kind: "scheduler.queued", Summary: queueActivityLine("queued", ev.Label, ev.Pools)}, true
	case protocol.SchedulerAdmitted:
		return tool.TaskTranscriptEntry{Index: index, Kind: "scheduler.admitted", Summary: queueActivityLine("admitted", ev.Label, ev.Pools)}, true
	case protocol.SchedulerCanceled:
		return tool.TaskTranscriptEntry{Index: index, Kind: "scheduler.canceled", Summary: queueActivityLine("canceled", ev.Label, ev.Pools)}, true
	case protocol.UserMessage:
		return tool.TaskTranscriptEntry{Index: index, Kind: "user", Summary: truncateRunes(ev.Text, 240)}, true
	case protocol.TurnStarted:
		return tool.TaskTranscriptEntry{Index: index, Kind: "turn.started", Summary: ev.TurnID}, true
	case protocol.TurnCompleted:
		return tool.TaskTranscriptEntry{Index: index, Kind: "turn.completed", Summary: ev.StopReason}, true
	case protocol.ToolCallBegin:
		return tool.TaskTranscriptEntry{Index: index, Kind: "tool.begin", Summary: ev.Name}, true
	case protocol.ToolCallEnd:
		s := strings.TrimSpace(ev.Title)
		if s == "" {
			s = ev.CallID
		}
		if ev.IsError {
			s += " error"
		}
		if out := strings.TrimSpace(ev.Output); out != "" {
			s += ": " + truncateRunes(out, 160)
		}
		return tool.TaskTranscriptEntry{Index: index, Kind: "tool.end", Summary: s}, true
	case protocol.PermissionAsked:
		return tool.TaskTranscriptEntry{Index: index, Kind: "permission.asked", Summary: ev.Permission}, true
	case protocol.PermissionResolved:
		return tool.TaskTranscriptEntry{Index: index, Kind: "permission.resolved", Summary: string(ev.Decision)}, true
	case protocol.QuestionAsked:
		return tool.TaskTranscriptEntry{Index: index, Kind: "question.asked", Summary: ev.RequestID}, true
	case protocol.QuestionResolved:
		return tool.TaskTranscriptEntry{Index: index, Kind: "question.resolved", Summary: ev.RequestID}, true
	case protocol.ReasoningDelta:
		if t := strings.TrimSpace(ev.Text); t != "" {
			return tool.TaskTranscriptEntry{Index: index, Kind: "reasoning", Summary: truncateRunes(t, 160)}, true
		}
	case protocol.HarnessProgress:
		summary := strings.TrimSpace(ev.Name + ": " + string(ev.Payload))
		return tool.TaskTranscriptEntry{Index: index, Kind: "harness.progress", Summary: truncateRunes(strings.TrimSuffix(summary, ":"), 200)}, true
	case protocol.EngineError:
		return tool.TaskTranscriptEntry{Index: index, Kind: "error", Summary: truncateRunes(ev.Message, 200)}, true
	}
	return tool.TaskTranscriptEntry{}, false
}

func (e *Engine) childStatus(ctx context.Context, req tool.TaskStatusRequest) (tool.TaskStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TaskStatusResult{}, err
	}
	id := strings.TrimSpace(req.SessionID)
	if id == "" {
		return tool.TaskStatusResult{}, fmt.Errorf("session_id is required")
	}
	// Delegation id (dN) with no child yet: return lifecycle snapshot.
	if e.team != nil {
		if d, ok := e.team.GetDelegation(id); ok && d.SessionID == "" {
			res := tool.TaskStatusResult{
				State:     string(d.State),
				SessionID: d.SessionID,
				Elapsed:   formatElapsed(time.Since(d.CreatedAt)),
			}
			return attachLifecycleFields(res, d), nil
		}
		if d, ok := e.team.GetDelegation(id); ok && d.SessionID != "" {
			id = d.SessionID
		}
	}
	id = e.resolveOwnedChildRef(id)

	e.childMu.Lock()
	h := e.children[id]
	rec := e.childHistory[id]
	e.childMu.Unlock()

	if h != nil {
		res := h.statusSnapshot(req.IncludeRecent)
		res.FilesTouched = e.childFilesTouched(id)
		e.applyDelegationLifecycle(&res, id)
		// Sync blocked ↔ needs_attention for live children.
		if e.team != nil {
			if res.State == "needs_attention" {
				if d, ok := e.team.SetDelegationBlocked(id, "needs_attention"); ok && d.State == protocol.DelegationBlocked {
					e.applyDelegationLifecycle(&res, id)
				}
			} else if res.Lifecycle == string(protocol.DelegationBlocked) && res.BlockReason == "needs_attention" {
				if d, ok := e.team.SetDelegationWorking(id); ok {
					e.applyDelegationLifecycle(&res, id)
					_ = d
				}
			}
		}
		return res, nil
	}
	if rec != nil {
		res := rec.statusSnapshot(req.IncludeRecent)
		if len(res.FilesTouched) == 0 {
			res.FilesTouched = e.childFilesTouched(id)
		}
		e.applyDelegationLifecycle(&res, id)
		return res, nil
	}
	return tool.TaskStatusResult{}, fmt.Errorf("unknown or inaccessible child session %q", id)
}

// resolveOwnedChildRef maps a session id or team name alias to a session id.
// When the ref is not on the team, the original string is returned so ownership
// checks still fail closed with the caller's token.
func (e *Engine) resolveOwnedChildRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" || e == nil || e.team == nil {
		return ref
	}
	if id, ok := e.team.Resolve(ref); ok {
		return id
	}
	return ref
}

func (h *childHandle) statusSnapshot(includeRecent bool) tool.TaskStatusResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	// Refresh soft stall/loop flags for live pulse.
	if h.budget != nil {
		_, _, _, _ = h.budget.evaluate(now, h.startedAt)
	}
	state := "starting"
	switch {
	case h.budget != nil && h.budget.escalated && h.budget.terminal == protocol.ChildStatusBlocked:
		state = "blocked"
	case h.budget != nil && h.budget.escalated:
		state = "failed"
	case h.awaitingPerm || h.awaitingQ:
		state = "needs_attention"
	case len(h.queuePools) > 0:
		// Waiting on a pool is still live work — never report idle/starting.
		state = "working"
	case h.turnRunning || h.currentTool != "":
		state = "working"
	case h.nextEventIndex > 1:
		state = "working"
	}
	out := tool.TaskStatusResult{
		SessionID:   h.id,
		State:       state,
		Elapsed:     formatElapsed(now.Sub(h.startedAt)),
		CurrentTool: h.currentTool,
		QueuePools:  append([]string(nil), h.queuePools...),
		QueueLabel:  h.queueLabel,
	}
	if includeRecent && len(h.activity) > 0 {
		out.LatestActivity = append([]string(nil), h.activity...)
	}
	if h.budget != nil {
		out.Objective = h.budget.objective
		out.LastAction = h.budget.lastAction
		if h.budget.escalated && h.budget.reason != "" {
			out.BlockReason = h.budget.reason
		}
		out.Budget = h.budget.snapshot(now, h.startedAt)
		out.HasBudget = true
	}
	return out
}

// queueActivityLine formats a short activity pulse for scheduler lifecycle.
func queueActivityLine(phase, label string, pools []string) string {
	tag := strings.TrimSpace(label)
	if tag == "" && len(pools) > 0 {
		tag = strings.Join(pools, ",")
	}
	if tag == "" {
		return phase
	}
	return phase + " " + tag
}

func (r *childRecord) statusSnapshot(includeRecent bool) tool.TaskStatusResult {
	state := string(r.status)
	if state == "" {
		state = string(protocol.ChildStatusCompleted)
	}
	elapsed := r.endedAt.Sub(r.startedAt)
	if r.endedAt.IsZero() {
		elapsed = time.Since(r.startedAt)
	}
	out := tool.TaskStatusResult{
		SessionID:       r.id,
		State:           state,
		Elapsed:         formatElapsed(elapsed),
		TerminalSummary: r.summary,
		Handoff:         toolHandoff(r.handoff),
		HasHandoff:      true,
		Objective:       r.objective,
		LastAction:      r.lastAction,
		FilesTouched:    append([]string(nil), r.filesTouched...),
	}
	if r.verification != nil {
		out.Verification = toolVerification(*r.verification)
		out.HasVerification = true
	}
	if includeRecent && len(r.activity) > 0 {
		out.LatestActivity = append([]string(nil), r.activity...)
	}
	if r.hasBudget {
		out.Budget = r.budgetSnap
		out.HasBudget = true
		if r.budgetSnap.EscalateReason != "" && out.BlockReason == "" {
			out.BlockReason = r.budgetSnap.EscalateReason
		}
	}
	return out
}

func toolHandoff(h protocol.CompletionHandoff) tool.CompletionHandoff {
	files := h.FilesChanged
	if files == nil {
		files = []string{}
	}
	findings := h.Findings
	if findings == nil {
		findings = []string{}
	}
	blockers := h.Blockers
	if blockers == nil {
		blockers = []string{}
	}
	return tool.CompletionHandoff{
		Summary:               h.Summary,
		FilesChanged:          files,
		Verification:          h.Verification,
		Findings:              findings,
		Blockers:              blockers,
		RecommendedNextAction: h.RecommendedNextAction,
		Incomplete:            h.Incomplete,
	}
}

func toolVerification(v protocol.VerificationReport) tool.VerificationReport {
	checks := make([]tool.VerificationCheck, 0, len(v.Checks))
	for _, c := range v.Checks {
		checks = append(checks, tool.VerificationCheck{
			Name:       c.Name,
			Kind:       c.Kind,
			Value:      c.Value,
			Passed:     c.Passed,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Error:      c.Error,
			DurationMs: c.DurationMs,
		})
	}
	if checks == nil {
		checks = []tool.VerificationCheck{}
	}
	return tool.VerificationReport{
		Passed:   v.Passed,
		Claimed:  v.Claimed,
		Verified: v.Verified,
		Checks:   checks,
		Env: tool.VerificationEnv{
			WorkDir:    v.Env.WorkDir,
			SessionID:  v.Env.SessionID,
			WorktreeID: v.Env.WorktreeID,
			ModelID:    v.Env.ModelID,
			StartedAt:  v.Env.StartedAt,
			FinishedAt: v.Env.FinishedAt,
		},
		Summary:    v.Summary,
		DurationMs: v.DurationMs,
	}
}

// runChildVerification executes spawn-time gates against the claimed handoff.
// Returns a non-nil report whenever gates were configured.
func (e *Engine) runChildVerification(h *childHandle, child *Engine, handoff protocol.CompletionHandoff) *protocol.VerificationReport {
	if h == nil || len(h.gates) == 0 {
		return nil
	}
	gates := make([]verify.Gate, 0, len(h.gates))
	for _, g := range h.gates {
		gates = append(gates, verify.Gate{
			Kind:        g.Kind,
			Value:       g.Value,
			Description: g.Description,
		})
	}
	// Detached timeout: child context is already canceled at this point.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	workDir := ""
	modelID := ""
	if e != nil {
		workDir = e.opts.WorkDir
	}
	if child != nil {
		if child.opts.WorkDir != "" {
			workDir = child.opts.WorkDir
		}
		modelID = strings.TrimSpace(child.model)
		if modelID == "" {
			modelID = strings.TrimSpace(child.opts.InitialModel)
		}
	}

	// HasStructured: engine set Incomplete=false only when parse succeeded.
	hv := &verify.HandoffView{
		Summary:       handoff.Summary,
		Incomplete:    handoff.Incomplete,
		HasStructured: !handoff.Incomplete,
	}
	// If incomplete but summary is only the default, still not structured.
	if handoff.Incomplete {
		hv.HasStructured = false
	}

	runner := &verify.Runner{WorkDir: workDir}
	res := runner.Run(ctx, gates, verify.Input{
		Claimed: true,
		Handoff: hv,
		Env: verify.EnvMetadata{
			WorkDir:   workDir,
			SessionID: h.id,
			ModelID:   modelID,
		},
	})
	rep := verifyResultToProtocol(res)
	return &rep
}

func verifyResultToProtocol(res verify.Result) protocol.VerificationReport {
	checks := make([]protocol.VerificationCheck, 0, len(res.Checks))
	for _, c := range res.Checks {
		checks = append(checks, protocol.VerificationCheck{
			Name:       c.Name,
			Kind:       c.Kind,
			Value:      c.Value,
			Passed:     c.Passed,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Error:      c.Error,
			DurationMs: c.DurationMs,
		})
	}
	if checks == nil {
		checks = []protocol.VerificationCheck{}
	}
	return protocol.VerificationReport{
		Passed:   res.Passed,
		Claimed:  res.Claimed,
		Verified: res.Verified,
		Checks:   checks,
		Env: protocol.VerificationEnv{
			WorkDir:    res.Env.WorkDir,
			SessionID:  res.Env.SessionID,
			WorktreeID: res.Env.WorktreeID,
			ModelID:    res.Env.ModelID,
			StartedAt:  res.Env.StartedAt,
			FinishedAt: res.Env.FinishedAt,
		},
		Summary:    res.Summary,
		DurationMs: res.DurationMs,
	}
}

func protocolReportToVerifyResult(rep *protocol.VerificationReport) verify.Result {
	if rep == nil {
		return verify.Result{}
	}
	checks := make([]verify.CheckResult, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		checks = append(checks, verify.CheckResult{
			Name:       c.Name,
			Kind:       c.Kind,
			Value:      c.Value,
			Passed:     c.Passed,
			ExitCode:   c.ExitCode,
			Output:     c.Output,
			Error:      c.Error,
			DurationMs: c.DurationMs,
		})
	}
	return verify.Result{
		Passed:   rep.Passed,
		Claimed:  rep.Claimed,
		Verified: rep.Verified,
		Checks:   checks,
		Env: verify.EnvMetadata{
			WorkDir:    rep.Env.WorkDir,
			SessionID:  rep.Env.SessionID,
			WorktreeID: rep.Env.WorktreeID,
			ModelID:    rep.Env.ModelID,
			StartedAt:  rep.Env.StartedAt,
			FinishedAt: rep.Env.FinishedAt,
		},
		Summary:    rep.Summary,
		DurationMs: rep.DurationMs,
	}
}

func appendUniqueString(in []string, s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return in
	}
	for _, x := range in {
		if x == s {
			return in
		}
	}
	return append(in, s)
}

func (e *Engine) childRead(ctx context.Context, req tool.TaskReadRequest) (tool.TaskReadResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TaskReadResult{}, err
	}
	id := strings.TrimSpace(req.SessionID)
	if id == "" {
		return tool.TaskReadResult{}, fmt.Errorf("session_id is required")
	}
	id = e.resolveOwnedChildRef(id)
	limit := tool.ClampTaskReadLimit(req.Limit)
	if req.Last > 0 {
		limit = tool.ClampTaskReadLimit(req.Last)
	}

	e.childMu.Lock()
	h := e.children[id]
	rec := e.childHistory[id]
	e.childMu.Unlock()

	var all []tool.TaskTranscriptEntry
	switch {
	case h != nil:
		h.mu.Lock()
		all = append([]tool.TaskTranscriptEntry(nil), h.events...)
		h.mu.Unlock()
	case rec != nil:
		all = append([]tool.TaskTranscriptEntry(nil), rec.events...)
	default:
		return tool.TaskReadResult{}, fmt.Errorf("unknown or inaccessible child session %q", id)
	}

	filtered := make([]tool.TaskTranscriptEntry, 0, len(all))
	for _, ent := range all {
		if !req.IncludeTools && (ent.Kind == "tool.begin" || ent.Kind == "tool.end") {
			continue
		}
		if !req.IncludeReasoning && ent.Kind == "reasoning" {
			continue
		}
		// Text deltas are high-volume; only include when tools are included
		// (treat as content) — always keep user/turn/child/error rows.
		if ent.Kind == "text" && !req.IncludeReasoning && !req.IncludeTools {
			// keep text as content by default
		}
		filtered = append(filtered, ent)
	}
	total := len(filtered)

	offset := req.Offset
	if req.Last > 0 {
		if total > limit {
			offset = total - limit
		} else {
			offset = 0
		}
	}
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	slice := filtered[offset:end]
	next := end
	if end >= total {
		next = -1
	}
	return tool.TaskReadResult{
		SessionID:  id,
		Entries:    slice,
		Offset:     offset,
		Limit:      limit,
		Total:      total,
		Truncated:  total > len(slice) || (req.Last == 0 && end < total),
		NextOffset: next,
	}, nil
}

func (e *Engine) childMessage(ctx context.Context, req tool.TaskMessageRequest) (tool.TaskMessageResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TaskMessageResult{}, err
	}
	id := strings.TrimSpace(req.SessionID)
	text := strings.TrimSpace(req.Text)
	if id == "" {
		return tool.TaskMessageResult{}, fmt.Errorf("session_id is required")
	}
	if text == "" {
		return tool.TaskMessageResult{}, fmt.Errorf("text is required")
	}
	id = e.resolveOwnedChildRef(id)

	e.childMu.Lock()
	h := e.children[id]
	rec := e.childHistory[id]
	e.childMu.Unlock()

	if rec != nil && h == nil {
		st := string(rec.status)
		if st == "" {
			st = string(protocol.ChildStatusCompleted)
		}
		return tool.TaskMessageResult{
			SessionID: id,
			Status:    "rejected",
			State:     st,
			Detail:    "child session is closed (" + st + ")",
		}, nil
	}
	if h == nil {
		return tool.TaskMessageResult{}, fmt.Errorf("unknown or inaccessible child session %q", id)
	}

	// Snapshot activity state before send.
	live := h.statusSnapshot(false)
	queued := h.eng != nil && h.eng.turnActive()

	select {
	case <-ctx.Done():
		return tool.TaskMessageResult{}, ctx.Err()
	case h.ops <- protocol.UserInput{Text: text}:
	case <-time.After(2 * time.Second):
		return tool.TaskMessageResult{
			SessionID: id,
			Status:    "rejected",
			State:     live.State,
			Detail:    "child ops channel blocked",
		}, nil
	}

	// Steer updates live objective for roster/status observability (#774).
	h.mu.Lock()
	if h.budget != nil {
		h.budget.setObjective(text)
		h.budget.noteProgress(time.Now(), "steer")
	}
	h.mu.Unlock()

	status := "accepted"
	detail := "delivered to child"
	if queued {
		status = "queued"
		detail = "queued until active child turn finishes"
	}
	// Re-read after send (still running).
	live = h.statusSnapshot(false)
	return tool.TaskMessageResult{
		SessionID: id,
		Status:    status,
		State:     live.State,
		Detail:    detail,
	}, nil
}

func (e *Engine) childInterrupt(ctx context.Context, req tool.TaskInterruptRequest) (tool.TaskInterruptResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TaskInterruptResult{}, err
	}
	id := strings.TrimSpace(req.SessionID)
	if id == "" {
		return tool.TaskInterruptResult{}, fmt.Errorf("session_id is required")
	}
	// Cancel a queued delegation that never spawned.
	if e.team != nil {
		if d, ok := e.team.GetDelegation(id); ok && d.SessionID == "" {
			actor := strings.TrimSpace(e.opts.SessionID)
			prev := d.State
			item, err := e.team.TransitionDelegation(d.ID, actor, protocol.DelegationCanceled, "interrupted", 0)
			if err != nil {
				return tool.TaskInterruptResult{}, err
			}
			e.emitDelegationChanged(item, prev, "interrupted")
			return tool.TaskInterruptResult{
				SessionID: d.ID,
				State:     string(protocol.DelegationCanceled),
				Detail:    "queued delegation canceled",
			}, nil
		}
		if d, ok := e.team.GetDelegation(id); ok && d.SessionID != "" {
			id = d.SessionID
		}
	}
	id = e.resolveOwnedChildRef(id)

	e.childMu.Lock()
	h := e.children[id]
	rec := e.childHistory[id]
	e.childMu.Unlock()

	if rec != nil && h == nil {
		st := string(rec.status)
		if st == "" {
			st = string(protocol.ChildStatusCompleted)
		}
		return tool.TaskInterruptResult{
			SessionID: id,
			State:     st,
			Detail:    "child already finished",
		}, nil
	}
	if h == nil {
		return tool.TaskInterruptResult{}, fmt.Errorf("unknown or inaccessible child session %q", id)
	}

	h.cancel()
	select {
	case h.ops <- protocol.Interrupt{}:
	default:
	}

	// Wait briefly for terminal record so callers see canceled without racing.
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return tool.TaskInterruptResult{}, ctx.Err()
		case <-h.done:
			e.childMu.Lock()
			rec = e.childHistory[id]
			e.childMu.Unlock()
			if rec != nil {
				st := string(rec.status)
				if st == "" {
					st = string(protocol.ChildStatusCanceled)
				}
				return tool.TaskInterruptResult{
					SessionID: id,
					State:     st,
					Detail:    "child interrupted",
				}, nil
			}
			return tool.TaskInterruptResult{
				SessionID: id,
				State:     string(protocol.ChildStatusCanceled),
				Detail:    "child interrupted",
			}, nil
		case <-timer.C:
			live := h.statusSnapshot(false)
			return tool.TaskInterruptResult{
				SessionID: id,
				State:     live.State,
				Detail:    "interrupt sent; child still shutting down",
			}, nil
		}
	}
}

func formatElapsed(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		m := int(d.Minutes())
		s := int(d.Seconds()) % 60
		return fmt.Sprintf("%dm%ds", m, s)
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%dm", h, m)
}

// queueChildCompleted records a durable model-facing notice for a finished
// child and wakes any in-flight sleep. Consumed mid-turn by
// injectPendingChildNotices or as an idle auto-nudge via flushPendingChildNotices.
func (e *Engine) queueChildCompleted(c protocol.ChildCompleted) {
	e.noticeMu.Lock()
	defer e.noticeMu.Unlock()
	e.pendingChildNotices = append(e.pendingChildNotices, formatChildCompletedNotice(c))
	e.signalChildWakeLocked()
}

// hasPendingChildNotices reports whether any child.completed texts are queued.
func (e *Engine) hasPendingChildNotices() bool {
	e.noticeMu.Lock()
	defer e.noticeMu.Unlock()
	return len(e.pendingChildNotices) > 0
}

// takePendingChildNotices removes and returns all queued notices (may be nil).
func (e *Engine) takePendingChildNotices() []string {
	e.noticeMu.Lock()
	defer e.noticeMu.Unlock()
	if len(e.pendingChildNotices) == 0 {
		return nil
	}
	out := e.pendingChildNotices
	e.pendingChildNotices = nil
	return out
}

// childWakeCh returns the channel closed on the next child completion.
func (e *Engine) childWakeCh() <-chan struct{} {
	e.noticeMu.Lock()
	defer e.noticeMu.Unlock()
	if e.childWake == nil {
		e.childWake = make(chan struct{})
	}
	return e.childWake
}

// signalChildWakeLocked closes the current wake channel and installs a fresh
// one. Caller must hold noticeMu.
func (e *Engine) signalChildWakeLocked() {
	if e.childWake == nil {
		e.childWake = make(chan struct{})
	}
	select {
	case <-e.childWake:
		// already closed
	default:
		close(e.childWake)
	}
	e.childWake = make(chan struct{})
}

// injectPendingChildNotices appends queued child.completed texts into the
// live model history so the next Stream sees them without waiting for the
// parent turn to end. Safe to call only from the turn worker (sole writer of
// e.messages during a turn). Emits UserMessage for session restore.
func (e *Engine) injectPendingChildNotices() {
	notices := e.takePendingChildNotices()
	if len(notices) == 0 {
		return
	}
	text := strings.Join(notices, "\n\n")
	e.emit(protocol.UserMessage{Correlation: e.sessionCorr(), Text: text})
	e.messages = append(e.messages, provider.Message{
		Role: provider.RoleUser,
		Text: text,
	})
}

// flushPendingChildNotices starts a short parent turn with queued child
// completion summaries when the parent is idle and a provider is selected.
// Notices stay queued while a turn is active or no model is available so the
// next successful flush (or user turn after provider select) can deliver them.
// Mid-turn injectPendingChildNotices may already have drained the queue.
func (e *Engine) flushPendingChildNotices(ctx context.Context) {
	if !e.hasPendingChildNotices() {
		return
	}
	e.joinFinishingTurn()
	if e.turnActive() {
		return
	}
	if e.prov == nil || ctx.Err() != nil {
		return
	}
	notices := e.takePendingChildNotices()
	if len(notices) == 0 {
		return
	}
	e.startTurn(ctx, strings.Join(notices, "\n\n"), nil)
}

func formatChildCompletedNotice(c protocol.ChildCompleted) string {
	status := string(c.Status)
	if status == "" {
		status = string(protocol.ChildStatusCompleted)
	}
	id := strings.TrimSpace(c.SessionID)
	short := id
	if len(short) > 8 {
		short = short[:8]
	}
	name := strings.TrimSpace(c.Name)
	var b strings.Builder
	switch {
	case short != "" && name != "":
		fmt.Fprintf(&b, "[child.completed session=%s name=%s status=%s]", short, name, status)
	case short != "":
		fmt.Fprintf(&b, "[child.completed session=%s status=%s]", short, status)
	case name != "":
		fmt.Fprintf(&b, "[child.completed name=%s status=%s]", name, status)
	default:
		fmt.Fprintf(&b, "[child.completed status=%s]", status)
	}
	// Prefer structured handoff JSON for the lead; fall back to free-form summary.
	handoff := c.Handoff
	if strings.TrimSpace(handoff.Summary) == "" && strings.TrimSpace(c.Summary) != "" {
		handoff.Summary = c.Summary
	}
	if handoff.FilesChanged == nil {
		handoff.FilesChanged = []string{}
	}
	if handoff.Findings == nil {
		handoff.Findings = []string{}
	}
	if handoff.Blockers == nil {
		handoff.Blockers = []string{}
	}
	// Zero-value handoff (legacy/tests): still emit a minimal object from Summary.
	if strings.TrimSpace(handoff.Summary) == "" && strings.TrimSpace(c.Summary) == "" {
		handoff.Summary = defaultHandoffSummary(c.Status)
		handoff.Incomplete = true
	}
	b.WriteByte('\n')
	b.WriteString("handoff: ")
	b.WriteString(marshalHandoffModelJSON(handoff))
	if handoff.Incomplete {
		b.WriteString("\n(note: handoff.incomplete=true — child did not supply structured fields; engine filled defaults + tracked files)")
	}
	if c.Verification != nil {
		b.WriteByte('\n')
		b.WriteString("verification: ")
		b.WriteString(marshalVerificationModelJSON(*c.Verification))
		if !c.Verification.Passed {
			b.WriteString("\n(note: independent verification gates failed — status is blocked, not harness-verified done. Address gate output and re-delegate or fix.)")
		}
	}
	b.WriteString("\nDo not sleep-poll for subagents; this is the terminal result. Prefer the handoff JSON over free-form prose.")
	return b.String()
}

func (e *Engine) persistChildEvent(id string, ev protocol.Event) {
	if e.opts.AppendChildEvent == nil {
		return
	}
	_ = e.opts.AppendChildEvent(id, ev)
}

func (e *Engine) closeChildSession(id string) {
	if e.opts.CloseChildSession == nil {
		return
	}
	_ = e.opts.CloseChildSession(id)
}

// routePermissionReply delivers a reply to the parent service and every
// active child. Request IDs are session-scoped so only one service matches.
func (e *Engine) routePermissionReply(op protocol.PermissionReply) {
	replies := e.childPermReplies()
	e.perms.Reply(op)
	for _, reply := range replies {
		reply(op)
	}
}

// routeQuestionReply delivers a reply to the parent service and every
// active child. Request IDs are session-scoped so only one service matches.
func (e *Engine) routeQuestionReply(op protocol.QuestionReply) {
	replies := e.childQuestionReplies()
	e.questions.Reply(op)
	for _, reply := range replies {
		reply(op)
	}
}

func (e *Engine) childPermReplies() []func(protocol.PermissionReply) {
	e.childMu.Lock()
	defer e.childMu.Unlock()
	out := make([]func(protocol.PermissionReply), 0, len(e.children))
	for _, h := range e.children {
		out = append(out, h.permReply)
	}
	return out
}

func (e *Engine) childQuestionReplies() []func(protocol.QuestionReply) {
	e.childMu.Lock()
	defer e.childMu.Unlock()
	out := make([]func(protocol.QuestionReply), 0, len(e.children))
	for _, h := range e.children {
		out = append(out, h.qReply)
	}
	return out
}

// snapshotChildren returns active child handles for shutdown.
func (e *Engine) snapshotChildren() []*childHandle {
	e.childMu.Lock()
	defer e.childMu.Unlock()
	out := make([]*childHandle, 0, len(e.children))
	for _, h := range e.children {
		out = append(out, h)
	}
	return out
}

// shutdownChildren cancels every active child and waits for drain completion.
// Called from Run exit so Events stays open until children finish emitting.
func (e *Engine) shutdownChildren() {
	handles := e.snapshotChildren()
	for _, h := range handles {
		h.cancel()
		select {
		case h.ops <- protocol.Interrupt{}:
		default:
		}
	}
	for _, h := range handles {
		<-h.done
	}
}

func lastAssistantText(messages []provider.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == provider.RoleAssistant {
			if text := strings.TrimSpace(messages[i].Text); text != "" {
				return text
			}
		}
	}
	return ""
}

// briefAgentSessionTitle is the default child/subagent display name:
// "{agent} {shortId}" (or just one part when the other is empty).
func briefAgentSessionTitle(agent, id string) string {
	agent = strings.TrimSpace(agent)
	short := shortSessionID(id)
	switch {
	case agent != "" && short != "":
		return agent + " " + short
	case agent != "":
		return agent
	case short != "":
		return short
	default:
		return "task"
	}
}

// shortSessionID returns a compact id fragment for default session labels.
func shortSessionID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if i := strings.LastIndexAny(id, "/-_"); i >= 0 && i+1 < len(id) {
		tail := id[i+1:]
		if len([]rune(tail)) >= 6 {
			id = tail
		}
	}
	r := []rune(id)
	const max = 8
	if len(r) <= max {
		return id
	}
	return string(r[:max])
}

// taskModelPin is a resolved optional model override for a child spawn.
type taskModelPin struct {
	lock     bool
	provider string
	model    string
	prov     provider.Provider // non-nil when Select was required for a foreign provider
}

// taskEffortPin is a resolved optional effort override for a child spawn.
type taskEffortPin struct {
	lock  bool
	level protocol.Effort
}

// resolveTaskEffortPin parses an optional task effort pin. Empty means inherit
// the parent dial (agent pins may still apply). A set level locks the dial so
// the child agent profile cannot override it.
func resolveTaskEffortPin(pin string) (taskEffortPin, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return taskEffortPin{}, nil
	}
	level, ok := protocol.ParseEffort(pin)
	if !ok || level == protocol.EffortDefault {
		return taskEffortPin{}, fmt.Errorf("unknown effort %q (want %s)", pin, effortNames())
	}
	return taskEffortPin{lock: true, level: level}, nil
}

// resolveTaskModelPin parses an optional task model pin (bare id or
// "provider/model"), validates it against the shared catalog when available,
// and Select-s a foreign provider when needed. Empty pin means inherit.
func (e *Engine) resolveTaskModelPin(ctx context.Context, pin string) (taskModelPin, error) {
	pin = strings.TrimSpace(pin)
	if pin == "" {
		return taskModelPin{}, nil
	}
	var providerName, model string
	if prov, bare, ok := splitProviderModel(pin); ok {
		providerName, model = prov, bare
	} else {
		if strings.TrimSpace(e.provName) == "" {
			return taskModelPin{}, fmt.Errorf("no provider selected; cannot resolve model %q", pin)
		}
		providerName, model = e.provName, pin
	}
	providerName = config.CanonicalProviderID(providerName)
	model = stripMatchingProviderPrefixes(providerName, model)
	if model == "" {
		return taskModelPin{}, fmt.Errorf("model is empty")
	}
	if err := e.validateCatalogModel(ctx, providerName, model); err != nil {
		return taskModelPin{}, err
	}
	out := taskModelPin{lock: true, provider: providerName, model: model}
	if providerName == e.provName && e.prov != nil {
		return out, nil
	}
	if e.opts.Select == nil {
		return taskModelPin{}, fmt.Errorf("cannot select provider %q for task model", providerName)
	}
	p, defaultModel, err := e.opts.Select(providerName)
	if err != nil {
		return taskModelPin{}, fmt.Errorf("task model provider %q: %w", providerName, err)
	}
	if p == nil {
		return taskModelPin{}, fmt.Errorf("task model provider %q: no provider", providerName)
	}
	if out.model == "" {
		out.model = defaultModel
	}
	out.prov = p
	return out, nil
}

// validateCatalogModel checks model against ListModels (same catalog as the
// UI /model picker). When the catalog is unavailable or empty, freeform ids
// are allowed — matching the picker fallback. When the catalog returns ids,
// unknown models are rejected.
func (e *Engine) validateCatalogModel(ctx context.Context, providerName, model string) error {
	if e.opts.ListModels == nil {
		return nil
	}
	ids, err := e.opts.ListModels(ctx, providerName)
	if err != nil || len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if id == model {
			return nil
		}
	}
	return fmt.Errorf("unknown model %q for provider %q", model, providerName)
}

// applyChildProviderModel sets the child's live provider/model from either a
// resolved task pin or parent inheritance.
func (e *Engine) applyChildProviderModel(child *Engine, pin taskModelPin) {
	if pin.lock {
		if pin.prov != nil {
			child.prov = pin.prov
			child.provName = config.CanonicalProviderID(pin.provider)
			child.model = pin.model
		} else {
			child.prov = e.prov
			child.provName = e.provName
			child.model = pin.model
		}
		child.priority = e.priority
		child.opts.InitialProvider = ""
		child.opts.InitialModel = ""
		return
	}
	if e.prov != nil {
		child.prov = e.prov
		child.provName = e.provName
		child.model = e.model
		child.priority = e.priority
		child.opts.InitialProvider = ""
		child.opts.InitialModel = ""
	}
}
