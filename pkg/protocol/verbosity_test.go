package protocol

import (
	"encoding/json"
	"testing"
)

func TestParseVerbosity(t *testing.T) {
	tests := []struct {
		in   string
		want Verbosity
		ok   bool
	}{
		{"", VerbosityDefault, true},
		{"default", VerbosityDefault, true},
		{" Concise ", VerbosityConcise, true},
		{"DETAILED", VerbosityDetailed, true},
		{"loud", "", false},
	}
	for _, tt := range tests {
		got, ok := ParseVerbosity(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseVerbosity(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestVerbosityWireRoundTrip(t *testing.T) {
	env, err := WrapOp(SetVerbosity{Level: VerbosityConcise})
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != "set.verbosity" {
		t.Fatalf("op type = %q, want set.verbosity", env.Type)
	}
	op, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := op.(SetVerbosity); !ok || got.Level != VerbosityConcise {
		t.Fatalf("op = %#v", op)
	}

	ev := VerbositySelected{Correlation: Correlation{SessionID: "s1"}, Level: VerbosityDetailed, Delivery: VerbosityDeliveryInstruction}
	wrapped, err := Wrap(ev)
	if err != nil {
		t.Fatal(err)
	}
	if wrapped.Type != "verbosity.selected" {
		t.Fatalf("event type = %q", wrapped.Type)
	}
	var data map[string]any
	if err := json.Unmarshal(wrapped.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data["level"] != "detailed" || data["delivery"] != "instruction" || data["sessionId"] != "s1" {
		t.Fatalf("event data = %v", data)
	}
	back, err := wrapped.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := back.(VerbositySelected); !ok || got != ev {
		t.Fatalf("round trip = %#v, want %#v", back, ev)
	}
}
