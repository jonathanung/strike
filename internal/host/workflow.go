package host

// Workflow source labels for WorkflowSummary.Source. Values match config
// WorkflowSource strings so frontends can display them without importing
// internal/config.
const (
	WorkflowSourceBuiltin = "builtin"
	WorkflowSourceGlobal  = "global"
	WorkflowSourceProject = "project"
	WorkflowSourcePlugin  = "plugin"
)

// WorkflowPermission is one phase permission rule for catalog display.
// Action is allow|ask|deny.
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

// Workflows is the catalog of loaded workflow definitions. Nil means the
// capability is absent; frontends must degrade without panic.
type Workflows interface {
	// List returns summaries in stable load order.
	List() []WorkflowSummary
	// Get returns one summary by name.
	Get(name string) (WorkflowSummary, bool)
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
