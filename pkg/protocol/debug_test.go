package protocol

import (
	"reflect"
	"testing"
)

func TestDebugWireRoundTrip(t *testing.T) {
	op := DebugControl{
		Action:     DebugActionHypotheses,
		Hypotheses: []DebugHypothesis{{ID: "h1", Statement: "a"}, {ID: "h2", Statement: "b", Confirms: "c"}},
	}
	env, err := WrapOp(op)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "debug.control" {
		t.Fatalf("op type = %q", env.Type)
	}
	gotOp, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotOp, op) {
		t.Fatalf("op round trip = %#v, want %#v", gotOp, op)
	}

	ev := DebugUpdated{
		Correlation:     Correlation{SessionID: "s"},
		Action:          DebugActionEnd,
		Phase:           "closed",
		Hypotheses:      []DebugHypothesis{{ID: "h1", Rank: 1, Statement: "a", Status: "confirmed", Evidence: []DebugEvidence{{Source: "repro", Text: "x"}}}},
		Removed:         []DebugHit{{Path: "a.go", Line: 2, Text: "probe"}},
		Instrumentation: nil,
	}
	wrapped, err := Wrap(ev)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.Type != "debug.updated" {
		t.Fatalf("event type = %q", wrapped.Type)
	}
	gotEv, err := wrapped.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotEv, ev) {
		t.Fatalf("event round trip = %#v, want %#v", gotEv, ev)
	}
}
