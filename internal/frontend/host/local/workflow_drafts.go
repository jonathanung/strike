package local

import (
	"fmt"
	"strings"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/internal/config"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

// NewWorkflowDrafts builds a host.WorkflowDrafts service bound to workDir for
// project-scoped saves and agent validation. workDir may be empty (global-only
// saves still work; agent resolution uses builtins + global).
func NewWorkflowDrafts(workDir string) host.WorkflowDrafts {
	return workflowDrafts{workDir: workDir}
}

type workflowDrafts struct {
	workDir string
}

func (w workflowDrafts) Review(jsonDocument string) host.WorkflowDraftReview {
	known := w.knownAgents()
	d := config.DraftFromJSON([]byte(jsonDocument), "draft")
	d.Revalidate(known)
	rev := config.ReviewWorkflowDraft(d, config.DraftReviewOpts{
		Baseline:    []permission.Ruleset{permission.Defaults()},
		KnownAgents: known,
	})
	out := host.WorkflowDraftReview{
		Name:        rev.Name,
		Description: rev.Description,
		SourceLabel: rev.SourceLabel,
		Valid:       rev.Valid,
		Fingerprint: rev.Fingerprint,
		HasChecks:   rev.HasChecks,
		HasWidening: rev.HasWidening,
	}
	if len(rev.Diagnostics) > 0 {
		out.ValidationError = rev.Diagnostics.Error()
	}
	if rev.Valid {
		if raw, err := config.FormatWorkflow(d.Workflow); err == nil {
			out.CanonicalJSON = string(raw)
		}
	} else if len(d.Raw) > 0 {
		out.CanonicalJSON = string(d.Raw)
	}
	out.Phases = make([]host.WorkflowPhaseDraftReview, 0, len(rev.Phases))
	for _, p := range rev.Phases {
		ps := host.WorkflowPhaseDraftReview{
			Name:             p.Name,
			Description:      p.Description,
			Agent:            p.Agent,
			Context:          p.Context,
			Gate:             p.Gate,
			GateCommand:      p.GateCommand,
			CheckHighlighted: p.CheckHighlighted,
		}
		for _, r := range p.Permissions {
			ps.Permissions = append(ps.Permissions, host.WorkflowPermission{
				Permission: r.Permission,
				Pattern:    r.Pattern,
				Action:     string(r.Action),
			})
		}
		for _, r := range p.Widening {
			ps.Widening = append(ps.Widening, host.WorkflowPermission{
				Permission: r.Permission,
				Pattern:    r.Pattern,
				Action:     string(r.Action),
			})
		}
		out.Phases = append(out.Phases, ps)
	}
	return out
}

func (w workflowDrafts) Save(jsonDocument string, scope string, confirm, force bool) (host.WorkflowDraftSave, error) {
	if !confirm {
		return host.WorkflowDraftSave{}, config.ErrSaveNotConfirmed
	}
	known := w.knownAgents()
	d := config.DraftFromJSON([]byte(jsonDocument), "draft")
	d.Revalidate(known)
	if !d.Valid() {
		if len(d.Diagnostics) > 0 {
			return host.WorkflowDraftSave{}, fmt.Errorf("%w: %s", config.ErrDraftInvalid, d.Diagnostics.Error())
		}
		return host.WorkflowDraftSave{}, config.ErrDraftInvalid
	}
	path, err := config.SaveWorkflowDraft(d, config.SaveDraftOpts{
		Scope:   strings.ToLower(strings.TrimSpace(scope)),
		WorkDir: w.workDir,
		Force:   force,
		Confirm: true,
	})
	if err != nil {
		return host.WorkflowDraftSave{}, err
	}
	return host.WorkflowDraftSave{Path: path, Activated: false}, nil
}

func (w workflowDrafts) knownAgents() map[string]struct{} {
	agents, err := config.LoadAgentsWithError(w.workDir)
	if err != nil {
		// Fall back to empty set — structural validation still runs.
		return nil
	}
	return config.AgentNameSet(agents)
}
