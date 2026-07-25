package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestParseAutonomyAcceptsEveryModeCaseInsensitively(t *testing.T) {
	for _, mode := range protocol.Autonomies() {
		for _, spelling := range []string{string(mode), " " + string(mode) + " ", strings.ToUpper(string(mode))} {
			got, ok := protocol.ParseAutonomy(spelling)
			if !ok {
				t.Errorf("ParseAutonomy(%q) rejected", spelling)
				continue
			}
			if got != mode {
				t.Errorf("ParseAutonomy(%q) = %q, want %q", spelling, got, mode)
			}
		}
	}
}

func TestParseAutonomyEmptyIsSupervisedAndUnknownIsRejected(t *testing.T) {
	if got, ok := protocol.ParseAutonomy("  "); !ok || got != protocol.AutonomySupervised {
		t.Errorf("ParseAutonomy(blank) = (%q, %v), want supervised and accepted", got, ok)
	}
	for _, bad := range []string{"auto", "yolo", "user", "check"} {
		if _, ok := protocol.ParseAutonomy(bad); ok {
			t.Errorf("ParseAutonomy(%q) accepted, want rejected", bad)
		}
	}
}

func TestAutonomiesAreOrderedAndDescribed(t *testing.T) {
	modes := protocol.Autonomies()
	want := []protocol.Autonomy{
		protocol.AutonomySupervised, protocol.AutonomyAgent, protocol.AutonomyChecks,
	}
	if len(modes) != len(want) {
		t.Fatalf("Autonomies() = %v, want %v", modes, want)
	}
	for i, mode := range want {
		if modes[i] != mode {
			t.Errorf("Autonomies()[%d] = %q, want %q", i, modes[i], mode)
		}
		if modes[i].Describe() == "" {
			t.Errorf("%q has no description for the picker", modes[i])
		}
	}
	if protocol.Autonomy("").Normalize() != protocol.AutonomySupervised {
		t.Errorf("empty Normalize = %q, want supervised", protocol.Autonomy("").Normalize())
	}
	if got, want := protocol.AutonomySupervised.Short(), "sup"; got != want {
		t.Errorf("supervised Short = %q, want %q", got, want)
	}
	if got, want := protocol.AutonomyAgent.Short(), "agent"; got != want {
		t.Errorf("agent Short = %q, want %q", got, want)
	}
	if got, want := protocol.AutonomyChecks.Short(), "checks"; got != want {
		t.Errorf("checks Short = %q, want %q", got, want)
	}
}

func TestAutonomySelectedRoundTripsThroughTheEnvelope(t *testing.T) {
	original := protocol.AutonomySelected{
		Correlation: protocol.Correlation{
			SessionID:         "session-1",
			TurnID:            "turn-1",
			ProviderRequestID: "provider-1",
		},
		Mode: protocol.AutonomyChecks,
	}
	envelope, err := protocol.Wrap(original)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if envelope.Type != "autonomy.selected" {
		t.Errorf("envelope type = %q, want autonomy.selected", envelope.Type)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode envelope data: %v", err)
	}
	if got := string(data["mode"]); got != `"checks"` {
		t.Errorf("mode = %s, want \"checks\"", got)
	}
	decoded, err := envelope.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, ok := decoded.(protocol.AutonomySelected)
	if !ok {
		t.Fatalf("decoded type = %T, want protocol.AutonomySelected", decoded)
	}
	if got != original {
		t.Errorf("round trip = %+v, want %+v", got, original)
	}
}
