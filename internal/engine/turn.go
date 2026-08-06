package engine

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/sandbox"
	"github.com/jonathanung/strike-cli/internal/scheduler"
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

// enqueueUserInput buffers input for FIFO start after the active turn ends.
// Empty/whitespace-only text with no images is ignored. Queue survives Interrupt.
func (e *Engine) enqueueUserInput(op protocol.UserInput) {
	if strings.TrimSpace(op.Text) == "" && len(op.Images) == 0 {
		return
	}
	if len(e.pendingUserInputs) >= maxPendingUserInputs {
		e.emit(protocol.EngineError{
			Correlation: e.sessionCorr(),
			Message:     "input queue full; wait for the current turn to finish",
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
	turnCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	finishing := make(chan struct{})
	e.turnCancel = cancel
	e.turnDone = done
	e.turnFinishing = finishing
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
	e.emit(protocol.UserMessage{Correlation: turnCorr, Text: text, Images: images})
	e.maybeTitleSession(text)
	e.emit(protocol.TurnStarted{Correlation: turnCorr})
	e.fireHookRules(turnCorr, permission.HookEventTurnStart, "", "")
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
			e.failTurn(err, turnCorr, finishing)
			return
		}
		if len(result.Calls) != 0 || result.StopReason == "tool_use" {
			e.failTurn(errors.New("task harness returned tool calls that it cannot execute"), turnCorr, finishing)
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
		e.completeTurn(finishing, turnCorr, result.StopReason)
		return
	}

	for {
		// Deliver child.completed and peer mailbox messages into model history
		// before each Stream (tool-round boundary). Never mid-tool-call.
		e.injectPendingChildNotices()
		e.injectPendingMailbox()
		e.maybePruneToolResults()
		e.maybeThresholdCompact(ctx, turnID)
		outcome, reqCorr, err := e.streamModel(ctx, turnID)
		if err != nil {
			e.failTurn(err, reqCorr, finishing)
			return
		}

		e.messages = append(e.messages, provider.Message{
			Role:      provider.RoleAssistant,
			Text:      outcome.text,
			ToolCalls: outcome.calls,
			Reasoning: outcome.reasoning,
		})
		if len(outcome.calls) == 0 {
			e.completeTurn(finishing, reqCorr, outcome.stopReason)
			return
		}
		for i, call := range outcome.calls {
			// Unstarted calls: history-only synthetic results, no begin/end/Execute.
			if ctx.Err() != nil {
				e.appendUnstartedToolResults(outcome.calls[i:])
				e.failTurn(ctx.Err(), reqCorr, finishing)
				return
			}
			e.messages = append(e.messages, e.execToolCall(ctx, call, reqCorr))
			if ctx.Err() != nil {
				// Current call was started (and canceled); remaining are unstarted.
				e.appendUnstartedToolResults(outcome.calls[i+1:])
				e.failTurn(ctx.Err(), reqCorr, finishing)
				return
			}
		}
		// Apply tool-queued agent switch so the next Stream uses the new
		// agent system prompt (not only after TurnCompleted).
		e.applyPendingAgent()
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
		delay := e.streamRetryDelay(attempt + 1)
		e.emit(protocol.ProviderRetrying{
			Correlation: reqCorr,
			NextAttempt: attempt + 1,
			DelayMs:     int(delay / time.Millisecond),
			Message:     err.Error(),
		})
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

func (e *Engine) streamRetryDelay(nextAttempt int) time.Duration {
	if e.opts.StreamRetryBackoff != nil {
		return e.opts.StreamRetryBackoff(nextAttempt)
	}
	// 200ms, 400ms, 800ms… capped at 2s.
	shift := nextAttempt - 2
	if shift < 0 {
		shift = 0
	}
	d := 200 * time.Millisecond << shift
	if d > 2*time.Second {
		d = 2 * time.Second
	}
	return d
}

// consumeStream runs one Provider.Stream attempt and applies the terminal
// contract. On success, history-ready text/tool/reasoning are returned and
// usage is emitted; nothing is appended to e.messages here.
func (e *Engine) consumeStream(ctx context.Context, reqCorr protocol.Correlation) (streamOutcome, error) {
	layers := e.systemLayers()
	system := joinPromptLayerTexts(layers)
	tools, _ := e.effectiveToolSchemas()
	e.recordStreamEffective(layers, system, tools)
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
			CallID:  call.ID,
			Output:  unstartedToolOutput,
			IsError: true,
		}))
	}
}

// toolFeedback is the uniform settlement for one tool call: optional
// ToolCallEnd for the frontend plus a RoleTool message for the model.
// EmitEnd is false for unstarted calls (history-only synthetic results).
type toolFeedback struct {
	Corr     protocol.Correlation
	CallID   string
	Output   string
	IsError  bool
	Title    string
	Metadata json.RawMessage
	EmitEnd  bool
}

// settleToolFeedback is the formal tool-result feedback path: one place that
// pairs model history with (when EmitEnd) a ToolCallEnd event. Permission
// denials, user rejects, hook blocks, interrupts, and ordinary results all
// settle here so future phase bounces and hook messages share the same shape.
func (e *Engine) settleToolFeedback(fb toolFeedback) provider.Message {
	if fb.EmitEnd {
		e.emit(protocol.ToolCallEnd{
			Correlation: fb.Corr,
			CallID:      fb.CallID,
			Title:       fb.Title,
			Output:      fb.Output,
			IsError:     fb.IsError,
			Metadata:    fb.Metadata,
		})
	}
	return provider.Message{
		Role:       provider.RoleTool,
		ToolResult: &provider.ToolResult{CallID: fb.CallID, Output: fb.Output, IsError: fb.IsError},
	}
}

// modelFacingToolOutput maps Execute errors onto protocol.ToolFeedback* text.
// Success returns the tool's own output unchanged.
func modelFacingToolOutput(res tool.Result, err error) (output string, isError bool) {
	if err == nil {
		return res.Output, false
	}
	var permDenied *permission.DeniedError
	var permRejected *permission.RejectedError
	var qRejected *question.RejectedError
	var toolRejected *tool.UserRejectedError
	switch {
	case errors.As(err, &permDenied):
		return permDenied.Error(), true
	case errors.As(err, &permRejected):
		return permRejected.Error(), true
	case errors.As(err, &qRejected):
		return qRejected.Error(), true
	case errors.As(err, &toolRejected):
		return protocol.ToolFeedbackUserRejected(toolRejected.Message), true
	default:
		return protocol.ToolFeedbackError(err.Error()), true
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
	begin := protocol.ToolCallBegin{
		Correlation: corr,
		CallID:      call.ID,
		Name:        call.Name,
		Args:        call.Args,
	}
	// Ask Run to emit begin so Interrupt can be applied while Events is full.
	result := make(chan beginAck, 1)
	select {
	case e.beginReqs <- beginReq{begin: begin, result: result}:
	case <-ctx.Done():
		// Canceled before Run accepted the begin request — unstarted.
		return e.settleToolFeedback(toolFeedback{
			CallID:  call.ID,
			Output:  unstartedToolOutput,
			IsError: true,
		})
	}
	ack := <-result
	if !ack.emitted {
		return e.settleToolFeedback(toolFeedback{
			CallID:  call.ID,
			Output:  unstartedToolOutput,
			IsError: true,
		})
	}
	// Begin was emitted. Pre-Execute cancel/shutdown check (no Execute).
	if ctx.Err() != nil {
		return e.canceledToolResult(call.ID, corr)
	}

	// Declarative rules first (cheap, no process). Block skips shell + Execute.
	if d := e.fireHookRules(corr, permission.HookEventPreToolUse, call.Name, call.ID); d.Block {
		return e.settleToolFeedback(toolFeedback{
			Corr:    corr,
			CallID:  call.ID,
			Output:  protocol.ToolFeedbackBlocked(d.BlockMessage()),
			IsError: true,
			EmitEnd: true,
		})
	}

	pre, err := e.runToolHooks(ctx, tool.HookEventPreToolUse, call, corr, "", false)
	if err != nil {
		if ctx.Err() != nil || errors.Is(err, context.Canceled) {
			return e.canceledToolResult(call.ID, corr)
		}
		return e.settleToolFeedback(toolFeedback{
			Corr:    corr,
			CallID:  call.ID,
			Output:  protocol.ToolFeedbackError(err.Error()),
			IsError: true,
			EmitEnd: true,
		})
	}
	if !pre.Allow {
		return e.settleToolFeedback(toolFeedback{
			Corr:    corr,
			CallID:  call.ID,
			Output:  protocol.ToolFeedbackBlocked(pre.Inject),
			IsError: true,
			EmitEnd: true,
		})
	}

	var res tool.Result
	t, ok := e.opts.Registry.Get(call.Name)
	if !ok {
		err = fmt.Errorf("unknown tool %q; available tools: %s", call.Name, e.toolNames())
	} else {
		// Promote deferred tools called by name so subsequent streams include
		// their schemas (mirrors toolsearch discovery).
		if e.opts.Registry != nil {
			e.opts.Registry.Discover(call.Name)
		}
		callID := call.ID
		tc := &tool.Context{
			WorkDir:         e.opts.WorkDir,
			SandboxMode:     e.opts.SandboxMode,
			Sandbox:         e.bashSandboxPolicy(),
			NetworkAllow:    e.opts.NetworkAllow,
			Scheduler:       e.opts.Scheduler,
			SchedulerPolicy: e.opts.SchedulerPolicy,
			SchedulerAcquire: func(ctx context.Context, label string, pools ...string) (*scheduler.Lease, error) {
				return e.acquireScheduler(ctx, corr, label, pools...)
			},
			Files:      e.files,
			Checkpoint: e.checkpoints.Snapshot,
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
			AdvancePhase:   e.advancePhase,
			ReportOutput: func(data string) {
				if data == "" {
					return
				}
				e.emit(protocol.ToolCallOutput{
					Correlation: corr,
					CallID:      callID,
					Data:        data,
				})
			},
			Process: tool.ProcessObserver{
				Started: func(id string, argv []string) {
					e.emit(protocol.ProcessStarted{
						Correlation: corr,
						ProcessID:   id,
						CallID:      callID,
						Argv:        argv,
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
						Data:        data,
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
			tc.TaskStatus = e.childStatus
			tc.TaskRead = e.childRead
			tc.TaskMessage = e.childMessage
			tc.TaskInterrupt = e.childInterrupt
		}
		// Team tools are available on lead and children (shared team).
		// Messaging is not stripped at depth ceiling (unlike nested task).
		if e.team != nil {
			tc.AgentRoster = e.agentRoster
			tc.AgentMessage = e.agentMessage
			tc.AgentBroadcast = e.agentBroadcast
			tc.TeamTask = e.teamTask
		}
		tc.ChildWake = e.childWakeCh()
		tc.HasChildNotice = e.hasPendingChildNotices
		res, err = t.Execute(ctx, call.Args, tc)
	}

	// Normalize cancellation after Execute (including permission-wait cancel).
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return e.canceledToolResult(call.ID, corr)
	}

	output, isError := modelFacingToolOutput(res, err)
	if pre.Inject != "" {
		if output == "" {
			output = pre.Inject
		} else {
			output = output + "\n" + pre.Inject
		}
	}

	post, postErr := e.runToolHooks(ctx, tool.HookEventPostToolUse, call, corr, output, isError)
	if postErr != nil {
		if ctx.Err() != nil || errors.Is(postErr, context.Canceled) {
			return e.canceledToolResult(call.ID, corr)
		}
		// Post-hook infrastructure errors do not discard a successful tool result.
	} else if !post.Allow {
		isError = true
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

	msg := e.settleToolFeedback(toolFeedback{
		Corr:     corr,
		CallID:   call.ID,
		Output:   output,
		IsError:  isError,
		Title:    res.Title,
		Metadata: res.Metadata,
		EmitEnd:  true,
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
		Event:      event,
		SessionID:  e.opts.SessionID,
		CWD:        e.opts.WorkDir,
		ToolName:   call.Name,
		ToolCallID: call.ID,
		ToolInput:  call.Args,
		ToolOutput: toolOutput,
		IsError:    isError,
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

func (e *Engine) canceledToolResult(callID string, corr protocol.Correlation) provider.Message {
	return e.settleToolFeedback(toolFeedback{
		Corr:    corr,
		CallID:  callID,
		Output:  canceledToolOutput,
		IsError: true,
		EmitEnd: true,
	})
}

func (e *Engine) failTurn(err error, corr protocol.Correlation, finishing chan struct{}) {
	if errors.Is(err, context.Canceled) {
		e.completeTurn(finishing, corr, "interrupted")
		return
	}
	e.emit(protocol.EngineError{Correlation: corr, Message: err.Error()})
	e.completeTurn(finishing, corr, "error")
}

// completeTurn closes finishing then emits the terminal TurnCompleted. Call
// only once per turn, after all history mutations and any preceding EngineError.
// Any remaining tool-queued agent switch is applied after TurnCompleted so
// Run's join on turnDone observes the new agent (belt-and-suspenders with the
// post-tool-batch apply in runTurn).
func (e *Engine) completeTurn(finishing chan struct{}, corr protocol.Correlation, stopReason string) {
	close(finishing)
	e.checkpoints.CommitTurn()
	e.fireHookRules(corr, permission.HookEventTurnEnd, "", "")
	e.emit(protocol.TurnCompleted{Correlation: corr, StopReason: stopReason})
	e.applyPendingAgent()
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
// Attaches config network.allow for /sandbox explain (webfetch enforces it
// separately; bash OS network stays all-or-nothing via Policy.Network).
func (e *Engine) bashSandboxPolicy() sandbox.Policy {
	mode := sandbox.ResolveMode(e.opts.SandboxMode)
	var p sandbox.Policy
	if e.perms == nil {
		// No permission service: keep host networking (same as Defaults Ask).
		p = sandbox.Policy{Mode: mode, WorkDir: e.opts.WorkDir, Network: true}
	} else {
		p = e.perms.CompileSandbox(mode, e.opts.WorkDir)
	}
	p.NetworkAllow = sandbox.CloneNetworkAllow(e.opts.NetworkAllow)
	return p
}
