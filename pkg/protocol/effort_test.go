package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestParseEffortAcceptsEveryLevelCaseInsensitively(t *testing.T) {
	for _, level := range protocol.Efforts() {
		for _, spelling := range []string{string(level), " " + string(level) + " ", strings.ToUpper(string(level))} {
			got, ok := protocol.ParseEffort(spelling)
			if !ok {
				t.Errorf("ParseEffort(%q) rejected", spelling)
				continue
			}
			if got != level {
				t.Errorf("ParseEffort(%q) = %q, want %q", spelling, got, level)
			}
		}
	}
}

func TestParseEffortEmptyIsUnsetAndUnknownIsRejected(t *testing.T) {
	if got, ok := protocol.ParseEffort("  "); !ok || got != protocol.EffortDefault {
		t.Errorf("ParseEffort(blank) = (%q, %v), want unset and accepted", got, ok)
	}
	for _, bad := range []string{"none", "highest", "turbo", "0"} {
		if _, ok := protocol.ParseEffort(bad); ok {
			t.Errorf("ParseEffort(%q) accepted, want rejected", bad)
		}
	}
}

func TestEffortsAreOrderedAndDescribed(t *testing.T) {
	levels := protocol.Efforts()
	want := []protocol.Effort{
		protocol.EffortOff, protocol.EffortLow, protocol.EffortMedium,
		protocol.EffortHigh, protocol.EffortXHigh, protocol.EffortMax,
	}
	if len(levels) != len(want) {
		t.Fatalf("Efforts() = %v, want %v", levels, want)
	}
	for i, level := range want {
		if levels[i] != level {
			t.Errorf("Efforts()[%d] = %q, want %q", i, levels[i], level)
		}
		if levels[i].Describe() == "" {
			t.Errorf("%q has no description for the picker", levels[i])
		}
	}
}

// TestEffortSelectedRoundTripsThroughTheEnvelope keeps the event replayable
// from a session log, like every other event in the stream.
func TestEffortSelectedRoundTripsThroughTheEnvelope(t *testing.T) {
	original := protocol.EffortSelected{
		Correlation: protocol.Correlation{
			SessionID:         "session-1",
			TurnID:            "turn-1",
			ProviderRequestID: "provider-1",
		},
		Level: protocol.EffortXHigh,
	}
	envelope, err := protocol.Wrap(original)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if envelope.Type != "effort.selected" {
		t.Errorf("envelope type = %q, want effort.selected", envelope.Type)
	}
	var data map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("decode envelope data: %v", err)
	}
	for key, want := range map[string]string{
		"sessionId":         `"session-1"`,
		"turnId":            `"turn-1"`,
		"providerRequestId": `"provider-1"`,
	} {
		if got := string(data[key]); got != want {
			t.Errorf("%s = %s, want %s; data: %s", key, got, want, envelope.Data)
		}
	}
	if _, ok := data["correlation"]; ok {
		t.Errorf("correlation must be flat, not nested: %s", envelope.Data)
	}
	decoded, err := envelope.Decode()
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	got, ok := decoded.(protocol.EffortSelected)
	if !ok {
		t.Fatalf("decoded type = %T, want protocol.EffortSelected", decoded)
	}
	if got != original {
		t.Errorf("round trip = %+v, want %+v", got, original)
	}
}
