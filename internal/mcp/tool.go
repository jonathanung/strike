package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"github.com/jonathanung/strike-cli/internal/tool"
)

// Permission is the permission.Ruleset name for all MCP-backed tools.
const Permission = "mcp"

type bridgeTool struct {
	client  *Client
	mcpName string // name on the wire (tools/call)
	strike  string // namespaced name exposed to the model
	desc    string
	schema  json.RawMessage
	server  string
}

func newBridge(client *Client, info toolInfo) tool.Tool {
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

func (t *bridgeTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if tc == nil || tc.Ask == nil {
		return tool.Result{}, fmt.Errorf("mcp: permission ask unavailable")
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
		return tool.Result{}, t.client.deadErr()
	}

	callCtx, cancel := context.WithTimeout(ctx, defaultCallTimeout)
	defer cancel()

	res, err := t.client.CallTool(callCtx, t.mcpName, args)
	if err != nil {
		return tool.Result{}, err
	}

	out := formatContent(res.Content)
	if res.IsError {
		if out == "" {
			out = "MCP tool returned an error"
		}
		return tool.Result{}, fmt.Errorf("%s", out)
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
