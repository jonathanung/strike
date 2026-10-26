// Package knowledge stores pinned document paths for a project and a global
// set. Pins are plain files under ~/.strike/knowledge. The agent reads the
// path list at prompt composition; file bodies stay on disk and are opened
// through the existing file API.
package knowledge

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

	"github.com/jonathanung/strike/pkg/redact"
)

const (
	fileVersion = 1
	maxPins     = 200
	maxPathLen  = 1024
	maxNoteLen  = 4 * 1024
	// GlobalKey selects the cross-project pin file. It is not a filesystem path.
	GlobalKey = "strike:knowledge:global"
	// MaxAutoLoadPins caps how many pins are injected into a prompt.
	MaxAutoLoadPins = 32
	// MaxAutoLoadChars caps the prompt text built from pins.
	MaxAutoLoadChars = 4000
)

var (
	errClosed      = errors.New("knowledge: store is closed")
	errEmptyPath   = errors.New("knowledge: path is required")
	errPathTooLong = fmt.Errorf("knowledge: path exceeds %d bytes", maxPathLen)
	errNoteTooLong = fmt.Errorf("knowledge: note exceeds %d bytes", maxNoteLen)
	errFull        = fmt.Errorf("knowledge: at most %d pins", maxPins)
	// ErrNotFound is returned by Delete when the path is not pinned.
	ErrNotFound = errors.New("knowledge: path not found")
)

// Pin is one pinned path.
type Pin struct {
	Path     string    `json:"path"`
	Note     string    `json:"note,omitempty"`
	Author   string    `json:"author,omitempty"`
	PinnedAt time.Time `json:"pinned_at"`
}

type fileDoc struct {
	Version int   `json:"version"`
	Pins    []Pin `json:"pins"`
}

// Store is one knowledge file (project or global).
type Store struct {
	mu         sync.Mutex
	path       string
	projectKey string
	pins       map[string]Pin
	closed     bool
}

// Open opens or creates the knowledge file for projectKey under globalRoot/knowledge.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("knowledge: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("knowledge: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "knowledge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create knowledge directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("resolve knowledge path: %w", err)
	}
	s := &Store{
		path:       path,
		projectKey: projectKey,
		pins:       make(map[string]Pin),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.rewriteProjectionLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

// Scope is "global" for GlobalKey and "project" otherwise.
func (s *Store) Scope() string {
	if s != nil && s.projectKey == GlobalKey {
		return "global"
	}
	return "project"
}

// Path returns the JSON file path.
func (s *Store) Path() string { return s.path }

// MarkdownPath returns the plain-text projection.
func (s *Store) MarkdownPath() string {
	return strings.TrimSuffix(s.path, ".json") + ".md"
}

// List returns pins sorted by path.
func (s *Store) List() ([]Pin, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Pin, 0, len(s.pins))
	for _, p := range s.pins {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// Put pins path, replacing an existing pin with the same path.
// Note and path are credential-scrubbed before they are stored.
func (s *Store) Put(path, note, author string) (Pin, error) {
	pin, err := scrubPin(Pin{Path: path, Note: note, Author: author})
	if err != nil {
		return Pin{}, err
	}
	pin.PinnedAt = time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Pin{}, errClosed
	}
	if _, exists := s.pins[pin.Path]; !exists && len(s.pins) >= maxPins {
		return Pin{}, errFull
	}
	s.pins[pin.Path] = pin
	if err := s.persistLocked(); err != nil {
		return Pin{}, err
	}
	return pin, nil
}

// Delete removes a pin. The path is matched after the same scrub applied on write.
func (s *Store) Delete(path string) error {
	key := redact.String(strings.TrimSpace(path))
	if err := validatePath(key); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if _, ok := s.pins[key]; !ok {
		return ErrNotFound
	}
	delete(s.pins, key)
	return s.persistLocked()
}

// Close marks the store closed. The file remains on disk.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// FormatAutoLoad renders a capped path list for the system prompt.
func FormatAutoLoad(scope string, pins []Pin) (string, int) {
	if len(pins) == 0 {
		return "", 0
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Path < pins[j].Path })
	var b strings.Builder
	fmt.Fprintf(&b, "Pinned knowledge (%s). These are paths, not file bodies:\n", scope)
	omitted := 0
	n := 0
	for _, p := range pins {
		line := "- " + p.Path
		if p.Note != "" {
			line += " — " + p.Note
		}
		line += "\n"
		if n >= MaxAutoLoadPins || b.Len()+len(line) > MaxAutoLoadChars {
			omitted++
			continue
		}
		b.WriteString(line)
		n++
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "(%d more pins omitted)\n", omitted)
	}
	return b.String(), omitted
}

func scrubPin(p Pin) (Pin, error) {
	p.Path = redact.String(strings.TrimSpace(p.Path))
	p.Note = redact.String(strings.TrimSpace(p.Note))
	p.Author = redact.String(strings.TrimSpace(p.Author))
	if err := validatePath(p.Path); err != nil {
		return Pin{}, err
	}
	if len(p.Note) > maxNoteLen {
		return Pin{}, errNoteTooLong
	}
	return p, nil
}

func validatePath(path string) error {
	if path == "" {
		return errEmptyPath
	}
	if len(path) > maxPathLen {
		return errPathTooLong
	}
	if strings.ContainsRune(path, 0) || strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf("knowledge: path contains invalid character")
	}
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read knowledge: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode knowledge: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode knowledge: unsupported version %d", doc.Version)
	}
	changed := false
	for _, p := range doc.Pins {
		clean, err := scrubPin(p)
		if err != nil {
			continue
		}
		if clean.Path != p.Path || clean.Note != p.Note || clean.Author != p.Author {
			changed = true
		}
		clean.PinnedAt = p.PinnedAt.UTC()
		s.pins[clean.Path] = clean
	}
	if changed {
		return s.persistLocked()
	}
	return nil
}

func (s *Store) persistLocked() error {
	pins := make([]Pin, 0, len(s.pins))
	for _, p := range s.pins {
		pins = append(pins, p)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Path < pins[j].Path })
	data, err := json.MarshalIndent(fileDoc{Version: fileVersion, Pins: pins}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode knowledge: %w", err)
	}
	data = append(data, '\n')
	if err := writeAtomic(s.path, data); err != nil {
		return err
	}
	return s.rewriteProjectionLocked()
}

func (s *Store) rewriteProjectionLocked() error {
	pins := make([]Pin, 0, len(s.pins))
	for _, p := range s.pins {
		pins = append(pins, p)
	}
	sort.Slice(pins, func(i, j int) bool { return pins[i].Path < pins[j].Path })
	var b strings.Builder
	fmt.Fprintf(&b, "# Strike knowledge (%s)\n\n", s.Scope())
	b.WriteString("Pinned paths. The agent sees this list at session start. File bodies are not copied here.\n\n")
	if len(pins) == 0 {
		b.WriteString("_No pins._\n")
	}
	for _, p := range pins {
		fmt.Fprintf(&b, "- `%s`", p.Path)
		if p.Note != "" {
			fmt.Fprintf(&b, " — %s", p.Note)
		}
		if p.Author != "" {
			fmt.Fprintf(&b, " (%s)", p.Author)
		}
		b.WriteString("\n")
	}
	return writeAtomic(s.MarkdownPath(), []byte(b.String()))
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".knowledge-*.tmp")
	if err != nil {
		return fmt.Errorf("create knowledge temp: %w", err)
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
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace knowledge file: %w", err)
	}
	cleanup = false
	return nil
}
