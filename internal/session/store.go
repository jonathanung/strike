// Package session persists a session as a JSONL log of protocol events —
// the event stream is the transcript, so resume/replay is just re-reading
// the log.
package session

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

type Store struct {
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

// NewID returns a sortable session identifier.
func NewID() string {
	return time.Now().UTC().Format("20060102-150405")
}

func Open(dir, id string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &Store{f: f, enc: json.NewEncoder(f)}, nil
}

func (s *Store) Path() string { return s.f.Name() }

func (s *Store) Append(ev protocol.Event) error {
	env, err := protocol.Wrap(ev)
	if err != nil {
		return err
	}
	return s.enc.Encode(env)
}

func (s *Store) Close() error { return s.f.Close() }

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
