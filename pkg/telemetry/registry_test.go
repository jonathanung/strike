package telemetry_test

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/redact"
	"github.com/jonathanung/strike-cli/pkg/telemetry"
)

func TestEmbeddedRegistryValid(t *testing.T) {
	reg, err := telemetry.EmbeddedRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.SchemaVersion != telemetry.SchemaVersion {
		t.Fatalf("schemaVersion = %q, want %q", reg.SchemaVersion, telemetry.SchemaVersion)
	}
	for _, id := range telemetry.CoreFamilyIDs {
		if _, ok := reg.Family(id); !ok {
			t.Errorf("missing core family %q", id)
		}
	}
}

func TestRegistryDrift(t *testing.T) {
	reg := telemetry.MustEmbeddedRegistry()
	if err := telemetry.CheckDrift(reg); err != nil {
		t.Fatal(err)
	}
}

func TestDiskRegistryMatchesEmbedded(t *testing.T) {
	root := repoRoot(t)
	if err := telemetry.CheckEmbeddedFile(root); err != nil {
		t.Fatal(err)
	}
}

func TestRedactPermissionPatterns(t *testing.T) {
	ev := telemetry.PermissionEvent{
		Permission:  "bash",
		Action:      "deny",
		Patterns:    []string{`API_KEY=sk-ant-secretvalue123456`, "git status"},
		RulePattern: "Bearer FAKESECRET_m3n4o5p6q7r8s9t0u1v2",
		Layer:       "config",
	}
	if err := telemetry.RedactRecord(&ev); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ev.Patterns[0], "sk-ant-") {
		t.Fatalf("pattern not scrubbed: %q", ev.Patterns[0])
	}
	if !strings.Contains(ev.Patterns[0], redact.Placeholder) && !strings.Contains(ev.Patterns[0], "REDACTED") {
		t.Fatalf("expected redaction marker in %q", ev.Patterns[0])
	}
	if strings.Contains(ev.RulePattern, "supersecrettoken") {
		t.Fatalf("rulePattern not scrubbed: %q", ev.RulePattern)
	}
	// Non-scrub fields preserved.
	if ev.Permission != "bash" || ev.Action != "deny" || ev.Layer != "config" {
		t.Fatalf("none-policy fields mutated: %+v", ev)
	}
}

func TestRedactOmitAndHash(t *testing.T) {
	// Use a local struct with hash/omit to exercise policies beyond core families.
	type sample struct {
		Keep   string `telemetry:"redact=none"`
		Secret string `telemetry:"redact=hash"`
		Drop   string `telemetry:"redact=omit"`
	}
	s := sample{Keep: "ok", Secret: "raw-secret-value", Drop: "gone"}
	if err := telemetry.RedactRecord(&s); err != nil {
		t.Fatal(err)
	}
	if s.Keep != "ok" {
		t.Fatalf("keep = %q", s.Keep)
	}
	if s.Drop != "" {
		t.Fatalf("omit left %q", s.Drop)
	}
	if s.Secret == "raw-secret-value" || len(s.Secret) != 64 {
		t.Fatalf("hash = %q, want 64-char hex", s.Secret)
	}
	if s.Secret != telemetry.HashString("raw-secret-value") {
		t.Fatalf("hash mismatch")
	}
}

func TestNewEnvelopeRedacts(t *testing.T) {
	ev := telemetry.ToolEvent{
		CallID:      "c1",
		Name:        "bash",
		ArgsPreview: "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz",
		ErrorCode:   "permission_denied",
	}
	env, err := telemetry.NewEnvelope(telemetry.FamilyTool, time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC), ev)
	if err != nil {
		t.Fatal(err)
	}
	if env.SchemaVersion != telemetry.SchemaVersion || env.Family != telemetry.FamilyTool {
		t.Fatalf("envelope meta: %+v", env)
	}
	if !strings.Contains(env.Time, "2026-08-06") {
		t.Fatalf("time = %q", env.Time)
	}
	var out telemetry.ToolEvent
	if err := json.Unmarshal(env.Payload, &out); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.ArgsPreview, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Fatalf("args not redacted: %q", out.ArgsPreview)
	}
	if out.CallID != "c1" || out.ErrorCode != "permission_denied" {
		t.Fatalf("stable fields: %+v", out)
	}
}

func TestGoldenFixtures(t *testing.T) {
	reg := telemetry.MustEmbeddedRegistry()
	raw, err := os.ReadFile(filepath.Join("testdata", "golden_envelopes.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) < len(telemetry.CoreFamilyIDs) {
		t.Fatalf("golden lines = %d, want >= %d", len(lines), len(telemetry.CoreFamilyIDs))
	}
	seen := map[string]bool{}
	for i, line := range lines {
		var env telemetry.Envelope
		if err := json.Unmarshal(line, &env); err != nil {
			t.Fatalf("line %d: %v", i+1, err)
		}
		if env.SchemaVersion != telemetry.SchemaVersion {
			t.Errorf("line %d schemaVersion = %q", i+1, env.SchemaVersion)
		}
		if _, ok := reg.Family(env.Family); !ok {
			t.Errorf("line %d unknown family %q", i+1, env.Family)
		}
		seen[env.Family] = true
		// Payload must be an object.
		if !json.Valid(env.Payload) || len(env.Payload) == 0 || env.Payload[0] != '{' {
			t.Errorf("line %d payload not object: %s", i+1, env.Payload)
		}
	}
	for _, id := range telemetry.CoreFamilyIDs {
		if !seen[id] {
			t.Errorf("golden missing family %q", id)
		}
	}
}

func TestLoadRegistryRejectsBad(t *testing.T) {
	_, err := telemetry.LoadRegistry([]byte(`{"schemaVersion":"1","families":[]}`))
	if err == nil {
		t.Fatal("expected error for empty families")
	}
	_, err = telemetry.LoadRegistry([]byte(`{"schemaVersion":"1","families":[{"id":"tool","fields":[{"name":"x","type":"string","redact":"nope"}]}]}`))
	if err == nil {
		t.Fatal("expected error for bad redact")
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	// pkg/telemetry → repo root
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
