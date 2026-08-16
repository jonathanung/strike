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
	"github.com/jonathanung/strike-cli/providers/base"
)

// ResponsesProvider talks to the OpenAI platform Responses API
// ({baseURL}/responses). Matches OpenCode / @ai-sdk/openai default languageModel
// (not chat-completions). Phase 0 is non-streaming.
type ResponsesProvider struct {
	base.Client
	baseURL string
}

// NewResponses builds a Responses API adapter. baseURL is the OpenCode-style
// root including /v1 (e.g. https://api.openai.com/v1); /responses is appended.
func NewResponses(name, baseURL string, bearer BearerSource, headers map[string]string) *ResponsesProvider {
	h := map[string]string{}
	for k, v := range headers {
		if k == "" {
			continue
		}
		h[k] = v
	}
	return &ResponsesProvider{
		Client: base.Client{
			ProviderName: name,
			HTTP:         &http.Client{Timeout: 5 * time.Minute},
			Auth:         base.BearerAuth(bearer),
			Headers:      h,
		},
		baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
	}
}

func (p *ResponsesProvider) endpoint() string {
	return ResponsesURL(p.baseURL)
}

// ResponsesURL joins baseURL onto /responses (OpenCode / @ai-sdk/openai).
// A base that already ends with /responses is left unchanged.
func ResponsesURL(baseURL string) string {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return "https://api.openai.com/v1/responses"
	}
	if strings.HasSuffix(base, "/responses") {
		return base
	}
	return base + "/responses"
}

type responsesRequest struct {
	Model        string               `json:"model"`
	Instructions string               `json:"instructions,omitempty"`
	Input        []responsesInputItem `json:"input"`
	Tools        []responsesTool      `json:"tools,omitempty"`
	ToolChoice   string               `json:"tool_choice,omitempty"`
	Store        bool                 `json:"store"`
	Stream       bool                 `json:"stream"`
	Reasoning    *responsesReasoning  `json:"reasoning,omitempty"`
	// PromptCacheKey improves sticky routing for shared prompt prefixes
	// (OpenAI/xAI prompt_cache_key). Omitted when empty.
	PromptCacheKey string `json:"prompt_cache_key,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responsesInputItem struct {
	Type    string                  `json:"type,omitempty"`
	Role    string                  `json:"role,omitempty"`
	Content []responsesContentBlock `json:"content,omitempty"`
	// function_call
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

type responsesContentBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL string `json:"image_url,omitempty"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type responsesResponse struct {
	Status string                `json:"status"`
	Output []responsesOutputItem `json:"output"`
	Usage  *responsesUsage       `json:"usage,omitempty"`
	Error  *responsesError       `json:"error,omitempty"`
}

type responsesError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

type responsesUsage struct {
	InputTokens        int                       `json:"input_tokens"`
	OutputTokens       int                       `json:"output_tokens"`
	TotalTokens        int                       `json:"total_tokens"`
	InputTokensDetails *responsesInputTokDetails `json:"input_tokens_details,omitempty"`
}

// responsesInputTokDetails carries OpenAI/xAI cache breakouts. Fields are
// subsets of input_tokens (not additive extras).
type responsesInputTokDetails struct {
	CachedTokens     int `json:"cached_tokens"`
	CacheWriteTokens int `json:"cache_write_tokens"`
}

type responsesOutputItem struct {
	Type      string                  `json:"type"`
	Role      string                  `json:"role,omitempty"`
	Content   []responsesContentBlock `json:"content,omitempty"`
	Name      string                  `json:"name,omitempty"`
	Arguments string                  `json:"arguments,omitempty"`
	CallID    string                  `json:"call_id,omitempty"`
	// reasoning summary parts (optional)
	Summary []responsesContentBlock `json:"summary,omitempty"`
}

func (p *ResponsesProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	body := toResponsesRequest(req)
	return base.Stream(func(ch chan<- provider.StreamEvent) {
		var resp responsesResponse
		if err := p.PostJSON(ctx, p.endpoint(), body, &resp); err != nil {
			base.Fail(ch, err)
			return
		}
		if resp.Error != nil && resp.Error.Message != "" {
			base.Fail(ch, fmt.Errorf("%s: %s", p.Name(), resp.Error.Message))
			return
		}
		for _, item := range resp.Output {
			switch item.Type {
			case "message":
				for _, c := range item.Content {
					if (c.Type == "output_text" || c.Type == "text") && c.Text != "" {
						ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: c.Text}
					}
				}
			case "function_call":
				args := json.RawMessage(item.Arguments)
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:   item.CallID,
					Name: item.Name,
					Args: args,
				}}
			case "reasoning":
				for _, c := range item.Summary {
					if c.Text != "" {
						ch <- provider.StreamEvent{Type: provider.EventReasoning, Text: c.Text}
					}
				}
				for _, c := range item.Content {
					if c.Text != "" {
						ch <- provider.StreamEvent{Type: provider.EventReasoning, Text: c.Text}
					}
				}
			}
		}
		done := provider.StreamEvent{Type: provider.EventDone, StopReason: resp.Status}
		if done.StopReason == "" {
			done.StopReason = "completed"
		}
		if resp.Usage != nil {
			done.Usage = responsesUsageToProvider(resp.Usage)
		}
		ch <- done
	}), nil
}

// responsesUsageToProvider maps Responses API usage onto provider.Usage with
// the same subset accounting as chatUsageToProvider.
func responsesUsageToProvider(u *responsesUsage) *provider.Usage {
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
		Stream:       false,
	}
	if k := strings.TrimSpace(req.CacheKey); k != "" {
		out.PromptCacheKey = k
	}
	if effort := base.OpenAIEffort(req.Effort); effort != "" {
		out.Reasoning = &responsesReasoning{Effort: effort}
	}
	for _, t := range req.Tools {
		out.Tools = append(out.Tools, responsesTool{
			Type:        "function",
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	if len(out.Tools) == 0 {
		out.ToolChoice = ""
	}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			out.Input = append(out.Input, responsesInputItem{
				Type:    "message",
				Role:    "user",
				Content: responsesUserContent(m),
			})
		case provider.RoleAssistant:
			if m.Text != "" {
				out.Input = append(out.Input, responsesInputItem{
					Type: "message",
					Role: "assistant",
					Content: []responsesContentBlock{
						{Type: "output_text", Text: m.Text},
					},
				})
			}
			for _, call := range m.ToolCalls {
				out.Input = append(out.Input, responsesInputItem{
					Type:      "function_call",
					Name:      call.Name,
					Arguments: string(call.Args),
					CallID:    call.ID,
				})
			}
		case provider.RoleTool:
			out.Input = append(out.Input, responsesInputItem{
				Type:   "function_call_output",
				CallID: m.ToolResult.CallID,
				Output: m.ToolResult.Output,
			})
		}
	}
	return out
}

func responsesUserContent(m provider.Message) []responsesContentBlock {
	blocks := make([]responsesContentBlock, 0, 1+len(m.Images))
	if m.Text != "" || len(m.Images) == 0 {
		blocks = append(blocks, responsesContentBlock{Type: "input_text", Text: m.Text})
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
		blocks = append(blocks, responsesContentBlock{Type: "input_image", ImageURL: url})
	}
	return blocks
}
