// Package chatgpt adapts OpenAI's ChatGPT backend (Responses API at
// chatgpt.com/backend-api/codex) to the provider interface. This is the
// subscription-billed transport used by ChatGPT sign-in — the OAuth access
// token plus a ChatGPT-Account-Id header, never a platform API key.
// Requests are stateless (store: false, full input each turn) and always
// streamed, matching how Codex CLI and opencode drive this endpoint.
package chatgpt

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/provider/base"
)

const defaultEndpoint = "https://chatgpt.com/backend-api/codex/responses"

// TokenSource resolves the OAuth access token and ChatGPT account id.
type TokenSource func(ctx context.Context) (access, accountID string, err error)

type Provider struct {
	base.Client
	endpoint string
}

func New(source TokenSource) *Provider {
	return &Provider{
		Client: base.Client{
			ProviderName: "openai (chatgpt)",
			HTTP:         &http.Client{}, // no client timeout: responses stream; ctx governs
			Auth:         chatGPTAuth(source),
			// The backend gates on a known CLI originator, same as reusing
			// the public client id for login.
			Headers: map[string]string{
				"OpenAI-Beta": "responses=experimental",
				"originator":  "codex_cli_rs",
				"session_id":  newUUID(),
			},
		},
		endpoint: defaultEndpoint,
	}
}

// chatGPTAuth applies the subscription credentials: bearer access token
// plus the ChatGPT-Account-Id header.
func chatGPTAuth(source TokenSource) base.AuthFunc {
	return func(ctx context.Context, req *http.Request) error {
		access, accountID, err := source(ctx)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("ChatGPT-Account-Id", accountID)
		return nil
	}
}

// Responses API wire types (the subset we use). Codex CLI and OpenClaw both
// send prompt_cache_key + include=["reasoning.encrypted_content"] on this
// transport; matching that shape keeps sticky cache routing and multi-turn
// reasoning continuity on the subscription backend.

type responsesRequest struct {
	Model             string           `json:"model"`
	Instructions      string           `json:"instructions,omitempty"`
	Input             []inputItem      `json:"input"`
	Tools             []responseTool   `json:"tools,omitempty"`
	ToolChoice        string           `json:"tool_choice"`
	ParallelToolCalls bool             `json:"parallel_tool_calls"`
	Store             bool             `json:"store"`
	Stream            bool             `json:"stream"`
	Include           []string         `json:"include"`
	Reasoning         *reasoningConfig `json:"reasoning,omitempty"`
	// PromptCacheKey improves sticky routing for shared prompt prefixes
	// (same field as platform Responses). Omitted when empty. Do not send
	// prompt_cache_retention — the chatgpt.com backend rejects it with 400.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type reasoningConfig struct {
	Effort string `json:"effort,omitempty"`
}

type inputItem struct {
	Type    string         `json:"type"`
	Role    string         `json:"role,omitempty"`
	Content []contentBlock `json:"content,omitempty"`
	// function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	// function_call_output
	Output *string `json:"output,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
	// input_image (Responses API): data URI or remote URL
	ImageURL string `json:"image_url,omitempty"`
}

type responseTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Strict      bool            `json:"strict"`
	Parameters  json.RawMessage `json:"parameters"`
}

// sseEvent is the union of streamed event payloads we care about.
type sseEvent struct {
	Type  string `json:"type"`
	Delta string `json:"delta"`
	Item  *struct {
		Type      string `json:"type"`
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
		CallID    string `json:"call_id"`
	} `json:"item"`
	Response *struct {
		Status string `json:"status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
		Usage *streamUsage `json:"usage"`
	} `json:"response"`
	Message string `json:"message"` // top-level error events
}

// streamUsage is Responses usage on response.completed. Cache breakouts are
// subsets of input_tokens (same accounting as openaicompat Responses).
type streamUsage struct {
	InputTokens        int                    `json:"input_tokens"`
	OutputTokens       int                    `json:"output_tokens"`
	TotalTokens        int                    `json:"total_tokens"`
	InputTokensDetails *streamInputTokDetails `json:"input_tokens_details,omitempty"`
}

type streamInputTokDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	// Prefer the session id for backend session affinity when the engine
	// stamped CacheKey (matches Codex session_id / prompt_cache_key pairing).
	if k := strings.TrimSpace(req.CacheKey); k != "" && p.Headers != nil {
		p.Headers["session_id"] = k
	}
	body, err := p.PostSSE(ctx, p.endpoint, toResponsesRequest(req))
	if err != nil {
		return nil, err
	}
	return base.Stream(func(ch chan<- provider.StreamEvent) {
		defer body.Close()
		if err := p.readStream(body, ch); err != nil {
			base.Fail(ch, err)
		}
	}), nil
}

// readStream parses the SSE stream, emitting normalized provider events.
// Returns an error only for stream-level failures; a clean
// response.completed emits EventDone and returns nil.
func (p *Provider) readStream(body io.Reader, ch chan<- provider.StreamEvent) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // ignore malformed keep-alives
		}
		switch ev.Type {
		case "response.output_text.delta":
			ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: ev.Delta}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			if ev.Delta != "" {
				ch <- provider.StreamEvent{Type: provider.EventReasoning, Text: ev.Delta}
			}
		case "response.output_item.done":
			if ev.Item != nil && ev.Item.Type == "function_call" {
				args := json.RawMessage(ev.Item.Arguments)
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:   ev.Item.CallID,
					Name: ev.Item.Name,
					Args: args,
				}}
			}
		case "response.completed":
			done := provider.StreamEvent{Type: provider.EventDone, StopReason: "completed"}
			if ev.Response != nil && ev.Response.Usage != nil {
				done.Usage = streamUsageToProvider(ev.Response.Usage)
			}
			ch <- done
			return nil
		case "response.failed":
			msg := "response failed"
			if ev.Response != nil && ev.Response.Error != nil {
				msg = ev.Response.Error.Message
			}
			return fmt.Errorf("chatgpt backend: %s", msg)
		case "error":
			return fmt.Errorf("chatgpt backend: %s", ev.Message)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return fmt.Errorf("chatgpt backend: stream ended without response.completed")
}

// streamUsageToProvider maps Responses usage onto provider.Usage. Cache
// counts are subsets of input_tokens (not additive extras).
func streamUsageToProvider(u *streamUsage) *provider.Usage {
	if u == nil {
		return nil
	}
	out := &provider.Usage{
		InputTokens:  u.InputTokens,
		OutputTokens: u.OutputTokens,
		TotalTokens:  u.TotalTokens,
	}
	if u.InputTokensDetails == nil {
		return out
	}
	cached := u.InputTokensDetails.CachedTokens
	write := u.InputTokensDetails.CacheWriteTokens
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

func toResponsesRequest(req provider.Request) responsesRequest {
	out := responsesRequest{
		Model:        req.Model,
		Instructions: req.System,
		ToolChoice:   "auto",
		Store:        false,
		Stream:       true,
		// Codex / OpenClaw always request encrypted reasoning content so
		// multi-turn reasoning models can resume without re-prefilling.
		Include: []string{"reasoning.encrypted_content"},
	}
	if k := strings.TrimSpace(req.CacheKey); k != "" {
		out.PromptCacheKey = k
	}
	if effort := base.OpenAIEffort(req.Effort); effort != "" {
		out.Reasoning = &reasoningConfig{Effort: effort}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, responseTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Strict:      false,
			Parameters:  t.InputSchema,
		})
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			out.Input = append(out.Input, inputItem{
				Type:    "message",
				Role:    "user",
				Content: userContentBlocks(m),
			})
		case provider.RoleAssistant:
			if m.Text != "" {
				out.Input = append(out.Input, inputItem{
					Type:    "message",
					Role:    "assistant",
					Content: []contentBlock{{Type: "output_text", Text: m.Text}},
				})
			}
			for _, call := range m.ToolCalls {
				out.Input = append(out.Input, inputItem{
					Type:      "function_call",
					Name:      call.Name,
					Arguments: string(call.Args),
					CallID:    call.ID,
				})
			}
		case provider.RoleTool:
			output := m.ToolResult.Output
			out.Input = append(out.Input, inputItem{
				Type:   "function_call_output",
				CallID: m.ToolResult.CallID,
				Output: &output,
			})
		}
	}
	return out
}

func userContentBlocks(m provider.Message) []contentBlock {
	blocks := make([]contentBlock, 0, 1+len(m.Images))
	if m.Text != "" || len(m.Images) == 0 {
		blocks = append(blocks, contentBlock{Type: "input_text", Text: m.Text})
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
		blocks = append(blocks, contentBlock{Type: "input_image", ImageURL: url})
	}
	return blocks
}

func newUUID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
