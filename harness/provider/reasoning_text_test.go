package provider_test

import (
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
)

func TestReasoningTextExtractsVendorShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty", raw: "", want: ""},
		{name: "anthropic thinking", raw: `{"type":"thinking","thinking":"step by step","signature":"x"}`, want: "step by step"},
		{name: "redacted empty", raw: `{"type":"redacted_thinking","data":"opaque"}`, want: ""},
		{name: "plain string", raw: `"just text"`, want: "just text"},
		{name: "summary field", raw: `{"summary":"brief plan"}`, want: "brief plan"},
		{name: "text field", raw: `{"text":"streamed"}`, want: "streamed"},
		{name: "not json", raw: `not-json`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var raw json.RawMessage
			if tt.raw != "" {
				raw = json.RawMessage(tt.raw)
			}
			if got := provider.ReasoningText(raw); got != tt.want {
				t.Errorf("ReasoningText(%s) = %q, want %q", tt.raw, got, tt.want)
			}
		})
	}
}
