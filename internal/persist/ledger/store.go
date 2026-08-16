package ledger

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
	fileVersion     = 1
	maxStatementLen = 8 * 1024
	maxReasonLen    = 4 * 1024
	maxEvidence     = 32
	maxEvidenceLen  = 512
	maxScopePaths   = 32
	maxScopeTasks   = 32
	maxScopeItemLen = 512
	maxEntries      = 2000
	maxAgentLen     = 128
)

var (
	errClosed            = errors.New("ledger: store is closed")
	errEmptyStatement    = errors.New("ledger: statement is required")
	errStatementTooLong  = fmt.Errorf("ledger: statement exceeds %d bytes", maxStatementLen)
	errReasonTooLong     = fmt.Errorf("ledger: reason exceeds %d bytes", maxReasonLen)
	errEmptyReason       = errors.New("ledger: invalidate reason is required")
	errEmptyKind         = errors.New("ledger: kind is required")
	errInvalidKind       = errors.New("ledger: kind must be decision, assumption, or constraint")
	errInvalidStatus     = errors.New("ledger: invalid status filter")
	errInvalidConfidence = errors.New("ledger: confidence must be low, medium, or high")
	errEmptyAuthor       = errors.New("ledger: author session is required")
	errEmptyRoot         = errors.New("ledger: global root is empty")
	errEmptyProject      = errors.New("ledger: project key is empty")
	errFull              = fmt.Errorf("ledger: at most %d entries", maxEntries)
	errNotActive         = errors.New("ledger: entry is not active")
	// ErrNotFound is returned when an entry id is absent.
	ErrNotFound = errors.New("ledger: not found")
)

type fileDoc struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Store is a project-scoped decision ledger backed by one JSON file under
// globalRoot/ledger/. Writes are mutex-serialized and replaced atomically.
type Store struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
	closed  bool
	// now, when non-nil, overrides time.Now for tests.
	now func() time.Time
}

// Open opens (or creates) the ledger file for projectKey under globalRoot/ledger.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, errEmptyRoot
	}
	if projectKey == "" {
		return nil, errEmptyProject
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "ledger")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create ledger directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("resolve ledger path: %w", err)
	}
	s := &Store{
		path:    path,
		entries: make(map[string]Entry),
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

// Append inserts a new active entry. When in.Supersedes is set, that prior
// active entry is marked superseded in the same atomic persist.
func (s *Store) Append(in AppendInput) (Entry, error) {
	kind := strings.TrimSpace(in.Kind)
	if kind == "" {
		return Entry{}, errEmptyKind
	}
	if !ValidKind(kind) {
		return Entry{}, errInvalidKind
	}
	statement := strings.TrimSpace(in.Statement)
	if err := validateStatement(statement); err != nil {
		return Entry{}, err
	}
	conf := strings.TrimSpace(in.Confidence)
	if conf == "" {
		conf = ConfidenceMedium
	}
	if !ValidConfidence(conf) {
		return Entry{}, errInvalidConfidence
	}
	author := strings.TrimSpace(in.AuthorSession)
	if author == "" {
		return Entry{}, errEmptyAuthor
	}
	root := strings.TrimSpace(in.AuthorRoot)
	if root == "" {
		root = author
	}
	agent := strings.TrimSpace(in.AuthorAgent)
	if len(agent) > maxAgentLen {
		return Entry{}, fmt.Errorf("ledger: author_agent exceeds %d bytes", maxAgentLen)
	}
	evidence, err := normalizeRefs(in.EvidenceRefs, maxEvidence, maxEvidenceLen, "evidence_refs")
	if err != nil {
		return Entry{}, err
	}
	pins, err := normalizePins(in.EvidencePins)
	if err != nil {
		return Entry{}, err
	}
	paths, err := normalizePaths(in.ScopePaths)
	if err != nil {
		return Entry{}, err
	}
	tasks, err := normalizeTaskIDs(in.ScopeTaskIDs)
	if err != nil {
		return Entry{}, err
	}
	supersedes := strings.TrimSpace(in.Supersedes)

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Entry{}, errClosed
	}
	if len(s.entries) >= maxEntries {
		return Entry{}, errFull
	}

	var prior Entry
	var priorOK bool
	if supersedes != "" {
		prior, priorOK = s.entries[supersedes]
		if !priorOK {
			return Entry{}, ErrNotFound
		}
		if prior.Status != StatusActive {
			return Entry{}, errNotActive
		}
	}

	id, err := newID()
	if err != nil {
		return Entry{}, err
	}
	now := s.clock()
	e := Entry{
		ID:            id,
		Kind:          kind,
		Statement:     statement,
		Confidence:    conf,
		EvidenceRefs:  evidence,
		EvidencePins:  pins,
		Status:        StatusActive,
		ScopePaths:    paths,
		ScopeTaskIDs:  tasks,
		AuthorSession: author,
		AuthorAgent:   agent,
		AuthorRoot:    root,
		CreatedAt:     now,
		UpdatedAt:     now,
		Supersedes:    supersedes,
	}

	// Snapshot for rollback on persist failure.
	var oldPrior Entry
	if priorOK {
		oldPrior = prior
		prior.Status = StatusSuperseded
		prior.SupersededBy = id
		prior.UpdatedAt = now
		s.entries[supersedes] = prior
	}
	s.entries[id] = e
	if err := s.persistLocked(); err != nil {
		delete(s.entries, id)
		if priorOK {
			s.entries[supersedes] = oldPrior
		}
		return Entry{}, err
	}
	return Clone(e), nil
}

// Get returns a deep copy when present.
func (s *Store) Get(id string) (Entry, bool, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Entry{}, false, errClosed
	}
	e, ok := s.entries[id]
	if !ok {
		return Entry{}, false, nil
	}
	return Clone(e), true, nil
}

// List returns entries newest-CreatedAt first, filtered.
func (s *Store) List(filter ListFilter) ([]Entry, error) {
	filter.Status = strings.TrimSpace(filter.Status)
	filter.Kind = strings.TrimSpace(filter.Kind)
	filter.Path = strings.TrimSpace(filter.Path)
	filter.TaskID = strings.TrimSpace(filter.TaskID)
	filter.AuthorSession = strings.TrimSpace(filter.AuthorSession)
	if filter.Status != "" && !ValidStatus(filter.Status) {
		return nil, errInvalidStatus
	}
	if filter.Kind != "" && !ValidKind(filter.Kind) {
		return nil, errInvalidKind
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		if filter.Status != "" && e.Status != filter.Status {
			continue
		}
		if filter.Kind != "" && e.Kind != filter.Kind {
			continue
		}
		if filter.AuthorSession != "" && e.AuthorSession != filter.AuthorSession {
			continue
		}
		if !MatchScope(e, filter.Path, filter.TaskID) {
			continue
		}
		out = append(out, Clone(e))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// ActiveSlice is List with status=active (for context bundles / spawn path).
func (s *Store) ActiveSlice(path, taskID string) ([]Entry, error) {
	return s.List(ListFilter{
		Status: StatusActive,
		Path:   path,
		TaskID: taskID,
	})
}

// Invalidate marks an active entry invalidated with reason/evidence.
// History is preserved (row stays; status changes).
func (s *Store) Invalidate(id string, in InvalidateInput) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, ErrNotFound
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" {
		return Entry{}, errEmptyReason
	}
	if len(reason) > maxReasonLen {
		return Entry{}, errReasonTooLong
	}
	evidence, err := normalizeRefs(in.Evidence, maxEvidence, maxEvidenceLen, "invalidate_evidence")
	if err != nil {
		return Entry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Entry{}, errClosed
	}
	cur, ok := s.entries[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	if cur.Status != StatusActive {
		return Entry{}, errNotActive
	}
	old := cur
	now := s.clock()
	cur.Status = StatusInvalidated
	cur.InvalidateReason = reason
	cur.InvalidateEvidence = evidence
	cur.InvalidatedAt = &now
	cur.UpdatedAt = now
	s.entries[id] = cur
	if err := s.persistLocked(); err != nil {
		s.entries[id] = old
		return Entry{}, err
	}
	return Clone(cur), nil
}

// Supersede marks prior active entry superseded and appends a replacement.
// Equivalent to Append with Supersedes set; exposed for clear tool API.
func (s *Store) Supersede(priorID string, in AppendInput) (Entry, error) {
	in.Supersedes = strings.TrimSpace(priorID)
	if in.Supersedes == "" {
		return Entry{}, ErrNotFound
	}
	return s.Append(in)
}

// Revalidate replaces evidence pins on an active entry without changing status.
// Use after the agent re-checks repository evidence. History is preserved.
func (s *Store) Revalidate(id string, pins []EvidencePin) (Entry, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Entry{}, ErrNotFound
	}
	normalized, err := normalizePins(pins)
	if err != nil {
		return Entry{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Entry{}, errClosed
	}
	cur, ok := s.entries[id]
	if !ok {
		return Entry{}, ErrNotFound
	}
	if cur.Status != StatusActive {
		return Entry{}, errNotActive
	}
	old := cur
	cur.EvidencePins = normalized
	cur.UpdatedAt = s.clock()
	s.entries[id] = cur
	if err := s.persistLocked(); err != nil {
		s.entries[id] = old
		return Entry{}, err
	}
	return Clone(cur), nil
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read ledger: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode ledger: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode ledger: unsupported version %d", doc.Version)
	}
	for _, e := range doc.Entries {
		if strings.TrimSpace(e.ID) == "" {
			continue
		}
		if !ValidKind(e.Kind) {
			continue
		}
		if !ValidStatus(e.Status) {
			e.Status = StatusActive
		}
		if !ValidConfidence(e.Confidence) {
			e.Confidence = ConfidenceMedium
		}
		if e.AuthorRoot == "" {
			e.AuthorRoot = e.AuthorSession
		}
		s.entries[e.ID] = e
	}
	return nil
}

func (s *Store) persistLocked() error {
	list := make([]Entry, 0, len(s.entries))
	for _, e := range s.entries {
		list = append(list, Clone(e))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.MarshalIndent(fileDoc{
		Version: fileVersion,
		Entries: list,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode ledger: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".ledger-*.tmp")
	if err != nil {
		return fmt.Errorf("create ledger temp: %w", err)
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
		return fmt.Errorf("chmod ledger temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write ledger temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync ledger temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close ledger temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace ledger file: %w", err)
	}
	cleanup = false
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("ledger: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
