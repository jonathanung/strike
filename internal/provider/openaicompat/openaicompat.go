// Package openaicompat adapts OpenAI-compatible chat-completions APIs
// (OpenAI platform, xAI) to the provider interface. Credentials come from a
// per-request bearer source so OAuth refresh happens transparently — for
// xAI, a Grok OAuth access token used as the bearer bills the SuperGrok
// subscription directly against the standard API (no separate backend,
// unlike OpenAI's ChatGPT mode). Phase 0 is non-streaming.
package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/base"
)

// BearerSource resolves the Authorization bearer for each request.
type BearerSource = func(ctx context.Context) (string, error)

type Provider struct {
	base.Client
	baseURL string
}

func New(name, baseURL string, bearer BearerSource) *Provider {
	return &Provider{
		Client: base.Client{
			ProviderName: name,
			HTTP:         &http.Client{Timeout: 5 * time.Minute},
			Auth:         base.BearerAuth(bearer),
		},
		baseURL: baseURL,
	}
}

func NewOpenAI(bearer BearerSource) *Provider {
	return New("openai", "https://api.openai.com/v1", bearer)
}

func NewXAI(bearer BearerSource) *Provider {
	return New("xai", "https://api.x.ai/v1", bearer)
}

// Wire types for the chat completions API.

type chatRequest struct {
	Model           string        `json:"model"`
	Messages        []chatMessage `json:"messages"`
	Tools           []chatTool    `json:"tools,omitempty"`
	ReasoningEffort string        `json:"reasoning_effort,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string          `json:"type"`
	Function chatToolFuncDef `json:"function"`
}

type chatToolFuncDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type chatResponse struct {
	Choices []struct {
		Message      chatMessage `json:"message"`
		FinishReason string      `json:"finish_reason"`
	} `json:"choices"`
}

func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	chatReq := toChatRequest(req)
	return base.Stream(func(ch chan<- provider.StreamEvent) {
		var resp chatResponse
		if err := p.PostJSON(ctx, p.baseURL+"/chat/completions", chatReq, &resp); err != nil {
			base.Fail(ch, err)
			return
		}
		if len(resp.Choices) == 0 {
			base.Fail(ch, fmt.Errorf("%s: response has no choices", p.Name()))
			return
		}
		choice := resp.Choices[0]
		if choice.Message.Content != "" {
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: choice.Message.Content}
		}
		for _, call := range choice.Message.ToolCalls {
			args := json.RawMessage(call.Function.Arguments)
			if len(args) == 0 {
				args = json.RawMessage(`{}`)
			}
			ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
				ID:   call.ID,
				Name: call.Function.Name,
				Args: args,
			}}
		}
		ch <- provider.StreamEvent{Type: provider.EventDone, StopReason: choice.FinishReason}
	}), nil
}

func toChatRequest(req provider.Request) chatRequest {
	out := chatRequest{Model: req.Model, ReasoningEffort: base.OpenAIEffort(req.Effort)}
	if req.System != "" {
		out.Messages = append(out.Messages, chatMessage{Role: "system", Content: req.System})
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, chatTool{
			Type: "function",
			Function: chatToolFuncDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			out.Messages = append(out.Messages, chatMessage{Role: "user", Content: m.Text})
		case provider.RoleAssistant:
			msg := chatMessage{Role: "assistant", Content: m.Text}
			for _, call := range m.ToolCalls {
				msg.ToolCalls = append(msg.ToolCalls, chatToolCall{
					ID:   call.ID,
					Type: "function",
					Function: chatFunction{
						Name:      call.Name,
						Arguments: string(call.Args),
					},
				})
			}
			out.Messages = append(out.Messages, msg)
		case provider.RoleTool:
			out.Messages = append(out.Messages, chatMessage{
				Role:       "tool",
				Content:    m.ToolResult.Output,
				ToolCallID: m.ToolResult.CallID,
			})
		}
	}
	return out
}
