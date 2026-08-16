package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	checkpointStackFormat  = "strike.checkpoints"
	checkpointStackVersion = 1
	checkpointStackFile    = "stack.json"
	checkpointBlobsDir     = "blobs"
)

// persistedStack is the on-disk document under PersistDir/stack.json.
type persistedStack struct {
	Format  string          `json:"format"`
	Version int             `json:"version"`
	Turns   []persistedTurn `json:"turns"`
}

type persistedTurn struct {
	TurnID    string          `json:"turnId"`
	Uncovered []string        `json:"uncovered,omitempty"`
	Files     []persistedFile `json:"files"`
}

type persistedFile struct {
	Path    string `json:"path"`
	Exists  bool   `json:"exists"`
	Skipped bool   `json:"skipped,omitempty"`
	// Blob is the content file name under blobs/<turnId>/ when exists && !skipped.
	Blob string `json:"blob,omitempty"`
}

// Load reads a previously persisted stack from PersistDir into memory.
// Missing directory/file yields an empty stack and nil error (fresh session).
// Configure must be called first. Replaces any in-memory stack.
func (s *CheckpointStore) Load() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loaded = false
	if s.persistDir == "" {
		return nil
	}
	path := filepath.Join(s.persistDir, checkpointStackFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.loaded = true
			s.stack = nil
			return nil
		}
		return fmt.Errorf("checkpoint load: %w", err)
	}
	var doc persistedStack
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("checkpoint load: bad JSON: %w", err)
	}
	if doc.Format != checkpointStackFormat {
		return fmt.Errorf("checkpoint load: unsupported format %q", doc.Format)
	}
	if doc.Version != checkpointStackVersion {
		return fmt.Errorf("checkpoint load: unsupported version %d", doc.Version)
	}
	stack := make([]*turnCheckpoint, 0, len(doc.Turns))
	for _, t := range doc.Turns {
		frame, err := s.decodeTurnLocked(t)
		if err != nil {
			return err
		}
		stack = append(stack, frame)
	}
	s.stack = stack
	s.loaded = true
	return nil
}

func (s *CheckpointStore) decodeTurnLocked(t persistedTurn) (*turnCheckpoint, error) {
	frame := &turnCheckpoint{
		turnID:    t.TurnID,
		files:     make(map[string]fileOrig, len(t.Files)),
		uncovered: make(map[string]struct{}, len(t.Uncovered)),
	}
	for _, r := range t.Uncovered {
		r = strings.TrimSpace(r)
		if r != "" {
			frame.uncovered[r] = struct{}{}
		}
	}
	for _, f := range t.Files {
		p := filepath.Clean(strings.TrimSpace(f.Path))
		if p == "" {
			continue
		}
		orig := fileOrig{exists: f.Exists, skipped: f.Skipped}
		if f.Exists && !f.Skipped {
			blob := strings.TrimSpace(f.Blob)
			if blob == "" || strings.Contains(blob, "..") || strings.Contains(blob, "/") ||
				strings.Contains(blob, "\\") {
				orig.skipped = true
			} else {
				bpath := filepath.Join(s.persistDir, checkpointBlobsDir, sanitizeTurnDir(t.TurnID), blob)
				data, err := os.ReadFile(bpath)
				if err != nil {
					orig.skipped = true
				} else {
					orig.data = data
				}
			}
		}
		frame.files[p] = orig
	}
	return frame, nil
}

// persistLocked writes the full stack to PersistDir. Best-effort: errors are
// swallowed so undo never fails closed on disk issues. Caller holds s.mu.
func (s *CheckpointStore) persistLocked() {
	if s == nil || s.persistDir == "" {
		return
	}
	if err := s.writeStackLocked(); err != nil {
		// Soft-fail: in-memory stack remains authoritative for this process.
		return
	}
}

func (s *CheckpointStore) writeStackLocked() error {
	if err := os.MkdirAll(s.persistDir, 0o700); err != nil {
		return err
	}
	doc := persistedStack{
		Format:  checkpointStackFormat,
		Version: checkpointStackVersion,
		Turns:   make([]persistedTurn, 0, len(s.stack)),
	}
	// Track blob dirs we still need so we can prune orphans after write.
	keepTurns := make(map[string]struct{}, len(s.stack))
	for _, frame := range s.stack {
		pt, err := s.encodeTurnLocked(frame)
		if err != nil {
			return err
		}
		doc.Turns = append(doc.Turns, pt)
		keepTurns[sanitizeTurnDir(frame.turnID)] = struct{}{}
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(s.persistDir, ".stack-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	final := filepath.Join(s.persistDir, checkpointStackFile)
	if err := os.Rename(tmpName, final); err != nil {
		return err
	}
	ok = true
	s.pruneBlobDirsLocked(keepTurns)
	return nil
}

func (s *CheckpointStore) encodeTurnLocked(frame *turnCheckpoint) (persistedTurn, error) {
	pt := persistedTurn{
		TurnID:    frame.turnID,
		Uncovered: uncoveredReasons(frame.uncovered),
		Files:     make([]persistedFile, 0, len(frame.files)),
	}
	turnDir := filepath.Join(s.persistDir, checkpointBlobsDir, sanitizeTurnDir(frame.turnID))
	paths := make([]string, 0, len(frame.files))
	for p := range frame.files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		orig := frame.files[p]
		pf := persistedFile{
			Path:    p,
			Exists:  orig.exists,
			Skipped: orig.skipped,
		}
		if orig.exists && !orig.skipped {
			sum := sha256.Sum256([]byte(p))
			blob := hex.EncodeToString(sum[:16]) // 128-bit path key is enough
			if err := os.MkdirAll(turnDir, 0o700); err != nil {
				return pt, err
			}
			bpath := filepath.Join(turnDir, blob)
			if err := os.WriteFile(bpath, orig.data, 0o600); err != nil {
				return pt, err
			}
			pf.Blob = blob
		}
		pt.Files = append(pt.Files, pf)
	}
	return pt, nil
}

func (s *CheckpointStore) pruneBlobDirsLocked(keep map[string]struct{}) {
	root := filepath.Join(s.persistDir, checkpointBlobsDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if _, ok := keep[name]; ok {
			continue
		}
		_ = os.RemoveAll(filepath.Join(root, name))
	}
}

// RemoveCheckpointDir deletes durable checkpoint data for a session id.
// Missing dirs are ignored. Safe for retention / session destroy hooks.
func RemoveCheckpointDir(sessionID string) error {
	dir := DefaultCheckpointDir(sessionID)
	if dir == "" {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// sanitizeTurnDir maps a turn id to a single path segment.
func sanitizeTurnDir(turnID string) string {
	turnID = strings.TrimSpace(turnID)
	if turnID == "" {
		return "_empty"
	}
	var b strings.Builder
	for _, r := range turnID {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "." || out == ".." {
		return "_dot"
	}
	return out
}

// CheckpointsRoot returns ~/.strike/checkpoints (for restore / listing).
func CheckpointsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "strike", "checkpoints")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "checkpoints")
}
