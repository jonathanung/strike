package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionIsSemver(t *testing.T) {
	parts := strings.Split(Version, ".")
	if len(parts) != 3 {
		t.Fatalf("Version %q: want major.minor.patch", Version)
	}
	for _, p := range parts {
		if p == "" {
			t.Fatalf("Version %q has empty component", Version)
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				t.Fatalf("Version %q: non-digit in %q", Version, p)
			}
		}
	}
	if LegacyVersion != Version {
		// 1.x line: legacy empty-v envelopes match current until a major bump
		// that still accepts them via SchemaVersion defaults.
		t.Logf("LegacyVersion %q differs from Version %q (expected after major bumps)", LegacyVersion, Version)
	}
}

func TestWrapSetsEnvelopeVersion(t *testing.T) {
	env, err := Wrap(UserMessage{Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if env.Version != Version {
		t.Fatalf("Version = %q, want %q", env.Version, Version)
	}
	if env.SchemaVersion() != Version {
		t.Fatalf("SchemaVersion = %q, want %q", env.SchemaVersion(), Version)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"v":"`+Version+`"`) {
		t.Fatalf("marshaled envelope missing v field: %s", raw)
	}
}

func TestLegacyEnvelopeSchemaVersion(t *testing.T) {
	var env Envelope
	if err := json.Unmarshal([]byte(`{"type":"user.message","time":"2024-01-01T00:00:00Z","data":{"text":"hi"}}`), &env); err != nil {
		t.Fatal(err)
	}
	if env.Version != "" {
		t.Fatalf("legacy Version = %q, want empty", env.Version)
	}
	if got := env.SchemaVersion(); got != LegacyVersion {
		t.Fatalf("SchemaVersion = %q, want %q", got, LegacyVersion)
	}
	ev, err := env.Decode()
	if err != nil {
		t.Fatal(err)
	}
	if um, ok := ev.(UserMessage); !ok || um.Text != "hi" {
		t.Fatalf("Decode = %#v", ev)
	}
}

func TestWrapOpSetsEnvelopeVersion(t *testing.T) {
	env, err := WrapOp(Interrupt{})
	if err != nil {
		t.Fatal(err)
	}
	if env.Version != Version {
		t.Fatalf("Version = %q, want %q", env.Version, Version)
	}
	if env.SchemaVersion() != Version {
		t.Fatalf("SchemaVersion = %q, want %q", env.SchemaVersion(), Version)
	}
}
