package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// WireAPI is the HTTP dialect a custom provider endpoint speaks. It is not a
// brand name — "openai" means chat-completions; "anthropic" means messages.
type WireAPI string

const (
	WireOpenAI    WireAPI = "openai"
	WireAnthropic WireAPI = "anthropic"
)

// BuiltinProviderNames cannot be used as custom provider ids.
var BuiltinProviderNames = map[string]struct{}{
	"anthropic": {},
	"openai":    {},
	"xai":       {},
	"gemini":    {},
	"kimi":      {},
	"deepseek":  {},
	"echo":      {},
}

// providerNameRE is a stable slug: letter start, then letters/digits/_/-.
var providerNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// CustomProvider is a user-declared LLM endpoint identified by Name.
// Credentials never live here — only in the auth store (or apiKeyEnv).
//
// Models is the legacy flat id list (also the JSON field for config.providers).
// ModelDefs holds rich nested defs from providers.jsonc; when empty, Normalize
// synthesizes bare defs from Models. When ModelDefs is set, Models is kept in
// sync as the id list for callers that only need ids.
type CustomProvider struct {
	Name      string            `json:"name"`
	BaseURL   string            `json:"baseURL"`
	API       WireAPI           `json:"api"`
	Headers   map[string]string `json:"headers,omitempty"`
	APIKeyEnv string            `json:"apiKeyEnv,omitempty"`
	Models    []string          `json:"models,omitempty"`
	ModelDefs []ModelDef        `json:"-"`
}

// Validate checks a custom provider definition before persistence or use.
func (p CustomProvider) Validate() error {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		return errors.New("provider name is required")
	}
	if !providerNameRE.MatchString(name) {
		return fmt.Errorf("provider name %q must be a lowercase slug (letter, then letters/digits/_/-)", name)
	}
	if _, builtin := BuiltinProviderNames[name]; builtin {
		return fmt.Errorf("provider name %q is reserved for a built-in provider", name)
	}
	if err := ValidateWireAPI(p.API); err != nil {
		return err
	}
	if err := ValidateBaseURL(p.BaseURL); err != nil {
		return err
	}
	if p.APIKeyEnv != "" {
		envName := NormalizeAPIKeyEnv(p.APIKeyEnv)
		if envName == "" || strings.ContainsAny(envName, " \t\n=") || !isEnvIdent(envName) {
			return fmt.Errorf("apiKeyEnv %q is not a valid environment variable name", p.APIKeyEnv)
		}
	}
	for _, m := range p.Models {
		if strings.TrimSpace(m) == "" {
			return errors.New("models must not contain empty ids")
		}
	}
	for _, d := range p.ModelDefs {
		if err := d.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ValidateWireAPI accepts known wire dialects.
func ValidateWireAPI(api WireAPI) error {
	switch api {
	case WireOpenAI, WireAnthropic:
		return nil
	case "":
		return errors.New("api (wire dialect) is required: openai or anthropic")
	default:
		return fmt.Errorf("unknown api %q (want openai or anthropic)", api)
	}
}

// ValidateBaseURL requires an absolute http(s) URL with a host, or a string
// that contains env placeholders ({env:NAME}, $NAME, ${NAME}) resolved later.
func ValidateBaseURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return errors.New("baseURL is required")
	}
	if ContainsEnvRef(raw) {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("baseURL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("baseURL must be http or https, got %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("baseURL must include a host")
	}
	return nil
}

// ResolveCustom expands env placeholders in BaseURL and Headers for runtime use.
// APIKeyEnv is normalized to a bare variable name. The returned value is a copy.
func ResolveCustom(p CustomProvider) CustomProvider {
	p = NormalizeCustomProvider(p)
	p.BaseURL = strings.TrimRight(strings.TrimSpace(ExpandEnv(p.BaseURL)), "/")
	p.APIKeyEnv = NormalizeAPIKeyEnv(p.APIKeyEnv)
	if len(p.Headers) > 0 {
		out := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			out[k] = ExpandEnv(v)
		}
		p.Headers = out
	}
	return p
}

// NormalizeCustomProvider trims fields and lowercases the name/api.
// BaseURL is not env-expanded here (kept as template until ResolveCustom).
func NormalizeCustomProvider(p CustomProvider) CustomProvider {
	p.Name = strings.ToLower(strings.TrimSpace(p.Name))
	// Only strip trailing slash on concrete URLs; keep templates intact.
	base := strings.TrimSpace(p.BaseURL)
	if !ContainsEnvRef(base) {
		base = strings.TrimRight(base, "/")
	}
	p.BaseURL = base
	p.API = WireAPI(strings.ToLower(strings.TrimSpace(string(p.API))))
	p.APIKeyEnv = NormalizeAPIKeyEnv(p.APIKeyEnv)
	if len(p.Headers) > 0 {
		out := make(map[string]string, len(p.Headers))
		for k, v := range p.Headers {
			k = strings.TrimSpace(k)
			if k == "" {
				continue
			}
			out[k] = v
		}
		p.Headers = out
	}
	// Prefer rich ModelDefs when present; otherwise promote Models ids.
	if len(p.ModelDefs) > 0 {
		defs := make([]ModelDef, 0, len(p.ModelDefs))
		seen := map[string]struct{}{}
		ids := make([]string, 0, len(p.ModelDefs))
		for _, d := range p.ModelDefs {
			d.ID = strings.TrimSpace(d.ID)
			if d.ID == "" {
				continue
			}
			if _, ok := seen[d.ID]; ok {
				continue
			}
			seen[d.ID] = struct{}{}
			d.Name = strings.TrimSpace(d.Name)
			defs = append(defs, d)
			ids = append(ids, d.ID)
		}
		p.ModelDefs = defs
		p.Models = ids
	} else if len(p.Models) > 0 {
		models := make([]string, 0, len(p.Models))
		seen := map[string]struct{}{}
		for _, m := range p.Models {
			m = strings.TrimSpace(m)
			if m == "" {
				continue
			}
			if _, ok := seen[m]; ok {
				continue
			}
			seen[m] = struct{}{}
			models = append(models, m)
		}
		p.Models = models
		defs := make([]ModelDef, len(models))
		for i, id := range models {
			defs[i] = ModelDef{ID: id}
		}
		p.ModelDefs = defs
	}
	return p
}

// FindCustom returns the custom provider with the given name, if any.
func FindCustom(list []CustomProvider, name string) (CustomProvider, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, p := range list {
		if p.Name == name {
			return p, true
		}
	}
	return CustomProvider{}, false
}

// mergeProviders concatenates layers; later entries with the same name replace
// earlier ones. Order is first-seen with updates applied in place.
func mergeProviders(base, layer []CustomProvider) []CustomProvider {
	if len(layer) == 0 {
		return base
	}
	out := slices.Clone(base)
	index := make(map[string]int, len(out))
	for i, p := range out {
		index[p.Name] = i
	}
	for _, p := range layer {
		p = NormalizeCustomProvider(p)
		if p.Name == "" {
			continue
		}
		if i, ok := index[p.Name]; ok {
			out[i] = p
			continue
		}
		index[p.Name] = len(out)
		out = append(out, p)
	}
	return out
}

// CustomStore is a thread-safe view of custom providers persisted across
// ~/.strike/config, optional providers.jsonc layers, and project overrides.
// Upsert writes the global config providers array; Remove drops the name from
// memory and from every known persistence layer (config + providers.jsonc).
//
// ModelOverlays holds providers.jsonc model refinements for built-in (and
// catalog-backed) providers — they never become CustomProvider rows.
type CustomStore struct {
	mu       sync.Mutex
	items    []CustomProvider
	overlays map[string][]ModelDef
	workDir  string
}

// NewCustomStore seeds a store from a merged provider list (e.g. config.Load).
// workDir scopes project-layer removals (./.strike/…); empty skips project files.
func NewCustomStore(items []CustomProvider, workDir string) *CustomStore {
	return NewCustomStoreWithOverlays(items, nil, workDir)
}

// NewCustomStoreWithOverlays seeds customs plus builtin model overlays.
func NewCustomStoreWithOverlays(items []CustomProvider, overlays map[string][]ModelDef, workDir string) *CustomStore {
	out := make([]CustomProvider, len(items))
	copy(out, items)
	return &CustomStore{items: out, overlays: cloneOverlayMap(overlays), workDir: workDir}
}

// ModelOverlay returns config model defs for provider (builtin overlays or
// custom provider ModelDefs). Caller must not mutate the returned slice.
func (s *CustomStore) ModelOverlay(provider string) []ModelDef {
	s.mu.Lock()
	defer s.mu.Unlock()
	provider = strings.ToLower(strings.TrimSpace(provider))
	if defs := s.overlays[provider]; len(defs) > 0 {
		return cloneModelDefs(defs)
	}
	if cp, ok := FindCustom(s.items, provider); ok {
		return cloneModelDefs(cp.ModelDefs)
	}
	return nil
}

// List returns a snapshot of custom providers in stable order.
func (s *CustomStore) List() []CustomProvider {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]CustomProvider, len(s.items))
	copy(out, s.items)
	return out
}

// Get looks up one custom provider by name.
func (s *CustomStore) Get(name string) (CustomProvider, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return FindCustom(s.items, name)
}

// Upsert validates and inserts or replaces a custom provider, then persists
// the full list to the global config file.
func (s *CustomStore) Upsert(p CustomProvider) error {
	p = NormalizeCustomProvider(p)
	if err := p.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for i, existing := range s.items {
		if existing.Name == p.Name {
			s.items[i] = p
			found = true
			break
		}
	}
	if !found {
		s.items = append(s.items, p)
	}
	return saveGlobalProviders(s.items)
}

// Remove deletes a custom provider by name from memory and every persistence
// layer (global/project config providers arrays and providers.jsonc files).
// Missing names are OK.
func (s *CustomStore) Remove(name string) error {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return errors.New("provider name is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.items[:0]
	for _, p := range s.items {
		if p.Name != name {
			out = append(out, p)
		}
	}
	s.items = slices.Clone(out)
	workDir := s.workDir
	// Drop from each layer independently so a providers.jsonc-only entry does
	// not reappear on next Load, and config-only entries stay off disk too.
	if err := removeProviderFromConfigFile(GlobalPath(), name); err != nil {
		return err
	}
	if root := GlobalRoot(); root != "" {
		for _, path := range providersFileCandidates(root) {
			if err := removeProviderFromProvidersFile(path, name); err != nil {
				return err
			}
		}
	}
	if workDir != "" {
		if err := removeProviderFromConfigFile(ProjectPath(workDir), name); err != nil {
			return err
		}
		for _, path := range providersFileCandidates(projectRoot(workDir)) {
			if err := removeProviderFromProvidersFile(path, name); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveGlobalProviders rewrites the providers array in ~/.strike/config while
// preserving unrelated fields.
func saveGlobalProviders(items []CustomProvider) error {
	path := GlobalPath()
	if path == "" {
		return fmt.Errorf("cannot locate home directory")
	}
	var cfg Config
	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// fresh
	case err != nil:
		return err
	default:
		if err := json.Unmarshal(data, &cfg); err != nil {
			return fmt.Errorf("existing %s is not valid JSON (%v) — fix it before saving providers", path, err)
		}
	}
	cfg.Providers = items
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(out, '\n'), 0o644)
}
