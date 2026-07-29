package openaicompat

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestNewOpenAI(t *testing.T) {
	p := NewOpenAI(func(context.Context) (string, error) { return "k", nil })
	if p.Name() != "openai" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.baseURL != "https://api.openai.com/v1" {
		t.Errorf("baseURL = %q", p.baseURL)
	}
	if !p.priorityTier {
		t.Error("priorityTier want true for OpenAI platform")
	}
}

func TestNewXAI(t *testing.T) {
	p := NewXAI(func(context.Context) (string, error) { return "k", nil })
	if p.Name() != "xai" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.baseURL != "https://api.x.ai/v1" {
		t.Errorf("baseURL = %q", p.baseURL)
	}
	if p.priorityTier {
		t.Error("priorityTier want false for xAI")
	}
}

func TestNewWithHeadersSkipsEmptyKeys(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = map[string]string{
			"X-Custom": r.Header.Get("X-Custom"),
			"Empty":    r.Header.Get(""),
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := NewWithHeaders("gw", srv.URL, func(context.Context) (string, error) { return "tok", nil }, map[string]string{
		"X-Custom": "yes",
		"":         "skip-me",
	})
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if got["X-Custom"] != "yes" {
		t.Errorf("X-Custom = %q", got["X-Custom"])
	}
}

func TestStreamAuthBearerAndPath(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "secret", nil })
	stream, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/chat/completions" {
		t.Errorf("path = %q, want /chat/completions", gotPath)
	}
}

func TestStreamAuthError(t *testing.T) {
	want := errors.New("no creds")
	p := New("openai", "http://127.0.0.1:1", func(context.Context) (string, error) {
		return "", want
	})
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream start err = %v, want nil (error is async)", err)
	}
	var got error
	for ev := range stream {
		if ev.Type == provider.EventError {
			got = ev.Err
		}
	}
	if !errors.Is(got, want) {
		t.Fatalf("stream err = %v, want %v", got, want)
	}
}

func TestStreamEmitsReasoningContentBeforeAnswer(t *testing.T) {
	const body = `{
		"choices":[{
			"message":{
				"role":"assistant",
				"reasoning_content":"internal plan",
				"content":"answer"
			},
			"finish_reason":"stop"
		}]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	p := NewOpenAI(func(context.Context) (string, error) { return "k", nil })
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var reasoning, text string
	var order []provider.StreamEventType
	for ev := range stream {
		order = append(order, ev.Type)
		switch ev.Type {
		case provider.EventReasoning:
			reasoning += ev.Text
		case provider.EventTextDelta:
			text += ev.Text
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if reasoning != "internal plan" || text != "answer" {
		t.Errorf("reasoning=%q text=%q", reasoning, text)
	}
	if len(order) < 2 || order[0] != provider.EventReasoning || order[1] != provider.EventTextDelta {
		t.Errorf("event order = %v, want reasoning then text", order)
	}
}

func TestStreamEmitsTextToolCallsAndDone(t *testing.T) {
	const body = `{
		"choices":[{
			"message":{
				"role":"assistant",
				"content":"checking",
				"tool_calls":[
					{"id":"t1","type":"function","function":{"name":"bash","arguments":"{\"cmd\":\"ls\"}"}},
					{"id":"t2","type":"function","function":{"name":"read","arguments":""}}
				]
			},
			"finish_reason":"tool_calls"
		}],
		"usage":{"prompt_tokens":11,"completion_tokens":5,"total_tokens":16}
	}`
	var gotReq chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	p := NewOpenAI(func(context.Context) (string, error) { return "k", nil })
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		System:   "sys",
		Effort:   provider.EffortHigh,
		Priority: true,
		Tools: []provider.ToolSchema{{
			Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "go"},
			{
				Role: provider.RoleAssistant, Text: "sure",
				ToolCalls: []provider.ToolCall{{ID: "old", Name: "bash", Args: json.RawMessage(`{}`)}},
			},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "old", Output: "ok"}},
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
	if text != "checking" {
		t.Errorf("text = %q", text)
	}
	if len(calls) != 2 {
		t.Fatalf("calls = %d, want 2", len(calls))
	}
	if calls[0].ID != "t1" || calls[0].Name != "bash" || string(calls[0].Args) != `{"cmd":"ls"}` {
		t.Errorf("call0 = %+v", calls[0])
	}
	if calls[1].ID != "t2" || string(calls[1].Args) != `{}` {
		t.Errorf("empty args call = %+v, want args {}", calls[1])
	}
	if done == nil || done.StopReason != "tool_calls" {
		t.Fatalf("done = %+v", done)
	}
	if done.Usage == nil || done.Usage.InputTokens != 11 || done.Usage.OutputTokens != 5 || done.Usage.TotalTokens != 16 {
		t.Errorf("Usage = %+v", done.Usage)
	}

	if gotReq.Model != "gpt-5.5" || gotReq.ReasoningEffort != "high" || gotReq.ServiceTier != "priority" {
		t.Errorf("req fields = %+v", gotReq)
	}
	if len(gotReq.Messages) < 4 || gotReq.Messages[0].Role != "system" {
		t.Errorf("messages = %+v", gotReq.Messages)
	}
	if len(gotReq.Tools) != 1 || gotReq.Tools[0].Function.Name != "bash" {
		t.Errorf("tools = %+v", gotReq.Tools)
	}
}

func TestStreamNoChoices(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "k", nil })
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got error
	for ev := range stream {
		if ev.Type == provider.EventError {
			got = ev.Err
		}
	}
	if got == nil || !strings.Contains(got.Error(), "no choices") {
		t.Fatalf("err = %v, want no choices", got)
	}
}

func TestStreamHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"type":"rate_limit","message":"slow down"}}`))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "k", nil })
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got error
	for ev := range stream {
		if ev.Type == provider.EventError {
			got = ev.Err
		}
	}
	if got == nil || !strings.Contains(got.Error(), "slow down") {
		t.Fatalf("err = %v", got)
	}
}

func TestStreamContentOnlyOmitsToolEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "k", nil })
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var textEvents, toolEvents, doneEvents int
	for ev := range stream {
		switch ev.Type {
		case provider.EventTextDelta:
			textEvents++
		case provider.EventToolCall:
			toolEvents++
		case provider.EventDone:
			doneEvents++
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if textEvents != 0 || toolEvents != 0 || doneEvents != 1 {
		t.Fatalf("text=%d tools=%d done=%d", textEvents, toolEvents, doneEvents)
	}
}

func TestChatRequestCarriesReasoningEffort(t *testing.T) {
	out := toChatRequest(provider.Request{Model: "gpt-5.5", Effort: provider.EffortMax}, true, true)
	if out.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high (max clamps down)", out.ReasoningEffort)
	}
}

// TestVariantOptionsPassthrough maps a providers.jsonc variant bag onto the
// chat-completions reasoning_effort wire field (httptest).
func TestVariantOptionsPassthrough(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(srv.Close)

	// Simulate config variant resolution (providers.jsonc variants.high).
	variant := map[string]any{"reasoningEffort": "high", "textVerbosity": "low"}
	level, ok := config.VariantEffort(variant)
	if !ok {
		t.Fatal("VariantEffort failed")
	}
	p := NewWithHeaders("openai", srv.URL, func(context.Context) (string, error) { return "sk-test", nil }, nil)
	ch, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
		Effort:   provider.Effort(string(level)),
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if gotBody["model"] != "gpt-5.5" {
		t.Errorf("model = %v", gotBody["model"])
	}
	if gotBody["reasoning_effort"] != "high" {
		t.Errorf("reasoning_effort = %v, want high from variant", gotBody["reasoning_effort"])
	}
}

// TestChatRequestOmitsReasoningEffortWhenUnset keeps the field out of the body
// entirely for models that would reject it.
func TestChatRequestOmitsReasoningEffortWhenUnset(t *testing.T) {
	out := toChatRequest(provider.Request{Model: "gpt-5.5"}, true, true)
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if _, present := keys["reasoning_effort"]; present {
		t.Errorf("body carries reasoning_effort with no effort set: %s", data)
	}
}

func TestToChatRequestPriorityTier(t *testing.T) {
	tests := []struct {
		name         string
		priority     bool
		priorityTier bool
		wantTier     string
	}{
		{name: "openai fast on", priority: true, priorityTier: true, wantTier: "priority"},
		{name: "openai fast off", priorityTier: true},
		{name: "xai omits service tier", priority: true},
		{name: "generic provider omits service tier", priority: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := toChatRequest(provider.Request{Model: "gpt-5.6-sol", Priority: tt.priority}, tt.priorityTier, true)
			if out.ServiceTier != tt.wantTier {
				t.Fatalf("ServiceTier = %q, want %q", out.ServiceTier, tt.wantTier)
			}
			raw, err := json.Marshal(out)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]json.RawMessage
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			_, present := decoded["service_tier"]
			if tt.wantTier == "" && present {
				t.Fatalf("service_tier present in JSON when empty: %s", raw)
			}
			if tt.wantTier != "" && !present {
				t.Fatalf("service_tier missing from JSON: %s", raw)
			}
		})
	}
}

func TestToChatRequestCombinesReasoningEffortAndPriorityTier(t *testing.T) {
	out := toChatRequest(provider.Request{
		Model:    "gpt-5.6-sol",
		Effort:   provider.EffortMax,
		Priority: true,
	}, true, true)
	if out.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high", out.ReasoningEffort)
	}
	if out.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", out.ServiceTier)
	}
}

func TestToChatRequestMapsRoles(t *testing.T) {
	out := toChatRequest(provider.Request{
		Model:  "m",
		System: "s",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "u"},
			{
				Role: provider.RoleAssistant, Text: "a",
				ToolCalls: []provider.ToolCall{{ID: "c", Name: "bash", Args: json.RawMessage(`{"x":1}`)}},
			},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c", Output: "o"}},
		},
	}, false, true)
	if len(out.Messages) != 4 {
		t.Fatalf("messages = %d, want 4", len(out.Messages))
	}
	if out.Messages[0].Role != "system" || out.Messages[0].Content != "s" {
		t.Errorf("system = %+v", out.Messages[0])
	}
	if out.Messages[1].Role != "user" || out.Messages[1].Content != "u" {
		t.Errorf("user = %+v", out.Messages[1])
	}
	if out.Messages[2].Role != "assistant" || len(out.Messages[2].ToolCalls) != 1 {
		t.Errorf("assistant = %+v", out.Messages[2])
	}
	if out.Messages[2].ToolCalls[0].Function.Arguments != `{"x":1}` {
		t.Errorf("args = %q", out.Messages[2].ToolCalls[0].Function.Arguments)
	}
	if out.Messages[3].Role != "tool" || out.Messages[3].ToolCallID != "c" || out.Messages[3].Content != "o" {
		t.Errorf("tool = %+v", out.Messages[3])
	}
}

func TestStreamMapsUsageTokens(t *testing.T) {
	const body = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "tok", nil })
	stream, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5"})
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
	if done == nil {
		t.Fatal("missing EventDone")
	}
	if done.Usage == nil {
		t.Fatal("Usage is nil")
	}
	if done.Usage.InputTokens != 100 || done.Usage.OutputTokens != 20 || done.Usage.TotalTokens != 120 {
		t.Errorf("Usage = %+v, want input=100 output=20 total=120", done.Usage)
	}
	if done.Usage.Estimated {
		t.Error("Estimated must be false")
	}
}

func TestStreamOmitsUsageWhenVendorOmits(t *testing.T) {
	const body = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "tok", nil })
	stream, err := p.Stream(context.Background(), provider.Request{Model: "gpt-5.5"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var sawDone bool
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
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

func TestToChatRequestIncludesImageParts(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	req := provider.Request{
		Model: "gpt-4o",
		Messages: []provider.Message{{
			Role:   provider.RoleUser,
			Text:   "describe",
			Images: []provider.Image{{MIME: "image/png", Data: png}},
		}},
	}
	out := toChatRequest(req, false, true)
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %d", len(out.Messages))
	}
	parts, ok := out.Messages[0].Content.([]chatContentPart)
	if !ok {
		t.Fatalf("content type %T, want []chatContentPart", out.Messages[0].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("parts = %d, want 2", len(parts))
	}
	if parts[0].Type != "text" || parts[0].Text != "describe" {
		t.Errorf("text part = %+v", parts[0])
	}
	if parts[1].Type != "image_url" || parts[1].ImageURL == nil {
		t.Fatalf("image part = %+v", parts[1])
	}
	if !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Errorf("image url = %q", parts[1].ImageURL.URL)
	}
}

func TestToChatRequestOmitsImagesForTextOnlyProvider(t *testing.T) {
	req := provider.Request{
		Model: "deepseek-chat",
		Messages: []provider.Message{{
			Role:   provider.RoleUser,
			Text:   "continue from this image",
			Images: []provider.Image{{MIME: "image/png", Data: []byte{1, 2, 3}}},
		}},
	}
	out := toChatRequest(req, false, false)
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %d, want 1", len(out.Messages))
	}
	if got, ok := out.Messages[0].Content.(string); !ok || got != "continue from this image" {
		t.Errorf("content = %#v, want text without image parts", out.Messages[0].Content)
	}
}

func TestResponsesURL(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "https://api.openai.com/v1/responses"},
		{"https://api.openai.com/v1", "https://api.openai.com/v1/responses"},
		{"https://api.openai.com/v1/", "https://api.openai.com/v1/responses"},
		{"https://proxy.example/v1/responses", "https://proxy.example/v1/responses"},
	}
	for _, tc := range cases {
		if got := ResponsesURL(tc.in); got != tc.want {
			t.Errorf("ResponsesURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestResponsesStreamWireModelAndPath(t *testing.T) {
	var gotPath, gotModel, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if m, ok := body["model"].(string); ok {
			gotModel = m
		}
		if stream, _ := body["stream"].(bool); stream {
			t.Errorf("stream = true, want false (non-streaming phase 0)")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []map[string]any{
				{
					"type": "message",
					"content": []map[string]any{
						{"type": "output_text", "text": "hello"},
					},
				},
				{
					"type":      "function_call",
					"name":      "bash",
					"call_id":   "c1",
					"arguments": `{"cmd":"ls"}`,
				},
			},
			"usage": map[string]any{"input_tokens": 2, "output_tokens": 3, "total_tokens": 5},
		})
	}))
	t.Cleanup(srv.Close)

	p := NewResponses("qgenie", srv.URL+"/v1", func(context.Context) (string, error) {
		return "sk-test", nil
	}, nil)
	ch, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
		Tools: []provider.ToolSchema{{
			Name: "bash", Description: "run", InputSchema: json.RawMessage(`{}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var text string
	var calls int
	var usage *provider.Usage
	for ev := range ch {
		switch ev.Type {
		case provider.EventTextDelta:
			text += ev.Text
		case provider.EventToolCall:
			calls++
			if ev.ToolCall.Name != "bash" || ev.ToolCall.ID != "c1" {
				t.Errorf("tool = %+v", ev.ToolCall)
			}
		case provider.EventDone:
			usage = ev.Usage
		case provider.EventError:
			t.Fatalf("error: %v", ev.Err)
		}
	}
	if gotPath != "/v1/responses" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotModel != "gpt-5.5" {
		t.Errorf("model = %q", gotModel)
	}
	if text != "hello" || calls != 1 {
		t.Errorf("text=%q calls=%d", text, calls)
	}
	if usage == nil || usage.InputTokens != 2 || usage.OutputTokens != 3 {
		t.Errorf("usage = %+v", usage)
	}
}

func TestToChatRequestPromptCacheKey(t *testing.T) {
	out := toChatRequest(provider.Request{
		Model:    "gpt-5.5",
		CacheKey: "  sess-abc  ",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	}, true, true)
	if out.PromptCacheKey != "sess-abc" {
		t.Fatalf("PromptCacheKey = %q, want sess-abc", out.PromptCacheKey)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"prompt_cache_key":"sess-abc"`) {
		t.Fatalf("wire missing prompt_cache_key: %s", raw)
	}

	empty := toChatRequest(provider.Request{Model: "gpt-5.5", CacheKey: "   "}, true, true)
	rawEmpty, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawEmpty), "prompt_cache_key") {
		t.Fatalf("blank CacheKey must omit prompt_cache_key: %s", rawEmpty)
	}
}

func TestToResponsesRequestPromptCacheKey(t *testing.T) {
	out := toResponsesRequest(provider.Request{
		Model:    "gpt-5.5",
		CacheKey: "sess-xyz",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if out.PromptCacheKey != "sess-xyz" {
		t.Fatalf("PromptCacheKey = %q, want sess-xyz", out.PromptCacheKey)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"prompt_cache_key":"sess-xyz"`) {
		t.Fatalf("wire missing prompt_cache_key: %s", raw)
	}
}

func TestChatUsageToProviderCacheBreakout(t *testing.T) {
	tests := []struct {
		name string
		in   chatUsage
		want provider.Usage
	}{
		{
			name: "no details",
			in:   chatUsage{PromptTokens: 100, CompletionTokens: 20, TotalTokens: 120},
			want: provider.Usage{InputTokens: 100, OutputTokens: 20, TotalTokens: 120},
		},
		{
			name: "cached only",
			in: chatUsage{
				PromptTokens: 2006, CompletionTokens: 300, TotalTokens: 2306,
				PromptTokensDetails: &chatPromptTokDetails{CachedTokens: 1920},
			},
			// uncached = 2006-1920 = 86
			want: provider.Usage{InputTokens: 86, OutputTokens: 300, CacheReadTokens: 1920, TotalTokens: 2306},
		},
		{
			name: "cached and write",
			in: chatUsage{
				PromptTokens: 1000, CompletionTokens: 10, TotalTokens: 1010,
				PromptTokensDetails: &chatPromptTokDetails{CachedTokens: 700, CacheWriteTokens: 200},
			},
			// uncached non-write = 1000-700-200 = 100
			want: provider.Usage{InputTokens: 100, OutputTokens: 10, CacheReadTokens: 700, CacheCreationTokens: 200, TotalTokens: 1010},
		},
		{
			name: "clamps cached above prompt",
			in: chatUsage{
				PromptTokens: 50, CompletionTokens: 1, TotalTokens: 51,
				PromptTokensDetails: &chatPromptTokDetails{CachedTokens: 999},
			},
			want: provider.Usage{InputTokens: 0, OutputTokens: 1, CacheReadTokens: 50, TotalTokens: 51},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := chatUsageToProvider(&tt.in)
			if got == nil {
				t.Fatal("nil usage")
			}
			if *got != tt.want {
				t.Fatalf("got %+v, want %+v", *got, tt.want)
			}
			// Occupancy identity: parts sum to prompt+completion when details present.
			parts := got.InputTokens + got.CacheReadTokens + got.CacheCreationTokens + got.OutputTokens
			wantParts := tt.in.PromptTokens + tt.in.CompletionTokens
			if parts != wantParts {
				t.Fatalf("parts sum %d, want prompt+completion %d", parts, wantParts)
			}
		})
	}
}

func TestResponsesUsageToProviderCacheBreakout(t *testing.T) {
	got := responsesUsageToProvider(&responsesUsage{
		InputTokens:  500,
		OutputTokens: 40,
		TotalTokens:  540,
		InputTokensDetails: &responsesInputTokDetails{
			CachedTokens:     400,
			CacheWriteTokens: 50,
		},
	})
	want := provider.Usage{
		InputTokens:         50,
		OutputTokens:        40,
		CacheReadTokens:     400,
		CacheCreationTokens: 50,
		TotalTokens:         540,
	}
	if got == nil || *got != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// TestStreamSendsPromptCacheKeyOnWire pins chat-completions request-side cache key.
func TestStreamSendsPromptCacheKeyOnWire(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
			"usage":{
				"prompt_tokens":2006,
				"completion_tokens":3,
				"total_tokens":2009,
				"prompt_tokens_details":{"cached_tokens":1920,"cache_write_tokens":0}
			}
		}`))
	}))
	defer srv.Close()

	p := New("openai", srv.URL, func(context.Context) (string, error) { return "k", nil })
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		CacheKey: "session-491",
		System:   "stable system",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *provider.Usage
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == provider.EventDone {
			usage = ev.Usage
		}
	}
	if !strings.Contains(string(body), `"prompt_cache_key":"session-491"`) {
		t.Fatalf("request body missing prompt_cache_key: %s", body)
	}
	if usage == nil || usage.CacheReadTokens != 1920 || usage.InputTokens != 86 || usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v, want cacheRead=1920 input=86 output=3", usage)
	}
}

// TestResponsesStreamSendsPromptCacheKeyOnWire pins Responses API cache key + usage map.
func TestResponsesStreamSendsPromptCacheKeyOnWire(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "completed",
			"output": []map[string]any{
				{"type": "message", "content": []map[string]any{
					{"type": "output_text", "text": "ok"},
				}},
			},
			"usage": map[string]any{
				"input_tokens":  100,
				"output_tokens": 5,
				"total_tokens":  105,
				"input_tokens_details": map[string]any{
					"cached_tokens":      80,
					"cache_write_tokens": 10,
				},
			},
		})
	}))
	defer srv.Close()

	p := NewResponses("openai", srv.URL+"/v1", func(context.Context) (string, error) {
		return "k", nil
	}, nil)
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:    "gpt-5.5",
		CacheKey: "session-resp",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var usage *provider.Usage
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == provider.EventDone {
			usage = ev.Usage
		}
	}
	if !strings.Contains(string(body), `"prompt_cache_key":"session-resp"`) {
		t.Fatalf("request body missing prompt_cache_key: %s", body)
	}
	if usage == nil || usage.CacheReadTokens != 80 || usage.CacheCreationTokens != 10 || usage.InputTokens != 10 {
		t.Fatalf("usage = %+v, want cacheRead=80 creation=10 input=10", usage)
	}
}
