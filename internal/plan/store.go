package plan

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
	fileVersion       = 1
	maxTitleLen       = 512
	maxSectionBodyLen = 64 * 1024
	maxSections       = 256
	maxPlans          = 500
)

var (
	errClosed         = errors.New("plan: store is closed")
	errEmptyTitle     = errors.New("plan: title is required")
	errTitleTooLong   = fmt.Errorf("plan: title exceeds %d bytes", maxTitleLen)
	errBodyTooLong    = fmt.Errorf("plan: section body exceeds %d bytes", maxSectionBodyLen)
	errFull           = fmt.Errorf("plan: at most %d plans", maxPlans)
	errTooManySecs    = fmt.Errorf("plan: at most %d sections", maxSections)
	errEmptyOwner     = errors.New("plan: owner root session id is required")
	errEmptySectionID = errors.New("plan: section id is required")
	// ErrNotFound is returned when a plan or section id is absent.
	ErrNotFound = errors.New("plan: not found")
	// ErrNotOwner is returned when a non-owning root attempts mutation.
	ErrNotOwner = errors.New("plan: only the owning root may mutate this plan")
	// ErrConflict is returned when expectedVersion does not match the stored version.
	ErrConflict = errors.New("plan: version conflict")
	// ErrInvalidStatus is returned for unknown or illegal lifecycle values.
	ErrInvalidStatus = errors.New("plan: invalid status")
	// ErrClosedPlan is returned when mutating content on a closed plan (use Reopen).
	ErrClosedPlan = errors.New("plan: plan is closed")
)

type fileDoc struct {
	Version int    `json:"version"`
	Plans   []Plan `json:"plans"`
}

// Store is a project-scoped plan database backed by one JSON file under
// globalRoot/plans/. Writes are mutex-serialized and replaced atomically.
type Store struct {
	mu     sync.Mutex
	path   string
	plans  map[string]Plan
	closed bool
}

// Open opens (or creates) the plan file for projectKey under globalRoot/plans.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("plan: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("plan: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "plans")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create plans directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("resolve plans path: %w", err)
	}
	s := &Store{
		path:  path,
		plans: make(map[string]Plan),
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// Path returns the on-disk JSON path.
func (s *Store) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.path
}

// Close marks the store closed. The file remains on disk.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Create inserts a draft plan owned by ownerRoot with optional initial sections.
// Section IDs are assigned (s1, s2, …). Version starts at 1.
func (s *Store) Create(ownerRoot, title string, sections []SectionInput) (Plan, error) {
	ownerRoot = strings.TrimSpace(ownerRoot)
	title = strings.TrimSpace(title)
	if ownerRoot == "" {
		return Plan{}, errEmptyOwner
	}
	if err := validateTitle(title); err != nil {
		return Plan{}, err
	}
	if len(sections) > maxSections {
		return Plan{}, errTooManySecs
	}
	clean := make([]Section, 0, len(sections))
	for i, in := range sections {
		st := strings.TrimSpace(in.Title)
		if st == "" {
			return Plan{}, fmt.Errorf("plan: section %d: title is required", i)
		}
		if err := validateTitle(st); err != nil {
			return Plan{}, fmt.Errorf("plan: section %d: %w", i, err)
		}
		if err := validateSectionBody(in.Body); err != nil {
			return Plan{}, fmt.Errorf("plan: section %d: %w", i, err)
		}
		clean = append(clean, Section{Title: st, Body: in.Body})
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Plan{}, errClosed
	}
	if len(s.plans) >= maxPlans {
		return Plan{}, errFull
	}
	id, err := newID()
	if err != nil {
		return Plan{}, err
	}
	now := time.Now().UTC()
	secs := make([]Section, len(clean))
	for i, sec := range clean {
		secs[i] = Section{
			ID:    fmt.Sprintf("s%d", i+1),
			Title: sec.Title,
			Body:  sec.Body,
		}
	}
	p := Plan{
		ID:             id,
		OwnerRoot:      ownerRoot,
		Title:          title,
		Status:         StatusDraft,
		Sections:       secs,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
		NextSectionSeq: len(secs) + 1,
	}
	s.plans[id] = p
	if err := s.persistLocked(); err != nil {
		delete(s.plans, id)
		return Plan{}, err
	}
	return ClonePlan(p), nil
}

// Get returns a deep copy of the plan when present.
func (s *Store) Get(id string) (Plan, bool, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Plan{}, false, errClosed
	}
	p, ok := s.plans[id]
	if !ok {
		return Plan{}, false, nil
	}
	return ClonePlan(p), true, nil
}

// List returns project-wide index metadata newest-UpdatedAt first.
// Every root in the project may list; bodies are not included.
func (s *Store) List() ([]Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Meta, 0, len(s.plans))
	for _, p := range s.plans {
		out = append(out, MetaFromPlan(p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// UpdateTitle CAS-updates the plan title. Only the owning root may mutate.
func (s *Store) UpdateTitle(id, actorRoot, title string, expectedVersion int) (Plan, error) {
	title = strings.TrimSpace(title)
	if err := validateTitle(title); err != nil {
		return Plan{}, err
	}
	return s.mutate(id, actorRoot, expectedVersion, true, func(p *Plan) error {
		p.Title = title
		return nil
	})
}

// UpdateSection CAS-updates one section's title and/or body by stable id.
// Nil pointers leave fields unchanged. Only the owning root may mutate.
func (s *Store) UpdateSection(id, actorRoot, sectionID string, title, body *string, expectedVersion int) (Plan, error) {
	sectionID = strings.TrimSpace(sectionID)
	if sectionID == "" {
		return Plan{}, errEmptySectionID
	}
	var newTitle *string
	if title != nil {
		t := strings.TrimSpace(*title)
		if err := validateTitle(t); err != nil {
			return Plan{}, err
		}
		newTitle = &t
	}
	if body != nil {
		if err := validateSectionBody(*body); err != nil {
			return Plan{}, err
		}
	}
	return s.mutate(id, actorRoot, expectedVersion, true, func(p *Plan) error {
		idx := -1
		for i := range p.Sections {
			if p.Sections[i].ID == sectionID {
				idx = i
				break
			}
		}
		if idx < 0 {
			return ErrNotFound
		}
		if newTitle != nil {
			p.Sections[idx].Title = *newTitle
		}
		if body != nil {
			p.Sections[idx].Body = *body
		}
		return nil
	})
}

// AddSection appends a section with a new stable id (sN). Owner + CAS.
func (s *Store) AddSection(id, actorRoot, title, body string, expectedVersion int) (Plan, error) {
	title = strings.TrimSpace(title)
	if err := validateTitle(title); err != nil {
		return Plan{}, err
	}
	if err := validateSectionBody(body); err != nil {
		return Plan{}, err
	}
	return s.mutate(id, actorRoot, expectedVersion, true, func(p *Plan) error {
		if len(p.Sections) >= maxSections {
			return errTooManySecs
		}
		if p.NextSectionSeq < 1 {
			p.NextSectionSeq = len(p.Sections) + 1
		}
		secID := fmt.Sprintf("s%d", p.NextSectionSeq)
		p.NextSectionSeq++
		p.Sections = append(p.Sections, Section{ID: secID, Title: title, Body: body})
		return nil
	})
}

// SetStatus CAS-transitions lifecycle (draft↔approved, either→closed).
// Closed plans cannot leave closed via SetStatus — use Reopen.
func (s *Store) SetStatus(id, actorRoot, status string, expectedVersion int) (Plan, error) {
	status = strings.TrimSpace(status)
	if !ValidStatus(status) {
		return Plan{}, ErrInvalidStatus
	}
	return s.mutate(id, actorRoot, expectedVersion, false, func(p *Plan) error {
		if !canTransition(p.Status, status) {
			return fmt.Errorf("%w: cannot transition %s → %s", ErrInvalidStatus, p.Status, status)
		}
		p.Status = status
		return nil
	})
}

// Reopen CAS-moves a closed plan back to draft. Owner-only.
func (s *Store) Reopen(id, actorRoot string, expectedVersion int) (Plan, error) {
	return s.mutate(id, actorRoot, expectedVersion, false, func(p *Plan) error {
		if p.Status != StatusClosed {
			return fmt.Errorf("%w: reopen requires closed status (have %s)", ErrInvalidStatus, p.Status)
		}
		p.Status = StatusDraft
		return nil
	})
}

// mutate is the shared owner+CAS write path. contentMutation rejects closed plans.
func (s *Store) mutate(id, actorRoot string, expectedVersion int, contentMutation bool, fn func(*Plan) error) (Plan, error) {
	id = strings.TrimSpace(id)
	actorRoot = strings.TrimSpace(actorRoot)
	if id == "" {
		return Plan{}, ErrNotFound
	}
	if actorRoot == "" {
		return Plan{}, errEmptyOwner
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Plan{}, errClosed
	}
	p, ok := s.plans[id]
	if !ok {
		return Plan{}, ErrNotFound
	}
	if p.OwnerRoot != actorRoot {
		return Plan{}, ErrNotOwner
	}
	if expectedVersion != p.Version {
		return Plan{}, fmt.Errorf("%w: have %d, expected %d", ErrConflict, p.Version, expectedVersion)
	}
	if contentMutation && p.Status == StatusClosed {
		return Plan{}, ErrClosedPlan
	}
	// Work on a deep copy so a failed fn cannot leave partial in-memory state.
	working := ClonePlan(p)
	if err := fn(&working); err != nil {
		return Plan{}, err
	}
	working.Version = p.Version + 1
	working.UpdatedAt = time.Now().UTC()
	// Preserve identity fields the callback must not rewrite.
	working.ID = p.ID
	working.OwnerRoot = p.OwnerRoot
	working.CreatedAt = p.CreatedAt
	s.plans[id] = working
	if err := s.persistLocked(); err != nil {
		s.plans[id] = p // roll back memory on disk failure
		return Plan{}, err
	}
	return ClonePlan(working), nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read plans: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode plans: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode plans: unsupported version %d", doc.Version)
	}
	for _, p := range doc.Plans {
		if strings.TrimSpace(p.ID) == "" {
			continue
		}
		if !ValidStatus(p.Status) {
			p.Status = StatusDraft
		}
		if p.Version < 1 {
			p.Version = 1
		}
		if p.NextSectionSeq < 1 {
			maxN := 0
			for _, sec := range p.Sections {
				var n int
				if _, err := fmt.Sscanf(sec.ID, "s%d", &n); err == nil && n > maxN {
					maxN = n
				}
			}
			p.NextSectionSeq = maxN + 1
			if p.NextSectionSeq < len(p.Sections)+1 {
				p.NextSectionSeq = len(p.Sections) + 1
			}
		}
		if p.Sections == nil {
			p.Sections = []Section{}
		}
		s.plans[p.ID] = p
	}
	return nil
}

func (s *Store) persistLocked() error {
	plans := make([]Plan, 0, len(s.plans))
	for _, p := range s.plans {
		plans = append(plans, ClonePlan(p))
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].ID < plans[j].ID })
	data, err := json.MarshalIndent(fileDoc{
		Version: fileVersion,
		Plans:   plans,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode plans: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".plans-*.tmp")
	if err != nil {
		return fmt.Errorf("create plans temp: %w", err)
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
		return fmt.Errorf("chmod plans temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write plans temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync plans temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close plans temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace plans file: %w", err)
	}
	cleanup = false
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("plan: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
