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
	base := `Search available tools by name or description substring.

Use when you need to discover which tools are registered and what they do. Query tokens are matched case-insensitively against each tool's name and description; all whitespace-separated tokens must match. Wrap multi-word phrases in double quotes to search for a literal substring (e.g. "read file").`
	if t.reg != nil && t.reg.DeferLoading() {
		return base + `

When deferred tool schemas are enabled, matching non-core tools are loaded into the tools array for subsequent model requests so you can call them.`
	}
	return base
}

func (t *toolSearchTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"query": {"type": "string", "description": "Case-insensitive substring query. Whitespace-separated tokens all must match. Wrap multi-word phrases in double quotes to search for a literal substring (e.g. \"read file\")."}
		},
		"required": ["query"]
	}`)
}

type toolSearchArgs struct {
	Query string `json:"query"`
}

// splitQuery splits a tool search query into tokens. Quoted segments ("...")
// are treated as single literal tokens with spaces preserved. Unquoted segments
// are split on whitespace. An unmatched opening quote treats the rest of the
// string as one token.
func splitQuery(query string) []string {
	var tokens []string
	i := 0
	n := len(query)
	for i < n {
		// Skip whitespace.
		if query[i] == ' ' || query[i] == '\t' {
			i++
			continue
		}
		if query[i] == '"' {
			// Quoted phrase — find closing quote.
			i++ // skip opening quote
			start := i
			for i < n && query[i] != '"' {
				i++
			}
			tok := strings.TrimSpace(query[start:i])
			if tok != "" {
				tokens = append(tokens, strings.ToLower(tok))
			}
			if i < n {
				i++ // skip closing quote
			}
		} else {
			// Unquoted word — scan until whitespace or quote.
			start := i
			for i < n && query[i] != ' ' && query[i] != '\t' && query[i] != '"' {
				i++
			}
			tokens = append(tokens, strings.ToLower(query[start:i]))
		}
	}
	return tokens
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

	tokens := splitQuery(query)
	// Search the full registry (including deferred tools not yet in Tools[]).
	schemas := t.reg.Schemas()
	var matches []string
	var matchNames []string
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
		matchNames = append(matchNames, s.Name)
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

	// Promote matches into provider Tools for subsequent streams (same turn
	// tool loop and later turns). No-op when defer loading is off.
	if len(matchNames) > 0 {
		t.reg.Discover(matchNames...)
	}

	var out string
	if len(matches) == 0 {
		out = fmt.Sprintf("No tools matched %q", query)
	} else {
		out = strings.Join(matches, "\n")
		if t.reg.DeferLoading() {
			out += "\n\nDiscovered tools are available with full schemas on the next model request."
		}
	}
	meta, _ := json.Marshal(map[string]any{
		"query":      query,
		"count":      len(matches),
		"discovered": matchNames,
	})
	return Result{
		Title:    fmt.Sprintf("%d matches for %q", len(matches), query),
		Output:   out,
		Metadata: meta,
	}, nil
}
