package engine

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
)

// WorkflowSchemaVersion is the kernel workflow document version.
const WorkflowSchemaVersion = 1

// GateType is who/what clears a phase exit gate.
type GateType string

const (
	GateAgent GateType = "agent"
	GateCheck GateType = "check"
	GateUser  GateType = "user"
)

// WorkflowSource is where a loaded workflow definition came from.
type WorkflowSource string

const (
	WorkflowSourceBuiltin WorkflowSource = "builtin"
	WorkflowSourceGlobal  WorkflowSource = "global"
	WorkflowSourceProject WorkflowSource = "project"
	WorkflowSourcePlugin  WorkflowSource = "plugin"
)

// ExitGate declares how a phase is cleared before the next phase loads.
type ExitGate struct {
	Type    GateType
	Command string
}

// Phase is one step in a workflow: optional persona pin, prompt context,
// permission profile, and exit gate.
type Phase struct {
	Name        string
	Description string
	Agent       string
	Context     string
	Permissions permission.Ruleset
	Exit        ExitGate
}

// Workflow is an ordered sequence of phases. Product config workflows are
// converted at the composition root (enginebind.Workflows).
type Workflow struct {
	SchemaVersion int
	Name          string
	Description   string
	Phases        []Phase
	Source        WorkflowSource
	Path          string
	Fingerprint   string
}

// BuiltinPlanImplement is the shipped plan→implement workflow.
func BuiltinPlanImplement() Workflow {
	return Workflow{
		SchemaVersion: WorkflowSchemaVersion,
		Name:          "plan-implement",
		Description:   "Read-only planning phase, then implementation",
		Source:        WorkflowSourceBuiltin,
		Phases: []Phase{
			{
				Name:        "plan",
				Description: "Read-only analysis and planning",
				Agent:       "plan",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
					{Permission: "edit", Pattern: "*", Action: permission.Deny},
				},
				Exit: ExitGate{Type: GateUser},
			},
			{
				Name:        "implement",
				Description: "Execute the approved plan",
				Agent:       "build",
				Exit:        ExitGate{Type: GateAgent},
			},
		},
	}
}

// BuiltinReviewFix is review→fix.
func BuiltinReviewFix() Workflow {
	return Workflow{
		SchemaVersion: WorkflowSchemaVersion,
		Name:          "review-fix",
		Description:   "Read-only review, then fix until tests pass",
		Source:        WorkflowSourceBuiltin,
		Phases: []Phase{
			{
				Name:        "review",
				Description: "Read-only correctness review of the change",
				Agent:       "reviewer",
				Permissions: permission.Ruleset{
					{Permission: "write", Pattern: "*", Action: permission.Deny},
					{Permission: "edit", Pattern: "*", Action: permission.Deny},
				},
				Context: "Review the current branch or named PR. Rank findings; do not edit.",
				Exit:    ExitGate{Type: GateUser},
			},
			{
				Name:        "fix",
				Description: "Address review findings and verify",
				Agent:       "build",
				Context:     "Fix blocking and should-fix review findings. Prefer /verify gates before calling phase_done.",
				Exit:        ExitGate{Type: GateCheck, Command: "make test"},
			},
		},
	}
}

// ValidatePhaseName rejects empty or unsafe phase identifiers.
func ValidatePhaseName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("phase name is empty")
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("phase name %q has leading or trailing whitespace", name)
	}
	if strings.ContainsAny(name, `/\`) {
		return fmt.Errorf("phase name %q contains a path separator", name)
	}
	return nil
}

// ValidateWorkflow checks identifiers, phases, and exit gates. Product loaders
// still run the fuller config validator before injection.
func ValidateWorkflow(w Workflow) error {
	name := strings.TrimSpace(w.Name)
	if name == "" {
		return fmt.Errorf("workflow name is empty")
	}
	if strings.ContainsAny(w.Name, `/\`) {
		return fmt.Errorf("workflow name %q contains a path separator", w.Name)
	}
	if len(w.Phases) == 0 {
		return fmt.Errorf("workflow has no phases")
	}
	seen := map[string]int{}
	for i, p := range w.Phases {
		if err := ValidatePhaseName(p.Name); err != nil {
			return fmt.Errorf("phases[%d]: %w", i, err)
		}
		if prev, ok := seen[p.Name]; ok {
			return fmt.Errorf("phases[%d]: duplicate phase %q (also phases[%d])", i, p.Name, prev)
		}
		seen[p.Name] = i
		if p.Exit.Type == GateCheck && strings.TrimSpace(p.Exit.Command) == "" {
			return fmt.Errorf("phases[%d]: check gate requires command", i)
		}
	}
	return nil
}
