package engine

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

// maxTeamMemberNameLen bounds optional teammate aliases.
const maxTeamMemberNameLen = 64

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
	Name            string // optional stable alias unique within the team
	Persona         string // agent persona name
	State           protocol.TeamMemberState
	ParentSessionID string    // empty on the lead
	Depth           int       // 0 = lead
	StartedAt       time.Time // enrollment / lead creation time when known
	Summary         string    // short terminal summary when done
}

// ValidateMemberName normalizes and checks an optional teammate alias.
// Empty input is valid (no alias). Non-empty names must be ≤64 runes, contain
// no whitespace, and use only letters, digits, '_' and '-'.
func ValidateMemberName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if utf8.RuneCountInString(name) > maxTeamMemberNameLen {
		return "", fmt.Errorf("name exceeds %d characters", maxTeamMemberNameLen)
	}
	for _, r := range name {
		if unicode.IsSpace(r) {
			return "", fmt.Errorf("name must not contain whitespace")
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' {
			continue
		}
		return "", fmt.Errorf("name may only contain letters, digits, '_' and '-'")
	}
	return name, nil
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

// IsLive reports whether sessionID has an attached running engine mailbox.
func (t *Team) IsLive(sessionID string) bool {
	if t == nil {
		return false
	}
	id := strings.TrimSpace(sessionID)
	if id == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	target := t.live[id]
	return target != nil && target.eng != nil && target.box != nil
}

// ResolveAddress maps a teammate address to a session id.
// Prefer exact session id; otherwise unique stable name match.
// Empty, unknown, or ambiguous name → ok false with a detail reason.
func (t *Team) ResolveAddress(addr string) (sessionID string, ok bool) {
	id, _, ok := t.ResolveAddressDetail(addr)
	return id, ok
}

// ResolveAddressDetail is ResolveAddress plus a stable rejection detail when !ok.
func (t *Team) ResolveAddressDetail(addr string) (sessionID, detail string, ok bool) {
	if t == nil {
		return "", "no team", false
	}
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", "address is required", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, found := t.members[addr]; found {
		return addr, "", true
	}
	var match string
	for id, m := range t.members {
		if strings.TrimSpace(m.Name) == addr {
			if match != "" {
				return "", "teammate name is ambiguous", false
			}
			match = id
		}
	}
	if match == "" {
		return "", "recipient is not on this team", false
	}
	return match, "", true
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

// Resolve maps a session id or stable name alias to a session id.
// Session id match wins; names must be unique (ambiguous → false).
func (t *Team) Resolve(ref string) (sessionID string, ok bool) {
	return t.ResolveAddress(ref)
}

// NameOwner returns the session id that currently holds name, if any.
func (t *Team) NameOwner(name string) (sessionID string, ok bool) {
	if t == nil {
		return "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for id, m := range t.members {
		if m.Name == name {
			return id, true
		}
	}
	return "", false
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
// must be the lead or an enrolled member), and name collisions with another
// member. Already-enrolled members update persona/name/summary when provided
// (name still unique across the team).
func (t *Team) Enroll(m TeamMember) bool {
	if t == nil {
		return false
	}
	id := strings.TrimSpace(m.SessionID)
	parent := strings.TrimSpace(m.ParentSessionID)
	if id == "" || parent == "" {
		return false
	}
	name, err := ValidateMemberName(m.Name)
	if err != nil {
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
	if name != "" {
		for otherID, other := range t.members {
			if other.Name == name && otherID != id {
				return false
			}
		}
	}
	if existing, ok := t.members[id]; ok {
		if p := strings.TrimSpace(m.Persona); p != "" {
			existing.Persona = p
		}
		if name != "" {
			existing.Name = name
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
		Name:            name,
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
