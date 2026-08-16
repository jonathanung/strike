package mcp

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/pkg/redact"
)

// BoundText redacts secrets and truncates to MaxContentBytes.
func BoundText(s string) string {
	s = redact.String(s)
	if len(s) <= MaxContentBytes {
		return s
	}
	// Cut on rune boundary.
	cut := MaxContentBytes
	for cut > 0 && !utf8.ValidString(s[:cut]) {
		cut--
	}
	return s[:cut] + "…[truncated]"
}

// FormatPromptResult builds model-facing text from a prompts/get result.
func FormatPromptResult(server, name string, res getPromptResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp:%s prompt:%s]", server, name)
	if d := strings.TrimSpace(res.Description); d != "" {
		fmt.Fprintf(&b, " %s", BoundText(d))
	}
	b.WriteByte('\n')
	for i, m := range res.Messages {
		if i >= MaxContentBlocks {
			fmt.Fprintf(&b, "… (%d more messages truncated)\n", len(res.Messages)-i)
			break
		}
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		text := promptContentText(m.Content)
		fmt.Fprintf(&b, "%s: %s\n", role, BoundText(text))
	}
	return strings.TrimRight(b.String(), "\n")
}

func promptContentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// content may be string, object, or array of blocks.
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var block contentBlock
	if err := json.Unmarshal(raw, &block); err == nil && (block.Text != "" || block.Type != "") {
		if block.Text != "" {
			return block.Text
		}
		return fmt.Sprintf("[%s content]", block.Type)
	}
	var blocks []contentBlock
	if err := json.Unmarshal(raw, &blocks); err == nil {
		return formatContent(blocks)
	}
	return BoundText(string(raw))
}

// FormatResourceResult builds model-facing text from resources/read.
func FormatResourceResult(server string, res readResourceResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[mcp:%s resource]", server)
	b.WriteByte('\n')
	for i, c := range res.Contents {
		if i >= MaxContentBlocks {
			fmt.Fprintf(&b, "… (%d more contents truncated)\n", len(res.Contents)-i)
			break
		}
		uri := c.URI
		if uri == "" {
			uri = "(no uri)"
		}
		fmt.Fprintf(&b, "uri=%s", BoundText(uri))
		if c.MimeType != "" {
			fmt.Fprintf(&b, " mime=%s", c.MimeType)
		}
		b.WriteByte('\n')
		if c.Text != "" {
			b.WriteString(BoundText(c.Text))
			b.WriteByte('\n')
		} else if c.Blob != "" {
			fmt.Fprintf(&b, "[binary blob omitted len=%d]\n", len(c.Blob))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// ProvenanceMeta returns JSON metadata for tool results.
func ProvenanceMeta(server, kind, name string) json.RawMessage {
	m := map[string]any{
		"mcpServer": server,
		"mcpKind":   kind,
	}
	if name != "" {
		m["mcpName"] = name
	}
	raw, _ := json.Marshal(m)
	return raw
}
