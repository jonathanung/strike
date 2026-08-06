package harness

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

func TestServeProviderProgressAndComplete(t *testing.T) {
	stdin, writeInput := io.Pipe()
	defer writeInput.Close()
	readOutput, stdout := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serve(stdin, stdout, func(input Input, provider Provider, emit Emit) (Result, error) {
			if input.Request.Model != "caller-request" {
				t.Errorf("input model = %q", input.Request.Model)
			}
			type response struct {
				text string
				err  error
			}
			responses := make(chan response, 2)
			for _, model := range []string{"alpha", "beta"} {
				go func() {
					result, err := provider.Call(Request{Model: model, Messages: []Message{}})
					responses <- response{text: result.Text, err: err}
				}()
			}
			texts := make([]string, 0, 2)
			for range 2 {
				result := <-responses
				if result.err != nil {
					return Result{}, result.err
				}
				texts = append(texts, result.text)
			}
			if err := emit(map[string]any{"calls": 2}); err != nil {
				return Result{}, err
			}
			return Result{Text: strings.Join(texts, ","), StopReason: "end_turn"}, nil
		})
	}()

	writeJSONLine(t, writeInput, map[string]any{
		"version": 1, "type": "harness.start", "invocationId": "inv-1",
		"request": map[string]any{"model": "caller-request", "messages": []any{}},
	})
	scanner := bufio.NewScanner(readOutput)
	providerCalls := 0
	progress := 0
	for scanner.Scan() {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		var messageType string
		if err := json.Unmarshal(message["type"], &messageType); err != nil {
			t.Fatal(err)
		}
		switch messageType {
		case "provider.call":
			providerCalls++
			var callID string
			var request Request
			if err := json.Unmarshal(message["callId"], &callID); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(message["request"], &request); err != nil {
				t.Fatal(err)
			}
			writeJSONLine(t, writeInput, map[string]any{
				"version": 1, "type": "provider.result", "invocationId": "inv-1",
				"callId": callID, "text": request.Model + "-result", "stopReason": "end_turn",
			})
		case "progress.emit":
			progress++
		case "harness.complete":
			var text string
			if err := json.Unmarshal(message["text"], &text); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(text, "alpha-result") || !strings.Contains(text, "beta-result") {
				t.Fatalf("complete text = %q", text)
			}
			if providerCalls != 2 || progress != 1 {
				t.Fatalf("provider calls = %d, progress = %d", providerCalls, progress)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("output ended before harness.complete: %v", scanner.Err())
}

func TestServeToolExecuteAndComplete(t *testing.T) {
	stdin, writeInput := io.Pipe()
	defer writeInput.Close()
	readOutput, stdout := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serve(stdin, stdout, func(input Input, _ Provider, emit Emit) (Result, error) {
			if input.Tools == nil {
				return Result{}, errors.New("tools unavailable")
			}
			res, err := input.Tools.Execute(ToolCall{
				ID:        "call-1",
				Name:      "read",
				Arguments: json.RawMessage(`{"filePath":"a"}`),
			})
			if err != nil {
				return Result{}, err
			}
			if res.IsError {
				return Result{}, fmt.Errorf("tool error: %s", res.Output)
			}
			if err := emit(map[string]any{"tool": true}); err != nil {
				return Result{}, err
			}
			return Result{Text: res.Output, StopReason: "end_turn"}, nil
		})
	}()

	writeJSONLine(t, writeInput, map[string]any{
		"version": 1, "type": "harness.start", "invocationId": "inv-tool",
		"request":      map[string]any{"model": "fixture", "messages": []any{}},
		"capabilities": []string{"provider.call", "tool.execute", "progress.emit", "harness.cancel"},
	})
	scanner := bufio.NewScanner(readOutput)
	for scanner.Scan() {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		var messageType string
		if err := json.Unmarshal(message["type"], &messageType); err != nil {
			t.Fatal(err)
		}
		switch messageType {
		case "tool.execute":
			var callID, name string
			if err := json.Unmarshal(message["callId"], &callID); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(message["name"], &name); err != nil {
				t.Fatal(err)
			}
			if name != "read" {
				t.Fatalf("name = %q", name)
			}
			writeJSONLine(t, writeInput, map[string]any{
				"version": 1, "type": "tool.result", "invocationId": "inv-tool",
				"callId": callID, "output": "file-a", "isError": false,
			})
		case "progress.emit":
			// ok
		case "harness.complete":
			var text string
			if err := json.Unmarshal(message["text"], &text); err != nil {
				t.Fatal(err)
			}
			if text != "file-a" {
				t.Fatalf("complete text = %q", text)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("output ended before harness.complete: %v", scanner.Err())
}

func TestServeCancellationReachesInputContext(t *testing.T) {
	stdin, writeInput := io.Pipe()
	defer writeInput.Close()
	readOutput, stdout := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serve(stdin, stdout, func(input Input, _ Provider, _ Emit) (Result, error) {
			<-input.Context.Done()
			return Result{}, input.Context.Err()
		})
	}()
	writeJSONLine(t, writeInput, map[string]any{
		"version": 1, "type": "harness.start", "invocationId": "inv-2",
		"request": map[string]any{"model": "fixture", "messages": []any{}},
	})
	writeJSONLine(t, writeInput, map[string]any{
		"version": 1, "type": "harness.cancel", "invocationId": "inv-2", "reason": "stopped",
	})
	scanner := bufio.NewScanner(readOutput)
	if !scanner.Scan() {
		t.Fatalf("missing harness.error: %v", scanner.Err())
	}
	var message struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	if message.Type != "harness.error" || message.Error != "context canceled" {
		t.Fatalf("message = %#v", message)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
}

func writeJSONLine(t *testing.T, writer io.Writer, value any) {
	t.Helper()
	line, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}
