// Package models queries the models.dev catalog (the provider/model
// registry opencode uses) for available models per provider. The catalog
// is cached at ~/.strike/cache/models.json for 24h, with stale-cache
// fallback when offline — the same logic opencode ships. internal/host/local
// is the only importer, wrapping it as host.Catalog; internal/tui never
// imports it directly.
package models

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	catalogURL = "https://models.dev/api.json"
	cacheTTL   = 24 * time.Hour
)

type Model struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Models map[string]Model `json:"models"`
}

// Catalog maps provider id (anthropic, openai, xai, …) to its models.
type Catalog map[string]Provider

func cachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "models.json")
	}
	return filepath.Join(home, ".strike", "cache", "models.json")
}

// Load returns the catalog: fresh cache if young enough, otherwise a
// network fetch with the stale cache as offline fallback.
func Load(ctx context.Context) (Catalog, error) {
	path := cachePath()
	if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) < cacheTTL {
		if catalog, err := readCache(path); err == nil {
			return catalog, nil
		}
	}
	catalog, raw, err := fetch(ctx)
	if err != nil {
		if catalog, cacheErr := readCache(path); cacheErr == nil {
			return catalog, nil // offline: stale beats nothing
		}
		return nil, err
	}
	writeCache(path, raw)
	return catalog, nil
}

// ModelIDs lists a provider's model ids, sorted.
func (c Catalog) ModelIDs(provider string) []string {
	p, ok := c[provider]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(p.Models))
	for id := range p.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func fetch(ctx context.Context) (Catalog, []byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, catalogURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetching models.dev catalog: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("models.dev returned %s", resp.Status)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	var catalog Catalog
	if err := json.Unmarshal(raw, &catalog); err != nil {
		return nil, nil, fmt.Errorf("parsing models.dev catalog: %w", err)
	}
	return catalog, raw, nil
}

func readCache(path string) (Catalog, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var catalog Catalog
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, err
	}
	return catalog, nil
}

func writeCache(path string, raw []byte) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, raw, 0o644)
}
