// Package artifact stores shared typed, versioned multi-agent work products
// (findings, patches, test reports, contracts, lightweight plans) with
// compare-and-swap updates.
//
// Non-overlap vs sibling stores:
//   - internal/persist/memory — untyped key/value facts and preferences (no versions/types)
//   - internal/persist/issue — tracked open/closed work items for humans/agents
//   - internal/persist/plan — root-owned structured multi-section plans with lifecycle
//     (draft/approved/closed). Prefer plan_write/plan_read for that domain;
//     artifact type "plan" is a lightweight shared blob/ref, not a second plan DB.
//
// Artifacts are addressable by id+version, permissioned (owner vs team), and
// optionally session-scoped (still durable on disk so --continue / resume sees
// them). cmd/strike opens the store; tools artifact_write/artifact_read are the
// agent surface; protocol.ArtifactUpdated + CompletionHandoff.ArtifactRefs
// carry refs on the wire.
package artifact

import (
	"fmt"
	"strings"
	"time"
)

// Built-in artifact types. The store accepts these plus future registry entries
// via RegisterType (tests/extensions); unknown types are rejected on write.
const (
	TypePlan       = "plan"
	TypeContract   = "contract"
	TypeFindings   = "findings"
	TypePatch      = "patch"
	TypeTestReport = "test_report"
)

// Durability scopes.
const (
	ScopeProject = "project" // visible project-wide (subject to access)
	ScopeSession = "session" // tagged to a session; durable for resume/replay
)

// Access modes for read/write authorization.
const (
	// AccessOwner: only the owning session may read or write.
	AccessOwner = "owner"
	// AccessTeam: any session under the same root may read or write (default).
	AccessTeam = "team"
)

// Artifact is one versioned typed object in the shared store.
type Artifact struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Title        string     `json:"title,omitempty"`
	Content      string     `json:"content"`
	Version      int        `json:"version"` // CAS token; increments on every accepted update
	Scope        string     `json:"scope"`   // project | session
	SessionID    string     `json:"session_id,omitempty"`
	Access       string     `json:"access"` // owner | team
	OwnerSession string     `json:"owner_session"`
	OwnerRoot    string     `json:"owner_root"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// Meta is list/index metadata (content omitted).
type Meta struct {
	ID           string     `json:"id"`
	Type         string     `json:"type"`
	Title        string     `json:"title,omitempty"`
	Version      int        `json:"version"`
	Scope        string     `json:"scope"`
	SessionID    string     `json:"session_id,omitempty"`
	Access       string     `json:"access"`
	OwnerSession string     `json:"owner_session"`
	OwnerRoot    string     `json:"owner_root"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

// CreateInput is the create payload.
type CreateInput struct {
	Type         string
	Title        string
	Content      string
	Scope        string // empty → project
	SessionID    string // required when scope=session
	Access       string // empty → team
	OwnerSession string
	OwnerRoot    string
	TTL          time.Duration // optional; zero = no expiry
}

// UpdateInput is the CAS update payload. Nil pointers leave fields unchanged.
type UpdateInput struct {
	Title   *string
	Content *string
	Access  *string
	TTL     *time.Duration // non-nil: set/clear expiry (0 clears)
}

// Clone returns a deep copy safe for callers to mutate.
func Clone(a Artifact) Artifact {
	out := a
	if a.ExpiresAt != nil {
		t := *a.ExpiresAt
		out.ExpiresAt = &t
	}
	return out
}

// MetaFrom builds index metadata from a full artifact.
func MetaFrom(a Artifact) Meta {
	m := Meta{
		ID:           a.ID,
		Type:         a.Type,
		Title:        a.Title,
		Version:      a.Version,
		Scope:        a.Scope,
		SessionID:    a.SessionID,
		Access:       a.Access,
		OwnerSession: a.OwnerSession,
		OwnerRoot:    a.OwnerRoot,
		CreatedAt:    a.CreatedAt,
		UpdatedAt:    a.UpdatedAt,
	}
	if a.ExpiresAt != nil {
		t := *a.ExpiresAt
		m.ExpiresAt = &t
	}
	return m
}

// ValidType reports whether t is a registered artifact type.
func ValidType(t string) bool {
	switch t {
	case TypePlan, TypeContract, TypeFindings, TypePatch, TypeTestReport:
		return true
	default:
		return registeredTypes[t]
	}
}

// registeredTypes holds extension types (tests / future plugins).
var registeredTypes = map[string]bool{}

// RegisterType adds an extension type name (empty/invalid names ignored).
// Intended for tests and carefully gated extensions — not a general plugin API.
func RegisterType(name string) {
	if name == "" || !validTypeName(name) {
		return
	}
	registeredTypes[name] = true
}

// ValidScope reports whether s is project or session.
func ValidScope(s string) bool {
	return s == ScopeProject || s == ScopeSession
}

// ValidAccess reports whether a is owner or team.
func ValidAccess(a string) bool {
	return a == AccessOwner || a == AccessTeam
}

// CanRead reports whether actorSession under actorRoot may read a.
func CanRead(a Artifact, actorSession, actorRoot string) bool {
	actorSession = strings.TrimSpace(actorSession)
	actorRoot = strings.TrimSpace(actorRoot)
	if actorRoot == "" {
		actorRoot = actorSession
	}
	if a.Access == AccessOwner {
		return actorSession != "" && actorSession == a.OwnerSession
	}
	// team
	if a.OwnerRoot == "" {
		return actorSession != "" && actorSession == a.OwnerSession
	}
	return actorRoot != "" && actorRoot == a.OwnerRoot
}

// CanWrite reports whether actorSession under actorRoot may CAS-update a.
func CanWrite(a Artifact, actorSession, actorRoot string) bool {
	return CanRead(a, actorSession, actorRoot) // same matrix for v1
}

func validTypeName(name string) bool {
	if len(name) > 64 {
		return false
	}
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			continue
		}
		return false
	}
	return name != ""
}

func validateTitle(title string) error {
	if len(title) > maxTitleLen {
		return errTitleTooLong
	}
	for _, r := range title {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("artifact: title contains invalid character")
		}
	}
	return nil
}

func validateContent(content string) error {
	if len(content) > maxContentLen {
		return errContentTooLong
	}
	return nil
}
