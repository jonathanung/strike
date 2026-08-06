package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// DefaultCheckpointMaxBytes is the largest file content snapshotted for undo
// restore. Larger files are skipped (recorded as skipped) so checkpoints stay
// bounded.
const DefaultCheckpointMaxBytes int64 = 2 << 20 // 2 MiB

// fileOrig is the on-disk state of a path at first mutation touch in a turn.
type fileOrig struct {
	exists  bool
	data    []byte
	skipped bool // true when content was not captured (too large / unreadable)
}

// turnCheckpoint holds pre-mutation originals for one completed turn.
type turnCheckpoint struct {
	turnID    string
	files     map[string]fileOrig // absolute path → original
	uncovered map[string]struct{} // reasons disk may have mutations outside snapshots (e.g. "bash")
}

// CheckpointStore snapshots file contents before mutating tools write, keyed
// by turn id. One frame is committed per turn (possibly empty) so rewind pops
// align with chat undo. A nil receiver is a no-op on every method.
//
// Restore is per-file only (write original bytes or remove created paths). It
// never runs git reset --hard. Bash-driven mutations are not snapshotted
// (#572); MarkUncovered records that gap so undo UX can warn instead of
// claiming silent full success (#801).
type CheckpointStore struct {
	mu       sync.Mutex
	maxBytes int64
	active   *turnCheckpoint
	stack    []*turnCheckpoint // completed turns, oldest first
}

// NewCheckpointStore returns an empty store with the default size limit.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{maxBytes: DefaultCheckpointMaxBytes}
}

// BeginTurn starts capturing mutations for turnID. Replaces any uncommitted
// active frame (e.g. after an unexpected turn abort).
func (s *CheckpointStore) BeginTurn(turnID string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = &turnCheckpoint{
		turnID:    turnID,
		files:     make(map[string]fileOrig),
		uncovered: make(map[string]struct{}),
	}
}

// MarkUncovered records that the active turn may have disk mutations outside
// per-file snapshots (e.g. bash). reason is a short stable token ("bash").
// No-op when there is no active turn. Duplicate reasons collapse.
func (s *CheckpointStore) MarkUncovered(reason string) {
	if s == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return
	}
	if s.active.uncovered == nil {
		s.active.uncovered = make(map[string]struct{})
	}
	s.active.uncovered[reason] = struct{}{}
}

// Snapshot records the pre-mutation state of absPath on first touch in the
// active turn. Subsequent snapshots of the same path are ignored. Missing
// paths are recorded as non-existent (so restore deletes a created file).
// Oversized or unreadable files are marked skipped without failing the tool.
func (s *CheckpointStore) Snapshot(absPath string) {
	if s == nil || absPath == "" {
		return
	}
	absPath = filepath.Clean(absPath)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return
	}
	if _, ok := s.active.files[absPath]; ok {
		return
	}
	info, err := os.Lstat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			s.active.files[absPath] = fileOrig{exists: false}
			return
		}
		s.active.files[absPath] = fileOrig{skipped: true}
		return
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		// Directories / specials / symlinks: do not try to snapshot contents.
		s.active.files[absPath] = fileOrig{skipped: true}
		return
	}
	max := s.maxBytes
	if max <= 0 {
		max = DefaultCheckpointMaxBytes
	}
	if info.Size() > max {
		s.active.files[absPath] = fileOrig{exists: true, skipped: true}
		return
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		s.active.files[absPath] = fileOrig{exists: true, skipped: true}
		return
	}
	s.active.files[absPath] = fileOrig{exists: true, data: data}
}

// CommitTurn pushes the active frame onto the stack (even when empty) and
// clears active. No-op when there is no active turn.
func (s *CheckpointStore) CommitTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return
	}
	s.stack = append(s.stack, s.active)
	s.active = nil
}

// PopResult is the outcome of popping the last committed turn checkpoint.
type PopResult struct {
	TurnID    string
	Restored  []string // absolute paths successfully restored
	Skipped   int      // paths that had no capturable original
	HadFiles  bool     // true when the frame listed any path
	RestoredN int
	// Uncovered lists stable reasons the turn may have disk mutations outside
	// restored paths (e.g. "bash"). Present whether or not restore ran.
	Uncovered []string
}

// PeekResult is a non-destructive view of the most recent committed frame.
// Used for undo preview UX (#801).
type PeekResult struct {
	TurnID     string
	Restorable []string // absolute paths with capturable originals (sorted)
	Skipped    int      // paths recorded but not capturable
	HadFiles   bool
	Uncovered  []string // sorted reasons (e.g. "bash")
	Empty      bool     // true when the stack has no committed frames
}

// Pop drops the most recent committed turn frame. When restore is true,
// per-file originals are written back (or created files removed). Empty stack
// yields a zero PopResult and nil error.
func (s *CheckpointStore) Pop(restore bool) (PopResult, error) {
	if s == nil {
		return PopResult{}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stack) == 0 {
		return PopResult{}, nil
	}
	frame := s.stack[len(s.stack)-1]
	s.stack = s.stack[:len(s.stack)-1]
	out := PopResult{
		TurnID:    frame.turnID,
		HadFiles:  len(frame.files) > 0,
		Uncovered: uncoveredReasons(frame.uncovered),
	}
	if !restore || len(frame.files) == 0 {
		// Still count skipped when restore requested so callers can warn.
		if restore {
			for _, orig := range frame.files {
				if orig.skipped {
					out.Skipped++
				}
			}
		}
		return out, nil
	}
	var errs []error
	// Restore in stable path order for deterministic multi-file undo (#801).
	paths := make([]string, 0, len(frame.files))
	for p := range frame.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		orig := frame.files[p]
		if orig.skipped {
			out.Skipped++
			continue
		}
		if err := restoreFileOriginal(p, orig); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		out.Restored = append(out.Restored, p)
	}
	out.RestoredN = len(out.Restored)
	if len(errs) == 0 {
		return out, nil
	}
	return out, joinErrors(errs)
}

// Peek returns a non-destructive summary of the most recent committed frame.
func (s *CheckpointStore) Peek() PeekResult {
	if s == nil {
		return PeekResult{Empty: true}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stack) == 0 {
		return PeekResult{Empty: true}
	}
	frame := s.stack[len(s.stack)-1]
	out := PeekResult{
		TurnID:    frame.turnID,
		HadFiles:  len(frame.files) > 0,
		Uncovered: uncoveredReasons(frame.uncovered),
	}
	paths := make([]string, 0, len(frame.files))
	for p, orig := range frame.files {
		if orig.skipped {
			out.Skipped++
			continue
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	out.Restorable = paths
	return out
}

// PeekHasFiles reports whether the most recent committed frame captured any
// path (including skipped). Used by the TUI to bias the undo default.
func (s *CheckpointStore) PeekHasFiles() bool {
	p := s.Peek()
	return p.HadFiles
}

func uncoveredReasons(m map[string]struct{}) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for r := range m {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

func restoreFileOriginal(abs string, orig fileOrig) error {
	if !orig.exists {
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return err
	}
	return os.WriteFile(abs, orig.data, 0o644)
}

func joinErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	msg := errs[0].Error()
	for _, e := range errs[1:] {
		msg += "; " + e.Error()
	}
	return fmt.Errorf("%s", msg)
}
