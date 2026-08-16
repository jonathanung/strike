package tool

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// ChangeKind classifies a harness file mutation for per-turn diffs.
type ChangeKind string

const (
	ChangeCreate ChangeKind = "create"
	ChangeUpdate ChangeKind = "update"
	ChangeDelete ChangeKind = "delete"
)

// FileChange is one path touched by harness edit tools in a turn.
type FileChange struct {
	Path string     // workspace-relative slash path when possible
	Kind ChangeKind // create | update | delete
}

// TurnDiff accumulates create/update/delete for harness-touched paths within
// one turn. Composes with CheckpointStore (pre-mutation bytes for #540 undo)
// and PathOwnership (#772 overlap): this is the structured per-turn summary
// for timeline/UI, not a second file-state system. Nil receiver is a no-op.
type TurnDiff struct {
	mu      sync.Mutex
	changes map[string]ChangeKind // rel path → kind
}

// Note records a successful mutation. deleted marks removals; existedBefore
// distinguishes create vs update for non-deletes. Last write wins per path;
// create then delete in the same turn drops the path (net no change).
func (d *TurnDiff) Note(rel string, existedBefore, deleted bool) {
	if d == nil || rel == "" {
		return
	}
	rel = filepath.ToSlash(strings.TrimSpace(rel))
	if rel == "" || rel == "." {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changes == nil {
		d.changes = make(map[string]ChangeKind)
	}
	prev, had := d.changes[rel]
	if deleted {
		if had && prev == ChangeCreate {
			delete(d.changes, rel)
			return
		}
		d.changes[rel] = ChangeDelete
		return
	}
	if had && prev == ChangeDelete {
		// deleted then re-created → create (or update if it existed before delete)
		if existedBefore {
			d.changes[rel] = ChangeUpdate
		} else {
			d.changes[rel] = ChangeCreate
		}
		return
	}
	if had {
		// keep create if first touch was create; otherwise update
		if prev == ChangeCreate {
			return
		}
		d.changes[rel] = ChangeUpdate
		return
	}
	if existedBefore {
		d.changes[rel] = ChangeUpdate
	} else {
		d.changes[rel] = ChangeCreate
	}
}

// Snapshot returns a stable-sorted copy of recorded changes.
func (d *TurnDiff) Snapshot() []FileChange {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.changes) == 0 {
		return nil
	}
	paths := make([]string, 0, len(d.changes))
	for p := range d.changes {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out := make([]FileChange, 0, len(paths))
	for _, p := range paths {
		out = append(out, FileChange{Path: p, Kind: d.changes[p]})
	}
	return out
}

// Reset clears the active turn's changes (call at turn start).
func (d *TurnDiff) Reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.changes = nil
}

// RelPathForDiff maps an absolute path to a workspace-relative slash path.
func RelPathForDiff(workDir, absPath string) string {
	absPath = filepath.Clean(strings.TrimSpace(absPath))
	if absPath == "" {
		return ""
	}
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return filepath.ToSlash(absPath)
	}
	rel, err := filepath.Rel(workDir, absPath)
	if err != nil || rel == "." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return filepath.ToSlash(absPath)
	}
	return filepath.ToSlash(rel)
}

// FileExisted reports whether absPath exists as any leaf (including symlink).
func FileExisted(absPath string) bool {
	if absPath == "" {
		return false
	}
	_, err := os.Lstat(absPath)
	return err == nil
}
