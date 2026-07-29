package chatgpt

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestNewWiresNameHeadersAndAuth(t *testing.T) {
	var gotAuth, gotAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("ChatGPT-Account-Id")
		if r.Header.Get("OpenAI-Beta") != "responses=experimental" {
			t.Errorf("OpenAI-Beta = %q", r.Header.Get("OpenAI-Beta"))
		}
		if r.Header.Get("originator") != "codex_cli_rs" {
			t.Errorf("originator = %q", r.Header.Get("originator"))
		}
		if r.Header.Get("session_id") == "" {
			t.Error("session_id missing")
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed"}}`+"\n")
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, string, error) {
		return "access-tok", "acct-99", nil
	})
	p.endpoint = srv.URL
	if p.Name() != "openai (chatgpt)" {
		t.Errorf("Name = %q", p.Name())
	}

	stream, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if gotAuth != "Bearer access-tok" {
		t.Errorf("Authorization = %q, want Bearer access-tok", gotAuth)
	}
	if gotAccount != "acct-99" {
		t.Errorf("ChatGPT-Account-Id = %q, want acct-99", gotAccount)
	}
}

func TestStreamAuthError(t *testing.T) {
	want := errors.New("token expired")
	p := New(func(context.Context) (string, string, error) {
		return "", "", want
	})
	p.endpoint = "http://127.0.0.1:1" // never reached
	_, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5"})
	if !errors.Is(err, want) {
		t.Fatalf("Stream err = %v, want %v", err, want)
	}
}

func TestStreamEmitsReasoningSummaryDeltas(t *testing.T) {
	const sse = "" +
		`data: {"type":"response.reasoning_summary_text.delta","delta":"plan "}` + "\n" +
		`data: {"type":"response.reasoning_text.delta","delta":"detail"}` + "\n" +
		`data: {"type":"response.output_text.delta","delta":"done"}` + "\n" +
		`data: {"type":"response.completed","response":{"status":"completed"}}` + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, string, error) { return "t", "a", nil })
	p.endpoint = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5", Messages: []provider.Message{
		{Role: provider.RoleUser, Text: "hi"},
	}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning, text string
	for ev := range stream {
		switch ev.Type {
		case provider.EventReasoning:
			reasoning += ev.Text
		case provider.EventTextDelta:
			text += ev.Text
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if reasoning != "plan detail" {
		t.Errorf("reasoning = %q, want %q", reasoning, "plan detail")
	}
	if text != "done" {
		t.Errorf("text = %q, want done", text)
	}
}

func TestStreamEmitsTextToolCallAndDone(t *testing.T) {
	const sse = "" +
		`data: {"type":"response.output_text.delta","delta":"hi "}` + "\n" +
		`data: {"type":"response.output_text.delta","delta":"there"}` + "\n" +
		`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"bash","arguments":"{\"cmd\":\"ls\"}","call_id":"c1"}}` + "\n" +
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14}}}` + "\n"

	var gotBody map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sse)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, string, error) { return "t", "a", nil })
	p.endpoint = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:  "gpt-5.5",
		System: "be brief",
		Effort: provider.EffortLow,
		Tools: []provider.ToolSchema{{
			Name:        "bash",
			Description: "run shell",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "list"},
			{
				Role: provider.RoleAssistant,
				Text: "ok",
				ToolCalls: []provider.ToolCall{{
					ID: "prev", Name: "bash", Args: json.RawMessage(`{"cmd":"pwd"}`),
				}},
			},
			{
				Role:       provider.RoleTool,
				ToolResult: &provider.ToolResult{CallID: "prev", Output: "/tmp"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var text string
	var calls []provider.ToolCall
	var done *provider.StreamEvent
	for ev := range stream {
		switch ev.Type {
		case provider.EventTextDelta:
			text += ev.Text
		case provider.EventToolCall:
			if ev.ToolCall != nil {
				calls = append(calls, *ev.ToolCall)
			}
		case provider.EventDone:
			cp := ev
			done = &cp
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if text != "hi there" {
		t.Errorf("text = %q, want %q", text, "hi there")
	}
	if len(calls) != 1 || calls[0].ID != "c1" || calls[0].Name != "bash" {
		t.Errorf("tool calls = %+v", calls)
	}
	if done == nil || done.StopReason != "completed" {
		t.Fatalf("done = %+v", done)
	}
	if done.Usage == nil || done.Usage.InputTokens != 10 || done.Usage.OutputTokens != 4 || done.Usage.TotalTokens != 14 {
		t.Errorf("Usage = %+v", done.Usage)
	}

	// Wire body shape from Stream request mapping.
	if string(gotBody["model"]) != `"gpt-5.5"` {
		t.Errorf("model = %s", gotBody["model"])
	}
	if string(gotBody["instructions"]) != `"be brief"` {
		t.Errorf("instructions = %s", gotBody["instructions"])
	}
	var reasoning map[string]string
	if err := json.Unmarshal(gotBody["reasoning"], &reasoning); err != nil || reasoning["effort"] != "low" {
		t.Errorf("reasoning = %s", gotBody["reasoning"])
	}
	if string(gotBody["store"]) != "false" || string(gotBody["stream"]) != "true" {
		t.Errorf("store/stream = %s / %s", gotBody["store"], gotBody["stream"])
	}
}

func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"auth","message":"bad token"}}`))
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, string, error) { return "t", "a", nil })
	p.endpoint = srv.URL
	_, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5"})
	if err == nil {
		t.Fatal("want HTTP error")
	}
	if !strings.Contains(err.Error(), "bad token") {
		t.Errorf("err = %v", err)
	}
}

func TestResponsesRequestCarriesReasoningEffort(t *testing.T) {
	out := toResponsesRequest(provider.Request{Model: "gpt-5.5", Effort: provider.EffortLow})
	if out.Reasoning == nil {
		t.Fatal("reasoning omitted, want effort low")
	}
	if out.Reasoning.Effort != "low" {
		t.Errorf("reasoning.effort = %q, want low", out.Reasoning.Effort)
	}
}

// TestToResponsesRequestPromptCacheKey pins Codex/OpenClaw-shaped cache key
// on the chatgpt.com Responses body (and omits blank keys).
func TestToResponsesRequestPromptCacheKey(t *testing.T) {
	out := toResponsesRequest(provider.Request{
		Model:    "gpt-5.5",
		CacheKey: "  sess-chatgpt  ",
	})
	if out.PromptCacheKey != "sess-chatgpt" {
		t.Fatalf("PromptCacheKey = %q, want sess-chatgpt", out.PromptCacheKey)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"prompt_cache_key":"sess-chatgpt"`) {
		t.Fatalf("wire missing prompt_cache_key: %s", raw)
	}
	// Must never emit prompt_cache_retention — chatgpt.com 400s on it.
	if strings.Contains(string(raw), "prompt_cache_retention") {
		t.Fatalf("chatgpt body must omit prompt_cache_retention: %s", raw)
	}

	empty := toResponsesRequest(provider.Request{Model: "gpt-5.5", CacheKey: "   "})
	rawEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawEmpty), "prompt_cache_key") {
		t.Fatalf("blank CacheKey must omit prompt_cache_key: %s", rawEmpty)
	}
}

// TestToResponsesRequestIncludesEncryptedReasoning matches Codex include list.
func TestToResponsesRequestIncludesEncryptedReasoning(t *testing.T) {
	out := toResponsesRequest(provider.Request{Model: "gpt-5.5"})
	if len(out.Include) != 1 || out.Include[0] != "reasoning.encrypted_content" {
		t.Fatalf("Include = %#v, want [reasoning.encrypted_content]", out.Include)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"reasoning.encrypted_content"`) {
		t.Fatalf("wire missing include entry: %s", raw)
	}
}

// TestStreamSendsPromptCacheKeyOnWire pins request body cache key + usage map.
func TestStreamSendsPromptCacheKeyOnWire(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("session_id") == "" {
			t.Error("session_id header missing")
		}
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":5,"total_tokens":105,"input_tokens_details":{"cached_tokens":80,"cache_write_tokens":10}}}}`+"\n")
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, string, error) { return "t", "a", nil })
	p.endpoint = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		CacheKey: "session-531",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var done *provider.StreamEvent
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == provider.EventDone {
			cp := ev
			done = &cp
		}
	}
	if !strings.Contains(string(gotBody), `"prompt_cache_key":"session-531"`) {
		t.Fatalf("request body missing prompt_cache_key: %s", gotBody)
	}
	if !strings.Contains(string(gotBody), `"reasoning.encrypted_content"`) {
		t.Fatalf("request body missing include: %s", gotBody)
	}
	if done == nil || done.Usage == nil {
		t.Fatalf("done/usage = %+v", done)
	}
	// 100 input − 80 cached − 10 write = 10 uncached input
	if done.Usage.InputTokens != 10 || done.Usage.CacheReadTokens != 80 || done.Usage.CacheCreationTokens != 10 {
		t.Errorf("Usage = %+v, want input=10 cacheRead=80 cacheWrite=10", done.Usage)
	}
	if done.Usage.OutputTokens != 5 || done.Usage.TotalTokens != 105 {
		t.Errorf("Usage output/total = %+v", done.Usage)
	}
}

func TestStreamUsageToProviderCacheBreakout(t *testing.T) {
	tests := []struct {
		name string
		in   *streamUsage
		want provider.Usage
	}{
		{
			name: "nil",
			in:   nil,
		},
		{
			name: "no details",
			in:   &streamUsage{InputTokens: 50, OutputTokens: 3, TotalTokens: 53},
			want: provider.Usage{InputTokens: 50, OutputTokens: 3, TotalTokens: 53},
		},
		{
			name: "cached and write subsets",
			in: &streamUsage{
				InputTokens: 200, OutputTokens: 1, TotalTokens: 201,
				InputTokensDetails: &streamInputTokDetails{CachedTokens: 150, CacheWriteTokens: 20},
			},
			want: provider.Usage{InputTokens: 30, OutputTokens: 1, TotalTokens: 201, CacheReadTokens: 150, CacheCreationTokens: 20},
		},
		{
			name: "clamps oversize cache counts",
			in: &streamUsage{
				InputTokens:        10,
				InputTokensDetails: &streamInputTokDetails{CachedTokens: 100, CacheWriteTokens: 50},
			},
			want: provider.Usage{InputTokens: 0, CacheReadTokens: 10, CacheCreationTokens: 0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := streamUsageToProvider(tt.in)
			if tt.in == nil {
				if got != nil {
					t.Fatalf("got %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatal("got nil")
			}
			if *got != tt.want {
				t.Errorf("got %+v, want %+v", *got, tt.want)
			}
		})
	}
}

// TestResponsesRequestOmitsReasoningWhenUnset keeps the nested object out of
// the body rather than sending an empty one.
func TestResponsesRequestOmitsReasoningWhenUnset(t *testing.T) {
	out := toResponsesRequest(provider.Request{Model: "gpt-5.5"})
	if out.Reasoning != nil {
		t.Fatalf("reasoning = %+v, want omitted", out.Reasoning)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if _, present := keys["reasoning"]; present {
		t.Errorf("body carries reasoning with no effort set: %s", data)
	}
}

func TestToResponsesRequestMapsRolesToolsAndDefaults(t *testing.T) {
	out := toResponsesRequest(provider.Request{
		Model:  "m",
		System: "sys",
		Tools: []provider.ToolSchema{{
			Name: "bash", Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "u"},
			{
				Role: provider.RoleAssistant,
				Text: "a",
				ToolCalls: []provider.ToolCall{{
					ID: "c1", Name: "bash", Args: json.RawMessage(`{"x":1}`),
				}},
			},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: "out"}},
			{Role: provider.RoleAssistant}, // text-less assistant: only tool calls if any
		},
	})
	if !out.Stream || out.Store || out.ToolChoice != "auto" {
		t.Errorf("defaults: stream=%v store=%v tool_choice=%q", out.Stream, out.Store, out.ToolChoice)
	}
	if out.Instructions != "sys" || len(out.Tools) != 1 || out.Tools[0].Name != "bash" {
		t.Errorf("instructions/tools = %q / %+v", out.Instructions, out.Tools)
	}
	if len(out.Input) != 4 {
		// user msg + assistant text + function_call + function_call_output
		// (text-less assistant with no tools contributes nothing)
		t.Fatalf("input len = %d, want 4", len(out.Input))
	}
	if out.Input[0].Type != "message" || out.Input[0].Role != "user" || out.Input[0].Content[0].Type != "input_text" {
		t.Errorf("user item = %+v", out.Input[0])
	}
	if out.Input[1].Type != "message" || out.Input[1].Role != "assistant" || out.Input[1].Content[0].Type != "output_text" {
		t.Errorf("assistant text = %+v", out.Input[1])
	}
	if out.Input[2].Type != "function_call" || out.Input[2].CallID != "c1" || out.Input[2].Arguments != `{"x":1}` {
		t.Errorf("function_call = %+v", out.Input[2])
	}
	if out.Input[3].Type != "function_call_output" || out.Input[3].CallID != "c1" || out.Input[3].Output == nil || *out.Input[3].Output != "out" {
		t.Errorf("function_call_output = %+v", out.Input[3])
	}
}

func TestResponsesRequestPairsDeniedCallAndEmptyFallbackOutput(t *testing.T) {
	out := toResponsesRequest(provider.Request{Messages: []provider.Message{
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: "git-denied", Name: "bash", Args: json.RawMessage(`{"command":"git diff"}`),
			}},
		},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "git-denied", Output: "Permission denied."}},
		{
			Role: provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{
				ID: "empty-fallback", Name: "webfetch", Args: json.RawMessage(`{"url":"https://example.com"}`),
			}},
		},
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "empty-fallback", Output: ""}},
	}})

	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []map[string]json.RawMessage `json:"input"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	calls := make(map[string]bool)
	outputs := make(map[string]json.RawMessage)
	for _, item := range body.Input {
		var typ, callID string
		if err := json.Unmarshal(item["type"], &typ); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(item["call_id"], &callID); err != nil {
			t.Fatal(err)
		}
		switch typ {
		case "function_call":
			calls[callID] = true
		case "function_call_output":
			output, ok := item["output"]
			if !ok {
				t.Fatalf("output omitted for call %q: %s", callID, data)
			}
			outputs[callID] = output
		}
	}
	for callID := range calls {
		if _, ok := outputs[callID]; !ok {
			t.Errorf("function call %q has no matching output: %s", callID, data)
		}
	}
	if got := string(outputs["git-denied"]); got != `"Permission denied."` {
		t.Errorf("denial output = %s", got)
	}
	if got := string(outputs["empty-fallback"]); got != `""` {
		t.Errorf("empty fallback output = %s, want required empty string", got)
	}
}

func TestReadStreamMapsUsageFromResponseCompleted(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"type":"response.output_text.delta","delta":"hi"}`,
		`data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":80,"output_tokens":15,"total_tokens":95}}}`,
		"",
	}, "\n")
	p := &Provider{}
	ch := make(chan provider.StreamEvent, 8)
	if err := p.readStream(strings.NewReader(sse), ch); err != nil {
		t.Fatalf("readStream: %v", err)
	}
	close(ch)

	var done *provider.StreamEvent
	for ev := range ch {
		if ev.Type == provider.EventDone {
			cp := ev
			done = &cp
		}
	}
	if done == nil {
		t.Fatal("missing EventDone")
	}
	if done.StopReason != "completed" {
		t.Errorf("StopReason = %q, want completed", done.StopReason)
	}
	if done.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if done.Usage.InputTokens != 80 || done.Usage.OutputTokens != 15 || done.Usage.TotalTokens != 95 {
		t.Errorf("Usage = %+v, want input=80 output=15 total=95", done.Usage)
	}
	if done.Usage.Estimated {
		t.Error("Estimated must be false")
	}
}

func TestReadStreamOmitsUsageWhenResponseCompletedHasNone(t *testing.T) {
	sse := `data: {"type":"response.completed","response":{"status":"completed"}}` + "\n"
	p := &Provider{}
	ch := make(chan provider.StreamEvent, 4)
	if err := p.readStream(strings.NewReader(sse), ch); err != nil {
		t.Fatalf("readStream: %v", err)
	}
	close(ch)
	var sawDone bool
	for ev := range ch {
		if ev.Type == provider.EventDone {
			sawDone = true
			if ev.Usage != nil {
				t.Errorf("Usage = %+v, want nil", ev.Usage)
			}
		}
	}
	if !sawDone {
		t.Fatal("missing EventDone")
	}
}

func TestReadStreamSSEEdgeCases(t *testing.T) {
	tests := []struct {
		name    string
		sse     string
		wantErr string
		check   func(t *testing.T, events []provider.StreamEvent)
	}{
		{
			name: "empty function args become object",
			sse: strings.Join([]string{
				`data: {"type":"response.output_item.done","item":{"type":"function_call","name":"bash","arguments":"","call_id":"c0"}}`,
				`data: {"type":"response.completed","response":{"status":"completed"}}`,
				"",
			}, "\n"),
			check: func(t *testing.T, events []provider.StreamEvent) {
				var call *provider.ToolCall
				for _, ev := range events {
					if ev.Type == provider.EventToolCall {
						call = ev.ToolCall
					}
				}
				if call == nil || string(call.Args) != `{}` {
					t.Fatalf("call = %+v, want args {}", call)
				}
			},
		},
		{
			name: "ignores non-data lines malformed and done sentinel",
			sse: strings.Join([]string{
				`: keep-alive`,
				`event: ping`,
				`data: not-json`,
				`data: [DONE]`,
				`data: `,
				`data: {"type":"response.output_item.done","item":{"type":"message"}}`,
				`data: {"type":"response.completed","response":{"status":"completed"}}`,
				"",
			}, "\n"),
			check: func(t *testing.T, events []provider.StreamEvent) {
				if len(events) != 1 || events[0].Type != provider.EventDone {
					t.Fatalf("events = %+v, want single Done", events)
				}
			},
		},
		{
			name:    "response.failed with message",
			sse:     `data: {"type":"response.failed","response":{"error":{"message":"quota"}}}` + "\n",
			wantErr: "quota",
		},
		{
			name:    "response.failed default message",
			sse:     `data: {"type":"response.failed"}` + "\n",
			wantErr: "response failed",
		},
		{
			name:    "top-level error event",
			sse:     `data: {"type":"error","message":"boom"}` + "\n",
			wantErr: "boom",
		},
		{
			name:    "stream ends without completed",
			sse:     `data: {"type":"response.output_text.delta","delta":"x"}` + "\n",
			wantErr: "stream ended without response.completed",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &Provider{}
			ch := make(chan provider.StreamEvent, 16)
			err := p.readStream(strings.NewReader(tt.sse), ch)
			close(ch)
			var events []provider.StreamEvent
			for ev := range ch {
				events = append(events, ev)
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("readStream: %v", err)
			}
			if tt.check != nil {
				tt.check(t, events)
			}
		})
	}
}

func TestNewUUIDFormat(t *testing.T) {
	id := newUUID()
	parts := strings.Split(id, "-")
	if len(parts) != 5 || len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Fatalf("uuid = %q, want 8-4-4-4-12", id)
	}
	// version nibble is 4
	if parts[2][0] != '4' {
		t.Errorf("version nibble = %c, want 4", parts[2][0])
	}
}

func TestUserMessageIncludesInputImage(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47}
	req := provider.Request{
		Model: "gpt",
		Messages: []provider.Message{{
			Role:   provider.RoleUser,
			Text:   "hi",
			Images: []provider.Image{{MIME: "image/png", Data: png}},
		}},
	}
	out := toResponsesRequest(req)
	if len(out.Input) != 1 {
		t.Fatalf("input = %d", len(out.Input))
	}
	blocks := out.Input[0].Content
	if len(blocks) != 2 {
		t.Fatalf("blocks = %d", len(blocks))
	}
	if blocks[0].Type != "input_text" || blocks[0].Text != "hi" {
		t.Errorf("text = %+v", blocks[0])
	}
	if blocks[1].Type != "input_image" || !strings.HasPrefix(blocks[1].ImageURL, "data:image/png;base64,") {
		t.Errorf("image = %+v", blocks[1])
	}
}
