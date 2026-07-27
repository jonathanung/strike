package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestParseNestedModels(t *testing.T) {
	raw := []byte(`{
  "acme": {
    "options": { "baseURL": "https://a.example/v1", "apiKey": "{env:ACME_KEY}" },
    "models": {
      "k2": {
        "name": "Acme K2",
        "limit": { "context": 128000, "output": 8192 },
        "options": { "forcedReasoning": true },
        "variants": {
          "high": { "reasoningEffort": "high", "textVerbosity": "low" },
          "low": { "reasoningEffort": "low" }
        }
      },
      "k1": { "name": "Acme K1" }
    }
  }
}`)
	pf, err := ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Customs) != 1 {
		t.Fatalf("customs = %+v", pf.Customs)
	}
	acme := pf.Customs[0]
	if acme.APIKeyEnv != "ACME_KEY" || len(acme.ModelDefs) != 2 {
		t.Fatalf("acme = %+v", acme)
	}
	// Sorted keys: k1, k2
	if acme.Models[0] != "k1" || acme.Models[1] != "k2" {
		t.Fatalf("models order = %v", acme.Models)
	}
	k2, ok := FindModelDef(acme.ModelDefs, "k2")
	if !ok || k2.Name != "Acme K2" || k2.Limit == nil || k2.Limit.Context != 128000 {
		t.Fatalf("k2 = %+v", k2)
	}
	if len(k2.VariantIDs()) != 2 {
		t.Fatalf("variants = %v", k2.VariantIDs())
	}
	opts, ok := ResolveVariant(k2, "high")
	if !ok || opts["reasoningEffort"] != "high" {
		t.Fatalf("high variant = %v ok=%v", opts, ok)
	}
	level, ok := VariantEffort(opts)
	if !ok || level != protocol.EffortHigh {
		t.Fatalf("VariantEffort = %q ok=%v", level, ok)
	}
}

func TestParseLegacyFlatModels(t *testing.T) {
	raw := []byte(`{
  "ollama": {
    "api": "openai",
    "options": { "baseURL": "http://127.0.0.1:11434/v1" },
    "models": ["llama3", "qwen2"]
  }
}`)
	pf, err := ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	p := pf.Customs[0]
	if len(p.Models) != 2 || p.Models[0] != "llama3" || p.ModelDefs[0].ID != "llama3" {
		t.Fatalf("legacy flat = %+v defs=%+v", p.Models, p.ModelDefs)
	}
}

func TestParseBuiltinOverlayDoesNotCreateCustom(t *testing.T) {
	raw := []byte(`{
  "openai": {
    "models": {
      "gpt-5.5": {
        "name": "GPT-5.5 Custom",
        "limit": { "context": 272000 },
        "variants": { "high": { "reasoningEffort": "high" } }
      }
    }
  }
}`)
	pf, err := ParseProvidersFile(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.Customs) != 0 {
		t.Fatalf("builtin must not become custom: %+v", pf.Customs)
	}
	defs := pf.Overlays["openai"]
	if len(defs) != 1 || defs[0].ID != "gpt-5.5" || defs[0].Name != "GPT-5.5 Custom" {
		t.Fatalf("overlay = %+v", defs)
	}
	if defs[0].Limit == nil || defs[0].Limit.Context != 272000 {
		t.Fatalf("limit = %+v", defs[0].Limit)
	}
}

func TestParseInvalidModelFailsClearly(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "empty id in array",
			raw:  `{"x":{"options":{"baseURL":"https://x.example/v1"},"models":["ok",""]}}`,
			want: "empty",
		},
		{
			name: "negative context",
			raw: `{
  "x": {
    "options": { "baseURL": "https://x.example/v1" },
    "models": { "m": { "limit": { "context": -1 } } }
  }
}`,
			want: "context",
		},
		{
			name: "empty variant id",
			raw: `{
  "x": {
    "options": { "baseURL": "https://x.example/v1" },
    "models": { "m": { "variants": { "": { "reasoningEffort": "high" } } } }
  }
}`,
			want: "variant",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseProvidersFile([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(strings.ToLower(err.Error()), tc.want) {
				t.Errorf("err = %v, want substring %q", err, tc.want)
			}
		})
	}
}

func TestMergeModelDefsOverlayWins(t *testing.T) {
	base := []ModelDef{
		{ID: "a", Name: "A"},
		{ID: "b", Name: "B", Limit: &ModelLimit{Context: 100}},
	}
	layer := []ModelDef{
		{ID: "b", Name: "B2", Limit: &ModelLimit{Context: 200}},
		{ID: "c", Name: "C"},
	}
	got := mergeModelDefs(base, layer)
	if len(got) != 3 || got[0].ID != "a" || got[1].Name != "B2" || got[1].Limit.Context != 200 || got[2].ID != "c" {
		t.Fatalf("merge = %+v", got)
	}
}

func TestVariantEffortKeys(t *testing.T) {
	level, ok := VariantEffort(map[string]any{"reasoningEffort": "xhigh"})
	if !ok || level != protocol.EffortXHigh {
		t.Fatalf("reasoningEffort = %q ok=%v", level, ok)
	}
	level, ok = VariantEffort(map[string]any{"effort": "low"})
	if !ok || level != protocol.EffortLow {
		t.Fatalf("effort = %q ok=%v", level, ok)
	}
	if _, ok := VariantEffort(map[string]any{"reasoningEffort": "turbo"}); ok {
		t.Fatal("unknown effort should fail")
	}
	if _, ok := VariantEffort(map[string]any{"textVerbosity": "low"}); ok {
		t.Fatal("no effort key should fail")
	}
}

func TestLoadMergesBuiltinOverlay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	path := filepath.Join(home, ".strike", "providers.jsonc")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
  "openai": {
    "models": {
      "gpt-overlay": { "name": "Overlay", "limit": { "context": 99 } }
    }
  },
  "acme": {
    "options": { "baseURL": "https://a.example/v1" },
    "models": ["k1"]
  }
}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := FindCustom(cfg.Providers, "openai"); ok {
		t.Fatal("openai must not be custom")
	}
	if len(cfg.ModelOverlays["openai"]) != 1 {
		t.Fatalf("overlays = %+v", cfg.ModelOverlays)
	}
	acme, ok := FindCustom(cfg.Providers, "acme")
	if !ok || len(acme.Models) != 1 {
		t.Fatalf("acme = %+v ok=%v", acme, ok)
	}
}
