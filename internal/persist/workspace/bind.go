package workspace

import (
	"os"
	"path/filepath"
	"strings"
)

// SessionBinding is the directory, rules block, and child-cwd roots a root
// session should use. Bound is false when the registry is empty, the store
// is nil, or the recorded workspace is gone: the engine stays on Dir (the
// process directory) and Roots is empty, so child cwd checks stay off.
type SessionBinding struct {
	ID             string
	Dir            string
	Root           string
	Bound          bool
	RulesBlock     string
	RulesPath      string
	PermissionMode string
	Roots          []string
}

// ForNew chooses the workspace for a session that does not exist yet.
// preferActive uses the registry's active workspace (web-created roots).
// Otherwise a launch directory inside a registered workspace records that
// id but keeps Dir at the launch directory. An empty registry returns an
// unbound binding so plain strike serve is unchanged.
func ForNew(s *Store, fallback string, preferActive bool) SessionBinding {
	fallback = canonicalDir(fallback)
	unbound := SessionBinding{Dir: fallback}
	if s == nil {
		return unbound
	}
	if preferActive {
		_, active := s.List()
		if ws, ok := s.Get(active); ok && dirExists(ws.Path) {
			return bindWorkspace(ws, canonicalDir(ws.Path))
		}
	}
	if ws, ok := matchContaining(s, fallback); ok {
		return bindWorkspace(ws, fallback)
	}
	return unbound
}

// ForResume binds the workspace id stored on the session. A missing id or
// a workspace whose directory is gone falls back to the process directory
// with no child-cwd roots, so resume still opens.
func ForResume(s *Store, id, fallback string) SessionBinding {
	fallback = canonicalDir(fallback)
	unbound := SessionBinding{Dir: fallback}
	if s == nil || strings.TrimSpace(id) == "" {
		return unbound
	}
	ws, ok := s.Get(strings.TrimSpace(id))
	if !ok || !dirExists(ws.Path) {
		return unbound
	}
	return bindWorkspace(ws, canonicalDir(ws.Path))
}

// InstructionCovers reports whether blocks already include the rules file.
func InstructionCovers(blocks []string, path string) bool {
	path = strings.TrimSpace(path)
	if path == "" {
		return false
	}
	prefix := "Instructions from: " + path
	for _, b := range blocks {
		if b == prefix || strings.HasPrefix(b, prefix+"\n") {
			return true
		}
	}
	return false
}

func bindWorkspace(ws Workspace, dir string) SessionBinding {
	root := canonicalDir(ws.Path)
	if dir == "" {
		dir = root
	}
	roots := []string{root}
	for _, extra := range ws.ExtraRoots {
		c := canonicalDir(extra)
		if c == "" || c == root {
			continue
		}
		roots = append(roots, c)
	}
	block, rulesPath := rulesBlock(root, ws.Rules)
	return SessionBinding{
		ID:             ws.ID,
		Dir:            dir,
		Root:           root,
		Bound:          true,
		RulesBlock:     block,
		RulesPath:      rulesPath,
		PermissionMode: strings.TrimSpace(ws.PermissionMode),
		Roots:          roots,
	}
}

func matchContaining(s *Store, dir string) (Workspace, bool) {
	if dir == "" {
		return Workspace{}, false
	}
	list, _ := s.List()
	var best Workspace
	bestLen := -1
	for _, w := range list {
		root := canonicalDir(w.Path)
		if root == "" || !dirExists(root) || !withinDir(root, dir) {
			continue
		}
		if len(root) > bestLen {
			best = w
			bestLen = len(root)
		}
	}
	if bestLen < 0 {
		return Workspace{}, false
	}
	return best, true
}

func rulesBlock(root, rules string) (block, path string) {
	resolved, err := rulesFileInside(root, rules)
	if err != nil || resolved == "" {
		return "", ""
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return "", ""
	}
	body := strings.TrimSpace(string(data))
	if body == "" {
		return "", ""
	}
	const max = 64 * 1024
	if len(body) > max {
		body = body[:max] + "\n[truncated]"
	}
	return "Instructions from: " + resolved + "\n" + body, resolved
}

func canonicalDir(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return abs
}

func dirExists(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}

func withinDir(root, target string) bool {
	root = filepath.Clean(root)
	target = filepath.Clean(target)
	if root == "" || target == "" {
		return false
	}
	if root == target {
		return true
	}
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
