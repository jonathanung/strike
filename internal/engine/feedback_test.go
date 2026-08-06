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
		name     string
		res      tool.Result
		err      error
		wantOut  string
		wantErr  bool
		wantCode tool.ErrorCode
	}{
		{
			name:    "success",
			res:     tool.Result{Output: "ok"},
			wantOut: "ok",
		},
		{
			name:     "permission denied",
			err:      &permission.DeniedError{Reason: "write is not allowed"},
			wantOut:  protocol.ToolFeedbackPermissionDenied("write is not allowed"),
			wantErr:  true,
			wantCode: tool.CodePermissionDenied,
		},
		{
			name:     "user rejected",
			err:      &permission.RejectedError{Message: "nope"},
			wantOut:  protocol.ToolFeedbackUserRejected("nope"),
			wantErr:  true,
			wantCode: tool.CodePermissionDenied,
		},
		{
			name:     "question rejected",
			err:      &question.RejectedError{Message: "dismissed"},
			wantOut:  "dismissed",
			wantErr:  true,
			wantCode: tool.CodePermissionDenied,
		},
		{
			name:     "tool user rejected",
			err:      &tool.UserRejectedError{Message: "stay in plan"},
			wantOut:  protocol.ToolFeedbackUserRejected("stay in plan"),
			wantErr:  true,
			wantCode: tool.CodePermissionDenied,
		},
		{
			name:     "invalid args structured",
			err:      tool.ErrInvalidArgs("oldString and newString are identical"),
			wantOut:  protocol.ToolFeedbackError("oldString and newString are identical"),
			wantErr:  true,
			wantCode: tool.CodeInvalidArgs,
		},
		{
			name:     "generic error",
			err:      errors.New("boom"),
			wantOut:  protocol.ToolFeedbackError("boom"),
			wantErr:  true,
			wantCode: tool.CodeInternal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, isErr, fail := modelFacingToolOutput(tc.res, tc.err)
			if out != tc.wantOut || isErr != tc.wantErr {
				t.Errorf("got (%q, %v), want (%q, %v)", out, isErr, tc.wantOut, tc.wantErr)
			}
			if fail.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", fail.Code, tc.wantCode)
			}
		})
	}
}

func TestClassifyToolFailurePermissionVsInvalidArgs(t *testing.T) {
	// Tier C: orchestrators must distinguish hard deny from bad args.
	deny := classifyToolFailure(&permission.DeniedError{Reason: "a permission rule matched"})
	if deny.Code != tool.CodePermissionDenied || deny.Retryable {
		t.Fatalf("deny = %+v, want permission_denied non-retryable", deny)
	}
	if deny.Output != protocol.ToolFeedbackPermissionDenied("a permission rule matched") {
		t.Fatalf("deny output = %q", deny.Output)
	}

	bad := classifyToolFailure(tool.ErrInvalidArgs("invalid arguments: unexpected EOF"))
	if bad.Code != tool.CodeInvalidArgs || bad.Retryable {
		t.Fatalf("invalid = %+v, want invalid_args non-retryable", bad)
	}
	if bad.Code == deny.Code {
		t.Fatal("permission_denied and invalid_args must differ")
	}
}

func TestIsUserTurnInterrupt(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", want: false},
		{name: "generic", err: errors.New("boom"), want: false},
		{name: "hard deny", err: &permission.DeniedError{Reason: "no"}, want: false},
		{name: "perm reject", err: &permission.RejectedError{Message: "nope"}, want: true},
		{name: "question reject", err: &question.RejectedError{Message: "dismissed"}, want: true},
		{name: "tool reject", err: &tool.UserRejectedError{Message: "no"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isUserTurnInterrupt(tc.err); got != tc.want {
				t.Errorf("isUserTurnInterrupt(%v) = %v, want %v", tc.err, got, tc.want)
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
		Corr:      corr,
		CallID:    "c1",
		Output:    protocol.ToolFeedbackBlocked("hook policy"),
		IsError:   true,
		Title:     "blocked",
		Metadata:  json.RawMessage(`{"k":1}`),
		EmitEnd:   true,
		ErrorCode: tool.CodeBlocked,
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
	if msg.ToolResult.ErrorCode != string(tool.CodeBlocked) {
		t.Errorf("ToolResult.ErrorCode = %q, want blocked", msg.ToolResult.ErrorCode)
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
	if end.Error == nil || end.Error.Code != string(tool.CodeBlocked) || end.Error.Retryable {
		t.Errorf("ToolCallEnd.Error = %#v, want blocked non-retryable", end.Error)
	}
}

func TestSettleToolFeedbackUnstartedSkipsEnd(t *testing.T) {
	eng := &Engine{events: make(chan protocol.Event, 1)}
	msg := eng.settleToolFeedback(toolFeedback{
		CallID:    "c2",
		Output:    protocol.ToolFeedbackUnstarted(),
		IsError:   true,
		ErrorCode: tool.CodeCanceled,
	})
	select {
	case ev := <-eng.events:
		t.Fatalf("unexpected event %#v", ev)
	default:
	}
	if msg.ToolResult == nil || msg.ToolResult.Output != protocol.ToolFeedbackUnstarted() {
		t.Errorf("message = %#v", msg)
	}
	if msg.ToolResult.ErrorCode != string(tool.CodeCanceled) {
		t.Errorf("ErrorCode = %q, want canceled", msg.ToolResult.ErrorCode)
	}
}

func TestSettleToolFeedbackSuccessOmitsError(t *testing.T) {
	var events []protocol.Event
	eng := &Engine{events: make(chan protocol.Event, 2)}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for ev := range eng.events {
			events = append(events, ev)
		}
	}()
	msg := eng.settleToolFeedback(toolFeedback{
		Corr:    protocol.Correlation{SessionID: "s"},
		CallID:  "ok1",
		Output:  "done",
		Title:   "edit",
		EmitEnd: true,
	})
	close(eng.events)
	<-done
	if msg.ToolResult.IsError || msg.ToolResult.ErrorCode != "" {
		t.Fatalf("success result = %#v", msg.ToolResult)
	}
	end := events[0].(protocol.ToolCallEnd)
	if end.Error != nil || end.IsError {
		t.Fatalf("success end = %#v", end)
	}
}
