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

// spawnChild runs a blocking foreground child engine for the task tool.
// It never calls child.Run on the parent turn worker: Run and event drain
// each get their own goroutine. Parent emits ChildStarted/ChildCompleted;
// only PermissionAsked/PermissionResolved and QuestionAsked/QuestionResolved
// are re-emitted from the child.
func (e *Engine) spawnChild(ctx context.Context, req tool.TaskRequest) (tool.TaskResult, error) {
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
		return tool.TaskResult{}, fmt.Errorf("unknown agent %q", agentName)
	}

	childID := rand.Text()
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
		SessionID:          childID,
		ParentSessionID:    e.opts.SessionID,
		Depth:              childDepth,
		MaxChildDepth:      maxDepth,
		Select:             e.opts.Select,
		Registry:           childReg,
		WorkDir:            e.opts.WorkDir,
		ProjectRoot:        e.opts.ProjectRoot,
		Instructions:       e.opts.Instructions,
		SystemPrompt:       e.opts.SystemPrompt,
		Agents:             e.opts.Agents,
		InitialAgent:       agentName,
		InitialProvider:    e.provName,
		InitialModel:       e.model,
		InitialEffort:      e.effort,
		MaxTokens:          e.opts.MaxTokens,
		Rules:              permission.DeriveChildRules(parentLayers, childAgent.Permissions),
		PersistProjectRule: e.opts.PersistProjectRule,
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

	childCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	e.childMu.Lock()
	e.activeChildReply = child.perms.Reply
	e.activeChildQuestionReply = child.questions.Reply
	e.activeChildOps = child.Ops()
	e.childMu.Unlock()
	defer func() {
		e.childMu.Lock()
		e.activeChildReply = nil
		e.activeChildQuestionReply = nil
		e.activeChildOps = nil
		e.childMu.Unlock()
	}()

	childCorr := protocol.Correlation{
		SessionID:       childID,
		ParentSessionID: e.opts.SessionID,
		Depth:           childDepth,
	}
	e.emit(protocol.ChildStarted{
		Correlation: childCorr,
		Agent:       agentName,
		Prompt:      req.Prompt,
	})

	// stopReason is delivered once when the child turn ends. Buffer 1 so the
	// drain goroutine never blocks on a late parent reader.
	stopCh := make(chan string, 1)
	drainDone := make(chan struct{})
	var (
		failMu  sync.Mutex
		failMsg string
	)
	go func() {
		defer close(drainDone)
		for ev := range child.Events() {
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
			}
		}
	}()

	go child.Run(childCtx)

	// Deliver the subtask prompt unless the parent context is already done.
	select {
	case <-ctx.Done():
		cancel()
		select {
		case child.Ops() <- protocol.Interrupt{}:
		default:
		}
	case child.Ops() <- protocol.UserInput{Text: req.Prompt}:
	}

	var stopReason string
	var gotStop bool
	collectStop := func() {
		if gotStop {
			return
		}
		select {
		case stopReason = <-stopCh:
			gotStop = true
		case <-drainDone:
			select {
			case stopReason = <-stopCh:
				gotStop = true
			default:
			}
		}
	}

	select {
	case stopReason = <-stopCh:
		gotStop = true
	case <-ctx.Done():
		cancel()
		select {
		case child.Ops() <- protocol.Interrupt{}:
		default:
		}
		collectStop()
	case <-drainDone:
		collectStop()
	}

	// Shut down the child Run loop and join the drain goroutine.
	cancel()
	<-drainDone

	var once sync.Once
	var result tool.TaskResult
	complete := func(status protocol.ChildStatus, summary string) {
		once.Do(func() {
			e.emit(protocol.ChildCompleted{
				Correlation: childCorr,
				Status:      status,
				Summary:     summary,
			})
			result = tool.TaskResult{
				Output: summary,
				Status: string(status),
			}
		})
	}

	summary := lastAssistantText(child.messages)
	switch {
	case ctx.Err() != nil || (gotStop && stopReason == "interrupted"):
		if summary == "" {
			summary = "task canceled"
		}
		complete(protocol.ChildStatusCanceled, summary)
	case !gotStop || stopReason == "error":
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
		complete(protocol.ChildStatusFailed, summary)
	default:
		if summary == "" {
			summary = "task completed"
		}
		complete(protocol.ChildStatusCompleted, summary)
	}
	return result, nil
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
