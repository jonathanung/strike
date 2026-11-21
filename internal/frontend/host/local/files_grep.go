package local

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/jonathanung/strike/internal/frontend/host"
)

// Content search walk limits (#1406). Any limit hit sets GrepResult.Capped.
const (
	defaultGrepResults = 200
	maxGrepResults     = 500
	maxGrepPerFile     = 20
	maxGrepFiles       = 5000
	maxGrepFileBytes   = maxFileBytes // bytes scanned per file (prefix)
	maxGrepTotalBytes  = 64 << 20     // bytes scanned per search
	maxGrepQueryBytes  = 256
	maxGrepSnippetRune = 200
	grepBinarySniff    = 8000 // NUL in this prefix marks a file binary
)

var _ host.ContentSearcher = (*filesService)(nil)

// GrepFiles implements host.ContentSearcher over the same file index as
// SearchFiles (git ls-files honoring .gitignore, else a walk that skips
// .git and other noise directories). Each file is re-resolved under the
// work root and the allowed-roots ceiling before it is read.
func (f *filesService) GrepFiles(ctx context.Context, query string, limit int) (host.GrepResult, error) {
	res := host.GrepResult{Matches: []host.GrepMatch{}}
	work, ceilingOn, ceiling := f.docRoots()
	if strings.TrimSpace(work) == "" {
		return res, fmt.Errorf("work directory is empty")
	}
	if strings.TrimSpace(query) == "" {
		return res, fmt.Errorf("query is empty")
	}
	if len(query) > maxGrepQueryBytes {
		return res, fmt.Errorf("query longer than %d bytes", maxGrepQueryBytes)
	}
	if strings.ContainsAny(query, "\r\n") {
		return res, fmt.Errorf("query must be a single line")
	}
	if limit <= 0 {
		limit = defaultGrepResults
	}
	if limit > maxGrepResults {
		limit = maxGrepResults
	}
	index, err := f.fileIndex()
	if err != nil {
		return res, err
	}
	if len(index) >= maxSearchIndex {
		// The index itself stopped at its cap: files past it are unsearched.
		res.Capped = true
	}
	needle := []byte(strings.ToLower(query))
	var scanned int64
	for _, rel := range index {
		if strings.HasSuffix(rel, "/") {
			continue
		}
		if ctx.Err() != nil || res.FilesScanned >= maxGrepFiles || scanned >= maxGrepTotalBytes {
			res.Capped = true
			break
		}
		data, truncated, ok := readGrepFile(work, rel, ceilingOn, ceiling)
		if !ok {
			continue
		}
		res.FilesScanned++
		scanned += int64(len(data))
		if truncated {
			res.Capped = true
		}
		if full := grepLines(&res, rel, data, needle, limit); full {
			res.Capped = true
			break
		}
	}
	return res, nil
}

// grepLines appends rel's matching lines to res. It returns true when the
// total limit is exceeded (a further match existed past it).
func grepLines(res *host.GrepResult, rel string, data, needle []byte, limit int) bool {
	per := 0
	line := 0
	for len(data) > 0 {
		line++
		var raw []byte
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			raw, data = data[:i], data[i+1:]
		} else {
			raw, data = data, nil
		}
		if !bytes.Contains(bytes.ToLower(raw), needle) {
			continue
		}
		if per >= maxGrepPerFile {
			res.Capped = true
			return false
		}
		if len(res.Matches) >= limit {
			return true
		}
		res.Matches = append(res.Matches, host.GrepMatch{
			Path:    rel,
			Line:    line,
			Snippet: grepSnippet(string(raw), string(needle)),
		})
		per++
	}
	return false
}

// readGrepFile reads up to maxGrepFileBytes of one index entry. ok is false
// for anything that is not a regular, non-symlink, non-binary file under the
// work root and inside the ceiling (when one is set).
func readGrepFile(work, rel string, ceilingOn bool, ceiling []string) (data []byte, truncated, ok bool) {
	if err := rejectSymlinkPath(work, rel); err != nil {
		return nil, false, false
	}
	resolved, _, err := resolveUnderRoot(work, rel)
	if err != nil {
		return nil, false, false
	}
	if withinCeiling(resolved, ceilingOn, ceiling) != nil {
		return nil, false, false
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return nil, false, false
	}
	file, err := os.Open(filepath.Clean(resolved))
	if err != nil {
		return nil, false, false
	}
	defer file.Close()
	data, err = io.ReadAll(io.LimitReader(file, maxGrepFileBytes+1))
	if err != nil {
		return nil, false, false
	}
	if len(data) > maxGrepFileBytes {
		data = data[:maxGrepFileBytes]
		truncated = true
	}
	sniff := data
	if len(sniff) > grepBinarySniff {
		sniff = sniff[:grepBinarySniff]
	}
	if bytes.IndexByte(sniff, 0) >= 0 {
		return nil, false, false
	}
	return data, truncated, true
}

// grepSnippet trims line and clips it to maxGrepSnippetRune runes, keeping
// the first match (lowerNeedle) in view when the line is long.
func grepSnippet(line, lowerNeedle string) string {
	s := strings.TrimSpace(strings.ToValidUTF8(strings.TrimRight(line, "\r"), "�"))
	runes := []rune(s)
	if len(runes) <= maxGrepSnippetRune {
		return s
	}
	start := 0
	// Byte offsets line up only when lowering kept the length (ASCII and
	// most text); otherwise clip from the start.
	if lower := strings.ToLower(s); len(lower) == len(s) {
		if at := strings.Index(lower, lowerNeedle); at >= 0 {
			matchRune := utf8.RuneCountInString(s[:at])
			start = matchRune - maxGrepSnippetRune/4
			if start < 0 {
				start = 0
			}
			if start+maxGrepSnippetRune > len(runes) {
				start = len(runes) - maxGrepSnippetRune
			}
		}
	}
	out := string(runes[start : start+maxGrepSnippetRune])
	if start > 0 {
		out = "…" + out
	}
	if start+maxGrepSnippetRune < len(runes) {
		out += "…"
	}
	return out
}
