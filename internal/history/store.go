// Package history stores project-scoped submitted prompts.
package history

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	recordVersion = 1
	maxEntries    = 100
)

type record struct {
	Version   int       `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Prompt    string    `json:"prompt"`
}

type Store struct {
	mu            sync.Mutex
	f             *os.File
	entries       []string
	malformedTail bool
}

// Open opens the history log for projectKey below globalRoot/history.
func Open(globalRoot, projectKey string) (*Store, error) {
	dir := filepath.Join(globalRoot, "history")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create history directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure history directory: %w", err)
	}

	digest := sha256.Sum256([]byte(projectKey))
	path := filepath.Join(dir, hex.EncodeToString(digest[:])+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return nil, fmt.Errorf("secure history file: %w", err)
	}

	s := &Store{f: f}
	if err := s.load(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

// Entries returns an oldest-to-newest snapshot of the recalled prompts.
func (s *Store) Entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.entries...)
}

// Add durably appends an exact submitted prompt. Whitespace-only prompts are ignored.
func (s *Store) Add(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.malformedTail {
		return errors.New("history: cannot append after a malformed final record")
	}

	data, err := json.Marshal(record{
		Version:   recordVersion,
		Timestamp: time.Now().UTC(),
		Prompt:    prompt,
	})
	if err != nil {
		return fmt.Errorf("encode history record: %w", err)
	}
	data = append(data, '\n')
	n, err := s.f.Write(data)
	if err != nil {
		if n != 0 {
			s.malformedTail = true
		}
		return fmt.Errorf("append history record: %w", err)
	}
	if n != len(data) {
		s.malformedTail = true
		return fmt.Errorf("append history record: %w", io.ErrShortWrite)
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("sync history record: %w", err)
	}
	s.remember(prompt)
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.f.Close()
}

func (s *Store) load() error {
	reader := bufio.NewReader(s.f)
	for lineNumber := 1; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) != 0 {
			var rec record
			decodeErr := json.Unmarshal(line, &rec)
			if decodeErr == nil && rec.Version != recordVersion {
				decodeErr = fmt.Errorf("unsupported record version %d", rec.Version)
			}
			if decodeErr != nil {
				if readErr == io.EOF {
					s.malformedTail = true
					return nil
				}
				if _, err := reader.Peek(1); err == io.EOF {
					s.malformedTail = true
					return nil
				} else if err != nil {
					return fmt.Errorf("read history after line %d: %w", lineNumber, err)
				}
				return fmt.Errorf("decode history line %d: %w", lineNumber, decodeErr)
			}
			s.remember(rec.Prompt)
		}

		if readErr == io.EOF {
			return nil
		}
		if readErr != nil {
			return fmt.Errorf("read history line %d: %w", lineNumber, readErr)
		}
	}
}

func (s *Store) remember(prompt string) {
	for i := len(s.entries) - 1; i >= 0; i-- {
		if s.entries[i] == prompt {
			copy(s.entries[i:], s.entries[i+1:])
			s.entries = s.entries[:len(s.entries)-1]
			break
		}
	}
	s.entries = append(s.entries, prompt)
	if len(s.entries) > maxEntries {
		copy(s.entries, s.entries[len(s.entries)-maxEntries:])
		s.entries = s.entries[:maxEntries]
	}
}
