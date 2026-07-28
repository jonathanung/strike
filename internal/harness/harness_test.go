package harness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestNewRegistry_HasBuiltins(t *testing.T) {
	r := harness.NewRegistry()
	for _, name := range harness.BuiltinNames() {
		if !r.Known(name) {
			t.Errorf("registry missing builtin %q", name)
		}
		h := r.Get(name)
		if h == nil {
			t.Errorf("Get(%q) = nil", name)
		}
		if h.Name() != name {
			t.Errorf("Get(%q).Name() = %q", name, h.Name())
		}
	}
}

func TestRegistry_NilRegistry(t *testing.T) {
	var r *harness.Registry
	if r.Known("default") {
		t.Error("nil registry should not know any name")
	}
	if r.Get("default") != nil {
		t.Error("nil registry Get should return nil")
	}
}

func TestRegistry_Resolve(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"", "default", false},
		{"default", "default", false},
		{"unknown", "", true},
	}
	r := harness.NewRegistry()
	for _, tt := range tests {
		t.Run("name="+tt.name, func(t *testing.T) {
			h, err := r.Resolve(tt.name)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if h.Name() != tt.want {
				t.Errorf("Name() = %q, want %q", h.Name(), tt.want)
			}
		})
	}
}

func TestRegistry_ResolveNilRegistry(t *testing.T) {
	var r *harness.Registry
	h, err := r.Resolve("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if h.Name() != "default" {
		t.Errorf("Name() = %q, want default", h.Name())
	}

	h, err = r.Resolve("unknown")
	if err == nil {
		t.Error("expected error for unknown harness on nil registry")
	}
	if h != nil {
		t.Error("expected nil harness for unknown")
	}
}

func TestDefaultHarness_EmptyTurn(t *testing.T) {
	dh := &harness.DefaultHarness{}
	if dh.Name() != "default" {
		t.Errorf("Name() = %q, want default", dh.Name())
	}

	streamed := false
	result, err := dh.Run(context.Background(), harness.Request{
		TurnID: "turn-1",
		Stream: func(ctx context.Context) (harness.Outcome, error) {
			streamed = true
			return harness.Outcome{StopReason: "end_turn"}, nil
		},
		Execute: func(ctx context.Context, call provider.ToolCall) provider.Message {
			t.Fatal("should not execute tools on no-call stream")
			return provider.Message{}
		},
		Progress: func(payload json.RawMessage) {},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !streamed {
		t.Error("stream was not called")
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", result.StopReason)
	}
}

func TestDefaultHarness_ToolLoop(t *testing.T) {
	dh := &harness.DefaultHarness{}
	callID := "call-1"

	streamCalls := 0
	execCalls := 0

	req := harness.Request{
		TurnID: "turn-1",
		Stream: func(ctx context.Context) (harness.Outcome, error) {
			streamCalls++
			switch streamCalls {
			case 1:
				return harness.Outcome{
					Calls: []provider.ToolCall{
						{ID: callID, Name: "bash", Args: json.RawMessage(`{"command":"echo hi"}`)},
					},
					StopReason: "tool_use",
				}, nil
			default:
				return harness.Outcome{StopReason: "end_turn"}, nil
			}
		},
		Execute: func(ctx context.Context, call provider.ToolCall) provider.Message {
			execCalls++
			if call.ID != callID {
				t.Errorf("call.ID = %q, want %q", call.ID, callID)
			}
			return provider.Message{
				Role:       provider.RoleTool,
				ToolResult: &provider.ToolResult{CallID: call.ID, Output: "hi"},
			}
		},
		Progress: func(payload json.RawMessage) {},
	}

	result, err := dh.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if streamCalls != 2 {
		t.Errorf("streamCalls = %d, want 2", streamCalls)
	}
	if execCalls != 1 {
		t.Errorf("execCalls = %d, want 1", execCalls)
	}
	if result.StopReason != "end_turn" {
		t.Errorf("StopReason = %q, want end_turn", result.StopReason)
	}
}

func TestDefaultHarness_StreamError(t *testing.T) {
	dh := &harness.DefaultHarness{}
	wantErr := errors.New("provider down")

	_, err := dh.Run(context.Background(), harness.Request{
		TurnID: "turn-1",
		Stream: func(ctx context.Context) (harness.Outcome, error) {
			return harness.Outcome{}, wantErr
		},
		Execute:  func(ctx context.Context, call provider.ToolCall) provider.Message { return provider.Message{} },
		Progress: func(payload json.RawMessage) {},
	})
	if !errors.Is(err, wantErr) {
		t.Errorf("err = %v, want %v", err, wantErr)
	}
}

func TestDefaultHarness_Cancellation(t *testing.T) {
	dh := &harness.DefaultHarness{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediate cancel

	_, err := dh.Run(ctx, harness.Request{
		TurnID: "turn-1",
		Stream: func(ctx context.Context) (harness.Outcome, error) {
			return harness.Outcome{StopReason: "end_turn"}, nil
		},
		Execute:  func(ctx context.Context, call provider.ToolCall) provider.Message { return provider.Message{} },
		Progress: func(payload json.RawMessage) {},
	})
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestDefaultHarness_ExecutesAllCallsEvenCanceled(t *testing.T) {
	dh := &harness.DefaultHarness{}
	callID1 := "call-1"
	callID2 := "call-2"

	execCalls := 0
	ctx, cancel := context.WithCancel(context.Background())

	_, err := dh.Run(ctx, harness.Request{
		TurnID: "turn-1",
		Stream: func(ctx context.Context) (harness.Outcome, error) {
			return harness.Outcome{
				Calls: []provider.ToolCall{
					{ID: callID1, Name: "bash", Args: json.RawMessage(`{}`)},
					{ID: callID2, Name: "bash", Args: json.RawMessage(`{}`)},
				},
				StopReason: "tool_use",
			}, nil
		},
		Execute: func(ctx context.Context, call provider.ToolCall) provider.Message {
			execCalls++
			if execCalls == 1 {
				cancel() // cancel after first execution
			}
			return provider.Message{
				Role:       provider.RoleTool,
				ToolResult: &provider.ToolResult{CallID: call.ID, Output: "ok"},
			}
		},
		Progress: func(payload json.RawMessage) {},
	})
	if err == nil {
		t.Fatal("expected error after cancellation")
	}
	// Both calls should have been executed (the harness executes all calls
	// so every call gets a matching tool-result in history).
	if execCalls != 2 {
		t.Errorf("execCalls = %d, want 2", execCalls)
	}
}

func TestRegistry_RegisterAndOverride(t *testing.T) {
	r := harness.NewRegistry()
	if !r.Known("default") {
		t.Fatal("expected default harness to be registered")
	}

	customErr := errors.New("custom")
	custom := &stubHarness{name: "default", runErr: customErr}
	r.Register(custom)

	got := r.Get("default")
	if got.Name() != "default" {
		t.Errorf("Name() = %q, want default", got.Name())
	}
	_, err := got.Run(context.Background(), harness.Request{})
	if err == nil {
		t.Fatal("expected error from custom harness")
	}
	if !errors.Is(err, customErr) {
		t.Errorf("custom harness not invoked: err = %v", err)
	}
}

func TestRegistryRegisterFunc(t *testing.T) {
	r := harness.NewRegistry()
	r.RegisterFunc("chess", func(_ context.Context, req harness.Request) (harness.Result, error) {
		return harness.Result{Text: "move for " + req.Agent}, nil
	})
	h, err := r.Resolve("chess")
	if err != nil {
		t.Fatal(err)
	}
	result, err := h.Run(context.Background(), harness.Request{Agent: "stockfish"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "move for stockfish" {
		t.Fatalf("result.Text = %q", result.Text)
	}
}

type stubHarness struct {
	name   string
	runErr error
}

func (s *stubHarness) Name() string { return s.name }

func (s *stubHarness) Run(ctx context.Context, req harness.Request) (harness.Result, error) {
	if s.runErr != nil {
		return harness.Result{}, s.runErr
	}
	return harness.Result{StopReason: "stub"}, nil
}

func TestOutcome_ZeroValue(t *testing.T) {
	var o harness.Outcome
	if o.Text != "" {
		t.Errorf("zero Text = %q, want empty", o.Text)
	}
	if o.StopReason != "" {
		t.Errorf("zero StopReason = %q, want empty", o.StopReason)
	}
}

func TestResult_ZeroValue(t *testing.T) {
	var r harness.Result
	if r.StopReason != "" {
		t.Errorf("zero StopReason = %q, want empty", r.StopReason)
	}
}
