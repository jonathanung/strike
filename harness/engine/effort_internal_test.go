package engine

import (
	"testing"

	"github.com/jonathanung/strike-cli/harness/provider"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// TestProviderEffortCoversEveryLevel is the guard that keeps the two effort
// ladders in lockstep. protocol owns the frontend vocabulary and provider owns
// the wire mapping; they are separate types so internal/tui never imports the
// provider layer, and this test is what makes that duplication safe. Adding a
// rung to protocol.Efforts without teaching providerEffort about it would
// otherwise silently fall through to EffortDefault.
func TestProviderEffortCoversEveryLevel(t *testing.T) {
	protocolLevels := protocol.Efforts()
	providerLevels := provider.Efforts()
	if len(protocolLevels) != len(providerLevels) {
		t.Fatalf("ladders differ in length: protocol has %d, provider has %d", len(protocolLevels), len(providerLevels))
	}
	for i, level := range protocolLevels {
		got := providerEffort(level)
		if got == provider.EffortDefault {
			t.Errorf("providerEffort(%q) fell through to the unset default", level)
			continue
		}
		if string(got) != string(level) {
			t.Errorf("providerEffort(%q) = %q, want the matching provider level", level, got)
		}
		if got != providerLevels[i] {
			t.Errorf("ladder order differs at %d: protocol %q maps to %q, provider has %q", i, level, got, providerLevels[i])
		}
	}
}

func TestProviderEffortLeavesUnsetAlone(t *testing.T) {
	if got := providerEffort(protocol.EffortDefault); got != provider.EffortDefault {
		t.Errorf("providerEffort(unset) = %q, want unset", got)
	}
}
