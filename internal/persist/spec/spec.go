// Package spec stores phase-gated feature specs (#1348): requirements in
// EARS notation, a design document, and a task list whose tasks cite the
// requirements they implement. A spec lives in the workspace as plain
// files under <root>/<name>/ so it is diffable in git:
//
//	spec.json        authoritative structured state (phases, versions, hand-off)
//	requirements.md  rendered requirements (read-only mirror)
//	design.md        the design phase's free text
//	tasks.md         rendered tasks with their requirement citations
//
// Phases are gated: requirements → design → tasks. A phase can be approved
// only after the previous one is approved, and only by an explicit call
// (the user action in the UI). Editing a phase moves it and every later
// phase back to draft. Hand-off turns approved tasks into a plan exactly
// once; afterwards the spec is read-only history.
package spec

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Phase names, in gate order.
const (
	PhaseRequirements = "requirements"
	PhaseDesign       = "design"
	PhaseTasks        = "tasks"
)

// Phase statuses.
const (
	StatusDraft    = "draft"
	StatusReview   = "review"
	StatusApproved = "approved"
)

// Phases lists the phases in gate order.
var Phases = []string{PhaseRequirements, PhaseDesign, PhaseTasks}

var (
	// ErrNotFound is returned for an unknown spec name.
	ErrNotFound = errors.New("spec: not found")
	// ErrExists is returned when creating a spec whose name is taken.
	ErrExists = errors.New("spec: already exists")
	// ErrConflict is returned when expectedVersion does not match.
	ErrConflict = errors.New("spec: version conflict")
	// ErrGate is returned when a phase gate refuses a transition.
	ErrGate = errors.New("spec: phase gate")
	// ErrHandedOff is returned for any mutation after hand-off.
	ErrHandedOff = errors.New("spec: handed off to the task runner; start a new spec revision to change direction")
	// ErrInvalid is returned for malformed input.
	ErrInvalid = errors.New("spec: invalid")
)

// Requirement is one user story with EARS acceptance criteria.
type Requirement struct {
	ID       string   `json:"id"`
	Story    string   `json:"story"`
	Criteria []string `json:"criteria"`
}

// Task is one implementation task citing the requirements it implements.
type Task struct {
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Body         string   `json:"body,omitempty"`
	Requirements []string `json:"requirements"`
}

// Spec is the authoritative state of one spec.
type Spec struct {
	Name         string            `json:"name"`
	Title        string            `json:"title"`
	Description  string            `json:"description,omitempty"`
	Requirements []Requirement     `json:"requirements"`
	Design       string            `json:"design"`
	Tasks        []Task            `json:"tasks"`
	Status       map[string]string `json:"status"`
	PlanID       string            `json:"planId,omitempty"`
	HandedOffAt  *time.Time        `json:"handedOffAt,omitempty"`
	Version      int               `json:"version"`
	CreatedAt    time.Time         `json:"createdAt"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// Summary is one row of List.
type Summary struct {
	Name      string            `json:"name"`
	Title     string            `json:"title"`
	Status    map[string]string `json:"status"`
	PlanID    string            `json:"planId,omitempty"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// Coverage is the traceability check between tasks and requirements.
type Coverage struct {
	Covered   int      `json:"covered"`
	Total     int      `json:"total"`
	Uncovered []string `json:"uncovered"`
	// UnknownRefs are task citations that name no requirement.
	UnknownRefs []string `json:"unknownRefs"`
}

// Complete reports whether every requirement is cited by a task and no
// task cites an unknown requirement.
func (c Coverage) Complete() bool {
	return len(c.Uncovered) == 0 && len(c.UnknownRefs) == 0
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidName reports whether name is a safe spec directory name.
func ValidName(name string) bool { return nameRE.MatchString(name) }

func phaseIndex(phase string) int {
	for i, p := range Phases {
		if p == phase {
			return i
		}
	}
	return -1
}

func validStatus(s string) bool {
	return s == StatusDraft || s == StatusReview || s == StatusApproved
}

// ComputeCoverage returns the requirement coverage of s's tasks.
func ComputeCoverage(s Spec) Coverage {
	known := map[string]bool{}
	for _, r := range s.Requirements {
		known[r.ID] = false
	}
	unknown := map[string]bool{}
	for _, t := range s.Tasks {
		for _, id := range t.Requirements {
			id = strings.TrimSpace(id)
			if _, ok := known[id]; ok {
				known[id] = true
			} else if id != "" {
				unknown[id] = true
			}
		}
	}
	out := Coverage{Total: len(s.Requirements), Uncovered: []string{}, UnknownRefs: []string{}}
	for _, r := range s.Requirements {
		if known[r.ID] {
			out.Covered++
		} else {
			out.Uncovered = append(out.Uncovered, r.ID)
		}
	}
	for id := range unknown {
		out.UnknownRefs = append(out.UnknownRefs, id)
	}
	sort.Strings(out.UnknownRefs)
	return out
}

var earsLead = regexp.MustCompile(`(?i)^\s*(when|while|if|where|the)\b`)
var earsShall = regexp.MustCompile(`(?i)\bshall\b`)

// LintEARS returns warnings for a criterion that does not read as EARS
// ("WHEN ‹condition› THE SYSTEM SHALL ‹behaviour›" and its While / If /
// Where / ubiquitous forms). Warnings never block saving or approval.
func LintEARS(criterion string) []string {
	c := strings.TrimSpace(criterion)
	if c == "" {
		return []string{"empty criterion"}
	}
	var out []string
	if !earsShall.MatchString(c) {
		out = append(out, "no SHALL — EARS criteria state what the system shall do")
	}
	if !earsLead.MatchString(c) {
		out = append(out, "does not start with WHEN, WHILE, IF, WHERE or THE")
	}
	return out
}

// Lint maps "R1#0" (requirement id # criterion index) to its warnings.
func Lint(s Spec) map[string][]string {
	out := map[string][]string{}
	for _, r := range s.Requirements {
		if len(r.Criteria) == 0 {
			out[r.ID] = []string{"no acceptance criteria"}
		}
		for i, c := range r.Criteria {
			if w := LintEARS(c); len(w) > 0 {
				out[fmt.Sprintf("%s#%d", r.ID, i)] = w
			}
		}
	}
	return out
}

// normalizeRequirements trims input and assigns R<n> ids to rows without one.
func normalizeRequirements(in []Requirement) ([]Requirement, error) {
	used := map[string]bool{}
	for _, r := range in {
		if id := strings.TrimSpace(r.ID); id != "" {
			if used[id] {
				return nil, fmt.Errorf("%w: duplicate requirement id %s", ErrInvalid, id)
			}
			used[id] = true
		}
	}
	next := 1
	out := make([]Requirement, 0, len(in))
	for _, r := range in {
		story := strings.TrimSpace(r.Story)
		if story == "" {
			return nil, fmt.Errorf("%w: every requirement needs a user story", ErrInvalid)
		}
		id := strings.TrimSpace(r.ID)
		if id == "" {
			for used[fmt.Sprintf("R%d", next)] {
				next++
			}
			id = fmt.Sprintf("R%d", next)
			used[id] = true
		}
		var crit []string
		for _, c := range r.Criteria {
			if c = strings.TrimSpace(c); c != "" {
				crit = append(crit, c)
			}
		}
		if crit == nil {
			crit = []string{}
		}
		out = append(out, Requirement{ID: id, Story: story, Criteria: crit})
	}
	return out, nil
}

// normalizeTasks trims input and assigns T<n> ids to rows without one.
func normalizeTasks(in []Task) ([]Task, error) {
	used := map[string]bool{}
	for _, t := range in {
		if id := strings.TrimSpace(t.ID); id != "" {
			if used[id] {
				return nil, fmt.Errorf("%w: duplicate task id %s", ErrInvalid, id)
			}
			used[id] = true
		}
	}
	next := 1
	out := make([]Task, 0, len(in))
	for _, t := range in {
		title := strings.TrimSpace(t.Title)
		if title == "" {
			return nil, fmt.Errorf("%w: every task needs a title", ErrInvalid)
		}
		id := strings.TrimSpace(t.ID)
		if id == "" {
			for used[fmt.Sprintf("T%d", next)] {
				next++
			}
			id = fmt.Sprintf("T%d", next)
			used[id] = true
		}
		refs := []string{}
		seen := map[string]bool{}
		for _, r := range t.Requirements {
			if r = strings.TrimSpace(r); r != "" && !seen[r] {
				seen[r] = true
				refs = append(refs, r)
			}
		}
		out = append(out, Task{ID: id, Title: title, Body: t.Body, Requirements: refs})
	}
	return out, nil
}

// resetFrom moves phase and every later phase back to draft.
func resetFrom(s *Spec, phase string) {
	for i := phaseIndex(phase); i >= 0 && i < len(Phases); i++ {
		s.Status[Phases[i]] = StatusDraft
	}
}

// setPhaseStatus applies the gate rules to one transition.
func setPhaseStatus(s *Spec, phase, status string) error {
	idx := phaseIndex(phase)
	if idx < 0 {
		return fmt.Errorf("%w: unknown phase %q", ErrInvalid, phase)
	}
	if !validStatus(status) {
		return fmt.Errorf("%w: unknown status %q", ErrInvalid, status)
	}
	switch status {
	case StatusApproved:
		if idx > 0 && s.Status[Phases[idx-1]] != StatusApproved {
			return fmt.Errorf("%w: approve %s first", ErrGate, Phases[idx-1])
		}
		if phase == PhaseRequirements && len(s.Requirements) == 0 {
			return fmt.Errorf("%w: add at least one requirement before approving", ErrGate)
		}
		if phase == PhaseTasks && len(s.Tasks) == 0 {
			return fmt.Errorf("%w: add at least one task before approving", ErrGate)
		}
		s.Status[phase] = StatusApproved
	case StatusReview:
		s.Status[phase] = StatusReview
	case StatusDraft:
		resetFrom(s, phase)
	}
	return nil
}

// RenderRequirements renders the requirements phase as markdown.
func RenderRequirements(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — Requirements\n\nStatus: %s\n", s.Title, s.Status[PhaseRequirements])
	for _, r := range s.Requirements {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n", r.ID, r.Story)
		for _, c := range r.Criteria {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}
	return b.String()
}

// RenderTasks renders the tasks phase as markdown with citations.
func RenderTasks(s Spec) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s — Tasks\n\nStatus: %s\n", s.Title, s.Status[PhaseTasks])
	for _, t := range s.Tasks {
		fmt.Fprintf(&b, "\n## %s %s\n\nImplements: %s\n", t.ID, t.Title, strings.Join(t.Requirements, ", "))
		if strings.TrimSpace(t.Body) != "" {
			fmt.Fprintf(&b, "\n%s\n", t.Body)
		}
	}
	return b.String()
}

// PlanSection is one section of the plan a hand-off creates.
type PlanSection struct {
	Title string
	Body  string
}

// PlanSections converts tasks into plan sections whose bodies cite the
// requirements (id + story + criteria) each task implements.
func PlanSections(s Spec) []PlanSection {
	byID := map[string]Requirement{}
	for _, r := range s.Requirements {
		byID[r.ID] = r
	}
	out := make([]PlanSection, 0, len(s.Tasks))
	for _, t := range s.Tasks {
		var b strings.Builder
		if body := strings.TrimSpace(t.Body); body != "" {
			b.WriteString(body)
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Implements (spec %s):\n", s.Name)
		for _, id := range t.Requirements {
			r := byID[id]
			fmt.Fprintf(&b, "- %s: %s\n", id, r.Story)
			for _, c := range r.Criteria {
				fmt.Fprintf(&b, "  - %s\n", c)
			}
		}
		out = append(out, PlanSection{Title: fmt.Sprintf("%s %s", t.ID, t.Title), Body: b.String()})
	}
	return out
}
