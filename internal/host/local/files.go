package local

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/harness/tool"
	"github.com/jonathanung/strike-cli/internal/host"
)

const (
	maxFileBytes         = 1 << 20 // 1 MiB
	maxListEntries       = 2000
	maxSearchIndex       = 8000
	maxSearchResults     = 50
	maxDirListingEntries = 200
	searchCacheTTL       = 2 * time.Second
	scopedTruncateCap    = 1 << 20 // same as max; notice when truncated at cap
	gitLsTimeout         = 3 * time.Second
)

// defaultSkipDirNames are never walked/indexed for @file completion unless the
// query explicitly targets them (exact-path resolve still works via ReadScoped).
var defaultSkipDirNames = []string{
	".git", "node_modules", ".strike", "vendor", ".hg", ".svn", ".jj",
	".plan", "dist", "build", "target", ".next", ".cache", "__pycache__",
	".venv", "venv", "coverage", "out", ".idea", ".vscode",
}

// filesService reads workspace files for host.Files frontends.
type filesService struct {
	workDir  string
	skipDirs map[string]struct{} // basename → skip during index walk

	mu        sync.Mutex
	cache     []string
	cacheAt   time.Time
	cacheRoot string
}

// NewFiles returns a host.Files that resolves paths relative to workDir
// (absolute paths are cleaned as-is for ReadFile/ListDir). SearchFiles and
// ReadScoped reject paths that escape workDir via ".." or symlinks.
//
// Indexing prefers `git ls-files` (honors .gitignore) and falls back to a
// walk that skips heavy/noise directories. Extra skip basenames may be listed
// one-per-line in `.strike/file-index-skip` under the work directory.
func NewFiles(workDir string) host.Files {
	return &filesService{
		workDir:  workDir,
		skipDirs: loadSkipDirs(workDir),
	}
}

// SetFilesWorkDir updates the workspace root used for relative paths. Used when
// the active multi-root session switches to another worktree. Safe for
// concurrent use with readers.
func SetFilesWorkDir(f host.Files, workDir string) {
	fs, ok := f.(*filesService)
	if !ok || fs == nil {
		return
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.workDir = workDir
	fs.skipDirs = loadSkipDirs(workDir)
	fs.cache = nil
	fs.cacheAt = time.Time{}
	fs.cacheRoot = ""
}

func (f *filesService) workRoot() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.workDir
}

func (f *filesService) skipSet() map[string]struct{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.skipDirs == nil {
		return defaultSkipSet()
	}
	return f.skipDirs
}

func (f *filesService) ApplyEdit(req host.EditApply) (host.EditApplyResult, error) {
	path := strings.TrimSpace(req.Path)
	if path == "" {
		return host.EditApplyResult{}, fmt.Errorf("path is empty")
	}
	if req.OldString == req.NewString {
		return host.EditApplyResult{}, fmt.Errorf("oldString and newString are identical")
	}
	resolved, rel, err := resolveUnderRoot(f.workRoot(), path)
	if err != nil {
		return host.EditApplyResult{}, err
	}
	info, err := os.Lstat(resolved)
	if err != nil {
		if os.IsNotExist(err) {
			return host.EditApplyResult{}, fmt.Errorf("file not found: %s", rel)
		}
		return host.EditApplyResult{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return host.EditApplyResult{}, fmt.Errorf("symlink not allowed: %s", rel)
	}
	if !info.Mode().IsRegular() {
		return host.EditApplyResult{}, fmt.Errorf("not a regular file: %s", rel)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		return host.EditApplyResult{}, err
	}
	content := string(data)
	count := strings.Count(content, req.OldString)
	if count == 0 {
		// Already applied: file has newString and not oldString (single-edit case).
		if !req.ReplaceAll && strings.Contains(content, req.NewString) {
			return host.EditApplyResult{Path: rel, Already: true}, nil
		}
		return host.EditApplyResult{}, fmt.Errorf("oldString not found in %s", rel)
	}
	if count > 1 && !req.ReplaceAll {
		return host.EditApplyResult{}, fmt.Errorf("oldString matches %d locations in %s; need unique context or replaceAll", count, rel)
	}
	var updated string
	replaced := 1
	if req.ReplaceAll {
		updated = strings.ReplaceAll(content, req.OldString, req.NewString)
		replaced = count
	} else {
		updated = strings.Replace(content, req.OldString, req.NewString, 1)
	}
	if err := writeFileAtomic(resolved, []byte(updated), info.Mode().Perm()); err != nil {
		return host.EditApplyResult{}, err
	}
	return host.EditApplyResult{Path: rel, Count: replaced}, nil
}

func (f *filesService) ApplyPatch(patch string) (string, error) {
	root := f.workRoot()
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("work directory is empty")
	}
	return tool.ApplyPatchToWorkDir(root, patch)
}

// writeFileAtomic writes data via a same-directory temp file + rename so a
// failed apply does not leave a truncated target.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	if perm == 0 {
		perm = 0o644
	}
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".strike-apply-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func (f *filesService) ReadFile(path string) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("path is empty")
	}
	resolved := absPath(f.workRoot(), path)
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
	workDir := f.workRoot()
	var resolved string
	if path == "" {
		if workDir == "" {
			return nil, fmt.Errorf("path is empty")
		}
		resolved = filepath.Clean(workDir)
	} else {
		resolved = absPath(workDir, path)
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
	if f.workRoot() == "" {
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
	query = strings.TrimPrefix(query, "./")
	if query == "" {
		out := stableIndexPrefix(index, limit)
		return out, nil
	}
	wantNoise := queryTargetsNoise(query)
	type hit struct {
		path string
		rank int
	}
	var hits []hit
	for _, p := range index {
		if !wantNoise && pathHasSkippedComponent(p, f.skipSet()) {
			// Index should already exclude these; belt-and-suspenders.
			continue
		}
		rank := matchRank(p, query)
		if rank < 0 {
			continue
		}
		// Demote noise paths unless the query explicitly targets them.
		if !wantNoise && isNoisePath(p) {
			rank += 10
		}
		hits = append(hits, hit{path: p, rank: rank})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].rank != hits[j].rank {
			return hits[i].rank < hits[j].rank
		}
		// Prefer shorter / shallower paths within a rank.
		si, sj := pathScore(hits[i].path), pathScore(hits[j].path)
		if si != sj {
			return si < sj
		}
		return hits[i].path < hits[j].path
	})
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, h := range hits {
		if _, ok := seen[h.path]; ok {
			continue
		}
		seen[h.path] = struct{}{}
		out = append(out, h.path)
		if len(out) >= limit {
			break
		}
	}
	// Exact typed path must attach even when absent from fuzzy top-k / index.
	if exact, ok := f.exactPathHit(query); ok {
		out = prependUnique(out, exact, limit)
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
	// Allow trailing slash for folder mentions.
	trimmed := strings.TrimSuffix(display, "/")
	if trimmed == "" {
		return host.FileContent{Path: display, Skip: true, Notice: "empty path"}, nil
	}
	resolved, rel, err := resolveUnderRoot(f.workRoot(), trimmed)
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
	if info.Mode()&os.ModeSymlink != 0 {
		return host.FileContent{Path: rel, Skip: true, Notice: "symlink not allowed"}, nil
	}
	if info.IsDir() {
		return f.readScopedDir(resolved, rel)
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

// readScopedDir expands a folder mention to an immediate child listing only
// (not recursive multi-file contents). Caps entry count for safety.
func (f *filesService) readScopedDir(resolved, rel string) (host.FileContent, error) {
	display := rel
	if display != "" && !strings.HasSuffix(display, "/") {
		display += "/"
	}
	entries, err := os.ReadDir(resolved)
	if err != nil {
		return host.FileContent{Path: display, Skip: true, Notice: err.Error()}, nil
	}
	type row struct {
		name  string
		isDir bool
	}
	rows := make([]row, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if name == "." || name == ".." {
			continue
		}
		rows = append(rows, row{name: name, isDir: e.IsDir()})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].isDir != rows[j].isDir {
			return rows[i].isDir
		}
		return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
	})
	var b strings.Builder
	b.WriteString("directory listing (immediate children only):\n")
	n := 0
	for _, r := range rows {
		if n >= maxDirListingEntries {
			fmt.Fprintf(&b, "… truncated after %d entries\n", maxDirListingEntries)
			break
		}
		if r.isDir {
			fmt.Fprintf(&b, "%s/\n", r.name)
		} else {
			fmt.Fprintf(&b, "%s\n", r.name)
		}
		n++
	}
	if n == 0 {
		b.WriteString("(empty)\n")
	}
	fc := host.FileContent{Path: display, Content: b.String()}
	if len(rows) > maxDirListingEntries {
		fc.Notice = fmt.Sprintf("listing truncated to %d entries", maxDirListingEntries)
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
	skip := f.skipDirs
	if skip == nil {
		skip = defaultSkipSet()
	}
	var out []string
	if gitOut, ok := gitLsFilesIndex(rootReal, skip); ok {
		out = gitOut
	} else {
		var walkErr error
		out, walkErr = walkFileIndex(rootReal, skip)
		if walkErr != nil {
			return nil, walkErr
		}
	}
	sort.Strings(out)
	f.cache = out
	f.cacheAt = time.Now()
	f.cacheRoot = root
	return out, nil
}

// exactPathHit returns a project-relative path when query names an existing
// file or directory under the work root (symlink-safe).
func (f *filesService) exactPathHit(query string) (string, bool) {
	q := strings.TrimSpace(query)
	q = strings.ReplaceAll(q, "\\", "/")
	q = strings.TrimPrefix(q, "./")
	if q == "" || q == "." || q == ".." || strings.HasPrefix(q, "../") {
		return "", false
	}
	wantDir := strings.HasSuffix(q, "/")
	trimmed := strings.TrimSuffix(q, "/")
	if trimmed == "" {
		return "", false
	}
	resolved, rel, err := resolveUnderRoot(f.workRoot(), trimmed)
	if err != nil {
		return "", false
	}
	info, err := os.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return "", false
	}
	if info.IsDir() {
		if !strings.HasSuffix(rel, "/") {
			rel += "/"
		}
		return rel, true
	}
	if wantDir {
		// Queried as dir but is a file.
		return "", false
	}
	if info.Mode().IsRegular() {
		return rel, true
	}
	return "", false
}

func gitLsFilesIndex(rootReal string, skip map[string]struct{}) ([]string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), gitLsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "-C", rootReal, "ls-files", "-z",
		"--cached", "--others", "--exclude-standard")
	// Avoid inheriting noisy git UI.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0")
	raw, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	parts := bytes.Split(raw, []byte{0})
	seen := make(map[string]struct{}, len(parts))
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		if _, ok := seen[p]; ok {
			return
		}
		if len(out) >= maxSearchIndex {
			return
		}
		seen[p] = struct{}{}
		out = append(out, p)
	}
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		rel := filepath.ToSlash(string(part))
		if rel == "." || strings.HasPrefix(rel, "../") {
			continue
		}
		if pathHasSkippedComponent(rel, skip) {
			continue
		}
		// Only regular files that still exist and are not symlink escapes.
		abs := filepath.Join(rootReal, filepath.FromSlash(rel))
		info, err := os.Lstat(abs)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// Ensure path stays under root (reject weird git paths).
		if _, err := filepath.Rel(rootReal, abs); err != nil {
			continue
		}
		add(rel)
		// Parent directories as @path/ candidates.
		dir := pathDirSlash(rel)
		for dir != "" && dir != "." {
			if pathHasSkippedComponent(dir, skip) {
				break
			}
			add(dir + "/")
			parent := pathDirSlash(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
		if len(out) >= maxSearchIndex {
			break
		}
	}
	if len(out) == 0 {
		// Empty git repo / no files — still a successful git probe; walk may
		// find nothing either, but prefer walk when git returned empty so
		// untracked-only trees still index? git ls-files --others includes
		// untracked, so empty really means empty. OK.
		return out, true
	}
	return out, true
}

func walkFileIndex(rootReal string, skip map[string]struct{}) ([]string, error) {
	var out []string
	err := filepath.WalkDir(rootReal, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			// Skip unreadable entries; keep indexing the rest.
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if path != rootReal {
				if shouldSkipDirName(name, skip) {
					return fs.SkipDir
				}
				// Do not descend through directory symlinks (escape risk).
				if info, err := d.Info(); err == nil && info.Mode()&os.ModeSymlink != 0 {
					return fs.SkipDir
				}
				rel, err := filepath.Rel(rootReal, path)
				if err == nil && isRelInside(rel) {
					out = append(out, filepath.ToSlash(rel)+"/")
					if len(out) >= maxSearchIndex {
						return fs.SkipAll
					}
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(rootReal, path)
		if err != nil || !isRelInside(rel) {
			return nil
		}
		slash := filepath.ToSlash(rel)
		if pathHasSkippedComponent(slash, skip) {
			return nil
		}
		out = append(out, slash)
		if len(out) >= maxSearchIndex {
			return fs.SkipAll
		}
		return nil
	})
	return out, err
}

func matchRank(path, query string) int {
	lower := strings.ToLower(path)
	base := strings.ToLower(filepath.Base(strings.TrimSuffix(path, "/")))
	q := strings.TrimSuffix(query, "/")
	lowerTrim := strings.TrimSuffix(lower, "/")
	switch {
	case lower == query || lowerTrim == q || base == q:
		return 0
	case strings.HasPrefix(lower, query) || strings.HasPrefix(lowerTrim, q) || strings.HasPrefix(base, q):
		return 1
	case strings.Contains(lower, q) || strings.Contains(base, q):
		return 2
	case orderedSubsequence(lower, query) || orderedSubsequence(base, q):
		return 3
	default:
		return -1
	}
}

func pathScore(p string) int {
	// Lower is better: prefer shallow short paths.
	n := strings.Count(p, "/")
	return n*1000 + len(p)
}

func stableIndexPrefix(index []string, limit int) []string {
	// Prefer non-noise, then shallow paths, then alpha (index already sorted).
	type item struct {
		p     string
		noise bool
		score int
	}
	items := make([]item, 0, len(index))
	for _, p := range index {
		items = append(items, item{p: p, noise: isNoisePath(p), score: pathScore(p)})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].noise != items[j].noise {
			return !items[i].noise
		}
		if items[i].score != items[j].score {
			return items[i].score < items[j].score
		}
		return items[i].p < items[j].p
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].p
	}
	return out
}

func prependUnique(out []string, exact string, limit int) []string {
	for i, p := range out {
		if p == exact || strings.TrimSuffix(p, "/") == strings.TrimSuffix(exact, "/") {
			if i == 0 {
				out[0] = exact
				return out
			}
			// Move to front.
			copy(out[1:i+1], out[0:i])
			out[0] = exact
			return out
		}
	}
	if len(out) >= limit {
		out = out[:limit-1]
	}
	return append([]string{exact}, out...)
}

func queryTargetsNoise(query string) bool {
	q := strings.ToLower(query)
	// Explicit path into a noise tree, or basename that is the noise dir.
	for _, name := range defaultSkipDirNames {
		n := strings.ToLower(name)
		if q == n || q == n+"/" || strings.HasPrefix(q, n+"/") || strings.Contains(q, "/"+n+"/") || strings.HasSuffix(q, "/"+n) {
			return true
		}
	}
	return false
}

func isNoisePath(p string) bool {
	return pathHasSkippedComponent(p, defaultSkipSet())
}

func pathHasSkippedComponent(p string, skip map[string]struct{}) bool {
	p = strings.TrimSuffix(p, "/")
	if p == "" {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." {
			continue
		}
		if shouldSkipDirName(part, skip) {
			return true
		}
	}
	return false
}

func shouldSkipDirName(name string, skip map[string]struct{}) bool {
	if name == "" {
		return false
	}
	if skip != nil {
		if _, ok := skip[name]; ok {
			return true
		}
	}
	return false
}

func defaultSkipSet() map[string]struct{} {
	m := make(map[string]struct{}, len(defaultSkipDirNames))
	for _, n := range defaultSkipDirNames {
		m[n] = struct{}{}
	}
	return m
}

func loadSkipDirs(workDir string) map[string]struct{} {
	m := defaultSkipSet()
	if workDir == "" {
		return m
	}
	// Optional project overrides: one directory basename per line.
	path := filepath.Join(workDir, ".strike", "file-index-skip")
	file, err := os.Open(path)
	if err != nil {
		return m
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSuffix(line, "/")
		if line == "" || strings.ContainsAny(line, `/\`) {
			continue
		}
		m[line] = struct{}{}
	}
	return m
}

func pathDirSlash(p string) string {
	p = strings.TrimSuffix(p, "/")
	i := strings.LastIndexByte(p, '/')
	if i <= 0 {
		return ""
	}
	return p[:i]
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
		candidate = filepath.Join(rootReal, cleaned)
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
