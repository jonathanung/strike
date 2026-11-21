package spec

import (
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

// Store reads and writes specs under one root directory (normally
// <workspace>/.strike/specs). Mutations are serialized by a mutex and each
// file is replaced atomically.
type Store struct {
	mu   sync.Mutex
	root string
	now  func() time.Time
}

// Open returns a store rooted at dir. The directory is created on first
// write, not here, so opening never touches the workspace.
func Open(dir string) (*Store, error) {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, fmt.Errorf("%w: spec root is required", ErrInvalid)
	}
	return &Store{root: dir, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Store) dir(name string) string { return filepath.Join(s.root, name) }

func (s *Store) load(name string) (Spec, error) {
	if !ValidName(name) {
		return Spec{}, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(s.dir(name), "spec.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Spec{}, ErrNotFound
	}
	if err != nil {
		return Spec{}, err
	}
	var out Spec
	if err := json.Unmarshal(data, &out); err != nil {
		return Spec{}, fmt.Errorf("spec %s: %w", name, err)
	}
	if out.Status == nil {
		out.Status = map[string]string{}
	}
	for _, p := range Phases {
		if out.Status[p] == "" {
			out.Status[p] = StatusDraft
		}
	}
	if out.Requirements == nil {
		out.Requirements = []Requirement{}
	}
	if out.Tasks == nil {
		out.Tasks = []Task{}
	}
	return out, nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".spec-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// save writes spec.json plus the markdown mirrors.
func (s *Store) save(sp Spec) error {
	dir := s.dir(sp.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(sp, "", "  ")
	if err != nil {
		return err
	}
	files := []struct {
		name string
		data []byte
	}{
		{"spec.json", append(data, '\n')},
		{"requirements.md", []byte(RenderRequirements(sp))},
		{"design.md", []byte(sp.Design)},
		{"tasks.md", []byte(RenderTasks(sp))},
	}
	for _, f := range files {
		if err := writeAtomic(filepath.Join(dir, f.name), f.data); err != nil {
			return err
		}
	}
	return nil
}

// List returns every spec under the root, newest update first.
func (s *Store) List() ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entries, err := os.ReadDir(s.root)
	if errors.Is(err, os.ErrNotExist) {
		return []Summary{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []Summary{}
	for _, e := range entries {
		if !e.IsDir() || !ValidName(e.Name()) {
			continue
		}
		sp, err := s.load(e.Name())
		if err != nil {
			continue
		}
		out = append(out, Summary{Name: sp.Name, Title: sp.Title, Status: sp.Status, PlanID: sp.PlanID, UpdatedAt: sp.UpdatedAt})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Get returns one spec.
func (s *Store) Get(name string) (Spec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(name)
}

// Create starts a new spec with every phase in draft.
func (s *Store) Create(name, title, description string) (Spec, error) {
	name = strings.TrimSpace(name)
	if !ValidName(name) {
		return Spec{}, fmt.Errorf("%w: name must be lowercase letters, digits and dashes (1-64)", ErrInvalid)
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = name
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.load(name); err == nil {
		return Spec{}, ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return Spec{}, err
	}
	now := s.now()
	sp := Spec{
		Name:         name,
		Title:        title,
		Description:  strings.TrimSpace(description),
		Requirements: []Requirement{},
		Tasks:        []Task{},
		Status:       map[string]string{},
		Version:      1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	for _, p := range Phases {
		sp.Status[p] = StatusDraft
	}
	if err := s.save(sp); err != nil {
		return Spec{}, err
	}
	return sp, nil
}

// mutate loads name, checks hand-off and CAS, applies fn and saves.
func (s *Store) mutate(name string, expected int, fn func(*Spec) error) (Spec, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sp, err := s.load(name)
	if err != nil {
		return Spec{}, err
	}
	if sp.HandedOffAt != nil {
		return Spec{}, ErrHandedOff
	}
	if expected != sp.Version {
		return Spec{}, fmt.Errorf("%w: have %d, expected %d", ErrConflict, sp.Version, expected)
	}
	if err := fn(&sp); err != nil {
		return Spec{}, err
	}
	sp.Version++
	sp.UpdatedAt = s.now()
	if err := s.save(sp); err != nil {
		return Spec{}, err
	}
	return sp, nil
}

// SaveRequirements replaces the requirements phase and resets it and every
// later phase to draft.
func (s *Store) SaveRequirements(name string, reqs []Requirement, expected int) (Spec, error) {
	norm, err := normalizeRequirements(reqs)
	if err != nil {
		return Spec{}, err
	}
	return s.mutate(name, expected, func(sp *Spec) error {
		sp.Requirements = norm
		resetFrom(sp, PhaseRequirements)
		return nil
	})
}

// SaveDesign replaces the design text and resets design and tasks to draft.
func (s *Store) SaveDesign(name, design string, expected int) (Spec, error) {
	return s.mutate(name, expected, func(sp *Spec) error {
		sp.Design = design
		resetFrom(sp, PhaseDesign)
		return nil
	})
}

// SaveTasks replaces the task list and resets the tasks phase to draft.
func (s *Store) SaveTasks(name string, tasks []Task, expected int) (Spec, error) {
	norm, err := normalizeTasks(tasks)
	if err != nil {
		return Spec{}, err
	}
	return s.mutate(name, expected, func(sp *Spec) error {
		sp.Tasks = norm
		resetFrom(sp, PhaseTasks)
		return nil
	})
}

// SetPhaseStatus moves one phase through draft / review / approved under
// the gate rules (approve needs the previous phase approved; draft resets
// later phases).
func (s *Store) SetPhaseStatus(name, phase, status string, expected int) (Spec, error) {
	return s.mutate(name, expected, func(sp *Spec) error {
		return setPhaseStatus(sp, phase, status)
	})
}

// HandOff converts approved, fully covered tasks into a plan via create
// and records the plan id. The spec is read-only afterwards. create runs
// under the store lock so a spec hands off at most once.
func (s *Store) HandOff(name string, expected int, create func(title string, sections []PlanSection) (string, error)) (Spec, error) {
	return s.mutate(name, expected, func(sp *Spec) error {
		for _, p := range Phases {
			if sp.Status[p] != StatusApproved {
				return fmt.Errorf("%w: approve %s before hand-off", ErrGate, p)
			}
		}
		cov := ComputeCoverage(*sp)
		if len(cov.Uncovered) > 0 {
			return fmt.Errorf("%w: requirements not covered by any task: %s", ErrGate, strings.Join(cov.Uncovered, ", "))
		}
		if len(cov.UnknownRefs) > 0 {
			return fmt.Errorf("%w: tasks cite unknown requirements: %s", ErrGate, strings.Join(cov.UnknownRefs, ", "))
		}
		planID, err := create(sp.Title, PlanSections(*sp))
		if err != nil {
			return err
		}
		now := s.now()
		sp.PlanID = planID
		sp.HandedOffAt = &now
		return nil
	})
}
