package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/providers/base"
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

func TestMessagesURLOpenCodeParity(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1", "https://api.anthropic.com/v1/messages"},
		{"https://api.anthropic.com/v1/", "https://api.anthropic.com/v1/messages"},
		{"https://proxy.example/anthropic/v1", "https://proxy.example/anthropic/v1/messages"},
		{"https://proxy.example/v1/messages", "https://proxy.example/v1/messages"},
		{"https://proxy.example/anthropic", "https://proxy.example/anthropic/v1/messages"},
	}
	for _, tc := range cases {
		if got := MessagesURL(tc.in); got != tc.want {
			t.Errorf("MessagesURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStreamOpenCodeBaseURLWithV1(t *testing.T) {
	var sawPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}]}`))
	}))
	t.Cleanup(srv.Close)

	p, err := NewCustom("proxy", srv.URL+"/v1", "k", nil)
	if err != nil {
		t.Fatal(err)
	}
	ch, err := p.Stream(context.Background(), provider.Request{
		Model: "claude-test", MaxTokens: 8,
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if sawPath != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages (not /v1/v1/messages)", sawPath)
	}
}

func wantEphemeral(t *testing.T, cc *apiCacheControl, where string) {
	t.Helper()
	if cc == nil {
		t.Fatalf("%s: cache_control missing", where)
	}
	if cc.Type != "ephemeral" {
		t.Fatalf("%s: cache_control.type = %q, want ephemeral", where, cc.Type)
	}
}

func blockCacheControl(t *testing.T, raw json.RawMessage) *apiCacheControl {
	t.Helper()
	var block apiBlock
	if err := json.Unmarshal(raw, &block); err != nil {
		t.Fatalf("decode block: %v", err)
	}
	return block.CacheControl
}

// TestPromptCacheBreakpointsOnStablePrefix pins the three request-side
// breakpoints peers set (system + last tool + last eligible message block).
func TestPromptCacheBreakpointsOnStablePrefix(t *testing.T) {
	req := provider.Request{
		Model:  "claude-opus-5",
		System: "you are strike",
		Tools: []provider.ToolSchema{
			{Name: "read", Description: "read file", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Name: "bash", Description: "run shell", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "hi"},
			{Role: provider.RoleAssistant, Text: "hello"},
			{Role: provider.RoleUser, Text: "list files"},
		},
	}
	out, err := toAPIRequest(req)
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}

	if len(out.System) != 1 {
		t.Fatalf("system blocks = %d, want 1", len(out.System))
	}
	if out.System[0].Type != "text" || out.System[0].Text != "you are strike" {
		t.Fatalf("system[0] = %+v", out.System[0])
	}
	wantEphemeral(t, out.System[0].CacheControl, "system[0]")

	if len(out.Tools) != 2 {
		t.Fatalf("tools = %d, want 2", len(out.Tools))
	}
	if out.Tools[0].CacheControl != nil {
		t.Errorf("tools[0] has cache_control; only last tool should")
	}
	wantEphemeral(t, out.Tools[1].CacheControl, "tools[last]")

	if len(out.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(out.Messages))
	}
	// Earlier messages must not carry cache_control.
	for i := 0; i < 2; i++ {
		for j, raw := range out.Messages[i].Content {
			if cc := blockCacheControl(t, raw); cc != nil {
				t.Errorf("messages[%d].content[%d] has cache_control; only last eligible block should", i, j)
			}
		}
	}
	last := out.Messages[2].Content
	if len(last) != 1 {
		t.Fatalf("last message blocks = %d, want 1", len(last))
	}
	wantEphemeral(t, blockCacheControl(t, last[0]), "messages[last].content[last]")
}

// TestPromptCacheWireShape asserts the JSON the API receives — a wrong tag
// would pass struct-level checks but fail on the wire.
func TestPromptCacheWireShape(t *testing.T) {
	out, err := toAPIRequest(provider.Request{
		Model:  "claude-opus-5",
		System: "sys",
		Tools: []provider.ToolSchema{
			{Name: "bash", Description: "sh", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		System []struct {
			Type         string `json:"type"`
			Text         string `json:"text"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"system"`
		Tools []struct {
			Name         string `json:"name"`
			CacheControl *struct {
				Type string `json:"type"`
			} `json:"cache_control"`
		} `json:"tools"`
		Messages []struct {
			Content []struct {
				Type         string `json:"type"`
				Text         string `json:"text"`
				CacheControl *struct {
					Type string `json:"type"`
				} `json:"cache_control"`
			} `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	if len(wire.System) != 1 || wire.System[0].CacheControl == nil || wire.System[0].CacheControl.Type != "ephemeral" {
		t.Errorf("wire system = %+v", wire.System)
	}
	if len(wire.Tools) != 1 || wire.Tools[0].CacheControl == nil || wire.Tools[0].CacheControl.Type != "ephemeral" {
		t.Errorf("wire tools = %+v", wire.Tools)
	}
	if len(wire.Messages) != 1 || len(wire.Messages[0].Content) != 1 {
		t.Fatalf("wire messages = %+v", wire.Messages)
	}
	c := wire.Messages[0].Content[0]
	if c.CacheControl == nil || c.CacheControl.Type != "ephemeral" || c.Text != "hi" {
		t.Errorf("wire last content = %+v", c)
	}
	// system must be an array, not a bare string
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		t.Fatal(err)
	}
	if len(top["system"]) == 0 || top["system"][0] != '[' {
		t.Errorf("system wire = %s, want JSON array", top["system"])
	}
}

// TestPromptCacheSkipsThinkingBlocks places the message breakpoint on the last
// eligible block, not on thinking/redacted_thinking (API rejects those).
func TestPromptCacheSkipsThinkingBlocks(t *testing.T) {
	thinking := json.RawMessage(`{"type":"thinking","thinking":"","signature":"sig=="}`)
	out, err := toAPIRequest(provider.Request{
		Model:  "claude-opus-5",
		Effort: provider.EffortHigh,
		Messages: []provider.Message{{
			Role:      provider.RoleAssistant,
			Text:      "done",
			Reasoning: []json.RawMessage{thinking},
			ToolCalls: []provider.ToolCall{{ID: "t1", Name: "bash", Args: json.RawMessage(`{"cmd":"ls"}`)}},
		}},
	})
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	content := out.Messages[0].Content
	// [thinking, text, tool_use] — breakpoint on tool_use (last eligible).
	if got := len(content); got != 3 {
		t.Fatalf("blocks = %d, want 3", got)
	}
	if string(content[0]) != string(thinking) {
		t.Errorf("thinking block mutated: %s", content[0])
	}
	if cc := blockCacheControl(t, content[1]); cc != nil {
		t.Errorf("text block has cache_control; want only last eligible")
	}
	wantEphemeral(t, blockCacheControl(t, content[2]), "tool_use")
}

// TestPromptCacheOmitsSystemWhenEmpty keeps system omitted rather than sending
// an empty array that would burn a breakpoint for nothing.
func TestPromptCacheOmitsSystemWhenEmpty(t *testing.T) {
	out, err := toAPIRequest(provider.Request{
		Model:    "claude-opus-5",
		Messages: []provider.Message{{Role: provider.RoleUser, Text: "hi"}},
	})
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	if len(out.System) != 0 {
		t.Fatalf("system = %+v, want omitted", out.System)
	}
	data, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil {
		t.Fatal(err)
	}
	if _, ok := keys["system"]; ok {
		t.Errorf("wire carries system = %s, want omitted", keys["system"])
	}
	wantEphemeral(t, blockCacheControl(t, out.Messages[0].Content[0]), "user text")
}

// TestPromptCacheOnToolResult marks tool_result when it is the conversation tail
// (common after a tool round before the next model turn).
func TestPromptCacheOnToolResult(t *testing.T) {
	out, err := toAPIRequest(provider.Request{
		Model: "claude-opus-5",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Text: "run"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "bash", Args: json.RawMessage(`{}`)},
			}},
			{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c1", Output: "ok"}},
		},
	})
	if err != nil {
		t.Fatalf("toAPIRequest: %v", err)
	}
	last := out.Messages[len(out.Messages)-1]
	var block apiBlock
	if err := json.Unmarshal(last.Content[0], &block); err != nil {
		t.Fatal(err)
	}
	if block.Type != "tool_result" {
		t.Fatalf("last block type = %q, want tool_result", block.Type)
	}
	wantEphemeral(t, block.CacheControl, "tool_result")
}

// TestStreamSendsCacheControlOnWire catches regressions where toAPIRequest
// sets breakpoints but Stream posts a different body.
func TestStreamSendsCacheControlOnWire(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var err error
		body, err = io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],` +
			`"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":8,"cache_creation_input_tokens":2}}`))
	}))
	t.Cleanup(srv.Close)

	p := &Provider{Client: baseClientForTest(), baseURL: srv.URL}
	stream, err := p.Stream(context.Background(), provider.Request{
		Model:  "claude-opus-5",
		System: "sys",
		Tools: []provider.ToolSchema{
			{Name: "bash", Description: "sh", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
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
	if !bytes.Contains(body, []byte(`"cache_control"`)) || !bytes.Contains(body, []byte(`"ephemeral"`)) {
		t.Fatalf("request body missing cache_control: %s", body)
	}
	if usage == nil || usage.CacheReadTokens != 8 || usage.CacheCreationTokens != 2 {
		t.Fatalf("usage = %+v, want cache read=8 creation=2 still reported", usage)
	}
}
