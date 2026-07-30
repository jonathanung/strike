package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAgentMessageNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"to": "s1", "body": "hi",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want not available", err)
	}
}

func TestAgentMessageAccepted(t *testing.T) {
	tc := allowAll(t.TempDir())
	var got AgentMessageRequest
	tc.AgentMessage = func(_ context.Context, req AgentMessageRequest) (AgentMessageResult, error) {
		got = req
		return AgentMessageResult{
			To: req.To, Status: "accepted", Detail: "enqueued", MessageID: "m1",
		}, nil
	}
	res, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"to": "child-1", "body": "hello peer", "summary": "hi",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if got.To != "child-1" || got.Body != "hello peer" || got.Summary != "hi" {
		t.Fatalf("req = %+v", got)
	}
	if !strings.Contains(res.Output, `"status":"accepted"`) || !strings.Contains(res.Output, `"message_id":"m1"`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Title, "accepted") {
		t.Fatalf("title = %q", res.Title)
	}
}

func TestAgentMessageRejectedSurfacesError(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentMessage = func(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
		return AgentMessageResult{
			To: "x", Status: "rejected", Detail: "recipient is not on this team",
		}, nil
	}
	_, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"to": "x", "body": "nope",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not on this team") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentMessagePermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(context.Context, AskRequest) error {
			return errors.New("denied")
		},
		AgentMessage: func(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
			t.Fatal("should not run")
			return AgentMessageResult{}, nil
		},
	}
	if _, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"to": "s", "body": "b",
	}), tc); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestAgentMessageValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentMessage = func(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
		t.Fatal("should not run")
		return AgentMessageResult{}, nil
	}
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"missing to", map[string]any{"body": "x"}, "to is required"},
		{"missing body", map[string]any{"to": "s"}, "body is required"},
		{"empty body", map[string]any{"to": "s", "body": "  "}, "body is required"},
	}
	for _, tcase := range cases {
		_, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, tcase.args), tc)
		if err == nil || !strings.Contains(err.Error(), tcase.want) {
			t.Fatalf("%s: err = %v, want %q", tcase.name, err, tcase.want)
		}
	}
	if _, err := NewAgentMessage().Execute(context.Background(), json.RawMessage(`{`), tc); err == nil {
		t.Fatal("expected invalid json")
	}
}

func TestAgentMessageBodyRuneCap(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentMessage = func(context.Context, AgentMessageRequest) (AgentMessageResult, error) {
		t.Fatal("should not run")
		return AgentMessageResult{}, nil
	}
	body := strings.Repeat("x", MaxAgentMessageBodyRunes+1)
	if utf8.RuneCountInString(body) <= MaxAgentMessageBodyRunes {
		t.Fatal("test setup")
	}
	_, err := NewAgentMessage().Execute(context.Background(), mustJSON(t, map[string]any{
		"to": "s", "body": body,
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentBroadcastNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewAgentBroadcast().Execute(context.Background(), mustJSON(t, map[string]any{
		"body": "hi all",
	}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentBroadcastPartialSuccess(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentBroadcast = func(_ context.Context, req AgentBroadcastRequest) (AgentBroadcastResult, error) {
		if req.Body != "ping" || req.Summary != "s" {
			t.Fatalf("req = %+v", req)
		}
		return AgentBroadcastResult{
			Delivered: 1,
			Rejected:  1,
			Results: []AgentBroadcastDelivery{
				{To: "a", Status: "accepted", MessageID: "1"},
				{To: "b", Status: "rejected", Detail: "closed"},
			},
		}, nil
	}
	res, err := NewAgentBroadcast().Execute(context.Background(), mustJSON(t, map[string]any{
		"body": "ping", "summary": "s",
	}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"delivered":1`) || !strings.Contains(res.Title, "1/2") {
		t.Fatalf("res = %+v", res)
	}
}

func TestAgentBroadcastAllRejectedErrors(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentBroadcast = func(context.Context, AgentBroadcastRequest) (AgentBroadcastResult, error) {
		return AgentBroadcastResult{
			Rejected: 2,
			Results: []AgentBroadcastDelivery{
				{To: "a", Status: "rejected", Detail: "closed"},
				{To: "b", Status: "rejected", Detail: "closed"},
			},
		}, nil
	}
	_, err := NewAgentBroadcast().Execute(context.Background(), mustJSON(t, map[string]any{"body": "x"}), tc)
	if err == nil || !strings.Contains(err.Error(), "0 teammates") {
		t.Fatalf("err = %v", err)
	}
}

func TestAgentBroadcastPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask:     func(context.Context, AskRequest) error { return errors.New("denied") },
		AgentBroadcast: func(context.Context, AgentBroadcastRequest) (AgentBroadcastResult, error) {
			t.Fatal("should not run")
			return AgentBroadcastResult{}, nil
		},
	}
	if _, err := NewAgentBroadcast().Execute(context.Background(), mustJSON(t, map[string]any{"body": "x"}), tc); err == nil {
		t.Fatal("expected deny")
	}
}

func TestAgentBroadcastValidation(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentBroadcast = func(context.Context, AgentBroadcastRequest) (AgentBroadcastResult, error) {
		t.Fatal("should not run")
		return AgentBroadcastResult{}, nil
	}
	_, err := NewAgentBroadcast().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "body is required") {
		t.Fatalf("err = %v", err)
	}
}
