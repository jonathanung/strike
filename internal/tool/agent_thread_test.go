package tool

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestAgentThreadNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewAgentThread().Execute(context.Background(), mustJSON(t, map[string]any{
		"task_id": "t1",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentThreadOK(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got AgentThreadRequest
	tc.AgentThread = func(_ context.Context, req AgentThreadRequest) (AgentThreadResult, error) {
		got = req
		return AgentThreadResult{
			TaskID: req.TaskID,
			Messages: []AgentThreadMessage{
				{MessageID: "m1", From: "a", To: "b", Body: "hi", TaskID: req.TaskID, Urgency: "high"},
			},
		}, nil
	}
	res, err := NewAgentThread().Execute(context.Background(), mustJSON(t, map[string]any{
		"task_id": "d1", "limit": 5,
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "d1" || got.Limit != 5 {
		t.Fatalf("req = %+v", got)
	}
	if !strings.Contains(res.Output, `"task_id":"d1"`) || !strings.Contains(res.Output, "hi") {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Title, "agent_thread") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestAgentThreadPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
		AgentThread: func(context.Context, AgentThreadRequest) (AgentThreadResult, error) {
			t.Fatal("should not run")
			return AgentThreadResult{}, nil
		},
	}
	if _, err := NewAgentThread().Execute(context.Background(), mustJSON(t, map[string]any{"task_id": "t"}), tc); err == nil {
		t.Fatal("expected deny")
	}
}

func TestAgentThreadValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentThread = func(context.Context, AgentThreadRequest) (AgentThreadResult, error) {
		t.Fatal("should not run")
		return AgentThreadResult{}, nil
	}
	_, err := NewAgentThread().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "task_id is required") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentMessageContractArgs(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got AgentMessageRequest
	tc.AgentMessage = func(_ context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
		got = req
		return AgentMessageResult{
			To: req.To, Status: "accepted", MessageID: "m1",
			TaskID: req.TaskID, Urgency: req.Urgency, Kind: req.Kind,
			RequireAck: true, AckStatus: "pending", AckTimeoutSeconds: 60,
		}, nil
	}
	res, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"to": "child", "body": "please", "task_id": "d1",
		"kind": "request", "urgency": "blocker", "ack_timeout_seconds": 90,
		"escalate_to": "lead",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.TaskID != "d1" || got.Kind != "request" || got.Urgency != "blocker" {
		t.Fatalf("got = %+v", got)
	}
	if got.AckTimeoutSeconds != 90 || got.EscalateTo != "lead" {
		t.Fatalf("timeout/escalate = %+v", got)
	}
	if !strings.Contains(res.Output, `"require_ack":true`) || !strings.Contains(res.Title, "blocker") {
		t.Fatalf("res = %+v", res)
	}
}

func TestAgentMessageAckArgs(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got AgentMessageRequest
	tc.AgentMessage = func(_ context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
		got = req
		return AgentMessageResult{To: "lead", Status: "accepted", Kind: "ack", InReplyTo: req.InReplyTo, AckStatus: "acked"}, nil
	}
	_, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"kind": "ack", "in_reply_to": "msg-9", "body": "ok",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "ack" || got.InReplyTo != "msg-9" || got.Body != "ok" {
		t.Fatalf("got = %+v", got)
	}
	// Missing in_reply_to
	tc.AgentMessage = func(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
		t.Fatal("should not run")
		return AgentMessageResult{}, nil
	}
	_, err = NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{"kind": "ack"}), tc)
	if err == nil || !strings.Contains(err.Error(), "in_reply_to") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentMessageDescriptionContracts(t *testing.T) {
	d := NewAgentMessage().Description()
	for _, needle := range []string{
		"require_ack", "task_id", "urgency", "kind", "agent_thread", "escalate",
	} {
		if !strings.Contains(d, needle) {
			t.Errorf("description missing %q", needle)
		}
	}
	td := NewAgentThread().Description()
	if !strings.Contains(td, "task_id") || !strings.Contains(td, "chatty") {
		t.Errorf("agent_thread description weak: %s", td)
	}
}
