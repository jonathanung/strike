package engine

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// ChildSessionSnapshot is a persisted child log loaded for resume (#1035).
type ChildSessionSnapshot struct {
	SessionID       string
	ParentSessionID string
	LeadSessionID   string
	Title           string
	Events          []protocol.Event
}

// resumeChild reopens a persisted owned child as the same delegated task.
// Ownership and lineage are validated; model history and selections restore
// via RestoreSession. Completed tool boundaries stay settled (no re-execution).
// Terminal children refuse unless Continue is set. ChildCompleted is emitted
// only when the resumed runtime finishes a new turn — never for prior results.
func (e *Engine) resumeChild(ctx context.Context, req tool.TaskResumeRequest) (tool.TaskResult, error) {
	if err := ctx.Err(); err != nil {
		return tool.TaskResult{}, err
	}
	ref := strings.TrimSpace(req.ID)
	if ref == "" {
		return tool.TaskResult{}, fmt.Errorf("resume: id is required")
	}
	if e.opts.LoadChildSession == nil {
		return tool.TaskResult{}, fmt.Errorf("resume: child session persistence is not available")
	}

	childID, memberName, delegID, live := e.resolveResumeIdentity(ref)
	if live {
		return tool.TaskResult{}, fmt.Errorf("resume: child %q is already running", childID)
	}
	if childID == "" {
		childID = ref
	}

	snap, err := e.opts.LoadChildSession(childID)
	if err != nil {
		return tool.TaskResult{}, fmt.Errorf("resume: load child session: %w", err)
	}
	if strings.TrimSpace(snap.SessionID) != "" {
		childID = strings.TrimSpace(snap.SessionID)
	}
	if err := e.validateChildOwnership(snap); err != nil {
		return tool.TaskResult{}, err
	}

	priorTerminal, priorStatus, _ := childLogTerminal(snap.Events)
	if priorTerminal && !req.Continue {
		return tool.TaskResult{}, fmt.Errorf(
			"resume: child %q is terminal (%s); pass continue=true for an explicit continuation",
			childID, priorStatus,
		)
	}

	restored := RestoreSession(snap.Events, childID)
	agentName, prompt, nameFromLog, bundle := childSpawnMeta(snap.Events)
	if memberName == "" {
		memberName = nameFromLog
	}
	if strings.TrimSpace(prompt) == "" {
		prompt = strings.TrimSpace(req.Prompt)
	}
	if agentName == "" {
		agentName = e.agent.Name
	}
	if agentName == "" {
		agentName = "build"
	}
	childAgent, ok := e.findAgent(agentName)
	if !ok {
		childAgent = e.agent
		agentName = e.agent.Name
	}

	maxDepth := e.opts.MaxChildDepth
	if maxDepth == 0 {
		maxDepth = 1
	}
	if e.opts.Depth >= maxDepth {
		return tool.TaskResult{}, fmt.Errorf("task depth limit reached")
	}
	childDepth := e.opts.Depth + 1

	if e.opts.ReopenChildSession != nil {
		if err := e.opts.ReopenChildSession(childID); err != nil {
			return tool.TaskResult{}, fmt.Errorf("resume: reopen child session: %w", err)
		}
	}

	var childReg *tool.Registry
	if e.opts.Registry == nil {
		childReg = tool.NewRegistry()
	} else if childDepth >= maxDepth {
		childReg = e.opts.Registry.CloneWithout(leafTaskTools...)
	} else {
		childReg = e.opts.Registry.CloneWithout()
	}

	parentLayers := append([]permission.Ruleset(nil), e.opts.Rules...)
	if len(e.agent.Permissions) > 0 {
		parentLayers = append(parentLayers, append(permission.Ruleset(nil), e.agent.Permissions...))
	}
	if phaseRules := e.perms.PhaseRules(); len(phaseRules) > 0 {
		parentLayers = append(parentLayers, phaseRules)
	}
	if scope := bundlePathScopeRules(bundle.AllowedPaths); len(scope) > 0 {
		parentLayers = append(parentLayers, scope)
	}
	childPermMode := e.childInitialPermissionMode(restored.PermissionMode)

	initialProvider := restored.Provider
	if initialProvider == "" {
		initialProvider = e.provName
	}
	initialModel := restored.Model
	if initialModel == "" {
		initialModel = e.model
	}
	initialEffort := restored.Effort
	if initialEffort == protocol.EffortDefault {
		initialEffort = e.effort
	}

	child := New(Options{
		SessionID:                  childID,
		ParentSessionID:            e.opts.SessionID,
		RootSessionID:              e.rootSessionID(),
		Depth:                      childDepth,
		MaxChildDepth:              maxDepth,
		TaskOneShot:                true,
		ContextBundle:              bundle,
		Team:                       e.team,
		Select:                     e.opts.Select,
		Registry:                   childReg,
		WorkDir:                    e.opts.WorkDir,
		ProjectRoot:                e.opts.ProjectRoot,
		Instructions:               e.opts.Instructions,
		Memory:                     e.opts.Memory,
		Ledger:                     e.opts.Ledger,
		Attachments:                e.opts.Attachments,
		SystemPrompt:               e.opts.SystemPrompt,
		LeanCode:                   e.opts.LeanCode,
		HarnessRegistry:            e.opts.HarnessRegistry,
		Scheduler:                  e.opts.Scheduler,
		SchedulerPolicy:            e.opts.SchedulerPolicy,
		FileSync:                   e.opts.FileSync,
		CollectDiagnostics:         e.opts.CollectDiagnostics,
		TUISnapshot:                e.opts.TUISnapshot,
		Agents:                     e.opts.Agents,
		InitialAgent:               agentName,
		InitialProvider:            initialProvider,
		InitialModel:               initialModel,
		InitialEffort:              initialEffort,
		InitialMessages:            restored.Messages,
		InitialPriority:            restored.Priority,
		InitialTitled:              restored.Titled || snap.Title != "",
		InitialAutonomy:            restored.Autonomy,
		InitialPermissionMode:      childPermMode,
		InitialAlwaysGrants:        restored.AlwaysGrants,
		QuietStartup:               true,
		SandboxMode:                e.opts.SandboxMode,
		NetworkAllow:               e.opts.NetworkAllow,
		WebSearch:                  e.opts.WebSearch,
		AllowYoloWithoutSandbox:    e.opts.AllowYoloWithoutSandbox,
		MaxTokens:                  e.opts.MaxTokens,
		MaxStreamAttempts:          e.opts.MaxStreamAttempts,
		StreamRetryBackoff:         e.opts.StreamRetryBackoff,
		MaxToolRetryAttempts:       e.opts.MaxToolRetryAttempts,
		ToolRetryBackoff:           e.opts.ToolRetryBackoff,
		ToolLoopThreshold:          e.opts.ToolLoopThreshold,
		ContextWindow:              e.contextWindow(),
		LookupContextWindow:        e.opts.LookupContextWindow,
		ListModels:                 e.opts.ListModels,
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
		ManagedRules:               append(permission.Ruleset(nil), e.opts.ManagedRules...),
		LockPermissionMode:         e.opts.LockPermissionMode,
		Hooks:                      e.opts.Hooks,
		HookRules:                  e.opts.HookRules,
		PersistProjectRule:         e.opts.PersistProjectRule,
		DangerouslySkipPermissions: e.opts.DangerouslySkipPermissions,
		DefaultChildBudget:         e.opts.DefaultChildBudget,
		DelegationPolicy:           e.opts.DelegationPolicy,
		SessionBudgetExhausted:     e.opts.SessionBudgetExhausted,
		SessionBudget:              e.opts.SessionBudget,
		EstimateUsageCost:          e.opts.EstimateUsageCost,
		MaxSessionCostUSD:          e.opts.MaxSessionCostUSD,
	})

	// Inherit parent live provider when restore left the child without one.
	e.applyChildProviderModel(child, taskModelPin{})

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
	budgetLimits := MergeAgentBudget(e.opts.DefaultChildBudget, tool.AgentBudgetLimits{})
	if delegID != "" && e.team != nil {
		if d, ok := e.team.GetDelegation(delegID); ok && d.Budget != (tool.AgentBudgetLimits{}) {
			budgetLimits = NormalizeAgentBudget(d.Budget)
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
		prompt:    prompt,
		name:      memberName,
		parent:    e,
		budget:    newChildBudget(budgetLimits, prompt, startedAt),
	}
	for _, ev := range snap.Events {
		h.noteEvent(ev)
	}

	e.childMu.Lock()
	if e.children == nil {
		e.children = make(map[string]*childHandle)
	}
	if e.childHistory != nil {
		delete(e.childHistory, childID)
	}
	e.children[childID] = h
	e.childMu.Unlock()

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
			e.childMu.Lock()
			delete(e.children, childID)
			e.childMu.Unlock()
			cancel()
			e.closeChildSession(childID)
			if memberName != "" {
				return tool.TaskResult{}, fmt.Errorf("name %q is already used on this team", memberName)
			}
			return tool.TaskResult{}, fmt.Errorf("failed to enroll resumed child on team")
		}
	}

	if e.team != nil {
		if delegID == "" {
			if d, ok := e.team.GetDelegation(childID); ok {
				delegID = d.ID
			}
		}
		if delegID != "" {
			if d, ok := e.team.GetDelegation(delegID); ok {
				prev := d.State
				if d.SessionID == "" || d.SessionID == childID {
					if linked, linkErr := e.team.LinkDelegationSession(delegID, childID); linkErr == nil {
						d = linked
					}
				}
				if d.State != protocol.DelegationWorking {
					actor := strings.TrimSpace(e.opts.SessionID)
					if moved, mErr := e.team.TransitionDelegation(delegID, actor, protocol.DelegationWorking, "resumed", 0); mErr == nil {
						d = moved
						e.emitDelegationChanged(d, prev, "resumed")
					} else if moved, ok := e.team.SetDelegationWorking(childID); ok {
						d = moved
						e.emitDelegationChanged(d, prev, "resumed")
					}
				}
				delegID = d.ID
			}
		}
	}

	startedEv := protocol.ChildStarted{
		Correlation:   childCorr,
		Agent:         agentName,
		Prompt:        prompt,
		Name:          memberName,
		Provider:      child.provName,
		Model:         child.model,
		PolicyReason:  "resumed",
		ContextBundle: protocolContextBundle(bundle),
	}
	e.emit(startedEv)
	e.persistChildEvent(childID, startedEv)
	h.noteEvent(startedEv)
	e.emitTeamRoster()

	stopCh := make(chan string, 1)
	var (
		failMu  sync.Mutex
		failMsg string
	)
	go func() {
		defer close(h.done)
		defer e.closeChildSession(childID)
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
				e.emit(ev)
			case protocol.ChildCompleted:
				e.emit(ev)
			case protocol.AgentMessage:
				e.emit(ev)
			case protocol.AgentContractTimeout:
				e.emit(ev)
			case protocol.TeamRoster:
				e.emit(ev)
			case protocol.PathOverlap:
				e.emit(ev)
			case protocol.SchedulerQueued, protocol.SchedulerAdmitted, protocol.SchedulerCanceled:
				e.emit(ev)
			case protocol.TurnCompleted:
				select {
				case stopCh <- ev.StopReason:
				default:
				}
			case protocol.EngineError:
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
		cancel()

		stopReason := ""
		gotStop := false
		select {
		case stopReason = <-stopCh:
			gotStop = true
		default:
		}

		assistantText := lastAssistantText(child.messages)
		var status protocol.ChildStatus
		var errText string
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
		if !gotStop && strings.TrimSpace(assistantText) == "" && priorTerminal && req.Continue && strings.TrimSpace(req.Prompt) == "" {
			status = protocol.ChildStatusCanceled
			errText = "resumed terminal child exited without a new turn"
		}

		trackedFiles := child.mutatedPathsSnapshot()
		handoff, handoffParsed := buildCompletionHandoffParsed(status, assistantText, trackedFiles)
		h.mu.Lock()
		trackedArts := append([]protocol.ArtifactRef(nil), h.artifacts...)
		h.mu.Unlock()
		mergeArtifactRefsIntoHandoff(&handoff, trackedArts)
		if errText != "" {
			if strings.TrimSpace(handoff.Summary) == "" || handoff.Summary == defaultHandoffSummary(status) {
				handoff.Summary = errText
			}
		}
		if strings.TrimSpace(handoff.Summary) == "" {
			handoff.Summary = defaultHandoffSummary(status)
		}
		status = applyMissingContextStatus(status, &handoff)
		budgetKind, finalization := h.budgetTerminalMeta()
		if budgetKind != "" && finalization != protocol.FinalizationSkippedHard {
			if handoffParsed {
				finalization = protocol.FinalizationSucceeded
			} else if finalization != protocol.FinalizationNone {
				finalization = protocol.FinalizationFailed
			}
		}
		applyHandoffQuality(&handoff, handoffParsed)

		completed := protocol.ChildCompleted{
			Correlation:  childCorr,
			Status:       status,
			Summary:      handoff.Summary,
			Name:         memberName,
			Handoff:      handoff,
			DelegationID: delegID,
			BudgetKind:   budgetKind,
			Finalization: finalization,
		}
		e.emit(completed)
		e.persistChildEvent(childID, completed)
		h.noteEvent(completed)
		e.finishChild(h, completed)
		e.onChildDelegationTerminal(childID, status)
		e.notifyWaitersFromCompleted(completed)
		select {
		case e.childDone <- completed:
		default:
		}
	}()

	go child.Run(childCtx)
	e.startChildBudgetWatch(h)

	cont := strings.TrimSpace(req.Prompt)
	if cont != "" {
		select {
		case <-ctx.Done():
			cancel()
			select {
			case child.Ops() <- protocol.Interrupt{}:
			default:
			}
			return tool.TaskResult{}, ctx.Err()
		case child.Ops() <- protocol.UserInput{Text: cont}:
		}
	}

	out := fmt.Sprintf(
		"Resumed child session %s (agent %s). It runs independently and does not block this turn. A [child.completed] message will deliver a new terminal summary when this resume finishes — prior results are not re-delivered.",
		childID, agentName,
	)
	if memberName != "" {
		out = fmt.Sprintf(
			"Resumed child session %s (name %s, agent %s). It runs independently and does not block this turn. A [child.completed] message will deliver a new terminal summary when this resume finishes — prior results are not re-delivered. Address this teammate by name %q or session id.",
			childID, memberName, agentName, memberName,
		)
	}
	if delegID != "" {
		out += fmt.Sprintf(" Delegation %s.", delegID)
	}
	if priorTerminal && req.Continue {
		out += " Explicit continuation of a previously terminal child."
	}
	lifecycle := string(protocol.DelegationWorking)
	if delegID != "" && e.team != nil {
		if d, ok := e.team.GetDelegation(delegID); ok {
			lifecycle = string(d.State)
		}
	}
	return tool.TaskResult{
		Output:       out,
		Status:       "started",
		SessionID:    childID,
		Name:         memberName,
		DelegationID: delegID,
		Lifecycle:    lifecycle,
		PolicyReason: "resumed",
	}, nil
}

func (e *Engine) resolveResumeIdentity(ref string) (childID, name, delegID string, live bool) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", "", false
	}
	e.childMu.Lock()
	for id, h := range e.children {
		if h == nil {
			continue
		}
		if id == ref || h.name == ref {
			cid, n := id, h.name
			e.childMu.Unlock()
			deleg := ""
			if e.team != nil {
				if d, ok := e.team.GetDelegation(cid); ok {
					deleg = d.ID
				}
			}
			return cid, n, deleg, true
		}
	}
	for id, rec := range e.childHistory {
		if rec == nil {
			continue
		}
		if id == ref || rec.name == ref {
			e.childMu.Unlock()
			deleg := ""
			if e.team != nil {
				if d, ok := e.team.GetDelegation(id); ok {
					deleg = d.ID
				}
			}
			return id, rec.name, deleg, false
		}
	}
	e.childMu.Unlock()

	if e.team != nil {
		if d, ok := e.team.GetDelegation(ref); ok {
			return d.SessionID, d.Name, d.ID, false
		}
		if owner, ok := e.team.NameOwner(ref); ok {
			live = false
			e.childMu.Lock()
			_, live = e.children[owner]
			e.childMu.Unlock()
			deleg := ""
			name := ref
			if d, ok := e.team.GetDelegation(owner); ok {
				deleg = d.ID
				if d.Name != "" {
					name = d.Name
				}
			}
			return owner, name, deleg, live
		}
	}
	return "", "", "", false
}

func (e *Engine) validateChildOwnership(snap ChildSessionSnapshot) error {
	parent := strings.TrimSpace(snap.ParentSessionID)
	if parent == "" {
		return fmt.Errorf("resume: session %q is not a child session", snap.SessionID)
	}
	self := strings.TrimSpace(e.opts.SessionID)
	root := strings.TrimSpace(e.rootSessionID())
	lead := strings.TrimSpace(snap.LeadSessionID)
	if parent == self {
		return nil
	}
	// Root/lead may resume any descendant in the lineage.
	if self != "" && self == root {
		if lead == "" || lead == root || lead == self {
			return nil
		}
		if parent == root {
			return nil
		}
	}
	if lead != "" && lead == self {
		return nil
	}
	return fmt.Errorf("resume: child %q is not owned by this session (parent %s)", snap.SessionID, parent)
}

func childLogTerminal(events []protocol.Event) (terminal bool, status protocol.ChildStatus, summary string) {
	for i := len(events) - 1; i >= 0; i-- {
		if c, ok := events[i].(protocol.ChildCompleted); ok {
			return true, c.Status, c.Summary
		}
	}
	return false, "", ""
}

func childSpawnMeta(events []protocol.Event) (agent, prompt, name string, bundle tool.ContextBundle) {
	for _, ev := range events {
		if c, ok := ev.(protocol.ChildStarted); ok {
			agent = c.Agent
			prompt = c.Prompt
			name = c.Name
			if c.ContextBundle != nil {
				bundle = toolContextBundle(*c.ContextBundle)
			}
			return agent, prompt, name, bundle
		}
	}
	for _, ev := range events {
		if a, ok := ev.(protocol.AgentSelected); ok && strings.TrimSpace(a.Name) != "" {
			agent = a.Name
		}
	}
	return agent, prompt, name, bundle
}

// toolContextBundle maps protocol bundle → tool bundle.
func toolContextBundle(b protocol.ContextBundle) tool.ContextBundle {
	out := tool.ContextBundle{
		Goal:          b.Goal,
		Acceptance:    append([]string(nil), b.Acceptance...),
		AllowedPaths:  append([]string(nil), b.AllowedPaths...),
		RequiredPaths: append([]string(nil), b.RequiredPaths...),
		Constraints:   append([]string(nil), b.Constraints...),
	}
	if len(b.Artifacts) > 0 {
		out.Artifacts = make([]tool.BundleArtifactRef, len(b.Artifacts))
		for i, a := range b.Artifacts {
			out.Artifacts[i] = tool.BundleArtifactRef{ID: a.ID, Version: a.Version, Type: a.Type}
		}
	}
	if len(b.Items) > 0 {
		out.Items = make([]tool.ContextBundleItem, len(b.Items))
		for i, it := range b.Items {
			out.Items[i] = tool.ContextBundleItem{
				ID: it.ID, Kind: it.Kind, Title: it.Title, Text: it.Text, Path: it.Path, Hash: it.Hash,
			}
			if it.Artifact != nil {
				ref := tool.BundleArtifactRef{ID: it.Artifact.ID, Version: it.Artifact.Version, Type: it.Artifact.Type}
				out.Items[i].Artifact = &ref
			}
		}
	}
	if len(b.FilePins) > 0 {
		out.FilePins = make([]tool.ContextFilePin, len(b.FilePins))
		for i, p := range b.FilePins {
			out.FilePins[i] = tool.ContextFilePin{Path: p.Path, Hash: p.Hash, Text: p.Text}
		}
	}
	return out
}
