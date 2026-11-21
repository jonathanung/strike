package server

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/jonathanung/strike/internal/protocol"
)

// TestEditResendRewindWire pins the browser side of edit & resend
// (#1361): the cockpit posts exactly this envelope to /v1/ops, the
// daemon advertises the op to browsers, and it decodes to the engine's
// last-turn Rewind with file restore.
func TestEditResendRewindWire(t *testing.T) {
	if !slices.Contains(browserProtocolOps, "rewind") {
		t.Fatal("rewind is not in browserProtocolOps")
	}
	var env protocol.OpEnvelope
	if err := json.Unmarshal([]byte(`{"type":"rewind","data":{"restoreFiles":true}}`), &env); err != nil {
		t.Fatal(err)
	}
	op, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := op.(protocol.Rewind)
	if !ok {
		t.Fatalf("decoded %T, want protocol.Rewind", op)
	}
	if !got.RestoreFiles {
		t.Fatal("restoreFiles was dropped")
	}
}
