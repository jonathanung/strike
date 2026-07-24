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
