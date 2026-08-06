package protocol_test

import (
	"reflect"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
	pub "github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestShimSharesIdentityWithPublicPackage(t *testing.T) {
	if protocol.Version != pub.Version {
		t.Fatalf("Version: internal %q != pkg %q", protocol.Version, pub.Version)
	}
	if protocol.LegacyVersion != pub.LegacyVersion {
		t.Fatalf("LegacyVersion: internal %q != pkg %q", protocol.LegacyVersion, pub.LegacyVersion)
	}

	// Type aliases must be identical types (assignable both ways without conversion).
	var (
		_ protocol.Event = pub.TextDelta{Text: "x"}
		_ pub.Event      = protocol.TextDelta{Text: "x"}
		_ protocol.Op    = pub.UserInput{Text: "hi"}
		_ pub.Op         = protocol.UserInput{Text: "hi"}
	)

	env, err := protocol.Wrap(protocol.UserMessage{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Version != pub.Version {
		t.Fatalf("Wrap version = %q, want %q", env.Version, pub.Version)
	}
	got, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	um, ok := got.(protocol.UserMessage)
	if !ok || um.Text != "hi" {
		t.Fatalf("Decode via shim = %#v", got)
	}

	// Ensure a representative helper still matches.
	if !reflect.DeepEqual(protocol.Efforts(), pub.Efforts()) {
		t.Fatalf("Efforts mismatch: %#v vs %#v", protocol.Efforts(), pub.Efforts())
	}
}
