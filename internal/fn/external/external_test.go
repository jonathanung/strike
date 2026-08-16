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
	"sync"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/internal/fn"
	"github.com/jonathanung/strike-cli/internal/fn/external"
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
	result, err := h(fn.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "selected"},
	}, fn.Provider{
		Call: func(req provider.Request) (fn.ModelResponse, error) {
			if req.Model != "candidate" {
				t.Errorf("callback model = %q, want fixture request", req.Model)
			}
			return fn.ModelResponse{Text: "candidate", StopReason: "stop", Usage: &provider.Usage{OutputTokens: 1}}, nil
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

func TestExternalToolExecute(t *testing.T) {
	t.Setenv("GO_WANT_HARNESS_HELPER", "tool-execute")
	adapter, err := external.Command(external.Config{Command: os.Args[0], Args: []string{"-test.run=TestHarnessHelperProcess"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := external.New("fixture", adapter)
	if err != nil {
		t.Fatal(err)
	}
	var sawCall provider.ToolCall
	result, err := h(fn.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "selected"},
		Tools: fn.Tools{
			Execute: func(call provider.ToolCall) (provider.ToolResult, error) {
				sawCall = call
				return provider.ToolResult{CallID: call.ID, Output: "tool-body"}, nil
			},
		},
	}, fn.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "tool-body" || result.StopReason != "end_turn" {
		t.Fatalf("result = %#v", result)
	}
	if sawCall.Name != "read" || string(sawCall.Args) != `{"filePath":"x"}` {
		t.Fatalf("tool call = %#v", sawCall)
	}
}

func TestExternalToolExecuteStructuredError(t *testing.T) {
	t.Setenv("GO_WANT_HARNESS_HELPER", "tool-execute")
	adapter, err := external.Command(external.Config{Command: os.Args[0], Args: []string{"-test.run=TestHarnessHelperProcess"}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := external.New("fixture", adapter)
	if err != nil {
		t.Fatal(err)
	}
	result, err := h(fn.Input{
		Context: context.Background(),
		Tools: fn.Tools{
			Execute: func(call provider.ToolCall) (provider.ToolResult, error) {
				return provider.ToolResult{
					CallID:    call.ID,
					Output:    "Permission denied.",
					IsError:   true,
					ErrorCode: "permission_denied",
				}, nil
			},
		},
	}, fn.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "err:permission_denied" {
		t.Fatalf("result = %#v", result)
	}
}

func TestGoSDKToolExecute(t *testing.T) {
	run := newFixture(t, "go-sdk-tool")
	result, err := run(fn.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "fixture"},
		Tools: fn.Tools{
			Execute: func(call provider.ToolCall) (provider.ToolResult, error) {
				if call.Name != "read" {
					t.Fatalf("name = %q", call.Name)
				}
				return provider.ToolResult{CallID: call.ID, Output: "sdk-tool-ok"}, nil
			},
		},
	}, fn.Provider{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "sdk-tool-ok" {
		t.Fatalf("result = %#v", result)
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
	run, err := external.New("choose-best", adapter)
	if err != nil {
		t.Fatal(err)
	}

	candidates := []string{"first", "the longest candidate", "third choice"}
	providerCalls := 0
	progress := make(chan json.RawMessage, len(candidates))
	result, err := run(fn.Input{
		Context: context.Background(),
		Request: provider.Request{
			Model:    "fixture-model",
			Messages: []provider.Message{{Role: provider.RoleUser, Text: "solve"}},
		},
	}, fn.Provider{
		Call: func(req provider.Request) (fn.ModelResponse, error) {
			if len(req.Messages) != 2 {
				t.Errorf("provider messages = %#v", req.Messages)
			}
			response := fn.ModelResponse{Text: candidates[providerCalls], StopReason: "end_turn"}
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
	run := newFixture(t, "go-sdk")
	progress := make(chan json.RawMessage, 1)
	result, err := run(fn.Input{
		Context: context.Background(),
		Request: provider.Request{Model: "fixture", Messages: []provider.Message{{Role: provider.RoleUser, Text: "solve"}}},
	}, fn.Provider{
		Call: func(req provider.Request) (fn.ModelResponse, error) {
			if req.Model != "fixture" || len(req.Messages) != 1 {
				t.Fatalf("provider request = %#v", req)
			}
			return fn.ModelResponse{Text: "go sdk result", StopReason: "end_turn"}, nil
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
			_, err := h(fn.Input{Context: context.Background()}, fn.Provider{}, nil)
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
	result, err := h(fn.Input{Context: context.Background()}, fn.Provider{}, nil)
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
	result, err := h(fn.Input{Context: context.Background()}, fn.Provider{
		Call: func(provider.Request) (fn.ModelResponse, error) {
			<-release
			return fn.ModelResponse{}, nil
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
	_, err := h(fn.Input{Context: ctx}, fn.Provider{}, nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("cancellation took %v", elapsed)
	}
}

func newFixture(t *testing.T, mode string) fn.Func {
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
	bumpStartCount()
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
	if mode == "go-sdk-tool" {
		err := gosdk.Run(func(input gosdk.Input, _ gosdk.Provider, _ gosdk.Emit) (gosdk.Result, error) {
			if input.Tools == nil {
				return gosdk.Result{}, errors.New("tools unavailable")
			}
			res, err := input.Tools.Execute(gosdk.ToolCall{
				Name:      "read",
				Arguments: json.RawMessage(`{"filePath":"x"}`),
			})
			if err != nil {
				return gosdk.Result{}, err
			}
			if res.IsError {
				return gosdk.Result{Text: "err:" + res.ErrorCode, StopReason: "end_turn"}, nil
			}
			return gosdk.Result{Text: res.Output, StopReason: "end_turn"}, nil
		})
		if err != nil {
			os.Exit(2)
		}
		return
	}
	if mode == "go-sdk-worker" {
		err := gosdk.RunWorker(func(input gosdk.Input, p gosdk.Provider, _ gosdk.Emit) (gosdk.Result, error) {
			response, err := p.Call(input.Request)
			if err != nil {
				return gosdk.Result{}, err
			}
			return gosdk.Result{Text: response.Text, StopReason: response.StopReason}, nil
		})
		if err != nil {
			os.Exit(2)
		}
		return
	}
	if mode == "count-start" {
		// Oneshot-compatible: no harness.ready prologue.
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			os.Exit(2)
		}
		var start struct {
			InvocationID string `json:"invocationId"`
			Request      *struct {
				Model string `json:"model"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &start) != nil || start.InvocationID == "" || start.Request == nil {
			os.Exit(2)
		}
		fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":%q,"text":%q,"stopReason":"end_turn"}`+"\n", start.InvocationID, start.Request.Model)
		return
	}
	if mode == "persistent-echo" || mode == "persistent-block" || mode == "persistent-crash" {
		runPersistentHelper(mode)
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		os.Exit(2)
	}
	var start struct {
		InvocationID string   `json:"invocationId"`
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(scanner.Bytes(), &start) != nil || start.InvocationID == "" {
		os.Exit(2)
	}
	if mode == "tool-execute" {
		// Require additive capability advertisement.
		hasTool := false
		for _, c := range start.Capabilities {
			if c == "tool.execute" {
				hasTool = true
				break
			}
		}
		if !hasTool {
			fmt.Printf(`{"version":1,"type":"harness.error","invocationId":%q,"error":"missing tool.execute capability"}`+"\n", start.InvocationID)
			return
		}
		fmt.Printf(`{"version":1,"type":"tool.execute","invocationId":%q,"callId":"t1","name":"read","arguments":{"filePath":"x"}}`+"\n", start.InvocationID)
		for scanner.Scan() {
			var m struct {
				Type      string `json:"type"`
				Output    string `json:"output"`
				IsError   bool   `json:"isError"`
				ErrorCode string `json:"errorCode"`
			}
			if json.Unmarshal(scanner.Bytes(), &m) != nil || m.Type != "tool.result" {
				continue
			}
			text := m.Output
			if m.IsError {
				text = "err:" + m.ErrorCode
			}
			fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":%q,"text":%q,"stopReason":"end_turn"}`+"\n", start.InvocationID, text)
			return
		}
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

func bumpStartCount() {
	path := os.Getenv("GO_HARNESS_START_COUNT_FILE")
	if path == "" {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = f.WriteString("1\n")
	_ = f.Close()
}

func runPersistentHelper(mode string) {
	fmt.Printf(`{"version":1,"type":"harness.ready"}` + "\n")
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	type invState struct {
		cancel chan struct{}
	}
	var mu sync.Mutex
	active := map[string]*invState{}
	for scanner.Scan() {
		var m struct {
			Type         string `json:"type"`
			InvocationID string `json:"invocationId"`
			Reason       string `json:"reason"`
			Request      *struct {
				Model  string `json:"model"`
				System string `json:"system"`
			} `json:"request"`
		}
		if json.Unmarshal(scanner.Bytes(), &m) != nil {
			os.Exit(2)
		}
		switch m.Type {
		case "harness.shutdown":
			return
		case "harness.cancel":
			mu.Lock()
			st := active[m.InvocationID]
			if st != nil {
				select {
				case <-st.cancel:
				default:
					close(st.cancel)
				}
				delete(active, m.InvocationID)
			}
			mu.Unlock()
			// Best-effort error so host unblocks without killing when we cooperate.
			fmt.Printf(`{"version":1,"type":"harness.error","invocationId":%q,"code":"canceled","error":%q}`+"\n", m.InvocationID, m.Reason)
		case "harness.start":
			if m.InvocationID == "" || m.Request == nil {
				os.Exit(2)
			}
			if mode == "persistent-crash" {
				os.Exit(3)
			}
			st := &invState{cancel: make(chan struct{})}
			mu.Lock()
			active[m.InvocationID] = st
			mu.Unlock()
			id := m.InvocationID
			model := m.Request.Model
			system := m.Request.System
			go func() {
				defer func() {
					mu.Lock()
					delete(active, id)
					mu.Unlock()
				}()
				if mode == "persistent-block" && system != "quick" {
					select {
					case <-st.cancel:
						return
					case <-time.After(time.Hour):
					}
				}
				if system == "slow" {
					select {
					case <-st.cancel:
						return
					case <-time.After(50 * time.Millisecond):
					}
				}
				select {
				case <-st.cancel:
					return
				default:
					fmt.Printf(`{"version":1,"type":"harness.complete","invocationId":%q,"text":%q,"stopReason":"end_turn"}`+"\n", id, model)
				}
			}()
		}
	}
}
