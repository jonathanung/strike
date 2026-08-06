// Package session persists sessions as JSONL logs of protocol events and
// coordinates concurrent open logs via Manager. The event stream is the
// transcript, so resume/replay is re-reading the log. cmd/strike is the only
// importer: it tees engine events through a store (or Manager) on their way to
// the frontend. internal/tui never imports this package directly.
package session

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/secret"
)

type Store struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// DefaultDir is ~/.strike/sessions — ~/.strike is strike's home for all
// user-level state. Existing ~/.strike directory symlinks are resolved.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "sessions")
	}
	root := filepath.Join(home, ".strike")
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Join(root, "sessions")
}

// newIDLast is advanced under newIDMu so rapid NewID calls stay strictly
// increasing and therefore lexically sortable despite random suffixes.
var (
	newIDMu   sync.Mutex
	newIDLast time.Time
)

// NewID returns a UTC timestamp-first, filename-safe, collision-resistant
// session identifier. Lexical order matches creation order.
func NewID() string {
	newIDMu.Lock()
	defer newIDMu.Unlock()
	now := time.Now().UTC()
	if !now.After(newIDLast) {
		now = newIDLast.Add(time.Nanosecond)
	}
	newIDLast = now
	// Fixed-width fractional seconds so equal-length prefixes sort by time.
	return now.Format("20060102T150405.000000000Z") + "-" + rand.Text()
}

// LogPath is the JSONL event-log path for a session id under dir.
func LogPath(dir, id string) string {
	return filepath.Join(dir, id+".jsonl")
}

func Open(dir, id string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := LogPath(dir, id)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Store{f: f, enc: json.NewEncoder(f)}, nil
}

func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Name()
}

func (s *Store) Append(ev protocol.Event) error {
	// Scrub credentials before JSONL persist so session logs, timeline export
	// consumers, and diagnostic bundles never retain raw secrets.
	env, err := protocol.Wrap(secret.RedactEvent(ev))
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(env)
}

// Sync flushes the underlying JSONL file so concurrent readers see all
// appended events (e.g. Fork copying an open session).
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.f == nil {
		return nil
	}
	return s.f.Sync()
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

// idCreatedAt parses the UTC timestamp prefix of a NewID value when present.
func idCreatedAt(id string) (time.Time, bool) {
	// NewID: 20060102T150405.000000000Z-<suffix>
	i := strings.Index(id, "Z-")
	if i <= 0 {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102T150405.999999999Z", id[:i+1])
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}

// Replay reads all events back from a session log.
func Replay(path string) ([]protocol.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []protocol.Event
	scanner := bufio.NewScanner(f)
	// Multimodal user.message lines can carry multi-MiB base64 images.
	scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
	line := 0
	for scanner.Scan() {
		line++
		var env protocol.Envelope
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		ev, err := env.Decode()
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		events = append(events, ev)
	}
	return events, scanner.Err()
}

// ReplaySlice returns a bounded ordered slice of events from a session log.
// offset is 0-based; limit caps the number of events returned (must be > 0).
// total is the full event count. Never loads more than needed into the result
// slice, though the file is still scanned to compute total and reach offset.
func ReplaySlice(path string, offset, limit int) (events []protocol.Event, total int, err error) {
	if offset < 0 {
		return nil, 0, fmt.Errorf("offset must be >= 0")
	}
	if limit <= 0 {
		return nil, 0, fmt.Errorf("limit must be > 0")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
	line := 0
	for scanner.Scan() {
		line++
		if total >= offset && len(events) < limit {
			var env protocol.Envelope
			if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
				return nil, 0, fmt.Errorf("line %d: %w", line, err)
			}
			ev, err := env.Decode()
			if err != nil {
				return nil, 0, fmt.Errorf("line %d: %w", line, err)
			}
			events = append(events, ev)
		}
		total++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return events, total, nil
}

// ReplayLast returns up to n trailing events from a session log (bounded).
// n must be > 0. Order is chronological (oldest of the tail first).
func ReplayLast(path string, n int) (events []protocol.Event, total int, err error) {
	if n <= 0 {
		return nil, 0, fmt.Errorf("n must be > 0")
	}
	// Ring of the last n events while counting total.
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	ring := make([]protocol.Event, 0, n)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
	line := 0
	for scanner.Scan() {
		line++
		var env protocol.Envelope
		if err := json.Unmarshal(scanner.Bytes(), &env); err != nil {
			return nil, 0, fmt.Errorf("line %d: %w", line, err)
		}
		ev, err := env.Decode()
		if err != nil {
			return nil, 0, fmt.Errorf("line %d: %w", line, err)
		}
		if len(ring) < n {
			ring = append(ring, ev)
		} else {
			copy(ring, ring[1:])
			ring[n-1] = ev
		}
		total++
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, err
	}
	return ring, total, nil
}
