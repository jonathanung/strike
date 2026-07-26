package local

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/host"
)

const (
	maxFileBytes      = 1 << 20 // 1 MiB
	maxListEntries    = 2000
	maxSearchIndex    = 8000
	maxSearchResults  = 50
	searchCacheTTL    = 2 * time.Second
	scopedTruncateCap = 1 << 20 // same as max; notice when truncated at cap
)

// filesService reads workspace files for host.Files frontends.
type filesService struct {
	workDir string

	mu        sync.Mutex
	cache     []string
	cacheAt   time.Time
	cacheRoot string
}

// NewFiles returns a host.Files that resolves paths relative to workDir
// (absolute paths are cleaned as-is for ReadFile/ListDir). SearchFiles and
// ReadScoped reject paths that escape workDir via ".." or symlinks.
func NewFiles(workDir string) host.Files {
	return &filesService{workDir: workDir}
}

func (f *filesService) ReadFile(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("path is empty")
	}
	resolved := absPath(f.workDir, path)
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("file not found: %s", path)
		}
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("not a regular file: %s", path)
	}
	if info.Size() > maxFileBytes {
		return nil, fmt.Errorf("file exceeds 1MB limit")
	}
	file, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	// LimitReader guards TOCTOU growth between Stat and read.
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxFileBytes {
		return nil, fmt.Errorf("file exceeds 1MB limit")
	}
	return data, nil
}

func (f *filesService) ListDir(path string) ([]host.DirEntry, error) {
	path = strings.TrimSpace(path)
	var resolved string
	if path == "" {
		if f.workDir == "" {
			return nil, fmt.Errorf("path is empty")
		}
		resolved = filepath.Clean(f.workDir)
	} else {
		resolved = absPath(f.workDir, path)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			if path == "" {
				return nil, fmt.Errorf("directory not found: %s", resolved)
			}
			return nil, fmt.Errorf("directory not found: %s", path)
		}
		return nil, err
	}
	if !info.IsDir() {
		if path == "" {
			return nil, fmt.Errorf("not a directory: %s", resolved)
		}
		return nil, fmt.Errorf("not a directory: %s", path)
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return nil, err
	}
	out := make([]host.DirEntry, 0, len(entries))
	for _, e := range entries {
		// Skip the synthetic . and .. entries if present.
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		out = append(out, host.DirEntry{Name: name, IsDir: e.IsDir()})
		if len(out) >= maxListEntries {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

func (f *filesService) SearchFiles(query string, limit int) ([]string, error) {
	if f.workDir == "" {
		return nil, fmt.Errorf("work directory is empty")
	}
	if limit <= 0 || limit > maxSearchResults {
		limit = maxSearchResults
	}
	index, err := f.fileIndex()
	if err != nil {
		return nil, err
	}
	query = strings.ToLower(strings.TrimSpace(query))
	query = strings.ReplaceAll(query, "\\", "/")
	if query == "" {
		if len(index) > limit {
			return append([]string(nil), index[:limit]...), nil
		}
		return append([]string(nil), index...), nil
	}
	buckets := [3][]string{}
	for _, p := range index {
		lower := strings.ToLower(p)
		base := strings.ToLower(filepath.Base(p))
		rank := -1
		switch {
		case lower == query || base == query:
			rank = 0
		case strings.HasPrefix(lower, query) || strings.HasPrefix(base, query):
			rank = 1
		case orderedSubsequence(lower, query) || orderedSubsequence(base, query):
			rank = 2
		}
		if rank >= 0 {
			buckets[rank] = append(buckets[rank], p)
		}
	}
	out := make([]string, 0, limit)
	for _, b := range buckets {
		for _, p := range b {
			out = append(out, p)
			if len(out) >= limit {
				return out, nil
			}
		}
	}
	return out, nil
}

func (f *filesService) ReadScoped(path string) (host.FileContent, error) {
	display := strings.TrimSpace(path)
	display = strings.ReplaceAll(display, "\\", "/")
	display = strings.TrimPrefix(display, "./")
	if display == "" {
		return host.FileContent{Path: path, Skip: true, Notice: "empty path"}, nil
	}
	resolved, rel, err := resolveUnderRoot(f.workDir, display)
	if err != nil {
		return host.FileContent{Path: display, Skip: true, Notice: err.Error()}, nil
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return host.FileContent{Path: rel, Skip: true, Notice: "file not found"}, nil
		}
		return host.FileContent{Path: rel, Skip: true, Notice: err.Error()}, nil
	}
	// Reject symlink final targets that somehow slipped; resolveUnderRoot already
	// EvalSymlinks, so Lstat should be a regular file.
	if info.Mode()&os.ModeSymlink != 0 {
		return host.FileContent{Path: rel, Skip: true, Notice: "symlink not allowed"}, nil
	}
	if !info.Mode().IsRegular() {
		return host.FileContent{Path: rel, Skip: true, Notice: "not a regular file"}, nil
	}
	file, err := os.Open(resolved)
	if err != nil {
		return host.FileContent{Path: rel, Skip: true, Notice: err.Error()}, nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil {
		return host.FileContent{Path: rel, Skip: true, Notice: err.Error()}, nil
	}
	truncated := false
	if int64(len(data)) > maxFileBytes {
		data = data[:maxFileBytes]
		truncated = true
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return host.FileContent{
			Path:   rel,
			Skip:   true,
			Notice: "binary file skipped",
		}, nil
	}
	if !utf8.Valid(data) {
		return host.FileContent{
			Path:   rel,
			Skip:   true,
			Notice: "non-UTF-8 file skipped",
		}, nil
	}
	fc := host.FileContent{Path: rel, Content: string(data)}
	if truncated {
		fc.Notice = fmt.Sprintf("truncated to %d bytes", scopedTruncateCap)
	}
	return fc, nil
}

func (f *filesService) fileIndex() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	root := filepath.Clean(f.workDir)
	if len(f.cache) > 0 && f.cacheRoot == root && time.Since(f.cacheAt) < searchCacheTTL {
		return f.cache, nil
	}
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, fmt.Errorf("resolve work directory: %w", err)
	}
	var out []string
	err = filepath.WalkDir(rootReal, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Skip unreadable entries; keep indexing the rest.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != rootReal && shouldSkipDir(name) {
				return fs.SkipDir
			}
			// Do not descend through directory symlinks (escape risk).
			if path != rootReal {
				if info, err := d.Info(); err == nil && info.Mode()&os.ModeSymlink != 0 {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			// Skip symlink files in the index; ReadScoped would also reject
			// escapes, but listing only real files keeps the picker honest.
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			return nil
		}
		rel, err := filepath.Rel(rootReal, path)
		if err != nil || !isRelInside(rel) {
			return nil
		}
		out = append(out, filepath.ToSlash(rel))
		if len(out) >= maxSearchIndex {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(out)
	f.cache = out
	f.cacheAt = time.Now()
	f.cacheRoot = root
	return out, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", ".strike", "vendor", ".hg", ".svn",
		"dist", "build", "target", ".next", ".cache", "__pycache__":
		return true
	default:
		return false
	}
}

// resolveUnderRoot joins path under root and requires the final EvalSymlinks
// target to stay inside the physical root. Rejects absolute paths outside root,
// ".." escapes, and symlink escapes.
func resolveUnderRoot(root, path string) (resolved, rel string, err error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", fmt.Errorf("work directory is empty")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", fmt.Errorf("path is empty")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", "", fmt.Errorf("resolve work directory: %w", err)
	}
	rootReal, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", "", fmt.Errorf("resolve work directory: %w", err)
	}
	// Reject absolute paths that are not under root before join.
	var candidate string
	if filepath.IsAbs(path) {
		candidate = filepath.Clean(path)
	} else {
		// Disallow path components that clean above root without needing Stat.
		cleaned := filepath.Clean(path)
		if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
			return "", "", fmt.Errorf("path escapes project root")
		}
		candidate = filepath.Join(rootAbs, cleaned)
	}
	// Resolve existing path (follows final symlink).
	real, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		// Missing file: walk parents to ensure no symlink escape in the prefix,
		// then accept the cleaned candidate only if it stays under rootReal.
		if !os.IsNotExist(err) {
			return "", "", fmt.Errorf("path escapes project root")
		}
		if err := ensureUnderRootMissing(rootReal, candidate); err != nil {
			return "", "", err
		}
		rel, relErr := filepath.Rel(rootReal, candidate)
		if relErr != nil || !isRelInside(rel) {
			return "", "", fmt.Errorf("path escapes project root")
		}
		return candidate, filepath.ToSlash(rel), nil
	}
	rel, err = filepath.Rel(rootReal, real)
	if err != nil || !isRelInside(rel) {
		return "", "", fmt.Errorf("path escapes project root")
	}
	return real, filepath.ToSlash(rel), nil
}

// ensureUnderRootMissing checks that every existing prefix of candidate stays
// under rootReal when the leaf does not exist yet.
func ensureUnderRootMissing(rootReal, candidate string) error {
	cur := candidate
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		realParent, err := filepath.EvalSymlinks(parent)
		if err == nil {
			rel, relErr := filepath.Rel(rootReal, realParent)
			if relErr != nil || !isRelInside(rel) {
				return fmt.Errorf("path escapes project root")
			}
			// Rebuild candidate under the physical parent for the relative check.
			return nil
		}
		if !os.IsNotExist(err) {
			return fmt.Errorf("path escapes project root")
		}
		cur = parent
	}
	// No existing parent found under candidate — compare cleaned abs to root.
	rel, err := filepath.Rel(rootReal, filepath.Clean(candidate))
	if err != nil || !isRelInside(rel) {
		return fmt.Errorf("path escapes project root")
	}
	return nil
}

func isRelInside(rel string) bool {
	if rel == "" || rel == "." {
		return true
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	return !filepath.IsAbs(rel)
}

func orderedSubsequence(value, query string) bool {
	if query == "" {
		return true
	}
	qi := 0
	for i := 0; i < len(value) && qi < len(query); i++ {
		if value[i] == query[qi] {
			qi++
		}
	}
	return qi == len(query)
}

func absPath(workDir, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Join(workDir, p)
}
