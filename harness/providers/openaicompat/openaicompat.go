// Package openaicompat adapts OpenAI-compatible chat-completions APIs
// (OpenAI platform, xAI) to the provider interface. Credentials come from a
// per-request bearer source so OAuth refresh happens transparently — for
// xAI, a Grok OAuth access token used as the bearer bills the SuperGrok
// subscription directly against the standard API (no separate backend,
// unlike OpenAI's ChatGPT mode). Phase 0 is non-streaming.
package openaicompat

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/harness/providers/base"
)

// BearerSource resolves the Authorization bearer for each request.
type BearerSource = func(ctx context.Context) (string, error)

type Provider struct {
	base.Client
	baseURL string
	// images is false for compatible APIs whose chat-completions endpoint does
	// not accept image_url content parts.
	images bool
	// priorityTier is true for the OpenAI platform API, which accepts
	// service_tier=priority. xAI and other OpenAI-compatible hosts do not.
	priorityTier bool
}

func New(name, baseURL string, bearer BearerSource) *Provider {
	return NewWithHeaders(name, baseURL, bearer, nil)
}

// NewTextOnly creates a provider whose API accepts text-only chat messages.
// User images are omitted while retaining their accompanying text, allowing a
// conversation created with a vision model to continue after switching.
func NewTextOnly(name, baseURL string, bearer BearerSource) *Provider {
	p := New(name, baseURL, bearer)
	p.images = false
	return p
}

// NewWithHeaders is New plus optional static headers (custom gateways).
func NewWithHeaders(name, baseURL string, bearer BearerSource, headers map[string]string) *Provider {
	h := map[string]string{}
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
			Auth:         base.BearerAuth(bearer),
			Headers:      h,
		},
		baseURL: baseURL,
		images:  true,
	}
}

func NewOpenAI(bearer BearerSource) *Provider {
	p := New("openai", "https://api.openai.com/v1", bearer)
	p.priorityTier = true
	return p
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
	ServiceTier     string        `json:"service_tier,omitempty"`
	// PromptCacheKey improves sticky routing for shared prompt prefixes
	// (OpenAI prompt_cache_key / xAI conv affinity). Omitted when empty.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type chatMessage struct {
	// Content is a string for plain turns, or []chatContentPart when images
	// are attached (OpenAI multimodal chat-completions shape).
	Role    string `json:"role"`
	Content any    `json:"content,omitempty"`
	// ReasoningContent is optional CoT some OpenAI-compatible hosts return
	// alongside the final answer (not standard chat-completions).
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	ToolCalls        []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
}

type chatContentPart struct {
	Type     string        `json:"type"`
	Text     string        `json:"text,omitempty"`
	ImageURL *chatImageURL `json:"image_url,omitempty"`
}

type chatImageURL struct {
	URL string `json:"url"`
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
	Usage *chatUsage `json:"usage,omitempty"`
}

type chatUsage struct {
	PromptTokens        int                   `json:"prompt_tokens"`
	CompletionTokens    int                   `json:"completion_tokens"`
	TotalTokens         int                   `json:"total_tokens"`
	PromptTokensDetails *chatPromptTokDetails `json:"prompt_tokens_details,omitempty"`
}

// chatPromptTokDetails carries OpenAI/xAI cache breakouts. cached_tokens and
// cache_write_tokens are subsets of prompt_tokens (not additive extras).
type chatPromptTokDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	chatReq := toChatRequest(req, p.priorityTier, p.images)
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
		if choice.Message.ReasoningContent != "" {
			ch <- provider.StreamEvent{Type: provider.EventReasoning, Text: choice.Message.ReasoningContent}
		}
		if text := chatContentText(choice.Message.Content); text != "" {
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: text}
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
		done := provider.StreamEvent{Type: provider.EventDone, StopReason: choice.FinishReason}
		if resp.Usage != nil {
			done.Usage = chatUsageToProvider(resp.Usage)
		}
		ch <- done
	}), nil
}

// chatUsageToProvider maps chat-completions usage onto provider.Usage.
// OpenAI/xAI report prompt_tokens as the full prompt; cached_tokens and
// cache_write_tokens are subsets. Engine occupancy is
// Input+CacheRead+CacheCreation+Output, so uncached input is the remainder.
func chatUsageToProvider(u *chatUsage) *provider.Usage {
	if u == nil {
		return nil
	}
	out := &provider.Usage{
		InputTokens:  u.PromptTokens,
		OutputTokens: u.CompletionTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.PromptTokensDetails == nil {
		return out
	}
	cached := u.PromptTokensDetails.CachedTokens
	write := u.PromptTokensDetails.CacheWriteTokens
	if cached < 0 {
		cached = 0
	}
	if write < 0 {
		write = 0
	}
	if cached > out.InputTokens {
		cached = out.InputTokens
	}
	remain := out.InputTokens - cached
	if write > remain {
		write = remain
	}
	out.CacheReadTokens = cached
	out.CacheCreationTokens = write
	out.InputTokens = remain - write
	return out
}

func toChatRequest(req provider.Request, priorityTier, images bool) chatRequest {
	out := chatRequest{Model: req.Model, ReasoningEffort: base.OpenAIEffort(req.Effort)}
	if req.Priority && priorityTier {
		out.ServiceTier = "priority"
	}
	if k := strings.TrimSpace(req.CacheKey); k != "" {
		out.PromptCacheKey = k
	}
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
			out.Messages = append(out.Messages, chatMessage{Role: "user", Content: userContent(m, images)})
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

// userContent is plain text when images are unsupported or absent; otherwise
// it is a multimodal parts array (text + image_url data URIs).
func userContent(m provider.Message, images bool) any {
	if !images || len(m.Images) == 0 {
		return m.Text
	}
	parts := make([]chatContentPart, 0, 1+len(m.Images))
	if m.Text != "" {
		parts = append(parts, chatContentPart{Type: "text", Text: m.Text})
	}
	for _, img := range m.Images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		url := "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img.Data)
		parts = append(parts, chatContentPart{
			Type:     "image_url",
			ImageURL: &chatImageURL{URL: url},
		})
	}
	if len(parts) == 0 {
		return m.Text
	}
	return parts
}

// chatContentText extracts assistant prose from a string or multimodal content value.
func chatContentText(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return ""
		}
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
		var parts []chatContentPart
		if err := json.Unmarshal(raw, &parts); err != nil {
			return ""
		}
		var b string
		for _, p := range parts {
			if p.Type == "text" || p.Type == "" {
				b += p.Text
			}
		}
		return b
	}
}
