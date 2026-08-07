package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/rpc"
	"github.com/jonathanung/strike-cli/internal/session"
	pkgproto "github.com/jonathanung/strike-cli/pkg/protocol"
)

// rpcLifecycle adapts session.Manager / host.Sessions to rpc.Lifecycle.
type rpcLifecycle struct {
	sessions   *session.Manager
	hostSess   host.Sessions
	activeID   string
	projectKey string
}

func newRPCLifecycle(sessions *session.Manager, hostSess host.Sessions, activeID, projectKey string) rpc.Lifecycle {
	if sessions == nil && hostSess == nil {
		return nil
	}
	return &rpcLifecycle{
		sessions:   sessions,
		hostSess:   hostSess,
		activeID:   strings.TrimSpace(activeID),
		projectKey: strings.TrimSpace(projectKey),
	}
}

func (l *rpcLifecycle) Capabilities() pkgproto.LifecycleCapabilities {
	return pkgproto.LifecycleCapabilities{
		List:            true,
		Get:             true,
		Fork:            true,
		ForkAt:          true,
		Load:            true,
		RewindPoints:    true,
		Replay:          true,
		EngineRewind:    true,
		ActiveSessionID: l.activeID,
	}
}

func (l *rpcLifecycle) List(ctx context.Context, rootsOnly bool) ([]pkgproto.SessionSummary, error) {
	_ = ctx
	if l.hostSess != nil {
		items, err := l.hostSess.List(rootsOnly)
		if err != nil {
			return nil, err
		}
		out := make([]pkgproto.SessionSummary, 0, len(items))
		for _, s := range items {
			out = append(out, hostToSummary(s))
		}
		return out, nil
	}
	if l.sessions == nil {
		return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeUnsupported, "session list unavailable", "")
	}
	all, err := l.sessions.List()
	if err != nil {
		return nil, err
	}
	out := make([]pkgproto.SessionSummary, 0, len(all))
	for _, info := range all {
		if rootsOnly && info.ParentSessionID != "" {
			continue
		}
		if l.projectKey != "" && !session.BelongsToProject(info, l.projectKey) {
			continue
		}
		out = append(out, infoToSummary(info))
	}
	return out, nil
}

func (l *rpcLifecycle) Get(ctx context.Context, id string) (pkgproto.SessionSummary, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeInvalidSession, "id is empty", id)
	}
	if l.hostSess != nil {
		s, ok, err := l.hostSess.Get(id)
		if err != nil {
			return pkgproto.SessionSummary{}, err
		}
		if !ok {
			return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionNotFound, fmt.Sprintf("session %q not found", id), id)
		}
		sum := hostToSummary(s)
		if evs, err := l.replayEvents(id); err == nil {
			sum.EventCount = len(evs)
		}
		return sum, nil
	}
	if l.sessions == nil {
		return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeUnsupported, "session get unavailable", id)
	}
	info, err := l.sessions.Get(id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionNotFound, err.Error(), id)
		}
		return pkgproto.SessionSummary{}, err
	}
	sum := infoToSummary(info)
	if evs, err := l.sessions.Replay(id); err == nil {
		sum.EventCount = len(evs)
	} else if isCorrupt(err) {
		return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionCorrupt, err.Error(), id)
	}
	return sum, nil
}

func (l *rpcLifecycle) Fork(ctx context.Context, id string) (pkgproto.SessionSummary, error) {
	return l.ForkAt(ctx, id, -1)
}

func (l *rpcLifecycle) ForkAt(ctx context.Context, id string, keepEvents int) (pkgproto.SessionSummary, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeInvalidSession, "id is empty", id)
	}
	if l.hostSess != nil {
		var (
			s   host.Session
			err error
		)
		if keepEvents < 0 {
			s, err = l.hostSess.Fork(id)
		} else {
			s, err = l.hostSess.ForkAt(id, keepEvents)
		}
		if err != nil {
			return pkgproto.SessionSummary{}, mapSessionErr(id, err)
		}
		return hostToSummary(s), nil
	}
	if l.sessions == nil {
		return pkgproto.SessionSummary{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeUnsupported, "session fork unavailable", id)
	}
	info, err := l.sessions.ForkAt(id, keepEvents)
	if err != nil {
		return pkgproto.SessionSummary{}, mapSessionErr(id, err)
	}
	return infoToSummary(info), nil
}

func (l *rpcLifecycle) Load(ctx context.Context, id string) (pkgproto.SessionLoadResult, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return pkgproto.SessionLoadResult{}, pkgproto.NewLifecycleError(pkgproto.ErrorCodeInvalidSession, "id is empty", id)
	}
	// Validate the session exists and is a root before claiming load.
	sum, err := l.Get(ctx, id)
	if err != nil {
		return pkgproto.SessionLoadResult{}, err
	}
	if sum.ParentID != "" {
		return pkgproto.SessionLoadResult{}, pkgproto.NewLifecycleError(
			pkgproto.ErrorCodeInvalidSession,
			fmt.Sprintf("session %q is a subagent transcript; load a root session", id),
			id,
		)
	}
	// Single-process rpc binds one live engine at startup. Loading the active
	// id is a no-op success; a different id requires a new process (--session).
	if l.activeID != "" && id == l.activeID {
		return pkgproto.SessionLoadResult{ID: id, Active: true}, nil
	}
	return pkgproto.SessionLoadResult{}, pkgproto.NewLifecycleError(
		pkgproto.ErrorCodeSessionBusy,
		fmt.Sprintf("session %q is not the live rpc session %q; restart strike rpc --session %s", id, l.activeID, id),
		id,
	)
}

func (l *rpcLifecycle) RewindPoints(ctx context.Context, id string) ([]pkgproto.RewindPoint, error) {
	_ = ctx
	evs, err := l.replayEvents(id)
	if err != nil {
		return nil, err
	}
	return pkgproto.RewindPoints(evs), nil
}

func (l *rpcLifecycle) Replay(ctx context.Context, id string) ([]byte, error) {
	_ = ctx
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeInvalidSession, "id is empty", id)
	}
	if l.hostSess != nil {
		raw, err := l.hostSess.ReplayJSONL(id)
		if err != nil {
			return nil, mapSessionErr(id, err)
		}
		return raw, nil
	}
	if l.sessions == nil {
		return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeUnsupported, "session replay unavailable", id)
	}
	// Prefer raw file bytes so headers and unknown events round-trip.
	path := l.sessions.Path(id)
	raw, err := readFileBytes(path)
	if err != nil {
		// Fall back to re-encoding replayed events.
		evs, rerr := l.sessions.Replay(id)
		if rerr != nil {
			return nil, mapSessionErr(id, err)
		}
		return encodeEventsJSONL(evs)
	}
	return raw, nil
}

func (l *rpcLifecycle) replayEvents(id string) ([]protocol.Event, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeInvalidSession, "id is empty", id)
	}
	if l.sessions != nil {
		evs, err := l.sessions.Replay(id)
		if err != nil {
			return nil, mapSessionErr(id, err)
		}
		return evs, nil
	}
	if l.hostSess == nil {
		return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeUnsupported, "session replay unavailable", id)
	}
	raw, err := l.hostSess.ReplayJSONL(id)
	if err != nil {
		return nil, mapSessionErr(id, err)
	}
	return decodeJSONLEvents(raw, id)
}

func decodeJSONLEvents(raw []byte, id string) ([]protocol.Event, error) {
	var out []protocol.Event
	for i, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var env protocol.Envelope
		if err := json.Unmarshal([]byte(line), &env); err != nil {
			return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionCorrupt, fmt.Sprintf("line %d: %v", i+1, err), id)
		}
		// Skip session.header and non-event lines.
		if env.Type == "" || env.Type == "session.header" {
			continue
		}
		ev, err := env.Decode()
		if err != nil {
			return nil, pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionCorrupt, fmt.Sprintf("line %d: %v", i+1, err), id)
		}
		out = append(out, ev)
	}
	return out, nil
}

func encodeEventsJSONL(evs []protocol.Event) ([]byte, error) {
	var b strings.Builder
	for _, ev := range evs {
		env, err := protocol.Wrap(ev)
		if err != nil {
			continue
		}
		line, err := json.Marshal(env)
		if err != nil {
			return nil, err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return []byte(b.String()), nil
}

func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func hostToSummary(s host.Session) pkgproto.SessionSummary {
	return pkgproto.SessionSummary{
		ID:         s.ID,
		ParentID:   s.ParentID,
		Title:      s.Title,
		ProjectKey: s.ProjectKey,
		Open:       s.Open,
		UpdatedAt:  s.UpdatedAt,
	}
}

func infoToSummary(info session.Info) pkgproto.SessionSummary {
	return pkgproto.SessionSummary{
		ID:         info.ID,
		ParentID:   info.ParentSessionID,
		Title:      info.Title,
		ForkedFrom: info.ForkedFrom,
		ProjectKey: info.ProjectKey,
		Open:       info.Open,
		UpdatedAt:  info.UpdatedAt,
	}
}

func mapSessionErr(id string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found"):
		return pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionNotFound, msg, id)
	case strings.Contains(msg, "open") && (strings.Contains(msg, "force") || strings.Contains(msg, "busy") || strings.Contains(msg, "active")):
		return pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionBusy, msg, id)
	case isCorrupt(err):
		return pkgproto.NewLifecycleError(pkgproto.ErrorCodeSessionCorrupt, msg, id)
	case strings.Contains(msg, "subagent") || strings.Contains(msg, "parent"):
		return pkgproto.NewLifecycleError(pkgproto.ErrorCodeInvalidSession, msg, id)
	default:
		return err
	}
}

func isCorrupt(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "corrupt") ||
		strings.Contains(msg, "invalid envelope") ||
		strings.Contains(msg, "schema")
}
