// Package issue stores project-scoped tracked issues with open/closed status.
// cmd/strike opens the store at startup; tools and internal/host/local wrap it
// so agent tools and the /issues command share one durable project DB.
// Storage layout matches internal/persist/memory (JSON under ~/.strike/issues/).
package issue

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
	fileVersion   = 1
	exportFormat  = "strike.issues"
	exportVersion = 1
	maxTitleLen   = 512
	maxBodyLen    = 64 * 1024
	maxIssues     = 1000
	StatusOpen    = "open"
	StatusClosed  = "closed"
)

var (
	errClosed        = errors.New("issue: store is closed")
	errEmptyTitle    = errors.New("issue: title is required")
	errTitleTooLong  = fmt.Errorf("issue: title exceeds %d bytes", maxTitleLen)
	errBodyTooLong   = fmt.Errorf("issue: body exceeds %d bytes", maxBodyLen)
	errFull          = fmt.Errorf("issue: at most %d issues", maxIssues)
	errInvalidStatus = errors.New("issue: status must be open or closed")
	// ErrNotFound is returned when an issue id is absent.
	ErrNotFound = errors.New("issue: not found")
)

// Issue is one durable project issue record.
type Issue struct {
	ID        int       `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type fileDoc struct {
	Version int     `json:"version"`
	NextID  int     `json:"next_id"`
	Issues  []Issue `json:"issues"`
}

// Store is a project-scoped issue database backed by one JSON file.
type Store struct {
	mu     sync.Mutex
	path   string
	nextID int
	issues map[int]Issue
	closed bool
}

// Open opens (or creates) the issue file for projectKey under globalRoot/issues.
func Open(globalRoot, projectKey string) (*Store, error) {
	if globalRoot == "" {
		return nil, fmt.Errorf("issue: global root is empty")
	}
	if projectKey == "" {
		return nil, fmt.Errorf("issue: project key is empty")
	}
	digest := sha256.Sum256([]byte(projectKey))
	name := hex.EncodeToString(digest[:]) + ".json"
	dir := filepath.Join(globalRoot, "issues")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create issues directory: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("resolve issues path: %w", err)
	}
	s := &Store{
		path:   path,
		nextID: 1,
		issues: make(map[int]Issue),
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

// Get returns the issue for id when present.
func (s *Store) Get(id int) (Issue, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Issue{}, false, errClosed
	}
	iss, ok := s.issues[id]
	if !ok {
		return Issue{}, false, nil
	}
	return cloneIssue(iss), true, nil
}

// List returns issues sorted by id ascending. When status is non-empty, only
// issues with that status are returned (open or closed).
func (s *Store) List(status string) ([]Issue, error) {
	status = strings.TrimSpace(status)
	if status != "" && status != StatusOpen && status != StatusClosed {
		return nil, errInvalidStatus
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	out := make([]Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		if status != "" && iss.Status != status {
			continue
		}
		out = append(out, cloneIssue(iss))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Create inserts a new open issue and returns it.
func (s *Store) Create(title, body string) (Issue, error) {
	title = strings.TrimSpace(title)
	if err := validateTitle(title); err != nil {
		return Issue{}, err
	}
	if len(body) > maxBodyLen {
		return Issue{}, errBodyTooLong
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Issue{}, errClosed
	}
	if len(s.issues) >= maxIssues {
		return Issue{}, errFull
	}
	now := time.Now().UTC()
	iss := Issue{
		ID:        s.nextID,
		Title:     title,
		Body:      body,
		Status:    StatusOpen,
		CreatedAt: now,
		UpdatedAt: now,
	}
	s.nextID++
	s.issues[iss.ID] = iss
	if err := s.persistLocked(); err != nil {
		return Issue{}, err
	}
	return cloneIssue(iss), nil
}

// Update patches title, body, and/or status for an existing issue.
// Pass nil pointers to leave a field unchanged.
func (s *Store) Update(id int, title, body, status *string) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Issue{}, errClosed
	}
	iss, ok := s.issues[id]
	if !ok {
		return Issue{}, ErrNotFound
	}
	if title != nil {
		t := strings.TrimSpace(*title)
		if err := validateTitle(t); err != nil {
			return Issue{}, err
		}
		iss.Title = t
	}
	if body != nil {
		if len(*body) > maxBodyLen {
			return Issue{}, errBodyTooLong
		}
		iss.Body = *body
	}
	if status != nil {
		st := strings.TrimSpace(*status)
		if st != StatusOpen && st != StatusClosed {
			return Issue{}, errInvalidStatus
		}
		iss.Status = st
	}
	iss.UpdatedAt = time.Now().UTC()
	s.issues[id] = iss
	if err := s.persistLocked(); err != nil {
		return Issue{}, err
	}
	return cloneIssue(iss), nil
}

// CloseIssue sets status to closed.
func (s *Store) CloseIssue(id int) (Issue, error) {
	st := StatusClosed
	return s.Update(id, nil, nil, &st)
}

// Close marks the store closed. The file remains on disk.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// exportDoc is the portable backup format for project issues (git/handoff).
// It is distinct from the on-disk store fileDoc.
type exportDoc struct {
	Format  string  `json:"format"`
	Version int     `json:"version"`
	NextID  int     `json:"next_id"`
	Issues  []Issue `json:"issues"`
}

// Export writes a versioned portable JSON snapshot to path.
// path must already be cleaned/resolved by the caller.
func (s *Store) Export(path string) error {
	data, err := s.ExportBytes()
	if err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// ExportBytes returns the portable JSON snapshot without touching the filesystem.
func (s *Store) ExportBytes() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errClosed
	}
	issues := make([]Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		issues = append(issues, cloneIssue(iss))
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	data, err := json.MarshalIndent(exportDoc{
		Format:  exportFormat,
		Version: exportVersion,
		NextID:  s.nextID,
		Issues:  issues,
	}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode issues export: %w", err)
	}
	return append(data, '\n'), nil
}

// Import reads a portable JSON snapshot from path and merges or replaces issues.
// When replace is true the store is cleared first; otherwise issues merge by id
// (imported wins on conflict). IDs are preserved. Returns the number of issues
// applied from the file.
func (s *Store) Import(path string, replace bool) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("read issues import: %w", err)
	}
	return s.ImportBytes(data, replace)
}

// ImportBytes applies a portable JSON snapshot. See Import.
func (s *Store) ImportBytes(data []byte, replace bool) (int, error) {
	doc, err := decodeExport(data)
	if err != nil {
		return 0, err
	}
	clean := make([]Issue, 0, len(doc.Issues))
	seen := make(map[int]struct{}, len(doc.Issues))
	maxID := 0
	for _, iss := range doc.Issues {
		if iss.ID < 1 {
			return 0, fmt.Errorf("issue import: invalid id %d", iss.ID)
		}
		if _, dup := seen[iss.ID]; dup {
			return 0, fmt.Errorf("issue import: duplicate id %d", iss.ID)
		}
		seen[iss.ID] = struct{}{}
		title := strings.TrimSpace(iss.Title)
		if err := validateTitle(title); err != nil {
			return 0, fmt.Errorf("issue import: id %d: %w", iss.ID, err)
		}
		if len(iss.Body) > maxBodyLen {
			return 0, fmt.Errorf("issue import: id %d: %w", iss.ID, errBodyTooLong)
		}
		status := strings.TrimSpace(iss.Status)
		if status == "" {
			status = StatusOpen
		}
		if status != StatusOpen && status != StatusClosed {
			return 0, fmt.Errorf("issue import: id %d: %w", iss.ID, errInvalidStatus)
		}
		created := iss.CreatedAt
		updated := iss.UpdatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		if updated.IsZero() {
			updated = created
		}
		clean = append(clean, Issue{
			ID:        iss.ID,
			Title:     title,
			Body:      iss.Body,
			Status:    status,
			CreatedAt: created.UTC(),
			UpdatedAt: updated.UTC(),
		})
		if iss.ID > maxID {
			maxID = iss.ID
		}
	}
	nextID := doc.NextID
	if nextID < 1 {
		nextID = 1
	}
	if maxID >= nextID {
		nextID = maxID + 1
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, errClosed
	}
	if replace {
		s.issues = make(map[int]Issue, len(clean))
		s.nextID = nextID
	} else {
		newCount := 0
		for _, iss := range clean {
			if _, ok := s.issues[iss.ID]; !ok {
				newCount++
			}
		}
		if len(s.issues)+newCount > maxIssues {
			return 0, errFull
		}
		if nextID > s.nextID {
			s.nextID = nextID
		}
	}
	if replace && len(clean) > maxIssues {
		return 0, errFull
	}
	for _, iss := range clean {
		s.issues[iss.ID] = iss
	}
	// Keep nextID ahead of any retained id after merge.
	for id := range s.issues {
		if id >= s.nextID {
			s.nextID = id + 1
		}
	}
	if err := s.persistLocked(); err != nil {
		return 0, err
	}
	return len(clean), nil
}

func decodeExport(data []byte) (exportDoc, error) {
	if len(strings.TrimSpace(string(data))) == 0 {
		return exportDoc{}, fmt.Errorf("issue import: empty file")
	}
	var doc exportDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return exportDoc{}, fmt.Errorf("issue import: bad JSON: %w", err)
	}
	if doc.Format != exportFormat {
		return exportDoc{}, fmt.Errorf("issue import: unsupported format %q", doc.Format)
	}
	if doc.Version != exportVersion {
		return exportDoc{}, fmt.Errorf("issue import: unsupported version %d", doc.Version)
	}
	return doc, nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create export directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".issues-export-*.tmp")
	if err != nil {
		return fmt.Errorf("create export temp: %w", err)
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
		return fmt.Errorf("chmod export temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write export temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync export temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close export temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace export file: %w", err)
	}
	cleanup = false
	return nil
}

func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read issues: %w", err)
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return nil
	}
	var doc fileDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("decode issues: %w", err)
	}
	if doc.Version != fileVersion {
		return fmt.Errorf("decode issues: unsupported version %d", doc.Version)
	}
	if doc.NextID < 1 {
		doc.NextID = 1
	}
	s.nextID = doc.NextID
	for _, iss := range doc.Issues {
		if iss.ID < 1 {
			continue
		}
		if iss.Status != StatusOpen && iss.Status != StatusClosed {
			iss.Status = StatusOpen
		}
		s.issues[iss.ID] = Issue{
			ID:        iss.ID,
			Title:     iss.Title,
			Body:      iss.Body,
			Status:    iss.Status,
			CreatedAt: iss.CreatedAt,
			UpdatedAt: iss.UpdatedAt,
		}
		if iss.ID >= s.nextID {
			s.nextID = iss.ID + 1
		}
	}
	return nil
}

func (s *Store) persistLocked() error {
	issues := make([]Issue, 0, len(s.issues))
	for _, iss := range s.issues {
		issues = append(issues, cloneIssue(iss))
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].ID < issues[j].ID })
	data, err := json.MarshalIndent(fileDoc{
		Version: fileVersion,
		NextID:  s.nextID,
		Issues:  issues,
	}, "", "  ")
	if err != nil {
		return fmt.Errorf("encode issues: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".issues-*.tmp")
	if err != nil {
		return fmt.Errorf("create issues temp: %w", err)
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
		return fmt.Errorf("chmod issues temp: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write issues temp: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync issues temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close issues temp: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		return fmt.Errorf("replace issues file: %w", err)
	}
	cleanup = false
	return nil
}

func validateTitle(title string) error {
	if title == "" {
		return errEmptyTitle
	}
	if len(title) > maxTitleLen {
		return errTitleTooLong
	}
	for _, r := range title {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("issue: title contains invalid character")
		}
	}
	return nil
}

func cloneIssue(iss Issue) Issue {
	return Issue{
		ID:        iss.ID,
		Title:     iss.Title,
		Body:      iss.Body,
		Status:    iss.Status,
		CreatedAt: iss.CreatedAt,
		UpdatedAt: iss.UpdatedAt,
	}
}
