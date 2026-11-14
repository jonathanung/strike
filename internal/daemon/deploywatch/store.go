package deploywatch

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

const (
	fileVersion = 1
	maxFinished = 200
)

type fileState struct {
	Version int     `json:"version"`
	Watches []Watch `json:"watches"`
}

// Store is the 0600 watch registry at dir/deploy-watches.json.
type Store struct {
	mu   sync.Mutex
	path string
	st   fileState
}

// DefaultDir is ~/.strike.
func DefaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".strike"
	}
	return filepath.Join(home, ".strike")
}

// Open loads dir/deploy-watches.json. The file is created on the first write.
func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultDir()
	}
	s := &Store{
		path: filepath.Join(dir, "deploy-watches.json"),
		st:   fileState{Version: fileVersion, Watches: []Watch{}},
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("deploywatch: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, fmt.Errorf("deploywatch: parse %s: %w", s.path, err)
	}
	if s.st.Watches == nil {
		s.st.Watches = []Watch{}
	}
	return s, nil
}

// List returns every watch, newest first.
func (s *Store) List() []Watch {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Watch, 0, len(s.st.Watches))
	for i := len(s.st.Watches) - 1; i >= 0; i-- {
		out = append(out, s.st.Watches[i].clone())
	}
	return out
}

// Get returns one watch.
func (s *Store) Get(id string) (Watch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.index(id)
	if i < 0 {
		return Watch{}, ErrNotFound
	}
	return s.st.Watches[i].clone(), nil
}

// Put inserts or replaces w by id and persists. On write failure the
// in-memory state is rolled back.
func (s *Store) Put(w Watch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := slices.Clone(s.st.Watches)
	if i := s.index(w.ID); i >= 0 {
		s.st.Watches[i] = w.clone()
	} else {
		s.st.Watches = append(s.st.Watches, w.clone())
		s.pruneLocked()
	}
	if err := s.saveLocked(); err != nil {
		s.st.Watches = prev
		return err
	}
	return nil
}

// pruneLocked drops the oldest finished watches beyond maxFinished. Active
// watches are never dropped.
func (s *Store) pruneLocked() {
	finished := 0
	for _, w := range s.st.Watches {
		if !w.Active() {
			finished++
		}
	}
	if finished <= maxFinished {
		return
	}
	drop := finished - maxFinished
	kept := s.st.Watches[:0:0]
	for _, w := range s.st.Watches {
		if drop > 0 && !w.Active() {
			drop--
			continue
		}
		kept = append(kept, w)
	}
	s.st.Watches = kept
}

func (s *Store) index(id string) int {
	for i, w := range s.st.Watches {
		if w.ID == id {
			return i
		}
	}
	return -1
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".deploy-watches-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}

func (w Watch) clone() Watch {
	w.Checks = slices.Clone(w.Checks)
	w.Latest = slices.Clone(w.Latest)
	rounds := make([]Round, len(w.Rounds))
	for i, r := range w.Rounds {
		r.Results = slices.Clone(r.Results)
		rounds[i] = r
	}
	w.Rounds = rounds
	return w
}

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "dw_" + hex.EncodeToString(b[:])
}
