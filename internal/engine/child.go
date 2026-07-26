package engine

import (
	"context"
	"crypto/rand"
	"fmt"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/tool"
)

// childHandle tracks one non-blocking child engine while it runs.
type childHandle struct {
	id        string
	ops       chan<- protocol.Op
	cancel    context.CancelFunc
	done      chan struct{}
	permReply func(protocol.PermissionReply)
	qReply    func(protocol.QuestionReply)
	// eng is retained so the drain goroutine can read lastAssistantText.
	eng *Engine
}

// spawnChild starts a non-blocking child engine for the task tool and returns
// as soon as the child is running. The parent turn is not held open for the
// child's lifetime. Child Run and event drain each get their own goroutine
// under the parent engine's run context (not the parent turn context).
//
// Parent emits ChildStarted immediately and ChildCompleted when the child
// finishes. Only PermissionAsked/PermissionResolved and
// QuestionAsked/QuestionResolved are re-emitted from the child onto the
// parent event stream; optional ChildSession hooks persist the full child log.
func (e *Engine) spawnChild(ctx context.Context, req tool.TaskRequest) (tool.TaskResult, error) {
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

	childID := rand.Text()
	title := taskTitle(req.Prompt)
	if e.opts.OpenChildSession != nil {
		id, err := e.opts.OpenChildSession(e.opts.SessionID, childID, title)
		if err != nil {
			return tool.TaskResult{}, fmt.Errorf("open child session: %w", err)
		}
		if strings.TrimSpace(id) != "" {
			childID = id
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
		childReg = e.opts.Registry.CloneWithout("task")
	} else {
		childReg = e.opts.Registry.CloneWithout()
	}

	// Parent effective ceiling: configured layers plus the active parent
	// agent profile. Session always-grants are intentionally omitted so the
	// child starts with an empty granted set. Child agent Allows are dropped
	// in handleSelectAgent (Depth > 0) so a child profile cannot widen.
	parentLayers := append([]permission.Ruleset(nil), e.opts.Rules...)
	if len(e.agent.Permissions) > 0 {
		parentLayers = append(parentLayers, append(permission.Ruleset(nil), e.agent.Permissions...))
	}
	child := New(Options{
		SessionID:           childID,
		ParentSessionID:     e.opts.SessionID,
		Depth:               childDepth,
		MaxChildDepth:       maxDepth,
		Select:              e.opts.Select,
		Registry:            childReg,
		WorkDir:             e.opts.WorkDir,
		ProjectRoot:         e.opts.ProjectRoot,
		Instructions:        e.opts.Instructions,
		Memory:              e.opts.Memory,
		SystemPrompt:        e.opts.SystemPrompt,
		Agents:              e.opts.Agents,
		InitialAgent:        agentName,
		InitialProvider:     e.provName,
		InitialModel:        e.model,
		InitialEffort:       e.effort,
		MaxTokens:           e.opts.MaxTokens,
		MaxStreamAttempts:   e.opts.MaxStreamAttempts,
		StreamRetryBackoff:  e.opts.StreamRetryBackoff,
		ContextWindow:       e.contextWindow(),
		LookupContextWindow: e.opts.LookupContextWindow,
		CompactionThreshold: e.opts.CompactionThreshold,
		CompactionBuffer:    e.opts.CompactionBuffer,
		KeepUserTurns:       e.opts.KeepUserTurns,
		CompactionStrategy:  e.opts.CompactionStrategy,
		CompactionModel:     e.opts.CompactionModel,
		Rules:               permission.DeriveChildRules(parentLayers, childAgent.Permissions),
		Hooks:               e.opts.Hooks,
		HookRules:           e.opts.HookRules,
		PersistProjectRule:  e.opts.PersistProjectRule,
	})

	// Inherit the parent's live provider/model/priority. Clearing InitialProvider
	// avoids Run's silent Select failure leaving the child with no model while
	// the parent is healthy. Agent pins may still re-Select in handleSelectAgent
	// (those errors are emitted as EngineError and will surface via failMsg).
	if e.prov != nil {
		child.prov = e.prov
		child.provName = e.provName
		child.model = e.model
		child.priority = e.priority
		child.opts.InitialProvider = ""
		child.opts.InitialModel = ""
	}

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

	h := &childHandle{
		id:        childID,
		ops:       child.Ops(),
		cancel:    cancel,
		done:      make(chan struct{}),
		permReply: child.perms.Reply,
		qReply:    child.questions.Reply,
		eng:       child,
	}

	e.childMu.Lock()
	if e.children == nil {
		e.children = make(map[string]*childHandle)
	}
	e.children[childID] = h
	e.childMu.Unlock()

	e.emit(protocol.ChildStarted{
		Correlation: childCorr,
		Agent:       agentName,
		Prompt:      req.Prompt,
	})
	e.persistChildEvent(childID, protocol.ChildStarted{
		Correlation: childCorr,
		Agent:       agentName,
		Prompt:      req.Prompt,
	})

	// stopReason is delivered once when the child turn ends. Buffer 1 so the
	// drain goroutine never blocks on a late reader.
	stopCh := make(chan string, 1)
	var (
		failMu  sync.Mutex
		failMsg string
	)
	go func() {
		defer close(h.done)
		defer e.unregisterChild(childID)
		defer e.closeChildSession(childID)

		for ev := range child.Events() {
			e.persistChildEvent(childID, ev)
			switch ev := ev.(type) {
			case protocol.PermissionAsked:
				e.emit(ev)
			case protocol.PermissionResolved:
				e.emit(ev)
			case protocol.QuestionAsked:
				e.emit(ev)
			case protocol.QuestionResolved:
				e.emit(ev)
			case protocol.TurnCompleted:
				// One-shot task: shut down the child Run loop after its turn.
				select {
				case stopCh <- ev.StopReason:
				default:
				}
				cancel()
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
		summary := lastAssistantText(child.messages)
		var status protocol.ChildStatus
		switch {
		case (childCtx.Err() != nil && !gotStop) || (gotStop && stopReason == "interrupted"):
			status = protocol.ChildStatusCanceled
			if summary == "" {
				summary = "task canceled"
			}
		case !gotStop || stopReason == "error":
			status = protocol.ChildStatusFailed
			failMu.Lock()
			errText := failMsg
			failMu.Unlock()
			switch {
			case errText != "" && summary != "":
				summary = summary + "\n\nError: " + errText
			case errText != "":
				summary = errText
			case summary == "":
				summary = "task failed"
			}
		default:
			status = protocol.ChildStatusCompleted
			if summary == "" {
				summary = "task completed"
			}
		}
		completed := protocol.ChildCompleted{
			Correlation: childCorr,
			Status:      status,
			Summary:     summary,
		}
		e.emit(completed)
		e.persistChildEvent(childID, completed)
		// Wake Run so the parent can inject a model-visible summary (and
		// auto-nudge when idle). Non-blocking: drop if Run is shutting down.
		select {
		case e.childDone <- completed:
		default:
		}
	}()

	go child.Run(childCtx)

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

	out := fmt.Sprintf(
		"Started child session %s (agent %s). It runs independently and does not block this turn. A child.completed event will report its terminal summary.",
		childID, agentName,
	)
	return tool.TaskResult{
		Output:    out,
		Status:    "started",
		SessionID: childID,
	}, nil
}

func (e *Engine) unregisterChild(id string) {
	e.childMu.Lock()
	defer e.childMu.Unlock()
	delete(e.children, id)
}

// queueChildCompleted records a durable model-facing notice for a finished
// child. Flushed by flushPendingChildNotices when the parent is idle.
func (e *Engine) queueChildCompleted(c protocol.ChildCompleted) {
	e.pendingChildNotices = append(e.pendingChildNotices, formatChildCompletedNotice(c))
}

// flushPendingChildNotices starts a short parent turn with queued child
// completion summaries when the parent is idle and a provider is selected.
// Notices stay queued while a turn is active or no model is available so the
// next successful flush (or user turn after provider select) can deliver them.
func (e *Engine) flushPendingChildNotices(ctx context.Context) {
	if len(e.pendingChildNotices) == 0 {
		return
	}
	e.joinFinishingTurn()
	if e.turnActive() {
		return
	}
	if e.prov == nil || ctx.Err() != nil {
		return
	}
	text := strings.Join(e.pendingChildNotices, "\n\n")
	e.pendingChildNotices = nil
	e.startTurn(ctx, text)
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
	var b strings.Builder
	if short != "" {
		fmt.Fprintf(&b, "[child.completed session=%s status=%s]", short, status)
	} else {
		fmt.Fprintf(&b, "[child.completed status=%s]", status)
	}
	if summary := strings.TrimSpace(c.Summary); summary != "" {
		b.WriteByte('\n')
		b.WriteString(summary)
	}
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

func taskTitle(prompt string) string {
	s := strings.Join(strings.Fields(strings.TrimSpace(prompt)), " ")
	if s == "" {
		return "task"
	}
	const max = 48
	if len(s) <= max {
		return s
	}
	// Avoid mid-rune truncation.
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
