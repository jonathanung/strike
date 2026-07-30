package engine

import (
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// Team is the implicit session-scoped agent team for one lead.
//
// Team identity is the lead session id. No TeamCreate is required: spawning
// task children auto-enrolls them. The lead is always a member.
//
// Nested policy: the roster is flat under the lead. Direct children and nested
// descendants (when MaxChildDepth > 1) enroll when their ParentSessionID is the
// lead or an already-enrolled member. Intermediate engines share this Team
// pointer and do not form separate peer rosters. Children of unrelated roots
// never appear here.
//
// Terminal members stay listable until Dissolve (lead session end / GC).
//
// Live mailbox targets (see AttachMailbox) enable peer delivery while an
// engine's Run is active; completed members reject new mail.
type Team struct {
	mu      sync.Mutex
	leadID  string
	members map[string]TeamMember     // session id → member
	live    map[string]*mailboxTarget // session id → live engine mailbox
}

// TeamMember is one roster entry (lead or child).
type TeamMember struct {
	SessionID       string
	Name            string // optional stable alias (filled by later naming work)
	Persona         string // agent persona name
	State           protocol.TeamMemberState
	ParentSessionID string    // empty on the lead
	Depth           int       // 0 = lead
	StartedAt       time.Time // enrollment / lead creation time when known
	Summary         string    // short terminal summary when done
}

// NewTeam creates a team whose identity is leadID. The lead is enrolled as a
// running member. persona is the lead's agent name (may be empty at New).
func NewTeam(leadID, persona string) *Team {
	leadID = strings.TrimSpace(leadID)
	if leadID == "" {
		return nil
	}
	t := &Team{
		leadID:  leadID,
		members: make(map[string]TeamMember, 4),
	}
	t.members[leadID] = TeamMember{
		SessionID: leadID,
		Persona:   strings.TrimSpace(persona),
		State:     protocol.TeamMemberRunning,
		Depth:     0,
		StartedAt: time.Now(),
	}
	return t
}

// LeadID is the team identity (lead session id).
func (t *Team) LeadID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.leadID
}

// Contains reports whether sessionID is on this team's roster.
func (t *Team) Contains(sessionID string) bool {
	if t == nil {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	_, ok := t.members[id]
	return ok
}

// Member returns a copy of the roster entry for sessionID.
func (t *Team) Member(sessionID string) (TeamMember, bool) {
	if t == nil {
		return TeamMember{}, false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return TeamMember{}, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.members[id]
	return m, ok
}

// Roster returns a stable snapshot: lead first, then other members sorted by
// session id.
func (t *Team) Roster() []TeamMember {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.members) == 0 {
		return nil
	}
	out := make([]TeamMember, 0, len(t.members))
	if lead, ok := t.members[t.leadID]; ok {
		out = append(out, lead)
	}
	rest := make([]TeamMember, 0, len(t.members))
	for id, m := range t.members {
		if id == t.leadID {
			continue
		}
		rest = append(rest, m)
	}
	sort.Slice(rest, func(i, j int) bool {
		return rest[i].SessionID < rest[j].SessionID
	})
	return append(out, rest...)
}

// Enroll adds a child (or nested descendant) to the roster.
//
// Rejects empty ids, the lead id (already present), unknown parents (parent
// must be the lead or an enrolled member), and no-ops if already enrolled
// (updates persona/name when provided).
func (t *Team) Enroll(m TeamMember) bool {
	if t == nil {
		return false
	}
	id := strings.TrimSpace(m.SessionID)
	parent := strings.TrimSpace(m.ParentSessionID)
	if id == "" || parent == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == t.leadID {
		return false
	}
	if _, ok := t.members[parent]; !ok {
		return false
	}
	if existing, ok := t.members[id]; ok {
		if p := strings.TrimSpace(m.Persona); p != "" {
			existing.Persona = p
		}
		if n := strings.TrimSpace(m.Name); n != "" {
			existing.Name = n
		}
		if s := strings.TrimSpace(m.Summary); s != "" {
			existing.Summary = s
		}
		if !m.StartedAt.IsZero() && existing.StartedAt.IsZero() {
			existing.StartedAt = m.StartedAt
		}
		t.members[id] = existing
		return true
	}
	state := m.State
	if state == "" {
		state = protocol.TeamMemberRunning
	}
	started := m.StartedAt
	if started.IsZero() {
		started = time.Now()
	}
	t.members[id] = TeamMember{
		SessionID:       id,
		Name:            strings.TrimSpace(m.Name),
		Persona:         strings.TrimSpace(m.Persona),
		State:           state,
		ParentSessionID: parent,
		Depth:           m.Depth,
		StartedAt:       started,
		Summary:         strings.TrimSpace(m.Summary),
	}
	return true
}

// SetState updates a member's lifecycle state. Unknown members are ignored.
func (t *Team) SetState(sessionID string, state protocol.TeamMemberState) bool {
	if t == nil || state == "" {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.members[id]
	if !ok {
		return false
	}
	m.State = state
	t.members[id] = m
	return true
}

// SetTerminal marks a member terminal with optional summary.
func (t *Team) SetTerminal(sessionID string, state protocol.TeamMemberState, summary string) bool {
	if t == nil || state == "" {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.members[id]
	if !ok {
		return false
	}
	m.State = state
	if s := strings.TrimSpace(summary); s != "" {
		m.Summary = s
	}
	t.members[id] = m
	return true
}

// SetPersona updates the lead or a member's persona label.
func (t *Team) SetPersona(sessionID, persona string) {
	if t == nil {
		return
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.members[id]
	if !ok {
		return
	}
	m.Persona = strings.TrimSpace(persona)
	t.members[id] = m
}

// Dissolve clears the roster (team ends with the lead session). After Dissolve,
// Contains is false for everyone and Roster is empty. The Team value should not
// be reused; callers may replace the pointer.
func (t *Team) Dissolve() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.members = make(map[string]TeamMember)
	t.live = make(map[string]*mailboxTarget)
}

// Len returns the number of roster entries (including the lead while active).
func (t *Team) Len() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.members)
}
