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
	"strings"
	"time"
)

const (
	catalogURL = "https://models.dev/api.json"
	cacheTTL   = 24 * time.Hour
)

// ModelLimit is the models.dev token ceiling pair for a model.
type ModelLimit struct {
	Context int `json:"context"`
	Output  int `json:"output"`
}

// ModelCost is models.dev pricing in USD per million tokens.
type ModelCost struct {
	Input      float64 `json:"input"`
	Output     float64 `json:"output"`
	CacheRead  float64 `json:"cache_read,omitempty"`
	CacheWrite float64 `json:"cache_write,omitempty"`
}

type Model struct {
	ID           string        `json:"id"`
	Name         string        `json:"name"`
	Limit        *ModelLimit   `json:"limit,omitempty"`
	Cost         *ModelCost    `json:"cost,omitempty"`
	Attachment   bool          `json:"attachment"`
	Reasoning    bool          `json:"reasoning"`
	ToolCall     bool          `json:"tool_call"`
	Experimental *experimental `json:"experimental,omitempty"`
}

// Info is flat picker-facing metadata for one catalog model.
type Info struct {
	ID         string
	Name       string  // display label; empty means use ID
	Context    int     // tokens; 0 = unknown
	Output     int     // max output tokens; 0 = unknown
	InputCost  float64 // USD per million input tokens
	OutputCost float64 // USD per million output tokens
	HasCost    bool
	ToolCall   bool
	Reasoning  bool
	Attachment bool
}

// experimental holds optional models.dev mode metadata. The "fast" mode
// marks models that accept OpenAI's priority service tier.
type experimental struct {
	Modes map[string]json.RawMessage `json:"modes,omitempty"`
}

type Provider struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Models map[string]Model `json:"models"`
}

// Catalog maps provider id (anthropic, openai, xai, …) to its models.
type Catalog map[string]Provider

// modelsDevID maps strike provider ids to models.dev catalog keys.
// Strike's Google AI Studio provider id is "google" (same as models.dev);
// the shipped "gemini" alias still resolves here for uncanonicalized callers.
func modelsDevID(provider string) string {
	if provider == "gemini" {
		return "google"
	}
	return provider
}

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
	infos := c.Infos(provider)
	if infos == nil {
		return nil
	}
	ids := make([]string, len(infos))
	for i, info := range infos {
		ids[i] = info.ID
	}
	return ids
}

// Infos lists a provider's models with catalog metadata, sorted by id.
func (c Catalog) Infos(provider string) []Info {
	p, ok := c[modelsDevID(provider)]
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(p.Models))
	for id := range p.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]Info, 0, len(ids))
	for _, id := range ids {
		out = append(out, modelInfo(id, p.Models[id]))
	}
	return out
}

func modelInfo(id string, m Model) Info {
	info := Info{
		ID:         id,
		Name:       strings.TrimSpace(m.Name),
		ToolCall:   m.ToolCall,
		Reasoning:  m.Reasoning,
		Attachment: m.Attachment,
	}
	if m.Limit != nil {
		info.Context = m.Limit.Context
		info.Output = m.Limit.Output
	}
	if m.Cost != nil {
		info.HasCost = true
		info.InputCost = m.Cost.Input
		info.OutputCost = m.Cost.Output
	}
	return info
}

// SupportsPriority reports whether models.dev lists a "fast" experimental
// mode for the model (OpenAI priority service tier). Unknown providers or
// models return false.
func (c Catalog) SupportsPriority(provider, model string) bool {
	p, ok := c[modelsDevID(provider)]
	if !ok {
		return false
	}
	m, ok := p.Models[model]
	if !ok || m.Experimental == nil {
		return false
	}
	_, ok = m.Experimental.Modes["fast"]
	return ok
}

// ContextWindow returns the model's context window in tokens.
// ok is false when the provider, model, or limit is missing, or context is zero.
func (c Catalog) ContextWindow(provider, model string) (tokens int, ok bool) {
	m, ok := c.lookup(provider, model)
	if !ok || m.Limit == nil || m.Limit.Context == 0 {
		return 0, false
	}
	return m.Limit.Context, true
}

// OutputLimit returns the model's max output tokens.
// ok is false when the provider, model, or limit is missing, or output is zero.
func (c Catalog) OutputLimit(provider, model string) (tokens int, ok bool) {
	m, ok := c.lookup(provider, model)
	if !ok || m.Limit == nil || m.Limit.Output == 0 {
		return 0, false
	}
	return m.Limit.Output, true
}

func (c Catalog) lookup(provider, model string) (Model, bool) {
	p, ok := c[modelsDevID(provider)]
	if !ok {
		return Model{}, false
	}
	m, ok := p.Models[model]
	return m, ok
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
