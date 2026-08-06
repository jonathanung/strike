// Package external adapts any subprocess implementing the version 1 JSONL
// transport into a harness.Func. JavaScript and Lean harnesses use this same
// language-neutral path and are not linked into the Strike binary.
package external

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/provider"
)

const (
	maxLineBytes   = 1 << 20
	maxOutputBytes = 16 << 20
	cancelGrace    = 250 * time.Millisecond
)

type runner struct {
	name    string
	adapter Adapter
}

// New runs the language-neutral harness protocol over an adapter's pipe.
func New(name string, adapter Adapter) (harness.Func, error) {
	if strings.TrimSpace(name) == "" || adapter == nil {
		return nil, errors.New("external harness: name and adapter are required")
	}
	r := &runner{name: name, adapter: adapter}
	return r.run, nil
}

type envelope struct {
	Version      int               `json:"version"`
	Type         string            `json:"type"`
	InvocationID string            `json:"invocationId"`
	CallID       string            `json:"callId,omitempty"`
	Request      *wireRequest      `json:"request,omitempty"`
	Payload      json.RawMessage   `json:"payload,omitempty"`
	Message      json.RawMessage   `json:"message,omitempty"`
	Code         string            `json:"code,omitempty"`
	ErrorText    string            `json:"error,omitempty"`
	Text         string            `json:"text,omitempty"`
	Reasoning    []json.RawMessage `json:"reasoning,omitempty"`
	ToolCalls    []wireToolCall    `json:"toolCalls,omitempty"`
	StopReason   string            `json:"stopReason,omitempty"`
}

type wireRequest struct {
	Model     string           `json:"model"`
	System    string           `json:"system,omitempty"`
	Messages  []wireMessage    `json:"messages"`
	Tools     []wireToolSchema `json:"tools,omitempty"`
	MaxTokens int              `json:"maxOutputTokens,omitempty"`
	Effort    provider.Effort  `json:"effort,omitempty"`
	Priority  bool             `json:"priority,omitempty"`
}

type wireMessage struct {
	Role       provider.Role        `json:"role"`
	Text       string               `json:"text,omitempty"`
	Images     []provider.Image     `json:"images,omitempty"`
	ToolCalls  []wireToolCall       `json:"toolCalls,omitempty"`
	ToolResult *provider.ToolResult `json:"toolResult,omitempty"`
	Reasoning  []json.RawMessage    `json:"reasoning,omitempty"`
}

type wireToolCall struct {
	ID   string          `json:"id"`
	Name string          `json:"name"`
	Args json.RawMessage `json:"arguments"`
}

type wireToolSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
}

func toWire(r provider.Request) wireRequest {
	w := wireRequest{Model: r.Model, System: r.System, MaxTokens: r.MaxTokens, Effort: r.Effort, Priority: r.Priority}
	for _, m := range r.Messages {
		wm := wireMessage{Role: m.Role, Text: m.Text, Images: m.Images, ToolResult: m.ToolResult, Reasoning: m.Reasoning}
		for _, c := range m.ToolCalls {
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{c.ID, c.Name, c.Args})
		}
		w.Messages = append(w.Messages, wm)
	}
	for _, s := range r.Tools {
		w.Tools = append(w.Tools, wireToolSchema{s.Name, s.Description, s.InputSchema})
	}
	return w
}

func (w wireRequest) providerRequest() provider.Request {
	r := provider.Request{Model: w.Model, System: w.System, MaxTokens: w.MaxTokens, Effort: w.Effort, Priority: w.Priority}
	for _, m := range w.Messages {
		pm := provider.Message{Role: m.Role, Text: m.Text, Images: m.Images, ToolResult: m.ToolResult, Reasoning: m.Reasoning}
		for _, c := range m.ToolCalls {
			pm.ToolCalls = append(pm.ToolCalls, provider.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
		}
		r.Messages = append(r.Messages, pm)
	}
	for _, s := range w.Tools {
		r.Tools = append(r.Tools, provider.ToolSchema{Name: s.Name, Description: s.Description, InputSchema: s.InputSchema})
	}
	return r
}

func (e *runner) run(input harness.Input, p harness.Provider, emit harness.Emit) (harness.Result, error) {
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	invocationID := rand.Text()
	pipe, err := e.adapter.Start(ctx)
	if err != nil {
		return harness.Result{}, fmt.Errorf("start external harness %q: %w", e.name, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- pipe.Wait() }()

	var writeMu sync.Mutex
	write := func(v any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		if len(b) > maxLineBytes {
			return errors.New("external harness: outbound line exceeds limit")
		}
		_, err = pipe.Write(append(b, '\n'))
		return err
	}
	start := struct {
		Version      int         `json:"version"`
		Type         string      `json:"type"`
		InvocationID string      `json:"invocationId"`
		Request      wireRequest `json:"request"`
		Capabilities []string    `json:"capabilities"`
	}{1, "harness.start", invocationID, toWire(input.Request), []string{"provider.call", "progress.emit", "harness.cancel"}}
	if err := write(start); err != nil {
		_ = pipe.Kill()
		<-processDone
		return harness.Result{}, err
	}

	type terminal struct {
		result harness.Result
		err    error
	}
	done := make(chan terminal, 1)
	go func() {
		s := bufio.NewScanner(pipe)
		s.Buffer(make([]byte, 64*1024), maxLineBytes)
		total := 0
		ids := make(map[string]string)
		for s.Scan() {
			total += len(s.Bytes()) + 1
			if total > maxOutputBytes {
				done <- terminal{err: errors.New("external harness: output exceeds limit")}
				return
			}
			var m envelope
			if err := json.Unmarshal(s.Bytes(), &m); err != nil {
				done <- terminal{err: fmt.Errorf("external harness: malformed JSON: %w", err)}
				return
			}
			if m.Version != 1 || m.InvocationID != invocationID {
				done <- terminal{err: fmt.Errorf("external harness: invalid version or invocationId")}
				return
			}
			switch m.Type {
			case "provider.call":
				if m.CallID == "" || m.Request == nil {
					done <- terminal{err: errors.New("external harness: provider.call requires callId and request")}
					return
				}
				if previous := ids[m.CallID]; previous != "" {
					done <- terminal{err: fmt.Errorf("external harness: duplicate request ID %q (already used by %s)", m.CallID, previous)}
					return
				}
				ids[m.CallID] = m.Type
				go func() {
					relayProvider(ctx, p.Call, invocationID, m.CallID, m.Request.providerRequest(), write)
				}()
			case "progress.emit":
				p := m.Payload
				if len(p) == 0 {
					p = m.Message
				}
				if len(p) == 0 {
					p = json.RawMessage(`{}`)
				}
				if emit != nil {
					emit(p)
				}
			case "harness.complete":
				result := harness.Result{Text: m.Text, Reasoning: m.Reasoning, StopReason: m.StopReason}
				for _, c := range m.ToolCalls {
					result.Calls = append(result.Calls, provider.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
				}
				done <- terminal{result: result}
				return
			case "harness.error":
				msg := m.ErrorText
				if msg == "" {
					msg = string(m.Message)
				}
				done <- terminal{err: fmt.Errorf("external harness %q: %s: %s", e.name, m.Code, msg)}
				return
			default:
				done <- terminal{err: fmt.Errorf("external harness: unknown message type %q", m.Type)}
				return
			}
		}
		if err := s.Err(); err != nil {
			done <- terminal{err: fmt.Errorf("external harness: read: %w", err)}
		} else {
			done <- terminal{err: errors.New("external harness exited without harness.complete or harness.error")}
		}
	}()

	select {
	case t := <-done:
		cancel()
		_ = pipe.CloseWrite()
		if t.err != nil {
			_ = pipe.Kill()
		}
		var waitErr error
		forced := false
		select {
		case waitErr = <-processDone:
		case <-time.After(cancelGrace):
			forced = true
			_ = pipe.Kill()
			waitErr = <-processDone
		}
		if t.err == nil && waitErr != nil && !forced {
			t.err = fmt.Errorf("external harness exit: %w", waitErr)
		} else if t.err != nil && waitErr != nil && !forced {
			// Prefer nonzero exit when the scanner only saw pipe teardown
			// (process-group kill can surface "file already closed").
			msg := t.err.Error()
			if strings.Contains(msg, "exited without harness.complete") ||
				strings.Contains(msg, "file already closed") ||
				strings.Contains(msg, "external harness: read:") {
				t.err = fmt.Errorf("external harness exit: %w", waitErr)
			}
		}
		return t.result, t.err
	case <-ctx.Done():
		ctxErr := ctx.Err()
		_ = write(struct {
			Version      int    `json:"version"`
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Reason       string `json:"reason"`
		}{1, "harness.cancel", invocationID, ctxErr.Error()})
		cancel()
		select {
		case <-done:
		case <-time.After(cancelGrace):
			_ = pipe.Kill()
		}
		_ = pipe.CloseWrite()
		select {
		case <-processDone:
		case <-time.After(cancelGrace):
			_ = pipe.Kill()
			<-processDone
		}
		return harness.Result{}, ctxErr
	}
}

func relayProvider(ctx context.Context, call func(provider.Request) (harness.ModelResponse, error), invocationID, callID string, r provider.Request, write func(any) error) {
	out := struct {
		Version      int               `json:"version"`
		Type         string            `json:"type"`
		InvocationID string            `json:"invocationId"`
		CallID       string            `json:"callId"`
		Text         string            `json:"text,omitempty"`
		Reasoning    []json.RawMessage `json:"reasoning,omitempty"`
		ToolCalls    []wireToolCall    `json:"toolCalls,omitempty"`
		StopReason   string            `json:"stopReason,omitempty"`
		Usage        *provider.Usage   `json:"usage,omitempty"`
		Error        string            `json:"error,omitempty"`
	}{Version: 1, Type: "provider.result", InvocationID: invocationID, CallID: callID}
	if call == nil {
		out.Error = "provider callback unavailable"
		_ = write(out)
		return
	}
	if ctx.Err() != nil {
		return
	}
	result, err := call(r)
	if err != nil {
		out.Error = err.Error()
		_ = write(out)
		return
	}
	out.Text = result.Text
	out.Reasoning = result.Reasoning
	out.StopReason = result.StopReason
	out.Usage = result.Usage
	for _, call := range result.Calls {
		out.ToolCalls = append(out.ToolCalls, wireToolCall{ID: call.ID, Name: call.Name, Args: call.Args})
	}
	_ = write(out)
}
