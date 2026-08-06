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

// Run serves one harness invocation over stdin and stdout (oneshot).
func Run(fn Func) error {
	return serve(os.Stdin, os.Stdout, fn)
}

// RunWorker serves multiple harness.start messages until EOF or harness.shutdown.
// Use this when Strike config sets mode: "persistent" for the harness command.
func RunWorker(fn Func) error {
	return serveWorker(os.Stdin, os.Stdout, fn)
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
	cancel       context.CancelCauseFunc
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

func (p *processRuntime) rejectAll(err error) {
	p.waitMu.Lock()
	providers := p.providerWait
	tools := p.toolWait
	p.providerWait = make(map[string]chan callResult)
	p.toolWait = make(map[string]chan toolCallResult)
	p.waitMu.Unlock()
	for _, w := range providers {
		w <- callResult{err: err}
	}
	for _, w := range tools {
		w <- toolCallResult{err: err}
	}
}

func newWriteFunc(stdout io.Writer) func(any) error {
	var writeMu sync.Mutex
	return func(value any) error {
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

	write := newWriteFunc(stdout)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	runtime := &processRuntime{
		ctx:          ctx,
		cancel:       cancel,
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

	return runOne(fn, runtime, start, write)
}

func runOne(fn Func, runtime *processRuntime, start envelope, write func(any) error) error {
	emit := func(payload any) error {
		return write(struct {
			Version      int    `json:"version"`
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Payload      any    `json:"payload"`
		}{1, "progress.emit", start.InvocationID, payload})
	}
	result, err := fn(Input{Context: runtime.ctx, Request: *start.Request, Tools: runtime}, runtime, emit)
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

// serveWorker multiplexes harness.start / cancel / results until shutdown or EOF.
func serveWorker(stdin io.Reader, stdout io.Writer, fn Func) error {
	if fn == nil {
		return errors.New("strike harness: function is required")
	}
	write := newWriteFunc(stdout)
	// Optional readiness signal for hosts that wait on it.
	_ = write(struct {
		Version int    `json:"version"`
		Type    string `json:"type"`
	}{1, "harness.ready"})

	var (
		mu       sync.Mutex
		runtimes = map[string]*processRuntime{}
		wg       sync.WaitGroup
		seq      atomic.Uint64 // shared call-id space across invocations
	)

	lookup := func(id string) *processRuntime {
		mu.Lock()
		defer mu.Unlock()
		return runtimes[id]
	}
	register := func(id string, rt *processRuntime) {
		mu.Lock()
		runtimes[id] = rt
		mu.Unlock()
	}
	unregister := func(id string) {
		mu.Lock()
		delete(runtimes, id)
		mu.Unlock()
	}
	cancelAll := func(err error) {
		mu.Lock()
		snapshot := make([]*processRuntime, 0, len(runtimes))
		for _, rt := range runtimes {
			snapshot = append(snapshot, rt)
		}
		mu.Unlock()
		for _, rt := range snapshot {
			rt.cancel(err)
			rt.rejectAll(err)
		}
	}

	scanner := bufio.NewScanner(stdin)
	scanner.Buffer(make([]byte, 64*1024), maxLineBytes)
	for scanner.Scan() {
		var message envelope
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			cancelAll(fmt.Errorf("strike harness: decode message: %w", err))
			wg.Wait()
			return fmt.Errorf("strike harness: decode message: %w", err)
		}
		if message.Version != 1 {
			cancelAll(errors.New("strike harness: invalid version"))
			wg.Wait()
			return errors.New("strike harness: invalid version")
		}
		switch message.Type {
		case "harness.shutdown":
			cancelAll(errors.New("harness shutdown"))
			wg.Wait()
			return nil
		case "harness.start":
			if message.InvocationID == "" || message.Request == nil {
				cancelAll(errors.New("strike harness: invalid harness.start"))
				wg.Wait()
				return errors.New("strike harness: invalid harness.start")
			}
			if lookup(message.InvocationID) != nil {
				cancelAll(fmt.Errorf("strike harness: duplicate invocationId %q", message.InvocationID))
				wg.Wait()
				return fmt.Errorf("strike harness: duplicate invocationId %q", message.InvocationID)
			}
			ctx, cancel := context.WithCancelCause(context.Background())
			rt := &processRuntime{
				ctx:          ctx,
				cancel:       cancel,
				invocationID: message.InvocationID,
				write:        write,
				providerWait: make(map[string]chan callResult),
				toolWait:     make(map[string]chan toolCallResult),
			}
			// Offset sequence so concurrent invocations never collide on callId.
			rt.sequence.Store(seq.Add(1_000_000))
			register(message.InvocationID, rt)
			start := message
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer unregister(start.InvocationID)
				defer cancel(context.Canceled)
				_ = runOne(fn, rt, start, write)
			}()
		case "provider.result":
			if rt := lookup(message.InvocationID); rt != nil {
				rt.resolveProvider(message)
			}
		case "tool.result":
			if rt := lookup(message.InvocationID); rt != nil {
				rt.resolveTool(message)
			}
		case "harness.cancel":
			if rt := lookup(message.InvocationID); rt != nil {
				reason := message.Reason
				if reason == "" {
					reason = "harness canceled"
				}
				err := errors.New(reason)
				rt.cancel(err)
				rt.rejectAll(err)
			}
		default:
			// Ignore unknown host→worker messages for forward compatibility.
		}
	}
	if err := scanner.Err(); err != nil {
		cancelAll(err)
		wg.Wait()
		return err
	}
	cancelAll(io.EOF)
	wg.Wait()
	return nil
}
