package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const toolSearchDescMax = 120

type toolSearchTool struct {
	reg *Registry
}

// NewToolSearch returns a tool that searches the registry's tool schemas by
// name and description. Schemas() is called only during Execute.
func NewToolSearch(reg *Registry) Tool {
	return &toolSearchTool{reg: reg}
}

func (t *toolSearchTool) Name() string { return "toolsearch" }

func (t *toolSearchTool) Description() string {
	return `Search available tools by name or description substring.

Use when you need to discover which tools are registered and what they do. Query tokens are matched case-insensitively against each tool's name and description; all whitespace-separated tokens must match.`
}

func (t *toolSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Case-insensitive substring query (whitespace-separated tokens all must match)"}
		},
		"required": ["query"]
	}`)
}

type toolSearchArgs struct {
	Query string `json:"query"`
}

func (t *toolSearchTool) Execute(ctx context.Context, args json.RawMessage, tc *Context) (Result, error) {
	var a toolSearchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, fmt.Errorf("invalid arguments: %w", err)
	}
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return Result{}, fmt.Errorf("query is required")
	}
	if t.reg == nil {
		return Result{}, fmt.Errorf("tool registry is not configured")
	}
	if err := tc.Ask(ctx, AskRequest{
		Permission: "toolsearch",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return Result{}, err
	}

	tokens := strings.Fields(strings.ToLower(query))
	schemas := t.reg.Schemas()
	var matches []string
	for _, s := range schemas {
		hay := strings.ToLower(s.Name + " " + s.Description)
		ok := true
		for _, tok := range tokens {
			if !strings.Contains(hay, tok) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		desc := s.Description
		if utf8.RuneCountInString(desc) > toolSearchDescMax {
			var b strings.Builder
			n := 0
			for _, r := range desc {
				if n >= toolSearchDescMax {
					break
				}
				b.WriteRune(r)
				n++
			}
			desc = b.String() + "…"
		}
		// Collapse description to a single line for the listing.
		desc = strings.Join(strings.Fields(desc), " ")
		matches = append(matches, fmt.Sprintf("- %s: %s", s.Name, desc))
	}

	var out string
	if len(matches) == 0 {
		out = fmt.Sprintf("No tools matched %q", query)
	} else {
		out = strings.Join(matches, "\n")
	}
	meta, _ := json.Marshal(map[string]any{"query": query, "count": len(matches)})
	return Result{
		Title:    fmt.Sprintf("%d matches for %q", len(matches), query),
		Output:   out,
		Metadata: meta,
	}, nil
}
