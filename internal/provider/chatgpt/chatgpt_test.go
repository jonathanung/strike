package chatgpt

import (
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
)

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
