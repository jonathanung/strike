package engine

import (
	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// charsPerTokenEst is the local occupancy heuristic (~4 chars/token).
// Match doctor modal and estimateTokens — never claim measured precision.
const charsPerTokenEst = 4

// requestSliceChars is raw UTF-8 byte lengths per model-facing input slice.
type requestSliceChars struct {
	System      int
	Tools       int
	Messages    int
	ToolResults int
}

// attributeRequestChars splits a stream request into char budgets:
// system prompt, tool schemas, messages excluding tool_result bodies, and
// tool_result bodies (CallID counted with the result).
func attributeRequestChars(system string, tools []provider.ToolSchema, msgs []provider.Message) requestSliceChars {
	var s requestSliceChars
	s.System = len(system)
	for _, t := range tools {
		s.Tools += len(t.Name) + len(t.Description) + len(t.InputSchema)
	}
	for _, m := range msgs {
		s.Messages += len(m.Text)
		for _, tc := range m.ToolCalls {
			s.Messages += len(tc.ID) + len(tc.Name) + len(tc.Args)
		}
		for _, r := range m.Reasoning {
			s.Messages += len(r)
		}
		for _, img := range m.Images {
			s.Messages += len(img.MIME) + len(img.Data)
		}
		if m.ToolResult != nil {
			s.ToolResults += len(m.ToolResult.CallID) + len(m.ToolResult.Output)
		}
	}
	return s
}

// estTokensFromChars converts a char budget with the shared ~4 chars/token
// heuristic. Zero chars → zero tokens (not unknown).
func estTokensFromChars(chars int) int {
	if chars <= 0 {
		return 0
	}
	return (chars + charsPerTokenEst - 1) / charsPerTokenEst
}

// estimateRequestAttribution builds an estimate-labeled slice breakdown.
func estimateRequestAttribution(system string, tools []provider.ToolSchema, msgs []provider.Message) protocol.RequestTokenAttribution {
	c := attributeRequestChars(system, tools, msgs)
	sys := estTokensFromChars(c.System)
	tool := estTokensFromChars(c.Tools)
	msg := estTokensFromChars(c.Messages)
	tr := estTokensFromChars(c.ToolResults)
	total := estTokensFromChars(c.System + c.Tools + c.Messages + c.ToolResults)
	return protocol.RequestTokenAttribution{
		System:      protocol.KnownTokens(sys),
		Tools:       protocol.KnownTokens(tool),
		Messages:    protocol.KnownTokens(msg),
		ToolResults: protocol.KnownTokens(tr),
		Total:       protocol.KnownTokens(total),
		Source:      protocol.UsageSourceEstimated,
	}
}
