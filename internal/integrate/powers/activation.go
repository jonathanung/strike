package powers

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Connector is the MCP connection seam. Activation calls it; it does not
// reimplement MCP. Connect/Disconnect are scoped to one session.
type Connector interface {
	Connect(session, server string) error
	Disconnect(session, server string)
}

// DenyFunc reports whether a server is deny-listed by the session's
// permissions, with a reason. Activation never widens permissions.
type DenyFunc func(session, server string) (denied bool, reason string)

// ErrDenied is returned (wrapped) when a pack references a deny-listed server.
var ErrDenied = errors.New("powers: pack references a denied server")

// Result is the outcome of one activation. Admitted servers are connected
// and supplied to the session; Failed servers are not. The chip and the
// tool set both read from the same Result, so they always agree.
type Result struct {
	Name     string
	Admitted []string
	Failed   map[string]string // server -> error text
	Skills   []string
}

// Manager tracks per-session activations with reference counts, so packs
// that share a server keep it connected until the last one deactivates.
// Sessions are independent: ending one never touches another's servers.
//
// Connector.Connect runs outside the manager lock, so a slow MCP dial in one
// session never stalls another session (or a read of this one). Concurrent
// activations that need the same not-yet-connected server share one dial.
type Manager struct {
	mu       sync.Mutex
	conn     Connector
	deny     DenyFunc
	now      func() time.Time
	sessions map[string]*sessionState
	lastUsed map[string]time.Time // pack name -> last successful activation
}

type sessionState struct {
	active  map[string]Result // pack name -> activation result
	pending map[string]bool   // pack name -> activation in flight
	refs    map[string]int    // server -> admitted references
	skills  map[string]int    // skill -> references
	dials   map[string]*dial  // server -> connect in flight
	ended   bool              // EndSession ran while activations were in flight
}

// dial is one in-flight Connect shared by every activation that needs the
// server. The dialer adds a reference for itself and each waiter when the
// dial succeeds, under the lock, before closing done; so a waiter's server
// can never be released between the dial finishing and the waiter resuming.
type dial struct {
	done    chan struct{}
	err     error
	waiters int
}

// NewManager builds a Manager. deny may be nil (nothing denied).
func NewManager(conn Connector, deny DenyFunc) *Manager {
	return &Manager{
		conn:     conn,
		deny:     deny,
		now:      time.Now,
		sessions: map[string]*sessionState{},
		lastUsed: map[string]time.Time{},
	}
}

// Activate connects p's servers and loads its skills for session. A server
// that fails to connect is reported in Result.Failed and left out of the
// session; the rest stay admitted. A deny-listed server keeps the whole
// pack inactive (nothing is connected) and returns ErrDenied with the reason.
func (m *Manager) Activate(session string, p Power) (Result, error) {
	if session == "" || p.Name == "" {
		return Result{}, errors.New("powers: session and pack name are required")
	}
	if m.deny != nil {
		for _, srv := range p.Servers {
			if denied, reason := m.deny(session, srv); denied {
				return Result{}, fmt.Errorf("%w: %s: %s", ErrDenied, srv, reason)
			}
		}
	}

	// Phase 1 (locked): reserve the pack, take references on servers that
	// are already up, and claim or join dials for the rest.
	m.mu.Lock()
	st := m.sessions[session]
	if st == nil {
		st = &sessionState{
			active:  map[string]Result{},
			pending: map[string]bool{},
			refs:    map[string]int{},
			skills:  map[string]int{},
			dials:   map[string]*dial{},
		}
		m.sessions[session] = st
	}
	if st.ended {
		m.mu.Unlock()
		return Result{}, errors.New("powers: session has ended")
	}
	if _, ok := st.active[p.Name]; ok || st.pending[p.Name] {
		m.mu.Unlock()
		return Result{}, fmt.Errorf("powers: %q is already active in this session", p.Name)
	}
	st.pending[p.Name] = true
	admitted := map[string]bool{} // reference already held
	mine := map[string]*dial{}    // this activation dials
	joined := map[string]*dial{}  // another activation dials
	for _, srv := range p.Servers {
		if admitted[srv] || mine[srv] != nil || joined[srv] != nil {
			continue // duplicate server name in one pack
		}
		switch {
		case st.refs[srv] > 0:
			st.refs[srv]++
			admitted[srv] = true
		case st.dials[srv] != nil:
			d := st.dials[srv]
			d.waiters++
			joined[srv] = d
		default:
			d := &dial{done: make(chan struct{})}
			st.dials[srv] = d
			mine[srv] = d
		}
	}
	m.mu.Unlock()

	// Phase 2 (unlocked): dial our servers, then wait for shared dials.
	for srv, d := range mine {
		d.err = m.conn.Connect(session, srv)
		m.mu.Lock()
		if d.err == nil {
			st.refs[srv] += 1 + d.waiters
			admitted[srv] = true
		}
		delete(st.dials, srv)
		close(d.done)
		m.mu.Unlock()
	}
	for srv, d := range joined {
		<-d.done
		if d.err == nil {
			admitted[srv] = true
		}
	}

	// Phase 3 (locked): record the result, or give everything back if the
	// session ended while we were dialing.
	res := Result{Name: p.Name, Failed: map[string]string{}}
	seen := map[string]bool{}
	for _, srv := range p.Servers {
		if seen[srv] {
			continue
		}
		seen[srv] = true
		if admitted[srv] {
			res.Admitted = append(res.Admitted, srv)
			continue
		}
		if d := mine[srv]; d != nil {
			res.Failed[srv] = d.err.Error()
		} else if d := joined[srv]; d != nil {
			res.Failed[srv] = d.err.Error()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(st.pending, p.Name)
	if st.ended {
		m.releaseLocked(session, st, res)
		m.dropIfIdleLocked(session, st)
		return Result{}, errors.New("powers: session ended during activation")
	}
	for _, sk := range p.Skills {
		st.skills[sk]++
		res.Skills = append(res.Skills, sk)
	}
	st.active[p.Name] = res
	m.lastUsed[p.Name] = m.now()
	return cloneResult(res), nil
}

// Deactivate unloads one pack from session, disconnecting any server whose
// last reference it held.
func (m *Manager) Deactivate(session, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[session]
	if st == nil {
		return fmt.Errorf("powers: %q is not active in this session", name)
	}
	res, ok := st.active[name]
	if !ok {
		return fmt.Errorf("powers: %q is not active in this session", name)
	}
	m.releaseLocked(session, st, res)
	delete(st.active, name)
	m.dropIfIdleLocked(session, st)
	return nil
}

// EndSession deactivates every pack in session.
func (m *Manager) EndSession(session string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[session]
	if st == nil {
		return
	}
	for _, name := range sortedKeys(st.active) {
		m.releaseLocked(session, st, st.active[name])
	}
	st.active = map[string]Result{}
	st.ended = true
	// In-flight activations release their own references when they resume
	// and drop the session once the last one finishes.
	m.dropIfIdleLocked(session, st)
}

// LastUsed reports when name was last activated in any session.
func (m *Manager) LastUsed(name string) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.lastUsed[name]
	return t, ok
}

// dropIfIdleLocked forgets session once nothing is active or in flight.
func (m *Manager) dropIfIdleLocked(session string, st *sessionState) {
	if len(st.active) == 0 && len(st.pending) == 0 && len(st.dials) == 0 && m.sessions[session] == st {
		delete(m.sessions, session)
	}
}

// Active lists the session's active packs, sorted by name.
func (m *Manager) Active(session string) []Result {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[session]
	if st == nil {
		return nil
	}
	out := make([]Result, 0, len(st.active))
	for _, name := range sortedKeys(st.active) {
		out = append(out, cloneResult(st.active[name]))
	}
	return out
}

// Servers returns the servers currently supplied to session by Powers.
func (m *Manager) Servers(session string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[session]
	if st == nil {
		return nil
	}
	return sortedKeys(st.refs)
}

// Skills returns the skills currently loaded into session by Powers.
func (m *Manager) Skills(session string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[session]
	if st == nil {
		return nil
	}
	return sortedKeys(st.skills)
}

func (m *Manager) releaseLocked(session string, st *sessionState, res Result) {
	for _, srv := range res.Admitted {
		st.refs[srv]--
		if st.refs[srv] <= 0 {
			delete(st.refs, srv)
			m.conn.Disconnect(session, srv)
		}
	}
	for _, sk := range res.Skills {
		st.skills[sk]--
		if st.skills[sk] <= 0 {
			delete(st.skills, sk)
		}
	}
}

func cloneResult(r Result) Result {
	out := Result{
		Name:     r.Name,
		Admitted: append([]string(nil), r.Admitted...),
		Skills:   append([]string(nil), r.Skills...),
		Failed:   make(map[string]string, len(r.Failed)),
	}
	for k, v := range r.Failed {
		out.Failed[k] = v
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
