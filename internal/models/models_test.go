package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelIDs(t *testing.T) {
	c := Catalog{
		"openai": {ID: "openai", Models: map[string]Model{
			"z": {ID: "z"},
			"a": {ID: "a"},
		}},
	}
	ids := c.ModelIDs("openai")
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "z" {
		t.Fatalf("ids = %#v", ids)
	}
	if c.ModelIDs("missing") != nil {
		t.Fatal("expected nil")
	}
}

func TestGoogleAndGeminiAliasMapToGoogleCatalog(t *testing.T) {
	// models.dev lists Google AI Studio under "google"; strike's canonical
	// provider id is also "google", with "gemini" accepted as a shipped alias.
	c := Catalog{
		"google": {ID: "google", Name: "Google", Models: map[string]Model{
			"gemini-2.5-pro":   {ID: "gemini-2.5-pro", Name: "Gemini 2.5 Pro", Limit: &ModelLimit{Context: 1_048_576, Output: 65_536}},
			"gemini-2.5-flash": {ID: "gemini-2.5-flash", Name: "Gemini 2.5 Flash"},
		}},
	}
	for _, id := range []string{"google", "gemini"} {
		ids := c.ModelIDs(id)
		if len(ids) != 2 || ids[0] != "gemini-2.5-flash" || ids[1] != "gemini-2.5-pro" {
			t.Fatalf("ModelIDs(%q) = %#v", id, ids)
		}
		infos := c.Infos(id)
		if len(infos) != 2 || infos[1].Context != 1_048_576 {
			t.Fatalf("Infos(%q) = %#v", id, infos)
		}
		if tokens, ok := c.ContextWindow(id, "gemini-2.5-pro"); !ok || tokens != 1_048_576 {
			t.Errorf("ContextWindow(%q, gemini-2.5-pro) = %d,%v", id, tokens, ok)
		}
		if tokens, ok := c.OutputLimit(id, "gemini-2.5-pro"); !ok || tokens != 65_536 {
			t.Errorf("OutputLimit(%q, gemini-2.5-pro) = %d,%v", id, tokens, ok)
		}
	}
	if modelsDevID("google") != "google" || modelsDevID("gemini") != "google" || modelsDevID("openai") != "openai" {
		t.Errorf("modelsDevID mapping broken: google=%q gemini=%q openai=%q",
			modelsDevID("google"), modelsDevID("gemini"), modelsDevID("openai"))
	}
}

func TestInfosMetadata(t *testing.T) {
	c := Catalog{
		"openai": {ID: "openai", Models: map[string]Model{
			"z-bare": {ID: "z-bare"},
			"a-full": {
				ID:         "a-full",
				Limit:      &ModelLimit{Context: 128_000, Output: 16_384},
				Cost:       &ModelCost{Input: 2.5, Output: 10},
				ToolCall:   true,
				Reasoning:  true,
				Attachment: true,
			},
		}},
	}
	infos := c.Infos("openai")
	if len(infos) != 2 {
		t.Fatalf("Infos len = %d, want 2", len(infos))
	}
	if infos[0].ID != "a-full" || infos[1].ID != "z-bare" {
		t.Fatalf("order = %q, %q", infos[0].ID, infos[1].ID)
	}
	full := infos[0]
	if full.Context != 128_000 || !full.HasCost || full.InputCost != 2.5 || full.OutputCost != 10 {
		t.Errorf("a-full limits/cost = %+v", full)
	}
	if !full.ToolCall || !full.Reasoning || !full.Attachment {
		t.Errorf("a-full caps = tools=%v reason=%v attach=%v", full.ToolCall, full.Reasoning, full.Attachment)
	}
	bare := infos[1]
	if bare.Context != 0 || bare.HasCost || bare.ToolCall || bare.Reasoning || bare.Attachment {
		t.Errorf("z-bare should be empty metadata: %+v", bare)
	}
	if c.Infos("missing") != nil {
		t.Fatal("expected nil for missing provider")
	}
}

func TestContextWindowAndOutputLimit(t *testing.T) {
	c := Catalog{
		"openai": {ID: "openai", Models: map[string]Model{
			"with-limit": {
				ID:    "with-limit",
				Limit: &ModelLimit{Context: 128_000, Output: 16_384},
			},
			"zero-limit": {
				ID:    "zero-limit",
				Limit: &ModelLimit{Context: 0, Output: 0},
			},
			"no-limit": {ID: "no-limit"},
			"partial": {
				ID:    "partial",
				Limit: &ModelLimit{Context: 200_000, Output: 0},
			},
		}},
	}

	if tokens, ok := c.ContextWindow("openai", "with-limit"); !ok || tokens != 128_000 {
		t.Errorf("ContextWindow(with-limit) = %d,%v want 128000,true", tokens, ok)
	}
	if tokens, ok := c.OutputLimit("openai", "with-limit"); !ok || tokens != 16_384 {
		t.Errorf("OutputLimit(with-limit) = %d,%v want 16384,true", tokens, ok)
	}

	if tokens, ok := c.ContextWindow("openai", "no-limit"); ok || tokens != 0 {
		t.Errorf("ContextWindow(no-limit) = %d,%v want 0,false", tokens, ok)
	}
	if tokens, ok := c.OutputLimit("openai", "no-limit"); ok || tokens != 0 {
		t.Errorf("OutputLimit(no-limit) = %d,%v want 0,false", tokens, ok)
	}

	if tokens, ok := c.ContextWindow("openai", "zero-limit"); ok || tokens != 0 {
		t.Errorf("ContextWindow(zero-limit) = %d,%v want 0,false", tokens, ok)
	}
	if tokens, ok := c.OutputLimit("openai", "zero-limit"); ok || tokens != 0 {
		t.Errorf("OutputLimit(zero-limit) = %d,%v want 0,false", tokens, ok)
	}

	if tokens, ok := c.ContextWindow("openai", "partial"); !ok || tokens != 200_000 {
		t.Errorf("ContextWindow(partial) = %d,%v want 200000,true", tokens, ok)
	}
	if tokens, ok := c.OutputLimit("openai", "partial"); ok || tokens != 0 {
		t.Errorf("OutputLimit(partial) = %d,%v want 0,false", tokens, ok)
	}

	if tokens, ok := c.ContextWindow("missing", "x"); ok || tokens != 0 {
		t.Errorf("ContextWindow(missing) = %d,%v want 0,false", tokens, ok)
	}
	if tokens, ok := c.OutputLimit("openai", "missing-model"); ok || tokens != 0 {
		t.Errorf("OutputLimit(missing-model) = %d,%v want 0,false", tokens, ok)
	}
}

func TestSupportsPriority(t *testing.T) {
	c := Catalog{
		"openai": {ID: "openai", Models: map[string]Model{
			"gpt-5.6-sol": {
				ID: "gpt-5.6-sol",
				Experimental: &experimental{Modes: map[string]json.RawMessage{
					"fast": json.RawMessage(`{}`),
				}},
			},
			"gpt-old": {ID: "gpt-old"},
		}},
		"anthropic": {ID: "anthropic", Models: map[string]Model{
			"claude": {ID: "claude"},
		}},
	}
	if !c.SupportsPriority("openai", "gpt-5.6-sol") {
		t.Fatal("expected gpt-5.6-sol to support priority")
	}
	if c.SupportsPriority("openai", "gpt-old") {
		t.Fatal("gpt-old must not support priority")
	}
	if c.SupportsPriority("anthropic", "claude") {
		t.Fatal("anthropic must not support priority")
	}
	if c.SupportsPriority("missing", "x") {
		t.Fatal("missing provider must not support priority")
	}
}

func TestLoadFreshCache(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".strike", "cache", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"echo":{"id":"echo","name":"Echo","models":{"echo":{"id":"echo","name":"Echo"}}}}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	cat, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if cat.ModelIDs("echo") == nil || cat.ModelIDs("echo")[0] != "echo" {
		t.Fatalf("catalog = %#v", cat)
	}
}

func TestLoadStaleCacheFallback(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".strike", "cache", "models.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"cached":{"id":"cached","name":"C","models":{"m1":{"id":"m1","name":"M1"}}}}`)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Make cache stale.
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}

	// Point catalogURL is a const — we cannot override it. Exercise readCache
	// and writeCache helpers plus stale path by calling readCache directly and
	// ensuring Load falls back when network fails (invalid DNS via canceled ctx
	// after forcing fetch). A canceled context makes fetch fail immediately.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cat, err := Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := cat.ModelIDs("cached"); len(got) != 1 || got[0] != "m1" {
		t.Fatalf("fallback catalog = %#v err=%v", cat, err)
	}
}

func TestReadWriteCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	raw, _ := json.Marshal(Catalog{
		"xai": {ID: "xai", Models: map[string]Model{"g": {ID: "g", Name: "G"}}},
	})
	writeCache(path, raw)
	cat, err := readCache(path)
	if err != nil {
		t.Fatal(err)
	}
	if cat.ModelIDs("xai")[0] != "g" {
		t.Fatalf("%#v", cat)
	}
}

func TestReadCacheCorrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte(`{`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readCache(path); err == nil {
		t.Fatal("expected error")
	}
}

// Ensure httptest shape matches what fetch expects if we ever inject a client.
func TestCatalogJSONShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(Catalog{
			"anthropic": {ID: "anthropic", Name: "Anthropic", Models: map[string]Model{
				"claude": {ID: "claude", Name: "Claude"},
			}},
		})
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var cat Catalog
	if err := json.NewDecoder(resp.Body).Decode(&cat); err != nil {
		t.Fatal(err)
	}
	if cat.ModelIDs("anthropic")[0] != "claude" {
		t.Fatalf("%#v", cat)
	}
}

func TestCachePathResolvesStrikeDirSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	target := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(home, ".strike")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	got := cachePath()
	want := filepath.Join(target, "cache", "models.json")
	if got != want {
		t.Errorf("cachePath() = %q, want %q", got, want)
	}
}
