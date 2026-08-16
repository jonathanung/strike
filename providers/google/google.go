// Package google adapts the Google AI Studio generateContent API to the provider interface.
package google

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/providers/base"
)

const defaultBaseURL = "https://generativelanguage.googleapis.com/v1beta"

// TokenSource resolves a Google AI Studio API key per request.
type TokenSource = func(ctx context.Context) (string, error)

type Provider struct {
	base.Client
	baseURL string
}

func New(source TokenSource) *Provider {
	return &Provider{
		Client: base.Client{
			ProviderName: "google",
			HTTP:         &http.Client{Timeout: 5 * time.Minute},
			Auth: func(ctx context.Context, req *http.Request) error {
				token, err := source(ctx)
				if err != nil {
					return err
				}
				if token != "" {
					// Google AI Studio API keys use x-goog-api-key
					// (OAuth is not a supported auth path for this provider).
					req.Header.Set("x-goog-api-key", token)
				}
				return nil
			},
		},
		baseURL: defaultBaseURL,
	}
}

type apiRequest struct {
	SystemInstruction *apiContent       `json:"systemInstruction,omitempty"`
	Contents          []apiContent      `json:"contents"`
	Tools             []apiTool         `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type generationConfig struct {
	MaxOutputTokens int `json:"maxOutputTokens,omitempty"`
}

type apiContent struct {
	Role  string    `json:"role,omitempty"`
	Parts []apiPart `json:"parts"`
}

type apiPart struct {
	Text             string               `json:"text,omitempty"`
	InlineData       *inlineData          `json:"inlineData,omitempty"`
	FunctionCall     *apiFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *apiFunctionResponse `json:"functionResponse,omitempty"`
}

type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}

type apiFunctionCall struct {
	Name string          `json:"name"`
	Args json.RawMessage `json:"args,omitempty"`
}

type apiFunctionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type apiTool struct {
	FunctionDeclarations []apiFunctionDeclaration `json:"functionDeclarations"`
}

type apiFunctionDeclaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
}

type apiResponse struct {
	Candidates []struct {
		Content      apiContent `json:"content"`
		FinishReason string     `json:"finishReason"`
	} `json:"candidates"`
	UsageMetadata *usageMetadata `json:"usageMetadata,omitempty"`
}

type usageMetadata struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

func (p *Provider) Stream(ctx context.Context, req provider.Request) (<-chan provider.StreamEvent, error) {
	apiReq, err := toAPIRequest(req)
	if err != nil {
		return nil, err
	}
	return base.Stream(func(ch chan<- provider.StreamEvent) {
		var resp apiResponse
		if err := p.PostJSON(ctx, p.endpoint(req.Model), apiReq, &resp); err != nil {
			base.Fail(ch, err)
			return
		}
		if len(resp.Candidates) == 0 {
			base.Fail(ch, fmt.Errorf("google: response has no candidates"))
			return
		}
		candidate := resp.Candidates[0]
		for i, part := range candidate.Content.Parts {
			if part.Text != "" {
				ch <- provider.StreamEvent{Type: provider.EventTextDelta, Text: part.Text}
			}
			if part.FunctionCall != nil {
				args := part.FunctionCall.Args
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				ch <- provider.StreamEvent{Type: provider.EventToolCall, ToolCall: &provider.ToolCall{
					ID:   fmt.Sprintf("%s-%d", part.FunctionCall.Name, i),
					Name: part.FunctionCall.Name,
					Args: args,
				}}
			}
		}
		done := provider.StreamEvent{Type: provider.EventDone, StopReason: candidate.FinishReason}
		if resp.UsageMetadata != nil {
			done.Usage = &provider.Usage{
				InputTokens:  resp.UsageMetadata.PromptTokenCount,
				OutputTokens: resp.UsageMetadata.CandidatesTokenCount,
				TotalTokens:  resp.UsageMetadata.TotalTokenCount,
			}
		}
		ch <- done
	}), nil
}

func (p *Provider) endpoint(model string) string {
	model = strings.TrimPrefix(model, "models/")
	return strings.TrimRight(p.baseURL, "/") + "/models/" + url.PathEscape(model) + ":generateContent"
}

func toAPIRequest(req provider.Request) (apiRequest, error) {
	out := apiRequest{}
	if req.System != "" {
		out.SystemInstruction = &apiContent{Parts: []apiPart{{Text: req.System}}}
	}
	if req.MaxTokens > 0 {
		out.GenerationConfig = &generationConfig{MaxOutputTokens: req.MaxTokens}
	}
	if len(req.Tools) > 0 {
		tool := apiTool{FunctionDeclarations: make([]apiFunctionDeclaration, 0, len(req.Tools))}
		for _, t := range req.Tools {
			tool.FunctionDeclarations = append(tool.FunctionDeclarations, apiFunctionDeclaration{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			})
		}
		out.Tools = []apiTool{tool}
	}
	callNames := map[string]string{}
	for _, m := range req.Messages {
		switch m.Role {
		case provider.RoleUser:
			out.Contents = append(out.Contents, apiContent{Role: "user", Parts: userParts(m)})
		case provider.RoleAssistant:
			parts := make([]apiPart, 0, 1+len(m.ToolCalls))
			if m.Text != "" {
				parts = append(parts, apiPart{Text: m.Text})
			}
			for _, call := range m.ToolCalls {
				args := call.Args
				if len(args) == 0 {
					args = json.RawMessage(`{}`)
				}
				callNames[call.ID] = call.Name
				parts = append(parts, apiPart{FunctionCall: &apiFunctionCall{Name: call.Name, Args: args}})
			}
			if len(parts) > 0 {
				out.Contents = append(out.Contents, apiContent{Role: "model", Parts: parts})
			}
		case provider.RoleTool:
			if m.ToolResult == nil {
				continue
			}
			name := callNames[m.ToolResult.CallID]
			if name == "" {
				name = m.ToolResult.CallID
			}
			out.Contents = append(out.Contents, apiContent{Role: "user", Parts: []apiPart{{FunctionResponse: &apiFunctionResponse{
				Name: name,
				Response: map[string]any{
					"output":   m.ToolResult.Output,
					"is_error": m.ToolResult.IsError,
				},
			}}}})
		}
	}
	return out, nil
}

func userParts(m provider.Message) []apiPart {
	parts := make([]apiPart, 0, 1+len(m.Images))
	if m.Text != "" {
		parts = append(parts, apiPart{Text: m.Text})
	}
	for _, img := range m.Images {
		if len(img.Data) == 0 {
			continue
		}
		mime := img.MIME
		if mime == "" {
			mime = "image/png"
		}
		parts = append(parts, apiPart{InlineData: &inlineData{
			MIMEType: mime,
			Data:     base64.StdEncoding.EncodeToString(img.Data),
		}})
	}
	if len(parts) == 0 {
		return []apiPart{{Text: ""}}
	}
	return parts
}
