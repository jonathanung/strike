package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	mrand "math/rand"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/artifact"
	"github.com/jonathanung/strike-cli/internal/ledger"
	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
	"github.com/jonathanung/strike-cli/internal/secret"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func (e *Engine) turnActive() bool {
	if e.turnDone == nil {
		return false
	}
	select {
	case <-e.turnDone:
		return false
	default:
		return true
	}
}

// pendingUserInput is one mid-turn buffered prompt (text + optional images).
type pendingUserInput struct {
	text   string
	images []protocol.ImageAttachment
}

// pendingSteer is one active-turn redirect awaiting a safe request boundary.
type pendingSteer struct {
	text   string
	images []protocol.ImageAttachment
	// targetTurnID captured at accept time for durable TurnSteered events.
	targetTurnID string
}

// protocolImagesToProvider decodes base64 session attachments into provider images.
// Invalid entries are skipped so a corrupt log line does not block restore/send.
func protocolImagesToProvider(images []protocol.ImageAttachment) []provider.Image {
	if len(images) == 0 {
		return nil
	}
	out := make([]provider.Image, 0, len(images))
	for _, img := range images {
		mime := strings.TrimSpace(img.MIME)
		if mime == "" || img.Data == "" {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(img.Data)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(img.Data)
			if err != nil {
				continue
			}
		}
		if len(raw) == 0 {
			continue
		}
		out = append(out, provider.Image{MIME: mime, Data: raw})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// handleSteer accepts an active-turn redirect. Distinct from UserInput (queue)
// and Interrupt (cancel). Targets the live session/turn when ids are set.
func (e *Engine) handleSteer(op protocol.Steer) {
	if strings.TrimSpace(op.Text) == "" && len(op.Images) == 0 {
		return
	}
	if !e.turnActive() {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "no active turn to steer; use user.input to start a turn",
			Code:        protocol.ErrorCodeInvalidArgs,
		})
		return
	}
	// Session targeting (multi-root frontends).
	if sid := strings.TrimSpace(op.SessionID); sid != "" && e.opts.SessionID != "" && sid != e.opts.SessionID {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("steer session %q does not match live session %q", sid, e.opts.SessionID),
			Code:        protocol.ErrorCodeInvalidArgs,
		})
		return
	}
	if tid := strings.TrimSpace(op.TurnID); tid != "" && e.activeTurnID != "" && tid != e.activeTurnID {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     fmt.Sprintf("steer turn %q does not match active turn %q", tid, e.activeTurnID),
			Code:        protocol.ErrorCodeInvalidArgs,
		})
		return
	}
	// Replace prior pending steer (latest redirect wins).
	e.pendingSteerMu.Lock()
	e.pendingSteer = &pendingSteer{
		text:         op.Text,
		images:       append([]protocol.ImageAttachment(nil), op.Images...),
		targetTurnID: e.activeTurnID,
	}
	e.pendingSteerMu.Unlock()
}

// takePendingSteer atomically clears and returns any pending steer.
func (e *Engine) takePendingSteer() *pendingSteer {
	e.pendingSteerMu.Lock()
	ps := e.pendingSteer
	e.pendingSteer = nil
	e.pendingSteerMu.Unlock()
	return ps
}

// hasPendingSteer reports whether a steer is waiting (racy peek for branch
// decisions; takePendingSteer is the authoritative claim).
func (e *Engine) hasPendingSteer() bool {
	e.pendingSteerMu.Lock()
	defer e.pendingSteerMu.Unlock()
	return e.pendingSteer != nil
}

// applyPendingSteer injects pending steer as a user message at a safe history
// boundary and emits a durable TurnSteered event. No-op when none pending.
// Never called mid-tool-call.
func (e *Engine) applyPendingSteer(turnID, mode string) {
	ps := e.takePendingSteer()
	if ps == nil {
		return
	}
	corr := e.baseCorr()
	corr.TurnID = turnID
	// Durable user.message so resume sees the guidance in the transcript.
	e.emit(protocol.UserMessage{Correlation: corr, Text: ps.text, Images: ps.images})
	e.messages = append(e.messages, provider.Message{
		Role:   provider.RoleUser,
		Text:   ps.text,
		Images: protocolImagesToProvider(ps.images),
	})
	e.emit(protocol.TurnSteered{
		Correlation:  corr,
		Text:         ps.text,
		Mode:         mode,
		TargetTurnID: ps.targetTurnID,
	})
}

// fallbackSteerToQueue converts an unapplied steer into a next-turn UserInput
// with a visible TurnSteered(queued_fallback) event. Used on cancel/interrupt
// when a boundary was never reached.
//
// Enqueue is deferred onto Run via steerQueueFallback so pendingUserInputs is
// only mutated on the Run goroutine (same as handleOp → enqueueUserInput).
func (e *Engine) fallbackSteerToQueue(turnID string) {
	ps := e.takePendingSteer()
	if ps == nil {
		return
	}
	corr := e.baseCorr()
	corr.TurnID = turnID
	e.emit(protocol.TurnSteered{
		Correlation:  corr,
		Text:         ps.text,
		Mode:         protocol.SteerModeQueuedFallback,
		TargetTurnID: ps.targetTurnID,
	})
	e.pendingSteerMu.Lock()
	e.steerQueueFallback = &pendingUserInput{
		text:   ps.text,
		images: append([]protocol.ImageAttachment(nil), ps.images...),
	}
	e.pendingSteerMu.Unlock()
}

// takeSteerQueueFallback moves a deferred steer→queue item onto the Run path.
func (e *Engine) takeSteerQueueFallback() *pendingUserInput {
	e.pendingSteerMu.Lock()
	item := e.steerQueueFallback
	e.steerQueueFallback = nil
	e.pendingSteerMu.Unlock()
	return item
}

// enqueueUserInput buffers input for FIFO start after the active turn ends.
// Empty/whitespace-only text with no images is ignored. Queue survives Interrupt.
// When full (maxPendingUserInputs), rejects with EngineError code queue_full
// rather than blocking the Ops sender (explicit backpressure).
func (e *Engine) enqueueUserInput(op protocol.UserInput) {
	if strings.TrimSpace(op.Text) == "" && len(op.Images) == 0 {
		return
	}
	if len(e.pendingUserInputs) >= maxPendingUserInputs {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "input queue full; wait for the current turn to finish",
			Code:        protocol.ErrorCodeQueueFull,
		})
		return
	}
	e.pendingUserInputs = append(e.pendingUserInputs, pendingUserInput{
		text:   op.Text,
		images: append([]protocol.ImageAttachment(nil), op.Images...),
	})
}

// drainIdleFollowups starts at most one follow-up turn when idle: preferred
// user-queued input, otherwise pending child-completion notices, otherwise
// peer mailbox messages.
func (e *Engine) drainIdleFollowups(ctx context.Context) {
	// Promote deferred steer→queue items before draining the user-input FIFO.
	if item := e.takeSteerQueueFallback(); item != nil {
		e.enqueueUserInput(protocol.UserInput{Text: item.text, Images: item.images})
	}
	if e.startNextPendingUserInput(ctx) {
		return
	}
	if e.hasPendingChildNotices() {
		e.flushPendingChildNotices(ctx)
		return
	}
	e.flushPendingMailbox(ctx)
}

// startNextPendingUserInput pops and starts the next queued UserInput when
// idle with a provider. Returns true when a turn was started.
func (e *Engine) startNextPendingUserInput(ctx context.Context) bool {
	if len(e.pendingUserInputs) == 0 {
		return false
	}
	e.joinFinishingTurn()
	if e.turnActive() || e.prov == nil || ctx.Err() != nil {
		return false
	}
	if e.sessionBudget != nil && e.sessionBudget.Exhausted() {
		// Drop queued input; surface the envelope stop once.
		e.pendingUserInputs = nil
		_ = e.rejectUserInputIfBudgetExhausted()
		return false
	}
	item := e.pendingUserInputs[0]
	e.pendingUserInputs = e.pendingUserInputs[1:]
	if len(e.pendingUserInputs) == 0 {
		e.pendingUserInputs = nil
	}
	e.startTurn(ctx, item.text, item.images)
	return true
}

func (e *Engine) startTurn(ctx context.Context, text string, images []protocol.ImageAttachment) {
	// Mint turn ID only after input acceptance (provider present, no active turn).
	turnID := rand.Text()
	var turnCtx context.Context
	var cancel context.CancelFunc
	if e.opts.TurnTimeout > 0 {
		turnCtx, cancel = context.WithTimeout(ctx, e.opts.TurnTimeout)
	} else {
		turnCtx, cancel = context.WithCancel(ctx)
	}
	done := make(chan struct{})
	finishing := make(chan struct{})
	e.turnCancel = cancel
	e.turnDone = done
	e.turnFinishing = finishing
	e.activeTurnID = turnID
	go func() {
		defer close(done)
		defer cancel()
		e.runTurn(turnCtx, text, images, turnID, finishing)
	}()
}

// maybeTitleSession emits SessionTitled once from the first non-empty user text.
func (e *Engine) maybeTitleSession(text string) {
	if e.titled {
		return
	}
	title := sessionTitleFromText(text)
	if title == "" {
		return
	}
	e.titled = true
	e.emit(protocol.SessionTitled{Correlation: e.sessionCorr(), Title: title})
}

// sessionTitleFromText collapses whitespace, drops controls, and truncates.
// Kept local so engine does not import internal/session (cmd/strike only).
// Logic mirrors session.TitleFromText.
func sessionTitleFromText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	prevSpace := false
	for _, r := range text {
		switch {
		case r == '\u00a0':
			r = ' '
			fallthrough
		case unicode.IsSpace(r):
			if b.Len() == 0 || prevSpace {
				continue
			}
			b.WriteByte(' ')
			prevSpace = true
		case r < 0x20 || r == 0x7f:
			continue
		default:
			b.WriteRune(r)
			prevSpace = false
		}
	}
	s := strings.TrimSpace(b.String())
	if s == "" {
		return ""
	}
	const maxRunes = 32 // keep in sync with session.titleMaxRunes
	if utf8.RuneCountInString(s) <= maxRunes {
		return s
	}
	return string([]rune(s)[:maxRunes])
}

// runTurn establishes the user-turn lifecycle, then invokes the selected
// function harness or runs the engine's built-in model/tool loop.
// turnID is immutable for the turn; each Provider.Stream call gets its own
// provider-request ID and attempt number (retries included). finishing is
// closed exactly once immediately before the terminal TurnCompleted emission
// so Run can join the worker before the next op.
func (e *Engine) runTurn(ctx context.Context, text string, images []protocol.ImageAttachment, turnID string, finishing chan struct{}) {
	turnCorr := e.baseCorr()
	turnCorr.TurnID = turnID
	e.checkpoints.BeginTurn(turnID)
	e.turnDiff.Reset()
	if e.toolLoop != nil {
		e.toolLoop.reset()
	}
	e.toolLoopStop = ""
	// Reset content-free tool-chain correlation for this turn (#891).
	if e.perms != nil {
		e.perms.BeginTurn()
	}
	// Promote soft-budget finalization armed → active for this turn (#879).
	e.noteBudgetFinalizationTurnStart()
	// Per-turn token ceiling resets each turn (#577).
	if e.turnTokens != nil {
		e.turnTokens.reset()
	}
	e.sessionBudgetStop = false
	e.sessionBudgetStopKind = ""
	e.sessionBudgetStopMsg = ""
	e.emit(protocol.UserMessage{Correlation: turnCorr, Text: text, Images: images})
	e.maybeTitleSession(text)
	e.emit(protocol.TurnStarted{Correlation: turnCorr})
	e.fireLifecycle(turnCorr, permission.HookEventTurnStart, "", "", "start", "")
	e.messages = append(e.messages, provider.Message{
		Role:   provider.RoleUser,
		Text:   text,
		Images: protocolImagesToProvider(images),
	})

	// spawnChild attaches a function only when the selected task subagent has a
	// harness. Root engines never carry this state.
	if e.taskHarness != nil {
		input, providerObject, emit := e.harnessEnvironment(ctx, turnCorr, e.taskHarnessName)
		result, err := e.taskHarness(input, providerObject, emit)
		if err != nil {
			e.failTurn(ctx, err, turnCorr, finishing)
			return
		}
		if len(result.Calls) != 0 || result.StopReason == "tool_use" {
			e.failTurn(ctx, errors.New("task harness returned tool calls that it cannot execute"), turnCorr, finishing)
			return
		}
		for _, raw := range result.Reasoning {
			if text := provider.ReasoningText(raw); text != "" {
				e.emit(protocol.ReasoningDelta{Correlation: turnCorr, Text: text})
			}
		}
		if result.Text != "" {
			e.emit(protocol.TextDelta{Correlation: turnCorr, Text: result.Text})
		}
		e.messages = append(e.messages, provider.Message{Role: provider.RoleAssistant, Text: result.Text, Reasoning: result.Reasoning})
		e.completeTurn(ctx, finishing, turnCorr, result.StopReason)
		return
	}

	for {
		// Deliver child.completed, peer mailbox, and active-turn steer into
		// model history before each Stream (tool-round boundary). Never
		// mid-tool-call — preserves assistant/tool-result pairing.
		e.injectPendingChildNotices()
		e.injectPendingMailbox()
		e.applyPendingSteer(turnID, protocol.SteerModeBoundary)
		e.maybePruneToolResults()
		e.maybeThresholdCompact(ctx, turnID)
		e.maybeEmitFitWarning(turnID)
		outcome, reqCorr, err := e.streamModel(ctx, turnID)
		if err != nil {
			// If canceled with a pending steer, fall back to next-turn queue
			// rather than dropping the guidance (visible TurnSteered event).
			if e.hasPendingSteer() && (ctx.Err() != nil || errors.Is(err, context.Canceled)) {
				e.fallbackSteerToQueue(turnID)
			}
			e.failTurn(ctx, err, reqCorr, finishing)
			return
		}

		e.messages = append(e.messages, provider.Message{
			Role:      provider.RoleAssistant,
			Text:      outcome.text,
			ToolCalls: outcome.calls,
			Reasoning: outcome.reasoning,
		})
		// Session / per-turn envelope hard stop (#577): end after this stream
		// with a renderable event (already emitted from emitUsage).
		if e.sessionBudgetStop {
			e.finishSessionBudgetStop(ctx, finishing, reqCorr)
			return
		}
		if len(outcome.calls) == 0 {
			// Final assistant message with no tools. If a steer arrived during
			// this stream, continue the turn with cancel_restart semantics
			// (steer as next user message) instead of ending — avoids losing
			// guidance and does not duplicate any tool calls.
			if e.hasPendingSteer() {
				e.applyPendingSteer(turnID, protocol.SteerModeCancelRestart)
				continue
			}
			e.completeTurn(ctx, finishing, reqCorr, outcome.stopReason)
			return
		}
		// Soft-budget finalization: refuse tools and end the turn so the
		// reserved handoff cannot burn more budget (#879). Assistant text
		// (if any) is kept for structured handoff parse.
		if e.isBudgetFinalizing() {
			for _, call := range outcome.calls {
				e.messages = append(e.messages, e.blockedBudgetFinalizationTool(call, reqCorr))
			}
			e.completeTurn(ctx, finishing, reqCorr, "end_turn")
			return
		}
		for i, call := range outcome.calls {
			// Unstarted calls: history-only synthetic results, no begin/end/Execute.
			if ctx.Err() != nil {
				e.appendUnstartedToolResults(outcome.calls[i:])
				e.failTurn(ctx, ctx.Err(), reqCorr, finishing)
				return
			}
			e.messages = append(e.messages, e.execToolCall(ctx, call, reqCorr))
			if e.toolLoopStop != "" {
				e.appendUnstartedToolResults(outcome.calls[i+1:])
				e.failTurn(ctx, errToolLoopDetected, reqCorr, finishing)
				return
			}
			if ctx.Err() != nil {
				// Current call was started (and canceled); remaining are unstarted.
				e.appendUnstartedToolResults(outcome.calls[i+1:])
				e.failTurn(ctx, ctx.Err(), reqCorr, finishing)
				return
			}
		}
		// Apply tool-queued agent switch so the next Stream uses the new
		// agent system prompt (not only after TurnCompleted).
		e.applyPendingAgent()
		// Envelope may have tripped via child usage while tools ran (#577).
		if e.sessionBudgetStop || (e.sessionBudget != nil && e.sessionBudget.Exhausted()) {
			if !e.sessionBudgetStop {
				e.armSessionBudgetStop(protocol.SessionBudgetKindCostUSD,
					"session cost budget exhausted")
			}
			e.finishSessionBudgetStop(ctx, finishing, reqCorr)
			return
		}
	}
}

// streamOutcome is one successful provider stream (after any retries).
type streamOutcome struct {
	text       string
	calls      []provider.ToolCall
	reasoning  []json.RawMessage
	stopReason string
}

// streamModel performs one logical model request, retrying transient stream
// failures with a fresh attempt identity. Tools are never executed here, so a
// retry cannot duplicate completed tool side effects. A classified context
// overflow triggers at most one compaction + model-only retry.
func (e *Engine) streamModel(ctx context.Context, turnID string) (streamOutcome, protocol.Correlation, error) {
	outcome, corr, err := e.streamModelAttempts(ctx, turnID)
	if err == nil {
		return outcome, corr, nil
	}
	if ctx.Err() != nil {
		return streamOutcome{}, corr, ctx.Err()
	}
	if !provider.IsContextOverflow(err) {
		return streamOutcome{}, corr, err
	}
	overflowCorr := e.baseCorr()
	overflowCorr.TurnID = turnID
	if !e.applyCompaction(ctx, protocol.CompactionReasonOverflow, overflowCorr, "") {
		return streamOutcome{}, corr, fmt.Errorf("context window exceeded; compaction could not reduce history: %w", err)
	}
	// Single recovery pass: model-only, no tool replay (tools run after success).
	outcome, corr, err = e.streamModelAttempts(ctx, turnID)
	if err != nil && provider.IsContextOverflow(err) {
		return streamOutcome{}, corr, fmt.Errorf("context window exceeded after compaction: %w", err)
	}
	return outcome, corr, err
}

// streamModelAttempts retries transient provider failures for one logical
// model request. Overflow is not retried here (see streamModel).
func (e *Engine) streamModelAttempts(ctx context.Context, turnID string) (streamOutcome, protocol.Correlation, error) {
	maxAttempts := e.opts.MaxStreamAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	var lastCorr protocol.Correlation
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		reqCorr := e.baseCorr()
		reqCorr.TurnID = turnID
		reqCorr.ProviderRequestID = rand.Text()
		reqCorr.Attempt = attempt
		lastCorr = reqCorr

		outcome, err := e.consumeStream(ctx, reqCorr)
		if err == nil {
			return outcome, reqCorr, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return streamOutcome{}, reqCorr, ctx.Err()
		}
		if attempt == maxAttempts || !provider.IsRetryable(err) {
			return streamOutcome{}, reqCorr, err
		}
		delay, fromProvider := e.streamRetryDelayFor(err, attempt+1)
		e.emit(protocol.ProviderRetrying{
			Correlation:  reqCorr,
			NextAttempt:  attempt + 1,
			DelayMs:      int(delay / time.Millisecond),
			FromProvider: fromProvider,
			Message:      err.Error(),
		})
		e.fireProviderRetry(reqCorr, attempt+1, err.Error())
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return streamOutcome{}, reqCorr, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return streamOutcome{}, lastCorr, lastErr
}

// streamRetryDelayFor prefers valid provider Retry-After guidance; otherwise
// uses StreamRetryBackoff or the default jittered exponential policy.
func (e *Engine) streamRetryDelayFor(err error, nextAttempt int) (delay time.Duration, fromProvider bool) {
	if d, ok := provider.RetryAfter(err); ok {
		return d, true
	}
	return e.streamRetryDelay(nextAttempt), false
}

func (e *Engine) streamRetryDelay(nextAttempt int) time.Duration {
	if e.opts.StreamRetryBackoff != nil {
		return e.opts.StreamRetryBackoff(nextAttempt)
	}
	// 200ms, 400ms, 800ms… capped at 2s, full jitter in [0, d].
	shift := nextAttempt - 2
	if shift < 0 {
		shift = 0
	}
	d := 200 * time.Millisecond << shift
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	if d <= 0 {
		return 0
	}
	return time.Duration(mrand.Int63n(int64(d) + 1))
}

// consumeStream runs one Provider.Stream attempt and applies the terminal
// contract. On success, history-ready text/tool/reasoning are returned and
// usage is emitted; nothing is appended to e.messages here.
func (e *Engine) consumeStream(ctx context.Context, reqCorr protocol.Correlation) (streamOutcome, error) {
	e.fireProviderAttempt(reqCorr)
	layers, shed := e.systemLayersWithMeta()
	system := joinPromptLayerTexts(layers)
	tools, _ := e.effectiveToolSchemas()
	e.recordStreamEffective(layers, system, tools, shed)
	stream, err := e.admitModelStream(ctx, reqCorr, provider.Request{
		Model:     e.model,
		System:    system,
		Messages:  e.messages,
		Tools:     tools,
		MaxTokens: e.opts.MaxTokens,
		Effort:    providerEffort(e.effort),
		Priority:  e.priority,
		CacheKey:  e.opts.SessionID,
	})
	if err != nil {
		return streamOutcome{}, err
	}

	var textBuf strings.Builder
	var calls []provider.ToolCall
	var reasoning []json.RawMessage
	stopReason := ""
	var streamErr error
	terminated := false
	// Observe turn cancel between stream events so Interrupt does not wait
	// for a slow/uncooperative provider to close the channel. Remaining
	// events are drained so the provider goroutine is not stuck on send.
	for {
		if ctx.Err() != nil {
			go drainStream(stream)
			return streamOutcome{}, ctx.Err()
		}
		var ev provider.StreamEvent
		var ok bool
		select {
		case <-ctx.Done():
			go drainStream(stream)
			return streamOutcome{}, ctx.Err()
		case ev, ok = <-stream:
			if !ok {
				goto streamClosed
			}
		}
		if terminated {
			continue
		}
		switch ev.Type {
		case provider.EventTextDelta:
			textBuf.WriteString(ev.Text)
			e.emit(protocol.TextDelta{Correlation: reqCorr, Text: ev.Text})
		case provider.EventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case provider.EventReasoning:
			// Opaque bytes stay on the assistant message for vendor replay
			// (Anthropic requires thinking blocks verbatim). Displayable
			// prose, when present, streams to the frontend as ReasoningDelta.
			if len(ev.Reasoning) > 0 {
				reasoning = append(reasoning, ev.Reasoning)
			}
			text := ev.Text
			if text == "" {
				text = provider.ReasoningText(ev.Reasoning)
			}
			if text != "" {
				e.emit(protocol.ReasoningDelta{Correlation: reqCorr, Text: text})
			}
		case provider.EventDone:
			terminated = true
			stopReason = ev.StopReason
			e.emitUsage(reqCorr, ev.Usage)
		case provider.EventError:
			terminated = true
			streamErr = ev.Err
			if streamErr == nil {
				streamErr = errors.New("provider stream error")
			}
		}
	}
streamClosed:
	if ctx.Err() != nil {
		return streamOutcome{}, ctx.Err()
	}
	if !terminated {
		return streamOutcome{}, provider.ErrIncompleteStream
	}
	if streamErr != nil {
		return streamOutcome{}, streamErr
	}
	return streamOutcome{
		text:       textBuf.String(),
		calls:      calls,
		reasoning:  reasoning,
		stopReason: stopReason,
	}, nil
}

// drainStream consumes remaining provider events so a canceled consumer does
// not leave the producer blocked on a full channel send.
func drainStream(stream <-chan provider.StreamEvent) {
	for range stream {
	}
}

// appendUnstartedToolResults adds synthetic RoleTool error results for calls
// that never began execution after a turn interrupt.
func (e *Engine) appendUnstartedToolResults(calls []provider.ToolCall) {
	for _, call := range calls {
		e.messages = append(e.messages, e.settleToolFeedback(toolFeedback{
			CallID:    call.ID,
			Output:    unstartedToolOutput,
			IsError:   true,
			ErrorCode: protocol.ErrorCodeCanceled,
		}))
	}
}

// toolFeedback is the uniform settlement for one tool call: optional
// ToolCallEnd for the frontend plus a RoleTool message for the model.
// EmitEnd is false for unstarted calls (history-only synthetic results).
type toolFeedback struct {
	Corr      protocol.Correlation
	CallID    string
	Output    string
	IsError   bool
	ErrorCode string
	Retryable bool
	Title     string
	Metadata  json.RawMessage
	EmitEnd   bool
}

// blockedBudgetFinalizationTool emits begin+blocked end for a tool refused
// during soft-budget finalization (tools disabled; handoff text only).
func (e *Engine) blockedBudgetFinalizationTool(call provider.ToolCall, corr protocol.Correlation) provider.Message {
	e.emit(protocol.ToolCallBegin{
		Correlation: corr,
		CallID:      call.ID,
		Name:        call.Name,
		Args:        secret.RedactJSON(call.Args),
	})
	return e.settleToolFeedback(toolFeedback{
		Corr:      corr,
		CallID:    call.ID,
		Output:    protocol.ToolFeedbackBlocked("budget finalization: tools disabled; emit structured handoff JSON only"),
		IsError:   true,
		EmitEnd:   true,
		ErrorCode: protocol.ErrorCodeBlocked,
		Title:     call.Name,
	})
}

// settleToolFeedback is the formal tool-result feedback path: one place that
// pairs model history with (when EmitEnd) a ToolCallEnd event. Permission
// denials, user rejects, hook blocks, interrupts, and ordinary results all
// settle here so future phase bounces and hook messages share the same shape.
// Output is scrubbed so secrets never reach the model, TUI, or session tee.
func (e *Engine) settleToolFeedback(fb toolFeedback) provider.Message {
	fb.Output = secret.ScrubToolOutput(fb.Output)
	fb.Title = secret.Redact(fb.Title)
	if fb.EmitEnd {
		e.emit(protocol.ToolCallEnd{
			Correlation: fb.Corr,
			CallID:      fb.CallID,
			Title:       fb.Title,
			Output:      fb.Output,
			IsError:     fb.IsError,
			ErrorCode:   fb.ErrorCode,
			Metadata:    fb.Metadata,
		})
	}
	tr := &provider.ToolResult{
		CallID:  fb.CallID,
		Output:  fb.Output,
		IsError: fb.IsError,
	}
	if fb.IsError && fb.ErrorCode != "" {
		tr.ErrorCode = fb.ErrorCode
		tr.Retryable = fb.Retryable
	}
	return provider.Message{
		Role:       provider.RoleTool,
		ToolResult: tr,
	}
}

// classifiedToolFailure is model-facing text plus a stable error code.
type classifiedToolFailure struct {
	Output    string
	Code      string
	Retryable bool
}

// modelFacingToolOutput maps Execute errors onto protocol.ToolFeedback* text
// and a stable error code. Success returns the tool's own output unchanged.
func modelFacingToolOutput(res tool.Result, err error) (output string, isError bool, fail classifiedToolFailure) {
	if err == nil {
		return res.Output, false, classifiedToolFailure{}
	}
	fail = classifyToolFailure(err)
	return fail.Output, true, fail
}

// classifyToolFailure maps permission/question/tool/context errors onto stable
// codes without panicking on unknown types (fallback: internal).
func classifyToolFailure(err error) classifiedToolFailure {
	if err == nil {
		return classifiedToolFailure{}
	}
	var permDenied *permission.DeniedError
	var permRejected *permission.RejectedError
	var qRejected *question.RejectedError
	var toolRejected *tool.UserRejectedError
	var coded *tool.CodedError
	switch {
	case errors.As(err, &permDenied):
		return classifiedToolFailure{
			Output:    permDenied.Error(),
			Code:      protocol.ErrorCodePermissionDenied,
			Retryable: false,
		}
	case errors.As(err, &permRejected):
		return classifiedToolFailure{
			Output:    permRejected.Error(),
			Code:      protocol.ErrorCodePermissionDenied,
			Retryable: false,
		}
	case errors.As(err, &qRejected):
		return classifiedToolFailure{
			Output:    qRejected.Error(),
			Code:      protocol.ErrorCodePermissionDenied,
			Retryable: false,
		}
	case errors.As(err, &toolRejected):
		return classifiedToolFailure{
			Output:    protocol.ToolFeedbackUserRejected(toolRejected.Message),
			Code:      protocol.ErrorCodePermissionDenied,
			Retryable: false,
		}
	case errors.As(err, &coded) && coded != nil:
		out := protocol.ToolFeedbackError(coded.Error())
		code := string(coded.Code)
		if code == "" || !tool.ValidErrorCode(coded.Code) {
			code = protocol.ErrorCodeInternal
		}
		return classifiedToolFailure{
			Output:    out,
			Code:      code,
			Retryable: coded.Retryable,
		}
	default:
		classified := tool.Classify(err)
		out := protocol.ToolFeedbackError(classified.Message)
		if classified.Code == tool.CodeCanceled {
			out = protocol.ToolFeedbackCanceled()
		}
		return classifiedToolFailure{
			Output:    out,
			Code:      string(classified.Code),
			Retryable: classified.Retryable,
		}
	}
}

// isUserTurnInterrupt reports whether err is an interactive user rejection
// that should end the turn after the tool result is settled (permission
// reject, question dismiss, plan/phase decline). Hard ruleset denies are not
// included — those feed back so the model can try another approach.
func isUserTurnInterrupt(err error) bool {
	if err == nil {
		return false
	}
	var permRejected *permission.RejectedError
	var qRejected *question.RejectedError
	var toolRejected *tool.UserRejectedError
	return errors.As(err, &permRejected) ||
		errors.As(err, &qRejected) ||
		errors.As(err, &toolRejected)
}

// execToolCall runs one tool call and returns the tool-result message to
// feed back to the model. Failures (unknown tool, bad args, hard deny)
// become correctable error results so the model can course-correct. User
// permission rejects, question dismissals, and plan/phase declines settle
// as error results then interrupt the turn. Cancellation after
// ToolCallBegin yields one correlated ToolCallEnd with a deterministic
// canceled output; it does not invent PermissionResolved. If begin was
// never emitted, only a history-only unstarted result is returned.
func (e *Engine) execToolCall(ctx context.Context, call provider.ToolCall, corr protocol.Correlation) provider.Message {
	// Redact args on the emitted begin only — Execute still receives call.Args.
	begin := protocol.ToolCallBegin{
		Correlation: corr,
		CallID:      call.ID,
		Name:        call.Name,
		Args:        secret.RedactJSON(call.Args),
	}
	// Ask Run to emit begin so Interrupt can be applied while Events is full.
	result := make(chan beginAck, 1)
	select {
	case e.beginReqs <- beginReq{begin: begin, result: result}:
	case <-ctx.Done():
		// Canceled before Run accepted the begin request — unstarted.
		return e.settleToolFeedback(toolFeedback{
			CallID:    call.ID,
			Output:    unstartedToolOutput,
			IsError:   true,
			ErrorCode: protocol.ErrorCodeCanceled,
		})
	}
	ack := <-result
	if !ack.emitted {
		return e.settleToolFeedback(toolFeedback{
			CallID:    call.ID,
			Output:    unstartedToolOutput,
			IsError:   true,
			ErrorCode: protocol.ErrorCodeCanceled,
		})
	}
	// Begin was emitted. Pre-Execute cancel/shutdown check (no Execute).
	if ctx.Err() != nil {
		return e.canceledOrTimeoutToolResult(ctx, call.ID, corr, tool.Result{})
	}

	// Declarative rules first (cheap, no process). Block skips shell + Execute.
	if d := e.fireHookRules(corr, permission.HookEventPreToolUse, call.Name, call.ID); d.Block {
		return e.settleToolFeedback(toolFeedback{
			Corr:      corr,
			CallID:    call.ID,
			Output:    protocol.ToolFeedbackBlocked(d.BlockMessage()),
			IsError:   true,
			EmitEnd:   true,
			ErrorCode: protocol.ErrorCodeBlocked,
		})
	}

	// Hard permission deny: skip shell pre-hooks so they cannot run side effects
	// ahead of a deny (hooks must not widen or bypass hard denials). Execute
	// still performs Ask for deny audit/feedback.
	var pre tool.HookOutcome
	var err error
	if e.perms != nil && e.perms.Peek(tool.PermissionName(call.Name), "*") == permission.Deny {
		pre = tool.HookOutcome{Allow: true}
	} else {
		pre, err = e.runToolHooks(ctx, tool.HookEventPreToolUse, call, corr, "", false)
	}
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return e.canceledOrTimeoutToolResult(ctx, call.ID, corr, tool.Result{})
		}
		fail := classifyToolFailure(err)
		return e.settleToolFeedback(toolFeedback{
			Corr:      corr,
			CallID:    call.ID,
			Output:    fail.Output,
			IsError:   true,
			EmitEnd:   true,
			ErrorCode: fail.Code,
			Retryable: fail.Retryable,
		})
	}
	if !pre.Allow {
		return e.settleToolFeedback(toolFeedback{
			Corr:      corr,
			CallID:    call.ID,
			Output:    protocol.ToolFeedbackBlocked(pre.Inject),
			IsError:   true,
			EmitEnd:   true,
			ErrorCode: protocol.ErrorCodeBlocked,
		})
	}

	var res tool.Result
	// err already declared from pre-tool hooks; reuse for Execute outcome.
	err = nil
	t, ok := e.opts.Registry.Get(call.Name)
	if !ok {
		err = fmt.Errorf("unknown tool %q; available tools: %s", call.Name, e.toolNames())
	} else if e.toolLoop != nil && e.toolLoop.wouldTrip(call.Name, call.Args) {
		// Short-circuit: model re-issued a looping call — do not Execute again.
		tripped, reason, count := e.toolLoop.observe(call.Name, call.Args, false, protocol.ErrorCodeBlocked)
		if !tripped {
			// wouldTrip true implies observe will trip; force state.
			reason = toolLoopIdentical
			count = e.opts.ToolLoopThreshold
			e.toolLoop.tripped = true
			e.toolLoop.reason = reason
			e.toolLoop.toolName = call.Name
			e.toolLoop.count = count
		}
		e.noteToolLoop(corr, call.Name, reason, count)
		return e.settleToolFeedback(toolFeedback{
			Corr:      corr,
			CallID:    call.ID,
			Output:    protocol.ToolFeedbackBlocked(fmt.Sprintf("tool loop detected (%s) after %d identical failing calls; stopping turn", reason, count)),
			IsError:   true,
			EmitEnd:   true,
			ErrorCode: protocol.ErrorCodeBlocked,
			Retryable: false,
			Title:     call.Name,
		})
	} else {
		// Promote deferred tools called by name and elevate progressive
		// schemas when args need the advanced surface (mirrors toolsearch).
		if e.opts.Registry != nil {
			e.opts.Registry.NoteToolCall(call.Name, call.Args)
		}
		callID := call.ID
		tc := &tool.Context{
			WorkDir:         e.opts.WorkDir,
			SessionTempDir:  e.ensureSessionTemp(),
			SandboxMode:     e.opts.SandboxMode,
			Sandbox:         e.bashSandboxPolicy(),
			NetworkAllow:    e.opts.NetworkAllow,
			BashSecrets:     e.opts.BashSecrets,
			ContentGuard:    e.opts.ContentGuard,
			WebSearch:       e.opts.WebSearch,
			Scheduler:       e.opts.Scheduler,
			SchedulerPolicy: e.opts.SchedulerPolicy,
			SchedulerAcquire: func(ctx context.Context, label string, pools ...string) (*scheduler.Lease, error) {
				return e.acquireScheduler(ctx, corr, label, pools...)
			},
			Files:         e.files,
			SessionID:     e.opts.SessionID,
			RootSessionID: e.rootSessionID(),
			NotifyArtifact: func(op string, a artifact.Artifact) {
				e.emit(protocol.ArtifactUpdated{
					Correlation: corr,
					ID:          a.ID,
					Type:        a.Type,
					Version:     a.Version,
					Scope:       a.Scope,
					Title:       a.Title,
					Op:          op,
					SessionID:   a.SessionID,
				})
			},
			NotifyLedger: func(op string, entry ledger.Entry) {
				e.emit(protocol.LedgerUpdated{
					Correlation:   corr,
					ID:            entry.ID,
					Kind:          entry.Kind,
					Status:        entry.Status,
					Op:            op,
					Statement:     entry.Statement,
					Reason:        entry.InvalidateReason,
					Supersedes:    entry.Supersedes,
					SupersededBy:  entry.SupersededBy,
					AuthorSession: entry.AuthorSession,
					SessionID:     entry.AuthorSession,
				})
			},
			MemberName:          e.ownershipMemberName(),
			ContextBundle:       engineContextBundlePtr(e),
			Checkpoint:          e.checkpoints.Snapshot,
			CheckpointUncovered: e.checkpoints.MarkUncovered,
			TurnDiff:            e.turnDiff,
			// Record successful mutations only (post-write), not pre-mutation
			// snapshots — failed tools must not appear in handoff files_changed.
			FileSync: func(absPath string, content string, deleted bool) {
				e.noteMutatedPath(absPath)
				if e.opts.FileSync != nil {
					e.opts.FileSync(absPath, content, deleted)
				}
			},
			CollectDiagnostics: e.opts.CollectDiagnostics,
			Ask: func(ctx context.Context, req tool.AskRequest) error {
				return e.perms.AskWithCorrelation(ctx, req, corr)
			},
			AskUser: func(ctx context.Context, req tool.QuestionRequest) (tool.QuestionResponse, error) {
				prompts := make([]protocol.QuestionPrompt, len(req.Questions))
				for i, q := range req.Questions {
					opts := make([]protocol.QuestionOption, len(q.Options))
					for j, o := range q.Options {
						opts[j] = protocol.QuestionOption{Label: o.Label, Description: o.Description}
					}
					prompts[i] = protocol.QuestionPrompt{
						ID:       q.ID,
						Header:   q.Header,
						Question: q.Question,
						Options:  opts,
					}
				}
				answers, err := e.questions.Ask(ctx, corr, prompts)
				if err != nil {
					return tool.QuestionResponse{}, err
				}
				return tool.QuestionResponse{Answers: answers}, nil
			},
			SwitchAgent:    e.queueSwitchAgent,
			EnterPlanPhase: e.enterPlanPhase,
			StartWorkflow:  e.startWorkflow,
			StopWorkflow:   func() error { e.stopWorkflow(); return nil },
			AdvancePhase:   e.advancePhase,
			HandoffPlan:    e.handoffPlan,
			ReportOutput: func(data string) {
				if data == "" {
					return
				}
				e.emit(protocol.ToolCallOutput{
					Correlation: corr,
					CallID:      callID,
					Data:        secret.ScrubToolOutput(data),
				})
			},
			Process: tool.ProcessObserver{
				Started: func(id string, argv []string) {
					safeArgv := make([]string, len(argv))
					for i, a := range argv {
						safeArgv[i] = secret.Redact(a)
					}
					e.emit(protocol.ProcessStarted{
						Correlation: corr,
						ProcessID:   id,
						CallID:      callID,
						Argv:        safeArgv,
						Cwd:         e.opts.WorkDir,
					})
				},
				Output: func(id, stream, data string) {
					if data == "" {
						return
					}
					e.emit(protocol.ProcessOutput{
						Correlation: corr,
						ProcessID:   id,
						Stream:      stream,
						Data:        secret.ScrubToolOutput(data),
					})
				},
				Exited: func(id string, exitCode int, status tool.ProcessStatus) {
					e.emit(protocol.ProcessExited{
						Correlation: corr,
						ProcessID:   id,
						ExitCode:    exitCode,
						Status:      protocol.ProcessStatus(status),
					})
				},
			},
			RecordSessionPR: e.recordSessionPR(corr),
		}
		if e.opts.Depth < e.opts.MaxChildDepth {
			tc.SpawnTask = e.spawnChild
			tc.ResumeTask = e.resumeChild
			tc.TaskStatus = e.childStatus
			tc.TaskRead = e.childRead
			tc.TaskMessage = e.childMessage
			tc.TaskInterrupt = e.childInterrupt
			tc.Wait = e.childWait
		}
		// Team tools are available on lead and children (shared team).
		// Messaging is not stripped at depth ceiling (unlike nested task).
		if e.team != nil {
			tc.AgentRoster = e.agentRoster
			tc.AgentMessage = e.agentMessage
			tc.AgentBroadcast = e.agentBroadcast
			tc.AgentThread = e.agentThread
			tc.TeamTask = e.teamTask
			tc.PatchCollab = e.patchCollab
			tc.Delegate = e.delegate
			tc.Ownership = e.team.Ownership()
			tc.OnOverlap = e.emitPathOverlap
			tc.OwnershipQuery = e.ownershipQuery
			tc.OwnershipLease = e.ownershipLease
			tc.OwnershipReleaseLease = e.ownershipReleaseLease
		}
		tc.ChildWake = e.childWakeCh()
		tc.HasChildNotice = e.hasPendingChildNotices

		// Auto-retry only when policy says retry (safe-retry × transient/timeout).
		// Mutative/unsafe tools execute once — never blind double-apply.
		contract := tool.LookupContract(t)
		maxAttempts := e.opts.MaxToolRetryAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			res, err = t.Execute(ctx, call.Args, tc)
			if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return e.canceledOrTimeoutToolResult(ctx, call.ID, corr, res)
			}
			// Success path (no error and no stamped error code).
			if err == nil && res.ErrorCode == "" {
				break
			}
			// Classify without settling yet.
			_, isErr, fail := modelFacingToolOutput(res, err)
			code := fail.Code
			if res.ErrorCode == tool.ErrorCodeCanceled || res.ErrorCode == protocol.ErrorCodeCanceled {
				code = protocol.ErrorCodeCanceled
				isErr = true
			} else if res.ErrorCode == tool.ErrorCodeTimeout || res.ErrorCode == protocol.ErrorCodeTimeout {
				code = protocol.ErrorCodeTimeout
				isErr = true
			} else if res.ErrorCode != "" && code == "" {
				code = res.ErrorCode
				isErr = true
			}
			if !isErr {
				break
			}
			// User rejects / permission interactive: never auto-retry.
			if isUserTurnInterrupt(err) {
				break
			}
			decision := tool.DecideRetry(tool.ErrorCode(code), contract.Idempotency)
			if decision != tool.DecisionRetry || attempt >= maxAttempts {
				break
			}
			delay := e.toolRetryDelay(attempt + 1)
			e.emit(protocol.ToolRetrying{
				Correlation: corr,
				CallID:      call.ID,
				Name:        call.Name,
				NextAttempt: attempt + 1,
				DelayMs:     int(delay / time.Millisecond),
				ErrorCode:   code,
				Message:     fail.Output,
			})
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return e.canceledOrTimeoutToolResult(ctx, call.ID, corr, res)
				case <-timer.C:
				}
			}
			// Reset result for next attempt (avoid carrying partial metadata).
			res = tool.Result{}
		}
	}

	// Normalize cancellation/deadline after Execute (including permission-wait
	// cancel). Preserve partial output from the tool when present.
	// Tool-reported timeout/canceled without a dead ctx settles below so
	// post-tool hooks still observe the completed call.
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return e.canceledOrTimeoutToolResult(ctx, call.ID, corr, res)
	}

	output, isError, fail := modelFacingToolOutput(res, err)
	errCode := fail.Code
	errRetry := fail.Retryable
	// Result.ErrorCode (tool-reported cancel/timeout) wins when Execute succeeded
	// with a stamped code, or supplements when err was nil.
	if res.ErrorCode == tool.ErrorCodeCanceled || res.ErrorCode == protocol.ErrorCodeCanceled {
		output = protocol.ToolFeedbackCanceledPartial(output)
		isError = true
		errCode = protocol.ErrorCodeCanceled
		errRetry = false
	} else if res.ErrorCode == tool.ErrorCodeTimeout || res.ErrorCode == protocol.ErrorCodeTimeout {
		isError = true
		errCode = protocol.ErrorCodeTimeout
		errRetry = true
	} else if res.ErrorCode != "" && errCode == "" {
		isError = true
		errCode = res.ErrorCode
	}

	// Recovery path: no auto-retry, but attach a structured replan hint.
	if isError && errCode != "" {
		decision := tool.DecisionFail
		if ok {
			decision = tool.DecideRetry(tool.ErrorCode(errCode), tool.LookupContract(t).Idempotency)
		}
		if decision == tool.DecisionRecover {
			output = tool.AppendRecoveryHint(output, tool.ErrorCode(errCode), decision)
			errRetry = false
		} else if decision == tool.DecisionFail {
			// Policy may mark Retryable=false even when the code is nominally retryable
			// (e.g. transient on mutative tools) so the model does not re-issue blindly.
			if tool.LookupContract(t).Idempotency != tool.IdempotencySafeRetry {
				errRetry = false
			}
		}
	}

	if pre.Inject != "" {
		if output == "" {
			output = pre.Inject
		} else {
			output = output + "\n" + pre.Inject
		}
	}

	post, postErr := e.runToolHooks(ctx, tool.HookEventPostToolUse, call, corr, output, isError)
	if postErr != nil {
		if ctx.Err() != nil || errors.Is(postErr, context.Canceled) || errors.Is(postErr, context.DeadlineExceeded) {
			return e.canceledOrTimeoutToolResult(ctx, call.ID, corr, res)
		}
		// Post-hook infrastructure errors do not discard a successful tool result.
	} else if !post.Allow {
		isError = true
		errCode = protocol.ErrorCodeBlocked
		errRetry = false
		if post.Inject != "" {
			output = protocol.ToolFeedbackBlocked(post.Inject)
		} else {
			output = protocol.ToolFeedbackBlocked("")
		}
	} else if post.Inject != "" {
		if output == "" {
			output = post.Inject
		} else {
			output = output + "\n" + post.Inject
		}
	}

	// Declarative post rules observe the completed call (log/notify only).
	e.fireHookRules(corr, permission.HookEventPostToolUse, call.Name, call.ID)

	if isError && errCode == "" {
		errCode = protocol.ErrorCodeInternal
	}

	// Loop detector: identical failing tool+args or oscillating failures.
	if e.toolLoop != nil {
		tripped, reason, count := e.toolLoop.observe(call.Name, call.Args, !isError, errCode)
		if tripped {
			e.noteToolLoop(corr, call.Name, reason, count)
			if isError {
				output = tool.AppendRecoveryHint(
					output+"\n"+fmt.Sprintf("[loop: %s after %d calls; turn stopping]", reason, count),
					tool.CodeBlocked,
					tool.DecisionRecover,
				)
			}
		}
	}

	msg := e.settleToolFeedback(toolFeedback{
		Corr:      corr,
		CallID:    call.ID,
		Output:    output,
		IsError:   isError,
		ErrorCode: errCode,
		Retryable: errRetry,
		Title:     res.Title,
		Metadata:  res.Metadata,
		EmitEnd:   true,
	})
	// User reject/dismiss/decline: settle the tool with clear feedback, then
	// cancel the turn so the agent does not continue as if approved.
	if isUserTurnInterrupt(err) && e.turnCancel != nil {
		e.turnCancel()
	}
	return msg
}

// runToolHooks runs configured shell hooks for a tool lifecycle event.
// Trust is gated via permission "hook" (first-run ask by default).
func (e *Engine) runToolHooks(ctx context.Context, event string, call provider.ToolCall, corr protocol.Correlation, toolOutput string, isError bool) (tool.HookOutcome, error) {
	if len(e.opts.Hooks) == 0 {
		return tool.HookOutcome{Allow: true}, nil
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
		Subject:           call.Name,
		ToolName:          call.Name,
		ToolCallID:        call.ID,
		ToolInput:         call.Args,
		ToolOutput:        toolOutput,
		IsError:           isError,
	}
	return tool.RunHooks(ctx, e.opts.Hooks, event, payload, e.opts.WorkDir, func(ctx context.Context, command string) error {
		return e.perms.AskWithCorrelation(ctx, tool.AskRequest{
			Permission: "hook",
			Patterns:   []string{command},
			Always:     []string{command},
		}, corr)
	})
}

// recordSessionPR returns a tool callback that persists PR linkage and emits
// protocol.SessionMeta. Nil when neither persist nor emission is useful.
func (e *Engine) recordSessionPR(corr protocol.Correlation) func(tool.SessionPR) error {
	return func(pr tool.SessionPR) error {
		if pr.URL == "" {
			return nil
		}
		state := strings.ToLower(strings.TrimSpace(pr.State))
		if state == "" {
			state = "open"
		}
		meta := protocol.SessionMeta{
			Correlation: corr,
			PRURL:       pr.URL,
			PRNumber:    pr.Number,
			PRState:     state,
		}
		if e.opts.PersistSessionMeta != nil {
			if err := e.opts.PersistSessionMeta(meta); err != nil {
				return err
			}
		}
		e.emit(meta)
		return nil
	}
}

// canceledOrTimeoutToolResult settles a started tool that ended via cancel or
// deadline. Partial Output from the tool is preserved and marked incomplete;
// empty output uses the standard canceled/timeout feedback text.
//
// DeadlineExceeded on ctx wins over a tool-stamped canceled code: tools often
// observe ctx.Done() and report canceled generically, but the turn wall-clock
// must surface as timeout so operators can distinguish interrupt from expiry.
func (e *Engine) canceledOrTimeoutToolResult(ctx context.Context, callID string, corr protocol.Correlation, res tool.Result) provider.Message {
	code := res.ErrorCode
	switch {
	case ctx != nil && errors.Is(ctx.Err(), context.DeadlineExceeded):
		code = protocol.ErrorCodeTimeout
	case code == tool.ErrorCodeTimeout || code == protocol.ErrorCodeTimeout:
		code = protocol.ErrorCodeTimeout
	case code == tool.ErrorCodeCanceled || code == protocol.ErrorCodeCanceled:
		code = protocol.ErrorCodeCanceled
	case errors.Is(ctx.Err(), context.Canceled):
		code = protocol.ErrorCodeCanceled
	default:
		code = protocol.ErrorCodeCanceled
	}

	var output string
	switch code {
	case protocol.ErrorCodeTimeout:
		if strings.TrimSpace(res.Output) != "" && strings.TrimSpace(res.Output) != "(no output)" {
			output = res.Output
			if !strings.Contains(output, "timed out") {
				output = strings.TrimRight(output, "\n") + "\n" + protocol.ToolFeedbackTimeout("")
			}
		} else {
			output = protocol.ToolFeedbackTimeout("")
		}
	default:
		output = protocol.ToolFeedbackCanceledPartial(res.Output)
	}

	return e.settleToolFeedback(toolFeedback{
		Corr:      corr,
		CallID:    callID,
		Output:    output,
		IsError:   true,
		ErrorCode: code,
		Title:     res.Title,
		Metadata:  res.Metadata,
		EmitEnd:   true,
	})
}

func (e *Engine) failTurn(ctx context.Context, err error, corr protocol.Correlation, finishing chan struct{}) {
	if errors.Is(err, context.DeadlineExceeded) {
		e.emit(protocol.EngineError{
			Correlation: corr,
			Message:     "turn deadline exceeded",
			Code:        protocol.ErrorCodeTimeout,
		})
		e.completeTurn(ctx, finishing, corr, "timeout")
		return
	}
	if errors.Is(err, context.Canceled) {
		e.completeTurn(ctx, finishing, corr, "interrupted")
		return
	}
	if errors.Is(err, errToolLoopDetected) {
		reason := e.toolLoopStop
		if reason == "" {
			reason = toolLoopIdentical
		}
		e.emit(protocol.EngineError{
			Correlation: corr,
			Message:     "tool loop detected: " + reason,
			Code:        protocol.ErrorCodeBlocked,
		})
		e.completeTurn(ctx, finishing, corr, "loop_detected")
		return
	}
	e.emit(protocol.EngineError{Correlation: corr, Message: err.Error()})
	e.completeTurn(ctx, finishing, corr, "error")
}

// completeTurn closes finishing then emits the terminal TurnCompleted. Call
// only once per turn, after all history mutations and any preceding EngineError.
// Any remaining tool-queued agent switch is applied after TurnCompleted so
// Run's join on turnDone observes the new agent (belt-and-suspenders with the
// post-tool-batch apply in runTurn).
//
// When Options.Verify is set and the model claimed a successful completion
// (stopReason end_turn), independent gates run before TurnCompleted so the
// report attaches on the same terminal event (claim ≠ verified). Gates honor
// ctx so cancelAndJoinTurn / turn timeout can abort a hung cmd gate.
func (e *Engine) completeTurn(ctx context.Context, finishing chan struct{}, corr protocol.Correlation, stopReason string) {
	close(finishing)
	files := turnFileChanges(e.turnDiff.Snapshot())
	e.checkpoints.CommitTurn()
	peek := e.checkpoints.Peek()
	e.fireLifecycle(corr, permission.HookEventTurnEnd, "", "", "end", "")
	// Clear tool-chain state on every terminal path (end, interrupt, error).
	if e.perms != nil {
		e.perms.EndTurn()
	}
	var verification *protocol.VerificationReport
	if stopReason == "end_turn" && len(e.opts.Verify) > 0 {
		verification = e.runSoloVerification(ctx, corr)
	}
	e.emit(protocol.TurnCompleted{
		Correlation:       corr,
		StopReason:        stopReason,
		Files:             files,
		CheckpointSkipped: peek.Skipped,
		Uncovered:         peek.Uncovered,
		Verification:      verification,
	})
	e.applyPendingAgent()
}

// turnFileChanges maps tool.FileChange → protocol.TurnFileChange.
func turnFileChanges(in []tool.FileChange) []protocol.TurnFileChange {
	if len(in) == 0 {
		return nil
	}
	out := make([]protocol.TurnFileChange, len(in))
	for i, c := range in {
		out[i] = protocol.TurnFileChange{Path: c.Path, Kind: string(c.Kind)}
	}
	return out
}

// fireHookRules evaluates declarative config rules and emits HookMatched for
// each log/notify/block hit. Returns the decision so pre_tool_use can block
// before shell hooks and Execute. callID is optional tool correlation.
func (e *Engine) fireHookRules(corr protocol.Correlation, event, subject, callID string) permission.HookDecision {
	d := permission.EvaluateHooks(e.opts.HookRules, event, subject)
	if d.Block && strings.TrimSpace(d.BlockHit.Message) == "" {
		d.BlockHit.Message = permission.DefaultBlockMessage(event, d.BlockHit.Matcher, subject)
	}
	emitHit := func(hit permission.HookHit) {
		e.emit(protocol.HookMatched{
			Correlation: corr,
			Event:       hit.Event,
			Action:      hit.Action,
			Matcher:     hit.Matcher,
			Tool:        hit.Tool,
			Message:     hit.Message,
			CallID:      callID,
		})
	}
	for _, hit := range d.Log {
		emitHit(hit)
	}
	for _, hit := range d.Notify {
		emitHit(hit)
	}
	if d.Block {
		emitHit(d.BlockHit)
	}
	return d
}

// emitUsage translates provider.Usage into a protocol.UsageReported event.
// A nil usage means the vendor did not report counts — emit nothing (unknown).
//
// used = InputTokens + CacheReadTokens + CacheCreationTokens + OutputTokens;
// if all those are 0 but TotalTokens > 0, used = TotalTokens and input/output/
// cache stay unknown (a total alone is not a measured zero on the parts).
//
// Also accumulates session cost / per-turn token envelopes and emits
// SessionBudgetWarning at 50/80/100% (#577). Hard stop is armed here; the
// turn loop ends the turn with a renderable EngineError (not a silent halt).
func (e *Engine) emitUsage(corr protocol.Correlation, u *provider.Usage) {
	if u == nil {
		return
	}
	used := u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens + u.OutputTokens
	input := protocol.KnownTokens(u.InputTokens)
	output := protocol.KnownTokens(u.OutputTokens)
	cacheRead := protocol.KnownTokens(u.CacheReadTokens)
	cacheCreation := protocol.KnownTokens(u.CacheCreationTokens)
	if used == 0 && u.TotalTokens > 0 {
		used = u.TotalTokens
		input = protocol.UnknownTokens()
		output = protocol.UnknownTokens()
		cacheRead = protocol.UnknownTokens()
		cacheCreation = protocol.UnknownTokens()
	}
	source := protocol.UsageSourceActual
	if u.Estimated {
		source = protocol.UsageSourceEstimated
	}
	e.lastUsed = used
	e.lastUsedKnown = true
	e.emit(protocol.UsageReported{
		Correlation:   corr,
		Input:         input,
		Output:        output,
		CacheRead:     cacheRead,
		CacheCreation: cacheCreation,
		Used:          protocol.KnownTokens(used),
		Source:        source,
	})
	e.noteSessionBudgets(corr, *u, used)
}

// noteSessionBudgets updates shared session cost and per-turn token ceilings.
func (e *Engine) noteSessionBudgets(corr protocol.Correlation, u provider.Usage, usedTokens int) {
	if e == nil {
		return
	}
	// Session cost envelope (shared root + children).
	if e.sessionBudget != nil {
		note := e.sessionBudget.noteUsage(e.provName, e.model, u)
		for _, level := range note.Warnings {
			atCap := level == protocol.SessionBudgetLevel100
			msg := sessionBudgetCostMessage(level, note.CostUSD, note.MaxCostUSD, atCap)
			e.emit(protocol.SessionBudgetWarning{
				Correlation: corr,
				Level:       level,
				Kind:        protocol.SessionBudgetKindCostUSD,
				Exhausted:   atCap,
				CostUSD:     note.CostUSD,
				MaxCostUSD:  note.MaxCostUSD,
				Ratio:       safeRatio(note.CostUSD, note.MaxCostUSD),
				Message:     msg,
			})
		}
		if note.Exhausted {
			msg := sessionBudgetCostMessage(protocol.SessionBudgetLevel100, note.CostUSD, note.MaxCostUSD, true)
			e.armSessionBudgetStop(protocol.SessionBudgetKindCostUSD, msg)
		}
	}
	// Per-turn token ceiling (this engine only).
	if e.turnTokens != nil && usedTokens > 0 {
		tn := e.turnTokens.note(usedTokens)
		for _, level := range tn.Warnings {
			atCap := level == protocol.SessionBudgetLevel100
			msg := sessionBudgetTurnMessage(level, tn.Used, tn.Max, atCap)
			e.emit(protocol.SessionBudgetWarning{
				Correlation: corr,
				Level:       level,
				Kind:        protocol.SessionBudgetKindTurnTokens,
				Exhausted:   atCap,
				TokensUsed:  tn.Used,
				MaxTokens:   tn.Max,
				Ratio:       safeRatio(float64(tn.Used), float64(tn.Max)),
				Message:     msg,
			})
		}
		if tn.Exhausted {
			msg := sessionBudgetTurnMessage(protocol.SessionBudgetLevel100, tn.Used, tn.Max, true)
			e.armSessionBudgetStop(protocol.SessionBudgetKindTurnTokens, msg)
		}
	}
}

func safeRatio(used, max float64) float64 {
	if max <= 0 {
		return 0
	}
	return used / max
}

func (e *Engine) armSessionBudgetStop(kind, msg string) {
	if e == nil {
		return
	}
	e.sessionBudgetStop = true
	if e.sessionBudgetStopKind == "" {
		e.sessionBudgetStopKind = kind
	}
	if e.sessionBudgetStopMsg == "" {
		e.sessionBudgetStopMsg = msg
	}
}

// finishSessionBudgetStop emits EngineError + TurnCompleted for a hard envelope stop.
func (e *Engine) finishSessionBudgetStop(ctx context.Context, finishing chan struct{}, corr protocol.Correlation) {
	msg := e.sessionBudgetStopMsg
	if msg == "" {
		msg = "session budget exhausted"
	}
	e.emit(protocol.EngineError{
		Correlation: corr,
		Message:     msg,
		Code:        protocol.ErrorCodeBudgetExhausted,
	})
	e.completeTurn(ctx, finishing, corr, "budget_exhausted")
}

// rejectUserInputIfBudgetExhausted blocks new turns when the shared session
// cost tracker has crossed its ceiling. The Options.SessionBudgetExhausted hook
// alone still only gates delegation fan-out (policy), not idle UserInput.
// Returns true when the input was rejected. EngineError is emitted once per
// reject; SessionBudgetWarning is not re-fired (already emitted at trip).
func (e *Engine) rejectUserInputIfBudgetExhausted() bool {
	if e == nil || e.sessionBudget == nil || !e.sessionBudget.Exhausted() {
		return false
	}
	cost, _ := e.sessionBudget.CostUSD()
	max := e.sessionBudget.MaxCostUSD()
	msg := sessionBudgetCostMessage(protocol.SessionBudgetLevel100, cost, max, true)
	e.emit(protocol.EngineError{
		Correlation: e.sessionCorr(),
		Message:     msg,
		Code:        protocol.ErrorCodeBudgetExhausted,
	})
	return true
}

func (e *Engine) toolNames() string {
	schemas, _ := e.effectiveToolSchemas()
	var names []string
	for _, s := range schemas {
		names = append(names, s.Name)
	}
	return strings.Join(names, ", ")
}

// bashSandboxPolicy compiles the live permission layers into an OS sandbox
// Policy for bash (write denials, network posture, plan hard-denies).
// Attaches config network.allow for /sandbox explain and bash preflight
// (shared with webfetch). Bash OS network stays all-or-nothing via NetworkEnabled;
// per-host filtering is application-layer preflight only.
func (e *Engine) bashSandboxPolicy() sandbox.Policy {
	mode := sandbox.ResolveMode(e.opts.SandboxMode)
	var p sandbox.Policy
	if e.perms == nil {
		// No permission service: host networking on (Policy.NoNetwork zero value).
		p = sandbox.Policy{Mode: mode, WorkDir: e.opts.WorkDir}
	} else {
		p = e.perms.CompileSandbox(mode, e.opts.WorkDir)
	}
	p.NetworkAllow = sandbox.CloneNetworkAllow(e.opts.NetworkAllow)
	p.AllowDegrade = e.opts.SandboxAllowDegrade
	return p
}
