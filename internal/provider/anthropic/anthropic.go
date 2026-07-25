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
	// baseURL is overridable so tests can point at an httptest server; it
	// falls back to the public API when empty.
	baseURL string
}

func New(apiKey string) (*Provider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("no Anthropic credentials: set ANTHROPIC_API_KEY, run `strike auth login anthropic`, or use --provider echo for the offline dev provider")
	}
	return &Provider{Client: base.Client{
		ProviderName: "anthropic",
		HTTP:         &http.Client{Timeout: 5 * time.Minute},
		Headers: map[string]string{
			"x-api-key":         apiKey,
			"anthropic-version": apiVersion,
		},
	}}, nil
}

// NewCustom builds an Anthropic-messages adapter for a named custom/self-hosted
// endpoint. baseURL is the origin (e.g. https://api.anthropic.com); the client
// appends /v1/messages. Extra headers are merged after the auth headers.
// apiKey may be empty for open proxies; x-api-key is omitted when blank.
func NewCustom(name, baseURL, apiKey string, headers map[string]string) (*Provider, error) {
	if name == "" {
		return nil, fmt.Errorf("custom provider name is required")
	}
	if baseURL == "" {
		return nil, fmt.Errorf("custom provider %s: baseURL is required", name)
	}
	h := map[string]string{
		"anthropic-version": apiVersion,
	}
	if apiKey != "" {
		h["x-api-key"] = apiKey
	}
	for k, v := range headers {
		if k == "" {
			continue
		}
		h[k] = v
	}
	return &Provider{
		Client: base.Client{
			ProviderName: name,
			HTTP:         &http.Client{Timeout: 5 * time.Minute},
			Headers:      h,
		},
		baseURL: baseURL,
	}, nil
}

func (p *Provider) endpoint() string {
	if p.baseURL != "" {
		return p.baseURL + "/v1/messages"
	}
	return defaultBaseURL + "/v1/messages"
}

// Wire types for the Messages API.

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []apiMessage `json:"messages"`
	Tools     []apiTool    `json:"tools,omitempty"`
	// Thinking and OutputConfig carry the reasoning dial. budget_tokens is
	// gone from current models (it now 400s), so depth is expressed as
	// adaptive thinking plus an output_config.effort level.
	Thinking     *apiThinking     `json:"thinking,omitempty"`
	OutputConfig *apiOutputConfig `json:"output_config,omitempty"`
}

type apiThinking struct {
	Type string `json:"type"` // "adaptive" | "disabled"
}

type apiOutputConfig struct {
	Effort string `json:"effort,omitempty"` // low | medium | high | xhigh | max
}

// apiMessage content is held as raw blocks so thinking blocks can be replayed
// byte-for-byte; the API rejects a thinking block whose content was modified.
type apiMessage struct {
	Role    string            `json:"role"`
	Content []json.RawMessage `json:"content"`
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
	// Content stays raw so thinking blocks survive the round trip untouched.
	Content    []json.RawMessage `json:"content"`
	StopReason string            `json:"stop_reason"`
	Usage      *apiUsage         `json:"usage,omitempty"`
}

type apiUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	apiReq, err := toAPIRequest(req)
	if err != nil {
		return nil, err
	}
	return base.Stream(func(ch chan<- provider.StreamEvent) {
		var resp apiResponse
		if err := p.PostJSON(ctx, p.endpoint(), apiReq, &resp); err != nil {
			base.Fail(ch, err)
			return
		}
		for _, raw := range resp.Content {
			var block apiBlock
			if err := json.Unmarshal(raw, &block); err != nil {
				base.Fail(ch, fmt.Errorf("anthropic: malformed content block: %w", err))
				return
			}
			switch block.Type {
			case "text":
				ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: block.Text}
			case "tool_use":
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:   block.ID,
					Name: block.Name,
					Args: block.Input,
				}}
			case "thinking", "redacted_thinking":
				// Carried verbatim, not decoded: the next request must echo
				// these back unchanged or the API rejects the turn. The text
				// is empty under the default display setting, which is fine —
				// an empty block still has to be replayed.
				ch <- provider.StreamEvent{Type: provider.EventReasoning, Reasoning: raw}
			}
		}
		done := provider.StreamEvent{Type: provider.EventDone, StopReason: resp.StopReason}
		if resp.Usage != nil {
			done.Usage = &provider.Usage{
				InputTokens:         resp.Usage.InputTokens,
				OutputTokens:        resp.Usage.OutputTokens,
				CacheReadTokens:     resp.Usage.CacheReadInputTokens,
				CacheCreationTokens: resp.Usage.CacheCreationInputTokens,
			}
		}
		ch <- done
	}), nil
}

func toAPIRequest(req provider.Request) (apiRequest, error) {
	out := apiRequest{
		Model:     req.Model,
		MaxTokens: req.MaxTokens,
		System:    req.System,
	}
	applyEffort(&out, req.Effort)
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, apiTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			blocks, err := rawBlocks(apiBlock{Type: "text", Text: m.Text})
			if err != nil {
				return apiRequest{}, err
			}
			out.Messages = append(out.Messages, apiMessage{Role: "user", Content: blocks})
		case provider.RoleAssistant:
			// Thinking blocks must lead the assistant turn, in the order the
			// model produced them. They are dropped only when the caller has
			// explicitly turned reasoning off for this request.
			var blocks []json.RawMessage
			if req.Effort != provider.EffortOff {
				blocks = append(blocks, m.Reasoning...)
			}
			var typed []apiBlock
			if m.Text != "" {
				typed = append(typed, apiBlock{Type: "text", Text: m.Text})
			}
			for _, call := range m.ToolCalls {
				input := call.Args
				if len(input) == 0 {
					input = json.RawMessage(`{}`)
				}
				typed = append(typed, apiBlock{Type: "tool_use", ID: call.ID, Name: call.Name, Input: input})
			}
			encoded, err := rawBlocks(typed...)
			if err != nil {
				return apiRequest{}, err
			}
			out.Messages = append(out.Messages, apiMessage{Role: "assistant", Content: append(blocks, encoded...)})
		case provider.RoleTool:
			blocks, err := rawBlocks(apiBlock{
				Type:      "tool_result",
				ToolUseID: m.ToolResult.CallID,
				Content:   m.ToolResult.Output,
				IsError:   m.ToolResult.IsError,
			})
			if err != nil {
				return apiRequest{}, err
			}
			out.Messages = append(out.Messages, apiMessage{Role: "user", Content: blocks})
		}
	}
	return out, nil
}

// applyEffort maps the normalized dial onto the Messages API reasoning
// fields. EffortOff deliberately sends no output_config: disabled thinking is
// only accepted at effort "high" or below, and omitting the field leaves the
// default of "high".
func applyEffort(out *apiRequest, effort provider.Effort) {
	switch effort {
	case provider.EffortDefault:
		// Send nothing; the model's own default applies.
	case provider.EffortOff:
		out.Thinking = &apiThinking{Type: "disabled"}
	default:
		out.Thinking = &apiThinking{Type: "adaptive"}
		out.OutputConfig = &apiOutputConfig{Effort: string(effort)}
	}
}

func rawBlocks(blocks ...apiBlock) ([]json.RawMessage, error) {
	out := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		data, err := json.Marshal(block)
		if err != nil {
			return nil, fmt.Errorf("anthropic: encoding %s block: %w", block.Type, err)
		}
		out = append(out, data)
	}
	return out, nil
}
