package engine

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/jonathanung/strike-cli/internal/permission"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/provider"
	"github.com/jonathanung/strike-cli/internal/question"
	"github.com/jonathanung/strike-cli/internal/tool"
)

func TestModelFacingToolOutput(t *testing.T) {
	cases := []struct {
		name    string
		res     tool.Result
		err     error
		wantOut string
		wantErr bool
	}{
		{
			name:    "success",
			res:     tool.Result{Output: "ok"},
			wantOut: "ok",
		},
		{
			name:    "permission denied",
			err:     &permission.DeniedError{Reason: "write is not allowed"},
			wantOut: protocol.ToolFeedbackPermissionDenied("write is not allowed"),
			wantErr: true,
		},
		{
			name:    "user rejected",
			err:     &permission.RejectedError{Message: "nope"},
			wantOut: protocol.ToolFeedbackUserRejected("nope"),
			wantErr: true,
		},
		{
			name:    "question rejected",
			err:     &question.RejectedError{Message: "dismissed"},
			wantOut: "dismissed",
			wantErr: true,
		},
		{
			name:    "generic error",
			err:     errors.New("boom"),
			wantOut: protocol.ToolFeedbackError("boom"),
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, isErr := modelFacingToolOutput(tc.res, tc.err)
			if out != tc.wantOut || isErr != tc.wantErr {
				t.Errorf("got (%q, %v), want (%q, %v)", out, isErr, tc.wantOut, tc.wantErr)
			}
		})
	}
}

func TestSettleToolFeedbackPairsEndAndHistory(t *testing.T) {
	var events []protocol.Event
	eng := &Engine{
		events: make(chan protocol.Event, 4),
	}
	// Drain into slice from a side channel by using emit via direct send.
	// settleToolFeedback uses e.emit; wire a tiny collector.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range eng.events {
			events = append(events, ev)
		}
	}()

	corr := protocol.Correlation{SessionID: "s", TurnID: "t"}
	msg := eng.settleToolFeedback(toolFeedback{
		Corr:     corr,
		CallID:   "c1",
		Output:   protocol.ToolFeedbackBlocked("hook policy"),
		IsError:  true,
		Title:    "blocked",
		Metadata: json.RawMessage(`{"k":1}`),
		EmitEnd:  true,
	})
	close(eng.events)
	<-done

	if msg.Role != provider.RoleTool || msg.ToolResult == nil {
		t.Fatalf("message = %#v, want RoleTool with result", msg)
	}
	if msg.ToolResult.CallID != "c1" || !msg.ToolResult.IsError ||
		msg.ToolResult.Output != protocol.ToolFeedbackBlocked("hook policy") {
		t.Errorf("tool result = %#v", msg.ToolResult)
	}
	if len(events) != 1 {
		t.Fatalf("events = %#v, want one ToolCallEnd", events)
	}
	end, ok := events[0].(protocol.ToolCallEnd)
	if !ok {
		t.Fatalf("event = %T, want ToolCallEnd", events[0])
	}
	if end.CallID != "c1" || end.Output != msg.ToolResult.Output || !end.IsError || end.Title != "blocked" {
		t.Errorf("ToolCallEnd = %#v", end)
	}
}

func TestSettleToolFeedbackUnstartedSkipsEnd(t *testing.T) {
	eng := &Engine{events: make(chan protocol.Event, 1)}
	msg := eng.settleToolFeedback(toolFeedback{
		CallID:  "c2",
		Output:  protocol.ToolFeedbackUnstarted(),
		IsError: true,
	})
	select {
	case ev := <-eng.events:
		t.Fatalf("unexpected event %#v", ev)
	default:
	}
	if msg.ToolResult == nil || msg.ToolResult.Output != protocol.ToolFeedbackUnstarted() {
		t.Errorf("message = %#v", msg)
	}
}
