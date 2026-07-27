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
//	  "models": ["model-a"]              // legacy flat ids
//	  // or nested:
//	  // "models": { "model-a": { "name": "A", "limit": { "context": 128000 } } }
//	}
//
// The map key is the provider id (slug). npm is optional and never executed —
// strike has no npm SDK loader; it only hints openai vs anthropic wire shape.
// Built-in keys (openai, anthropic, …) may carry options (baseURL/apiKey) as
// endpoint overlays and/or models as catalog overlays — they never become
// CustomProvider rows.
type providersFileEntry struct {
	NPM     string             `json:"npm,omitempty"`
	Name    string             `json:"name,omitempty"`
	API     string             `json:"api,omitempty"`
	Models  modelsJSON         `json:"models,omitempty"`
	Options *providersFileOpts `json:"options,omitempty"`
	// Flat strike-native fields (also accepted without options{}).
	BaseURL   string            `json:"baseURL,omitempty"`
	APIKeyEnv string            `json:"apiKeyEnv,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
}

// ProvidersFile is the result of parsing providers.jsonc/json.
type ProvidersFile struct {
	Customs   []CustomProvider
	Overlays  map[string][]ModelDef       // builtin (catalog) provider model overlays
	Endpoints map[string]ProviderEndpoint // builtin endpoint overlays (baseURL/apiKey)
}

// ProviderEndpoint customizes a built-in provider's HTTP origin and/or API key
// env without registering a separate custom provider. Empty fields mean
// "keep the built-in default".
type ProviderEndpoint struct {
	BaseURL   string
	APIKeyEnv string
	Headers   map[string]string
}

// Active reports whether any endpoint field is set.
func (e ProviderEndpoint) Active() bool {
	return strings.TrimSpace(e.BaseURL) != "" || strings.TrimSpace(e.APIKeyEnv) != "" || len(e.Headers) > 0
}

// ResolveEndpoint expands env placeholders in BaseURL and Headers. APIKeyEnv
// is normalized to a bare variable name. Returns a copy.
func ResolveEndpoint(e ProviderEndpoint) ProviderEndpoint {
	base := strings.TrimSpace(e.BaseURL)
	if ContainsEnvRef(base) {
		base = strings.TrimSpace(ExpandEnv(base))
	}
	base = strings.TrimRight(base, "/")
	out := ProviderEndpoint{
		BaseURL:   base,
		APIKeyEnv: NormalizeAPIKeyEnv(e.APIKeyEnv),
	}
	if len(e.Headers) > 0 {
		h := make(map[string]string, len(e.Headers))
		for k, v := range e.Headers {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			h[k] = ExpandEnv(v)
		}
		out.Headers = h
	}
	return out
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
// Missing dir/files yield a zero ProvidersFile and nil error.
func loadProvidersFileLayer(dir string) (ProvidersFile, error) {
	for _, path := range providersFileCandidates(dir) {
		pf, err := ReadProvidersFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return ProvidersFile{}, fmt.Errorf("%s: %w", path, err)
		}
		return pf, nil
	}
	return ProvidersFile{}, nil
}

// ReadProvidersFile parses an OpenCode-style providers map or a JSON array of
// CustomProvider objects. Supports JSONC (// and /* */ comments).
func ReadProvidersFile(path string) (ProvidersFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProvidersFile{}, err
	}
	return ParseProvidersFile(data)
}

// ParseProvidersFile decodes providers.jsonc/json bytes.
func ParseProvidersFile(data []byte) (ProvidersFile, error) {
	stripped, err := stripJSONC(data)
	if err != nil {
		return ProvidersFile{}, err
	}
	stripped = bytesTrimSpace(stripped)
	if len(stripped) == 0 {
		return ProvidersFile{}, nil
	}
	switch stripped[0] {
	case '{':
		return parseProvidersMap(stripped)
	case '[':
		customs, err := parseProvidersArray(stripped)
		if err != nil {
			return ProvidersFile{}, err
		}
		return ProvidersFile{Customs: customs}, nil
	default:
		return ProvidersFile{}, fmt.Errorf("providers file must be a JSON object or array")
	}
}

func parseProvidersMap(data []byte) (ProvidersFile, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return ProvidersFile{}, err
	}
	// Stable order: sort keys for deterministic merge/tests.
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out ProvidersFile
	for _, key := range keys {
		// Nested "providers" key with array — rare wrap form.
		if key == "providers" {
			arr, err := parseProvidersArray(raw[key])
			if err != nil {
				return ProvidersFile{}, fmt.Errorf("providers: %w", err)
			}
			out.Customs = append(out.Customs, arr...)
			continue
		}
		var entry providersFileEntry
		if err := json.Unmarshal(raw[key], &entry); err != nil {
			return ProvidersFile{}, fmt.Errorf("provider %q: %w", key, err)
		}
		id := strings.ToLower(strings.TrimSpace(key))
		if id == "" {
			return ProvidersFile{}, errors.New("empty provider id")
		}
		if _, builtin := BuiltinProviderNames[id]; builtin {
			// Endpoint overlay (baseURL / apiKey / headers) — OpenCode-style
			// customization of a catalog-backed provider. echo has no HTTP
			// surface; ignore options on it.
			if id != "echo" {
				if ep := entry.toEndpoint(); ep.Active() {
					if out.Endpoints == nil {
						out.Endpoints = make(map[string]ProviderEndpoint)
					}
					out.Endpoints[id] = mergeEndpoint(out.Endpoints[id], ep)
				}
			}
			if len(entry.Models.defs) > 0 {
				if out.Overlays == nil {
					out.Overlays = make(map[string][]ModelDef)
				}
				out.Overlays[id] = mergeModelDefs(out.Overlays[id], entry.Models.defs)
			}
			continue
		}
		p, err := entry.toCustom(key)
		if err != nil {
			return ProvidersFile{}, fmt.Errorf("provider %q: %w", key, err)
		}
		out.Customs = append(out.Customs, p)
	}
	return out, nil
}

func parseProvidersArray(data []byte) ([]CustomProvider, error) {
	var arr []CustomProvider
	if err := json.Unmarshal(data, &arr); err != nil {
		return nil, err
	}
	out := make([]CustomProvider, 0, len(arr))
	for i, p := range arr {
		p = NormalizeCustomProvider(p)
		if name, ok := EnvRefName(p.APIKeyEnv); ok {
			p.APIKeyEnv = name
		} else {
			p.APIKeyEnv = NormalizeAPIKeyEnv(p.APIKeyEnv)
		}
		if err := p.Validate(); err != nil {
			return nil, fmt.Errorf("providers[%d] %q: %w", i, p.Name, err)
		}
		out = append(out, p)
	}
	return out, nil
}

// toEndpoint extracts options/flat baseURL+apiKey+headers for a builtin overlay.
func (e providersFileEntry) toEndpoint() ProviderEndpoint {
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
	ep := ProviderEndpoint{
		BaseURL:   baseURL,
		APIKeyEnv: apiKeyEnv,
	}
	if len(headers) > 0 {
		ep.Headers = headers
	}
	return ep
}

func (e providersFileEntry) toCustom(key string) (CustomProvider, error) {
	id := strings.ToLower(strings.TrimSpace(key))
	if id == "" {
		return CustomProvider{}, errors.New("empty provider id")
	}
	ep := e.toEndpoint()
	api := WireAPI(strings.ToLower(strings.TrimSpace(e.API)))
	if api == "" {
		api = wireFromNPM(e.NPM)
	}
	p := NormalizeCustomProvider(CustomProvider{
		Name:      id,
		BaseURL:   ep.BaseURL,
		API:       api,
		Headers:   ep.Headers,
		APIKeyEnv: ep.APIKeyEnv,
		ModelDefs: e.Models.defs,
	})
	if err := p.Validate(); err != nil {
		return CustomProvider{}, err
	}
	return p, nil
}

// mergeEndpoint overlays layer onto base; non-empty layer fields win.
func mergeEndpoint(base, layer ProviderEndpoint) ProviderEndpoint {
	out := base
	if u := strings.TrimSpace(layer.BaseURL); u != "" {
		out.BaseURL = u
	}
	if k := strings.TrimSpace(layer.APIKeyEnv); k != "" {
		out.APIKeyEnv = k
	}
	if len(layer.Headers) > 0 {
		if out.Headers == nil {
			out.Headers = make(map[string]string, len(layer.Headers))
		} else {
			h := make(map[string]string, len(out.Headers)+len(layer.Headers))
			for k, v := range out.Headers {
				h[k] = v
			}
			out.Headers = h
		}
		for k, v := range layer.Headers {
			out.Headers[k] = v
		}
	}
	return out
}

// mergeEndpointMaps merges provider→endpoint maps; later layer wins per field.
func mergeEndpointMaps(base, layer map[string]ProviderEndpoint) map[string]ProviderEndpoint {
	if len(layer) == 0 {
		return cloneEndpointMap(base)
	}
	out := cloneEndpointMap(base)
	if out == nil {
		out = make(map[string]ProviderEndpoint, len(layer))
	}
	for prov, ep := range layer {
		out[prov] = mergeEndpoint(out[prov], ep)
	}
	return out
}

func cloneEndpointMap(in map[string]ProviderEndpoint) map[string]ProviderEndpoint {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]ProviderEndpoint, len(in))
	for k, v := range in {
		out[k] = cloneEndpoint(v)
	}
	return out
}

func cloneEndpoint(e ProviderEndpoint) ProviderEndpoint {
	out := ProviderEndpoint{
		BaseURL:   e.BaseURL,
		APIKeyEnv: e.APIKeyEnv,
	}
	if len(e.Headers) > 0 {
		out.Headers = make(map[string]string, len(e.Headers))
		for k, v := range e.Headers {
			out.Headers[k] = v
		}
	}
	return out
}

// wireFromNPM maps optional npm package hints to a wire dialect (OpenCode parity).
// Packages are never installed — the string only selects the HTTP shape:
//
//   - name contains "anthropic" → Messages API
//   - @ai-sdk/openai (exact / …/openai, not openai-compatible) → Responses API
//   - @ai-sdk/openai-compatible and everything else → chat completions
func wireFromNPM(npm string) WireAPI {
	n := strings.ToLower(strings.TrimSpace(npm))
	if n == "" {
		return WireOpenAI
	}
	if strings.Contains(n, "anthropic") {
		return WireAnthropic
	}
	// @ai-sdk/openai default languageModel is Responses (/v1/responses).
	// openai-compatible stays chat-completions.
	if n == "@ai-sdk/openai" || strings.HasSuffix(n, "/openai") {
		return WireResponses
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
