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
	// beforeSave, when set, runs before each spec.json write. Tests use it
	// to fail the write after a plan exists; nil in production.
	beforeSave func(Spec) error
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
	if s.beforeSave != nil {
		if err := s.beforeSave(sp); err != nil {
			return err
		}
	}
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

// Planner creates the plan a hand-off produces and finds one an earlier,
// interrupted hand-off already created.
type Planner interface {
	// Create makes the plan and returns its id.
	Create(title string, sections []PlanSection) (string, error)
	// Find returns the id of a plan titled title that was created at or
	// after since, or "" when there is none. It covers a crash between
	// creating the plan and journaling its id.
	Find(title string, since time.Time) (string, error)
}

// PlannerFuncs adapts two functions to Planner. A nil FindFn finds nothing.
type PlannerFuncs struct {
	CreateFn func(title string, sections []PlanSection) (string, error)
	FindFn   func(title string, since time.Time) (string, error)
}

// Create calls CreateFn.
func (p PlannerFuncs) Create(title string, sections []PlanSection) (string, error) {
	return p.CreateFn(title, sections)
}

// Find calls FindFn, or finds nothing when it is nil.
func (p PlannerFuncs) Find(title string, since time.Time) (string, error) {
	if p.FindFn == nil {
		return "", nil
	}
	return p.FindFn(title, since)
}

// handOffJournal is <spec>/handoff.json: the intent recorded before the
// plan is created, then the plan id once it exists. It makes a hand-off
// retried after a failed spec write reuse the plan instead of creating a
// second one. Version ties it to the spec revision being handed off; an
// edit between attempts bumps the version and voids the journal.
type handOffJournal struct {
	Version   int       `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	PlanID    string    `json:"planId,omitempty"`
}

func (s *Store) journalPath(name string) string {
	return filepath.Join(s.dir(name), "handoff.json")
}

func (s *Store) readJournal(name string) (handOffJournal, bool) {
	data, err := os.ReadFile(s.journalPath(name))
	if err != nil {
		return handOffJournal{}, false
	}
	var j handOffJournal
	if json.Unmarshal(data, &j) != nil {
		return handOffJournal{}, false
	}
	return j, true
}

func (s *Store) writeJournal(name string, j handOffJournal) error {
	data, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeAtomic(s.journalPath(name), append(data, '\n'))
}

// handOffPlan returns the plan for this hand-off attempt: the journaled
// one, one an interrupted attempt created, or a new one. Callers hold mu.
func (s *Store) handOffPlan(sp Spec, planner Planner) (string, error) {
	j, ok := s.readJournal(sp.Name)
	if ok && j.Version == sp.Version {
		if j.PlanID != "" {
			return j.PlanID, nil
		}
		// Intent without an id: the plan may exist but its id was lost.
		id, err := planner.Find(sp.Title, j.StartedAt)
		if err != nil {
			return "", err
		}
		if id != "" {
			j.PlanID = id
			_ = s.writeJournal(sp.Name, j)
			return id, nil
		}
	}
	j = handOffJournal{Version: sp.Version, StartedAt: s.now()}
	if err := s.writeJournal(sp.Name, j); err != nil {
		return "", fmt.Errorf("record hand-off intent: %w", err)
	}
	id, err := planner.Create(sp.Title, PlanSections(sp))
	if err != nil {
		_ = os.Remove(s.journalPath(sp.Name))
		return "", err
	}
	j.PlanID = id
	// Best effort: if this write fails, a retry finds the plan via Find.
	_ = s.writeJournal(sp.Name, j)
	return id, nil
}

// HandOff converts approved, fully covered tasks into a plan and records
// the plan id. The spec is read-only afterwards. Planning runs under the
// store lock so a spec hands off at most once, and a retry after a failed
// spec write reuses the plan the failed attempt created.
func (s *Store) HandOff(name string, expected int, planner Planner) (Spec, error) {
	sp, err := s.mutate(name, expected, func(sp *Spec) error {
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
		planID, err := s.handOffPlan(*sp, planner)
		if err != nil {
			return err
		}
		now := s.now()
		sp.PlanID = planID
		sp.HandedOffAt = &now
		return nil
	})
	if err == nil {
		_ = os.Remove(s.journalPath(name))
	}
	return sp, err
}
