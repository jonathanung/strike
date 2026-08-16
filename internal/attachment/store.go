package attachment

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Store is a content-addressed attachment directory under globalRoot/attachments.
type Store struct {
	mu     sync.Mutex
	dir    string
	now    func() time.Time
	closed bool
}

// DefaultDir is ~/.strike/attachments.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "attachments")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "attachments")
}

// Open opens (or creates) the attachment store under globalRoot/attachments.
func Open(globalRoot string) (*Store, error) {
	if strings.TrimSpace(globalRoot) == "" {
		return nil, fmt.Errorf("attachment: global root is empty")
	}
	dir, err := filepath.Abs(filepath.Join(globalRoot, "attachments"))
	if err != nil {
		return nil, fmt.Errorf("attachment: resolve dir: %w", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("attachment: create dir: %w", err)
	}
	return &Store{dir: dir, now: time.Now}, nil
}

// Dir returns the on-disk store directory.
func (s *Store) Dir() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// Close marks the store closed. Blobs remain on disk.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Put classifies and stores raw bytes. Identical content is stored once.
func (s *Store) Put(raw []byte, in PutInput) (Meta, error) {
	if s == nil {
		return Meta{}, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Meta{}, ErrClosed
	}
	max := in.MaxBytes
	if max <= 0 {
		max = DefaultMaxBytes
	}
	if len(raw) == 0 {
		return Meta{}, ErrEmpty
	}
	if len(raw) > max {
		return Meta{}, fmt.Errorf("%w: %d > %d", ErrTooLarge, len(raw), max)
	}
	kind, mime, err := Classify(raw, in.MIME, in.Kind)
	if err != nil {
		return Meta{}, err
	}
	hexSum := SumHex(raw)
	meta := Meta{
		SHA256:    hexSum,
		Kind:      kind,
		MIME:      mime,
		Name:      strings.TrimSpace(in.Name),
		Bytes:     int64(len(raw)),
		CreatedAt: s.now().UTC(),
		SessionID: strings.TrimSpace(in.SessionID),
		Links:     append([]Link(nil), in.Links...),
	}
	blobPath := s.blobPathLocked(hexSum)
	if st, err := os.Stat(blobPath); err == nil && st.Size() > 0 {
		if existing, lerr := s.loadMetaLocked(hexSum); lerr == nil {
			return existing, nil
		}
		if werr := s.writeMetaLocked(meta); werr != nil {
			return Meta{}, werr
		}
		return meta, nil
	}
	if err := os.MkdirAll(filepath.Dir(blobPath), 0o700); err != nil {
		return Meta{}, fmt.Errorf("attachment: create shard: %w", err)
	}
	tmp := blobPath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return Meta{}, fmt.Errorf("attachment: write blob: %w", err)
	}
	if err := os.Rename(tmp, blobPath); err != nil {
		_ = os.Remove(tmp)
		return Meta{}, fmt.Errorf("attachment: rename blob: %w", err)
	}
	if err := s.writeMetaLocked(meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// Get loads blob bytes and metadata by ref or raw hex digest.
func (s *Store) Get(ref string) ([]byte, Meta, error) {
	if s == nil {
		return nil, Meta{}, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, Meta{}, ErrClosed
	}
	hexSum, err := s.resolveHexLocked(ref)
	if err != nil {
		return nil, Meta{}, err
	}
	raw, err := os.ReadFile(s.blobPathLocked(hexSum))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Meta{}, ErrNotFound
		}
		return nil, Meta{}, fmt.Errorf("attachment: read blob: %w", err)
	}
	meta, err := s.loadMetaLocked(hexSum)
	if err != nil {
		meta = Meta{SHA256: hexSum, Bytes: int64(len(raw)), MIME: SniffMIME(raw)}
		meta.Kind = KindFromMIME(meta.MIME)
	}
	return raw, meta, nil
}

// Stat returns metadata without loading blob bytes.
func (s *Store) Stat(ref string) (Meta, error) {
	if s == nil {
		return Meta{}, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Meta{}, ErrClosed
	}
	hexSum, err := s.resolveHexLocked(ref)
	if err != nil {
		return Meta{}, err
	}
	meta, err := s.loadMetaLocked(hexSum)
	if err != nil {
		return Meta{}, err
	}
	return meta, nil
}

// RetentionPolicy bounds on-disk attachment trees. Zero fields are unlimited.
type RetentionPolicy struct {
	MaxFiles int
	MaxAge   time.Duration
	MaxBytes int64
}

// RetentionResult summarizes ApplyRetention deletions.
type RetentionResult struct {
	Deleted []string
	Freed   int64
}

// ApplyRetention deletes oldest blobs that exceed the policy.
func (s *Store) ApplyRetention(p RetentionPolicy) (RetentionResult, error) {
	var out RetentionResult
	if s == nil {
		return out, ErrClosed
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return out, ErrClosed
	}
	if p.MaxFiles <= 0 && p.MaxAge <= 0 && p.MaxBytes <= 0 {
		return out, nil
	}
	type item struct {
		hex  string
		path string
		size int64
		mod  time.Time
	}
	var items []item
	err := filepath.Walk(s.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || strings.HasSuffix(info.Name(), ".meta.json") || strings.HasSuffix(info.Name(), ".tmp") {
			return nil
		}
		name := info.Name()
		if len(name) != 64 {
			return nil
		}
		items = append(items, item{hex: name, path: path, size: info.Size(), mod: info.ModTime()})
		return nil
	})
	if err != nil {
		return out, fmt.Errorf("attachment: walk: %w", err)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].mod.Before(items[j].mod) })
	now := s.now()
	keep := make([]bool, len(items))
	for i := range keep {
		keep[i] = true
	}
	if p.MaxAge > 0 {
		cutoff := now.Add(-p.MaxAge)
		for i, it := range items {
			if it.mod.Before(cutoff) {
				keep[i] = false
			}
		}
	}
	if p.MaxFiles > 0 {
		n := 0
		for i := len(items) - 1; i >= 0; i-- {
			if !keep[i] {
				continue
			}
			n++
			if n > p.MaxFiles {
				keep[i] = false
			}
		}
	}
	if p.MaxBytes > 0 {
		var remain int64
		for i, it := range items {
			if keep[i] {
				remain += it.size
			}
		}
		for i := 0; i < len(items) && remain > p.MaxBytes; i++ {
			if !keep[i] {
				continue
			}
			keep[i] = false
			remain -= items[i].size
		}
	}
	for i, it := range items {
		if keep[i] {
			continue
		}
		if err := os.Remove(it.path); err != nil && !os.IsNotExist(err) {
			return out, fmt.Errorf("attachment: delete %s: %w", it.hex, err)
		}
		_ = os.Remove(s.metaPathLocked(it.hex))
		out.Deleted = append(out.Deleted, RefFor(it.hex))
		out.Freed += it.size
	}
	return out, nil
}

func (s *Store) blobPathLocked(hexSum string) string {
	if len(hexSum) >= 2 {
		return filepath.Join(s.dir, hexSum[:2], hexSum)
	}
	return filepath.Join(s.dir, hexSum)
}

func (s *Store) metaPathLocked(hexSum string) string {
	return s.blobPathLocked(hexSum) + ".meta.json"
}

func (s *Store) resolveHexLocked(ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if hexSum, ok := ParseRef(ref); ok {
		return hexSum, nil
	}
	if len(ref) == 64 {
		if _, ok := ParseRef(RefFor(ref)); ok {
			return strings.ToLower(ref), nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidRef, ref)
}

func (s *Store) writeMetaLocked(meta Meta) error {
	path := s.metaPathLocked(meta.SHA256)
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("attachment: encode meta: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("attachment: write meta: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("attachment: rename meta: %w", err)
	}
	return nil
}

func (s *Store) loadMetaLocked(hexSum string) (Meta, error) {
	raw, err := os.ReadFile(s.metaPathLocked(hexSum))
	if err != nil {
		if os.IsNotExist(err) {
			return Meta{}, ErrNotFound
		}
		return Meta{}, fmt.Errorf("attachment: read meta: %w", err)
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Meta{}, fmt.Errorf("attachment: decode meta: %w", err)
	}
	return meta, nil
}
