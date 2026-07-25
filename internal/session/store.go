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
)

type Store struct {
	mu  sync.Mutex
	f   *os.File
	enc *json.Encoder
}

// DefaultDir is ~/.strike/sessions — ~/.strike is strike's home for all
// user-level state.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "strike", "sessions")
	}
	return filepath.Join(home, ".strike", "sessions")
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
	env, err := protocol.Wrap(ev)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(env)
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
	scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)
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
