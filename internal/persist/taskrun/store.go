package taskrun

import (
	"crypto/sha256"
	"encoding/hex"
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

const (
	fileVersion = 1
	maxSections = 256
	maxRuns     = 500
)

var (
	// ErrNotFound is returned when the plan has no run, or a section id is absent.
	ErrNotFound = errors.New("taskrun: not found")
	// ErrConflict is returned when expectedVersion does not match.
	ErrConflict = errors.New("taskrun: version conflict")
	// ErrNotOwner is returned when a different root tries to mutate the run.
	ErrNotOwner = errors.New("taskrun: only the owning root may mutate this run")
	// ErrClosed is returned when starting a run for a closed plan.
	ErrClosed = errors.New("taskrun: plan is closed")
	// ErrInvalid is returned for an illegal transition, including spawning a
	// section whose dependencies are not approved or skipped.
	ErrInvalid = errors.New("taskrun: invalid transition")
	errClosed  = errors.New("taskrun: store is closed")
)

type fileDoc struct {
	Version int   `json:"version"`
	Runs    []Run `json:"runs"`
}

// Store is a project-scoped run database backed by one JSON file under
// globalRoot/taskruns/. Writes are mutex-serialized and replaced atomically.
type Store struct {
	mu     sync.Mutex
	path   string
	runs   map[string]Run
	closed bool
}

// Open opens (or creates) the run file for projectKey under globalRoot/taskruns.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("taskrun: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("taskrun: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "taskruns")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create taskruns directory: %w", err)
	}
	s := &Store{
		path: filepath.Join(dir, name),
		runs: map[string]Run{},
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the JSON file path.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Close marks the store closed. The file remains on disk.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Get returns a deep copy of the run for a plan.
func (s *Store) Get(planID string) (Run, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Run{}, false, errClosed
	}
	run, ok := s.runs[planID]
	if !ok {
		return Run{}, false, nil
	}
	return clone(run), true, nil
}

// Ensure creates a run from the plan or refreshes titles and bodies.
// New sections are appended and depend on the previous checklist row.
// Existing dependency edges are kept. Closed plans can be refreshed once a
// run exists; the first open of a closed plan is refused.
func (s *Store) Ensure(planID, owner, title, planStatus string, sections []SectionInput) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.alive(); err != nil {
		return Run{}, err
	}
	planID = strings.TrimSpace(planID)
	owner = strings.TrimSpace(owner)
	if planID == "" || owner == "" {
		return Run{}, fmt.Errorf("taskrun: plan id and owner are required")
	}
	if err := validateInputs(sections); err != nil {
		return Run{}, err
	}
	cur, ok := s.runs[planID]
	if ok && cur.OwnerRoot != owner {
		return Run{}, ErrNotOwner
	}
	if !ok && planStatus == "closed" {
		return Run{}, ErrClosed
	}
	now := time.Now().UTC()
	if !ok {
		if len(s.runs) >= maxRuns {
			return Run{}, fmt.Errorf("taskrun: at most %d runs", maxRuns)
		}
		run := Run{
			PlanID:     planID,
			OwnerRoot:  owner,
			PlanTitle:  strings.TrimSpace(title),
			PlanStatus: planStatus,
			Version:    1,
			UpdatedAt:  now,
		}
		for i, in := range sections {
			sec := Section{
				ID:     in.ID,
				Title:  strings.TrimSpace(in.Title),
				Body:   in.Body,
				Status: StatusPending,
			}
			if i > 0 {
				sec.DependsOn = []string{sections[i-1].ID}
			}
			run.Sections = append(run.Sections, sec)
		}
		recompute(run.Sections)
		s.runs[planID] = run
		if err := s.persistLocked(); err != nil {
			delete(s.runs, planID)
			return Run{}, err
		}
		return clone(run), nil
	}
	next := clone(cur)
	next.PlanTitle = strings.TrimSpace(title)
	next.PlanStatus = planStatus
	byIn := map[string]SectionInput{}
	for _, in := range sections {
		byIn[in.ID] = in
	}
	seen := map[string]bool{}
	for i := range next.Sections {
		in, ok := byIn[next.Sections[i].ID]
		if !ok {
			continue
		}
		seen[in.ID] = true
		next.Sections[i].Title = strings.TrimSpace(in.Title)
		next.Sections[i].Body = in.Body
		if next.Sections[i].Checkpoint != nil {
			next.Sections[i].Checkpoint.SectionBody = in.Body
		}
	}
	for _, in := range sections {
		if seen[in.ID] {
			continue
		}
		sec := Section{
			ID:     in.ID,
			Title:  strings.TrimSpace(in.Title),
			Body:   in.Body,
			Status: StatusPending,
		}
		if n := len(next.Sections); n > 0 {
			sec.DependsOn = []string{next.Sections[n-1].ID}
		}
		next.Sections = append(next.Sections, sec)
	}
	if len(next.Sections) > maxSections {
		return Run{}, fmt.Errorf("taskrun: at most %d sections", maxSections)
	}
	recompute(next.Sections)
	if same(cur, next) {
		return clone(cur), nil
	}
	next.Version = cur.Version + 1
	next.UpdatedAt = now
	s.runs[planID] = next
	if err := s.persistLocked(); err != nil {
		s.runs[planID] = cur
		return Run{}, err
	}
	return clone(next), nil
}

// Reorder applies a permutation of section ids. Sections that still depend
// only on pending rows are re-chained to the new predecessor. A section that
// depends on a failed or blocked row keeps that edge.
func (s *Store) Reorder(planID string, ids []string, expected int) (Run, error) {
	return s.mutate(planID, expected, func(run *Run) error {
		if len(ids) != len(run.Sections) {
			return fmt.Errorf("%w: reorder must list every section", ErrInvalid)
		}
		by := index(run.Sections)
		next := make([]Section, 0, len(ids))
		seen := map[string]bool{}
		for _, id := range ids {
			sec, ok := by[id]
			if !ok || seen[id] {
				return fmt.Errorf("%w: reorder ids must be a permutation", ErrInvalid)
			}
			seen[id] = true
			next = append(next, sec)
		}
		chainFromOrder(next)
		recompute(next)
		run.Sections = next
		return nil
	})
}

// Skip marks a section skipped so dependants can proceed. A running section
// has to be interrupted first.
func (s *Store) Skip(planID, sectionID string, expected int) (Run, error) {
	return s.mutate(planID, expected, func(run *Run) error {
		sec := find(run, sectionID)
		if sec == nil {
			return ErrNotFound
		}
		if sec.Status == StatusRunning {
			return fmt.Errorf("%w: stop the child before skipping", ErrInvalid)
		}
		if sec.Status == StatusApproved {
			return fmt.Errorf("%w: approved sections stay approved", ErrInvalid)
		}
		sec.Status = StatusSkipped
		sec.Detail = "skipped"
		recompute(run.Sections)
		return nil
	})
}

// Approve accepts a checkpoint. Dependants stay pending until something
// claims them; this does not spawn.
func (s *Store) Approve(planID, sectionID string, expected int) (Run, error) {
	return s.mutate(planID, expected, func(run *Run) error {
		sec := find(run, sectionID)
		if sec == nil {
			return ErrNotFound
		}
		if sec.Status != StatusCheckpoint {
			return fmt.Errorf("%w: approve requires a checkpoint", ErrInvalid)
		}
		sec.Status = StatusApproved
		sec.Detail = "approved"
		recompute(run.Sections)
		return nil
	})
}

// Retry returns a failed or unapproved checkpoint to pending and clears the
// child correlation. Dependants stay blocked until this section is approved
// or skipped after the new attempt.
func (s *Store) Retry(planID, sectionID string, expected int) (Run, error) {
	return s.mutate(planID, expected, func(run *Run) error {
		sec := find(run, sectionID)
		if sec == nil {
			return ErrNotFound
		}
		if sec.Status != StatusFailed && sec.Status != StatusCheckpoint {
			return fmt.Errorf("%w: retry requires a failed section or a checkpoint", ErrInvalid)
		}
		sec.Status = StatusPending
		sec.ChildID = ""
		sec.Checkpoint = nil
		sec.Detail = ""
		recompute(run.Sections)
		return nil
	})
}

// ClaimOne marks one ready section running. It refuses a blocked, failed,
// or not-yet-unblocked section so a start cannot skip a failure.
func (s *Store) ClaimOne(planID, sectionID string) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.alive(); err != nil {
		return Run{}, err
	}
	cur, ok := s.runs[planID]
	if !ok {
		return Run{}, ErrNotFound
	}
	if cur.PlanStatus == "closed" {
		return Run{}, ErrClosed
	}
	next := clone(cur)
	sec := find(&next, sectionID)
	if sec == nil {
		return Run{}, ErrNotFound
	}
	recompute(next.Sections)
	sec = find(&next, sectionID)
	ready := false
	for _, id := range ReadyIDs(next) {
		if id == sectionID {
			ready = true
			break
		}
	}
	if !ready {
		return Run{}, fmt.Errorf("%w: section is not ready", ErrInvalid)
	}
	sec.Status = StatusRunning
	sec.ChildID = ""
	sec.Checkpoint = nil
	sec.Detail = "starting"
	next.Version++
	next.UpdatedAt = time.Now().UTC()
	s.runs[planID] = next
	if err := s.persistLocked(); err != nil {
		s.runs[planID] = cur
		return Run{}, err
	}
	return clone(next), nil
}

// BindChild records the spawned child on a claimed section.
func (s *Store) BindChild(planID, sectionID, childID string) (Run, error) {
	childID = strings.TrimSpace(childID)
	if childID == "" {
		return Run{}, fmt.Errorf("%w: child id is required", ErrInvalid)
	}
	return s.mutate(planID, 0, func(run *Run) error {
		sec := find(run, sectionID)
		if sec == nil {
			return ErrNotFound
		}
		if sec.Status != StatusRunning {
			return fmt.Errorf("%w: section is not running", ErrInvalid)
		}
		if sec.ChildID != "" && sec.ChildID != childID {
			return fmt.Errorf("%w: section already bound to %s", ErrInvalid, sec.ChildID)
		}
		sec.ChildID = childID
		sec.Detail = "running"
		return nil
	})
}

// Release returns a claimed section that never bound a child to pending.
func (s *Store) Release(planID, sectionID string) (Run, error) {
	return s.mutate(planID, 0, func(run *Run) error {
		sec := find(run, sectionID)
		if sec == nil {
			return ErrNotFound
		}
		if sec.Status != StatusRunning || sec.ChildID != "" {
			return fmt.Errorf("%w: only an unbound claim can be released", ErrInvalid)
		}
		sec.Status = StatusPending
		sec.Detail = ""
		recompute(run.Sections)
		return nil
	})
}

// ApplyChild records a terminal child result. A second record for the same
// child is a no-op. A failed outcome blocks dependants in the same write.
func (s *Store) ApplyChild(planID, sectionID string, o Outcome) (Run, error) {
	o.ChildID = strings.TrimSpace(o.ChildID)
	if o.ChildID == "" {
		return Run{}, fmt.Errorf("%w: child id is required", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.alive(); err != nil {
		return Run{}, err
	}
	cur, ok := s.runs[planID]
	if !ok {
		return Run{}, ErrNotFound
	}
	sec := find(&cur, sectionID)
	if sec == nil {
		return Run{}, ErrNotFound
	}
	if sec.Checkpoint != nil && sec.Checkpoint.ChildID == o.ChildID &&
		(sec.Status == StatusFailed || sec.Status == StatusCheckpoint || sec.Status == StatusApproved || sec.Status == StatusSkipped) {
		return clone(cur), nil
	}
	if sec.Status != StatusRunning {
		return clone(cur), nil
	}
	if sec.ChildID != "" && sec.ChildID != o.ChildID {
		return Run{}, fmt.Errorf("%w: child does not match section", ErrInvalid)
	}
	next := clone(cur)
	sec = find(&next, sectionID)
	applyOutcome(sec, o)
	recompute(next.Sections)
	next.Version = cur.Version + 1
	next.UpdatedAt = time.Now().UTC()
	s.runs[planID] = next
	if err := s.persistLocked(); err != nil {
		s.runs[planID] = cur
		return Run{}, err
	}
	return clone(next), nil
}

func (s *Store) mutate(planID string, expected int, fn func(*Run) error) (Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.alive(); err != nil {
		return Run{}, err
	}
	cur, ok := s.runs[planID]
	if !ok {
		return Run{}, ErrNotFound
	}
	if expected != 0 && cur.Version != expected {
		return Run{}, ErrConflict
	}
	next := clone(cur)
	if err := fn(&next); err != nil {
		return Run{}, err
	}
	next.Version = cur.Version + 1
	next.UpdatedAt = time.Now().UTC()
	s.runs[planID] = next
	if err := s.persistLocked(); err != nil {
		s.runs[planID] = cur
		return Run{}, err
	}
	return clone(next), nil
}

func (s *Store) alive() error {
	if s.closed {
		return errClosed
	}
	return nil
}

func find(run *Run, id string) *Section {
	for i := range run.Sections {
		if run.Sections[i].ID == id {
			return &run.Sections[i]
		}
	}
	return nil
}

func validateInputs(sections []SectionInput) error {
	if len(sections) > maxSections {
		return fmt.Errorf("taskrun: at most %d sections", maxSections)
	}
	seen := map[string]bool{}
	for _, sec := range sections {
		id := strings.TrimSpace(sec.ID)
		if id == "" || strings.TrimSpace(sec.Title) == "" {
			return fmt.Errorf("taskrun: section id and title are required")
		}
		if seen[id] {
			return fmt.Errorf("taskrun: duplicate section %s", id)
		}
		seen[id] = true
	}
	return nil
}

func same(a, b Run) bool {
	ab, err1 := json.Marshal(a.Sections)
	bb, err2 := json.Marshal(b.Sections)
	if err1 != nil || err2 != nil {
		return false
	}
	return a.PlanTitle == b.PlanTitle && a.PlanStatus == b.PlanStatus && string(ab) == string(bb)
}

func clone(run Run) Run {
	out := run
	if run.Sections == nil {
		return out
	}
	out.Sections = make([]Section, len(run.Sections))
	for i, sec := range run.Sections {
		out.Sections[i] = sec
		if sec.DependsOn != nil {
			out.Sections[i].DependsOn = append([]string(nil), sec.DependsOn...)
		}
		if sec.Checkpoint != nil {
			cp := *sec.Checkpoint
			cp.FilesChanged = append([]string(nil), sec.Checkpoint.FilesChanged...)
			cp.Findings = append([]string(nil), sec.Checkpoint.Findings...)
			cp.Blockers = append([]string(nil), sec.Checkpoint.Blockers...)
			cp.Checks = append([]Check(nil), sec.Checkpoint.Checks...)
			out.Sections[i].Checkpoint = &cp
		}
	}
	return out
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read task runs: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode task runs: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode task runs: unsupported version %d", doc.Version)
	}
	for _, run := range doc.Runs {
		if strings.TrimSpace(run.PlanID) == "" {
			continue
		}
		normalize(&run)
		s.runs[run.PlanID] = run
	}
	return nil
}

// normalize repairs a run read off disk. An unbound claim from a crash
// returns to pending. Blocked rows are derived again so a failed section
// still blocks its dependants after a restart.
func normalize(run *Run) {
	if run.Version < 1 {
		run.Version = 1
	}
	if run.Sections == nil {
		run.Sections = []Section{}
	}
	for i := range run.Sections {
		if run.Sections[i].Status == StatusRunning && strings.TrimSpace(run.Sections[i].ChildID) == "" {
			run.Sections[i].Status = StatusPending
			run.Sections[i].Detail = ""
		}
	}
	recompute(run.Sections)
}

func (s *Store) persistLocked() error {
	runs := make([]Run, 0, len(s.runs))
	for _, run := range s.runs {
		runs = append(runs, clone(run))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].PlanID < runs[j].PlanID })
	data, err := json.MarshalIndent(fileDoc{Version: fileVersion, Runs: runs}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode task runs: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".taskrun-*.tmp")
	if err != nil {
		return fmt.Errorf("create taskrun temp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("chmod taskrun temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write taskrun temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync taskrun temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close taskrun temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace taskrun file: %w", err)
	}
	cleanup = false
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("chmod taskrun file: %w", err)
	}
	return nil
}
