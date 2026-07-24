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

var errClosed = errors.New("history: store is closed")

type record struct {
	Version   int       `json:"version"`
	Timestamp time.Time `json:"timestamp"`
	Prompt    string    `json:"prompt"`
}

type request struct {
	prompt string
	done   chan error
}

type pathState struct {
	mu             sync.Mutex
	refs           int
	appendDisabled bool
	needsSeparator bool
}

var pathStates = struct {
	sync.Mutex
	states map[string]*pathState
}{states: make(map[string]*pathState)}

type Store struct {
	mu      sync.Mutex
	f       *os.File
	entries []string
	path    string
	state   *pathState

	submitMu sync.Mutex
	wakeup   *sync.Cond
	closed   bool
	requests []request
	worker   sync.WaitGroup
	close    sync.Once
	closeErr error
}

// Open opens the history log for projectKey below globalRoot/history.
func Open(globalRoot, projectKey string) (*Store, error) {
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".jsonl"
	path, err := filepath.Abs(filepath.Join(globalRoot, "history", name))
	if err != nil {
		return nil, fmt.Errorf("resolve history path: %w", err)
	}
	state := retainPathState(path)

	f, err := secureOpenHistory(globalRoot, name)
	if err != nil {
		releasePathState(path, state)
		return nil, err
	}

	s := &Store{
		f:     f,
		path:  path,
		state: state,
	}
	s.wakeup = sync.NewCond(&s.submitMu)
	state.mu.Lock()
	err = s.load()
	state.mu.Unlock()
	if err != nil {
		f.Close()
		releasePathState(path, state)
		return nil, err
	}

	s.worker.Add(1)
	go s.run()
	return s, nil
}

// Entries returns an oldest-to-newest snapshot of the recalled prompts.
func (s *Store) Entries() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.entries...)
}

// Enqueue accepts an exact submitted prompt for serial durable append.
func (s *Store) Enqueue(prompt string) <-chan error {
	done := make(chan error, 1)
	s.submitMu.Lock()
	if s.closed {
		s.submitMu.Unlock()
		done <- errClosed
		close(done)
		return done
	}
	s.requests = append(s.requests, request{prompt: prompt, done: done})
	s.wakeup.Signal()
	s.submitMu.Unlock()
	return done
}

// Add durably appends an exact submitted prompt. Whitespace-only prompts are ignored.
func (s *Store) Add(prompt string) error {
	return <-s.Enqueue(prompt)
}

// Close rejects new requests, drains accepted requests, and closes the history file.
func (s *Store) Close() error {
	s.close.Do(func() {
		s.submitMu.Lock()
		s.closed = true
		s.wakeup.Broadcast()
		s.submitMu.Unlock()

		s.worker.Wait()
		s.closeErr = s.f.Close()
		releasePathState(s.path, s.state)
	})
	return s.closeErr
}

func (s *Store) run() {
	defer s.worker.Done()
	for {
		s.submitMu.Lock()
		for len(s.requests) == 0 && !s.closed {
			s.wakeup.Wait()
		}
		if len(s.requests) == 0 {
			s.submitMu.Unlock()
			return
		}
		req := s.requests[0]
		s.requests[0] = request{}
		s.requests = s.requests[1:]
		s.submitMu.Unlock()

		err := s.append(req.prompt)
		req.done <- err
		close(req.done)
	}
}

func (s *Store) append(prompt string) error {
	if strings.TrimSpace(prompt) == "" {
		return nil
	}

	s.state.mu.Lock()
	defer s.state.mu.Unlock()
	if s.state.appendDisabled {
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
	if s.state.needsSeparator {
		data = append([]byte{'\n'}, data...)
	}
	n, err := s.f.Write(data)
	if err != nil {
		if n != 0 {
			s.state.appendDisabled = true
		}
		return fmt.Errorf("append history record: %w", err)
	}
	if n != len(data) {
		s.state.appendDisabled = true
		return fmt.Errorf("append history record: %w", io.ErrShortWrite)
	}
	s.state.needsSeparator = false
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("sync history record: %w", err)
	}
	s.mu.Lock()
	s.remember(prompt)
	s.mu.Unlock()
	return nil
}

func (s *Store) load() error {
	reader := bufio.NewReader(s.f)
	for lineNumber := 1; ; lineNumber++ {
		line, readErr := reader.ReadBytes('\n')
		if len(line) != 0 {
			final := readErr == io.EOF
			if !final {
				if _, err := reader.Peek(1); err == io.EOF {
					final = true
				} else if err != nil {
					return fmt.Errorf("read history after line %d: %w", lineNumber, err)
				}
			}

			var rec record
			if err := json.Unmarshal(line, &rec); err != nil {
				if final {
					s.state.appendDisabled = true
					return nil
				}
				return fmt.Errorf("decode history line %d: %w", lineNumber, err)
			}
			if rec.Version != recordVersion {
				return fmt.Errorf("decode history line %d: unsupported record version %d", lineNumber, rec.Version)
			}
			s.remember(rec.Prompt)
			if readErr == io.EOF {
				s.state.needsSeparator = true
			}
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

func retainPathState(path string) *pathState {
	pathStates.Lock()
	defer pathStates.Unlock()
	state := pathStates.states[path]
	if state == nil {
		state = &pathState{}
		pathStates.states[path] = state
	}
	state.refs++
	return state
}

func releasePathState(path string, state *pathState) {
	pathStates.Lock()
	defer pathStates.Unlock()
	state.refs--
	if state.refs == 0 {
		delete(pathStates.states, path)
	}
}
