package tool

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// FileState tracks the last successful read of each path so edit/write can
// refuse to clobber content the model has not re-read after an external
// change (for example /vim). A nil receiver is a no-op on every method.
type FileState struct {
	mu    sync.Mutex
	reads map[string]fileSnapshot // absolute path → snapshot
	dirty map[string]struct{}     // absolute paths marked stale by FilesChanged
}

type fileSnapshot struct {
	modTime time.Time
	size    int64
}

// Record stores a post-read snapshot for path and clears any dirty flag.
// No-op when s is nil or info is nil.
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
// trustworthy: either marked dirty, missing on disk, or mtime/size drifted
// from the stored snapshot. Never-read paths are treated as fresh.
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
		return fmt.Errorf("%s was modified externally since it was read; read it again before editing", display)
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s was modified externally since it was read (file missing); read it again before editing", display)
		}
		return err
	}
	if !info.ModTime().Equal(snap.modTime) || info.Size() != snap.size {
		return fmt.Errorf("%s was modified externally since it was read; read it again before editing", display)
	}
	return nil
}
