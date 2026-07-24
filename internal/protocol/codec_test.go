package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWrapDecodeRoundTrip(t *testing.T) {
	events := []Event{
		UserMessage{Text: "hi"},
		TurnStarted{},
		TextDelta{Text: "chunk"},
		ToolCallBegin{CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo"}`)},
		ToolCallEnd{CallID: "c1", Title: "echo", Output: "ok", IsError: false, Metadata: json.RawMessage(`{"exitCode":0}`)},
		PermissionAsked{RequestID: "p1", Permission: "bash", Patterns: []string{"echo hi"}},
		PermissionResolved{RequestID: "p1", Decision: DecisionOnce},
		TurnCompleted{StopReason: "end_turn"},
		ModelSelected{Provider: "echo", Model: "echo"},
		AgentSelected{Name: "build"},
		EngineError{Message: "boom"},
	}
	for _, want := range events {
		env, err := Wrap(want)
		if err != nil {
			t.Fatalf("Wrap(%T): %v", want, err)
		}
		if env.Type == "" {
			t.Fatalf("empty type for %T", want)
		}
		if env.Time.IsZero() || env.Time.Location() != time.UTC {
			t.Errorf("time = %v, want non-zero UTC", env.Time)
		}
		got, err := env.Decode()
		if err != nil {
			t.Fatalf("Decode %s: %v", env.Type, err)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Errorf("%s: got %s, want %s", env.Type, gotJSON, wantJSON)
		}
	}
}

func TestWrapUnknownEvent(t *testing.T) {
	type unknown struct{}
	// unknown does not implement Event; use a typed nil Event via empty interface cast workaround
	// by wrapping a value that implements isEvent through a private type is not possible from tests.
	// Instead verify Decode rejects unknown envelope types.
	env := Envelope{Type: "not.a.real.type", Data: json.RawMessage(`{}`)}
	if _, err := env.Decode(); err == nil {
		t.Fatal("expected error for unknown envelope type")
	}
}

func TestDecodeMalformedData(t *testing.T) {
	env := Envelope{Type: "user.message", Data: json.RawMessage(`{`)}
	if _, err := env.Decode(); err == nil {
		t.Fatal("expected error for malformed data")
	}
}

func TestEventTypeCoverage(t *testing.T) {
	// Ensure every known event maps to a stable type string used by sessions.
	want := map[string]Event{
		"user.message":         UserMessage{},
		"turn.started":         TurnStarted{},
		"text.delta":           TextDelta{},
		"tool.begin":           ToolCallBegin{},
		"tool.end":             ToolCallEnd{},
		"permission.asked":     PermissionAsked{},
		"permission.resolved":  PermissionResolved{},
		"turn.completed":       TurnCompleted{},
		"model.selected":       ModelSelected{},
		"agent.selected":       AgentSelected{},
		"engine.error":         EngineError{},
	}
	for typ, ev := range want {
		env, err := Wrap(ev)
		if err != nil {
			t.Fatalf("Wrap %s: %v", typ, err)
		}
		if env.Type != typ {
			t.Errorf("type = %q, want %q", env.Type, typ)
		}
	}
}
