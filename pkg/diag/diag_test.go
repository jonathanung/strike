package diag_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonathanung/strike-cli/pkg/diag"
	"github.com/jonathanung/strike-cli/pkg/protocol"
)

func TestBuildIncludesPrecedenceLayersAndDigests(t *testing.T) {
	fixed := time.Date(2026, 8, 5, 15, 4, 5, 0, time.UTC)
	secret := "sk-ant-api03-SUPERSECRETDIAGKEY99"
	b := diag.Build(diag.Input{
		Session: diag.Session{
			SessionID:       "sess-root",
			RootSessionID:   "sess-root",
			Depth:           0,
			ParentSessionID: "",
		},
		Layers: []protocol.PromptLayerInfo{
			{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 100, Preview: "You are strike"},
			{Kind: protocol.PromptLayerPersona, Source: "agent:build", Mode: protocol.PromptLayerReplace, Chars: 40, Preview: "PERSONA key=" + secret},
			{Kind: protocol.PromptLayerInstruction, Source: "file:/tmp/AGENTS.md", Mode: protocol.PromptLayerAppend, Chars: 20, Preview: "Use make test"},
		},
		SystemChars:    160,
		MessageCount:   2,
		FromLastStream: true,
		Attribution: protocol.RequestTokenAttribution{
			System: protocol.KnownTokens(40),
			Total:  protocol.KnownTokens(50),
			Source: protocol.UsageSourceEstimated,
		},
		Config: diag.Config{
			Provider:       "anthropic",
			Model:          "claude-sonnet-5",
			Agent:          "build",
			Effort:         "high",
			Autonomy:       "supervised",
			PermissionMode: "default",
			Sandbox:        "workspace-write",
			LeanCode:       "lite",
			MaxTokens:      8192,
			MaxChildDepth:  1,
			Compaction: diag.Compaction{
				Strategy:  "trim",
				Threshold: 0.7,
				Buffer:    4096,
			},
			Scheduler: diag.Scheduler{Limits: map[string]int{"process": 4, "model": 2}},
		},
		ProtocolVersion: protocol.Version,
		StrikeVersion:   "dev",
		Clock:           func() time.Time { return fixed },
	})

	if b.SchemaVersion != diag.SchemaVersion {
		t.Fatalf("schema = %q", b.SchemaVersion)
	}
	if !b.Redacted {
		t.Fatal("expected redacted=true")
	}
	if !b.ExportedAt.Equal(fixed) {
		t.Fatalf("exportedAt = %v, want %v", b.ExportedAt, fixed)
	}
	if b.ProtocolVersion != protocol.Version {
		t.Fatalf("protocolVersion = %q", b.ProtocolVersion)
	}
	if len(b.Prompt.Precedence) == 0 {
		t.Fatal("empty precedence")
	}
	if b.Prompt.LayerCount != 3 || len(b.Prompt.Layers) != 3 {
		t.Fatalf("layers = %+v", b.Prompt.Layers)
	}
	if b.Prompt.SystemChars != 160 || !b.Prompt.FromLastStream {
		t.Fatalf("prompt meta = %+v", b.Prompt)
	}
	if b.Session.IsChild {
		t.Fatal("root session marked isChild")
	}
	if b.Config.Digests["effective"] == "" || b.Config.Digests["layers"] == "" {
		t.Fatalf("digests = %+v", b.Config.Digests)
	}
	// Secret must not appear anywhere in the marshaled bundle.
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) || strings.Contains(string(raw), "sk-ant-api03-") {
		t.Fatalf("secret leaked in bundle:\n%s", raw)
	}
	if !strings.Contains(b.Prompt.Layers[1].Preview, "REDACTED") {
		t.Fatalf("persona preview not redacted: %q", b.Prompt.Layers[1].Preview)
	}
}

func TestBuildChildSession(t *testing.T) {
	b := diag.Build(diag.Input{
		Session: diag.Session{
			SessionID:       "child-1",
			ParentSessionID: "root-1",
			RootSessionID:   "root-1",
			Depth:           1,
		},
		Config: diag.Config{Agent: "explore", Provider: "echo", Model: "echo"},
		Clock:  func() time.Time { return time.Unix(0, 0).UTC() },
	})
	if !b.Session.IsChild {
		t.Fatal("expected isChild")
	}
	if b.Session.Depth != 1 || b.Session.ParentSessionID != "root-1" {
		t.Fatalf("session = %+v", b.Session)
	}
}

func TestExportJSONRedactsAndRoundTrips(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out", "diag.json")
	secret := "sk-proj-nested-secret-value-99"
	b := diag.Build(diag.Input{
		Session: diag.Session{SessionID: "s1", RootSessionID: "s1"},
		Layers: []protocol.PromptLayerInfo{
			{Kind: protocol.PromptLayerShared, Source: "builtin:shared", Mode: protocol.PromptLayerAppend, Chars: 10, Preview: "hi OPENAI_API_KEY=" + secret},
		},
		SystemChars: 10,
		Config: diag.Config{
			Provider: "openai",
			Model:    "gpt-test",
			WorkDir:  "/tmp/ws",
		},
		Clock: func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) },
	})
	if err := diag.ExportJSON(path, b); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{secret, "sk-proj-nested"} {
		if strings.Contains(string(data), banned) {
			t.Fatalf("unredacted %q in export:\n%s", banned, data)
		}
	}
	var got diag.Bundle
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != diag.SchemaVersion {
		t.Fatalf("schema = %q", got.SchemaVersion)
	}
	if !got.Redacted || got.Prompt.LayerCount != 1 {
		t.Fatalf("got = %+v", got)
	}
	if got.Config.Provider != "openai" {
		t.Fatalf("provider = %q", got.Config.Provider)
	}
}

func TestDigestJSONStable(t *testing.T) {
	a, err := diag.DigestJSON(map[string]int{"model": 2, "process": 4})
	if err != nil {
		t.Fatal(err)
	}
	b, err := diag.DigestJSON(map[string]int{"model": 2, "process": 4})
	if err != nil {
		t.Fatal(err)
	}
	if a != b || a == "" {
		t.Fatalf("digests differ or empty: %q vs %q", a, b)
	}
}

func TestPrecedenceDocumentsKnownKinds(t *testing.T) {
	want := []string{
		protocol.PromptLayerShared,
		protocol.PromptLayerTools,
		protocol.PromptLayerEnvironment,
		protocol.PromptLayerInstruction,
		protocol.PromptLayerMemory,
	}
	joined := strings.Join(diag.Precedence, ",")
	for _, k := range want {
		if !strings.Contains(joined, k) {
			t.Errorf("precedence missing %q: %v", k, diag.Precedence)
		}
	}
}
