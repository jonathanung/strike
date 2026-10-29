package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// BrowseEntry is one directory the picker may open.
type BrowseEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// BrowseResult is a directory listing constrained to the allowlist.
type BrowseResult struct {
	Path    string        `json:"path"`
	Parent  string        `json:"parent,omitempty"`
	Entries []BrowseEntry `json:"entries"`
}

// ConfigureBrowse sets the picker allowlist (typically the daemon user's
// home and the process working directory). Workspace paths and extra roots
// are always included as well. Calling this turns on the Add constraint.
func (s *Store) ConfigureBrowse(roots []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.browse = append([]string(nil), roots...)
	s.constrainBrowse = true
}

// BrowseRoots returns the canonical directories the picker may walk.
func (s *Store) BrowseRoots() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.browseRootsLocked()
}

// Browse lists directories. An empty path lists the allowlist itself.
// Symlinks that resolve outside the allowlist are omitted.
func (s *Store) Browse(path string) (BrowseResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	roots := s.browseRootsLocked()
	path = strings.TrimSpace(path)
	if strings.ContainsRune(path, 0) {
		return BrowseResult{}, fmt.Errorf("workspace: path is invalid")
	}
	if path == "" {
		entries := make([]BrowseEntry, 0, len(roots))
		for _, root := range roots {
			entries = append(entries, BrowseEntry{Name: filepath.Base(root), Path: root})
		}
		return BrowseResult{Entries: entries}, nil
	}
	resolved, err := resolveExistingDir(path)
	if err != nil {
		return BrowseResult{}, err
	}
	if !insideAny(roots, resolved) {
		return BrowseResult{}, fmt.Errorf("workspace: %s is outside the allowed roots", resolved)
	}
	dirEntries, err := os.ReadDir(resolved)
	if err != nil {
		return BrowseResult{}, fmt.Errorf("workspace: %w", err)
	}
	out := make([]BrowseEntry, 0, len(dirEntries))
	for _, e := range dirEntries {
		name := e.Name()
		if name == "." || name == ".." || strings.ContainsRune(name, 0) {
			continue
		}
		full := filepath.Join(resolved, name)
		target, err := filepath.EvalSymlinks(full)
		if err != nil {
			continue
		}
		info, err := os.Stat(target)
		if err != nil || !info.IsDir() {
			continue
		}
		if !insideAny(roots, target) {
			continue
		}
		out = append(out, BrowseEntry{Name: name, Path: target})
		if len(out) >= 200 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return BrowseResult{Path: resolved, Parent: parentInside(roots, resolved), Entries: out}, nil
}

// Mkdir creates one directory segment under parent. Parent must already be
// inside the allowlist. The name cannot contain a path separator.
func (s *Store) Mkdir(parent, name string) (string, error) {
	name = strings.TrimSpace(name)
	if err := validSegment(name); err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	roots := s.browseRootsLocked()
	parent = strings.TrimSpace(parent)
	if parent == "" {
		return "", fmt.Errorf("workspace: open a folder before creating one")
	}
	resolved, err := resolveExistingDir(parent)
	if err != nil {
		return "", err
	}
	if !insideAny(roots, resolved) {
		return "", fmt.Errorf("workspace: %s is outside the allowed roots", resolved)
	}
	dest := filepath.Join(resolved, name)
	if err := os.Mkdir(dest, 0o755); err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	created, err := filepath.EvalSymlinks(dest)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	if !insideAny(roots, created) {
		_ = os.Remove(dest)
		return "", fmt.Errorf("workspace: %s is outside the allowed roots", created)
	}
	return created, nil
}

// Recents returns up to n workspaces, most recently opened first.
func (s *Store) Recents(n int) []Workspace {
	if n <= 0 {
		n = 5
	}
	list, _ := s.List()
	sort.SliceStable(list, func(i, j int) bool {
		if !list[i].LastOpened.Equal(list[j].LastOpened) {
			return list[i].LastOpened.After(list[j].LastOpened)
		}
		return list[i].Name < list[j].Name
	})
	out := make([]Workspace, 0, n)
	for _, w := range list {
		if w.LastOpened.IsZero() {
			continue
		}
		out = append(out, w)
		if len(out) >= n {
			break
		}
	}
	return out
}

// Get returns one workspace by id.
func (s *Store) Get(id string) (Workspace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id = strings.TrimSpace(id)
	for _, w := range s.st.Workspaces {
		if w.ID == id {
			return w, true
		}
	}
	return Workspace{}, false
}

// Rename changes the display name. The directory path stays.
func (s *Store) Rename(id, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 80 || strings.ContainsAny(name, "/\\\n\r\x00") {
		return Workspace{}, fmt.Errorf("workspace: name must be 1–80 characters without path separators")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Workspaces {
		if s.st.Workspaces[i].ID != id {
			continue
		}
		s.st.Workspaces[i].Name = name
		if err := s.saveLocked(); err != nil {
			return Workspace{}, err
		}
		return s.st.Workspaces[i], nil
	}
	return Workspace{}, fmt.Errorf("workspace: %q not found", id)
}

// SetExtraRoots replaces the extra child working directories for id.
// An empty slice clears them. Each path must be an existing directory and
// must not be the filesystem root. Symlinks are stored resolved.
func (s *Store) SetExtraRoots(id string, roots []string) (Workspace, error) {
	ws, ok := s.Get(id)
	if !ok {
		return Workspace{}, fmt.Errorf("workspace: %q not found", id)
	}
	resolved, err := resolveExtraRoots(ws.Path, roots)
	if err != nil {
		return Workspace{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Workspaces {
		if s.st.Workspaces[i].ID != id {
			continue
		}
		s.st.Workspaces[i].ExtraRoots = resolved
		if err := s.saveLocked(); err != nil {
			return Workspace{}, err
		}
		return s.st.Workspaces[i], nil
	}
	return Workspace{}, fmt.Errorf("workspace: %q not found", id)
}

func resolveExtraRoots(workspacePath string, roots []string) ([]string, error) {
	if len(roots) == 0 {
		return nil, nil
	}
	base := canonicalDir(workspacePath)
	seen := map[string]struct{}{}
	out := make([]string, 0, len(roots))
	for _, raw := range roots {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if strings.ContainsRune(raw, 0) {
			return nil, fmt.Errorf("workspace: extra root is invalid")
		}
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, fmt.Errorf("workspace: extra root: %w", err)
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("workspace: extra root: %w", err)
		}
		info, err := os.Stat(resolved)
		if err != nil {
			return nil, fmt.Errorf("workspace: extra root: %w", err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("workspace: extra root %s is not a directory", resolved)
		}
		if resolved == string(filepath.Separator) {
			return nil, fmt.Errorf("workspace: extra root cannot be the filesystem root")
		}
		if resolved == base {
			continue
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		out = append(out, resolved)
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (s *Store) browseRootsLocked() []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(p string) {
		c := canonicalDir(p)
		if c == "" || !dirExists(c) {
			return
		}
		if _, ok := seen[c]; ok {
			return
		}
		seen[c] = struct{}{}
		out = append(out, c)
	}
	for _, r := range s.browse {
		add(r)
	}
	for _, w := range s.st.Workspaces {
		add(w.Path)
		for _, extra := range w.ExtraRoots {
			add(extra)
		}
	}
	sort.Strings(out)
	return out
}

func (s *Store) insideBrowseLocked(path string) bool {
	return insideAny(s.browseRootsLocked(), path)
}

func insideAny(roots []string, path string) bool {
	for _, root := range roots {
		if withinDir(root, path) {
			return true
		}
	}
	return false
}

func parentInside(roots []string, path string) string {
	for _, root := range roots {
		if path == root {
			return ""
		}
	}
	parent := filepath.Dir(path)
	if parent == path || !insideAny(roots, parent) {
		return ""
	}
	return parent
}

func resolveExistingDir(path string) (string, error) {
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("workspace: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("workspace: %s is not a directory", resolved)
	}
	return resolved, nil
}

func validSegment(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("workspace: folder name must be a single path segment")
	}
	return nil
}
