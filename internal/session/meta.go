package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// PR state values stored on Meta.PRState (GitHub-style, lowercase).
const (
	PRStateOpen   = "open"
	PRStateMerged = "merged"
	PRStateClosed = "closed"
)

// Meta is durable session-level metadata stored beside the JSONL event log
// (lineage, title, shipping side-effects). Missing fields stay zero-valued so
// older sidecars remain readable.
type Meta struct {
	Title           string `json:"title,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	// ForkedFrom is the source session id when this session was created via
	// Fork. Empty for ordinary roots and subagent children. Forks remain root
	// sessions (ParentSessionID empty) so --continue / pickers still work.
	ForkedFrom string `json:"forkedFrom,omitempty"`
	// ProjectKey is the launch project identity (same key as history/memory),
	// typically the canonical git root or cwd. Empty on legacy sidecars.
	ProjectKey string `json:"projectKey,omitempty"`
	// WorktreePath is the absolute path of a strike-managed git worktree bound
	// to this session (tool CWD). Empty when the session uses the launch cwd.
	WorktreePath string `json:"worktreePath,omitempty"`
	// WorktreeBranch is the local branch checked out in WorktreePath.
	WorktreeBranch string `json:"worktreeBranch,omitempty"`
	CreatedAt      string `json:"createdAt,omitempty"` // RFC3339 UTC
	PRURL          string `json:"prUrl,omitempty"`
	PRNumber       int    `json:"prNumber,omitempty"`
	PRState        string `json:"prState,omitempty"`     // open|merged|closed
	PRUpdatedAt    string `json:"prUpdatedAt,omitempty"` // RFC3339 UTC when PR fields last written
}

// NormalizePRState maps forge state strings to open|merged|closed, or "".
func NormalizePRState(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case PRStateOpen:
		return PRStateOpen
	case PRStateMerged:
		return PRStateMerged
	case PRStateClosed:
		return PRStateClosed
	default:
		return ""
	}
}

// MetaPath is the sidecar JSON path for a session id under dir.
func MetaPath(dir, id string) string {
	return filepath.Join(dir, id+".meta.json")
}

// ReadMeta loads session metadata. Missing files yield a zero Meta and nil error.
func ReadMeta(dir, id string) (Meta, error) {
	data, err := os.ReadFile(MetaPath(dir, id))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Meta{}, nil
		}
		return Meta{}, err
	}
	var m Meta
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("session meta: %w", err)
	}
	return m, nil
}

// WriteMeta replaces the session metadata sidecar.
func WriteMeta(dir, id string, m Meta) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := MetaPath(dir, id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// UpdateMeta reads, mutates, and writes session metadata under a process-wide
// lock so concurrent tool writes do not clobber each other.
func UpdateMeta(dir, id string, fn func(*Meta)) (Meta, error) {
	metaMu.Lock()
	defer metaMu.Unlock()
	m, err := ReadMeta(dir, id)
	if err != nil {
		return Meta{}, err
	}
	fn(&m)
	if err := WriteMeta(dir, id, m); err != nil {
		return Meta{}, err
	}
	return m, nil
}

var metaMu sync.Mutex
