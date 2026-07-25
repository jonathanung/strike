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
	if out.Input[3].Type != "function_call_output" || out.Input[3].CallID != "c1" || out.Input[3].Output != "out" {
		t.Errorf("function_call_output = %+v", out.Input[3])
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
