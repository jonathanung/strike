package local

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/jonathanung/strike-cli/harness/permission"
	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/product/config"
)

// WorkflowsOpts configures a mutable workflow catalog with authoring support.
type WorkflowsOpts struct {
	// WorkDir is the project working directory (required for project-scope Save).
	WorkDir string
	// Agents are known agent names for ValidateWorkflowAgents (optional).
	Agents []string
}

// NewWorkflows adapts loaded config workflows to the host.Workflows catalog.
// A nil or empty list yields a non-nil empty catalog (List returns nil slice).
// Callers that lack workflow support should leave Services.Workflows nil.
//
// Without opts, Save to project scope fails (no work dir) and agent validation
// is skipped. Prefer NewWorkflowsWithOpts from the composition root.
func NewWorkflows(list []config.Workflow) host.Workflows {
	return NewWorkflowsWithOpts(list, nil, WorkflowsOpts{})
}

// NewWorkflowsWithOpts is NewWorkflows plus workDir/agents for authoring.
func NewWorkflowsWithOpts(list []config.Workflow, invalid []host.WorkflowSummary, opts WorkflowsOpts) host.Workflows {
	c := &workflowsCatalog{
		workDir: strings.TrimSpace(opts.WorkDir),
		agents:  agentSet(opts.Agents),
		docs:    make(map[string]host.WorkflowDocument),
	}
	for _, w := range list {
		sum := workflowToSummary(w)
		c.items = append(c.items, sum)
		c.docs[w.Name] = workflowToDocument(w)
	}
	for _, w := range invalid {
		if strings.TrimSpace(w.Name) == "" {
			continue
		}
		w.Valid = false
		// Avoid duplicate names: invalid loses to already-loaded valid.
		if _, exists := c.docs[w.Name]; exists {
			continue
		}
		c.items = append(c.items, w)
		c.docs[w.Name] = summaryToDocument(w)
	}
	return c
}

// NewWorkflowsWithErrors builds a catalog that includes both valid loaded
// definitions and invalid entries (Valid=false) so the UX can show validation
// state without offering activation.
func NewWorkflowsWithErrors(valid []config.Workflow, invalid []host.WorkflowSummary) host.Workflows {
	return NewWorkflowsWithOpts(valid, invalid, WorkflowsOpts{})
}

func agentSet(names []string) map[string]struct{} {
	if len(names) == 0 {
		return nil
	}
	out := make(map[string]struct{}, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		if n != "" {
			out[n] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

type workflowsCatalog struct {
	mu      sync.Mutex
	items   []host.WorkflowSummary
	docs    map[string]host.WorkflowDocument
	workDir string
	agents  map[string]struct{}
}

func (c *workflowsCatalog) List() []host.WorkflowSummary {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.items) == 0 {
		return nil
	}
	out := make([]host.WorkflowSummary, len(c.items))
	copy(out, c.items)
	return out
}

func (c *workflowsCatalog) Get(name string) (host.WorkflowSummary, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
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

func (c *workflowsCatalog) Document(name string) (host.WorkflowDocument, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name = strings.TrimSpace(name)
	if name == "" {
		return host.WorkflowDocument{}, false
	}
	doc, ok := c.docs[name]
	if !ok {
		return host.WorkflowDocument{}, false
	}
	return cloneDocument(doc), true
}

func (c *workflowsCatalog) Scaffold(name string) (host.WorkflowDocument, error) {
	w, err := config.ScaffoldWorkflow(name)
	if err != nil {
		return host.WorkflowDocument{}, err
	}
	return workflowToDocument(w), nil
}

func (c *workflowsCatalog) Validate(doc host.WorkflowDocument) error {
	w, err := documentToWorkflow(doc)
	if err != nil {
		return err
	}
	if err := config.ValidateWorkflow(w); err != nil {
		return err
	}
	c.mu.Lock()
	agents := c.agents
	c.mu.Unlock()
	if err := config.ValidateWorkflowAgents(w, agents); err != nil {
		return err
	}
	return nil
}

func (c *workflowsCatalog) Format(doc host.WorkflowDocument) (string, error) {
	w, err := documentToWorkflow(doc)
	if err != nil {
		return "", err
	}
	raw, err := config.FormatWorkflow(w)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func (c *workflowsCatalog) PhaseGrants(doc host.WorkflowDocument, phaseIndex int) []host.WorkflowPermission {
	if phaseIndex < 0 || phaseIndex >= len(doc.Phases) {
		return nil
	}
	perms := doc.Phases[phaseIndex].Permissions
	if len(perms) == 0 {
		return nil
	}
	out := make([]host.WorkflowPermission, len(perms))
	copy(out, perms)
	return out
}

func (c *workflowsCatalog) Save(doc host.WorkflowDocument, scope string, force bool) (string, error) {
	scope = strings.ToLower(strings.TrimSpace(scope))
	switch scope {
	case host.WorkflowScopeGlobal, host.WorkflowScopeProject:
	default:
		return "", fmt.Errorf("unknown workflow scope %q (want global or project)", scope)
	}
	if err := c.Validate(doc); err != nil {
		return "", fmt.Errorf("%w: %v", host.ErrWorkflowInvalid, err)
	}
	w, err := documentToWorkflow(doc)
	if err != nil {
		return "", fmt.Errorf("%w: %v", host.ErrWorkflowInvalid, err)
	}

	c.mu.Lock()
	workDir := c.workDir
	c.mu.Unlock()

	dir, err := config.WorkflowDir(scope, workDir)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, w.Name+".json")
	if !force {
		if _, statErr := os.Stat(path); statErr == nil {
			return "", fmt.Errorf("%w: %s", host.ErrWorkflowExists, path)
		} else if !os.IsNotExist(statErr) {
			return "", statErr
		}
	}
	if err := config.WriteWorkflowFile(path, w, force); err != nil {
		// Map overwrite message to typed error when force was false.
		if !force && strings.Contains(err.Error(), "already exists") {
			return "", fmt.Errorf("%w: %s", host.ErrWorkflowExists, path)
		}
		return "", err
	}

	// Reload written file so fingerprint/path/source match disk.
	loaded, err := config.LoadWorkflowFileSource(path, config.WorkflowSource(scope))
	if err != nil {
		// Disk write succeeded; still surface catalog from what we wrote.
		loaded = w
		loaded.Path = path
		loaded.Source = config.WorkflowSource(scope)
		loaded.Fingerprint = config.MustWorkflowFingerprint(w)
	}
	c.upsertLocked(loaded)
	return path, nil
}

func (c *workflowsCatalog) upsertLocked(w config.Workflow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	sum := workflowToSummary(w)
	doc := workflowToDocument(w)
	if c.docs == nil {
		c.docs = make(map[string]host.WorkflowDocument)
	}
	c.docs[w.Name] = doc
	for i, item := range c.items {
		if item.Name == w.Name {
			c.items[i] = sum
			return
		}
	}
	c.items = append(c.items, sum)
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

func workflowToDocument(w config.Workflow) host.WorkflowDocument {
	doc := host.WorkflowDocument{
		SchemaVersion: w.SchemaVersion,
		Name:          w.Name,
		Description:   w.Description,
	}
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = config.WorkflowSchemaVersion
	}
	doc.Phases = make([]host.WorkflowPhaseDocument, 0, len(w.Phases))
	for _, p := range w.Phases {
		gate := string(p.Exit.Type)
		if gate == "" {
			gate = string(config.GateAgent)
		}
		pd := host.WorkflowPhaseDocument{
			Name:        p.Name,
			Description: p.Description,
			Agent:       p.Agent,
			Context:     p.Context,
			Gate:        gate,
			GateCommand: p.Exit.Command,
		}
		if len(p.Permissions) > 0 {
			pd.Permissions = make([]host.WorkflowPermission, 0, len(p.Permissions))
			for _, r := range p.Permissions {
				pd.Permissions = append(pd.Permissions, host.WorkflowPermission{
					Permission: r.Permission,
					Pattern:    r.Pattern,
					Action:     string(r.Action),
				})
			}
		}
		doc.Phases = append(doc.Phases, pd)
	}
	return doc
}

func summaryToDocument(s host.WorkflowSummary) host.WorkflowDocument {
	doc := host.WorkflowDocument{
		SchemaVersion: config.WorkflowSchemaVersion,
		Name:          s.Name,
		Description:   s.Description,
	}
	doc.Phases = make([]host.WorkflowPhaseDocument, 0, len(s.Phases))
	for _, p := range s.Phases {
		gate := p.Gate
		if gate == "" {
			gate = string(config.GateAgent)
		}
		pd := host.WorkflowPhaseDocument{
			Name:        p.Name,
			Description: p.Description,
			Agent:       p.Agent,
			Gate:        gate,
			GateCommand: p.GateCommand,
		}
		if len(p.Permissions) > 0 {
			pd.Permissions = append([]host.WorkflowPermission(nil), p.Permissions...)
		}
		doc.Phases = append(doc.Phases, pd)
	}
	return doc
}

func documentToWorkflow(doc host.WorkflowDocument) (config.Workflow, error) {
	w := config.Workflow{
		SchemaVersion: doc.SchemaVersion,
		Name:          strings.TrimSpace(doc.Name),
		Description:   doc.Description,
	}
	if w.SchemaVersion == 0 {
		w.SchemaVersion = config.WorkflowSchemaVersion
	}
	w.Phases = make([]config.Phase, 0, len(doc.Phases))
	for i, p := range doc.Phases {
		gate := config.GateType(strings.TrimSpace(p.Gate))
		if gate == "" {
			gate = config.GateAgent
		}
		phase := config.Phase{
			Name:        p.Name,
			Description: p.Description,
			Agent:       p.Agent,
			Context:     p.Context,
			Exit: config.ExitGate{
				Type:    gate,
				Command: p.GateCommand,
			},
		}
		if len(p.Permissions) > 0 {
			rs := make(permission.Ruleset, 0, len(p.Permissions))
			for j, r := range p.Permissions {
				action := permission.Action(strings.TrimSpace(r.Action))
				if !permission.ValidAction(action) {
					return config.Workflow{}, fmt.Errorf("phases[%d].permissions[%d]: invalid action %q", i, j, r.Action)
				}
				rs = append(rs, permission.Rule{
					Permission: strings.TrimSpace(r.Permission),
					Pattern:    r.Pattern,
					Action:     action,
				})
			}
			phase.Permissions = rs
		}
		w.Phases = append(w.Phases, phase)
	}
	return w, nil
}

func cloneDocument(doc host.WorkflowDocument) host.WorkflowDocument {
	out := doc
	if len(doc.Phases) == 0 {
		out.Phases = nil
		return out
	}
	out.Phases = make([]host.WorkflowPhaseDocument, len(doc.Phases))
	for i, p := range doc.Phases {
		out.Phases[i] = p
		if len(p.Permissions) > 0 {
			out.Phases[i].Permissions = append([]host.WorkflowPermission(nil), p.Permissions...)
		}
	}
	return out
}
