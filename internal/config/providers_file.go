package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// providersFileEntry is one OpenCode-style object under providers.jsonc.
//
//	"my-proxy": {
//	  "npm": "@ai-sdk/openai-compatible",  // optional; maps wire dialect
//	  "name": "Display Name",             // optional display label (ignored as id)
//	  "api": "openai",                    // optional strike override
//	  "options": {
//	    "baseURL": "https://…",
//	    "apiKey": "{env:PROVIDER_API_KEY}"
//	  },
//	  "models": ["model-a"]
//	}
//
// The map key is the provider id (slug). npm is optional and never executed —
// strike has no npm SDK loader; it only hints openai vs anthropic wire shape.
type providersFileEntry struct {
	NPM     string             `json:"npm,omitempty"`
	Name    string             `json:"name,omitempty"`
	API     string             `json:"api,omitempty"`
	Models  []string           `json:"models,omitempty"`
	Options *providersFileOpts `json:"options,omitempty"`
	// Flat strike-native fields (also accepted without options{}).
	BaseURL   string            `json:"baseURL,omitempty"`
	APIKeyEnv string            `json:"apiKeyEnv,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type providersFileOpts struct {
	BaseURL string            `json:"baseURL,omitempty"`
	APIKey  string            `json:"apiKey,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// GlobalProvidersFilePath prefers providers.jsonc then providers.json under
// ~/.strike. Empty string means the path cannot be resolved.
func GlobalProvidersFilePath() string {
	root := GlobalRoot()
	if root == "" {
		return ""
	}
	return firstExisting(
		filepath.Join(root, "providers.jsonc"),
		filepath.Join(root, "providers.json"),
	)
}

// ProjectProvidersFilePath prefers providers.jsonc then providers.json under
// <workDir>/.strike.
func ProjectProvidersFilePath(workDir string) string {
	if workDir == "" {
		return ""
	}
	root := projectRoot(workDir)
	return firstExisting(
		filepath.Join(root, "providers.jsonc"),
		filepath.Join(root, "providers.json"),
	)
}

// providersFileCandidates returns paths to try for a .strike root (jsonc then json).
func providersFileCandidates(dir string) []string {
	if dir == "" {
		return nil
	}
	return []string{
		filepath.Join(dir, "providers.jsonc"),
		filepath.Join(dir, "providers.json"),
	}
}

func firstExisting(paths ...string) string {
	for _, p := range paths {
		if p == "" {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Prefer jsonc path for writers even when missing.
	if len(paths) > 0 {
		return paths[0]
	}
	return ""
}

// loadProvidersFileLayer loads the first existing providers.jsonc/json in dir.
// Missing dir/files yield (nil, nil).
func loadProvidersFileLayer(dir string) ([]CustomProvider, error) {
	for _, path := range providersFileCandidates(dir) {
		items, err := ReadProvidersFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		return items, nil
	}
	return nil, nil
}

// ReadProvidersFile parses an OpenCode-style providers map or a JSON array of
// CustomProvider objects. Supports JSONC (// and /* */ comments).
func ReadProvidersFile(path string) ([]CustomProvider, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseProvidersFile(data)
}

// ParseProvidersFile decodes providers.jsonc/json bytes.
func ParseProvidersFile(data []byte) ([]CustomProvider, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return nil, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return nil, nil
	}
	switch stripped[0] {
	case '{':
		return parseProvidersMap(stripped)
	case '[':
		return parseProvidersArray(stripped)
	default:
		return nil, fmt.Errorf("providers file must be a JSON object or array")
	}
}

func parseProvidersMap(data []byte) ([]CustomProvider, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	// Stable order: sort keys for deterministic merge/tests.
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]CustomProvider, 0, len(keys))
	for _, key := range keys {
		// Nested "providers" key with array — rare wrap form.
		if key == "providers" {
			var arr []CustomProvider
			if err := json.Unmarshal(raw[key], &arr); err == nil {
				for _, p := range arr {
					p = NormalizeCustomProvider(p)
					if err := p.Validate(); err != nil {
						continue
					}
					out = append(out, p)
				}
				continue
			}
		}
		var entry providersFileEntry
		if err := json.Unmarshal(raw[key], &entry); err != nil {
			continue
		}
		p, err := entry.toCustom(key)
		if err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func parseProvidersArray(data []byte) ([]CustomProvider, error) {
	var arr []CustomProvider
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	out := make([]CustomProvider, 0, len(arr))
	for _, p := range arr {
		p = NormalizeCustomProvider(p)
		if name, ok := EnvRefName(p.APIKeyEnv); ok {
			p.APIKeyEnv = name
		} else {
			p.APIKeyEnv = NormalizeAPIKeyEnv(p.APIKeyEnv)
		}
		if err := p.Validate(); err != nil {
			continue
		}
		out = append(out, p)
	}
	return out, nil
}

func (e providersFileEntry) toCustom(key string) (CustomProvider, error) {
	id := strings.ToLower(strings.TrimSpace(key))
	if id == "" {
		return CustomProvider{}, errors.New("empty provider id")
	}
	baseURL := strings.TrimSpace(e.BaseURL)
	headers := e.Headers
	apiKeyRaw := ""
	if e.Options != nil {
		if u := strings.TrimSpace(e.Options.BaseURL); u != "" {
			baseURL = u
		}
		apiKeyRaw = strings.TrimSpace(e.Options.APIKey)
		if len(e.Options.Headers) > 0 {
			headers = e.Options.Headers
		}
	}
	apiKeyEnv := NormalizeAPIKeyEnv(e.APIKeyEnv)
	if apiKeyEnv == "" && apiKeyRaw != "" {
		if name, ok := EnvRefName(apiKeyRaw); ok {
			apiKeyEnv = name
		}
		// Literal apiKey values are refused — credentials stay in auth/env only.
	}
	api := WireAPI(strings.ToLower(strings.TrimSpace(e.API)))
	if api == "" {
		api = wireFromNPM(e.NPM)
	}
	p := NormalizeCustomProvider(CustomProvider{
		Name:      id,
		BaseURL:   baseURL,
		API:       api,
		Headers:   headers,
		APIKeyEnv: apiKeyEnv,
		Models:    e.Models,
	})
	if err := p.Validate(); err != nil {
		return CustomProvider{}, err
	}
	return p, nil
}

// wireFromNPM maps optional npm package hints to a wire dialect. Unknown or
// empty npm defaults to openai-compatible chat completions.
func wireFromNPM(npm string) WireAPI {
	n := strings.ToLower(strings.TrimSpace(npm))
	if n == "" {
		return WireOpenAI
	}
	if strings.Contains(n, "anthropic") {
		return WireAnthropic
	}
	return WireOpenAI
}

// removeProviderFromProvidersFile deletes a provider key/entry from a
// providers.jsonc/json map or array. Missing files are OK.
func removeProviderFromProvidersFile(path, name string) error {
	if path == "" {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stripped, err := stripJSONC(data)
	if err != nil {
		return err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return nil
	}
	var out []byte
	switch stripped[0] {
	case '{':
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(stripped, &raw); err != nil {
			return err
		}
		// Case-insensitive key match.
		for k := range raw {
			if strings.ToLower(k) == name {
				delete(raw, k)
			}
		}
		if wrap, ok := raw["providers"]; ok {
			var arr []CustomProvider
			if err := json.Unmarshal(wrap, &arr); err == nil {
				filtered := arr[:0]
				for _, p := range arr {
					if strings.ToLower(p.Name) != name {
						filtered = append(filtered, p)
					}
				}
				b, err := json.Marshal(filtered)
				if err != nil {
					return err
				}
				raw["providers"] = b
			}
		}
		out, err = json.MarshalIndent(raw, "", "  ")
		if err != nil {
			return err
		}
	case '[':
		var arr []CustomProvider
		if err := json.Unmarshal(stripped, &arr); err != nil {
			return err
		}
		filtered := make([]CustomProvider, 0, len(arr))
		for _, p := range arr {
			if strings.ToLower(p.Name) != name {
				filtered = append(filtered, p)
			}
		}
		out, err = json.MarshalIndent(filtered, "", "  ")
		if err != nil {
			return err
		}
	default:
		return nil
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}

// removeProviderFromConfigFile drops name from a strike config JSON's providers array.
func removeProviderFromConfigFile(path, name string) error {
	if path == "" {
		return nil
	}
	name = strings.ToLower(strings.TrimSpace(name))
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w", path, err)
	}
	if len(cfg.Providers) == 0 {
		return nil
	}
	filtered := cfg.Providers[:0]
	changed := false
	for _, p := range cfg.Providers {
		if p.Name == name {
			changed = true
			continue
		}
		filtered = append(filtered, p)
	}
	if !changed {
		return nil
	}
	cfg.Providers = filtered
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
