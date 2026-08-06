package external_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/harness"
	"github.com/jonathanung/strike-cli/internal/harness/external"
	"github.com/jonathanung/strike-cli/internal/provider"
	gosdk "github.com/jonathanung/strike-cli/sdk/go/harness"
)

func TestExternalHappyConcurrentProviderCalls(t *testing.T) {
	t.Setenv("GO_WANT_HARNESS_HELPER", "happy")
	adapter, err := external.Command(external.Config{Command: os.Args[0], Args: []string{"-test.run=TestHarnessHelperProcess"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := external.New("fixture", adapter)
	if err != nil {
		t.Fatal(err)
	}
	progress := make(chan json.RawMessage, 1)
	result, err := h(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "selected"},
	}, harness.Provider{
		Call: func(req provider.Request) (harness.ModelResponse, error) {
			if req.Model != "candidate" {
				t.Errorf("callback model = %q, want fixture request", req.Model)
			}
			return harness.ModelResponse{Text: "candidate", StopReason: "stop", Usage: &provider.Usage{OutputTokens: 1}}, nil
		},
	}, func(raw json.RawMessage) { progress <- raw })
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

func TestSDKExampleChooseBest(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required to run the SDK harness example")
	}
	example, err := filepath.Abs(filepath.Join("..", "..", "..", "examples", "harnesses", "choose-best.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := external.Command(external.Config{Command: node, Args: []string{example}})
	if err != nil {
		t.Fatal(err)
	}
	fn, err := external.New("choose-best", adapter)
	if err != nil {
		t.Fatal(err)
	}

	candidates := []string{"first", "the longest candidate", "third choice"}
	providerCalls := 0
	progress := make(chan json.RawMessage, len(candidates))
	result, err := fn(harness.Input{
		Context: context.Background(),
		Request: provider.Request{
			Model:    "fixture-model",
			Messages: []provider.Message{{Role: provider.RoleUser, Text: "solve"}},
		},
	}, harness.Provider{
		Call: func(req provider.Request) (harness.ModelResponse, error) {
			if len(req.Messages) != 2 {
				t.Errorf("provider messages = %#v", req.Messages)
			}
			response := harness.ModelResponse{Text: candidates[providerCalls], StopReason: "end_turn"}
			providerCalls++
			return response, nil
		},
	}, func(payload json.RawMessage) { progress <- payload })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "the longest candidate" || result.StopReason != "end_turn" {
		t.Fatalf("result = %#v", result)
	}
	if providerCalls != 3 || len(progress) != 3 {
		t.Fatalf("provider calls = %d, progress events = %d", providerCalls, len(progress))
	}
}

func TestGoSDKEndToEnd(t *testing.T) {
	fn := newFixture(t, "go-sdk")
	progress := make(chan json.RawMessage, 1)
	result, err := fn(harness.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "fixture", Messages: []provider.Message{{Role: provider.RoleUser, Text: "solve"}}},
	}, harness.Provider{
		Call: func(req provider.Request) (harness.ModelResponse, error) {
			if req.Model != "fixture" || len(req.Messages) != 1 {
				t.Fatalf("provider request = %#v", req)
			}
			return harness.ModelResponse{Text: "go sdk result", StopReason: "end_turn"}, nil
		},
	}, func(payload json.RawMessage) { progress <- payload })
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "go sdk result" || result.StopReason != "end_turn" {
		t.Fatalf("result = %#v", result)
	}
	if got := string(<-progress); got != `{"kind":"go-sdk"}` {
		t.Fatalf("progress = %s", got)
	}
}

func TestExternalRejectsMalformedJSON(t *testing.T) {
	tests := []struct {
		mode string
		// wantAny: error must contain at least one of these (process-group
		// teardown can race scanner EOF vs "file already closed" / exit status).
		wantAny []string
	}{
		{"malformed", []string{"malformed JSON", "exited without harness.complete", "external harness exit", "file already closed"}},
		{"version", []string{"invalid version", "exited without harness.complete", "external harness exit", "file already closed"}},
		{"unknown", []string{"unknown message type", "exited without harness.complete", "external harness exit", "file already closed"}},
		{"missing", []string{"exited without harness.complete", "external harness exit", "file already closed"}},
		{"nonzero", []string{"external harness exit", "exited without harness.complete", "file already closed"}},
		{"duplicate", []string{"duplicate request ID", "exited without harness.complete", "external harness exit", "file already closed"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			h := newFixture(t, tt.mode)
			_, err := h(harness.Input{Context: context.Background()}, harness.Provider{}, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			msg := err.Error()
			for _, want := range tt.wantAny {
				if strings.Contains(msg, want) {
					return
				}
			}
			t.Fatalf("err = %v, want one of %q", err, tt.wantAny)
		})
	}
}

func TestExternalCompleteDoesNotWaitForLiveHarness(t *testing.T) {
	h := newFixture(t, "stays-alive")
	started := time.Now()
	result, err := h(harness.Input{Context: context.Background()}, harness.Provider{}, nil)
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

func TestExternalCompleteDoesNotWaitForOutstandingProviderCall(t *testing.T) {
	h := newFixture(t, "complete-with-call")
	release := make(chan struct{})
	defer close(release)
	result, err := h(harness.Input{Context: context.Background()}, harness.Provider{
		Call: func(provider.Request) (harness.ModelResponse, error) {
			<-release
			return harness.ModelResponse{}, nil
		},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "final" {
		t.Fatalf("result.Text = %q", result.Text)
	}
}

func TestExternalCancellationStopsLiveHarness(t *testing.T) {
	h := newFixture(t, "blocks")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := h(harness.Input{Context: ctx}, harness.Provider{}, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func newFixture(t *testing.T, mode string) harness.Func {
	t.Helper()
	t.Setenv("GO_WANT_HARNESS_HELPER", mode)
	adapter, err := external.Command(external.Config{Command: os.Args[0], Args: []string{"-test.run=TestHarnessHelperProcess"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := external.New("fixture", adapter)
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
	if mode == "go-sdk" {
		err := gosdk.Run(func(input gosdk.Input, p gosdk.Provider, emit gosdk.Emit) (gosdk.Result, error) {
			response, err := p.Call(input.Request)
			if err != nil {
				return gosdk.Result{}, err
			}
			if err := emit(map[string]string{"kind": "go-sdk"}); err != nil {
				return gosdk.Result{}, err
			}
			return gosdk.Result{Text: response.Text, StopReason: response.StopReason}, nil
		})
		if err != nil {
			os.Exit(2)
		}
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	var start struct {
		InvocationID string `json:"invocationId"`
	}
	if json.Unmarshal(scanner.Bytes(), &start) != nil || start.InvocationID == "" {
		os.Exit(2)
	}
	if mode == "malformed" {
		fmt.Println("not-json")
		return
	}
	if mode == "version" {
		fmt.Printf(`{"version":2,"type":"harness.complete","invocationId":%q}`+"\n", start.InvocationID)
		return
	}
	if mode == "unknown" {
		fmt.Printf(`{"version":1,"type":"future.message","invocationId":%q}`+"\n", start.InvocationID)
		return
	}
	if mode == "missing" {
		os.Exit(0)
	}
	if mode == "nonzero" {
		os.Exit(3)
	}
	if mode == "duplicate" {
		fmt.Printf(`{"version":1,"type":"provider.call","invocationId":%q,"callId":"same","request":{"model":"candidate","messages":[]}}`+"\n", start.InvocationID)
		fmt.Printf(`{"version":1,"type":"provider.call","invocationId":%q,"callId":"same","request":{"model":"candidate","messages":[]}}`+"\n", start.InvocationID)
		time.Sleep(time.Hour)
	}
	if mode == "stays-alive" {
		fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":%q,"text":"final"}`+"\n", start.InvocationID)
		time.Sleep(time.Hour)
	}
	if mode == "complete-with-call" {
		fmt.Printf(`{"version":1,"type":"provider.call","invocationId":%q,"callId":"pending","request":{"model":"candidate","messages":[]}}`+"\n", start.InvocationID)
		fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":%q,"text":"final"}`+"\n", start.InvocationID)
		time.Sleep(time.Hour)
	}
	if mode == "blocks" {
		time.Sleep(time.Hour)
	}
	fmt.Printf(`{"version":1,"type":"progress.emit","invocationId":%q,"payload":{"message":"working"}}`+"\n", start.InvocationID)
	for _, callID := range []string{"a", "b"} {
		fmt.Printf(`{"version":1,"type":"provider.call","invocationId":%q,"callId":%q,"request":{"model":"candidate","messages":[]}}`+"\n", start.InvocationID, callID)
	}
	completions := 0
	for scanner.Scan() {
		var m struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) == nil && m.Type == "provider.result" {
			completions++
			if completions == 2 {
				fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":%q,"text":"final","stopReason":"end_turn"}`+"\n", start.InvocationID)
				return
			}
		}
	}
}
