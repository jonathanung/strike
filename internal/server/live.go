package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// AgentInfo is a selectable agent exposed to the web UI.
type AgentInfo struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// StatusSnapshot is the live cockpit chrome state.
type StatusSnapshot struct {
	SessionID      string `json:"sessionId"`
	Provider       string `json:"provider,omitempty"`
	Model          string `json:"model,omitempty"`
	Agent          string `json:"agent,omitempty"`
	Effort         string `json:"effort,omitempty"`
	Autonomy       string `json:"autonomy,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	Phase          string `json:"phase,omitempty"`
	Workflow       string `json:"workflow,omitempty"`
	CWD            string `json:"cwd,omitempty"`
	Busy           bool   `json:"busy"`
	ContextUsed    int    `json:"contextUsed,omitempty"`
	ContextLimit   int    `json:"contextLimit,omitempty"`
}

// Live bridges a running engine to HTTP/WebSocket clients.
type Live struct {
	sessionID string
	cwd       string
	agents    []AgentInfo

	ops chan<- protocol.Op

	mu      sync.RWMutex
	status  StatusSnapshot
	subs    map[chan protocol.Event]struct{}
	closed  bool
	closeCh chan struct{}
}

// NewLive creates a live session bridge. ops must be the engine Ops channel.
func NewLive(sessionID, cwd string, agents []AgentInfo, ops chan<- protocol.Op) *Live {
	l := &Live{
		sessionID: sessionID,
		cwd:       cwd,
		agents:    append([]AgentInfo(nil), agents...),
		ops:       ops,
		subs:      make(map[chan protocol.Event]struct{}),
		closeCh:   make(chan struct{}),
		status: StatusSnapshot{
			SessionID: sessionID,
			CWD:       cwd,
		},
	}
	return l
}

// SessionID returns the live session id.
func (l *Live) SessionID() string { return l.sessionID }

// Agents returns selectable agents.
func (l *Live) Agents() []AgentInfo {
	out := make([]AgentInfo, len(l.agents))
	copy(out, l.agents)
	return out
}

// Status returns a copy of the current status snapshot.
func (l *Live) Status() StatusSnapshot {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.status
}

// Submit sends an op to the engine. Returns err if the live session is closed
// or the op channel blocks beyond a short timeout.
func (l *Live) Submit(ctx context.Context, op protocol.Op) error {
	if l == nil || l.ops == nil {
		return errors.New("live session unavailable")
	}
	l.mu.RLock()
	closed := l.closed
	l.mu.RUnlock()
	if closed {
		return errors.New("live session closed")
	}
	select {
	case l.ops <- op:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-l.closeCh:
		return errors.New("live session closed")
	case <-time.After(5 * time.Second):
		return errors.New("engine ops queue full")
	}
}

// Publish fans an engine event out to subscribers and updates status. A
// subscriber that cannot keep up is disconnected rather than blocking the
// engine event drain or other subscribers.
func (l *Live) Publish(ev protocol.Event) {
	if l == nil {
		return
	}
	l.applyStatus(ev)
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	for ch := range l.subs {
		select {
		case ch <- ev:
		default:
			delete(l.subs, ch)
			close(ch)
		}
	}
	l.mu.Unlock()
}

// Subscribe receives live events until ctx is done or Live is closed.
// The returned channel is closed when unsubscribed.
func (l *Live) Subscribe(ctx context.Context) <-chan protocol.Event {
	ch := make(chan protocol.Event, 256)
	if l == nil {
		close(ch)
		return ch
	}
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		close(ch)
		return ch
	}
	l.subs[ch] = struct{}{}
	l.mu.Unlock()

	go func() {
		select {
		case <-ctx.Done():
		case <-l.closeCh:
		}
		l.mu.Lock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
		l.mu.Unlock()
	}()
	return ch
}

// Close marks the live session finished and unblocks subscribers.
func (l *Live) Close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	close(l.closeCh)
	for ch := range l.subs {
		delete(l.subs, ch)
		close(ch)
	}
}

func (l *Live) applyStatus(ev protocol.Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	switch e := ev.(type) {
	case protocol.ModelSelected:
		l.status.Provider = e.Provider
		l.status.Model = e.Model
	case protocol.AgentSelected:
		l.status.Agent = e.Name
	case protocol.EffortSelected:
		l.status.Effort = string(e.Level)
	case protocol.AutonomySelected:
		l.status.Autonomy = string(e.Mode)
	case protocol.PermissionModeSelected:
		l.status.PermissionMode = string(e.Mode)
	case protocol.PhaseChanged:
		l.status.Phase = e.Phase
		l.status.Workflow = e.Workflow
	case protocol.TurnStarted:
		l.status.Busy = true
	case protocol.TurnCompleted:
		l.status.Busy = false
	case protocol.UsageReported:
		if e.Used.Known {
			l.status.ContextUsed = e.Used.N
		}
	}
}
