package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
	turnID string
	files  map[string]fileOrig // absolute path → original
}

// CheckpointStore snapshots file contents before mutating tools write, keyed
// by turn id. One frame is committed per turn (possibly empty) so rewind pops
// align with chat undo. A nil receiver is a no-op on every method.
//
// Restore is per-file only (write original bytes or remove created paths). It
// never runs git reset --hard.
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
		turnID: turnID,
		files:  make(map[string]fileOrig),
	}
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
	out := PopResult{TurnID: frame.turnID, HadFiles: len(frame.files) > 0}
	if !restore || len(frame.files) == 0 {
		return out, nil
	}
	var errs []error
	// Restore in stable path order for deterministic tests.
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

// PeekHasFiles reports whether the most recent committed frame captured any
// path (including skipped). Used by the TUI to bias the undo default.
func (s *CheckpointStore) PeekHasFiles() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.stack) == 0 {
		return false
	}
	return len(s.stack[len(s.stack)-1].files) > 0
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
