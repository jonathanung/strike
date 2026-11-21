package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jonathanung/strike/harness/tool"
)

type steerAckTool struct{}

// NewSteerAck returns the steer_ack tool: the agent's acknowledgement of a
// mid-turn user steer (turn.steer_ack, #1860).
func NewSteerAck() tool.Tool { return steerAckTool{} }

func (steerAckTool) Name() string { return "steer_ack" }

func (steerAckTool) Contract() tool.Contract {
	return tool.StaticContract(tool.SideEffectNone, tool.IdempotencyConditional)
}

func (steerAckTool) Description() string {
	return `Acknowledge a steer: a user message that arrived while you were already working and changes what you are doing.

Call it once per steer, right after the steer appears, before continuing. summary is one line saying what you are doing now ("Now: using the v2 API instead of v1"). When the steer contradicts your in-flight plan or earlier instructions, set conflict to true and give a one-line reason; then keep going with your best reading of the user's intent. The user decides whether to stop or queue; this call never waits for them. Do not call it for ordinary messages that start a turn.`
}

func (steerAckTool) Schema() json.RawMessage {
	return json.RawMessage(`{
		"type": "object",
		"properties": {
			"summary": {"type": "string", "description": "One line: what you are doing now because of the steer."},
			"conflict": {"type": "boolean", "description": "True when the steer contradicts the in-flight plan."},
			"reason": {"type": "string", "description": "One line: why it conflicts. Required when conflict is true."}
		},
		"required": ["summary"],
		"additionalProperties": false
	}`)
}

type steerAckArgs struct {
	Summary  string `json:"summary"`
	Conflict bool   `json:"conflict"`
	Reason   string `json:"reason"`
}

func (steerAckTool) Execute(ctx context.Context, args json.RawMessage, tc *tool.Context) (tool.Result, error) {
	var a steerAckArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return tool.Result{}, fmt.Errorf("steer_ack: invalid arguments: %w", err)
		}
	}
	if err := tc.Ask(ctx, tool.AskRequest{
		Permission: "steer_ack",
		Patterns:   []string{"*"},
		Always:     []string{"*"},
	}); err != nil {
		return tool.Result{}, err
	}
	if tc.AckSteer == nil {
		return tool.Result{}, fmt.Errorf("steer_ack: not available in this session")
	}
	if err := tc.AckSteer(tool.SteerAck{Summary: a.Summary, Conflict: a.Conflict, Reason: a.Reason}); err != nil {
		return tool.Result{}, err
	}
	out := "Steer acknowledged. Continue with the new direction."
	if a.Conflict {
		out = "Steer acknowledged with a conflict. The user sees it and can stop or queue; continue with your best reading of their intent."
	}
	return tool.Result{Title: "steer acknowledged", Output: out}, nil
}
