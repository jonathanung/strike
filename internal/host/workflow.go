package host

import "errors"

// Workflow source labels for WorkflowSummary.Source. Values match config
// WorkflowSource strings so frontends can display them without importing
// internal/config.
const (
	WorkflowSourceBuiltin = "builtin"
	WorkflowSourceGlobal  = "global"
	WorkflowSourceProject = "project"
	WorkflowSourcePlugin  = "plugin"
)

// Workflow save scopes for Workflows.Save. Explicit scope is required; saving
// never activates a workflow.
const (
	WorkflowScopeGlobal  = "global"
	WorkflowScopeProject = "project"
)

// ErrWorkflowInvalid is returned by Save when the document fails validation.
var ErrWorkflowInvalid = errors.New("workflow is invalid")

// ErrWorkflowExists is returned by Save when the target file exists and force
// is false.
var ErrWorkflowExists = errors.New("workflow file already exists")

// WorkflowPermission is one phase permission rule for catalog display and
// authoring. Action is allow|ask|deny.
type WorkflowPermission struct {
	Permission string
	Pattern    string
	Action     string
}

// WorkflowPhaseSummary is one phase of a catalogued workflow.
type WorkflowPhaseSummary struct {
	Name        string
	Description string
	Agent       string
	Gate        string // agent | user | check
	GateCommand string
	Permissions []WorkflowPermission
}

// WorkflowSummary is a host-safe catalog entry for one loaded workflow.
// Invalid entries (Valid=false) are listed for inspection but must not be
// activated by frontends.
type WorkflowSummary struct {
	Name            string
	Description     string
	Source          string // builtin | global | project | plugin
	Fingerprint     string
	Path            string // absolute path when disk-backed; empty for builtin
	Valid           bool
	ValidationError string
	Phases          []WorkflowPhaseSummary
}

// WorkflowPhaseDocument is the editable phase shape (includes context).
type WorkflowPhaseDocument struct {
	Name        string
	Description string
	Agent       string
	Context     string
	Permissions []WorkflowPermission
	Gate        string // agent | user | check
	GateCommand string
}

// WorkflowDocument is the editable on-disk workflow shape for the TUI builder
// and other frontends. Runtime diagnostics (source/path/fingerprint) are not
// part of the document — Save writes only the canonical schema fields.
type WorkflowDocument struct {
	SchemaVersion int
	Name          string
	Description   string
	Phases        []WorkflowPhaseDocument
}

// Workflows is the catalog of loaded workflow definitions plus authoring
// helpers (validate/format/save). Nil means the capability is absent;
// frontends must degrade without panic.
type Workflows interface {
	// List returns summaries in stable load order.
	List() []WorkflowSummary
	// Get returns one summary by name.
	Get(name string) (WorkflowSummary, bool)
	// Document returns a full editable document for a catalog entry.
	// Builtins and disk-backed entries are both editable drafts; Save writes
	// a scoped override and never activates.
	Document(name string) (WorkflowDocument, bool)
	// Scaffold returns a minimal valid draft for name without writing disk.
	Scaffold(name string) (WorkflowDocument, error)
	// Validate runs structural (+ known-agent) checks. A nil error means the
	// document may be saved. Multi-error text is returned as err.Error().
	Validate(doc WorkflowDocument) error
	// Format returns canonical pretty-printed JSON (schema fields only).
	Format(doc WorkflowDocument) (string, error)
	// PhaseGrants returns the phase permission rules that would be reviewed
	// at activation (same surface as start-preview / CLI inspect grants).
	// Empty when the phase has no permission overrides.
	PhaseGrants(doc WorkflowDocument, phaseIndex int) []WorkflowPermission
	// Save validates then atomically writes doc to scope (global|project).
	// force overwrites an existing file. Never activates the workflow.
	// On success the in-memory catalog is updated so List/Get/Document see
	// the new definition. Returns the absolute path written.
	Save(doc WorkflowDocument, scope string, force bool) (path string, err error)
}

// WorkflowPhaseDraftReview is one phase in a host-safe draft review.
type WorkflowPhaseDraftReview struct {
	Name             string
	Description      string
	Agent            string
	Context          string
	Gate             string // agent | user | check
	GateCommand      string
	CheckHighlighted bool
	Permissions      []WorkflowPermission
	// Widening is effective grant delta vs the host baseline (allow/ask upgrades).
	Widening []WorkflowPermission
}

// WorkflowDraftReview is a structured review of an in-memory workflow draft.
// Frontends must not treat a draft as activated configuration.
type WorkflowDraftReview struct {
	Name            string
	Description     string
	SourceLabel     string
	Valid           bool
	ValidationError string
	Fingerprint     string
	HasChecks       bool
	HasWidening     bool
	Phases          []WorkflowPhaseDraftReview
	// CanonicalJSON is pretty-printed JSON when Valid; otherwise raw input.
	CanonicalJSON string
}

// WorkflowDraftSave is the result of an accepted draft save.
type WorkflowDraftSave struct {
	Path string
	// Activated is always false — saves never start a workflow.
	Activated bool
}

// WorkflowDrafts reviews and saves in-memory workflow drafts without activating
// them. Nil means the capability is absent; frontends must degrade without panic.
// Model generation may live in the CLI; this surface is the shared validate /
// review / save contract for TUI and web (#718/#719).
type WorkflowDrafts interface {
	// Review parses JSON into a draft and returns a structured review.
	// Invalid input still returns a review with Valid=false and diagnostics.
	Review(jsonDocument string) WorkflowDraftReview
	// Save validates and atomically writes a draft to scope ("global" or
	// "project"). confirm must be true. force is required to overwrite.
	// Invalid drafts and missing confirmation return an error without writing.
	// On failure the prior file (if any) is preserved. Never activates.
	Save(jsonDocument string, scope string, confirm, force bool) (WorkflowDraftSave, error)
}
