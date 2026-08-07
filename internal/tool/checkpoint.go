package tool

import (
	"crypto/sha256"
	"encoding/hex"
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

// DefaultCheckpointMaxTurns caps how many committed turn frames are retained
// in memory and on disk (#573). Oldest frames are dropped first.
const DefaultCheckpointMaxTurns = 50

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
	// baseline is a shadow-git tree oid captured when the first uncovered
	// tool (bash) runs. Used at CommitTurn to fill gaps for shell mutations.
	baseline string
}

// CheckpointStore snapshots file contents before mutating tools write, keyed
// by turn id. One frame is committed per turn (possibly empty) so rewind pops
// align with chat undo. A nil receiver is a no-op on every method.
//
// Restore is per-file only (write original bytes or remove created paths). It
// never runs git reset --hard.
//
// Bash coverage (#572): MarkUncovered("bash") records a pending gap and
// lazily captures a shadow-git baseline of WorkDir. CommitTurn reconciles
// the worktree against that baseline and snapshots any paths not already
// recorded by harness tools. When reconcile succeeds, the pending "bash"
// reason is cleared so undo does not warn about covered shell mutations.
//
// Persistence (#573): when PersistDir is configured, committed frames spill
// under ~/.strike/checkpoints/<session-id>/ so --continue can restore files.
type CheckpointStore struct {
	mu       sync.Mutex
	maxBytes int64
	maxTurns int
	active   *turnCheckpoint
	stack    []*turnCheckpoint // completed turns, oldest first

	workDir    string
	persistDir string // empty disables disk persistence
	shadow     *shadowGit
	// loaded is true after a successful Load from disk (even if empty).
	loaded bool
}

// NewCheckpointStore returns an empty store with the default size limit.
func NewCheckpointStore() *CheckpointStore {
	return &CheckpointStore{
		maxBytes: DefaultCheckpointMaxBytes,
		maxTurns: DefaultCheckpointMaxTurns,
	}
}

// Configure binds the store to a workspace and optional durable directory.
// persistDir empty keeps the stack process-local only. Safe to call once
// after New; subsequent calls with the same paths are no-ops.
func (s *CheckpointStore) Configure(workDir, persistDir string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	workDir = strings.TrimSpace(workDir)
	persistDir = strings.TrimSpace(persistDir)
	if workDir != "" {
		s.workDir = filepath.Clean(workDir)
	}
	if persistDir != "" {
		s.persistDir = filepath.Clean(persistDir)
	}
	if s.workDir != "" && s.persistDir != "" {
		s.shadow = newShadowGit(filepath.Join(s.persistDir, "shadow"), s.workDir)
	} else if s.workDir != "" {
		// Ephemeral shadow under the process temp dir when not persisting.
		sum := sha256.Sum256([]byte(s.workDir))
		tmp := filepath.Join(os.TempDir(), "strike", "shadow", hex.EncodeToString(sum[:12]))
		s.shadow = newShadowGit(tmp, s.workDir)
	}
}

// DefaultCheckpointDir returns ~/.strike/checkpoints/<sessionID>.
// Empty sessionID yields empty string.
func DefaultCheckpointDir(sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ""
	}
	// Reject path separators so session ids cannot escape the checkpoints root.
	if strings.Contains(sessionID, "/") || strings.Contains(sessionID, "\\") ||
		strings.Contains(sessionID, "..") {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "strike", "checkpoints", sessionID)
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "checkpoints", sessionID)
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
// Lazily captures a shadow-git baseline so CommitTurn can cover shell writes.
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
	// Capture baseline once per turn, before the mutating tool runs.
	if s.active.baseline == "" && s.shadow != nil {
		if tree, err := s.shadow.capture(); err == nil {
			s.active.baseline = tree
		}
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
	s.snapshotLocked(absPath)
}

func (s *CheckpointStore) snapshotLocked(absPath string) {
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
// clears active. Reconciles shadow-git coverage for bash before commit.
// No-op when there is no active turn.
func (s *CheckpointStore) CommitTurn() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil {
		return
	}
	s.reconcileShadowLocked()
	s.stack = append(s.stack, s.active)
	s.active = nil
	s.trimLocked()
	s.persistLocked()
}

// reconcileShadowLocked fills active.files from the shadow baseline for any
// worktree paths changed since baseline that harness tools did not snapshot.
// On success, clears uncovered reasons that shadow covers ("bash").
// Caller must hold s.mu.
func (s *CheckpointStore) reconcileShadowLocked() {
	if s.active == nil {
		return
	}
	baseline := s.active.baseline
	if baseline == "" || s.shadow == nil {
		// No baseline → leave uncovered markers as-is (bash ran without git).
		return
	}
	// Capture post-mutation tree and diff against baseline.
	// Temporarily release is not needed: capture only runs git against workDir.
	toTree, err := s.shadow.capture()
	if err != nil || toTree == "" {
		return
	}
	changes, err := s.shadow.diffTrees(baseline, toTree)
	if err != nil {
		return
	}
	max := s.maxBytes
	if max <= 0 {
		max = DefaultCheckpointMaxBytes
	}
	reconcileOK := true
	for _, ch := range changes {
		if ch.Path == "" {
			continue
		}
		if _, ok := s.active.files[ch.Path]; ok {
			continue // harness tool already recorded first-touch original
		}
		// Status A = added after baseline → original does not exist.
		if ch.Status == 'A' {
			s.active.files[ch.Path] = fileOrig{exists: false}
			continue
		}
		// M/D/T: recover bytes from baseline tree.
		data, exists, err := s.shadow.readAtTree(baseline, ch.Path)
		if err != nil {
			reconcileOK = false
			s.active.files[ch.Path] = fileOrig{skipped: true}
			continue
		}
		if !exists {
			// Diff said modified/deleted but baseline lacks path — treat as create.
			s.active.files[ch.Path] = fileOrig{exists: false}
			continue
		}
		if int64(len(data)) > max {
			s.active.files[ch.Path] = fileOrig{exists: true, skipped: true}
			continue
		}
		s.active.files[ch.Path] = fileOrig{exists: true, data: data}
	}
	if reconcileOK {
		// Shadow covered the turn's unknown mutations.
		delete(s.active.uncovered, "bash")
	}
}

func (s *CheckpointStore) trimLocked() {
	max := s.maxTurns
	if max <= 0 {
		max = DefaultCheckpointMaxTurns
	}
	if len(s.stack) <= max {
		return
	}
	// Drop oldest.
	drop := len(s.stack) - max
	s.stack = append([]*turnCheckpoint(nil), s.stack[drop:]...)
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
		s.persistLocked()
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
	s.persistLocked()
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

// Len returns how many committed frames are on the stack.
func (s *CheckpointStore) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.stack)
}

// Loaded reports whether Load successfully read a durable stack (possibly empty).
func (s *CheckpointStore) Loaded() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loaded
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
