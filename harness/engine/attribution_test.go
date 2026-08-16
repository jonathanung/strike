package engine

import (
	"encoding/json"
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestAttributeRequestCharsSlices(t *testing.T) {
	system := "SYS"                                // 3
	schema := json.RawMessage(`{"type":"object"}`) // 17
	tools := []provider.ToolSchema{
		{Name: "bash", Description: "run", InputSchema: schema},
	}
	// tools: 4 + 3 + 17 = 24
	args := json.RawMessage(`{"cmd":"ls"}`) // 12
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "hello"}, // 5
		{
			Role: provider.RoleAssistant,
			Text: "call",
			ToolCalls: []provider.ToolCall{
				{ID: "c1", Name: "bash", Args: args},
			},
		}, // 4 + 2 + 4 + 12 = 22
		{
			Role: provider.RoleTool,
			ToolResult: &provider.ToolResult{
				CallID: "c1",
				Output: "file.txt\n",
			},
		}, // tool results: 2 + 9 = 11
	}

	got := attributeRequestChars(system, tools, msgs)
	if got.System != 3 {
		t.Errorf("system = %d, want 3", got.System)
	}
	if got.Tools != 24 {
		t.Errorf("tools = %d, want 24", got.Tools)
	}
	if got.Messages != 5+22 {
		t.Errorf("messages = %d, want 27", got.Messages)
	}
	if got.ToolResults != 11 {
		t.Errorf("toolResults = %d, want 11", got.ToolResults)
	}
}

func TestAttributeRequestCharsEmpty(t *testing.T) {
	got := attributeRequestChars("", nil, nil)
	if got != (requestSliceChars{}) {
		t.Fatalf("empty = %+v, want zero", got)
	}
}

func TestAttributeRequestCharsPrunedPlaceholderSmall(t *testing.T) {
	large := make([]byte, 10_000)
	for i := range large {
		large[i] = 'x'
	}
	before := attributeRequestChars("", nil, []provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c", Output: string(large)}},
	})
	after := attributeRequestChars("", nil, []provider.Message{
		{Role: provider.RoleTool, ToolResult: &provider.ToolResult{CallID: "c", Output: "[Old tool result content cleared]"}},
	})
	if after.ToolResults >= before.ToolResults {
		t.Fatalf("pruned toolResults=%d not smaller than full=%d", after.ToolResults, before.ToolResults)
	}
	if after.ToolResults > 50 {
		t.Fatalf("placeholder toolResults=%d, want small", after.ToolResults)
	}
	if after.Messages != 0 {
		t.Fatalf("messages should exclude tool_result body, got %d", after.Messages)
	}
}

func TestEstTokensFromChars(t *testing.T) {
	cases := []struct {
		chars int
		want  int
	}{
		{0, 0},
		{-1, 0},
		{1, 1},
		{4, 1},
		{5, 2},
		{8, 2},
		{9, 3},
	}
	for _, tc := range cases {
		if got := estTokensFromChars(tc.chars); got != tc.want {
			t.Errorf("estTokensFromChars(%d) = %d, want %d", tc.chars, got, tc.want)
		}
	}
}

func TestEstimateRequestAttributionTable(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"path":{"type":"string"}}}`)
	cases := []struct {
		name    string
		system  string
		tools   []provider.ToolSchema
		msgs    []provider.Message
		wantSys int
		wantTol bool // tools > 0
		wantMsg bool
		wantTR  bool
	}{
		{
			name:    "system only",
			system:  "abcdefgh", // 8 → 2 tok
			wantSys: 2,
		},
		{
			name:   "tools schemas",
			system: "",
			tools: []provider.ToolSchema{
				{Name: "read", Description: "Read a file from disk", InputSchema: schema},
			},
			wantTol: true,
		},
		{
			name:   "messages without tool results",
			system: "s",
			msgs: []provider.Message{
				{Role: provider.RoleUser, Text: "aaaaaaaa"}, // 8
				{Role: provider.RoleAssistant, Text: "bbbbbbbb"},
			},
			wantSys: 1,
			wantMsg: true,
		},
		{
			name: "tool results separated",
			msgs: []provider.Message{
				{Role: provider.RoleUser, Text: "hi"},
				{
					Role: provider.RoleTool,
					ToolResult: &provider.ToolResult{
						CallID: "id",
						Output: "yyyyyyyyyyyyyyyy", // 16
					},
				},
			},
			wantMsg: true,
			wantTR:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := estimateRequestAttribution(tc.system, tc.tools, tc.msgs)
			if got.Source != protocol.UsageSourceEstimated {
				t.Fatalf("source = %q, want %q", got.Source, protocol.UsageSourceEstimated)
			}
			for _, part := range []protocol.TokenCount{got.System, got.Tools, got.Messages, got.ToolResults, got.Total} {
				if !part.Known {
					t.Fatalf("slice not Known: %+v", got)
				}
			}
			if tc.wantSys > 0 && got.System.N != tc.wantSys {
				t.Errorf("system = %d, want %d", got.System.N, tc.wantSys)
			}
			if tc.wantTol && got.Tools.N <= 0 {
				t.Errorf("tools = %d, want > 0", got.Tools.N)
			}
			if tc.wantMsg && got.Messages.N <= 0 {
				t.Errorf("messages = %d, want > 0", got.Messages.N)
			}
			if tc.wantTR && got.ToolResults.N <= 0 {
				t.Errorf("toolResults = %d, want > 0", got.ToolResults.N)
			}
			// Total is from summed chars, not sum of rounded slices.
			chars := attributeRequestChars(tc.system, tc.tools, tc.msgs)
			wantTotal := estTokensFromChars(chars.System + chars.Tools + chars.Messages + chars.ToolResults)
			if got.Total.N != wantTotal {
				t.Errorf("total = %d, want %d from chars", got.Total.N, wantTotal)
			}
		})
	}
}

func TestEstimateRequestAttributionTotalFromCharSum(t *testing.T) {
	system := "system prompt body"
	tools := []provider.ToolSchema{
		{Name: "bash", Description: "shell", InputSchema: json.RawMessage(`{}`)},
	}
	msgs := []provider.Message{
		{Role: provider.RoleUser, Text: "user text here"},
		{
			Role: provider.RoleAssistant,
			Text: "ok",
			ToolCalls: []provider.ToolCall{
				{ID: "1", Name: "bash", Args: json.RawMessage(`{}`)},
			},
		},
		{
			Role:       provider.RoleTool,
			ToolResult: &provider.ToolResult{CallID: "1", Output: "out"},
		},
	}
	attr := estimateRequestAttribution(system, tools, msgs)
	c := attributeRequestChars(system, tools, msgs)
	want := estTokensFromChars(c.System + c.Tools + c.Messages + c.ToolResults)
	if attr.Total.N != want {
		t.Fatalf("total = %d, want %d (chars sys=%d tools=%d msg=%d tr=%d)",
			attr.Total.N, want, c.System, c.Tools, c.Messages, c.ToolResults)
	}
	if attr.Tools.N <= 0 {
		t.Fatalf("tools slice empty: %+v", attr)
	}
	if attr.ToolResults.N <= 0 {
		t.Fatalf("toolResults slice empty: %+v", attr)
	}
}
