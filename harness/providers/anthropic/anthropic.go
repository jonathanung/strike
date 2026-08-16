// Package anthropic adapts the Anthropic Messages API to the provider
// interface. Phase 0 uses non-streaming requests and emits the whole
// response as one delta; SSE streaming is Phase 1.
package anthropic

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

const (
	// defaultBaseURL is the Anthropic origin. OpenCode / @ai-sdk/anthropic use
	// https://api.anthropic.com/v1 as baseURL and append /messages; MessagesURL
	// accepts both shapes.
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
// endpoint. baseURL matches OpenCode/AI SDK shape: either the API root including
// /v1 (https://api.anthropic.com/v1) or an origin (https://api.anthropic.com).
// MessagesURL joins the correct /messages path. Extra headers merge after auth.
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
	return MessagesURL(p.baseURL)
}

// MessagesURL joins baseURL onto the Anthropic Messages path the way OpenCode
// and @ai-sdk/anthropic do:
//
//   - empty → https://api.anthropic.com/v1/messages
//   - …/messages (full endpoint) → unchanged
//   - …/v1 (OpenCode/AI SDK baseURL) → …/v1/messages
//   - origin only → …/v1/messages
//
// This avoids the double-/v1 404 when users paste OpenCode configs with /v1.
func MessagesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return defaultBaseURL + "/v1/messages"
	}
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	if strings.HasSuffix(base, "/v1") {
		return base + "/messages"
	}
	return base + "/v1/messages"
}

// Wire types for the Messages API.

type apiRequest struct {
	Model     string `json:"model"`
	MaxTokens int    `json:"max_tokens"`
	// System is an array of text blocks so cache_control can mark the stable
	// system prefix (string form cannot carry breakpoints).
	System   []apiSystemBlock `json:"system,omitempty"`
	Messages []apiMessage     `json:"messages"`
	Tools    []apiTool        `json:"tools,omitempty"`
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

// apiCacheControl is Anthropic's ephemeral prompt-cache breakpoint marker.
// Max 4 per request; we place up to 3 (system, last tool, last message block).
type apiCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type apiSystemBlock struct {
	Type         string           `json:"type"` // "text"
	Text         string           `json:"text"`
	CacheControl *apiCacheControl `json:"cache_control,omitempty"`
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
	// image
	Source *apiImageSource `json:"source,omitempty"`
	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// cache_control marks a prompt-cache breakpoint on eligible blocks.
	CacheControl *apiCacheControl `json:"cache_control,omitempty"`
}

type apiImageSource struct {
	Type      string `json:"type"` // base64
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type apiTool struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	InputSchema  json.RawMessage  `json:"input_schema"`
	CacheControl *apiCacheControl `json:"cache_control,omitempty"`
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
				// these back unchanged or the API rejects the turn. Display
				// text is often empty under the default setting; empty prose
				// still replays the opaque block.
				ch <- provider.StreamEvent{
					Type:      provider.EventReasoning,
					Reasoning: raw,
					Text:      provider.ReasoningText(raw),
				}
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
	}
	if req.System != "" {
		out.System = []apiSystemBlock{{Type: "text", Text: req.System}}
	}
	applyEffort(&out, req.Effort)
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, apiTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			blocks, err := rawBlocks(userBlocks(m)...)
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
	applyPromptCache(&out)
	return out, nil
}

// applyPromptCache places ephemeral cache_control breakpoints on the stable
// Anthropic request prefix (OpenCode applyCaching / Claude Code patterns).
//
// Up to 3 of Anthropic's 4 allowed breakpoints:
//  1. last system text block — agent/system prompt (stable across turns)
//  2. last tool definition — tool schema prefix (stable when registry is)
//  3. last eligible message content block — conversation prefix through the
//     prior tail; moves each turn so the growing history remains cacheable
//
// thinking / redacted_thinking blocks are skipped (API rejects cache_control
// on them; Claude Code does the same). Agent switches that rewrite system or
// tools correctly miss cache — that is intentional, not thrash.
func applyPromptCache(req *apiRequest) {
	ephemeral := &apiCacheControl{Type: "ephemeral"}
	if n := len(req.System); n > 0 {
		req.System[n-1].CacheControl = ephemeral
	}
	if n := len(req.Tools); n > 0 {
		req.Tools[n-1].CacheControl = ephemeral
	}
	for i := len(req.Messages) - 1; i >= 0; i-- {
		msg := &req.Messages[i]
		for j := len(msg.Content) - 1; j >= 0; j-- {
			if markBlockCacheControl(&msg.Content[j], ephemeral) {
				return
			}
		}
	}
}

// markBlockCacheControl attaches cache_control to an eligible content block.
// Returns false for thinking blocks and malformed JSON so the caller can keep
// walking backward for a better breakpoint.
func markBlockCacheControl(raw *json.RawMessage, cc *apiCacheControl) bool {
	var block apiBlock
	if err := json.Unmarshal(*raw, &block); err != nil {
		return false
	}
	switch block.Type {
	case "text", "tool_use", "tool_result", "image":
	default:
		return false
	}
	block.CacheControl = cc
	data, err := json.Marshal(block)
	if err != nil {
		return false
	}
	*raw = data
	return true
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

// userBlocks builds Anthropic content blocks for a user turn (text + images).
func userBlocks(m provider.Message) []apiBlock {
	var blocks []apiBlock
	for _, img := range m.Images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		blocks = append(blocks, apiBlock{
			Type: "image",
			Source: &apiImageSource{
				Type:      "base64",
				MediaType: mime,
				Data:      base64.StdEncoding.EncodeToString(img.Data),
			},
		})
	}
	if m.Text != "" || len(blocks) == 0 {
		blocks = append(blocks, apiBlock{Type: "text", Text: m.Text})
	}
	return blocks
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
