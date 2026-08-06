package local

import (
	"strings"

	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/host"
)

// NewWorkflows adapts loaded config workflows to the host.Workflows catalog.
// A nil or empty list yields a non-nil empty catalog (List returns nil slice).
// Callers that lack workflow support should leave Services.Workflows nil.
func NewWorkflows(list []config.Workflow) host.Workflows {
	items := make([]host.WorkflowSummary, 0, len(list))
	for _, w := range list {
		items = append(items, workflowToSummary(w))
	}
	return workflowsCatalog{items: items}
}

// NewWorkflowsWithErrors builds a catalog that includes both valid loaded
// definitions and invalid entries (Valid=false) so the UX can show validation
// state without offering activation.
func NewWorkflowsWithErrors(valid []config.Workflow, invalid []host.WorkflowSummary) host.Workflows {
	items := make([]host.WorkflowSummary, 0, len(valid)+len(invalid))
	for _, w := range valid {
		items = append(items, workflowToSummary(w))
	}
	for _, w := range invalid {
		if strings.TrimSpace(w.Name) == "" {
			continue
		}
		w.Valid = false
		items = append(items, w)
	}
	return workflowsCatalog{items: items}
}

type workflowsCatalog struct {
	items []host.WorkflowSummary
}

func (c workflowsCatalog) List() []host.WorkflowSummary {
	if len(c.items) == 0 {
		return nil
	}
	out := make([]host.WorkflowSummary, len(c.items))
	copy(out, c.items)
	return out
}

func (c workflowsCatalog) Get(name string) (host.WorkflowSummary, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return host.WorkflowSummary{}, false
	}
	for _, w := range c.items {
		if w.Name == name {
			return w, true
		}
	}
	return host.WorkflowSummary{}, false
}

func workflowToSummary(w config.Workflow) host.WorkflowSummary {
	src := string(w.Source)
	if src == "" {
		src = host.WorkflowSourceBuiltin
	}
	sum := host.WorkflowSummary{
		Name:        w.Name,
		Description: w.Description,
		Source:      src,
		Fingerprint: w.Fingerprint,
		Path:        w.Path,
		Valid:       true,
	}
	// Defensive: surface structural problems even if the loader already
	// validated — activation still re-checks in the engine.
	if err := config.ValidateWorkflow(w); err != nil {
		sum.Valid = false
		sum.ValidationError = err.Error()
	}
	sum.Phases = make([]host.WorkflowPhaseSummary, 0, len(w.Phases))
	for _, p := range w.Phases {
		gate := string(p.Exit.Type)
		if gate == "" {
			gate = string(config.GateAgent)
		}
		ps := host.WorkflowPhaseSummary{
			Name:        p.Name,
			Description: p.Description,
			Agent:       p.Agent,
			Gate:        gate,
			GateCommand: p.Exit.Command,
		}
		if len(p.Permissions) > 0 {
			ps.Permissions = make([]host.WorkflowPermission, 0, len(p.Permissions))
			for _, r := range p.Permissions {
				ps.Permissions = append(ps.Permissions, host.WorkflowPermission{
					Permission: r.Permission,
					Pattern:    r.Pattern,
					Action:     string(r.Action),
				})
			}
		}
		sum.Phases = append(sum.Phases, ps)
	}
	return sum
}
