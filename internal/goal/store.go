package goal

import (
	"crypto/rand"
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
	maxGoals    = 500
)

var (
	errClosed = errors.New("goal: store is closed")
	// ErrNotFound is returned when a goal id is absent.
	ErrNotFound = errors.New("goal: not found")
	// ErrInvalidStatus is returned for illegal status transitions.
	ErrInvalidStatus = errors.New("goal: invalid status transition")
)

type fileDoc struct {
	Version int    `json:"version"`
	Goals   []Goal `json:"goals"`
}

// Event is one append-only observability record (JSONL).
type Event struct {
	At      time.Time       `json:"at"`
	GoalID  string          `json:"goal_id"`
	Type    string          `json:"type"`
	Iter    int             `json:"iter,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Store is a project-scoped goal database: goals JSON + per-goal iteration
// JSONL and event JSONL under globalRoot/goals/.
type Store struct {
	mu     sync.Mutex
	dir    string
	path   string // goals.json
	goals  map[string]Goal
	closed bool
	// intents tracks completed action keys for idempotent resume.
	intents map[string]struct{}
}

// Open opens (or creates) the goal store for projectKey under globalRoot/goals.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("goal: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("goal: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:])
	dir := filepath.Join(globalRoot, "goals", name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create goals directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, "goals.json"))
	if err != nil {
		return nil, fmt.Errorf("resolve goals path: %w", err)
	}
	s := &Store{
		dir:     dir,
		path:    path,
		goals:   make(map[string]Goal),
		intents: make(map[string]struct{}),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	if err := s.loadIntents(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the on-disk goals JSON path.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Dir returns the project goals directory.
func (s *Store) Dir() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dir
}

// Create validates and stores a pending goal (does not run).
func (s *Store) Create(description string, criteria []Criterion, constraints Constraints) (Goal, error) {
	if constraints.MaxIterations == 0 && constraints.MaxWallClockS == 0 {
		constraints = DefaultConstraints()
		// preserve caller allowlist if only that was set — handled below
	}
	// Fill zeros with defaults without wiping explicit values.
	def := DefaultConstraints()
	if constraints.MaxIterations == 0 {
		constraints.MaxIterations = def.MaxIterations
	}
	if constraints.MaxCostUSD == 0 && def.MaxCostUSD > 0 {
		// 0 means unlimited cost only if caller set negative? Keep 0 as "use default".
		constraints.MaxCostUSD = def.MaxCostUSD
	}
	if constraints.MaxWallClockS == 0 {
		constraints.MaxWallClockS = def.MaxWallClockS
	}
	if constraints.MaxNoProgressIters == 0 {
		constraints.MaxNoProgressIters = def.MaxNoProgressIters
	}
	// Normalize criterion descriptions from check when empty.
	clean := make([]Criterion, len(criteria))
	for i, c := range criteria {
		c.Description = strings.TrimSpace(c.Description)
		if c.Description == "" {
			c.Description = FormatCheckSpec(c.Check)
		}
		c.Satisfied = false // actor cannot pre-mark
		clean[i] = c
	}
	if err := ValidateGoal(description, clean, constraints); err != nil {
		return Goal{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Goal{}, errClosed
	}
	if len(s.goals) >= maxGoals {
		return Goal{}, fmt.Errorf("goal: at most %d goals", maxGoals)
	}
	now := time.Now().UTC()
	id, err := newID()
	if err != nil {
		return Goal{}, err
	}
	g := Goal{
		ID:          id,
		Description: strings.TrimSpace(description),
		Criteria:    clean,
		Constraints: constraints,
		Status:      StatusPending,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.goals[id] = g
	if err := s.persistLocked(); err != nil {
		return Goal{}, err
	}
	_ = s.appendEventLocked(Event{
		At:     now,
		GoalID: id,
		Type:   "goal.created",
	})
	return CloneGoal(g), nil
}

// Get returns a goal by id.
func (s *Store) Get(id string) (Goal, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Goal{}, false, errClosed
	}
	g, ok := s.goals[id]
	if !ok {
		return Goal{}, false, nil
	}
	return CloneGoal(g), true, nil
}

// List returns goals newest-first.
func (s *Store) List() ([]Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Goal, 0, len(s.goals))
	for _, g := range s.goals {
		out = append(out, CloneGoal(g))
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// UpdateGoal replaces the stored goal (used by loop controller).
func (s *Store) UpdateGoal(g Goal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if _, ok := s.goals[g.ID]; !ok {
		return ErrNotFound
	}
	g.UpdatedAt = time.Now().UTC()
	s.goals[g.ID] = CloneGoal(g)
	return s.persistLocked()
}

// SetStatus applies a user-driven status change (pause/resume/abort).
func (s *Store) SetStatus(id string, status Status, reason string) (Goal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Goal{}, errClosed
	}
	g, ok := s.goals[id]
	if !ok {
		return Goal{}, ErrNotFound
	}
	now := time.Now().UTC()
	switch status {
	case StatusPaused:
		if g.Status != StatusActive {
			return Goal{}, fmt.Errorf("%w: pause requires active (have %s)", ErrInvalidStatus, g.Status)
		}
		g.Status = StatusPaused
	case StatusActive:
		if g.Status != StatusPaused && g.Status != StatusPending {
			return Goal{}, fmt.Errorf("%w: resume/run requires pending or paused (have %s)", ErrInvalidStatus, g.Status)
		}
		if g.Status == StatusPending || g.ActiveStartedAt.IsZero() {
			g.ActiveStartedAt = now
		}
		g.Status = StatusActive
		g.AbortRequested = false
	case StatusAborted:
		if g.Status == StatusDone || g.Status == StatusFailed || g.Status == StatusAborted {
			return Goal{}, fmt.Errorf("%w: cannot abort terminal goal (%s)", ErrInvalidStatus, g.Status)
		}
		g.AbortRequested = true
		g.Status = StatusAborted
		g.FailReason = reason
		if g.FailReason == "" {
			g.FailReason = "aborted by user"
		}
	default:
		return Goal{}, fmt.Errorf("goal: unsupported SetStatus %q", status)
	}
	g.UpdatedAt = now
	s.goals[id] = g
	if err := s.persistLocked(); err != nil {
		return Goal{}, err
	}
	_ = s.appendEventLocked(Event{
		At:     now,
		GoalID: id,
		Type:   "goal.status",
		Payload: mustJSON(map[string]string{
			"status": string(status),
			"reason": reason,
		}),
	})
	return CloneGoal(g), nil
}

// RequestAbort sets the abort flag without requiring active (for mid-loop).
func (s *Store) RequestAbort(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	g, ok := s.goals[id]
	if !ok {
		return ErrNotFound
	}
	if g.Status == StatusDone || g.Status == StatusFailed || g.Status == StatusAborted {
		return fmt.Errorf("%w: cannot abort terminal goal (%s)", ErrInvalidStatus, g.Status)
	}
	g.AbortRequested = true
	g.UpdatedAt = time.Now().UTC()
	s.goals[id] = g
	return s.persistLocked()
}

// CommitIteration atomically appends an iteration and updates the goal.
func (s *Store) CommitIteration(g Goal, rec IterationRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if _, ok := s.goals[g.ID]; !ok {
		return ErrNotFound
	}
	rec.CommittedAt = time.Now().UTC()
	if err := s.appendIterationLocked(g.ID, rec); err != nil {
		return err
	}
	for _, a := range rec.Actions {
		if a.Completed && a.IntentKey != "" {
			s.intents[a.IntentKey] = struct{}{}
		}
	}
	if err := s.persistIntentsLocked(); err != nil {
		return err
	}
	g.LastIteration = rec.N
	g.UpdatedAt = rec.CommittedAt
	s.goals[g.ID] = CloneGoal(g)
	if err := s.persistLocked(); err != nil {
		return err
	}
	_ = s.appendEventLocked(Event{
		At:     rec.CommittedAt,
		GoalID: g.ID,
		Type:   "iteration.committed",
		Iter:   rec.N,
		Payload: mustJSON(map[string]any{
			"state_hash": rec.StateHash,
			"cost_usd":   rec.CostUSD,
			"plan":       rec.Plan,
		}),
	})
	return nil
}

// IntentDone reports whether an action intent was already completed.
func (s *Store) IntentDone(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.intents[key]
	return ok
}

// MarkIntent records a completed intent before side effects finish (resume).
func (s *Store) MarkIntent(key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	s.intents[key] = struct{}{}
	return s.persistIntentsLocked()
}

// ListIterations returns committed iterations for a goal ascending by n.
func (s *Store) ListIterations(id string) ([]IterationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	if _, ok := s.goals[id]; !ok {
		return nil, ErrNotFound
	}
	return s.readIterationsLocked(id)
}

// ListEvents returns observability events for a goal (or all if id empty).
func (s *Store) ListEvents(id string) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	path := filepath.Join(s.dir, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Event
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if id != "" && ev.GoalID != id {
			continue
		}
		out = append(out, ev)
	}
	return out, nil
}

// AppendEvent writes a structured observability event.
func (s *Store) AppendEvent(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errClosed
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	return s.appendEventLocked(ev)
}

// Close marks the store closed.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read goals: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode goals: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode goals: unsupported version %d", doc.Version)
	}
	for _, g := range doc.Goals {
		if g.ID == "" {
			continue
		}
		s.goals[g.ID] = g
	}
	return nil
}

func (s *Store) persistLocked() error {
	goals := make([]Goal, 0, len(s.goals))
	for _, g := range s.goals {
		goals = append(goals, CloneGoal(g))
	}
	sort.Slice(goals, func(i, j int) bool {
		return goals[i].CreatedAt.Before(goals[j].CreatedAt)
	})
	data, err := json.MarshalIndent(fileDoc{Version: fileVersion, Goals: goals}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode goals: %w", err)
	}
	data = append(data, '\n')
	return writeAtomic(s.path, data)
}

func (s *Store) loadIntents() error {
	path := filepath.Join(s.dir, "intents.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var keys []string
	if err := json.Unmarshal(data, &keys); err != nil {
		return fmt.Errorf("decode intents: %w", err)
	}
	for _, k := range keys {
		s.intents[k] = struct{}{}
	}
	return nil
}

func (s *Store) persistIntentsLocked() error {
	keys := make([]string, 0, len(s.intents))
	for k := range s.intents {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	data, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(filepath.Join(s.dir, "intents.json"), data)
}

func (s *Store) iterationPath(id string) string {
	return filepath.Join(s.dir, "iter_"+id+".jsonl")
}

func (s *Store) appendIterationLocked(id string, rec IterationRecord) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return appendJSONL(s.iterationPath(id), data)
}

func (s *Store) readIterationsLocked(id string) ([]IterationRecord, error) {
	data, err := os.ReadFile(s.iterationPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []IterationRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec IterationRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].N < out[j].N })
	return out, nil
}

func (s *Store) appendEventLocked(ev Event) error {
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	return appendJSONL(filepath.Join(s.dir, "events.jsonl"), data)
}

func appendJSONL(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".goals-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
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
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	cleanup = false
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return b
}

// IntentKey builds the idempotency key for an action.
func IntentKey(goalID string, iterN, actionIdx int) string {
	return fmt.Sprintf("%s:%d:%d", goalID, iterN, actionIdx)
}
