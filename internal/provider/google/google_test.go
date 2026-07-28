package google

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

func TestNew(t *testing.T) {
	p := New(func(context.Context) (string, error) { return "k", nil })
	if p.Name() != "google" {
		t.Errorf("Name = %q", p.Name())
	}
	if p.baseURL != defaultBaseURL {
		t.Errorf("baseURL = %q", p.baseURL)
	}
}

func TestStreamAuthHeaderAndPath(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("x-goog-api-key")
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, error) { return "secret", nil })
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if gotAuth != "secret" {
		t.Errorf("x-goog-api-key = %q", gotAuth)
	}
	if gotPath != "/models/gemini-2.5-pro:generateContent" {
		t.Errorf("path = %q", gotPath)
	}
}

func TestStreamAuthError(t *testing.T) {
	want := errors.New("no creds")
	p := New(func(context.Context) (string, error) { return "", want })
	p.baseURL = "http://127.0.0.1:1"
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream start err = %v, want nil", err)
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

func TestStreamEmitsTextToolCallsAndDone(t *testing.T) {
	const body = `{
		"candidates":[{
			"content":{"parts":[
				{"text":"checking"},
				{"functionCall":{"name":"bash","args":{"cmd":"ls"}}},
				{"functionCall":{"name":"read"}}
			]},
			"finishReason":"STOP"
		}],
		"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"totalTokenCount":16}
	}`
	var gotReq apiRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, error) { return "k", nil })
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:     "models/gemini-2.5-pro",
		System:    "sys",
		MaxTokens: 256,
		Tools: []provider.ToolSchema{{
			Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "go", Images: []provider.Image{{MIME: "image/png", Data: []byte{1, 2, 3}}}},
			{Role: provider.RoleAssistant, Text: "sure", ToolCalls: []provider.ToolCall{{ID: "old", Name: "bash", Args: json.RawMessage(`{"cmd":"pwd"}`)}}},
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
			calls = append(calls, *ev.ToolCall)
		case provider.EventDone:
			copy := ev
			done = &copy
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if text != "checking" {
		t.Errorf("text = %q", text)
	}
	if len(calls) != 2 || calls[0].ID != "bash-1" || calls[0].Name != "bash" || string(calls[0].Args) != `{"cmd":"ls"}` || string(calls[1].Args) != `{}` {
		t.Fatalf("calls = %+v", calls)
	}
	if done == nil || done.StopReason != "STOP" || done.Usage == nil || done.Usage.InputTokens != 11 || done.Usage.OutputTokens != 5 || done.Usage.TotalTokens != 16 {
		t.Fatalf("done = %+v", done)
	}
	if gotReq.SystemInstruction == nil || gotReq.SystemInstruction.Parts[0].Text != "sys" {
		t.Errorf("systemInstruction = %+v", gotReq.SystemInstruction)
	}
	if gotReq.GenerationConfig == nil || gotReq.GenerationConfig.MaxOutputTokens != 256 {
		t.Errorf("generationConfig = %+v", gotReq.GenerationConfig)
	}
	if len(gotReq.Tools) != 1 || len(gotReq.Tools[0].FunctionDeclarations) != 1 || gotReq.Tools[0].FunctionDeclarations[0].Name != "bash" {
		t.Errorf("tools = %+v", gotReq.Tools)
	}
	if len(gotReq.Contents) != 3 || gotReq.Contents[0].Role != "user" || gotReq.Contents[1].Role != "model" || gotReq.Contents[2].Role != "user" {
		t.Fatalf("contents = %+v", gotReq.Contents)
	}
	if gotReq.Contents[0].Parts[1].InlineData == nil || gotReq.Contents[0].Parts[1].InlineData.Data != "AQID" {
		t.Errorf("image part = %+v", gotReq.Contents[0].Parts[1])
	}
	if gotReq.Contents[2].Parts[0].FunctionResponse == nil || gotReq.Contents[2].Parts[0].FunctionResponse.Name != "bash" {
		t.Errorf("function response = %+v", gotReq.Contents[2].Parts[0].FunctionResponse)
	}
}

// TestStreamRequestBodyCamelCaseKeys pins the Google AI Studio wire dialect:
// system prompt, inline image, and tools must serialize as camelCase JSON keys
// (not the snake_case names some SDKs emit).
func TestStreamRequestBodyCamelCaseKeys(t *testing.T) {
	var raw []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		raw, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, error) { return "k", nil })
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:  "gemini-2.5-pro",
		System: "be helpful",
		Tools: []provider.ToolSchema{{
			Name: "bash", Description: "run", InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "look", Images: []provider.Image{{MIME: "image/png", Data: []byte{1, 2, 3}}}},
		},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	body := string(raw)
	if body == "" {
		t.Fatal("empty request body")
	}
	for _, want := range []string{"systemInstruction", "inlineData", "mimeType", "functionDeclarations"} {
		if !strings.Contains(body, `"`+want+`"`) {
			t.Errorf("missing camelCase key %q in body:\n%s", want, body)
		}
	}
	for _, bad := range []string{"system_instruction", "inline_data", "mime_type", "function_declarations"} {
		if strings.Contains(body, `"`+bad+`"`) {
			t.Errorf("unexpected snake_case key %q in body:\n%s", bad, body)
		}
	}
}

func TestStreamNoCandidates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, error) { return "k", nil })
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{Model: "m"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got string
	for ev := range stream {
		if ev.Type == provider.EventError {
			got = ev.Err.Error()
		}
	}
	if !strings.Contains(got, "response has no candidates") {
		t.Fatalf("error = %q", got)
	}
}

func TestStreamAlwaysUsesAPIKeyHeader(t *testing.T) {
	// OAuth is not a google auth path; every credential is sent as x-goog-api-key.
	var gotKey, gotBearer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-goog-api-key")
		gotBearer = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}]}`)
	}))
	defer srv.Close()

	p := New(func(context.Context) (string, error) {
		return "AIzaSy-test-google-ai-studio-key", nil
	})
	p.baseURL = srv.URL
	stream, err := p.Stream(context.Background(), provider.Request{Model: "gemini-2.5-pro"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for ev := range stream {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if gotKey != "AIzaSy-test-google-ai-studio-key" {
		t.Errorf("x-goog-api-key = %q", gotKey)
	}
	if gotBearer != "" {
		t.Errorf("Authorization = %q, want empty", gotBearer)
	}
}
