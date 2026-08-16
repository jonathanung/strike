package artifact

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
	fileVersion   = 1
	maxTitleLen   = 512
	maxContentLen = 256 * 1024
	maxArtifacts  = 2000
)

var (
	errClosed         = errors.New("artifact: store is closed")
	errEmptyType      = errors.New("artifact: type is required")
	errInvalidType    = errors.New("artifact: unknown type")
	errInvalidScope   = errors.New("artifact: scope must be project or session")
	errInvalidAccess  = errors.New("artifact: access must be owner or team")
	errEmptyOwner     = errors.New("artifact: owner session is required")
	errEmptyRoot      = errors.New("artifact: owner root is required")
	errSessionID      = errors.New("artifact: session_id is required when scope is session")
	errTitleTooLong   = fmt.Errorf("artifact: title exceeds %d bytes", maxTitleLen)
	errContentTooLong = fmt.Errorf("artifact: content exceeds %d bytes", maxContentLen)
	errFull           = fmt.Errorf("artifact: at most %d artifacts", maxArtifacts)
	// ErrNotFound is returned when an artifact id is absent (or expired/invisible).
	ErrNotFound = errors.New("artifact: not found")
	// ErrConflict is returned when expectedVersion does not match the stored version.
	ErrConflict = errors.New("artifact: version conflict")
	// ErrDenied is returned when the actor lacks read/write access.
	ErrDenied = errors.New("artifact: permission denied")
	// ErrExpired is returned when operating on an expired artifact.
	ErrExpired = errors.New("artifact: expired")
)

type fileDoc struct {
	Version   int        `json:"version"`
	Artifacts []Artifact `json:"artifacts"`
}

// Store is a project-scoped artifact database backed by one JSON file under
// globalRoot/artifacts/. Writes are mutex-serialized and replaced atomically.
type Store struct {
	mu        sync.Mutex
	path      string
	artifacts map[string]Artifact
	closed    bool
	// now, when non-nil, overrides time.Now for tests (TTL).
	now func() time.Time
}

// Open opens (or creates) the artifact file for projectKey under globalRoot/artifacts.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("artifact: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("artifact: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "artifacts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create artifacts directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("resolve artifacts path: %w", err)
	}
	s := &Store{
		path:      path,
		artifacts: make(map[string]Artifact),
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

// Create inserts a new artifact at version 1.
func (s *Store) Create(in CreateInput) (Artifact, error) {
	typ := strings.TrimSpace(in.Type)
	if typ == "" {
		return Artifact{}, errEmptyType
	}
	if !ValidType(typ) {
		return Artifact{}, fmt.Errorf("%w: %q", errInvalidType, typ)
	}
	title := strings.TrimSpace(in.Title)
	if err := validateTitle(title); err != nil {
		return Artifact{}, err
	}
	if err := validateContent(in.Content); err != nil {
		return Artifact{}, err
	}
	scope := strings.TrimSpace(in.Scope)
	if scope == "" {
		scope = ScopeProject
	}
	if !ValidScope(scope) {
		return Artifact{}, errInvalidScope
	}
	access := strings.TrimSpace(in.Access)
	if access == "" {
		access = AccessTeam
	}
	if !ValidAccess(access) {
		return Artifact{}, errInvalidAccess
	}
	ownerSession := strings.TrimSpace(in.OwnerSession)
	if ownerSession == "" {
		return Artifact{}, errEmptyOwner
	}
	ownerRoot := strings.TrimSpace(in.OwnerRoot)
	if ownerRoot == "" {
		ownerRoot = ownerSession
	}
	sessionID := strings.TrimSpace(in.SessionID)
	if scope == ScopeSession {
		if sessionID == "" {
			sessionID = ownerSession
		}
		if sessionID == "" {
			return Artifact{}, errSessionID
		}
	} else {
		sessionID = ""
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Artifact{}, errClosed
	}
	if len(s.artifacts) >= maxArtifacts {
		return Artifact{}, errFull
	}
	id, err := newID()
	if err != nil {
		return Artifact{}, err
	}
	now := s.clock()
	a := Artifact{
		ID:           id,
		Type:         typ,
		Title:        title,
		Content:      in.Content,
		Version:      1,
		Scope:        scope,
		SessionID:    sessionID,
		Access:       access,
		OwnerSession: ownerSession,
		OwnerRoot:    ownerRoot,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if in.TTL > 0 {
		exp := now.Add(in.TTL)
		a.ExpiresAt = &exp
	}
	s.artifacts[id] = a
	if err := s.persistLocked(); err != nil {
		delete(s.artifacts, id)
		return Artifact{}, err
	}
	return Clone(a), nil
}

// Get returns a deep copy when present, visible to the actor, and not expired.
// missing is distinguished from denied: denied returns ErrDenied; missing ok=false.
func (s *Store) Get(id, actorSession, actorRoot string) (Artifact, bool, error) {
	id = strings.TrimSpace(id)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Artifact{}, false, errClosed
	}
	a, ok := s.artifacts[id]
	if !ok {
		return Artifact{}, false, nil
	}
	if s.isExpiredLocked(a) {
		return Artifact{}, false, nil
	}
	if !CanRead(a, actorSession, actorRoot) {
		return Artifact{}, false, ErrDenied
	}
	return Clone(a), true, nil
}

// GetVersion is Get plus an optional exact version match (ok=false when version differs).
func (s *Store) GetVersion(id string, version int, actorSession, actorRoot string) (Artifact, bool, error) {
	a, ok, err := s.Get(id, actorSession, actorRoot)
	if err != nil || !ok {
		return a, ok, err
	}
	if version > 0 && a.Version != version {
		return Artifact{}, false, nil
	}
	return a, true, nil
}

// ListFilter selects artifacts for List.
type ListFilter struct {
	Type      string // empty = any
	Scope     string // empty = any
	SessionID string // when set, only session-scoped with this session_id
	// IncludeExpired keeps expired rows (default drops them).
	IncludeExpired bool
}

// List returns metadata newest-UpdatedAt first, filtered and access-checked.
func (s *Store) List(actorSession, actorRoot string, filter ListFilter) ([]Meta, error) {
	filter.Type = strings.TrimSpace(filter.Type)
	filter.Scope = strings.TrimSpace(filter.Scope)
	filter.SessionID = strings.TrimSpace(filter.SessionID)
	if filter.Type != "" && !ValidType(filter.Type) {
		return nil, fmt.Errorf("%w: %q", errInvalidType, filter.Type)
	}
	if filter.Scope != "" && !ValidScope(filter.Scope) {
		return nil, errInvalidScope
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Meta, 0, len(s.artifacts))
	for _, a := range s.artifacts {
		if !filter.IncludeExpired && s.isExpiredLocked(a) {
			continue
		}
		if filter.Type != "" && a.Type != filter.Type {
			continue
		}
		if filter.Scope != "" && a.Scope != filter.Scope {
			continue
		}
		if filter.SessionID != "" && a.SessionID != filter.SessionID {
			continue
		}
		if !CanRead(a, actorSession, actorRoot) {
			continue
		}
		out = append(out, MetaFrom(a))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	return out, nil
}

// Update CAS-updates content/title/access/TTL. expectedVersion must match.
func (s *Store) Update(id, actorSession, actorRoot string, expectedVersion int, in UpdateInput) (Artifact, error) {
	id = strings.TrimSpace(id)
	actorSession = strings.TrimSpace(actorSession)
	actorRoot = strings.TrimSpace(actorRoot)
	if actorRoot == "" {
		actorRoot = actorSession
	}
	if id == "" {
		return Artifact{}, ErrNotFound
	}
	if actorSession == "" {
		return Artifact{}, errEmptyOwner
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if err := validateTitle(t); err != nil {
			return Artifact{}, err
		}
		in.Title = &t
	}
	if in.Content != nil {
		if err := validateContent(*in.Content); err != nil {
			return Artifact{}, err
		}
	}
	if in.Access != nil {
		a := strings.TrimSpace(*in.Access)
		if !ValidAccess(a) {
			return Artifact{}, errInvalidAccess
		}
		in.Access = &a
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Artifact{}, errClosed
	}
	cur, ok := s.artifacts[id]
	if !ok {
		return Artifact{}, ErrNotFound
	}
	if s.isExpiredLocked(cur) {
		return Artifact{}, ErrExpired
	}
	if !CanWrite(cur, actorSession, actorRoot) {
		return Artifact{}, ErrDenied
	}
	if expectedVersion != cur.Version {
		return Artifact{}, fmt.Errorf("%w: have %d, expected %d", ErrConflict, cur.Version, expectedVersion)
	}

	working := Clone(cur)
	if in.Title != nil {
		working.Title = *in.Title
	}
	if in.Content != nil {
		working.Content = *in.Content
	}
	if in.Access != nil {
		// Only the owning session may tighten/loosen access.
		if actorSession != cur.OwnerSession {
			return Artifact{}, fmt.Errorf("%w: only owner may change access", ErrDenied)
		}
		working.Access = *in.Access
	}
	if in.TTL != nil {
		if *in.TTL <= 0 {
			working.ExpiresAt = nil
		} else {
			exp := s.clock().Add(*in.TTL)
			working.ExpiresAt = &exp
		}
	}
	working.Version = cur.Version + 1
	working.UpdatedAt = s.clock()
	// Preserve identity fields.
	working.ID = cur.ID
	working.Type = cur.Type
	working.Scope = cur.Scope
	working.SessionID = cur.SessionID
	working.OwnerSession = cur.OwnerSession
	working.OwnerRoot = cur.OwnerRoot
	working.CreatedAt = cur.CreatedAt

	s.artifacts[id] = working
	if err := s.persistLocked(); err != nil {
		s.artifacts[id] = cur
		return Artifact{}, err
	}
	return Clone(working), nil
}

func (s *Store) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}

func (s *Store) isExpiredLocked(a Artifact) bool {
	if a.ExpiresAt == nil {
		return false
	}
	return !s.clock().Before(a.ExpiresAt.UTC())
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read artifacts: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode artifacts: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode artifacts: unsupported version %d", doc.Version)
	}
	for _, a := range doc.Artifacts {
		if strings.TrimSpace(a.ID) == "" {
			continue
		}
		if !ValidType(a.Type) {
			continue
		}
		if !ValidScope(a.Scope) {
			a.Scope = ScopeProject
		}
		if !ValidAccess(a.Access) {
			a.Access = AccessTeam
		}
		if a.Version < 1 {
			a.Version = 1
		}
		if a.OwnerRoot == "" {
			a.OwnerRoot = a.OwnerSession
		}
		s.artifacts[a.ID] = a
	}
	return nil
}

func (s *Store) persistLocked() error {
	list := make([]Artifact, 0, len(s.artifacts))
	for _, a := range s.artifacts {
		list = append(list, Clone(a))
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.MarshalIndent(fileDoc{
		Version:   fileVersion,
		Artifacts: list,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode artifacts: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".artifacts-*.tmp")
	if err != nil {
		return fmt.Errorf("create artifacts temp: %w", err)
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
		return fmt.Errorf("chmod artifacts temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write artifacts temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync artifacts temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close artifacts temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace artifacts file: %w", err)
	}
	cleanup = false
	return nil
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("artifact: generate id: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}
