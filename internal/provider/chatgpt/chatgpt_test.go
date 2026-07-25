package chatgpt

import (
	"encoding/json"
	"strings"
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
