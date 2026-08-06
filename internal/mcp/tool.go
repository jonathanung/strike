package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// Permission is the permission.Ruleset name for all MCP-backed tools.
const Permission = "mcp"

type bridgeTool struct {
	client  session
	mcpName string // name on the wire (tools/call)
	strike  string // namespaced name exposed to the model
	desc    string
	schema  json.RawMessage
	server  string
}

func newBridge(client session, info toolInfo) tool.Tool {
	schema := info.InputSchema
	if len(schema) == 0 {
		schema = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	desc := strings.TrimSpace(info.Description)
	if desc == "" {
		desc = fmt.Sprintf("MCP tool %s from server %s", info.Name, client.Name())
	} else {
		desc = fmt.Sprintf("[mcp:%s] %s", client.Name(), desc)
	}
	return &bridgeTool{
		client:  client,
		mcpName: info.Name,
		strike:  NamespaceTool(client.Name(), info.Name),
		desc:    desc,
		schema:  schema,
		server:  client.Name(),
	}
}

// NamespaceTool builds the model-facing tool name: mcp_<server>_<tool>.
func NamespaceTool(server, toolName string) string {
	return "mcp_" + sanitizeName(server) + "_" + sanitizeName(toolName)
}

func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unnamed"
	}
	var b strings.Builder
	b.Grow(len(s))
	prevUnderscore := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
			prevUnderscore = false
			continue
		}
		if !prevUnderscore {
			b.WriteByte('_')
			prevUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unnamed"
	}
	if len(out) > 64 {
		out = out[:64]
	}
	return out
}

func (t *bridgeTool) Name() string        { return t.strike }
func (t *bridgeTool) Description() string { return t.desc }
func (t *bridgeTool) Schema() json.RawMessage {
	if len(t.schema) == 0 {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return t.schema
}

func (t *bridgeTool) Contract() tool.Contract {
	// MCP tools are external processes/servers; retry safety is unknown.
	return tool.Contract{
		Version:     tool.ContractVersion,
		SideEffect:  tool.SideEffectExternal,
		Idempotency: tool.IdempotencyConditional,
	}
}

func (t *bridgeTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if tc == nil || tc.Ask == nil {
		return tool.Result{}, tool.ErrInternal("mcp: permission ask unavailable")
	}
	pattern := t.server + "/" + t.mcpName
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: Permission,
		Patterns:   []string{pattern},
		Always:     []string{t.server + "/*", pattern},
	}); err != nil {
		return tool.Result{}, err
	}

	if t.client.Closed() {
		return tool.Result{}, mapMCPError(t.client.deadErr())
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	res, err := t.client.CallTool(callCtx, t.mcpName, args)
	if err != nil {
		return tool.Result{}, mapMCPError(err)
	}

	out := formatContent(res.Content)
	if res.IsError {
		if out == "" {
			out = "MCP tool returned an error"
		}
		// MCP isError payloads are free-text; map to internal fallback code.
		return tool.Result{}, mapMCPToolError(out)
	}
	meta, _ := json.Marshal(map[string]any{
		"mcpServer": t.server,
		"mcpTool":   t.mcpName,
	})
	title := t.strike
	if len(out) > 0 {
		// One-line preview for the UI cell title.
		line := out
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i]
		}
		if len(line) > 80 {
			line = line[:80] + "…"
		}
		if strings.TrimSpace(line) != "" {
			title = line
		}
	}
	return tool.Result{Title: title, Output: out, Metadata: meta}, nil
}

func formatContent(blocks []contentBlock) string {
	if len(blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text", "":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		default:
			// Non-text content (image, resource, …): note type only.
			parts = append(parts, fmt.Sprintf("[%s content]", b.Type))
		}
	}
	return strings.Join(parts, "\n")
}

// mapMCPError maps transport/client failures onto stable tool.CodedError codes.
// Unknown errors become CodeInternal without panicking.
func mapMCPError(err error) error {
	if err == nil {
		return nil
	}
	// Already structured (e.g. permission deny from Ask) — pass through.
	var te *tool.CodedError
	if errors.As(err, &te) && te != nil {
		return te
	}
	if errors.Is(err, context.Canceled) {
		return tool.ErrCanceled(err.Error())
	}
	if errors.Is(err, context.DeadlineExceeded) || tool.IsTimeout(err) {
		return tool.ErrTimeout(err.Error())
	}
	msg := err.Error()
	lower := strings.ToLower(msg)
	// Dead/disconnected MCP sessions are often worth a single retry after reconnect.
	if strings.Contains(lower, "not connected") ||
		strings.Contains(lower, "closed") ||
		strings.Contains(lower, "eof") ||
		strings.Contains(lower, "broken pipe") ||
		strings.Contains(lower, "connection reset") {
		return tool.ErrTransient(msg)
	}
	// Fallback: never panic; unknown MCP failures are internal.
	return tool.ErrInternal(msg)
}

// mapMCPToolError maps an MCP tools/call isError payload to a structured error.
// Free-text server errors have no stable vocabulary — use internal fallback.
func mapMCPToolError(message string) error {
	msg := strings.TrimSpace(message)
	if msg == "" {
		msg = "MCP tool returned an error"
	}
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "permission") && strings.Contains(lower, "denied"):
		return tool.ErrPermissionDenied(msg)
	case strings.Contains(lower, "invalid") && (strings.Contains(lower, "arg") || strings.Contains(lower, "param") || strings.Contains(lower, "input")):
		return tool.ErrInvalidArgs(msg)
	case strings.Contains(lower, "timed out") || strings.Contains(lower, "timeout") || strings.Contains(lower, "deadline"):
		return tool.ErrTimeout(msg)
	case strings.Contains(lower, "temporarily") || strings.Contains(lower, "try again") || strings.Contains(lower, "rate limit"):
		return tool.ErrTransient(msg)
	default:
		return tool.ErrInternal(msg)
	}
}
