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

// Workspace is one registered directory. It is not a session folder: a
// session may record this id, and the engine for that session uses Path.
// Session folders, when a client has them, only organise chats.
//
// Rules is an optional project-rules file inside Path (absolute, after
// symlink resolution). PermissionMode is the starting posture for a new
// session in this workspace (default, plan, soft-approve, accept-edits,
// yolo). ExtraRoots are additional directories a delegated child may use
// as its working directory. The filesystem root is rejected.
type Workspace struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	AddedAt        time.Time `json:"addedAt"`
	LastOpened     time.Time `json:"lastOpened,omitempty"`
	Rules          string    `json:"rules,omitempty"`
	PermissionMode string    `json:"permissionMode,omitempty"`
	ExtraRoots     []string  `json:"extraRoots,omitempty"`
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
	// browse is the picker allowlist (home and the process directory).
	// Empty and constrainBrowse false keeps Add on "any real directory",
	// which is what tests and a store opened without ConfigureBrowse need.
	browse          []string
	constrainBrowse bool
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

// Path is the registry file. The handler that lists daemon files reports
// this path and does not read the document into the response.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
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
	if s.constrainBrowse && !s.insideBrowseLocked(abs) {
		return Workspace{}, fmt.Errorf("workspace: %s is outside the allowed roots", abs)
	}
	if strings.TrimSpace(name) == "" {
		name = filepath.Base(abs)
	}
	now := s.now().UTC()
	w := Workspace{ID: id, Name: name, Path: abs, AddedAt: now, LastOpened: now}
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

// SetPolicy updates the rules file and/or permission mode for id.
// A nil pointer leaves that field unchanged. An empty rules string clears
// the file. A non-empty rules path must be a regular file inside the
// workspace directory; relative paths join Path, and a symlink that resolves
// outside Path is rejected. permissionMode is stored as given (the HTTP
// layer checks it is a real mode). An empty permissionMode clears it.
func (s *Store) SetPolicy(id string, rules, mode *string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Workspaces {
		if s.st.Workspaces[i].ID != id {
			continue
		}
		if rules != nil {
			resolved, err := rulesFileInside(s.st.Workspaces[i].Path, *rules)
			if err != nil {
				return Workspace{}, err
			}
			s.st.Workspaces[i].Rules = resolved
		}
		if mode != nil {
			s.st.Workspaces[i].PermissionMode = strings.TrimSpace(*mode)
		}
		if err := s.saveLocked(); err != nil {
			return Workspace{}, err
		}
		return s.st.Workspaces[i], nil
	}
	return Workspace{}, fmt.Errorf("workspace: %q not found", id)
}

// rulesFileInside resolves rules against root. Empty rules clears the field.
func rulesFileInside(root, rules string) (string, error) {
	rules = strings.TrimSpace(rules)
	if rules == "" {
		return "", nil
	}
	var abs string
	if filepath.IsAbs(rules) {
		abs = filepath.Clean(rules)
	} else {
		abs = filepath.Clean(filepath.Join(root, rules))
	}
	if !pathInside(root, abs) {
		return "", fmt.Errorf("workspace: rules file must be inside %s", root)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: rules file: %w", err)
	}
	if !pathInside(root, resolved) {
		return "", fmt.Errorf("workspace: rules file escapes %s", root)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspace: rules file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("workspace: rules path is not a file")
	}
	return resolved, nil
}

func pathInside(root, target string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(target))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
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
