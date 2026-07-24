package openaicompat

import (
	"encoding/json"
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
		{name: "openai fast off", priority: false, priorityTier: true, wantTier: ""},
		{name: "xai ignores fast", priority: true, priorityTier: false, wantTier: ""},
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
