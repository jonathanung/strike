package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/tool"
)

type contextBundleTool struct{}

// NewContextBundle builds the context_bundle tool (read sealed spawn context).
func NewContextBundle() tool.Tool { return contextBundleTool{} }

func (contextBundleTool) Name() string { return "context_bundle" }

func (contextBundleTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectRead, tool.IdempotencySafeRetry)
}

func (contextBundleTool) Description() string {
	return `Read the sealed context bundle attached when this agent was spawned.

Returns goal, acceptance criteria, allowed/required paths, artifact refs,
constraints, addressable items (for provenance citations), and optional file
pins. Use this instead of guessing lead intent. When you cannot proceed for
lack of context, finish with a structured handoff that includes missing_context
and status will be blocked for the lead to resupply.

Actions:
  - get (default): full bundle JSON
  - item: one item by id
  - list_items: id/kind/title index only`
}

func (contextBundleTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {
				"type": "string",
				"enum": ["get", "item", "list_items"],
				"description": "Read operation (default get)"
			},
			"id": {"type": "string", "description": "Bundle item id (action=item)"}
		}
	}`)
}

func (contextBundleTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	if err := ctx.Err(); err != nil {
		return tool.Result{}, err
	}
	var a struct {
		Action string `json:"action"`
		ID     string `json:"id"`
	}
	if len(args) > 0 && string(args) != "null" {
		if err := json.Unmarshal(args, &a); err != nil {
			return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	action := strings.ToLower(strings.TrimSpace(a.Action))
	if action == "" {
		action = "get"
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "context_bundle",
		Patterns:   []string{action},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if tc == nil || tc.ContextBundle == nil || tc.ContextBundle.Empty() {
		return tool.Result{
			Title:  "context_bundle empty",
			Output: `{"attached":false,"message":"no context bundle was attached at spawn"}`,
		}, nil
	}
	b := tc.ContextBundle.Clone()
	switch action {
	case "get":
		out, err := json.MarshalIndent(bundleToolView(b), "", "  ")
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Title: "context_bundle", Output: string(out)}, nil
	case "list_items":
		type row struct {
			ID    string `json:"id"`
			Kind  string `json:"kind,omitempty"`
			Title string `json:"title,omitempty"`
			Path  string `json:"path,omitempty"`
		}
		rows := make([]row, 0, len(b.Items))
		for _, it := range b.Items {
			rows = append(rows, row{ID: it.ID, Kind: it.Kind, Title: it.Title, Path: it.Path})
		}
		out, err := json.MarshalIndent(map[string]any{"items": rows}, "", "  ")
		if err != nil {
			return tool.Result{}, err
		}
		return tool.Result{Title: "context_bundle items", Output: string(out)}, nil
	case "item":
		id := strings.TrimSpace(a.ID)
		if id == "" {
			return tool.Result{}, fmt.Errorf("id is required for action=item")
		}
		for _, it := range b.Items {
			if it.ID == id {
				out, err := json.MarshalIndent(it, "", "  ")
				if err != nil {
					return tool.Result{}, err
				}
				return tool.Result{Title: "context_bundle item " + id, Output: string(out)}, nil
			}
		}
		return tool.Result{}, fmt.Errorf("bundle item %q not found", id)
	default:
		return tool.Result{}, fmt.Errorf("action must be get, item, or list_items")
	}
}

func bundleToolView(b tool.ContextBundle) map[string]any {
	return map[string]any{
		"attached":       true,
		"goal":           b.Goal,
		"acceptance":     b.Acceptance,
		"allowed_paths":  b.AllowedPaths,
		"required_paths": b.RequiredPaths,
		"artifacts":      b.Artifacts,
		"constraints":    b.Constraints,
		"items":          b.Items,
		"file_pins":      b.FilePins,
	}
}
