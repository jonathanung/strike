package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
)

// GateType is who/what clears a phase exit gate.
type GateType string

const (
	// GateAgent advances when the model calls phase_done (default).
	GateAgent GateType = "agent"
	// GateCheck advances when Command exits 0.
	GateCheck GateType = "check"
	// GateUser advances after an explicit user approval prompt.
	GateUser GateType = "user"
)

// ExitGate declares how a phase is cleared before the next phase loads.
type ExitGate struct {
	Type    GateType `json:"type"`
	Command string   `json:"command,omitempty"` // required when Type is check
}

// Phase is one step in a workflow: optional persona pin, prompt context,
// permission profile, and exit gate.
type Phase struct {
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Agent       string             `json:"agent,omitempty"`
	Context     string             `json:"context,omitempty"`
	Permissions permission.Ruleset `json:"permissions,omitempty"`
	Exit        ExitGate           `json:"exit"`
}

// Workflow is an ordered sequence of phases. When a phase gate clears, its
// context is dropped and the next phase's context and permission profile load.
type Workflow struct {
	Name        string  `json:"name"`
	Description string  `json:"description,omitempty"`
	Phases      []Phase `json:"phases"`
}

// BuiltinPlanImplement is the shipped plan→implement workflow: a read-only
// plan phase (hard-deny write/edit) with a user exit gate, then implement.
func BuiltinPlanImplement() Workflow {
	return Workflow{
		Name:        "plan-implement",
		Description: "Read-only planning phase, then implementation",
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

// ValidateWorkflow checks name, phases, permissions, and exit gates.
func ValidateWorkflow(w Workflow) error {
	if strings.TrimSpace(w.Name) == "" {
		return fmt.Errorf("workflow name is empty")
	}
	if len(w.Phases) == 0 {
		return fmt.Errorf("workflow %q has no phases", w.Name)
	}
	seen := map[string]struct{}{}
	for i, p := range w.Phases {
		name := strings.TrimSpace(p.Name)
		if name == "" {
			return fmt.Errorf("workflow %q phase %d: empty name", w.Name, i)
		}
		if _, ok := seen[name]; ok {
			return fmt.Errorf("workflow %q: duplicate phase %q", w.Name, name)
		}
		seen[name] = struct{}{}
		if err := permission.ValidateRuleset(p.Permissions); err != nil {
			return fmt.Errorf("workflow %q phase %q permissions: %w", w.Name, name, err)
		}
		if err := validateExitGate(p.Exit); err != nil {
			return fmt.Errorf("workflow %q phase %q exit: %w", w.Name, name, err)
		}
	}
	return nil
}

func validateExitGate(g ExitGate) error {
	switch g.Type {
	case GateAgent, "":
		return nil
	case GateUser:
		return nil
	case GateCheck:
		if strings.TrimSpace(g.Command) == "" {
			return fmt.Errorf("check gate requires command")
		}
		return nil
	default:
		return fmt.Errorf("unknown gate type %q (want agent, check, or user)", g.Type)
	}
}

// ParseWorkflow decodes and validates a workflow JSON document.
func ParseWorkflow(data []byte) (Workflow, error) {
	var w Workflow
	if err := json.Unmarshal(data, &w); err != nil {
		return Workflow{}, fmt.Errorf("parse workflow: %w", err)
	}
	// Empty exit type defaults to agent.
	for i := range w.Phases {
		if w.Phases[i].Exit.Type == "" {
			w.Phases[i].Exit.Type = GateAgent
		}
	}
	if err := ValidateWorkflow(w); err != nil {
		return Workflow{}, err
	}
	return w, nil
}

// LoadWorkflowFile reads one workflow JSON file.
func LoadWorkflowFile(path string) (Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Workflow{}, err
	}
	w, err := ParseWorkflow(data)
	if err != nil {
		return Workflow{}, fmt.Errorf("%s: %w", path, err)
	}
	return w, nil
}

// LoadWorkflows reads workflows/*.json from global then project .strike roots.
// Project entries override global ones with the same name. The built-in
// plan-implement workflow is always present and may be overridden by name.
func LoadWorkflows(workDir string) ([]Workflow, error) {
	byName := map[string]Workflow{
		"plan-implement": BuiltinPlanImplement(),
	}
	order := []string{"plan-implement"}
	for _, dir := range []string{
		filepath.Join(GlobalRoot(), "workflows"),
		filepath.Join(projectRoot(workDir), "workflows"),
	} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			path := filepath.Join(dir, e.Name())
			w, err := LoadWorkflowFile(path)
			if err != nil {
				return nil, err
			}
			if _, exists := byName[w.Name]; !exists {
				order = append(order, w.Name)
			}
			byName[w.Name] = w
		}
	}
	out := make([]Workflow, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}

// FindWorkflow returns the named workflow from the list.
func FindWorkflow(workflows []Workflow, name string) (Workflow, bool) {
	for _, w := range workflows {
		if w.Name == name {
			return w, true
		}
	}
	return Workflow{}, false
}
