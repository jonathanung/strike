package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
)

const maxLineBytes = 1 << 20

// Run serves one harness invocation over stdin and stdout.
func Run(fn Func) error {
	return serve(os.Stdin, os.Stdout, fn)
}

type envelope struct {
	Version      int      `json:"version"`
	Type         string   `json:"type"`
	InvocationID string   `json:"invocationId"`
	CallID       string   `json:"callId,omitempty"`
	Request      *Request `json:"request,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	Error        string   `json:"error,omitempty"`
	Output       string   `json:"output,omitempty"`
	IsError      bool     `json:"isError,omitempty"`
	ErrorCode    string   `json:"errorCode,omitempty"`
	Retryable    bool     `json:"retryable,omitempty"`
	ModelResponse
}

type callResult struct {
	response ModelResponse
	err      error
}

type toolCallResult struct {
	result ToolResult
	err    error
}

type processRuntime struct {
	ctx          context.Context
	invocationID string
	write        func(any) error
	sequence     atomic.Uint64
	waitMu       sync.Mutex
	providerWait map[string]chan callResult
	toolWait     map[string]chan toolCallResult
}

func (p *processRuntime) Call(request Request) (ModelResponse, error) {
	callID := fmt.Sprintf("provider-%d", p.sequence.Add(1))
	waiter := make(chan callResult, 1)
	p.waitMu.Lock()
	p.providerWait[callID] = waiter
	p.waitMu.Unlock()

	message := struct {
		Version      int     `json:"version"`
		Type         string  `json:"type"`
		InvocationID string  `json:"invocationId"`
		CallID       string  `json:"callId"`
		Request      Request `json:"request"`
	}{1, "provider.call", p.invocationID, callID, request}
	if err := p.write(message); err != nil {
		p.removeProvider(callID)
		return ModelResponse{}, err
	}
	select {
	case result := <-waiter:
		return result.response, result.err
	case <-p.ctx.Done():
		p.removeProvider(callID)
		return ModelResponse{}, context.Cause(p.ctx)
	}
}

func (p *processRuntime) Execute(call ToolCall) (ToolResult, error) {
	callID := fmt.Sprintf("tool-%d", p.sequence.Add(1))
	waiter := make(chan toolCallResult, 1)
	p.waitMu.Lock()
	p.toolWait[callID] = waiter
	p.waitMu.Unlock()

	toolCallID := call.ID
	if toolCallID == "" {
		toolCallID = callID
	}
	message := struct {
		Version      int             `json:"version"`
		Type         string          `json:"type"`
		InvocationID string          `json:"invocationId"`
		CallID       string          `json:"callId"`
		ToolCallID   string          `json:"toolCallId,omitempty"`
		Name         string          `json:"name"`
		Arguments    json.RawMessage `json:"arguments,omitempty"`
	}{1, "tool.execute", p.invocationID, callID, toolCallID, call.Name, call.Arguments}
	if err := p.write(message); err != nil {
		p.removeTool(callID)
		return ToolResult{}, err
	}
	select {
	case result := <-waiter:
		return result.result, result.err
	case <-p.ctx.Done():
		p.removeTool(callID)
		return ToolResult{}, context.Cause(p.ctx)
	}
}

func (p *processRuntime) resolveProvider(message envelope) {
	p.waitMu.Lock()
	waiter := p.providerWait[message.CallID]
	delete(p.providerWait, message.CallID)
	p.waitMu.Unlock()
	if waiter == nil {
		return
	}
	if message.Error != "" {
		waiter <- callResult{err: errors.New(message.Error)}
		return
	}
	waiter <- callResult{response: message.ModelResponse}
}

func (p *processRuntime) resolveTool(message envelope) {
	p.waitMu.Lock()
	waiter := p.toolWait[message.CallID]
	delete(p.toolWait, message.CallID)
	p.waitMu.Unlock()
	if waiter == nil {
		return
	}
	if message.Error != "" && message.Output == "" && !message.IsError {
		// Transport-level failure (callback unavailable, etc.).
		waiter <- toolCallResult{err: errors.New(message.Error)}
		return
	}
	result := ToolResult{
		CallID:    message.CallID,
		Output:    message.Output,
		IsError:   message.IsError,
		ErrorCode: message.ErrorCode,
		Retryable: message.Retryable,
	}
	if message.Error != "" && result.Output == "" {
		result.Output = message.Error
		result.IsError = true
	}
	waiter <- toolCallResult{result: result}
}

func (p *processRuntime) removeProvider(callID string) {
	p.waitMu.Lock()
	delete(p.providerWait, callID)
	p.waitMu.Unlock()
}

func (p *processRuntime) removeTool(callID string) {
	p.waitMu.Lock()
	delete(p.toolWait, callID)
	p.waitMu.Unlock()
}

func serve(stdin io.Reader, stdout io.Writer, fn Func) error {
	if fn == nil {
		return errors.New("strike harness: function is required")
	}
	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return err
		}
		return errors.New("strike harness: expected harness.start")
	}
	var start envelope
	if err := json.Unmarshal(scanner.Bytes(), &start); err != nil {
		return fmt.Errorf("strike harness: decode start: %w", err)
	}
	if start.Version != 1 || start.Type != "harness.start" || start.InvocationID == "" || start.Request == nil {
		return errors.New("strike harness: invalid harness.start")
	}

	var writeMu sync.Mutex
	write := func(value any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		line, err := json.Marshal(value)
		if err != nil {
			return err
		}
		if len(line) > maxLineBytes {
			return errors.New("strike harness: outbound line exceeds limit")
		}
		_, err = stdout.Write(append(line, '\n'))
		return err
	}

	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	runtime := &processRuntime{
		ctx:          ctx,
		invocationID: start.InvocationID,
		write:        write,
		providerWait: make(map[string]chan callResult),
		toolWait:     make(map[string]chan toolCallResult),
	}
	go func() {
		for scanner.Scan() {
			var message envelope
			if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
				cancel(fmt.Errorf("strike harness: decode message: %w", err))
				return
			}
			if message.Version != 1 || message.InvocationID != start.InvocationID {
				cancel(errors.New("strike harness: invalid version or invocationId"))
				return
			}
			switch message.Type {
			case "provider.result":
				runtime.resolveProvider(message)
			case "tool.result":
				runtime.resolveTool(message)
			case "harness.cancel":
				reason := message.Reason
				if reason == "" {
					reason = "harness canceled"
				}
				cancel(errors.New(reason))
				return
			}
		}
		if err := scanner.Err(); err != nil {
			cancel(err)
		} else {
			cancel(io.EOF)
		}
	}()

	emit := func(payload any) error {
		return write(struct {
			Version      int    `json:"version"`
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Payload      any    `json:"payload"`
		}{1, "progress.emit", start.InvocationID, payload})
	}
	result, err := fn(Input{Context: ctx, Request: *start.Request, Tools: runtime}, runtime, emit)
	if err != nil {
		if writeErr := write(struct {
			Version      int    `json:"version"`
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Error        string `json:"error"`
		}{1, "harness.error", start.InvocationID, err.Error()}); writeErr != nil {
			return writeErr
		}
		return nil
	}
	return write(struct {
		Version      int    `json:"version"`
		Type         string `json:"type"`
		InvocationID string `json:"invocationId"`
		Result
	}{1, "harness.complete", start.InvocationID, result})
}
