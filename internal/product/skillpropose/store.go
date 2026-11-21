package skillpropose

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Workspace is one workspace's opt-in and its pending and dismissed drafts.
// Enabled is off until the operator turns it on: the scan reads across
// sessions, so it never runs by default.
type Workspace struct {
	Enabled   bool       `json:"enabled"`
	Threshold int        `json:"threshold,omitempty"`
	LastScan  time.Time  `json:"lastScan,omitempty"`
	Pending   []Proposal `json:"pending,omitempty"`
	Dismissed []string   `json:"dismissed,omitempty"`
}

// EffectiveThreshold is Threshold, or the default when unset.
func (w Workspace) EffectiveThreshold() int {
	if w.Threshold == 0 {
		return DefaultThreshold
	}
	return w.Threshold
}

type fileState struct {
	Workspaces map[string]Workspace `json:"workspaces"`
}

// Store keeps every workspace's state in one 0600 JSON file.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore opens (lazily) the state file at path.
func NewStore(path string) *Store { return &Store{path: path} }

// ErrDisabled is returned by Scan when the workspace has not opted in.
var ErrDisabled = errors.New("skill proposals are off for this workspace")

// ErrNotFound is returned for an unknown proposal id.
var ErrNotFound = errors.New("no pending proposal with that id")

func (s *Store) load() (fileState, error) {
	st := fileState{Workspaces: map[string]Workspace{}}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, fmt.Errorf("skill proposals: %w", err)
	}
	if st.Workspaces == nil {
		st.Workspaces = map[string]Workspace{}
	}
	return st, nil
}

func (s *Store) save(st fileState) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".skill-proposals-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
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
	return os.Rename(name, s.path)
}

// update runs fn on one workspace and saves the result.
func (s *Store) update(key string, fn func(*Workspace) error) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return Workspace{}, err
	}
	ws := st.Workspaces[key]
	if err := fn(&ws); err != nil {
		return ws, err
	}
	st.Workspaces[key] = ws
	return ws, s.save(st)
}

// Get returns one workspace's state (zero value when never touched).
func (s *Store) Get(key string) (Workspace, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, err := s.load()
	if err != nil {
		return Workspace{}, err
	}
	return st.Workspaces[key], nil
}

// Settings changes the opt-in and/or the threshold. A nil field is left as
// is. Turning the scan off keeps pending drafts and dismissals.
func (s *Store) Settings(key string, enabled *bool, threshold *int) (Workspace, error) {
	return s.update(key, func(ws *Workspace) error {
		if threshold != nil {
			if *threshold < MinThreshold || *threshold > MaxThreshold {
				return fmt.Errorf("threshold must be between %d and %d", MinThreshold, MaxThreshold)
			}
			ws.Threshold = *threshold
		}
		if enabled != nil {
			ws.Enabled = *enabled
		}
		return nil
	})
}

// Scan replaces the workspace's pending drafts with freshly mined ones.
// Dismissed patterns and names in installed are never proposed.
func (s *Store) Scan(key string, wfs []Workflow, installed map[string]bool, now time.Time) (Workspace, error) {
	return s.update(key, func(ws *Workspace) error {
		if !ws.Enabled {
			return ErrDisabled
		}
		dismissed := map[string]bool{}
		for _, id := range ws.Dismissed {
			dismissed[id] = true
		}
		var pending []Proposal
		for _, p := range Mine(wfs, ws.EffectiveThreshold(), now) {
			if dismissed[p.ID] || installed[p.Name] {
				continue
			}
			pending = append(pending, p)
		}
		ws.Pending = pending
		ws.LastScan = now.UTC()
		return nil
	})
}

// Take removes a pending proposal and returns it. dismiss also records its
// pattern so a later scan does not propose it again.
func (s *Store) Take(key, id string, dismiss bool) (Proposal, error) {
	var got Proposal
	_, err := s.update(key, func(ws *Workspace) error {
		for i, p := range ws.Pending {
			if p.ID != id {
				continue
			}
			got = p
			ws.Pending = append(ws.Pending[:i:i], ws.Pending[i+1:]...)
			if dismiss {
				ws.Dismissed = append(ws.Dismissed, id)
			}
			return nil
		}
		return ErrNotFound
	})
	return got, err
}

// Find returns a pending proposal without removing it.
func (s *Store) Find(key, id string) (Proposal, error) {
	ws, err := s.Get(key)
	if err != nil {
		return Proposal{}, err
	}
	for _, p := range ws.Pending {
		if p.ID == id {
			return p, nil
		}
	}
	return Proposal{}, ErrNotFound
}
