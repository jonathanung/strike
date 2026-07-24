package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

func TestChatRequestCarriesReasoningEffort(t *testing.T) {
	out := toChatRequest(provider.Request{Model: "gpt-5.5", Effort: provider.EffortMax})
	if out.ReasoningEffort != "high" {
		t.Errorf("reasoning_effort = %q, want high (max clamps down)", out.ReasoningEffort)
	}
}

// TestChatRequestOmitsReasoningEffortWhenUnset keeps the field out of the body
// entirely for models that would reject it.
func TestChatRequestOmitsReasoningEffortWhenUnset(t *testing.T) {
	out := toChatRequest(provider.Request{Model: "gpt-5.5"})
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
