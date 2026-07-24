package protocol

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWrapDecodeRoundTrip(t *testing.T) {
	corr := Correlation{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"}
	events := []Event{
		UserMessage{Correlation: corr, Text: "hi"},
		TurnStarted{Correlation: corr},
		TextDelta{Correlation: corr, Text: "chunk"},
		ToolCallBegin{Correlation: corr, CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo"}`)},
		ToolCallEnd{Correlation: corr, CallID: "c1", Title: "echo", Output: "ok", IsError: false, Metadata: json.RawMessage(`{"exitCode":0}`)},
		PermissionAsked{Correlation: corr, RequestID: "p1", Permission: "bash", Patterns: []string{"echo hi"}},
		PermissionResolved{Correlation: corr, RequestID: "p1", Decision: DecisionOnce},
		TurnCompleted{Correlation: corr, StopReason: "end_turn"},
		ModelSelected{Correlation: corr, Provider: "echo", Model: "echo"},
		AgentSelected{Correlation: corr, Name: "build"},
		EffortSelected{Correlation: corr, Level: EffortXHigh},
		FastSelected{Correlation: corr, Enabled: true},
		EngineError{Correlation: corr, Message: "boom"},
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

func TestFastSelectedJSONIsFlatAndOptional(t *testing.T) {
	b, err := json.Marshal(FastSelected{
		Correlation: Correlation{SessionID: "session-1", TurnID: "turn-1", ProviderRequestID: "provider-1"},
		Enabled:     true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"sessionId":         `"session-1"`,
		"turnId":            `"turn-1"`,
		"providerRequestId": `"provider-1"`,
		"enabled":           `true`,
	} {
		if string(got[key]) != want {
			t.Errorf("%s = %s, want %s; JSON: %s", key, got[key], want, b)
		}
	}
	if _, ok := got["correlation"]; ok {
		t.Errorf("correlation must not be nested: %s", b)
	}
}

func TestCorrelationJSONIsFlatAndOptional(t *testing.T) {
	ev := PermissionAsked{
		Correlation: Correlation{
			SessionID:         "session-1",
			TurnID:            "turn-1",
			ProviderRequestID: "provider-1",
		},
		RequestID:  "permission-1",
		Permission: "bash",
		Patterns:   []string{"echo hi"},
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"sessionId":         `"session-1"`,
		"turnId":            `"turn-1"`,
		"providerRequestId": `"provider-1"`,
		"requestId":         `"permission-1"`,
	}
	for key, value := range want {
		if string(got[key]) != value {
			t.Errorf("%s = %s, want %s; JSON: %s", key, got[key], value, b)
		}
	}
	if _, ok := got["correlation"]; ok {
		t.Errorf("correlation must not be nested: %s", b)
	}

	empty, err := json.Marshal(TurnStarted{})
	if err != nil {
		t.Fatal(err)
	}
	if string(empty) != `{}` {
		t.Errorf("empty correlation JSON = %s, want {}", empty)
	}
}

func TestDecodeLiteralLegacyEnvelopeHasEmptyCorrelation(t *testing.T) {
	literal := `{"type":"permission.asked","time":"2020-01-01T00:00:00Z","data":{"requestId":"perm_7","permission":"bash","patterns":["echo hi"]}}`
	var env Envelope
	if err := json.Unmarshal([]byte(literal), &env); err != nil {
		t.Fatal(err)
	}
	decoded, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(PermissionAsked)
	if !ok {
		t.Fatalf("decoded event = %T, want PermissionAsked", decoded)
	}
	if got.Correlation != (Correlation{}) {
		t.Errorf("legacy correlation = %#v, want empty", got.Correlation)
	}
	if got.RequestID != "perm_7" {
		t.Errorf("requestId = %q, want perm_7", got.RequestID)
	}
}

func TestDecodeLiteralLegacyFastEnvelopeHasEmptyCorrelation(t *testing.T) {
	literal := `{"type":"fast.selected","time":"2020-01-01T00:00:00Z","data":{"enabled":true}}`
	var env Envelope
	if err := json.Unmarshal([]byte(literal), &env); err != nil {
		t.Fatal(err)
	}
	decoded, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decoded.(FastSelected)
	if !ok {
		t.Fatalf("decoded event = %T, want FastSelected", decoded)
	}
	if got.Correlation != (Correlation{}) {
		t.Errorf("legacy correlation = %#v, want empty", got.Correlation)
	}
	if !got.Enabled {
		t.Error("enabled = false, want true")
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
		"user.message":        UserMessage{},
		"turn.started":        TurnStarted{},
		"text.delta":          TextDelta{},
		"tool.begin":          ToolCallBegin{},
		"tool.end":            ToolCallEnd{},
		"permission.asked":    PermissionAsked{},
		"permission.resolved": PermissionResolved{},
		"turn.completed":      TurnCompleted{},
		"model.selected":      ModelSelected{},
		"agent.selected":      AgentSelected{},
		"effort.selected":     EffortSelected{},
		"fast.selected":       FastSelected{},
		"engine.error":        EngineError{},
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
