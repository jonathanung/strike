package schedule

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type fileState struct {
	Schedules []Schedule `json:"schedules"`
}

// Store is the ~/.strike/schedules.json registry. File mode is 0600.
// Script run output lives beside it under runsDir (see runs.go).
type Store struct {
	mu      sync.Mutex
	path    string
	runsDir string
	st      fileState
}

// OpenStore loads dir/schedules.json, creating nothing until the first write.
// Runs left "started" by a dead process become "interrupted", and sidecar
// output files whose run or schedule no longer exists are removed.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("schedule: empty dir")
	}
	// Sidecars live under schedules/runs, not dir/runs: ~/.strike/runs is the
	// session snapshot root (eval/replay, trace retention) and must not be swept.
	s := &Store{path: filepath.Join(dir, "schedules.json"), runsDir: filepath.Join(dir, "schedules", "runs")}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.reconcileOutputsLocked()
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("schedule: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(data, &s.st); err != nil {
		return nil, fmt.Errorf("schedule: parse %s: %w", s.path, err)
	}
	s.reconcileLocked()
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	s.reconcileOutputsLocked()
	return s, nil
}

func (s *Store) reconcileLocked() {
	for i := range s.st.Schedules {
		sch := &s.st.Schedules[i]
		changed := false
		for j := range sch.Runs {
			if sch.Runs[j].Status == StatusStarted {
				sch.Runs[j].Status = StatusInterrupted
				if sch.Runs[j].Error == "" {
					sch.Runs[j].Error = "daemon restarted before the run finished"
				}
				changed = true
			}
		}
		if sch.LastStatus == StatusStarted || sch.LastStatus == StatusQueued {
			sch.LastStatus = StatusInterrupted
			if sch.LastError == "" {
				sch.LastError = "daemon restarted before the run finished"
			}
			changed = true
		}
		if changed && len(sch.Runs) > maxRuns {
			sch.Runs = sch.Runs[:maxRuns]
		}
	}
}

func (s *Store) List() []Schedule {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Schedule, len(s.st.Schedules))
	for i := range s.st.Schedules {
		out[i] = cloneSchedule(s.st.Schedules[i])
	}
	return out
}

func (s *Store) Get(id string) (Schedule, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Schedules {
		if s.st.Schedules[i].ID == id {
			return cloneSchedule(s.st.Schedules[i]), true
		}
	}
	return Schedule{}, false
}

func (s *Store) Insert(sch Schedule) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.st.Schedules) >= maxSchedules {
		return ErrLimit
	}
	for i := range s.st.Schedules {
		if s.st.Schedules[i].ID == sch.ID {
			return fmt.Errorf("schedule: id %q exists", sch.ID)
		}
	}
	s.st.Schedules = append(s.st.Schedules, cloneSchedule(sch))
	return s.saveLocked()
}

func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Schedules {
		if s.st.Schedules[i].ID == id {
			s.st.Schedules = append(s.st.Schedules[:i], s.st.Schedules[i+1:]...)
			if err := s.saveLocked(); err != nil {
				return err
			}
			s.removeScheduleOutputs(id)
			return nil
		}
	}
	return ErrNotFound
}

// Mutate applies fn to a copy of id and persists it. fn sees a deep copy.
// Sidecar output of any run fn dropped (the 50-run cap) is removed.
func (s *Store) Mutate(id string, fn func(*Schedule) error) (Schedule, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.st.Schedules {
		if s.st.Schedules[i].ID != id {
			continue
		}
		prev := s.st.Schedules[i].Runs
		cur := cloneSchedule(s.st.Schedules[i])
		if err := fn(&cur); err != nil {
			return Schedule{}, err
		}
		s.st.Schedules[i] = cloneSchedule(cur)
		if err := s.saveLocked(); err != nil {
			return Schedule{}, err
		}
		s.removeDroppedOutputs(id, prev, cur.Runs)
		return cloneSchedule(cur), nil
	}
	return Schedule{}, ErrNotFound
}

func (s *Store) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
