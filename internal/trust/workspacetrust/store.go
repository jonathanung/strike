// Package workspacetrust persists per-workspace trust decisions outside the
// repository (default ~/.strike/workspace-trust.json, 0600).
//
// A workspace is untrusted until the user trusts it. While untrusted, every
// repo-supplied configuration surface stays inert: project .strike/config
// scalars and permission rules, mcp.jsonc servers, LSP servers, harnesses,
// hooks, agents, skills, workflows, project plugins, and AGENTS.md / CLAUDE.md
// steering. The session still runs on builtin + global config. Trust only
// lets that configuration load — it never widens a permission, pre-approves a
// command, or bypasses the per-command hook Ask (#1352).
//
// The store is keyed by canonical workspace root (the git top level when
// known) and lives under the user's global root so a repository can never
// change its own trust status.
package workspacetrust

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// FileName is the trust store file under the global root.
const FileName = "workspace-trust.json"

// fileVersion is the on-disk schema version.
const fileVersion = 1

// ErrUntrustable is returned by Trust for roots that must never be trusted:
// the user's home directory and the filesystem root. Trusting those would make
// every repository beneath them trusted by location alone.
var ErrUntrustable = errors.New("workspace root cannot be trusted")

// Record is one durable trust grant.
type Record struct {
	// TrustedAt is RFC3339 UTC.
	TrustedAt string `json:"trustedAt"`
}

type file struct {
	Version    int               `json:"version"`
	Workspaces map[string]Record `json:"workspaces"`
}

// Store is a process-wide trust store over one JSON file. All methods are
// safe for concurrent use.
type Store struct {
	mu   sync.Mutex
	path string
	now  func() time.Time
	data file
}

// DefaultPath returns <globalRoot>/workspace-trust.json ("" when globalRoot
// is empty).
func DefaultPath(globalRoot string) string {
	if globalRoot == "" {
		return ""
	}
	return filepath.Join(globalRoot, FileName)
}

// Open loads the store at path. A missing file is an empty store; a malformed
// or newer-version file is an error (fail closed rather than silently
// trusting nothing — callers should refuse to proceed with repo config).
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("workspace trust: empty store path")
	}
	s := &Store{path: path, now: time.Now, data: file{Version: fileVersion, Workspaces: map[string]Record{}}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace trust: %w", err)
	}
	var f file
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("workspace trust: %s: %w", path, err)
	}
	if f.Version > fileVersion {
		return nil, fmt.Errorf("workspace trust: %s: version %d is newer than supported %d", path, f.Version, fileVersion)
	}
	if f.Workspaces == nil {
		f.Workspaces = map[string]Record{}
	}
	f.Version = fileVersion
	s.data = f
	return s, nil
}

// Path returns the backing file path.
func (s *Store) Path() string { return s.path }

// CanonicalRoot returns the absolute, symlink-resolved form of root used as
// the store key. Missing paths resolve as far as they exist.
func CanonicalRoot(root string) (string, error) {
	if root == "" {
		return "", errors.New("workspace trust: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved), nil
	}
	return filepath.Clean(abs), nil
}

// Trusted reports whether root has a trust grant. Only the exact canonical
// root matches — trusting a parent directory does not trust its children.
func (s *Store) Trusted(root string) bool {
	key, err := CanonicalRoot(root)
	if err != nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.data.Workspaces[key]
	return ok
}

// Trust records a grant for root and persists it. Home and filesystem root
// return ErrUntrustable.
func (s *Store) Trust(root string) error {
	key, err := CanonicalRoot(root)
	if err != nil {
		return err
	}
	if untrustable(key) {
		return fmt.Errorf("%w: %s", ErrUntrustable, key)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Workspaces[key] = Record{TrustedAt: s.now().UTC().Format(time.RFC3339)}
	return s.saveLocked()
}

// Revoke removes any grant for root and persists. Revoking an untrusted root
// is a no-op.
func (s *Store) Revoke(root string) error {
	key, err := CanonicalRoot(root)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Workspaces[key]; !ok {
		return nil
	}
	delete(s.data.Workspaces, key)
	return s.saveLocked()
}

// Roots returns trusted roots in sorted order.
func (s *Store) Roots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.data.Workspaces))
	for k := range s.data.Workspaces {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Record returns the grant for root when present.
func (s *Store) Record(root string) (Record, bool) {
	key, err := CanonicalRoot(root)
	if err != nil {
		return Record{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.data.Workspaces[key]
	return r, ok
}

// Untrustable reports whether root can never accept a grant (the filesystem
// root or the user's home directory, after canonicalization).
func Untrustable(root string) bool {
	key, err := CanonicalRoot(root)
	if err != nil {
		return false
	}
	return untrustable(key)
}

// untrustable reports roots that never accept a grant: the filesystem root
// and the user's home directory (canonical form).
func untrustable(key string) bool {
	if key == filepath.Clean(string(filepath.Separator)) || filepath.Dir(key) == key {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	canonicalHome, err := CanonicalRoot(home)
	if err != nil {
		return false
	}
	return key == canonicalHome
}

// saveLocked writes the store atomically with 0600 permissions. The parent
// directory is created 0700 on first write.
func (s *Store) saveLocked() error {
	s.data.Version = fileVersion
	data, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("workspace trust: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".workspace-trust-*.tmp")
	if err != nil {
		return fmt.Errorf("workspace trust: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("workspace trust: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("workspace trust: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("workspace trust: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("workspace trust: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		cleanup()
		return fmt.Errorf("workspace trust: %w", err)
	}
	return nil
}
