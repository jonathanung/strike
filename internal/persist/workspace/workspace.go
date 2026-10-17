// Package workspace is the durable registry of workspace directories the
// daemon may open. The web UI selects workspaces by id; paths never leave
// the daemon host except in API responses to its authenticated owner.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Workspace is one registered directory.
type Workspace struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	AddedAt    time.Time `json:"addedAt"`
	LastOpened time.Time `json:"lastOpened,omitempty"`
}

type state struct {
	Workspaces []Workspace `json:"workspaces"`
	ActiveID   string      `json:"activeId,omitempty"`
}

// Store persists the registry as JSON at <dir>/workspaces.json.
type Store struct {
	mu   sync.Mutex
	path string
	st   state
	now  func() time.Time
}

// DefaultDir returns ~/.strike, the daemon state directory.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".strike"
	}
	return filepath.Join(home, ".strike")
}

// Open loads the registry in dir (created on first write).
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultDir()
	}
	s := &Store{path: filepath.Join(dir, "workspaces.json"), now: time.Now}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("workspace: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, fmt.Errorf("workspace: parse %s: %w", s.path, err)
	}
	return s, nil
}

// IDForPath derives the stable workspace id for a canonical path.
func IDForPath(path string) string {
	sum := sha256.Sum256([]byte(path))
	return hex.EncodeToString(sum[:6])
}

// List returns workspaces sorted by name, plus the active id.
func (s *Store) List() ([]Workspace, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]Workspace(nil), s.st.Workspaces...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	if out == nil {
		out = []Workspace{}
	}
	return out, s.st.ActiveID
}

// Add registers dir (must exist and be a directory). Re-adding returns the
// existing entry.
func (s *Store) Add(dir, name string) (Workspace, error) {
	abs, err := filepath.Abs(strings.TrimSpace(dir))
	if err != nil {
		return Workspace{}, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	info, err := os.Stat(abs)
	if err != nil {
		return Workspace{}, fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return Workspace{}, fmt.Errorf("workspace: %s is not a directory", abs)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := IDForPath(abs)
	for _, w := range s.st.Workspaces {
		if w.ID == id {
			return w, nil
		}
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(abs)
	}
	w := Workspace{ID: id, Name: name, Path: abs, AddedAt: s.now().UTC()}
	s.st.Workspaces = append(s.st.Workspaces, w)
	if s.st.ActiveID == "" {
		s.st.ActiveID = id
	}
	if err := s.saveLocked(); err != nil {
		return Workspace{}, err
	}
	return w, nil
}

// Activate marks id active and stamps LastOpened.
func (s *Store) Activate(id string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Workspaces {
		if s.st.Workspaces[i].ID == id {
			s.st.ActiveID = id
			s.st.Workspaces[i].LastOpened = s.now().UTC()
			if err := s.saveLocked(); err != nil {
				return Workspace{}, err
			}
			return s.st.Workspaces[i], nil
		}
	}
	return Workspace{}, fmt.Errorf("workspace: %q not found", id)
}

// Remove deletes id. Missing ids are an error.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Workspaces {
		if s.st.Workspaces[i].ID == id {
			s.st.Workspaces = append(s.st.Workspaces[:i], s.st.Workspaces[i+1:]...)
			if s.st.ActiveID == id {
				s.st.ActiveID = ""
			}
			return s.saveLocked()
		}
	}
	return fmt.Errorf("workspace: %q not found", id)
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
