// Package auth is the Strike product credential store (0600 ~/.strike/auth.json)
// plus compatibility forwards for reusable OAuth/PKCE/device flows that now
// live in github.com/jonathanung/strike-cli/providers/auth.
//
// Used by cmd/strike (the `strike auth` subcommand) and wrapped as host.Auth
// by internal/frontend/host/local; internal/frontend/tui never imports it — credentials never
// reach the frontend, only OAuthLogin/DeviceLogin handles and outcome strings do.
package auth

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

type Store struct {
	path  string
	mu    sync.Mutex
	creds map[string]Credential
}

// DefaultPath is ~/.strike/auth.json — ~/.strike is strike's home for all
// user-level state. Existing ~/.strike directory symlinks are resolved.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "auth.json")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "auth.json")
}

func OpenStore(path string) (*Store, error) {
	s := &Store{path: path, creds: map[string]Credential{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &s.creds); err != nil {
		return nil, err
	}
	return s, nil
}

// canonicalProvider maps shipped aliases onto the storage/list id.
// Kept private so auth never imports config; must stay aligned with
// config.CanonicalProviderID (gemini → google).
func canonicalProvider(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "gemini" {
		return "google"
	}
	return provider
}

func (s *Store) Get(provider string) (Credential, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getLocked(provider)
}

func (s *Store) getLocked(provider string) (Credential, bool) {
	if canonicalProvider(provider) == "google" {
		// Prefer the canonical key when both legacy gemini and google exist.
		if c, ok := s.creds["google"]; ok {
			return c, true
		}
		if c, ok := s.creds["gemini"]; ok {
			return c, true
		}
		return Credential{}, false
	}
	c, ok := s.creds[provider]
	return c, ok
}

func (s *Store) Set(provider string, c Credential) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if canonicalProvider(provider) == "google" {
		s.creds["google"] = c
		delete(s.creds, "gemini")
		return s.saveLocked()
	}
	s.creds[provider] = c
	return s.saveLocked()
}

func (s *Store) Delete(provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if canonicalProvider(provider) == "google" {
		delete(s.creds, "google")
		delete(s.creds, "gemini")
		return s.saveLocked()
	}
	delete(s.creds, provider)
	return s.saveLocked()
}

func (s *Store) Providers() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[string]struct{}, len(s.creds))
	out := make([]string, 0, len(s.creds))
	for p := range s.creds {
		name := p
		if p == "gemini" {
			name = "google"
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}
