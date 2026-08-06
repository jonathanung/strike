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
