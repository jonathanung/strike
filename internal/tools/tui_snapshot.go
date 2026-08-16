package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jonathanung/strike-cli/harness/tool"
)

type tuiSnapshotTool struct{}

// NewTUISnapshot returns the headless TUI frame capture tool.
func NewTUISnapshot() tool.Tool { return tuiSnapshotTool{} }

func (tuiSnapshotTool) Name() string { return "tui_snapshot" }

func (tuiSnapshotTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectNone, tool.IdempotencySafeRetry)
}

func (tuiSnapshotTool) Description() string {
	return `Capture the current TUI frame as bounded, redacted text.

- Returns an ANSI-stripped dump of the last painted Bubble Tea frame.
- Text works without multimodal. Optional include_image asks for an
  addressable image ref (never embeds the image payload).
- Fails when no frame is available (headless/non-TUI session).
- Output is size-bounded and redacted like other session exports.`
}

func (tuiSnapshotTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"include_image": {
				"type": "boolean",
				"description": "Request an optional addressable image ref when the host can render one. Text is always returned."
			}
		},
		"additionalProperties": false
	}`)
}

func (tuiSnapshotTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var req tool.TUISnapshotRequest
	if len(args) > 0 && string(args) != "null" && string(args) != "{}" {
		if err := json.Unmarshal(args, &req); err != nil {
			return tool.Result{}, fmt.Errorf("invalid arguments: %w", err)
		}
	}
	if err := tc.Ask(ctx, tool.AskRequest{Permission: "tui_snapshot", Patterns: []string{"*"}, Always: []string{"*"}}); err != nil {
		return tool.Result{}, err
	}
	if tc.TUISnapshot == nil {
		return tool.Result{}, tool.ErrPrecondition(tool.ErrNoTUIFrame)
	}
	res, err := tc.TUISnapshot(ctx, req)
	if err != nil {
		return tool.Result{}, err
	}
	out, err := json.Marshal(res)
	if err != nil {
		return tool.Result{}, err
	}
	title := "tui_snapshot"
	if res.Truncated {
		title += " truncated"
	}
	if res.ImageRef != "" {
		title += " +image"
	}
	return tool.Result{Title: title, Output: string(out)}, nil
}
