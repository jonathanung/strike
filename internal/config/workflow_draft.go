package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
)

// ErrWorkflowExists is returned when saving a draft would overwrite an
// existing file without force. Callers must confirm overwrite explicitly.
var ErrWorkflowExists = errors.New("workflow file already exists")

// ErrDraftInvalid is returned when a save is refused because the draft failed
// validation. The draft remains editable in memory.
var ErrDraftInvalid = errors.New("workflow draft is invalid")

// ErrSaveNotConfirmed is returned when SaveWorkflowDraft is called without
// Confirm set. Generation and review never imply save consent.
var ErrSaveNotConfirmed = errors.New("workflow save requires explicit confirmation")

// WorkflowDraft is an in-memory workflow candidate produced by model generation
// or manual edit. Drafts are never activated by parse, review, or save —
// activation remains a separate catalog/runtime step.
type WorkflowDraft struct {
	// Workflow is the best-effort decoded document. Zero value when parse
	// failed entirely; may still be partially filled after soft failures.
	Workflow Workflow
	// Raw is the last JSON bytes associated with this draft (model output or
	// user correction). Kept so invalid drafts remain editable.
	Raw []byte
	// Diagnostics collects parse and validation findings for the current Raw.
	Diagnostics WorkflowErrors
	// SourceLabel is a short origin tag for review (e.g. "model", "edit").
	SourceLabel string
}

// Valid reports whether the draft has a structurally valid workflow and no
// diagnostics. Agent-reference errors are included when Revalidate ran with a
// known-agent set.
func (d WorkflowDraft) Valid() bool {
	return len(d.Diagnostics) == 0 && d.Workflow.Name != "" && len(d.Workflow.Phases) > 0
}

// DraftFromJSON builds a draft from canonical (or model-emitted) JSON.
// Parse and validation failures never discard Raw — the draft stays editable
// with actionable diagnostics. Does not write disk or activate.
func DraftFromJSON(data []byte, sourceLabel string) WorkflowDraft {
	d := WorkflowDraft{
		Raw:         append([]byte(nil), data...),
		SourceLabel: strings.TrimSpace(sourceLabel),
	}
	if d.SourceLabel == "" {
		d.SourceLabel = "draft"
	}
	if len(bytes.TrimSpace(data)) == 0 {
		d.Diagnostics = WorkflowErrors{{Msg: "empty workflow JSON"}}
		return d
	}
	w, err := ParseWorkflowSource(data, d.SourceLabel)
	if err != nil {
		d.Diagnostics = asWorkflowErrors(err)
		// Best-effort partial decode for review of broken documents.
		var partial Workflow
		if uerr := json.Unmarshal(data, &partial); uerr == nil {
			normalizeWorkflow(&partial)
			d.Workflow = partial
		}
		return d
	}
	d.Workflow = w
	return d
}

// DraftFromModelText extracts a JSON document from model prose (fenced blocks
// or bare JSON) and builds a draft. Always returns a draft; invalid output
// yields diagnostics without saving.
func DraftFromModelText(text, sourceLabel string) WorkflowDraft {
	raw, err := ExtractWorkflowJSON(text)
	if err != nil {
		return WorkflowDraft{
			Raw:         []byte(text),
			SourceLabel: labelOr(sourceLabel, "model"),
			Diagnostics: WorkflowErrors{{Msg: err.Error()}},
		}
	}
	return DraftFromJSON(raw, labelOr(sourceLabel, "model"))
}

func labelOr(s, fallback string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	return s
}

// ExtractWorkflowJSON pulls a JSON object from model output. Prefers fenced
// ```json / ``` blocks; falls back to the first top-level {...} span.
func ExtractWorkflowJSON(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty model output")
	}
	// Fenced code block (```json ... ``` or ``` ... ```).
	if i := strings.Index(text, "```"); i >= 0 {
		rest := text[i+3:]
		// Optional language tag on the opening fence line.
		if nl := strings.IndexByte(rest, '\n'); nl >= 0 {
			lang := strings.TrimSpace(rest[:nl])
			if lang == "" || strings.EqualFold(lang, "json") || strings.EqualFold(lang, "jsonc") {
				rest = rest[nl+1:]
			}
		}
		if end := strings.Index(rest, "```"); end >= 0 {
			body := strings.TrimSpace(rest[:end])
			if body != "" {
				return []byte(body), nil
			}
		}
	}
	// Bare JSON object: first { to last }.
	start := strings.IndexByte(text, '{')
	end := strings.LastIndexByte(text, '}')
	if start >= 0 && end > start {
		return []byte(strings.TrimSpace(text[start : end+1])), nil
	}
	return nil, fmt.Errorf("no JSON object found in model output")
}

// Revalidate re-runs structural validation and optional agent resolution on
// the current Workflow, replacing Diagnostics. When Raw is set but Workflow is
// empty, re-parses Raw first. Does not save or activate.
func (d *WorkflowDraft) Revalidate(knownAgents map[string]struct{}) {
	if d == nil {
		return
	}
	if d.Workflow.Name == "" && len(d.Raw) > 0 {
		*d = DraftFromJSON(d.Raw, d.SourceLabel)
	}
	var errs WorkflowErrors
	if err := validateWorkflow(d.Workflow, d.SourceLabel); err != nil {
		errs = append(errs, asWorkflowErrors(err)...)
	}
	if err := validateWorkflowAgents(d.Workflow, knownAgents, d.SourceLabel); err != nil {
		errs = append(errs, asWorkflowErrors(err)...)
	}
	d.Diagnostics = errs
	if d.Valid() {
		d.Workflow.Fingerprint = MustWorkflowFingerprint(d.Workflow)
	}
}

// ApplyJSON replaces Raw with a user correction and re-parses. Used when the
// model output was invalid and the user (or a follow-up) supplies fixed JSON.
func (d *WorkflowDraft) ApplyJSON(data []byte, knownAgents map[string]struct{}) {
	if d == nil {
		return
	}
	label := d.SourceLabel
	if label == "" {
		label = "edit"
	} else if label == "model" {
		label = "edit"
	}
	*d = DraftFromJSON(data, label)
	d.Revalidate(knownAgents)
}

// DraftReviewOpts controls review rendering (widening baseline, agent set).
type DraftReviewOpts struct {
	// Baseline is the permission layers used to compute effective widening
	// (typically defaults + config + agent). Nil uses permission.Defaults().
	Baseline []permission.Ruleset
	// KnownAgents, when non-nil, is used to revalidate agent pins before review.
	KnownAgents map[string]struct{}
}

// PhaseDraftReview is one phase in a structured draft review.
type PhaseDraftReview struct {
	Name        string
	Description string
	Agent       string
	Context     string
	Gate        string
	GateCommand string
	// CheckHighlighted is true when this phase has an executable check gate.
	CheckHighlighted bool
	Permissions      []permission.Rule
	// Widening is the effective grant delta vs Baseline (allow/ask upgrades).
	Widening []permission.Rule
}

// WorkflowDraftReview is a structured, host-safe review of a draft before save.
type WorkflowDraftReview struct {
	Name        string
	Description string
	SourceLabel string
	Valid       bool
	Diagnostics WorkflowErrors
	Fingerprint string
	Phases      []PhaseDraftReview
	// HasChecks is true when any phase uses an executable check gate.
	HasChecks bool
	// HasWidening is true when any phase widens baseline permissions.
	HasWidening bool
}

// ReviewWorkflowDraft builds a structured review. Executable check gates and
// permission widenings are flagged for prominent display. Does not save or
// activate. When opts.KnownAgents is set, diagnostics are refreshed first.
func ReviewWorkflowDraft(d WorkflowDraft, opts DraftReviewOpts) WorkflowDraftReview {
	if opts.KnownAgents != nil {
		d.Revalidate(opts.KnownAgents)
	}
	baseline := opts.Baseline
	if len(baseline) == 0 {
		baseline = []permission.Ruleset{permission.Defaults()}
	}
	rev := WorkflowDraftReview{
		Name:        d.Workflow.Name,
		Description: d.Workflow.Description,
		SourceLabel: d.SourceLabel,
		Valid:       d.Valid(),
		Diagnostics: append(WorkflowErrors(nil), d.Diagnostics...),
		Fingerprint: d.Workflow.Fingerprint,
	}
	for _, p := range d.Workflow.Phases {
		gate := string(p.Exit.Type)
		if gate == "" {
			gate = string(GateAgent)
		}
		pr := PhaseDraftReview{
			Name:        p.Name,
			Description: p.Description,
			Agent:       p.Agent,
			Context:     p.Context,
			Gate:        gate,
			GateCommand: p.Exit.Command,
			Permissions: append(permission.Ruleset(nil), p.Permissions...),
		}
		if p.Exit.Type == GateCheck {
			pr.CheckHighlighted = true
			rev.HasChecks = true
		}
		delta := permission.WideningDelta(baseline, p.Permissions)
		if len(delta) > 0 {
			pr.Widening = delta
			rev.HasWidening = true
		}
		rev.Phases = append(rev.Phases, pr)
	}
	return rev
}

// FormatDraftReview returns a plain-text review with prominent sections for
// executable checks and widened grants. Suitable for CLI stdout.
func FormatDraftReview(rev WorkflowDraftReview) string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Workflow draft review: %s ===\n", emptyDash(rev.Name))
	if rev.SourceLabel != "" {
		fmt.Fprintf(&b, "source: %s\n", rev.SourceLabel)
	}
	if rev.Description != "" {
		fmt.Fprintf(&b, "description: %s\n", rev.Description)
	}
	if rev.Valid {
		b.WriteString("status: VALID\n")
	} else {
		b.WriteString("status: INVALID (editable draft — fix diagnostics before save)\n")
	}
	if rev.Fingerprint != "" {
		fp := rev.Fingerprint
		if len(fp) > 12 {
			fp = fp[:12]
		}
		fmt.Fprintf(&b, "fingerprint: %s\n", fp)
	}
	if len(rev.Diagnostics) > 0 {
		b.WriteString("diagnostics:\n")
		for _, e := range rev.Diagnostics {
			fmt.Fprintf(&b, "  - %s\n", e.Error())
		}
	}
	if rev.HasChecks {
		b.WriteString("\n*** EXECUTABLE CHECK GATES (review carefully) ***\n")
		for i, p := range rev.Phases {
			if !p.CheckHighlighted {
				continue
			}
			fmt.Fprintf(&b, "  phases[%d] %s: %s\n", i, emptyDash(p.Name), emptyDash(p.GateCommand))
		}
	}
	if rev.HasWidening {
		b.WriteString("\n*** EFFECTIVE PERMISSION WIDENING (review carefully) ***\n")
		for i, p := range rev.Phases {
			if len(p.Widening) == 0 {
				continue
			}
			fmt.Fprintf(&b, "  phases[%d] %s:\n", i, emptyDash(p.Name))
			for _, r := range p.Widening {
				pat := r.Pattern
				if pat == "" {
					pat = "*"
				}
				fmt.Fprintf(&b, "    + %s %s %s\n", r.Action, r.Permission, pat)
			}
		}
	}
	b.WriteString("\nphases:\n")
	if len(rev.Phases) == 0 {
		b.WriteString("  (none)\n")
	}
	for i, p := range rev.Phases {
		fmt.Fprintf(&b, "  %d. %s", i, emptyDash(p.Name))
		if p.Agent != "" {
			fmt.Fprintf(&b, " @%s", p.Agent)
		}
		fmt.Fprintf(&b, "  exit=%s", p.Gate)
		if p.GateCommand != "" {
			fmt.Fprintf(&b, " cmd=%q", p.GateCommand)
		}
		if p.CheckHighlighted {
			b.WriteString("  [CHECK]")
		}
		if len(p.Widening) > 0 {
			b.WriteString("  [WIDEN]")
		}
		b.WriteByte('\n')
		if p.Description != "" {
			fmt.Fprintf(&b, "     desc: %s\n", p.Description)
		}
		if strings.TrimSpace(p.Context) != "" {
			ctx := p.Context
			if len(ctx) > 120 {
				ctx = ctx[:117] + "..."
			}
			fmt.Fprintf(&b, "     context: %s\n", strings.ReplaceAll(ctx, "\n", " "))
		}
		if len(p.Permissions) > 0 {
			b.WriteString("     permissions:\n")
			for _, r := range p.Permissions {
				pat := r.Pattern
				if pat == "" {
					pat = "*"
				}
				fmt.Fprintf(&b, "       - %s %s %s\n", r.Action, r.Permission, pat)
			}
		} else {
			b.WriteString("     permissions: (none)\n")
		}
	}
	b.WriteString("\nnote: drafts are never activated by generate/review/save; use catalog start after save.\n")
	return b.String()
}

func emptyDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// SaveDraftOpts controls durable write of an accepted draft.
type SaveDraftOpts struct {
	// Scope is "global" or "project".
	Scope string
	// WorkDir is required for project scope.
	WorkDir string
	// Force overwrites an existing file after the caller confirmed overwrite.
	Force bool
	// Confirm must be true — generation/review never imply save consent.
	Confirm bool
}

// SaveWorkflowDraft validates then atomically writes a draft to the scoped
// workflows directory. Refuses invalid drafts and unconfirmed saves. Never
// activates the workflow. On failure the prior file (if any) is left intact
// (write via temp + rename).
func SaveWorkflowDraft(d WorkflowDraft, opts SaveDraftOpts) (path string, err error) {
	if !opts.Confirm {
		return "", ErrSaveNotConfirmed
	}
	if !d.Valid() {
		if len(d.Diagnostics) > 0 {
			return "", fmt.Errorf("%w: %s", ErrDraftInvalid, d.Diagnostics.Error())
		}
		return "", ErrDraftInvalid
	}
	// Re-validate structure one more time before disk I/O.
	if err := ValidateWorkflow(d.Workflow); err != nil {
		return "", fmt.Errorf("%w: %s", ErrDraftInvalid, err.Error())
	}
	dir, err := WorkflowDir(opts.Scope, opts.WorkDir)
	if err != nil {
		return "", err
	}
	path = filepath.Join(dir, d.Workflow.Name+".json")
	if err := writeWorkflowFilePreserve(path, d.Workflow, opts.Force); err != nil {
		return "", err
	}
	return path, nil
}

// writeWorkflowFilePreserve is WriteWorkflowFile with ErrWorkflowExists and
// guaranteed prior-file retention on failure (temp + rename in the same dir).
func writeWorkflowFilePreserve(path string, w Workflow, force bool) error {
	if path == "" {
		return fmt.Errorf("empty workflow path")
	}
	raw, err := FormatWorkflow(w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%w: %s (pass force after confirming overwrite)", ErrWorkflowExists, path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	// Read prior bytes so a failed rename path can be reasoned about in tests;
	// atomic rename never truncates the destination on failure.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".workflow-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(raw); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	return nil
}
