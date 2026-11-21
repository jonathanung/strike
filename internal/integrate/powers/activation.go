package powers

import (
	"errors"
	"fmt"
	"sort"
	"sync"
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
type Manager struct {
	mu       sync.Mutex
	conn     Connector
	deny     DenyFunc
	sessions map[string]*sessionState
}

type sessionState struct {
	active map[string]Result // pack name -> activation result
	refs   map[string]int    // server -> admitted references
	skills map[string]int    // skill -> references
}

// NewManager builds a Manager. deny may be nil (nothing denied).
func NewManager(conn Connector, deny DenyFunc) *Manager {
	return &Manager{conn: conn, deny: deny, sessions: map[string]*sessionState{}}
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
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.sessions[session]
	if st == nil {
		st = &sessionState{active: map[string]Result{}, refs: map[string]int{}, skills: map[string]int{}}
		m.sessions[session] = st
	}
	if _, ok := st.active[p.Name]; ok {
		return Result{}, fmt.Errorf("powers: %q is already active in this session", p.Name)
	}
	res := Result{Name: p.Name, Failed: map[string]string{}}
	for _, srv := range p.Servers {
		if st.refs[srv] == 0 {
			if err := m.conn.Connect(session, srv); err != nil {
				res.Failed[srv] = err.Error()
				continue
			}
		}
		st.refs[srv]++
		res.Admitted = append(res.Admitted, srv)
	}
	for _, sk := range p.Skills {
		st.skills[sk]++
		res.Skills = append(res.Skills, sk)
	}
	st.active[p.Name] = res
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
	if len(st.active) == 0 {
		delete(m.sessions, session)
	}
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
	delete(m.sessions, session)
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
