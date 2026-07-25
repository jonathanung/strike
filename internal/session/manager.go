package session

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Info is a snapshot of session identity and durable metadata.
type Info struct {
	ID              string
	ParentSessionID string
	Title           string
	Path            string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Open            bool
}

// CreateOptions configures Manager.Create. Empty ID mints via NewID.
type CreateOptions struct {
	ID              string
	ParentSessionID string
	Title           string
}

// Manager coordinates concurrent open session stores under a directory and
// inventories durable sessions on disk. It is safe for concurrent use.
// Engine ownership and per-session interrupt remain outside this package.
type Manager struct {
	dir string

	mu       sync.Mutex
	sessions map[string]*managed
}

type managed struct {
	store *Store
	info  Info
}

// NewManager returns a manager rooted at dir (created on first Create/Open).
func NewManager(dir string) *Manager {
	return &Manager{
		dir:      dir,
		sessions: make(map[string]*managed),
	}
}

// Dir is the sessions directory this manager owns.
func (m *Manager) Dir() string { return m.dir }

// Create opens a new session log, writes sidecar meta, and tracks it as open.
func (m *Manager) Create(opts CreateOptions) (Info, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = NewID()
	}
	if err := validateID(id); err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; ok {
		return Info{}, fmt.Errorf("session %q already open", id)
	}
	if _, err := os.Stat(LogPath(m.dir, id)); err == nil {
		return Info{}, fmt.Errorf("session %q already exists", id)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Info{}, err
	}

	store, err := Open(m.dir, id)
	if err != nil {
		return Info{}, err
	}
	now := time.Now().UTC()
	if t, ok := idCreatedAt(id); ok {
		now = t
	}
	meta := Meta{
		Title:           strings.TrimSpace(opts.Title),
		ParentSessionID: strings.TrimSpace(opts.ParentSessionID),
		CreatedAt:       now.Format(time.RFC3339Nano),
	}
	if err := WriteMeta(m.dir, id, meta); err != nil {
		_ = store.Close()
		return Info{}, err
	}
	info := Info{
		ID:              id,
		ParentSessionID: meta.ParentSessionID,
		Title:           meta.Title,
		Path:            store.Path(),
		CreatedAt:       now,
		UpdatedAt:       now,
		Open:            true,
	}
	m.sessions[id] = &managed{store: store, info: info}
	return info, nil
}

// Open opens an existing session log for append. Creates the log file only if
// it already exists on disk (use Create for new sessions).
func (m *Manager) Open(id string) (Info, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.sessions[id]; ok {
		return e.info, nil
	}

	path := LogPath(m.dir, id)
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Info{}, fmt.Errorf("session %q not found", id)
		}
		return Info{}, err
	}
	store, err := Open(m.dir, id)
	if err != nil {
		return Info{}, err
	}
	info, err := m.infoFromDiskLocked(id, st)
	if err != nil {
		_ = store.Close()
		return Info{}, err
	}
	info.Path = store.Path()
	info.Open = true
	m.sessions[id] = &managed{store: store, info: info}
	return info, nil
}

// Append writes an event to an open session. SessionTitled updates durable title.
func (m *Manager) Append(id string, ev protocol.Event) error {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	e, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session %q is not open", id)
	}
	store := e.store
	m.mu.Unlock()

	if err := store.Append(ev); err != nil {
		return err
	}

	now := time.Now().UTC()
	var title string
	if t, ok := ev.(protocol.SessionTitled); ok {
		title = strings.TrimSpace(t.Title)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok = m.sessions[id]
	if !ok {
		return nil
	}
	e.info.UpdatedAt = now
	if title != "" && e.info.Title != title {
		e.info.Title = title
		_, _ = UpdateMeta(m.dir, id, func(meta *Meta) {
			meta.Title = title
		})
	}
	return nil
}

// Close closes one open session. Idempotent for unknown ids.
func (m *Manager) Close(id string) error {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	e, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.sessions, id)
	store := e.store
	m.mu.Unlock()
	return store.Close()
}

// CloseAll closes every open session. Returns the first error encountered.
func (m *Manager) CloseAll() error {
	m.mu.Lock()
	ids := make([]string, 0, len(m.sessions))
	for id := range m.sessions {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var first error
	for _, id := range ids {
		if err := m.Close(id); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Get returns info for an open session or a durable on-disk session.
func (m *Manager) Get(id string) (Info, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Info{}, err
	}
	m.mu.Lock()
	if e, ok := m.sessions[id]; ok {
		info := e.info
		m.mu.Unlock()
		return info, nil
	}
	m.mu.Unlock()

	path := LogPath(m.dir, id)
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Info{}, fmt.Errorf("session %q not found", id)
		}
		return Info{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.infoFromDiskLocked(id, st)
}

// ListOpen returns currently open sessions, newest UpdatedAt first.
func (m *Manager) ListOpen() []Info {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Info, 0, len(m.sessions))
	for _, e := range m.sessions {
		out = append(out, e.info)
	}
	sortInfos(out)
	return out
}

// List returns all durable sessions under the manager directory (open + closed).
// Newest UpdatedAt first. Open sessions reflect live title/updated state.
func (m *Manager) List() ([]Info, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	seen := make(map[string]Info, len(entries))
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if id == "" || strings.Contains(id, string(filepath.Separator)) {
			continue
		}
		if e, ok := m.sessions[id]; ok {
			seen[id] = e.info
			continue
		}
		st, err := ent.Info()
		if err != nil {
			return nil, err
		}
		info, err := m.infoFromDiskLocked(id, st)
		if err != nil {
			return nil, err
		}
		seen[id] = info
	}
	out := make([]Info, 0, len(seen))
	for _, info := range seen {
		out = append(out, info)
	}
	sortInfos(out)
	return out, nil
}

// Replay loads the full event log for a session id.
func (m *Manager) Replay(id string) ([]protocol.Event, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return nil, err
	}
	return Replay(LogPath(m.dir, id))
}

// Path returns the JSONL path for an open session, or the canonical path if known.
func (m *Manager) Path(id string) string {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.sessions[id]; ok {
		return e.info.Path
	}
	return LogPath(m.dir, id)
}

// Bind returns an Append/Close handle for an open session (runSession tee).
func (m *Manager) Bind(id string) (Bound, error) {
	id = strings.TrimSpace(id)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[id]; !ok {
		return Bound{}, fmt.Errorf("session %q is not open", id)
	}
	return Bound{m: m, id: id}, nil
}

// Bound is the narrow persistence surface for one open session.
type Bound struct {
	m  *Manager
	id string
}

// ID is the bound session identifier.
func (b Bound) ID() string { return b.id }

// Path is the JSONL log path.
func (b Bound) Path() string { return b.m.Path(b.id) }

// Append persists one event.
func (b Bound) Append(ev protocol.Event) error { return b.m.Append(b.id, ev) }

// Close releases the open session store.
func (b Bound) Close() error { return b.m.Close(b.id) }

// TaggedEvent pairs a protocol event with the session that produced it.
type TaggedEvent struct {
	SessionID string
	Event     protocol.Event
}

// Mux fans in events from multiple session sources into one tagged stream.
// Each map key labels its source. When an event carries a non-empty
// Correlation.SessionID, that value is preferred as the tag. The output
// channel closes after every source is exhausted or ctx is cancelled.
func Mux(ctx context.Context, sources map[string]<-chan protocol.Event) <-chan TaggedEvent {
	out := make(chan TaggedEvent, 256)
	if len(sources) == 0 {
		close(out)
		return out
	}
	var wg sync.WaitGroup
	for id, ch := range sources {
		if ch == nil {
			continue
		}
		wg.Add(1)
		go func(sessionID string, src <-chan protocol.Event) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case ev, ok := <-src:
					if !ok {
						return
					}
					tag := sessionID
					if corr, ok := eventSessionID(ev); ok && corr != "" {
						tag = corr
					}
					select {
					case out <- TaggedEvent{SessionID: tag, Event: ev}:
					case <-ctx.Done():
						return
					}
				}
			}
		}(id, ch)
	}
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

func eventSessionID(ev protocol.Event) (string, bool) {
	// protocol events embed Correlation anonymously without a shared getter.
	switch e := ev.(type) {
	case protocol.UserMessage:
		return e.SessionID, true
	case protocol.SessionTitled:
		return e.SessionID, true
	case protocol.TurnStarted:
		return e.SessionID, true
	case protocol.TurnCompleted:
		return e.SessionID, true
	case protocol.TextDelta:
		return e.SessionID, true
	case protocol.ToolCallBegin:
		return e.SessionID, true
	case protocol.ToolCallEnd:
		return e.SessionID, true
	case protocol.ToolCallOutput:
		return e.SessionID, true
	case protocol.PermissionAsked:
		return e.SessionID, true
	case protocol.PermissionResolved:
		return e.SessionID, true
	case protocol.QuestionAsked:
		return e.SessionID, true
	case protocol.QuestionResolved:
		return e.SessionID, true
	case protocol.ChildStarted:
		return e.SessionID, true
	case protocol.ChildCompleted:
		return e.SessionID, true
	case protocol.EngineError:
		return e.SessionID, true
	case protocol.ModelSelected:
		return e.SessionID, true
	case protocol.AgentSelected:
		return e.SessionID, true
	case protocol.EffortSelected:
		return e.SessionID, true
	case protocol.AutonomySelected:
		return e.SessionID, true
	case protocol.FastSelected:
		return e.SessionID, true
	case protocol.FilesInvalidated:
		return e.SessionID, true
	case protocol.UsageReported:
		return e.SessionID, true
	case protocol.ProviderRetrying:
		return e.SessionID, true
	case protocol.SessionMeta:
		return e.SessionID, true
	default:
		return "", false
	}
}

func (m *Manager) infoFromDiskLocked(id string, st os.FileInfo) (Info, error) {
	meta, err := ReadMeta(m.dir, id)
	if err != nil {
		return Info{}, err
	}
	created := st.ModTime().UTC()
	if meta.CreatedAt != "" {
		if t, err := time.Parse(time.RFC3339Nano, meta.CreatedAt); err == nil {
			created = t.UTC()
		} else if t, err := time.Parse(time.RFC3339, meta.CreatedAt); err == nil {
			created = t.UTC()
		}
	} else if t, ok := idCreatedAt(id); ok {
		created = t
	}
	title := meta.Title
	if title == "" {
		// Cheap title: only if meta missing; full TitleFromEvents is for callers
		// that Replay. Avoid scanning every log on List.
	}
	return Info{
		ID:              id,
		ParentSessionID: meta.ParentSessionID,
		Title:           title,
		Path:            LogPath(m.dir, id),
		CreatedAt:       created,
		UpdatedAt:       st.ModTime().UTC(),
		Open:            false,
	}, nil
}

func sortInfos(infos []Info) {
	sort.SliceStable(infos, func(i, j int) bool {
		if !infos[i].UpdatedAt.Equal(infos[j].UpdatedAt) {
			return infos[i].UpdatedAt.After(infos[j].UpdatedAt)
		}
		return infos[i].ID > infos[j].ID
	})
}

func validateID(id string) error {
	if id == "" {
		return fmt.Errorf("session id is empty")
	}
	if strings.Contains(id, string(filepath.Separator)) || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return fmt.Errorf("session id %q must be a single path segment", id)
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("session id %q is invalid", id)
	}
	return nil
}
