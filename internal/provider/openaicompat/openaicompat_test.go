package openaicompat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestChatRequestCarriesReasoningEffort(t *testing.T) {
	out := toChatRequest(provider.Request{Model: "gpt-5.5", Effort: provider.EffortMax}, true)
	if out.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high (max clamps down)", out.ReasoningEffort)
	}
}

// TestChatRequestOmitsReasoningEffortWhenUnset keeps the field out of the body
// entirely for models that would reject it.
func TestChatRequestOmitsReasoningEffortWhenUnset(t *testing.T) {
	out := toChatRequest(provider.Request{Model: "gpt-5.5"}, true)
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
			out := toChatRequest(provider.Request{Model: "gpt-5.6-sol", Priority: tt.priority}, tt.priorityTier)
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
	}, true)
	if out.ReasoningEffort != "high" {
		t.Errorf("ReasoningEffort = %q, want high", out.ReasoningEffort)
	}
	if out.ServiceTier != "priority" {
		t.Errorf("ServiceTier = %q, want priority", out.ServiceTier)
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
