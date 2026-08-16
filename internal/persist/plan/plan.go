// Package plan stores root-session-owned structured plans with ordered
// stable-ID sections, lifecycle, and compare-and-swap versions.
// cmd/strike opens the store at startup; internal/frontend/host/local wraps it for
// frontends; internal/tool plan_read/plan_write are the agent surface.
package plan

import (
	"fmt"
	"time"
)

// Lifecycle statuses for a stored plan.
const (
	StatusDraft    = "draft"
	StatusApproved = "approved"
	StatusClosed   = "closed"
)

// Section delegation statuses (team-runtime refinement of one section).
// Empty means never delegated or cleared. in_flight rejects a second dispatch.
const (
	DelegateInFlight  = "in_flight"
	DelegateApplied   = "applied"
	DelegateFailed    = "failed"
	DelegateCanceled  = "canceled"
	DelegateConflict  = "conflict"
	DelegateMalformed = "malformed"
)

// Section is one ordered block inside a plan. IDs are stable for the life of
// the plan (never reused after assignment).
//
// Delegate* fields correlate a section to a child session for refinement via
// the existing task/team runtime. Content updates on completion use the
// base title/body snapshot (not plan-wide version alone) so concurrent edits
// to other sections do not block apply, while an intervening edit of this
// section yields conflict without overwrite.
type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`

	// Delegation correlation (persisted; survives resume/root switch).
	DelegateStatus      string `json:"delegate_status,omitempty"`
	DelegateChildID     string `json:"delegate_child_id,omitempty"`
	DelegateChildName   string `json:"delegate_child_name,omitempty"`
	DelegateBaseVersion int    `json:"delegate_base_version,omitempty"` // plan version at dispatch
	DelegateBaseTitle   string `json:"delegate_base_title,omitempty"`
	DelegateBaseBody    string `json:"delegate_base_body,omitempty"`
	DelegateDetail      string `json:"delegate_detail,omitempty"` // actionable status for UI/model
}

// ValidDelegateStatus reports whether s is a known terminal or in-flight value.
func ValidDelegateStatus(s string) bool {
	switch s {
	case "", DelegateInFlight, DelegateApplied, DelegateFailed,
		DelegateCanceled, DelegateConflict, DelegateMalformed:
		return true
	default:
		return false
	}
}

// DelegateOutcome is the terminal result of a section refinement child.
// Status must be a terminal delegate status (not in_flight or empty).
// Title/Body are applied only when Status is applied and the section content
// still matches the base snapshot captured at dispatch.
type DelegateOutcome struct {
	Status string  // applied|failed|canceled|conflict|malformed
	Title  *string // optional new title when applied
	Body   *string // optional new body when applied
	Detail string  // human/model-facing reason (required for non-applied)
}

// SectionInput is a create/add payload without an ID (the store assigns one).
type SectionInput struct {
	Title string
	Body  string
}

// Plan is one durable root-owned planning artifact.
type Plan struct {
	ID             string    `json:"id"`
	OwnerRoot      string    `json:"owner_root"`
	Title          string    `json:"title"`
	Status         string    `json:"status"`
	Sections       []Section `json:"sections"`
	Version        int       `json:"version"` // CAS token; increments on every accepted mutation
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	NextSectionSeq int       `json:"next_section_seq"` // next sN; not part of host projection
}

// Meta is project-index metadata visible to every root in the project.
// Full section bodies are omitted so non-owners can browse without a full read.
type Meta struct {
	ID           string    `json:"id"`
	OwnerRoot    string    `json:"owner_root"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Version      int       `json:"version"`
	SectionCount int       `json:"section_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// ClonePlan returns a deep copy safe for callers to mutate freely.
func ClonePlan(p Plan) Plan {
	out := p
	if p.Sections != nil {
		out.Sections = make([]Section, len(p.Sections))
		copy(out.Sections, p.Sections)
	} else {
		out.Sections = nil
	}
	return out
}

// MetaFromPlan builds index metadata from a full plan.
func MetaFromPlan(p Plan) Meta {
	return Meta{
		ID:           p.ID,
		OwnerRoot:    p.OwnerRoot,
		Title:        p.Title,
		Status:       p.Status,
		Version:      p.Version,
		SectionCount: len(p.Sections),
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
}

// ValidStatus reports whether s is a known lifecycle value.
func ValidStatus(s string) bool {
	switch s {
	case StatusDraft, StatusApproved, StatusClosed:
		return true
	default:
		return false
	}
}

// canTransition reports whether owner-driven status changes are legal.
// Reopen is the only path out of closed (closed → draft).
func canTransition(from, to string) bool {
	if from == to {
		return true
	}
	switch from {
	case StatusDraft:
		return to == StatusApproved || to == StatusClosed
	case StatusApproved:
		return to == StatusDraft || to == StatusClosed
	case StatusClosed:
		return false // use Reopen
	default:
		return false
	}
}

func validateTitle(title string) error {
	if title == "" {
		return errEmptyTitle
	}
	if len(title) > maxTitleLen {
		return errTitleTooLong
	}
	for _, r := range title {
		if r == 0 || r == '\n' || r == '\r' {
			return fmt.Errorf("plan: title contains invalid character")
		}
	}
	return nil
}

func validateSectionBody(body string) error {
	if len(body) > maxSectionBodyLen {
		return errBodyTooLong
	}
	return nil
}
