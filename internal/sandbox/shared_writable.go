package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Shared writable roots let bash operate normally under workspace-write:
// compilers and package managers need scratch + cache dirs outside the
// session worktree. Temp roots apply in any non-off mode (parity with
// macOS seatbelt); cache roots apply only when the workspace is writable.

// IsSharedWritablePath reports whether abs is a safe device node or lies
// under a well-known temp/cache root. Used by the bash static path guard
// and documented in /sandbox explain. Critical system paths (/, /tmp as a
// whole, $HOME, …) are still refused by the caller's dangerous-path check.
func IsSharedWritablePath(abs string) bool {
	abs = cleanAbsPath(abs)
	if abs == "" {
		return false
	}
	if isSafeDevicePath(abs) {
		return true
	}
	for _, root := range sharedWritableRootCandidates(true) {
		if pathUnderRoot(root, abs) {
			return true
		}
	}
	return false
}

// SharedWritablePaths returns existing absolute directories to bind writable
// in the OS sandbox. includeCaches adds user/tool cache roots (workspace-write);
// temp roots are always included when present on the host.
// workDir is omitted when a candidate is inside the workspace (already bound).
func SharedWritablePaths(workDir string, includeCaches bool) []string {
	wd := absWorkDir(workDir)
	seen := map[string]struct{}{}
	var out []string
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		abs := cleanAbsPath(raw)
		if abs == "" {
			return
		}
		// Prefer real path for bwrap bind matching.
		if real, err := filepath.EvalSymlinks(abs); err == nil && real != "" {
			abs = real
		}
		if _, ok := seen[abs]; ok {
			return
		}
		st, err := os.Stat(abs)
		if err != nil || st == nil || !st.IsDir() {
			return
		}
		if wd != "" && pathUnderRoot(wd, abs) {
			// Already covered by the workspace bind (or is the workspace).
			return
		}
		// Never bind the filesystem root.
		if abs == "/" || abs == filepath.Clean("/") {
			return
		}
		seen[abs] = struct{}{}
		out = append(out, abs)
	}
	for _, root := range sharedWritableRootCandidates(includeCaches) {
		add(root)
	}
	return out
}

func sharedWritableRootCandidates(includeCaches bool) []string {
	var roots []string
	// Temp / scratch — always candidates.
	roots = append(roots, "/tmp", "/var/tmp")
	if runtime.GOOS == "darwin" {
		roots = append(roots, "/private/tmp", "/private/var/tmp")
	}
	if td := strings.TrimSpace(os.Getenv("TMPDIR")); td != "" {
		roots = append(roots, td)
	}
	if runtime.GOOS == "linux" {
		roots = append(roots, "/dev/shm")
	}
	if !includeCaches {
		return roots
	}

	home := ""
	if h, err := os.UserHomeDir(); err == nil {
		home = h
	}
	if home == "" {
		home = os.Getenv("HOME")
	}

	if xdg := strings.TrimSpace(os.Getenv("XDG_CACHE_HOME")); xdg != "" {
		roots = append(roots, xdg)
	} else if home != "" {
		roots = append(roots, filepath.Join(home, ".cache"))
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); xdg != "" {
		roots = append(roots, xdg)
	}

	if g := strings.TrimSpace(os.Getenv("GOCACHE")); g != "" && !strings.EqualFold(g, "off") {
		roots = append(roots, g)
	}
	if g := strings.TrimSpace(os.Getenv("GOMODCACHE")); g != "" {
		roots = append(roots, g)
	} else if gp := firstPATHListEntry(os.Getenv("GOPATH")); gp != "" {
		roots = append(roots, filepath.Join(gp, "pkg", "mod"))
	} else if home != "" {
		roots = append(roots, filepath.Join(home, "go", "pkg", "mod"))
	}

	if home != "" {
		roots = append(roots,
			filepath.Join(home, ".npm"),
			filepath.Join(home, ".cargo"),
			filepath.Join(home, ".rustup"),
		)
	}
	return roots
}

func firstPATHListEntry(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	parts := filepath.SplitList(v)
	if len(parts) == 0 {
		return ""
	}
	return strings.TrimSpace(parts[0])
}

func isSafeDevicePath(abs string) bool {
	switch abs {
	case "/dev/null", "/dev/zero", "/dev/full",
		"/dev/stdout", "/dev/stderr", "/dev/tty",
		"/dev/stdin":
		return true
	}
	// /dev/fd/N — shell fd redirections
	if strings.HasPrefix(abs, "/dev/fd/") {
		rest := abs[len("/dev/fd/"):]
		if rest == "" {
			return false
		}
		for _, r := range rest {
			if r < '0' || r > '9' {
				return false
			}
		}
		return true
	}
	return false
}

func cleanAbsPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if !filepath.IsAbs(p) {
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
	}
	return filepath.Clean(p)
}

// absWorkDir returns the physical absolute workspace root for binds/profiles.
func absWorkDir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}
	clean := filepath.Clean(workDir)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return clean
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != "" {
		return real
	}
	return abs
}

func pathUnderRoot(root, path string) bool {
	root = filepath.Clean(root)
	path = filepath.Clean(path)
	if root == "" || path == "" {
		return false
	}
	if path == root {
		return true
	}
	sep := string(filepath.Separator)
	prefix := root
	if !strings.HasSuffix(prefix, sep) {
		prefix += sep
	}
	return strings.HasPrefix(path, prefix)
}
