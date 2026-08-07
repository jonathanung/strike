package engine

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
)

// resolveHarness maps a task subagent definition to its implementation. Empty
// and "default" keep control in the built-in child loop.
func (e *Engine) resolveHarness(agent Agent) (harness.Func, string, error) {
	name := agent.Harness
	if name == "" || name == "default" {
		return nil, "", nil
	}
	if e.opts.HarnessRegistry == nil {
		return nil, "", fmt.Errorf("agent %q specifies harness %q but no harness registry is configured", agent.Name, name)
	}
	fn, err := e.opts.HarnessRegistry.Resolve(name)
	if err != nil {
		return nil, "", fmt.Errorf("agent %q: %w", agent.Name, err)
	}
	return fn, name, nil
}

// harnessEnvironment creates the provider object, tools broker, and progress
// callback passed to one complete harness function call.
func (e *Engine) harnessEnvironment(ctx context.Context, corr protocol.Correlation, name string) (harness.Input, harness.Provider, harness.Emit) {
	var callbackMu sync.Mutex
	// Serialize tool execution: execToolCall shares engine state with the
	// built-in sequential tool loop and is not safe for overlapping runs.
	var toolMu sync.Mutex
	emit := func(payload json.RawMessage) {
		e.emit(protocol.HarnessProgress{
			Correlation: corr,
			Name:        name,
			Payload:     payload,
		})
	}
	providerCall := func(req provider.Request) (harness.ModelResponse, error) {
		req.Model = e.model
		req.Priority = e.priority
		maxAttempts := e.opts.MaxStreamAttempts
		if maxAttempts < 1 {
			maxAttempts = 1
		}
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			if ctx.Err() != nil {
				return harness.ModelResponse{}, ctx.Err()
			}
			requestCorr := corr
			requestCorr.ProviderRequestID = rand.Text()
			requestCorr.Attempt = attempt
			stream, err := e.admitModelStream(ctx, requestCorr, req)
			if err == nil {
				response := harness.ModelResponse{}
				var text strings.Builder
				for ev := range stream {
					switch ev.Type {
					case provider.EventTextDelta:
						text.WriteString(ev.Text)
					case provider.EventReasoning:
						response.Reasoning = append(response.Reasoning, ev.Reasoning)
					case provider.EventToolCall:
						if ev.ToolCall != nil {
							response.Calls = append(response.Calls, *ev.ToolCall)
						}
					case provider.EventDone:
						response.Text = text.String()
						response.StopReason = ev.StopReason
						response.Usage = ev.Usage
						callbackMu.Lock()
						e.emitUsage(requestCorr, ev.Usage)
						callbackMu.Unlock()
						return response, nil
					case provider.EventError:
						err = ev.Err
					}
				}
				if err == nil {
					err = provider.ErrIncompleteStream
				}
			}
			if attempt == maxAttempts || !provider.IsRetryable(err) {
				return harness.ModelResponse{}, err
			}
			delay, _ := e.streamRetryDelayFor(err, attempt+1)
			if delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return harness.ModelResponse{}, ctx.Err()
				case <-timer.C:
				}
			}
		}
		return harness.ModelResponse{}, provider.ErrIncompleteStream
	}

	executeTool := func(call provider.ToolCall) (provider.ToolResult, error) {
		if err := validateHarnessToolCall(call); err != nil {
			id := call.ID
			if id == "" {
				id = rand.Text()
			}
			return provider.ToolResult{
				CallID:    id,
				Output:    err.Error(),
				IsError:   true,
				ErrorCode: protocol.ErrorCodeInvalidArgs,
			}, nil
		}
		if call.ID == "" {
			call.ID = rand.Text()
		}
		if call.Args == nil {
			call.Args = json.RawMessage(`{}`)
		}
		toolMu.Lock()
		defer toolMu.Unlock()
		// Same path as the built-in loop: permissions, hooks, sandbox, events.
		// Result is returned to the harness only — not appended to e.messages.
		msg := e.execToolCall(ctx, call, corr)
		if msg.ToolResult == nil {
			return provider.ToolResult{
				CallID:    call.ID,
				Output:    "tool execution produced no result",
				IsError:   true,
				ErrorCode: protocol.ErrorCodeInternal,
			}, nil
		}
		return *msg.ToolResult, nil
	}

	input := harness.Input{
		Context: ctx,
		Request: provider.Request{
			Model: e.model, System: joinPromptLayerTexts(e.systemLayers()),
			Messages:  append([]provider.Message(nil), e.messages...),
			Tools:     func() []provider.ToolSchema { tools, _ := e.effectiveToolSchemas(); return tools }(),
			MaxTokens: e.opts.MaxTokens, Effort: providerEffort(e.effort), Priority: e.priority,
		},
		Tools: harness.Tools{Execute: executeTool},
	}
	return input, harness.Provider{Call: providerCall}, emit
}

// validateHarnessToolCall rejects malformed brokered tool requests before they
// enter execToolCall. Empty ID is allowed (host assigns one).
func validateHarnessToolCall(call provider.ToolCall) error {
	name := strings.TrimSpace(call.Name)
	if name == "" {
		return fmt.Errorf("tool name is required")
	}
	if call.Name != name {
		return fmt.Errorf("tool name %q has leading or trailing whitespace", call.Name)
	}
	if len(call.Args) > 0 && !json.Valid(call.Args) {
		return fmt.Errorf("tool arguments are not valid JSON")
	}
	return nil
}
