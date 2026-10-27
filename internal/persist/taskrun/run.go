// Package taskrun stores one durable execution of a plan: ordered sections,
// explicit dependencies, spawned child ids, and the checkpoint taken from
// that child's handoff and verification report.
//
// A section runs only when every section it depends on is approved or
// skipped. A failed section blocks its dependants. Nothing in this package
// spawns a child; the daemon does that and then records the outcome.
package taskrun

import (
	"strings"
	"time"
)

// Section lifecycle. blocked is derived: a dependency failed, or is itself
// blocked. pending means not started; it is not runnable until dependencies
// are approved or skipped.
const (
	StatusPending    = "pending"
	StatusRunning    = "running"
	StatusCheckpoint = "checkpoint"
	StatusApproved   = "approved"
	StatusFailed     = "failed"
	StatusSkipped    = "skipped"
	StatusBlocked    = "blocked"
)

// SectionInput is one plan section used to open or refresh a run.
type SectionInput struct {
	ID    string
	Title string
	Body  string
}

// Check is one verification gate, trimmed for the board.
type Check struct {
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Passed bool   `json:"passed"`
	Error  string `json:"error,omitempty"`
}

// Checkpoint is the review record for one section. It is the section body
// plus the child handoff and the harness verification report, not a second
// snapshot format.
type Checkpoint struct {
	SectionBody    string    `json:"sectionBody,omitempty"`
	ChildID        string    `json:"childId"`
	ChildStatus    string    `json:"childStatus,omitempty"`
	Summary        string    `json:"summary,omitempty"`
	FilesChanged   []string  `json:"filesChanged,omitempty"`
	HandoffText    string    `json:"handoffText,omitempty"`
	Findings       []string  `json:"findings,omitempty"`
	Blockers       []string  `json:"blockers,omitempty"`
	Incomplete     bool      `json:"incomplete,omitempty"`
	Quality        string    `json:"quality,omitempty"`
	HasVerify      bool      `json:"hasVerify,omitempty"`
	VerifyPassed   bool      `json:"verifyPassed"`
	VerifyClaimed  bool      `json:"verifyClaimed,omitempty"`
	VerifyVerified bool      `json:"verifyVerified,omitempty"`
	VerifySummary  string    `json:"verifySummary,omitempty"`
	Checks         []Check   `json:"checks,omitempty"`
	RecordedAt     time.Time `json:"recordedAt"`
}

// Outcome is the terminal child result the daemon records onto a section.
type Outcome struct {
	ChildID        string
	ChildStatus    string
	Summary        string
	FilesChanged   []string
	HandoffText    string
	Findings       []string
	Blockers       []string
	Incomplete     bool
	Quality        string
	HasVerify      bool
	VerifyPassed   bool
	VerifyClaimed  bool
	VerifyVerified bool
	VerifySummary  string
	Checks         []Check
}

// Section is one checklist row.
type Section struct {
	ID         string      `json:"id"`
	Title      string      `json:"title"`
	Body       string      `json:"body,omitempty"`
	DependsOn  []string    `json:"dependsOn,omitempty"`
	Status     string      `json:"status"`
	ChildID    string      `json:"childId,omitempty"`
	Detail     string      `json:"detail,omitempty"`
	Checkpoint *Checkpoint `json:"checkpoint,omitempty"`
}

// Run is the durable board for one plan.
type Run struct {
	PlanID     string    `json:"planId"`
	OwnerRoot  string    `json:"ownerRoot"`
	PlanTitle  string    `json:"planTitle"`
	PlanStatus string    `json:"planStatus"`
	Version    int       `json:"version"`
	Sections   []Section `json:"sections"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Failed reports whether a child outcome must stop dependants.
// schema:handoff (#1201) leaves the child blocked or the handoff incomplete;
// both are failures for the run. A passing verification with a structured
// handoff is a checkpoint the user still has to approve.
func Failed(o Outcome) bool {
	switch strings.ToLower(strings.TrimSpace(o.ChildStatus)) {
	case "failed", "canceled", "cancelled", "blocked":
		return true
	}
	if o.Incomplete {
		return true
	}
	if o.HasVerify && !o.VerifyPassed {
		return true
	}
	return false
}

// ReadyIDs returns sections that may be spawned. Order follows the checklist.
// A checkpoint is not ready: the user approves or skips it first.
func ReadyIDs(run Run) []string {
	by := index(run.Sections)
	var ids []string
	for _, sec := range run.Sections {
		if sec.Status != StatusPending {
			continue
		}
		if depsSatisfied(sec, by) {
			ids = append(ids, sec.ID)
		}
	}
	return ids
}

func depsSatisfied(sec Section, by map[string]Section) bool {
	for _, id := range sec.DependsOn {
		dep, ok := by[id]
		if !ok {
			return false
		}
		if dep.Status != StatusApproved && dep.Status != StatusSkipped {
			return false
		}
	}
	return true
}

func index(sections []Section) map[string]Section {
	by := make(map[string]Section, len(sections))
	for _, sec := range sections {
		by[sec.ID] = sec
	}
	return by
}

// recompute derives blocked vs pending. Terminal rows are left alone.
// A dependency that failed, or is blocked by one that did, blocks this row.
// A dependency that is merely not finished yet leaves this row pending.
func recompute(sections []Section) {
	for pass := 0; pass < len(sections)+1; pass++ {
		by := index(sections)
		changed := false
		for i := range sections {
			sec := &sections[i]
			switch sec.Status {
			case StatusRunning, StatusCheckpoint, StatusApproved, StatusFailed, StatusSkipped:
				continue
			}
			blocked, who := blockedBy(sec.DependsOn, by)
			next := StatusPending
			detail := ""
			if blocked {
				next = StatusBlocked
				detail = "blocked by " + who
			}
			if sec.Status != next || (blocked && sec.Detail != detail) || (!blocked && sec.Status == StatusBlocked) {
				sec.Status = next
				if blocked {
					sec.Detail = detail
				} else {
					sec.Detail = ""
				}
				changed = true
			}
		}
		if !changed {
			return
		}
	}
}

func blockedBy(deps []string, by map[string]Section) (bool, string) {
	for _, id := range deps {
		dep, ok := by[id]
		if !ok {
			return true, id
		}
		if dep.Status == StatusFailed || dep.Status == StatusBlocked {
			if dep.Title != "" {
				return true, dep.Title
			}
			return true, id
		}
	}
	return false, ""
}

// canRechain is true when a reorder may retarget this section's dependency
// to its new predecessor. A section that already depends on something that
// has left the pending state keeps that edge, so a reorder cannot route
// around a failure.
func canRechain(sec Section, by map[string]Section) bool {
	if sec.Status != StatusPending && sec.Status != StatusBlocked {
		return false
	}
	for _, id := range sec.DependsOn {
		dep, ok := by[id]
		if !ok || dep.Status != StatusPending {
			return false
		}
	}
	return true
}

func chainFromOrder(sections []Section) {
	by := index(sections)
	for i := range sections {
		if !canRechain(sections[i], by) {
			continue
		}
		if i == 0 {
			sections[i].DependsOn = nil
			continue
		}
		sections[i].DependsOn = []string{sections[i-1].ID}
	}
}

func applyOutcome(sec *Section, o Outcome) {
	cp := &Checkpoint{
		SectionBody:    sec.Body,
		ChildID:        o.ChildID,
		ChildStatus:    o.ChildStatus,
		Summary:        clip(o.Summary, 8<<10),
		FilesChanged:   clipList(o.FilesChanged, 100),
		HandoffText:    clip(o.HandoffText, 8<<10),
		Findings:       clipList(o.Findings, 50),
		Blockers:       clipList(o.Blockers, 50),
		Incomplete:     o.Incomplete,
		Quality:        o.Quality,
		HasVerify:      o.HasVerify,
		VerifyPassed:   o.VerifyPassed,
		VerifyClaimed:  o.VerifyClaimed,
		VerifyVerified: o.VerifyVerified,
		VerifySummary:  clip(o.VerifySummary, 8<<10),
		Checks:         clipChecks(o.Checks),
		RecordedAt:     time.Now().UTC(),
	}
	sec.Checkpoint = cp
	sec.ChildID = o.ChildID
	if Failed(o) {
		sec.Status = StatusFailed
		sec.Detail = failDetail(o)
		return
	}
	sec.Status = StatusCheckpoint
	sec.Detail = "Checkpoint ready for review"
}

func failDetail(o Outcome) string {
	if s := strings.TrimSpace(o.VerifySummary); s != "" {
		return clip(s, 500)
	}
	if o.Incomplete {
		return "schema:handoff blocked this section (handoff incomplete)"
	}
	if s := strings.TrimSpace(o.ChildStatus); s != "" {
		return "child " + s
	}
	return "section failed"
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func clipList(in []string, n int) []string {
	if len(in) == 0 {
		return nil
	}
	if len(in) > n {
		in = in[:n]
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = clip(s, 500)
	}
	return out
}

func clipChecks(in []Check) []Check {
	if len(in) == 0 {
		return nil
	}
	if len(in) > 40 {
		in = in[:40]
	}
	out := make([]Check, len(in))
	for i, c := range in {
		c.Name = clip(c.Name, 200)
		c.Kind = clip(c.Kind, 40)
		c.Error = clip(c.Error, 500)
		out[i] = c
	}
	return out
}

// SectionObjective is the team.spawn prompt for one section. The child must
// finish with a structured handoff; the schema:handoff gate blocks the
// section, and therefore its dependants, when that object is missing.
func SectionObjective(planTitle string, sec Section) string {
	var b strings.Builder
	b.WriteString("Plan ")
	b.WriteString(strconvQuote(planTitle))
	b.WriteString(", section ")
	b.WriteString(strconvQuote(sec.Title))
	b.WriteString(" (")
	b.WriteString(sec.ID)
	b.WriteString(").\n\nDo this work:\n\n")
	body := strings.TrimSpace(sec.Body)
	if body == "" {
		body = "(no body)"
	}
	b.WriteString(body)
	b.WriteString("\n\nFinish with a structured completion handoff: a JSON object with summary, files_changed, findings, and blockers. The schema:handoff gate blocks this section, and every section that depends on it, until that object is present.\n")
	return b.String()
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `'`) + `"`
}

// SectionName is the teammate alias for a spawned section.
func SectionName(sec Section) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == ' ' || r == '-' || r == '_':
			return '-'
		default:
			return -1
		}
	}, sec.Title)
	name = strings.Trim(name, "-")
	if name == "" {
		name = sec.ID
	}
	if len(name) > 24 {
		name = name[:24]
	}
	return "section-" + name
}
