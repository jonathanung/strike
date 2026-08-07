package protocol_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestLifecycleErrorError(t *testing.T) {
	err := protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, "missing", "abc")
	if err.Error() != "session_not_found: missing" {
		t.Fatalf("Error() = %q", err.Error())
	}
	var le *protocol.LifecycleError
	if !errors.As(err, &le) {
		t.Fatal("errors.As failed")
	}
	if le.SessionID != "abc" {
		t.Fatalf("SessionID = %q", le.SessionID)
	}
}

func TestLifecycleCapabilitiesJSON(t *testing.T) {
	caps := protocol.LifecycleCapabilities{
		List:            true,
		Get:             true,
		Fork:            true,
		ForkAt:          true,
		Load:            true,
		RewindPoints:    true,
		Replay:          true,
		EngineRewind:    true,
		ActiveSessionID: "s1",
	}
	raw, err := json.Marshal(caps)
	if err != nil {
		t.Fatal(err)
	}
	var back protocol.LifecycleCapabilities
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.List || !back.ForkAt || back.ActiveSessionID != "s1" {
		t.Fatalf("round-trip = %+v", back)
	}
}

func TestSessionSummaryJSON(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	sum := protocol.SessionSummary{
		ID:         "id1",
		Title:      "hello",
		ForkedFrom: "parent",
		UpdatedAt:  now,
		EventCount: 3,
	}
	raw, err := json.Marshal(sum)
	if err != nil {
		t.Fatal(err)
	}
	var back protocol.SessionSummary
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.ID != "id1" || back.ForkedFrom != "parent" || back.EventCount != 3 {
		t.Fatalf("back = %+v", back)
	}
}

func TestLifecycleMethodConstantsStable(t *testing.T) {
	// Wire names are part of the public contract — do not rename lightly.
	want := map[string]string{
		"capabilities":  protocol.LifecycleMethodCapabilities,
		"list":          protocol.LifecycleMethodList,
		"get":           protocol.LifecycleMethodGet,
		"fork":          protocol.LifecycleMethodFork,
		"fork_at":       protocol.LifecycleMethodForkAt,
		"load":          protocol.LifecycleMethodLoad,
		"rewind_points": protocol.LifecycleMethodRewindPoints,
		"replay":        protocol.LifecycleMethodReplay,
	}
	for k, v := range want {
		if v == "" {
			t.Fatalf("empty method for %s", k)
		}
		if v[:8] != "session." {
			t.Fatalf("%s = %q, want session.* prefix", k, v)
		}
	}
}
