// Package ledger stores a shared decision and assumption ledger for multi-agent
// runs. Entries are append-only history: invalidate/supersede change status
// without deleting rows so leads can audit the trail.
//
// Non-overlap vs sibling stores:
//   - internal/persist/memory — untyped key/value facts (no decision lifecycle)
//   - internal/persist/artifact — versioned work products (findings/patches), not choices
//   - internal/persist/issue — tracked open/closed work items
//   - internal/persist/plan — structured multi-section plans
//
// Prefer ledger_write over burying critical assumptions only in chat prose.
// Active entries can auto-load into the system prompt (context bundle) and are
// shared with child spawns via the same store.
package ledger

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// Entry kinds.
const (
	KindDecision   = "decision"
	KindAssumption = "assumption"
	KindConstraint = "constraint"
)

// Entry status values. History is preserved; rows are never deleted on
// invalidate/supersede.
const (
	StatusActive      = "active"
	StatusInvalidated = "invalidated"
	StatusSuperseded  = "superseded"
)

// Confidence levels (v1 string enum).
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// Entry is one durable ledger record.
type Entry struct {
	ID            string        `json:"id"`
	Kind          string        `json:"kind"` // decision | assumption | constraint
	Statement     string        `json:"statement"`
	Confidence    string        `json:"confidence,omitempty"` // low | medium | high
	EvidenceRefs  []string      `json:"evidence_refs,omitempty"`
	EvidencePins  []EvidencePin `json:"evidence_pins,omitempty"`
	Status        string        `json:"status"` // active | invalidated | superseded
	ScopePaths    []string      `json:"scope_paths,omitempty"`
	ScopeTaskIDs  []string      `json:"scope_task_ids,omitempty"`
	AuthorSession string        `json:"author_session"`
	AuthorAgent   string        `json:"author_agent,omitempty"`
	AuthorRoot    string        `json:"author_root,omitempty"`
	CreatedAt     time.Time     `json:"created_at"`
	UpdatedAt     time.Time     `json:"updated_at"`
	// Invalidation / supersede metadata (empty while active).
	InvalidateReason   string     `json:"invalidate_reason,omitempty"`
	InvalidateEvidence []string   `json:"invalidate_evidence,omitempty"`
	InvalidatedAt      *time.Time `json:"invalidated_at,omitempty"`
	SupersededBy       string     `json:"superseded_by,omitempty"`
	Supersedes         string     `json:"supersedes,omitempty"` // id this entry replaced on append
}

// AppendInput is the create payload.
type AppendInput struct {
	Kind          string
	Statement     string
	Confidence    string // empty → medium
	EvidenceRefs  []string
	EvidencePins  []EvidencePin
	ScopePaths    []string
	ScopeTaskIDs  []string
	AuthorSession string
	AuthorAgent   string
	AuthorRoot    string
	// Supersedes, when set, marks that prior active entry superseded after append.
	Supersedes string
}

// InvalidateInput carries contradicting evidence for an active entry.
type InvalidateInput struct {
	Reason   string
	Evidence []string
}

// ListFilter selects entries for List / ActiveSlice.
type ListFilter struct {
	Status string // empty = any; typically StatusActive for bundles
	Kind   string // empty = any
	// Path, when set, keeps entries with empty ScopePaths or a matching path prefix.
	Path string
	// TaskID, when set, keeps entries with empty ScopeTaskIDs or containing TaskID.
	TaskID string
	// AuthorSession limits to one author when non-empty.
	AuthorSession string
}

// Clone returns a deep copy safe for callers to mutate.
func Clone(e Entry) Entry {
	out := e
	out.EvidenceRefs = append([]string(nil), e.EvidenceRefs...)
	out.EvidencePins = clonePins(e.EvidencePins)
	out.ScopePaths = append([]string(nil), e.ScopePaths...)
	out.ScopeTaskIDs = append([]string(nil), e.ScopeTaskIDs...)
	out.InvalidateEvidence = append([]string(nil), e.InvalidateEvidence...)
	if e.InvalidatedAt != nil {
		t := *e.InvalidatedAt
		out.InvalidatedAt = &t
	}
	return out
}

// ValidKind reports whether k is a known entry kind.
func ValidKind(k string) bool {
	switch k {
	case KindDecision, KindAssumption, KindConstraint:
		return true
	default:
		return false
	}
}

// ValidStatus reports whether s is a known status.
func ValidStatus(s string) bool {
	switch s {
	case StatusActive, StatusInvalidated, StatusSuperseded:
		return true
	default:
		return false
	}
}

// ValidConfidence reports whether c is empty or a known level.
func ValidConfidence(c string) bool {
	switch c {
	case "", ConfidenceLow, ConfidenceMedium, ConfidenceHigh:
		return true
	default:
		return false
	}
}

// MatchScope reports whether e applies to path and/or taskID filters.
// Empty filter fields are ignored.
//
// Scope rules:
//   - Entry with both scopes empty is global (matches any filter).
//   - Path-only filter: global or path-matching entries; task-only entries excluded.
//   - Task-only filter: global or task-matching entries; path-only entries excluded.
//   - Both filters: must satisfy each dimension that the entry declares
//     (undeclared dimension is treated as unrestricted).
func MatchScope(e Entry, path, taskID string) bool {
	path = strings.TrimSpace(path)
	taskID = strings.TrimSpace(taskID)
	if path == "" && taskID == "" {
		return true
	}
	hasPaths := len(e.ScopePaths) > 0
	hasTasks := len(e.ScopeTaskIDs) > 0
	global := !hasPaths && !hasTasks
	if global {
		return true
	}

	if path != "" && taskID == "" {
		// Path query: skip task-only rows.
		if !hasPaths && hasTasks {
			return false
		}
		return !hasPaths || pathMatches(e.ScopePaths, path)
	}
	if taskID != "" && path == "" {
		// Task query: skip path-only rows.
		if !hasTasks && hasPaths {
			return false
		}
		return !hasTasks || taskMatches(e.ScopeTaskIDs, taskID)
	}
	// Both filters set.
	if hasPaths && !pathMatches(e.ScopePaths, path) {
		return false
	}
	if hasTasks && !taskMatches(e.ScopeTaskIDs, taskID) {
		return false
	}
	return true
}

func pathMatches(scopes []string, path string) bool {
	path = filepath.Clean(path)
	for _, s := range scopes {
		s = filepath.Clean(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		if path == s {
			return true
		}
		sep := string(filepath.Separator)
		if strings.HasPrefix(path, s+sep) || strings.HasPrefix(s, path+sep) {
			return true
		}
		// Simple string prefix for relative scopes without clean separators.
		if strings.HasPrefix(path, s) || strings.HasPrefix(s, path) {
			return true
		}
	}
	return false
}

func taskMatches(scopes []string, taskID string) bool {
	for _, t := range scopes {
		if strings.TrimSpace(t) == taskID {
			return true
		}
	}
	return false
}

func validateStatement(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errEmptyStatement
	}
	if len(s) > maxStatementLen {
		return errStatementTooLong
	}
	return nil
}

func normalizeRefs(refs []string, maxN, maxLen int, label string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if len(refs) > maxN {
		return nil, fmt.Errorf("ledger: at most %d %s", maxN, label)
	}
	seen := make(map[string]struct{}, len(refs))
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if len(r) > maxLen {
			return nil, fmt.Errorf("ledger: %s entry exceeds %d bytes", label, maxLen)
		}
		if _, ok := seen[r]; ok {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out, nil
}

func normalizePaths(paths []string) ([]string, error) {
	return normalizeRefs(paths, maxScopePaths, maxScopeItemLen, "scope_paths")
}

func normalizeTaskIDs(ids []string) ([]string, error) {
	return normalizeRefs(ids, maxScopeTasks, maxScopeItemLen, "scope_task_ids")
}
