package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jonathanung/strike/harness/tool"
)

type debugSessionTool struct{}

// NewDebugSession returns the debug_session tool: the agent's side of a
// hypothesis-first debug session (#1345, #1961). It sets ranked hypotheses,
// attaches evidence and marks verdicts. Unlocking fix, overriding the gate,
// marking the bug fixed and ending the session stay user-only.
func NewDebugSession() tool.Tool { return debugSessionTool{} }

func (debugSessionTool) Name() string { return "debug_session" }

func (debugSessionTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectNone, tool.IdempotencyConditional)
}

func (debugSessionTool) Description() string {
	return `Record your hypotheses and evidence in the user's active debug session (shown as cards in their Debug panel).

Actions:
- "hypotheses": set 2-5 ranked hypotheses, most likely first. Each has a unique id, a statement, and what evidence would confirm or refute it. Only in the hypothesis phase; replaces the list and resets verdicts.
- "evidence": attach captured output (text, with a source such as "repro", "log" or "command") to hypothesis_id.
- "mark": set hypothesis_id's status to confirmed, refuted or open. Confirming requires attached evidence.

You cannot start the session, unlock fix edits, override the gate, mark the bug fixed or end the session; those are the user's. Follow the debug system note for the instrumentation marker.`
}

func (debugSessionTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"action": {"type": "string", "enum": ["hypotheses", "evidence", "mark"]},
			"hypotheses": {
				"type": "array",
				"minItems": 2,
				"maxItems": 5,
				"description": "Ranked list for action hypotheses, most likely first.",
				"items": {
					"type": "object",
					"properties": {
						"id": {"type": "string", "description": "Short unique id, e.g. h1."},
						"statement": {"type": "string", "description": "The candidate cause."},
						"confirms": {"type": "string", "description": "Evidence that would confirm it."},
						"refutes": {"type": "string", "description": "Evidence that would refute it."}
					},
					"required": ["id", "statement"],
					"additionalProperties": false
				}
			},
			"hypothesis_id": {"type": "string", "description": "Target hypothesis for evidence and mark."},
			"status": {"type": "string", "enum": ["confirmed", "refuted", "open"], "description": "Verdict for action mark."},
			"source": {"type": "string", "description": "Evidence source, e.g. repro, log, command."},
			"text": {"type": "string", "description": "Captured output for action evidence."}
		},
		"required": ["action"],
		"additionalProperties": false
	}`)
}

type debugSessionArgs struct {
	Action     string `json:"action"`
	Hypotheses []struct {
		ID        string `json:"id"`
		Statement string `json:"statement"`
		Confirms  string `json:"confirms"`
		Refutes   string `json:"refutes"`
	} `json:"hypotheses"`
	HypothesisID string `json:"hypothesis_id"`
	Status       string `json:"status"`
	Source       string `json:"source"`
	Text         string `json:"text"`
}

func (debugSessionTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a debugSessionArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("debug_session: invalid arguments: %w", err)
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "debug_session",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if tc.DebugSession == nil {
		return tool.Result{}, fmt.Errorf("debug_session: not available in this session")
	}
	op := tool.DebugOp{
		Action:       a.Action,
		HypothesisID: a.HypothesisID,
		Status:       a.Status,
		Source:       a.Source,
		Text:         a.Text,
	}
	for _, h := range a.Hypotheses {
		op.Hypotheses = append(op.Hypotheses, tool.DebugHypothesis{
			ID: h.ID, Statement: h.Statement, Confirms: h.Confirms, Refutes: h.Refutes,
		})
	}
	summary, err := tc.DebugSession(op)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Title: "debug " + a.Action, Output: summary}, nil
}
