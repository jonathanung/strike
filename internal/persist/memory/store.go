// Package memory stores project-scoped key/value entries with optional tags.
// cmd/strike opens the store at startup; tools and internal/frontend/host/local wrap it
// so agent tools and the /memory command share one durable project DB.
package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	fileVersion   = 1
	exportFormat  = "strike.memory"
	exportVersion = 1
	maxKeyLen     = 256
	maxValueLen   = 64 * 1024
	maxTagLen     = 64
	maxTags       = 16
	maxEntries    = 1000
)

var (
	errClosed       = errors.New("memory: store is closed")
	errEmptyKey     = errors.New("memory: key is required")
	errKeyTooLong   = fmt.Errorf("memory: key exceeds %d bytes", maxKeyLen)
	errValueTooLong = fmt.Errorf("memory: value exceeds %d bytes", maxValueLen)
	errTooManyTags  = fmt.Errorf("memory: at most %d tags", maxTags)
	errTagTooLong   = fmt.Errorf("memory: tag exceeds %d bytes", maxTagLen)
	errFull         = fmt.Errorf("memory: at most %d entries", maxEntries)
	// ErrNotFound is returned by Delete when the key is absent.
	ErrNotFound = errors.New("memory: key not found")
)

// Entry is one durable memory record.
type Entry struct {
	Key       string    `json:"key"`
	Value     string    `json:"value"`
	Tags      []string  `json:"tags,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

type fileDoc struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Store is a project-scoped memory database backed by one JSON file.
type Store struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
	closed  bool
}

// Open opens (or creates) the memory file for projectKey under globalRoot/memory.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("memory: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("memory: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "memory")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create memory directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("resolve memory path: %w", err)
	}
	s := &Store{
		path:    path,
		entries: make(map[string]Entry),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the on-disk JSON path.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Get returns the entry for key when present.
func (s *Store) Get(key string) (Entry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Entry{}, false, errClosed
	}
	e, ok := s.entries[key]
	if !ok {
		return Entry{}, false, nil
	}
	return cloneEntry(e), true, nil
}

// List returns entries sorted by key. When tag is non-empty, only entries
// carrying that tag are returned.
func (s *Store) List(tag string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if tag != "" && !hasTag(e.Tags, tag) {
			continue
		}
		out = append(out, cloneEntry(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// Put inserts or replaces the entry for key.
func (s *Store) Put(key, value string, tags []string) error {
	key = strings.TrimSpace(key)
	if err := validateKey(key); err != nil {
		return err
	}
	if len(value) > maxValueLen {
		return errValueTooLong
	}
	cleanTags, err := normalizeTags(tags)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if _, exists := s.entries[key]; !exists && len(s.entries) >= maxEntries {
		return errFull
	}
	s.entries[key] = Entry{
		Key:       key,
		Value:     value,
		Tags:      cleanTags,
		UpdatedAt: time.Now().UTC(),
	}
	return s.persistLocked()
}

// Delete removes key. It is a no-op error when the key is missing.
func (s *Store) Delete(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if _, ok := s.entries[key]; !ok {
		return ErrNotFound
	}
	delete(s.entries, key)
	return s.persistLocked()
}

// Close marks the store closed. The file remains on disk.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// exportDoc is the portable backup format for project memory (git/handoff).
// It is distinct from the on-disk store fileDoc.
type exportDoc struct {
	Format  string  `json:"format"`
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Export writes a versioned portable JSON snapshot to path.
// path must already be cleaned/resolved by the caller.
func (s *Store) Export(path string) error {
	data, err := s.ExportBytes()
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// ExportBytes returns the portable JSON snapshot without touching the filesystem.
func (s *Store) ExportBytes() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	entries := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, cloneEntry(e))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	data, err := json.MarshalIndent(exportDoc{
		Format:  exportFormat,
		Version: exportVersion,
		Entries: entries,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode memory export: %w", err)
	}
	return append(data, '\n'), nil
}

// Import reads a portable JSON snapshot from path and merges or replaces entries.
// When replace is true the store is cleared first; otherwise keys from the file
// overwrite matching keys and new keys are added. Returns the number of entries
// applied from the file.
func (s *Store) Import(path string, replace bool) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read memory import: %w", err)
	}
	return s.ImportBytes(data, replace)
}

// ImportBytes applies a portable JSON snapshot. See Import.
func (s *Store) ImportBytes(data []byte, replace bool) (int, error) {
	doc, err := decodeExport(data)
	if err != nil {
		return 0, err
	}
	clean := make([]Entry, 0, len(doc.Entries))
	seen := make(map[string]struct{}, len(doc.Entries))
	for _, e := range doc.Entries {
		key := strings.TrimSpace(e.Key)
		if err := validateKey(key); err != nil {
			return 0, fmt.Errorf("memory import: invalid key %q: %w", e.Key, err)
		}
		if len(e.Value) > maxValueLen {
			return 0, fmt.Errorf("memory import: key %q: %w", key, errValueTooLong)
		}
		tags, err := normalizeTags(e.Tags)
		if err != nil {
			return 0, fmt.Errorf("memory import: key %q: %w", key, err)
		}
		if _, dup := seen[key]; dup {
			return 0, fmt.Errorf("memory import: duplicate key %q", key)
		}
		seen[key] = struct{}{}
		updated := e.UpdatedAt
		if updated.IsZero() {
			updated = time.Now().UTC()
		}
		clean = append(clean, Entry{
			Key:       key,
			Value:     e.Value,
			Tags:      tags,
			UpdatedAt: updated.UTC(),
		})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errClosed
	}
	if replace {
		s.entries = make(map[string]Entry, len(clean))
	}
	if !replace && len(s.entries)+countNewKeys(s.entries, clean) > maxEntries {
		return 0, errFull
	}
	if replace && len(clean) > maxEntries {
		return 0, errFull
	}
	for _, e := range clean {
		s.entries[e.Key] = e
	}
	if err := s.persistLocked(); err != nil {
		return 0, err
	}
	return len(clean), nil
}

func decodeExport(data []byte) (exportDoc, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return exportDoc{}, fmt.Errorf("memory import: empty file")
	}
	var doc exportDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return exportDoc{}, fmt.Errorf("memory import: bad JSON: %w", err)
	}
	if doc.Format != exportFormat {
		return exportDoc{}, fmt.Errorf("memory import: unsupported format %q", doc.Format)
	}
	if doc.Version != exportVersion {
		return exportDoc{}, fmt.Errorf("memory import: unsupported version %d", doc.Version)
	}
	return doc, nil
}

func countNewKeys(existing map[string]Entry, incoming []Entry) int {
	n := 0
	for _, e := range incoming {
		if _, ok := existing[e.Key]; !ok {
			n++
		}
	}
	return n
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".memory-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create export temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod export temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write export temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync export temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close export temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace export file: %w", err)
	}
	cleanup = false
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read memory: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode memory: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode memory: unsupported version %d", doc.Version)
	}
	for _, e := range doc.Entries {
		if e.Key == "" {
			continue
		}
		s.entries[e.Key] = Entry{
			Key:       e.Key,
			Value:     e.Value,
			Tags:      append([]string(nil), e.Tags...),
			UpdatedAt: e.UpdatedAt,
		}
	}
	return nil
}

func (s *Store) persistLocked() error {
	entries := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		entries = append(entries, cloneEntry(e))
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	data, err := json.MarshalIndent(fileDoc{Version: fileVersion, Entries: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode memory: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".memory-*.tmp")
	if err != nil {
		return fmt.Errorf("create memory temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod memory temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write memory temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync memory temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close memory temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace memory file: %w", err)
	}
	cleanup = false
	return nil
}

func validateKey(key string) error {
	if key == "" {
		return errEmptyKey
	}
	if len(key) > maxKeyLen {
		return errKeyTooLong
	}
	for _, r := range key {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("memory: key contains invalid character")
		}
	}
	return nil
}

func normalizeTags(tags []string) ([]string, error) {
	if len(tags) == 0 {
		return nil, nil
	}
	if len(tags) > maxTags {
		return nil, errTooManyTags
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" {
			continue
		}
		if len(tag) > maxTagLen {
			return nil, errTagTooLong
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	sort.Strings(out)
	return out, nil
}

func hasTag(tags []string, want string) bool {
	for _, t := range tags {
		if t == want {
			return true
		}
	}
	return false
}

func cloneEntry(e Entry) Entry {
	return Entry{
		Key:       e.Key,
		Value:     e.Value,
		Tags:      append([]string(nil), e.Tags...),
		UpdatedAt: e.UpdatedAt,
	}
}
