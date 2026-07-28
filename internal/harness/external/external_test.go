package external_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/harness/external"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestExternalHappyConcurrentProviderCalls(t *testing.T) {
	t.Setenv("GO_WANT_HARNESS_HELPER", "happy")
	h, err := external.New("fixture", external.Config{Command: os.Args[0], Args: []string{"-test.run=TestHarnessHelperProcess"}})
	if err != nil {
		t.Fatal(err)
	}
	progress := make(chan json.RawMessage, 1)
	result, err := h.Run(context.Background(), harness.Request{
		InvocationID: "invocation-1",
		Agent:        "build",
		ProviderName: "echo",
		Request:      provider.Request{Model: "selected"},
		Provider: func(_ context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
			if req.Model != "candidate" {
				t.Errorf("callback model = %q, want fixture request", req.Model)
			}
			ch := make(chan provider.StreamEvent, 2)
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: "candidate"}
			ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: "stop", Usage: &provider.Usage{OutputTokens: 1}}
			close(ch)
			return ch, nil
		},
		Execute: func(context.Context, provider.ToolCall) provider.Message {
			t.Fatal("unexpected tool call")
			return provider.Message{}
		},
		Progress: func(raw json.RawMessage) { progress <- raw },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "final" || result.StopReason != "end_turn" {
		t.Fatalf("result = %#v", result)
	}
	if got := string(<-progress); got != `{"message":"working"}` {
		t.Fatalf("progress = %s", got)
	}
}

func TestExternalRejectsMalformedJSON(t *testing.T) {
	tests := []struct {
		mode string
		want string
	}{
		{"malformed", "malformed JSON"},
		{"version", "invalid version"},
		{"unknown", "unknown message type"},
		{"missing", "exited without harness.complete"},
		{"nonzero", "external harness exit"},
		{"duplicate", "duplicate request ID"},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			h := newFixture(t, tt.mode)
			_, err := h.Run(context.Background(), harness.Request{InvocationID: "invocation-1"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestExternalCompleteDoesNotWaitForLiveHarness(t *testing.T) {
	h := newFixture(t, "stays-alive")
	started := time.Now()
	result, err := h.Run(context.Background(), harness.Request{InvocationID: "invocation-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "final" {
		t.Fatalf("result.Text = %q", result.Text)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Run took %v after harness.complete", elapsed)
	}
}

func TestExternalToolExecutePreservesID(t *testing.T) {
	h := newFixture(t, "tool")
	result, err := h.Run(context.Background(), harness.Request{
		InvocationID: "invocation-1",
		Execute: func(_ context.Context, call provider.ToolCall) provider.Message {
			if call.ID != "tool-7" || call.Name != "allowed" || string(call.Args) != `{"value":1}` {
				t.Errorf("call = %#v", call)
			}
			return provider.Message{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: call.ID, Output: "routed"}}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "routed" {
		t.Fatalf("result.Text = %q", result.Text)
	}
}

func TestExternalCancellationStopsLiveHarness(t *testing.T) {
	h := newFixture(t, "blocks")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := h.Run(ctx, harness.Request{InvocationID: "invocation-1"})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func newFixture(t *testing.T, mode string) *external.External {
	t.Helper()
	t.Setenv("GO_WANT_HARNESS_HELPER", mode)
	h, err := external.New("fixture", external.Config{Command: os.Args[0], Args: []string{"-test.run=TestHarnessHelperProcess"}})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestHarnessHelperProcess(t *testing.T) {
	mode := os.Getenv("GO_WANT_HARNESS_HELPER")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	if mode == "malformed" {
		fmt.Println("not-json")
		return
	}
	if mode == "version" {
		fmt.Println(`{"version":2,"type":"harness.complete","invocationId":"invocation-1"}`)
		return
	}
	if mode == "unknown" {
		fmt.Println(`{"version":1,"type":"future.message","invocationId":"invocation-1"}`)
		return
	}
	if mode == "missing" {
		os.Exit(0)
	}
	if mode == "nonzero" {
		os.Exit(3)
	}
	if mode == "duplicate" {
		fmt.Println(`{"version":1,"type":"provider.call","invocationId":"invocation-1","callId":"same","request":{"model":"candidate","messages":[]}}`)
		fmt.Println(`{"version":1,"type":"tool.execute","invocationId":"invocation-1","toolCallId":"same","name":"x","arguments":{}}`)
		time.Sleep(time.Hour)
	}
	if mode == "stays-alive" {
		fmt.Println(`{"version":1,"type":"harness.complete","invocationId":"invocation-1","text":"final"}`)
		time.Sleep(time.Hour)
	}
	if mode == "blocks" {
		time.Sleep(time.Hour)
	}
	if mode == "tool" {
		fmt.Println(`{"version":1,"type":"tool.execute","invocationId":"invocation-1","toolCallId":"tool-7","name":"allowed","arguments":{"value":1}}`)
		for scanner.Scan() {
			var m struct {
				Type       string `json:"type"`
				ToolCallID string `json:"toolCallId"`
				Output     string `json:"output"`
			}
			if json.Unmarshal(scanner.Bytes(), &m) == nil && m.Type == "tool.result" {
				fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":"invocation-1","text":%q}`+"\n", m.Output)
				return
			}
		}
		return
	}
	fmt.Println(`{"version":1,"type":"progress.emit","invocationId":"invocation-1","payload":{"message":"working"}}`)
	for _, id := range []string{"a", "b"} {
		fmt.Printf(`{"version":1,"type":"provider.call","invocationId":"invocation-1","callId":%q,"request":{"model":"candidate","messages":[]}}`+"\n", id)
	}
	completions := 0
	for scanner.Scan() {
		var m struct {
			Type string `json:"type"`
			Kind string `json:"kind"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) == nil && m.Type == "provider.event" && (m.Kind == "completion" || m.Kind == "error") {
			completions++
			if completions == 2 {
				fmt.Println(`{"version":1,"type":"harness.complete","invocationId":"invocation-1","text":"final","stopReason":"end_turn"}`)
				return
			}
		}
	}
}
