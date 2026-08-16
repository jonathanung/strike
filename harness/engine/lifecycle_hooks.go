package engine

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// fireLifecycle dispatches declarative rules then shell hooks for a lifecycle
// event. Order is deterministic (rules → shell, config order within each).
// Blocking is only honored for pre_tool_use (via fireHookRules + runToolHooks).
// Observe-only events never stop the engine path.
func (e *Engine) fireLifecycle(corr protocol.Correlation, event, subject, callID, status, detail string) {
	if e == nil {
		return
	}
	e.fireHookRules(corr, event, subject, callID)
	e.runLifecycleShellHooks(context.Background(), corr, event, subject, callID, status, detail, nil, "", false)
}

// fireLifecycleCtx is fireLifecycle with a cancellable context for shell hooks.
func (e *Engine) fireLifecycleCtx(ctx context.Context, corr protocol.Correlation, event, subject, callID, status, detail string) {
	if e == nil {
		return
	}
	e.fireHookRules(corr, event, subject, callID)
	if ctx == nil {
		ctx = context.Background()
	}
	e.runLifecycleShellHooks(ctx, corr, event, subject, callID, status, detail, nil, "", false)
}

// runLifecycleShellHooks runs shell hooks for non-tool lifecycle events.
// Tool pre/post continue to use runToolHooks (richer tool fields + block).
func (e *Engine) runLifecycleShellHooks(ctx context.Context, corr protocol.Correlation, event, subject, callID, status, detail string, toolInput json.RawMessage, toolOutput string, isError bool) {
	if e == nil || len(e.opts.Hooks) == 0 {
		return
	}
	payload := tool.HookPayload{
		SchemaVersion:     tool.LifecycleVocabularyVersion,
		Event:             event,
		SessionID:         corr.SessionID,
		TurnID:            corr.TurnID,
		ProviderRequestID: corr.ProviderRequestID,
		ParentSessionID:   corr.ParentSessionID,
		Depth:             corr.Depth,
		Attempt:           corr.Attempt,
		CWD:               e.opts.WorkDir,
		Subject:           subject,
		ToolName:          subject, // matcher compat for tool-shaped subjects
		ToolCallID:        callID,
		ToolInput:         toolInput,
		ToolOutput:        toolOutput,
		IsError:           isError,
		Status:            status,
		Detail:            detail,
	}
	// Observe-only: ignore outcome (fail-open / non-block). Trust ask still runs.
	_, _ = tool.RunHooks(ctx, e.opts.Hooks, event, payload, e.opts.WorkDir, func(ctx context.Context, command string) error {
		if e.perms == nil {
			return nil
		}
		return e.perms.AskWithCorrelation(ctx, tool.AskRequest{
			Permission: "hook",
			Patterns:   []string{command},
			Always:     []string{command},
		}, corr)
	})
}

// emitPermission forwards permission events and fires permission_resolution
// hooks. Hooks observe only — they cannot change or widen the decision.
func (e *Engine) emitPermission(ev protocol.Event) {
	if e == nil {
		return
	}
	e.emit(ev)
	switch v := ev.(type) {
	case protocol.PermissionDecided:
		// Prefer PermissionDecided (has permission name + action).
		status := v.Action
		if v.Decision != "" {
			status = string(v.Decision)
		}
		detail := "action=" + v.Action
		if v.RequestID != "" {
			detail += " request_id=" + v.RequestID
		}
		if v.Layer != "" {
			detail += " layer=" + v.Layer
		}
		e.firePermissionResolution(v.Correlation, v.Permission, status, detail)
	}
}

// fireSessionLifecycle emits session_start or session_resume once at Run entry.
func (e *Engine) fireSessionLifecycle() {
	if e == nil {
		return
	}
	corr := e.sessionCorr()
	event := permission.HookEventSessionStart
	status := "start"
	// Resume: quiet startup (JSONL restore) or seeded history.
	// Prefer Options flags (stable) over e.quietStartup which is cleared after startup.
	if e.opts.QuietStartup || len(e.opts.InitialMessages) > 0 {
		event = permission.HookEventSessionResume
		status = "resume"
	}
	detail := ""
	if e.opts.InitialAgent != "" {
		detail = "agent=" + e.opts.InitialAgent
	}
	e.fireLifecycle(corr, event, "", "", status, detail)
}

// fireSessionEnd emits session_end on Run shutdown (observe-only).
// Shell hooks run without a trust prompt so shutdown cannot hang on Ask.
func (e *Engine) fireSessionEnd() {
	if e == nil {
		return
	}
	corr := e.sessionCorr()
	e.fireHookRules(corr, permission.HookEventSessionEnd, "", "")
	if len(e.opts.Hooks) == 0 {
		return
	}
	payload := tool.HookPayload{
		SchemaVersion:   tool.LifecycleVocabularyVersion,
		Event:           permission.HookEventSessionEnd,
		SessionID:       corr.SessionID,
		ParentSessionID: corr.ParentSessionID,
		Depth:           corr.Depth,
		CWD:             e.opts.WorkDir,
		Status:          "end",
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// ask=nil: no trust gate on shutdown (fail-open observe-only).
	_, _ = tool.RunHooks(ctx, e.opts.Hooks, permission.HookEventSessionEnd, payload, e.opts.WorkDir, nil)
}

// fireProviderAttempt notifies hooks before a provider stream attempt.
func (e *Engine) fireProviderAttempt(corr protocol.Correlation) {
	if e == nil {
		return
	}
	detail := ""
	if e.provName != "" {
		detail = "provider=" + e.provName
		if e.model != "" {
			detail += " model=" + e.model
		}
	}
	e.fireLifecycle(corr, permission.HookEventProviderAttempt, e.provName, "", "attempt", detail)
}

// fireProviderRetry notifies hooks when a transient provider retry is scheduled.
func (e *Engine) fireProviderRetry(corr protocol.Correlation, nextAttempt int, message string) {
	if e == nil {
		return
	}
	detail := strings.TrimSpace(message)
	if nextAttempt > 0 {
		if detail != "" {
			detail += " "
		}
		detail += "next_attempt=" + strconv.Itoa(nextAttempt)
	}
	e.fireLifecycle(corr, permission.HookEventProviderRetry, e.provName, "", "retry", detail)
}

// firePermissionResolution notifies hooks after a permission decision.
// Never blocks and never changes the decision (hooks cannot widen hard denies).
func (e *Engine) firePermissionResolution(corr protocol.Correlation, permissionName, decision, detail string) {
	if e == nil {
		return
	}
	e.fireLifecycle(corr, permission.HookEventPermissionResolution, permissionName, "", decision, detail)
}

// fireCompaction notifies hooks after a successful compaction.
func (e *Engine) fireCompaction(corr protocol.Correlation, reason, strategy string, removed, kept int) {
	if e == nil {
		return
	}
	detail := "reason=" + reason + " strategy=" + strategy +
		" removed=" + strconv.Itoa(removed) + " kept=" + strconv.Itoa(kept)
	e.fireLifecycle(corr, permission.HookEventCompaction, reason, "", strategy, detail)
}

// firePhaseTransition notifies hooks on phase enter/clear/recovery.
func (e *Engine) firePhaseTransition(corr protocol.Correlation, workflow, phase string, index int, status string) {
	if e == nil {
		return
	}
	subject := phase
	if subject == "" {
		subject = workflow
	}
	detail := "workflow=" + workflow
	if phase != "" {
		detail += " phase=" + phase
	}
	if index >= 0 {
		detail += " index=" + strconv.Itoa(index)
	}
	e.fireLifecycle(corr, permission.HookEventPhaseTransition, subject, "", status, detail)
}

// fireChildLifecycle notifies hooks on child start/complete.
func (e *Engine) fireChildLifecycle(corr protocol.Correlation, childSessionID, agent, status, detail string) {
	if e == nil {
		return
	}
	subject := childSessionID
	if subject == "" {
		subject = agent
	}
	e.fireLifecycle(corr, permission.HookEventChildLifecycle, subject, "", status, detail)
}

// fireVerificationGate notifies hooks around independent completion gates.
func (e *Engine) fireVerificationGate(ctx context.Context, corr protocol.Correlation, scope, status, detail string) {
	if e == nil {
		return
	}
	e.fireLifecycleCtx(ctx, corr, permission.HookEventVerificationGate, scope, "", status, detail)
}
