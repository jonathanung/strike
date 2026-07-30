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
// (workspace path, lineage, title, shipping side-effects). Missing fields stay
// zero-valued so older sidecars remain readable. Field order is intentional:
// projectKey (workspace folder) is written first in the sidecar JSON.
type Meta struct {
	// ProjectKey is the launch workspace/folder identity (same key as
	// history/memory), typically the canonical git root or cwd. Empty on
	// legacy sidecars. Written first so session meta embeds the folder path
	// at the top of the JSON.
	ProjectKey      string `json:"projectKey,omitempty"`
	Title           string `json:"title,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	// LeadSessionID is the implicit agent-team lead for this session. Empty on
	// roots (the session is its own lead). Set on task children to the root
	// lead so roster/messaging can resolve team identity without the live
	// engine tree. Nested grandchildren store the same lead id (not their
	// immediate parent).
	LeadSessionID string `json:"leadSessionId,omitempty"`
	// ForkedFrom is the source session id when this session was created via
	// Fork. Empty for ordinary roots and subagent children. Forks remain root
	// sessions (ParentSessionID empty) so --continue / pickers still work.
	ForkedFrom string `json:"forkedFrom,omitempty"`
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

// TeamLeadID resolves the implicit team lead for sessionID given its meta.
// Roots (no parent) are their own lead. Children use LeadSessionID when set;
// otherwise parent is the best-effort lead (depth-1 legacy sidecars).
func (m Meta) TeamLeadID(sessionID string) string {
	sid := strings.TrimSpace(sessionID)
	if strings.TrimSpace(m.ParentSessionID) == "" {
		if sid != "" {
			return sid
		}
		return ""
	}
	if lead := strings.TrimSpace(m.LeadSessionID); lead != "" {
		return lead
	}
	return strings.TrimSpace(m.ParentSessionID)
}

// ResolveChildLeadID picks the LeadSessionID to store on a new child of parentID
// using the parent's meta. Roots yield parentID; children yield their lead.
func ResolveChildLeadID(parentID string, parentMeta Meta) string {
	parentID = strings.TrimSpace(parentID)
	if parentID == "" {
		return ""
	}
	if lead := parentMeta.TeamLeadID(parentID); lead != "" {
		return lead
	}
	return parentID
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
