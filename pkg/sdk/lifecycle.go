package sdk

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jonathanung/strike-cli/pkg/protocol"
)

// DefaultSessionsDir returns ~/.strike/sessions (or $STRIKE_HOME/sessions).
func DefaultSessionsDir() string {
	if home := strings.TrimSpace(os.Getenv("STRIKE_HOME")); home != "" {
		return filepath.Join(home, "sessions")
	}
	h, err := os.UserHomeDir()
	if err != nil || h == "" {
		return "sessions"
	}
	return filepath.Join(h, ".strike", "sessions")
}

// SessionStore is an offline/host-side session lifecycle client over a
// sessions directory (same layout as the stock CLI). It implements the public
// list/get/fork/fork_at/rewind_points/replay contract without the engine.
//
// Load is not supported offline (no live binding); callers use strike rpc
// session.load or restart with --session.
type SessionStore struct {
	Dir string
}

// NewSessionStore roots a store at dir (created on first write/fork).
func NewSessionStore(dir string) *SessionStore {
	if strings.TrimSpace(dir) == "" {
		dir = DefaultSessionsDir()
	}
	return &SessionStore{Dir: dir}
}

// Capabilities returns the offline lifecycle surface.
func (s *SessionStore) Capabilities() protocol.LifecycleCapabilities {
	return protocol.LifecycleCapabilities{
		List:         true,
		Get:          true,
		Fork:         true,
		ForkAt:       true,
		Load:         false,
		RewindPoints: true,
		Replay:       true,
		EngineRewind: false,
	}
}

// List returns durable sessions newest-UpdatedAt first.
func (s *SessionStore) List(rootsOnly bool) ([]protocol.SessionSummary, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []protocol.SessionSummary
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		sum, err := s.Get(id)
		if err != nil {
			// Skip corrupt rows in list; Get surfaces corruption for inspect.
			continue
		}
		if rootsOnly && sum.ParentID != "" {
			continue
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].UpdatedAt.After(out[j].UpdatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// Get inspects one session by id.
func (s *SessionStore) Get(id string) (protocol.SessionSummary, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeInvalidSession, "id is empty", id)
	}
	path := s.logPath(id)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, fmt.Sprintf("session %q not found", id), id)
		}
		return protocol.SessionSummary{}, err
	}
	meta, _ := readSessionMeta(s.metaPath(id))
	evs, err := ReadSession(path)
	if err != nil {
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeSessionCorrupt, err.Error(), id)
	}
	title := meta.Title
	if title == "" {
		title = titleFromEvents(evs)
	}
	return protocol.SessionSummary{
		ID:         id,
		ParentID:   meta.ParentSessionID,
		Title:      title,
		ForkedFrom: meta.ForkedFrom,
		ProjectKey: meta.ProjectKey,
		UpdatedAt:  st.ModTime().UTC(),
		EventCount: len(evs),
	}, nil
}

// Fork copies the full event log into a new root session.
func (s *SessionStore) Fork(id string) (protocol.SessionSummary, error) {
	return s.ForkAt(id, -1)
}

// ForkAt copies the first keepEvents of id's log into a new root session.
// keepEvents < 0 means the full log.
func (s *SessionStore) ForkAt(id string, keepEvents int) (protocol.SessionSummary, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeInvalidSession, "id is empty", id)
	}
	srcPath := s.logPath(id)
	evs, err := ReadSession(srcPath)
	if err != nil {
		if os.IsNotExist(err) {
			return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, fmt.Sprintf("session %q not found", id), id)
		}
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeSessionCorrupt, err.Error(), id)
	}
	if keepEvents > len(evs) {
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeInvalidSession, fmt.Sprintf("keepEvents %d exceeds log length %d", keepEvents, len(evs)), id)
	}
	if keepEvents >= 0 {
		evs = evs[:keepEvents]
	}
	srcMeta, _ := readSessionMeta(s.metaPath(id))
	if srcMeta.ParentSessionID != "" {
		return protocol.SessionSummary{}, protocol.NewLifecycleError(protocol.ErrorCodeInvalidSession, fmt.Sprintf("session %q is a subagent transcript; fork a root session", id), id)
	}

	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return protocol.SessionSummary{}, err
	}
	newID := newSessionID()
	dstPath := s.logPath(newID)
	if err := WriteSession(dstPath, evs); err != nil {
		return protocol.SessionSummary{}, err
	}
	title := srcMeta.Title
	if title == "" {
		title = titleFromEvents(evs)
	}
	if title != "" {
		title = "fork of " + title
	} else {
		title = "fork of " + shortID(id)
	}
	meta := sessionMetaFile{
		ProjectKey: srcMeta.ProjectKey,
		Title:      title,
		ForkedFrom: id,
		CreatedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeSessionMeta(s.metaPath(newID), meta); err != nil {
		_ = os.Remove(dstPath)
		return protocol.SessionSummary{}, err
	}
	st, _ := os.Stat(dstPath)
	updated := time.Now().UTC()
	if st != nil {
		updated = st.ModTime().UTC()
	}
	return protocol.SessionSummary{
		ID:         newID,
		Title:      title,
		ForkedFrom: id,
		ProjectKey: meta.ProjectKey,
		UpdatedAt:  updated,
		EventCount: len(evs),
	}, nil
}

// RewindPoints lists fork-at-turn candidates for id.
func (s *SessionStore) RewindPoints(id string) ([]protocol.RewindPoint, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, protocol.NewLifecycleError(protocol.ErrorCodeInvalidSession, "id is empty", id)
	}
	evs, err := ReadSession(s.logPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, fmt.Sprintf("session %q not found", id), id)
		}
		return nil, protocol.NewLifecycleError(protocol.ErrorCodeSessionCorrupt, err.Error(), id)
	}
	return protocol.RewindPoints(evs), nil
}

// ReplayJSONL returns the raw JSONL bytes for id.
func (s *SessionStore) ReplayJSONL(id string) ([]byte, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, protocol.NewLifecycleError(protocol.ErrorCodeInvalidSession, "id is empty", id)
	}
	raw, err := os.ReadFile(s.logPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, protocol.NewLifecycleError(protocol.ErrorCodeSessionNotFound, fmt.Sprintf("session %q not found", id), id)
		}
		return nil, err
	}
	return raw, nil
}

// Load is unsupported offline.
func (s *SessionStore) Load(id string) (protocol.SessionLoadResult, error) {
	return protocol.SessionLoadResult{}, protocol.NewLifecycleError(
		protocol.ErrorCodeUnsupported,
		"offline SessionStore cannot bind a live session; use strike rpc session.load or --session",
		strings.TrimSpace(id),
	)
}

func (s *SessionStore) logPath(id string) string {
	return filepath.Join(s.Dir, id+".jsonl")
}

func (s *SessionStore) metaPath(id string) string {
	return filepath.Join(s.Dir, id+".meta.json")
}

// sessionMetaFile mirrors the durable sidecar fields the offline store needs.
type sessionMetaFile struct {
	ProjectKey      string `json:"projectKey,omitempty"`
	Title           string `json:"title,omitempty"`
	ParentSessionID string `json:"parentSessionId,omitempty"`
	ForkedFrom      string `json:"forkedFrom,omitempty"`
	CreatedAt       string `json:"createdAt,omitempty"`
}

func readSessionMeta(path string) (sessionMetaFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return sessionMetaFile{}, err
	}
	var m sessionMetaFile
	if err := json.Unmarshal(raw, &m); err != nil {
		return sessionMetaFile{}, err
	}
	return m, nil
}

func writeSessionMeta(path string, m sessionMetaFile) error {
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return os.WriteFile(path, raw, 0o644)
}

func titleFromEvents(evs []protocol.Event) string {
	for _, ev := range evs {
		if t, ok := ev.(protocol.SessionTitled); ok && strings.TrimSpace(t.Title) != "" {
			return t.Title
		}
	}
	for _, ev := range evs {
		if u, ok := ev.(protocol.UserMessage); ok && strings.TrimSpace(u.Text) != "" {
			text := strings.Join(strings.Fields(u.Text), " ")
			if len([]rune(text)) > 32 {
				return string([]rune(text)[:32])
			}
			return text
		}
	}
	return ""
}

func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

func newSessionID() string {
	// Match stock CLI shape loosely: timestamp + random suffix.
	return time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + randomSuffix(26)
}

func randomSuffix(n int) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	b := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range b {
		v, err := rand.Int(rand.Reader, max)
		if err != nil {
			b[i] = alphabet[time.Now().UnixNano()%int64(len(alphabet))]
			continue
		}
		b[i] = alphabet[v.Int64()]
	}
	return string(b)
}

// Client lifecycle convenience methods (engine ops + documentation).

// Rewind is a convenience for protocol.Rewind on the live session.
func (c *Client) Rewind(ctx context.Context, restoreFiles bool) error {
	return c.Send(ctx, protocol.Rewind{RestoreFiles: restoreFiles})
}

// Ensure SessionStore methods stay aligned with the public contract.
var (
	_ = (*SessionStore).Capabilities
	_ = (*SessionStore).List
	_ = (*SessionStore).Get
	_ = (*SessionStore).Fork
	_ = (*SessionStore).ForkAt
	_ = (*SessionStore).RewindPoints
	_ = (*SessionStore).ReplayJSONL
	_ = (*SessionStore).Load
)

// CallLifecycle is a helper for JSON-RPC lifecycle clients: it marshals params
// and unmarshals the result. Transport is provided by the caller (e.g. a
// function that writes a request and reads the matching response).
type LifecycleCaller func(ctx context.Context, method string, params, result any) error

// LifecycleClient wraps a JSON-RPC transport for session.* methods.
type LifecycleClient struct {
	Call LifecycleCaller
}

// Capabilities fetches session.capabilities.
func (c LifecycleClient) Capabilities(ctx context.Context) (protocol.LifecycleCapabilities, error) {
	var out protocol.LifecycleCapabilities
	if c.Call == nil {
		return out, errors.New("sdk: nil lifecycle caller")
	}
	err := c.Call(ctx, protocol.LifecycleMethodCapabilities, nil, &out)
	return out, err
}

// List fetches session.list.
func (c LifecycleClient) List(ctx context.Context, rootsOnly bool) ([]protocol.SessionSummary, error) {
	if c.Call == nil {
		return nil, errors.New("sdk: nil lifecycle caller")
	}
	var out protocol.SessionListResult
	err := c.Call(ctx, protocol.LifecycleMethodList, protocol.SessionListParams{RootsOnly: rootsOnly}, &out)
	return out.Sessions, err
}

// Get fetches session.get.
func (c LifecycleClient) Get(ctx context.Context, id string) (protocol.SessionSummary, error) {
	if c.Call == nil {
		return protocol.SessionSummary{}, errors.New("sdk: nil lifecycle caller")
	}
	var out protocol.SessionSummary
	err := c.Call(ctx, protocol.LifecycleMethodGet, protocol.SessionIDParams{ID: id}, &out)
	return out, err
}

// Fork fetches session.fork.
func (c LifecycleClient) Fork(ctx context.Context, id string) (protocol.SessionSummary, error) {
	if c.Call == nil {
		return protocol.SessionSummary{}, errors.New("sdk: nil lifecycle caller")
	}
	var out protocol.SessionSummary
	err := c.Call(ctx, protocol.LifecycleMethodFork, protocol.SessionIDParams{ID: id}, &out)
	return out, err
}

// ForkAt fetches session.fork_at.
func (c LifecycleClient) ForkAt(ctx context.Context, id string, keepEvents int) (protocol.SessionSummary, error) {
	if c.Call == nil {
		return protocol.SessionSummary{}, errors.New("sdk: nil lifecycle caller")
	}
	var out protocol.SessionSummary
	err := c.Call(ctx, protocol.LifecycleMethodForkAt, protocol.SessionForkAtParams{ID: id, KeepEvents: keepEvents}, &out)
	return out, err
}

// Load fetches session.load.
func (c LifecycleClient) Load(ctx context.Context, id string) (protocol.SessionLoadResult, error) {
	if c.Call == nil {
		return protocol.SessionLoadResult{}, errors.New("sdk: nil lifecycle caller")
	}
	var out protocol.SessionLoadResult
	err := c.Call(ctx, protocol.LifecycleMethodLoad, protocol.SessionIDParams{ID: id}, &out)
	return out, err
}

// RewindPoints fetches session.rewind_points.
func (c LifecycleClient) RewindPoints(ctx context.Context, id string) ([]protocol.RewindPoint, error) {
	if c.Call == nil {
		return nil, errors.New("sdk: nil lifecycle caller")
	}
	var out protocol.SessionRewindPointsResult
	err := c.Call(ctx, protocol.LifecycleMethodRewindPoints, protocol.SessionIDParams{ID: id}, &out)
	return out.Points, err
}
