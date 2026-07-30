package tool

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestAgentRosterNilHandler(t *testing.T) {
	tc := allowAll(t.TempDir())
	_, err := NewAgentRoster().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("err = %v, want not available", err)
	}
}

func TestAgentRosterSuccess(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentRoster = func(context.Context, AgentRosterRequest) (AgentRosterResult, error) {
		return AgentRosterResult{
			LeadID: "lead-1",
			Members: []AgentRosterMember{
				{SessionID: "lead-1", Agent: "build", State: "working", Role: "lead", IsSelf: true},
				{SessionID: "child-1", Agent: "explore", State: "completed", Role: "member",
					ParentSessionID: "lead-1", Depth: 1, TerminalSummary: "done"},
			},
		}, nil
	}
	res, err := NewAgentRoster().Execute(context.Background(), mustJSON(t, map[string]any{}), tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Output, `"lead_id":"lead-1"`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Output, `"session_id":"child-1"`) || !strings.Contains(res.Output, `"state":"completed"`) {
		t.Fatalf("output = %s", res.Output)
	}
	if !strings.Contains(res.Title, "2") {
		t.Fatalf("title = %q", res.Title)
	}
	var parsed AgentRosterResult
	if err := json.Unmarshal([]byte(res.Output), &parsed); err != nil {
		t.Fatal(err)
	}
	if len(parsed.Members) != 2 || parsed.Members[0].IsSelf != true {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestAgentRosterPermissionDenied(t *testing.T) {
	tc := &Context{
		WorkDir: t.TempDir(),
		Ask: func(context.Context, AskRequest) error {
			return errors.New("denied")
		},
		AgentRoster: func(context.Context, AgentRosterRequest) (AgentRosterResult, error) {
			t.Fatal("should not run")
			return AgentRosterResult{}, nil
		},
	}
	if _, err := NewAgentRoster().Execute(context.Background(), mustJSON(t, map[string]any{}), tc); err == nil {
		t.Fatal("expected permission error")
	}
}

func TestAgentRosterInvalidJSON(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentRoster = func(context.Context, AgentRosterRequest) (AgentRosterResult, error) {
		return AgentRosterResult{}, nil
	}
	if _, err := NewAgentRoster().Execute(context.Background(), json.RawMessage(`{`), tc); err == nil {
		t.Fatal("expected invalid json error")
	}
}

func TestAgentRosterEmptyArgsOK(t *testing.T) {
	tc := allowAll(t.TempDir())
	tc.AgentRoster = func(context.Context, AgentRosterRequest) (AgentRosterResult, error) {
		return AgentRosterResult{
			LeadID:  "L",
			Members: []AgentRosterMember{{SessionID: "L", Role: "lead", State: "working", IsSelf: true}},
		}, nil
	}
	for _, args := range []json.RawMessage{nil, json.RawMessage(`null`), json.RawMessage(`{}`)} {
		if _, err := NewAgentRoster().Execute(context.Background(), args, tc); err != nil {
			t.Fatalf("args %s: %v", args, err)
		}
	}
}
