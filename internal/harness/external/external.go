// Package external implements the version 1 JSONL subprocess harness transport.
package external

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

type Config struct {
	Command string
	Args    []string
	Env     map[string]string
}

type External struct {
	name string
	cfg  Config
}

func New(name string, cfg Config) (*External, error) {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(cfg.Command) == "" {
		return nil, errors.New("external harness: name and command are required")
	}
	return &External{name: name, cfg: cfg}, nil
}

func (e *External) Name() string { return e.name }

type envelope struct {
	Version    int               `json:"version"`
	Type       string            `json:"type"`
	TurnID     string            `json:"turnId"`
	CallID     string            `json:"callId,omitempty"`
	ToolCallID string            `json:"toolCallId,omitempty"`
	Request    *wireRequest      `json:"request,omitempty"`
	Name       string            `json:"name,omitempty"`
	Arguments  json.RawMessage   `json:"arguments,omitempty"`
	Payload    json.RawMessage   `json:"payload,omitempty"`
	Message    json.RawMessage   `json:"message,omitempty"`
	Code       string            `json:"code,omitempty"`
	ErrorText  string            `json:"error,omitempty"`
	Text       string            `json:"text,omitempty"`
	Reasoning  []json.RawMessage `json:"reasoning,omitempty"`
	ToolCalls  []wireToolCall    `json:"toolCalls,omitempty"`
	StopReason string            `json:"stopReason,omitempty"`
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

func (e *External) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.Command(e.cfg.Command, e.cfg.Args...)
	prepareCommand(cmd)
	cmd.Env = os.Environ()
	for k, v := range e.cfg.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return harness.Result{}, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return harness.Result{}, err
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return harness.Result{}, fmt.Errorf("start external harness %q: %w", e.name, err)
	}
	processDone := make(chan error, 1)
	go func() { processDone <- cmd.Wait() }()

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
		_, err = stdin.Write(append(b, '\n'))
		return err
	}
	start := struct {
		Version      int         `json:"version"`
		Type         string      `json:"type"`
		TurnID       string      `json:"turnId"`
		Agent        string      `json:"agent"`
		Provider     string      `json:"provider"`
		Request      wireRequest `json:"request"`
		Capabilities []string    `json:"capabilities"`
	}{1, "turn.start", req.TurnID, req.Agent, req.ProviderName, toWire(req.Request), []string{"provider.call", "progress.emit", "tool.execute", "turn.cancel"}}
	if err := write(start); err != nil {
		terminateProcessTree(cmd)
		<-processDone
		return harness.Result{}, err
	}

	type terminal struct {
		result harness.Result
		err    error
	}
	done := make(chan terminal, 1)
	var relays sync.WaitGroup
	go func() {
		s := bufio.NewScanner(stdout)
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
			if m.Version != 1 || m.TurnID != req.TurnID {
				done <- terminal{err: fmt.Errorf("external harness: invalid version or turnId")}
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
				relays.Add(1)
				go func() {
					defer relays.Done()
					relayProvider(ctx, req, m.CallID, m.Request.providerRequest(), write)
				}()
			case "progress.emit":
				p := m.Payload
				if len(p) == 0 {
					p = m.Message
				}
				if len(p) == 0 {
					p = json.RawMessage(`{}`)
				}
				req.Progress(p)
			case "tool.execute":
				if m.ToolCallID == "" || m.Name == "" {
					done <- terminal{err: errors.New("external harness: tool.execute requires IDs and name")}
					return
				}
				if previous := ids[m.ToolCallID]; previous != "" {
					done <- terminal{err: fmt.Errorf("external harness: duplicate request ID %q (already used by %s)", m.ToolCallID, previous)}
					return
				}
				ids[m.ToolCallID] = m.Type
				relays.Add(1)
				go func(m envelope) {
					defer relays.Done()
					msg := req.Execute(ctx, provider.ToolCall{ID: m.ToolCallID, Name: m.Name, Args: m.Arguments})
					tr := msg.ToolResult
					out := struct {
						Version    int    `json:"version"`
						Type       string `json:"type"`
						TurnID     string `json:"turnId"`
						ToolCallID string `json:"toolCallId"`
						Output     string `json:"output,omitempty"`
						Error      string `json:"error,omitempty"`
					}{Version: 1, Type: "tool.result", TurnID: req.TurnID, ToolCallID: m.ToolCallID}
					if tr != nil {
						out.Output = tr.Output
						if tr.IsError {
							out.Error = tr.Output
						}
					}
					_ = write(out)
				}(m)
			case "turn.complete":
				result := harness.Result{Text: m.Text, Reasoning: m.Reasoning, StopReason: m.StopReason}
				for _, c := range m.ToolCalls {
					result.Calls = append(result.Calls, provider.ToolCall{ID: c.ID, Name: c.Name, Args: c.Args})
				}
				done <- terminal{result: result}
				return
			case "turn.error":
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
			done <- terminal{err: errors.New("external harness exited without turn.complete or turn.error")}
		}
	}()

	select {
	case t := <-done:
		cancel()
		_ = stdin.Close()
		if t.err != nil {
			terminateProcessTree(cmd)
		}
		var waitErr error
		forced := false
		select {
		case waitErr = <-processDone:
		case <-time.After(cancelGrace):
			forced = true
			terminateProcessTree(cmd)
			waitErr = <-processDone
		}
		relays.Wait()
		if t.err == nil && waitErr != nil && !forced {
			t.err = fmt.Errorf("external harness exit: %w", waitErr)
		} else if t.err != nil && waitErr != nil && !forced && strings.Contains(t.err.Error(), "exited without turn.complete") {
			t.err = fmt.Errorf("%v; external harness exit: %w", t.err, waitErr)
		}
		return t.result, t.err
	case <-ctx.Done():
		ctxErr := ctx.Err()
		_ = write(struct {
			Version int    `json:"version"`
			Type    string `json:"type"`
			TurnID  string `json:"turnId"`
			Reason  string `json:"reason"`
		}{1, "turn.cancel", req.TurnID, ctxErr.Error()})
		cancel()
		select {
		case <-done:
		case <-time.After(cancelGrace):
			terminateProcessTree(cmd)
		}
		_ = stdin.Close()
		select {
		case <-processDone:
		case <-time.After(cancelGrace):
			terminateProcessTree(cmd)
			<-processDone
		}
		relays.Wait()
		return harness.Result{}, ctxErr
	}
}

func relayProvider(ctx context.Context, req harness.Request, callID string, r provider.Request, write func(any) error) {
	if req.Provider == nil {
		_ = write(providerEvent(req.TurnID, callID, provider.StreamEvent{Type: provider.EventError, Err: errors.New("provider callback unavailable")}))
		return
	}
	stream, err := req.Provider(ctx, r)
	if err != nil {
		_ = write(providerEvent(req.TurnID, callID, provider.StreamEvent{Type: provider.EventError, Err: err}))
		return
	}
	stream = provider.NormalizeStream(stream)
	for {
		var ev provider.StreamEvent
		var ok bool
		select {
		case <-ctx.Done():
			return
		case ev, ok = <-stream:
			if !ok {
				return
			}
		}
		if ev.Type == provider.EventDone && ev.Usage != nil {
			if write(providerUsageEvent(req.TurnID, callID, ev.Usage)) != nil {
				return
			}
		}
		if write(providerEvent(req.TurnID, callID, ev)) != nil {
			return
		}
	}
}

func providerUsageEvent(turnID, callID string, usage *provider.Usage) any {
	return struct {
		Version int             `json:"version"`
		Type    string          `json:"type"`
		TurnID  string          `json:"turnId"`
		CallID  string          `json:"callId"`
		Kind    string          `json:"kind"`
		Usage   *provider.Usage `json:"usage"`
	}{1, "provider.event", turnID, callID, "usage", usage}
}

func providerEvent(turnID, callID string, ev provider.StreamEvent) any {
	kind := "error"
	done := false
	switch ev.Type {
	case provider.EventTextDelta:
		kind = "text"
	case provider.EventReasoning:
		kind = "reasoning"
	case provider.EventToolCall:
		kind = "tool_call"
	case provider.EventDone:
		kind = "completion"
		done = true
	case provider.EventError:
		done = true
	}
	errText := ""
	if ev.Err != nil {
		errText = ev.Err.Error()
	}
	return struct {
		Version    int                `json:"version"`
		Type       string             `json:"type"`
		TurnID     string             `json:"turnId"`
		CallID     string             `json:"callId"`
		Kind       string             `json:"kind"`
		Done       bool               `json:"done,omitempty"`
		Text       string             `json:"text,omitempty"`
		Reasoning  json.RawMessage    `json:"reasoning,omitempty"`
		ToolCall   *provider.ToolCall `json:"toolCall,omitempty"`
		Usage      *provider.Usage    `json:"usage,omitempty"`
		StopReason string             `json:"stopReason,omitempty"`
		Error      string             `json:"error,omitempty"`
	}{1, "provider.event", turnID, callID, kind, done, ev.Text, ev.Reasoning, ev.ToolCall, ev.Usage, ev.StopReason, errText}
}
