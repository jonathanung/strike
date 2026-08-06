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
	LeadSessionID   string // team lead; empty on roots (self is lead)
	Title           string
	ProjectKey      string // launch project identity; empty on legacy sessions
	WorktreePath    string // strike-managed git worktree; empty = launch cwd
	WorktreeBranch  string
	Path            string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	Open            bool
	PRURL           string
	PRNumber        int
	PRState         string // open|merged|closed when known
}

// CreateOptions configures Manager.Create. Empty ID mints via NewID.
type CreateOptions struct {
	ID              string
	ParentSessionID string
	LeadSessionID   string // implicit team lead for children; empty on roots
	Title           string
	ProjectKey      string // same key as history/memory (canonical project root)
}

// listCacheTTL is how long Manager.List reuses an in-memory snapshot before
// falling through to disk. Invalidation happens on any mutation (Create,
// Delete, Rename, Close, Fork/ForkAt).
const listCacheTTL = 5 * time.Second

// Manager coordinates concurrent open session stores under a directory and
// inventories durable sessions on disk. It is safe for concurrent use.
// Engine ownership and per-session interrupt remain outside this package.
type Manager struct {
	dir string

	mu       sync.Mutex
	sessions map[string]*managed

	listCache    []Info
	listCachedAt time.Time
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
		LeadSessionID:   strings.TrimSpace(opts.LeadSessionID),
		ProjectKey:      strings.TrimSpace(opts.ProjectKey),
		CreatedAt:       now.Format(time.RFC3339Nano),
	}
	if err := WriteMeta(m.dir, id, meta); err != nil {
		_ = store.Close()
		return Info{}, err
	}
	info := Info{
		ID:              id,
		ParentSessionID: meta.ParentSessionID,
		LeadSessionID:   meta.LeadSessionID,
		Title:           meta.Title,
		ProjectKey:      meta.ProjectKey,
		WorktreePath:    meta.WorktreePath,
		WorktreeBranch:  meta.WorktreeBranch,
		Path:            store.Path(),
		CreatedAt:       now,
		UpdatedAt:       now,
		Open:            true,
		PRURL:           meta.PRURL,
		PRNumber:        meta.PRNumber,
		PRState:         NormalizePRState(meta.PRState),
	}
	m.sessions[id] = &managed{store: store, info: info}
	m.invalidateListCache()
	return info, nil
}

// CountOpenRoots returns how many open sessions have no parent (root sessions).
func (m *Manager) CountOpenRoots() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, e := range m.sessions {
		if e.info.ParentSessionID == "" {
			n++
		}
	}
	return n
}

// SetWorktree records a bound worktree on an open session and durable meta.
// Empty path clears the binding.
func (m *Manager) SetWorktree(id, path, branch string) error {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session %q is not open", id)
	}
	path = strings.TrimSpace(path)
	branch = strings.TrimSpace(branch)
	meta, err := UpdateMeta(m.dir, id, func(meta *Meta) {
		meta.WorktreePath = path
		meta.WorktreeBranch = branch
	})
	if err != nil {
		return err
	}
	e.info.WorktreePath = meta.WorktreePath
	e.info.WorktreeBranch = meta.WorktreeBranch
	return nil
}

// Destroy closes an open session (if any) and removes its durable log + meta.
// Used to roll back a half-created session when worktree bind fails.
func (m *Manager) Destroy(id string) error {
	return m.delete(id, true)
}

// Rename sets the durable display title for id (open or closed). Empty title
// clears to untitled. Survives restart via the meta sidecar.
func (m *Manager) Rename(id, title string) (Info, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return Info{}, err
	}
	title = strings.TrimSpace(title)

	// Ensure the session exists on disk or is open.
	if _, err := m.Get(id); err != nil {
		return Info{}, err
	}
	meta, err := UpdateMeta(m.dir, id, func(meta *Meta) {
		meta.Title = title
	})
	if err != nil {
		return Info{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateListCache()
	if e, ok := m.sessions[id]; ok {
		e.info.Title = meta.Title
		return e.info, nil
	}
	// Closed: re-read so callers get a full Info snapshot.
	path := LogPath(m.dir, id)
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Info{}, fmt.Errorf("session %q not found", id)
		}
		return Info{}, err
	}
	return m.infoFromDiskLocked(id, st)
}

// Delete removes a session's JSONL log and meta sidecar. When the session is
// currently open in this manager, force must be true; otherwise Delete returns
// an error and leaves files intact.
func (m *Manager) Delete(id string, force bool) error {
	return m.delete(id, force)
}

func (m *Manager) delete(id string, force bool) error {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return err
	}
	m.mu.Lock()
	_, open := m.sessions[id]
	m.invalidateListCache()
	m.mu.Unlock()
	if open && !force {
		return fmt.Errorf("session %q is open; force required to delete", id)
	}
	// Existence check for closed sessions (open ones are known).
	if !open {
		if _, err := os.Stat(LogPath(m.dir, id)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("session %q not found", id)
			}
			return err
		}
	}
	_ = m.Close(id)
	var first error
	for _, p := range []string{LogPath(m.dir, id), MetaPath(m.dir, id)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) && first == nil {
			first = err
		}
	}
	return first
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
	var prMeta *protocol.SessionMeta
	if sm, ok := ev.(protocol.SessionMeta); ok {
		prMeta = &sm
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
	if prMeta != nil {
		if prMeta.PRURL != "" {
			e.info.PRURL = prMeta.PRURL
		}
		if prMeta.PRNumber != 0 {
			e.info.PRNumber = prMeta.PRNumber
		}
		if st := NormalizePRState(prMeta.PRState); st != "" {
			e.info.PRState = st
		} else if e.info.PRState == "" && e.info.PRURL != "" {
			e.info.PRState = PRStateOpen
		}
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
	m.invalidateListCache()
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

// LatestRoot returns the most recent root session (no parent) for --continue.
// When projectKey is non-empty, only sessions with that ProjectKey qualify
// (legacy empty-key sessions are skipped). Preference: newest UpdatedAt, then
// newest CreatedAt, then lexical ID (NewID is timestamp-first so lexical order
// matches creation).
func (m *Manager) LatestRoot(projectKey string) (Info, error) {
	projectKey = strings.TrimSpace(projectKey)
	list, err := m.List()
	if err != nil {
		return Info{}, err
	}
	var best Info
	found := false
	for _, info := range list {
		if info.ParentSessionID != "" {
			continue
		}
		if projectKey != "" && info.ProjectKey != projectKey {
			continue
		}
		if !found || rootMoreRecent(info, best) {
			best = info
			found = true
		}
	}
	if !found {
		return Info{}, fmt.Errorf("no session to continue")
	}
	return best, nil
}

// BelongsToProject reports whether info is scoped to projectKey. A non-empty
// filter never matches legacy sessions with an empty ProjectKey.
func BelongsToProject(info Info, projectKey string) bool {
	projectKey = strings.TrimSpace(projectKey)
	if projectKey == "" {
		return true
	}
	return info.ProjectKey == projectKey
}

func rootMoreRecent(a, b Info) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) {
		return a.CreatedAt.After(b.CreatedAt)
	}
	return a.ID > b.ID
}

// invalidateListCache clears the List result cache. Call from any method that
// mutates the session set (create, delete, rename, close, fork).
func (m *Manager) invalidateListCache() {
	m.listCache = nil
	m.listCachedAt = time.Time{}
}

// List returns all durable sessions under the manager directory (open + closed).
// Newest UpdatedAt first. Open sessions reflect live title/updated state.
func (m *Manager) List() ([]Info, error) {
	m.mu.Lock()
	if time.Since(m.listCachedAt) < listCacheTTL && m.listCache != nil {
		out := m.listCache
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()

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
	if len(out) > 0 {
		m.listCache = out
		m.listCachedAt = time.Now()
	}
	return out, nil
}

// Sync flushes an open session's JSONL writer so concurrent readers (Replay,
// ReplayJSONL) see the latest appends. No-op when the session is not open.
func (m *Manager) Sync(id string) error {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if e, ok := m.sessions[id]; ok && e.store != nil {
		return e.store.Sync()
	}
	return nil
}

// Replay loads the full event log for a session id.
func (m *Manager) Replay(id string) ([]protocol.Event, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return nil, err
	}
	// Flush open store so live child transcripts are fully visible.
	m.mu.Lock()
	if e, ok := m.sessions[id]; ok && e.store != nil {
		_ = e.store.Sync()
	}
	m.mu.Unlock()
	return Replay(LogPath(m.dir, id))
}

// ReplaySlice loads a bounded event window for a session id.
func (m *Manager) ReplaySlice(id string, offset, limit int) ([]protocol.Event, int, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return nil, 0, err
	}
	// Flush open store so concurrent readers see recent appends.
	m.mu.Lock()
	if e, ok := m.sessions[id]; ok && e.store != nil {
		_ = e.store.Sync()
	}
	m.mu.Unlock()
	return ReplaySlice(LogPath(m.dir, id), offset, limit)
}

// ReplayLast loads up to n trailing events for a session id.
func (m *Manager) ReplayLast(id string, n int) ([]protocol.Event, int, error) {
	id = strings.TrimSpace(id)
	if err := validateID(id); err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	if e, ok := m.sessions[id]; ok && e.store != nil {
		_ = e.store.Sync()
	}
	m.mu.Unlock()
	return ReplayLast(LogPath(m.dir, id), n)
}

// Fork copies sourceID's full event log into a new root session. The parent
// stays intact. Title is "fork of …"; meta.ForkedFrom records lineage.
// ParentSessionID stays empty so the fork remains eligible for --continue and
// the session picker.
func (m *Manager) Fork(sourceID string) (Info, error) {
	return m.ForkAt(sourceID, -1)
}

// ForkAt copies the first keepEvents of sourceID's log into a new root session.
// The parent stays intact. keepEvents < 0 means the full log (same as Fork).
// keepEvents may be 0 (empty transcript fork). keepEvents greater than the log
// length is an error.
func (m *Manager) ForkAt(sourceID string, keepEvents int) (Info, error) {
	sourceID = strings.TrimSpace(sourceID)
	if err := validateID(sourceID); err != nil {
		return Info{}, err
	}

	src, err := m.Get(sourceID)
	if err != nil {
		return Info{}, err
	}
	if src.ParentSessionID != "" {
		return Info{}, fmt.Errorf("session %q is a subagent transcript; fork a root session", sourceID)
	}

	// Flush an open source so Replay sees the latest appends.
	m.mu.Lock()
	if e, ok := m.sessions[sourceID]; ok && e.store != nil {
		_ = e.store.Sync()
	}
	m.mu.Unlock()

	events, err := m.Replay(sourceID)
	if err != nil {
		return Info{}, fmt.Errorf("fork: replaying: %w", err)
	}
	if keepEvents < 0 {
		keepEvents = len(events)
	}
	if keepEvents > len(events) {
		return Info{}, fmt.Errorf("fork: keepEvents %d exceeds log length %d", keepEvents, len(events))
	}
	events = events[:keepEvents]

	baseTitle := strings.TrimSpace(src.Title)
	if baseTitle == "" {
		baseTitle = TitleFromEvents(events)
	}
	if baseTitle == "" {
		// Fall back to full-log title when the kept prefix has no user text yet.
		if all, err := m.Replay(sourceID); err == nil {
			baseTitle = TitleFromEvents(all)
		}
	}
	if baseTitle == "" {
		baseTitle = sourceID
	}
	forkTitle := forkTitleOf(baseTitle)

	info, err := m.Create(CreateOptions{Title: forkTitle, ProjectKey: src.ProjectKey})
	if err != nil {
		return Info{}, fmt.Errorf("fork: creating: %w", err)
	}
	if _, err := UpdateMeta(m.dir, info.ID, func(meta *Meta) {
		meta.Title = forkTitle
		meta.ForkedFrom = sourceID
		meta.ProjectKey = src.ProjectKey
	}); err != nil {
		_ = m.Close(info.ID)
		_ = os.Remove(LogPath(m.dir, info.ID))
		_ = os.Remove(MetaPath(m.dir, info.ID))
		return Info{}, fmt.Errorf("fork: meta: %w", err)
	}
	// Refresh in-memory title (Create already set it; ForkedFrom is sidecar-only).
	m.mu.Lock()
	if e, ok := m.sessions[info.ID]; ok {
		e.info.Title = forkTitle
	}
	m.mu.Unlock()

	for _, ev := range events {
		if err := m.Append(info.ID, ev); err != nil {
			_ = m.Close(info.ID)
			return Info{}, fmt.Errorf("fork: copying events: %w", err)
		}
	}
	return m.Get(info.ID)
}

// forkTitleOf builds a display title for a forked session.
func forkTitleOf(base string) string {
	base = strings.TrimSpace(base)
	if base == "" {
		return "fork"
	}
	const prefix = "fork of "
	if strings.HasPrefix(strings.ToLower(base), prefix) {
		return base
	}
	return prefix + base
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
	case protocol.WaitStarted:
		return e.SessionID, true
	case protocol.WaitResolved:
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
	case protocol.PermissionModeSelected:
		return e.SessionID, true
	case protocol.PhaseChanged:
		return e.SessionID, true
	case protocol.FastSelected:
		return e.SessionID, true
	case protocol.FilesInvalidated:
		return e.SessionID, true
	case protocol.UsageReported:
		return e.SessionID, true
	case protocol.ProviderRetrying:
		return e.SessionID, true
	case protocol.CompactionStarted:
		return e.SessionID, true
	case protocol.CompactionCompleted:
		return e.SessionID, true
	case protocol.SessionMeta:
		return e.SessionID, true
	case protocol.SessionRewound:
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
		LeadSessionID:   meta.LeadSessionID,
		Title:           title,
		ProjectKey:      meta.ProjectKey,
		WorktreePath:    meta.WorktreePath,
		WorktreeBranch:  meta.WorktreeBranch,
		Path:            LogPath(m.dir, id),
		CreatedAt:       created,
		UpdatedAt:       st.ModTime().UTC(),
		Open:            false,
		PRURL:           meta.PRURL,
		PRNumber:        meta.PRNumber,
		PRState:         NormalizePRState(meta.PRState),
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
