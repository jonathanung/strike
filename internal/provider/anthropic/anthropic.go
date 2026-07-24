// Package anthropic adapts the Anthropic Messages API to the provider
// interface. Phase 0 uses non-streaming requests and emits the whole
// response as one delta; SSE streaming is Phase 1.
package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/base"
)

const (
	defaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
)

type Provider struct {
	base.Client
}

func New(apiKey string) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no Anthropic credentials: set ANTHROPIC_API_KEY, run `strike auth login anthropic`, or use --provider echo for the offline dev provider")
	}
	return &Provider{base.Client{
		ProviderName: "anthropic",
		HTTP:         &http.Client{Timeout: 5 * time.Minute},
		Headers: map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": apiVersion,
		},
	}}, nil
}

// Wire types for the Messages API.

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []apiMessage `json:"messages"`
	Tools     []apiTool    `json:"tools,omitempty"`
}

type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

type apiBlock struct {
	Type string `json:"type"`
	// text
	Text string `json:"text,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type apiTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type apiResponse struct {
	Content    []apiBlock `json:"content"`
	StopReason string     `json:"stop_reason"`
}

func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	apiReq := toAPIRequest(req)
	return base.Stream(func(ch chan<- provider.StreamEvent) {
		var resp apiResponse
		if err := p.PostJSON(ctx, defaultBaseURL+"/v1/messages", apiReq, &resp); err != nil {
			base.Fail(ch, err)
			return
		}
		for _, block := range resp.Content {
			switch block.Type {
			case "text":
				ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: block.Text}
			case "tool_use":
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:   block.ID,
					Name: block.Name,
					Args: block.Input,
				}}
			}
		}
		ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: resp.StopReason}
	}), nil
}

func toAPIRequest(req provider.Request) apiRequest {
	out := apiRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, apiTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			out.Messages = append(out.Messages, apiMessage{
				Role:    "user",
				Content: []apiBlock{{Type: "text", Text: m.Text}},
			})
		case provider.RoleAssistant:
			var blocks []apiBlock
			if m.Text != "" {
				blocks = append(blocks, apiBlock{Type: "text", Text: m.Text})
			}
			for _, call := range m.ToolCalls {
				input := call.Args
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				blocks = append(blocks, apiBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: input})
			}
			out.Messages = append(out.Messages, apiMessage{Role: "assistant", Content: blocks})
		case provider.RoleTool:
			out.Messages = append(out.Messages, apiMessage{
				Role: "user",
				Content: []apiBlock{{
					Type:      "tool_result",
					ToolUseID: m.ToolResult.CallID,
					Content:   m.ToolResult.Output,
					IsError:   m.ToolResult.IsError,
				}},
			})
		}
	}
	return out
}
