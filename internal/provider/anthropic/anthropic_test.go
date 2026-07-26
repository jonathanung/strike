package anthropic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/base"
)

// baseClientForTest is the transport New would build, minus credentials.
func baseClientForTest() base.Client {
	return base.Client{ProviderName: "anthropic", HTTP: &http.Client{}}
}

// TestApplyEffortMapsEveryLevel pins the wire shape of each rung. budget_tokens
// is deliberately absent: current models reject it, and depth is expressed as
// adaptive thinking plus an output_config effort.
func TestApplyEffortMapsEveryLevel(t *testing.T) {
	cases := []struct {
		effort       provider.Effort
		wantThinking string // "" means the field must be omitted
		wantEffort   string
	}{
		{provider.EffortDefault, "", ""},
		{provider.EffortOff, "disabled", ""},
		{provider.EffortLow, "adaptive", "low"},
		{provider.EffortMedium, "adaptive", "medium"},
		{provider.EffortHigh, "adaptive", "high"},
		{provider.EffortXHigh, "adaptive", "xhigh"},
		{provider.EffortMax, "adaptive", "max"},
	}
	for _, tc := range cases {
		t.Run(string(tc.effort), func(t *testing.T) {
			var out apiRequest
			applyEffort(&out, tc.effort)

			switch {
			case tc.wantThinking == "" && out.Thinking != nil:
				t.Fatalf("thinking = %+v, want omitted", out.Thinking)
			case tc.wantThinking != "" && out.Thinking == nil:
				t.Fatalf("thinking omitted, want type %q", tc.wantThinking)
			case tc.wantThinking != "" && out.Thinking.Type != tc.wantThinking:
				t.Errorf("thinking.type = %q, want %q", out.Thinking.Type, tc.wantThinking)
			}
			switch {
			case tc.wantEffort == "" && out.OutputConfig != nil:
				t.Errorf("output_config = %+v, want omitted", out.OutputConfig)
			case tc.wantEffort != "" && out.OutputConfig == nil:
				t.Errorf("output_config omitted, want effort %q", tc.wantEffort)
			case tc.wantEffort != "" && out.OutputConfig.Effort != tc.wantEffort:
				t.Errorf("output_config.effort = %q, want %q", out.OutputConfig.Effort, tc.wantEffort)
			}
		})
	}
}

// TestEffortOffSendsNoOutputConfig guards the one non-obvious rule in the
// mapping: disabled thinking is only accepted at effort "high" or below, so
// EffortOff must not pin a higher level alongside it.
func TestEffortOffSendsNoOutputConfig(t *testing.T) {
	var out apiRequest
	applyEffort(&out, provider.EffortOff)
	if out.OutputConfig != nil {
		t.Fatalf("output_config = %+v, want omitted so effort defaults to high", out.OutputConfig)
	}
}

// TestAssistantTurnReplaysThinkingBlocksVerbatimAndFirst is the load-bearing
// case: the API rejects a turn whose thinking blocks were reordered or
// rewritten, so they must lead the assistant content byte-for-byte.
func TestAssistantTurnReplaysThinkingBlocksVerbatimAndFirst(t *testing.T) {
	thinking := json.RawMessage(`{"type":"thinking","thinking":"","signature":"abc123=="}`)
	redacted := json.RawMessage(`{"type":"redacted_thinking","data":"opaque"}`)

	req := provider.Request{
		Model:  "claude-opus-5",
		Effort: provider.EffortHigh,
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "hi"},
			{
				Role:      provider.RoleAssistant,
				Text:      "on it",
				Reasoning: []json.RawMessage{thinking, redacted},
				ToolCalls: []provider.ToolCall{{ID: "t1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)}},
			},
		},
	}
	out, err := toAPIRequest(req)
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}

	assistant := out.Messages[1]
	if got := len(assistant.Content); got != 4 {
		t.Fatalf("assistant content blocks = %d, want 4 (2 reasoning + text + tool_use)", got)
	}
	if string(assistant.Content[0]) != string(thinking) {
		t.Errorf("block[0] = %s, want the thinking block verbatim %s", assistant.Content[0], thinking)
	}
	if string(assistant.Content[1]) != string(redacted) {
		t.Errorf("block[1] = %s, want the redacted block verbatim %s", assistant.Content[1], redacted)
	}
	for i, wantType := range []string{"text", "tool_use"} {
		var block apiBlock
		if err := json.Unmarshal(assistant.Content[2+i], &block); err != nil {
			t.Fatalf("decoding block[%d]: %v", 2+i, err)
		}
		if block.Type != wantType {
			t.Errorf("block[%d].type = %q, want %q", 2+i, block.Type, wantType)
		}
	}
}

// TestEffortOffDropsReplayedThinkingBlocks keeps a turn recorded while
// reasoning was on from being sent back after the user turns it off.
func TestEffortOffDropsReplayedThinkingBlocks(t *testing.T) {
	req := provider.Request{
		Model:  "claude-opus-5",
		Effort: provider.EffortOff,
		Messages: []provider.Message{{
			Role:      provider.RoleAssistant,
			Text:      "done",
			Reasoning: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":""}`)},
		}},
	}
	out, err := toAPIRequest(req)
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	if got := len(out.Messages[0].Content); got != 1 {
		t.Fatalf("assistant content blocks = %d, want 1 (text only)", got)
	}
	var block apiBlock
	if err := json.Unmarshal(out.Messages[0].Content[0], &block); err != nil {
		t.Fatal(err)
	}
	if block.Type != "text" {
		t.Errorf("block type = %q, want text", block.Type)
	}
}

// TestStreamEmitsReasoningTextAndToolCalls covers the response side: thinking
// blocks surface as EventReasoning carrying the untouched bytes, so the engine
// can store them for the next request.
func TestStreamEmitsReasoningTextAndToolCalls(t *testing.T) {
	const body = `{"stop_reason":"tool_use","content":[
		{"type":"thinking","thinking":"","signature":"sig=="},
		{"type":"text","text":"checking"},
		{"type":"tool_use","id":"t1","name":"bash","input":{"cmd":"ls"}}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := &Provider{Client: baseClientForTest(), baseURL: srv.URL}
	stream, err := p.Stream(context.Background(), provider.Request{Model: "claude-opus-5", Effort: provider.EffortHigh})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var reasoning []string
	var text string
	var calls int
	var stopReason string
	for ev := range stream {
		switch ev.Type {
		case provider.EventReasoning:
			reasoning = append(reasoning, string(ev.Reasoning))
		case provider.EventTextDelta:
			text += ev.Text
		case provider.EventToolCall:
			calls++
		case provider.EventDone:
			stopReason = ev.StopReason
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if len(reasoning) != 1 {
		t.Fatalf("reasoning events = %d, want 1", len(reasoning))
	}
	// The signature must survive: it is what the API validates on replay.
	var block struct {
		Type      string `json:"type"`
		Signature string `json:"signature"`
	}
	if err := json.Unmarshal([]byte(reasoning[0]), &block); err != nil {
		t.Fatal(err)
	}
	if block.Type != "thinking" || block.Signature != "sig==" {
		t.Errorf("reasoning block = %s, want the thinking block with its signature", reasoning[0])
	}
	if text != "checking" {
		t.Errorf("text = %q, want %q", text, "checking")
	}
	if calls != 1 {
		t.Errorf("tool calls = %d, want 1", calls)
	}
	if stopReason != "tool_use" {
		t.Errorf("stop reason = %q, want tool_use", stopReason)
	}
}

func TestStreamMapsUsageOntoEventDone(t *testing.T) {
	const body = `{"stop_reason":"end_turn","content":[{"type":"text","text":"hi"}],` +
		`"usage":{"input_tokens":12,"output_tokens":3,"cache_read_input_tokens":4,"cache_creation_input_tokens":5}}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := &Provider{Client: baseClientForTest(), baseURL: srv.URL}
	stream, err := p.Stream(context.Background(), provider.Request{Model: "claude-opus-5"})
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
		t.Fatal("Usage is nil, want mapped fields")
	}
	u := done.Usage
	if u.InputTokens != 12 || u.OutputTokens != 3 || u.CacheReadTokens != 4 || u.CacheCreationTokens != 5 {
		t.Errorf("Usage = %+v, want input=12 output=3 cacheRead=4 cacheCreation=5", u)
	}
	if u.Estimated {
		t.Error("Estimated must be false for real provider usage")
	}
}

func TestStreamOmitsUsageWhenVendorOmits(t *testing.T) {
	const body = `{"stop_reason":"end_turn","content":[{"type":"text","text":"hi"}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	p := &Provider{Client: baseClientForTest(), baseURL: srv.URL}
	stream, err := p.Stream(context.Background(), provider.Request{Model: "claude-opus-5"})
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
				t.Errorf("Usage = %+v, want nil when vendor omits usage", ev.Usage)
			}
		}
	}
	if !sawDone {
		t.Fatal("missing EventDone")
	}
}

// TestRequestEncodesReasoningFieldsOnTheWire asserts the JSON the API actually
// receives, not just the struct — a wrong tag would pass the mapping test.
func TestRequestEncodesReasoningFieldsOnTheWire(t *testing.T) {
	out, err := toAPIRequest(provider.Request{Model: "claude-opus-5", Effort: provider.EffortXHigh})
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Thinking     map[string]string `json:"thinking"`
		OutputConfig map[string]string `json:"output_config"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Thinking["type"] != "adaptive" {
		t.Errorf("thinking = %v, want type adaptive", wire.Thinking)
	}
	if wire.OutputConfig["effort"] != "xhigh" {
		t.Errorf("output_config = %v, want effort xhigh", wire.OutputConfig)
	}

	// A default-effort request must carry neither field at all.
	bare, err := toAPIRequest(provider.Request{Model: "claude-opus-5"})
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	data, err = json.Marshal(bare)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"thinking", "output_config"} {
		if _, present := keys[key]; present {
			t.Errorf("default-effort request carries %q, want it omitted", key)
		}
	}
}

func TestUserMessageIncludesImageBlocks(t *testing.T) {
	png := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	req := provider.Request{
		Model: "claude",
		Messages: []provider.Message{{
			Role:   provider.RoleUser,
			Text:   "what is this",
			Images: []provider.Image{{MIME: "image/png", Data: png}},
		}},
	}
	out, err := toAPIRequest(req)
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	if len(out.Messages) != 1 {
		t.Fatalf("messages = %d", len(out.Messages))
	}
	if len(out.Messages[0].Content) != 2 {
		t.Fatalf("blocks = %d, want 2", len(out.Messages[0].Content))
	}
	var img apiBlock
	if err := json.Unmarshal(out.Messages[0].Content[0], &img); err != nil {
		t.Fatal(err)
	}
	if img.Type != "image" || img.Source == nil || img.Source.MediaType != "image/png" || img.Source.Data == "" {
		t.Fatalf("image block = %+v", img)
	}
	var text apiBlock
	if err := json.Unmarshal(out.Messages[0].Content[1], &text); err != nil {
		t.Fatal(err)
	}
	if text.Type != "text" || text.Text != "what is this" {
		t.Fatalf("text block = %+v", text)
	}
}
