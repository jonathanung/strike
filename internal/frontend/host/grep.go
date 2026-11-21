package host

import "context"

// GrepMatch is one line matched by a workspace content search (#1406).
type GrepMatch struct {
	// Path is workspace-relative with forward slashes.
	Path string `json:"path"`
	// Line is 1-based.
	Line int `json:"line"`
	// Snippet is the matched line, whitespace-trimmed and clipped around
	// the match.
	Snippet string `json:"snippet"`
}

// GrepResult is the outcome of a content search. Capped is true when any
// walk or result limit stopped the search early, so callers can say the
// list is partial instead of presenting it as complete.
type GrepResult struct {
	Matches      []GrepMatch `json:"matches"`
	Capped       bool        `json:"capped"`
	FilesScanned int         `json:"filesScanned"`
}

// ContentSearcher is an optional Files capability: plain-text,
// case-insensitive substring search over the files under the work root.
// Implementations reuse the root scoping of SearchFiles and ReadDocument
// (never escape the root or the allowed-roots ceiling; skip VCS internals,
// ignored paths, symlinks and binary files) and bound the walk, reporting
// Capped rather than silently truncating. ctx cancellation ends the walk
// early with Capped set.
type ContentSearcher interface {
	GrepFiles(ctx context.Context, query string, limit int) (GrepResult, error)
}
