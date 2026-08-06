package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// FileState tracks the last successful read of each path so edit/write can
// refuse to clobber content the model has not re-read after an external
// change (for example /vim). Snapshots store mtime/size and, when available,
// a content hash for stronger concurrent-modification detection.
//
// Composes with CheckpointStore (#540 undo snapshots) and PathOwnership (#772
// multi-agent overlap leases): FileState is per-agent freshness; ownership is
// cross-agent claims; checkpoints are per-turn restore bytes. A nil receiver
// is a no-op on every method.
type FileState struct {
	mu    sync.Mutex
	reads map[string]fileSnapshot // absolute path → snapshot
	dirty map[string]struct{}     // absolute paths marked stale by FilesChanged
}

type fileSnapshot struct {
	modTime time.Time
	size    int64
	hash    string // sha256 hex of full content when recorded; empty if unknown
}

// ContentHash returns the lowercase hex SHA-256 of data. Used for optional
// baseHash preconditions on edit/apply_patch and for FileState snapshots.
func ContentHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Record stores a post-read snapshot for path and clears any dirty flag.
// No-op when s is nil or info is nil. Prefer RecordBytes when content is known
// so concurrent same-size edits are still detected.
func (s *FileState) Record(path string, info os.FileInfo) {
	if s == nil || info == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads == nil {
		s.reads = make(map[string]fileSnapshot)
	}
	s.reads[path] = fileSnapshot{modTime: info.ModTime(), size: info.Size()}
	delete(s.dirty, path)
}

// RecordBytes stores a post-read snapshot including a content hash.
// No-op when s is nil. info may be nil (hash-only); path must be non-empty.
func (s *FileState) RecordBytes(path string, info os.FileInfo, data []byte) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reads == nil {
		s.reads = make(map[string]fileSnapshot)
	}
	snap := fileSnapshot{hash: ContentHash(data)}
	if info != nil {
		snap.modTime = info.ModTime()
		snap.size = info.Size()
	} else {
		snap.size = int64(len(data))
	}
	s.reads[path] = snap
	delete(s.dirty, path)
}

// Forget drops any snapshot and dirty flag for path (e.g. after delete/move
// of the source). No-op when s is nil or path is empty.
func (s *FileState) Forget(path string) {
	if s == nil || path == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.reads, path)
	delete(s.dirty, path)
}

// MarkDirty flags paths as externally changed. A subsequent edit/write of a
// previously-read path fails until Record (re-read) clears the flag. Paths
// that were never read are unaffected.
func (s *FileState) MarkDirty(paths ...string) {
	if s == nil || len(paths) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dirty == nil {
		s.dirty = make(map[string]struct{})
	}
	for _, path := range paths {
		if path == "" {
			continue
		}
		s.dirty[path] = struct{}{}
	}
}

// CheckFresh reports an error when path was previously read and is no longer
// trustworthy: either marked dirty, missing on disk, mtime/size drifted, or
// (when a content hash was stored) the on-disk hash no longer matches.
// Never-read paths are treated as fresh. Failures use precondition_failed.
func (s *FileState) CheckFresh(path, display string) error {
	if s == nil || path == "" {
		return nil
	}
	s.mu.Lock()
	snap, hasSnap := s.reads[path]
	_, isDirty := s.dirty[path]
	s.mu.Unlock()
	if !hasSnap {
		return nil
	}
	if isDirty {
		return PreconditionFailed(fmt.Sprintf("%s was modified externally since it was read; read it again before editing", display))
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PreconditionFailed(fmt.Sprintf("%s was modified externally since it was read (file missing); read it again before editing", display))
		}
		return err
	}
	if !info.ModTime().Equal(snap.modTime) || info.Size() != snap.size {
		return PreconditionFailed(fmt.Sprintf("%s was modified externally since it was read; read it again before editing", display))
	}
	if snap.hash != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if ContentHash(data) != snap.hash {
			return PreconditionFailed(fmt.Sprintf("%s was modified externally since it was read; read it again before editing", display))
		}
	}
	return nil
}

// CheckBaseHash verifies that the on-disk content of path matches the expected
// lowercase (or any-case) hex SHA-256. Empty expected is a no-op. Failures use
// precondition_failed so callers can fail closed on stale bases.
func CheckBaseHash(path, expected, display string) error {
	expected = strings.TrimSpace(expected)
	if expected == "" || path == "" {
		return nil
	}
	expected = strings.ToLower(expected)
	if len(expected) != 64 {
		return InvalidArgs(fmt.Sprintf("baseHash for %s must be a 64-char sha256 hex digest", display))
	}
	for _, r := range expected {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return InvalidArgs(fmt.Sprintf("baseHash for %s must be a 64-char sha256 hex digest", display))
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PreconditionFailed(fmt.Sprintf("%s: baseHash precondition failed (file missing)", display))
		}
		return err
	}
	got := ContentHash(data)
	if got != expected {
		return PreconditionFailed(fmt.Sprintf("%s: baseHash mismatch (file changed since the hash was taken); re-read before editing", display))
	}
	return nil
}

// CheckContentUnchanged fails with precondition_failed when on-disk bytes no
// longer match expected (the content the tool planned against). Used to close
// the race between read/validate and the atomic write.
func CheckContentUnchanged(path string, expected []byte, display string) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return PreconditionFailed(fmt.Sprintf("%s changed concurrently (file missing); re-read before editing", display))
		}
		return err
	}
	if ContentHash(data) != ContentHash(expected) {
		return PreconditionFailed(fmt.Sprintf("%s changed concurrently between read and write; re-read before editing", display))
	}
	return nil
}
