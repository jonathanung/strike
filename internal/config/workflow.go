package config

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jonathanung/strike-cli/internal/permission"
)

// WorkflowSchemaVersion is the current on-disk workflow document version.
// Parsers accept missing/0 as v1 for backward compatibility; Format always
// emits the current version. Newer unknown versions are rejected.
const WorkflowSchemaVersion = 1

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

// WorkflowSource is where a loaded workflow definition came from.
type WorkflowSource string

const (
	// WorkflowSourceBuiltin is a shipped definition (not on disk).
	WorkflowSourceBuiltin WorkflowSource = "builtin"
	// WorkflowSourceGlobal is ~/.strike/workflows.
	WorkflowSourceGlobal WorkflowSource = "global"
	// WorkflowSourceProject is <project>/.strike/workflows.
	WorkflowSourceProject WorkflowSource = "project"
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
//
// On-disk JSON is the linear schema (schemaVersion, name, description, phases).
// Source, Path, and Fingerprint are runtime diagnostics only (json:"-") and are
// never written by FormatWorkflow.
type Workflow struct {
	SchemaVersion int     `json:"schemaVersion,omitempty"`
	Name          string  `json:"name"`
	Description   string  `json:"description,omitempty"`
	Phases        []Phase `json:"phases"`

	// Source is builtin|global|project after LoadWorkflows / LoadWorkflowFile.
	Source WorkflowSource `json:"-"`
	// Path is the absolute file path for disk-backed workflows.
	Path string `json:"-"`
	// Fingerprint is the hex SHA-256 of the canonical formatted document.
	Fingerprint string `json:"-"`
}

// WorkflowError is one validation or decode finding with an optional source
// location (file path or builtin id) and JSON-path-ish field path.
type WorkflowError struct {
	Source string // e.g. "/path/to/x.json" or "builtin:plan-implement"
	Path   string // e.g. "phases[0].exit.command"
	Msg    string
}

func (e WorkflowError) Error() string {
	var b strings.Builder
	if e.Source != "" {
		b.WriteString(e.Source)
	}
	if e.Path != "" {
		if b.Len() > 0 {
			b.WriteString(": ")
		}
		b.WriteString(e.Path)
	}
	if b.Len() > 0 {
		b.WriteString(": ")
	}
	b.WriteString(e.Msg)
	return b.String()
}

// WorkflowErrors is a multi-error collection from one validation pass.
type WorkflowErrors []WorkflowError

func (es WorkflowErrors) Error() string {
	if len(es) == 0 {
		return ""
	}
	if len(es) == 1 {
		return es[0].Error()
	}
	parts := make([]string, 0, len(es))
	for _, e := range es {
		parts = append(parts, e.Error())
	}
	return fmt.Sprintf("%d workflow errors:\n  - %s", len(es), strings.Join(parts, "\n  - "))
}

// BuiltinPlanImplement is the shipped plan→implement workflow: a read-only
// plan phase (hard-deny write/edit) with a user exit gate, then implement.
func BuiltinPlanImplement() Workflow {
	return annotateBuiltin(Workflow{
		SchemaVersion: WorkflowSchemaVersion,
		Name:          "plan-implement",
		Description:   "Read-only planning phase, then implementation",
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
	})
}

// BuiltinReviewFix is review→fix: read-only reviewer phase (user gate),
// then build fixes with a check gate (make test when present in the project).
func BuiltinReviewFix() Workflow {
	return annotateBuiltin(Workflow{
		SchemaVersion: WorkflowSchemaVersion,
		Name:          "review-fix",
		Description:   "Read-only review, then fix until tests pass",
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
	})
}

func annotateBuiltin(w Workflow) Workflow {
	w.Source = WorkflowSourceBuiltin
	w.Path = ""
	w.SchemaVersion = WorkflowSchemaVersion
	normalizeWorkflow(&w)
	w.Fingerprint = MustWorkflowFingerprint(w)
	return w
}

// BuiltinWorkflows returns shipped workflows in stable order.
func BuiltinWorkflows() []Workflow {
	return []Workflow{BuiltinPlanImplement(), BuiltinReviewFix()}
}

// ScaffoldWorkflow returns a minimal valid workflow template for name.
// The result is never activated; callers must write it and the user must
// start it explicitly (catalog / tools).
func ScaffoldWorkflow(name string) (Workflow, error) {
	name = strings.TrimSpace(name)
	if err := ValidateWorkflowName(name); err != nil {
		return Workflow{}, err
	}
	w := Workflow{
		SchemaVersion: WorkflowSchemaVersion,
		Name:          name,
		Description:   "TODO: describe this workflow",
		Phases: []Phase{
			{
				Name:        "step-one",
				Description: "TODO: first phase",
				Agent:       "build",
				Exit:        ExitGate{Type: GateAgent},
			},
		},
	}
	normalizeWorkflow(&w)
	if err := ValidateWorkflow(w); err != nil {
		return Workflow{}, err
	}
	w.Fingerprint = MustWorkflowFingerprint(w)
	return w, nil
}

// ValidateWorkflowName rejects empty or unsafe workflow identifiers.
func ValidateWorkflowName(name string) error {
	if err := validateConfigIdentifier(name, "workflow"); err != nil {
		return err
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("workflow name %q has leading or trailing whitespace", name)
	}
	return nil
}

// ValidatePhaseName rejects empty or unsafe phase identifiers.
func ValidatePhaseName(name string) error {
	if err := validateConfigIdentifier(name, "phase"); err != nil {
		return err
	}
	if strings.TrimSpace(name) != name {
		return fmt.Errorf("phase name %q has leading or trailing whitespace", name)
	}
	return nil
}

// ValidateWorkflow checks schema version, identifiers, phases, permissions,
// and exit gates. It reports every actionable structural error in one pass.
// Agent references are not checked here — use ValidateWorkflowAgents after
// agents are loaded.
func ValidateWorkflow(w Workflow) error {
	return validateWorkflow(w, "")
}

func validateWorkflow(w Workflow, source string) error {
	var errs WorkflowErrors
	add := func(path, msg string) {
		errs = append(errs, WorkflowError{Source: source, Path: path, Msg: msg})
	}

	ver := w.SchemaVersion
	if ver == 0 {
		ver = WorkflowSchemaVersion
	}
	if ver < 1 {
		add("schemaVersion", fmt.Sprintf("must be >= 1, got %d", w.SchemaVersion))
	} else if ver > WorkflowSchemaVersion {
		add("schemaVersion", fmt.Sprintf("unsupported version %d (max %d)", ver, WorkflowSchemaVersion))
	}

	if err := ValidateWorkflowName(w.Name); err != nil {
		add("name", err.Error())
	}

	if len(w.Phases) == 0 {
		add("phases", "workflow has no phases")
	}

	seen := map[string]int{}
	for i, p := range w.Phases {
		base := fmt.Sprintf("phases[%d]", i)
		if err := ValidatePhaseName(p.Name); err != nil {
			add(base+".name", err.Error())
		} else if prev, ok := seen[p.Name]; ok {
			add(base+".name", fmt.Sprintf("duplicate phase %q (also phases[%d])", p.Name, prev))
		} else {
			seen[p.Name] = i
		}
		if agent := strings.TrimSpace(p.Agent); agent != "" {
			if agent != p.Agent {
				add(base+".agent", fmt.Sprintf("agent name %q has leading or trailing whitespace", p.Agent))
			} else if err := ValidateAgentName(agent); err != nil {
				add(base+".agent", err.Error())
			}
		}
		if err := permission.ValidateRuleset(p.Permissions); err != nil {
			add(base+".permissions", err.Error())
		}
		if err := validateExitGate(p.Exit); err != nil {
			add(base+".exit", err.Error())
		}
	}

	if len(errs) == 0 {
		return nil
	}
	return errs
}

// ValidateWorkflowAgents checks that every non-empty phase.Agent exists in
// known. Missing agents are reported in one pass; empty agent pins are allowed
// (keep current persona).
func ValidateWorkflowAgents(w Workflow, known map[string]struct{}) error {
	return validateWorkflowAgents(w, known, "")
}

func validateWorkflowAgents(w Workflow, known map[string]struct{}, source string) error {
	if known == nil {
		return nil
	}
	var errs WorkflowErrors
	for i, p := range w.Phases {
		agent := strings.TrimSpace(p.Agent)
		if agent == "" {
			continue
		}
		if _, ok := known[agent]; !ok {
			errs = append(errs, WorkflowError{
				Source: source,
				Path:   fmt.Sprintf("phases[%d].agent", i),
				Msg:    fmt.Sprintf("unknown agent %q", agent),
			})
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errs
}

// AgentNameSet builds a set of agent names for ValidateWorkflowAgents.
func AgentNameSet(agents []Agent) map[string]struct{} {
	out := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		if a.Name != "" {
			out[a.Name] = struct{}{}
		}
	}
	return out
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

// ParseWorkflow decodes a workflow JSON document with strict unknown-field
// rejection, normalizes defaults, and runs structural validation.
func ParseWorkflow(data []byte) (Workflow, error) {
	return ParseWorkflowSource(data, "")
}

// ParseWorkflowSource is ParseWorkflow with a source label for error locations.
func ParseWorkflowSource(data []byte, source string) (Workflow, error) {
	var w Workflow
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		msg := err.Error()
		// encoding/json unknown-field errors are already descriptive.
		return Workflow{}, WorkflowError{Source: source, Msg: "parse workflow: " + msg}
	}
	// Reject trailing junk after the value.
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return Workflow{}, WorkflowError{Source: source, Msg: "parse workflow: trailing data after JSON value"}
		}
		return Workflow{}, WorkflowError{Source: source, Msg: "parse workflow: " + err.Error()}
	}
	normalizeWorkflow(&w)
	if err := validateWorkflow(w, source); err != nil {
		return Workflow{}, err
	}
	w.Fingerprint = MustWorkflowFingerprint(w)
	return w, nil
}

func normalizeWorkflow(w *Workflow) {
	if w.SchemaVersion == 0 {
		w.SchemaVersion = WorkflowSchemaVersion
	}
	for i := range w.Phases {
		if w.Phases[i].Exit.Type == "" {
			w.Phases[i].Exit.Type = GateAgent
		}
	}
}

// FormatWorkflow returns deterministic pretty-printed JSON for w. Runtime
// fields (Source, Path, Fingerprint) are omitted. Empty optional fields use
// omitempty. A trailing newline is always included.
func FormatWorkflow(w Workflow) ([]byte, error) {
	// Work on a copy so we do not mutate the caller's diagnostics.
	c := w
	normalizeWorkflow(&c)
	// Clear runtime-only fields (already json:"-", but keep intent clear).
	c.Source = ""
	c.Path = ""
	c.Fingerprint = ""
	out, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, err
	}
	out = append(out, '\n')
	return out, nil
}

// WorkflowFingerprint returns the hex SHA-256 of FormatWorkflow(w).
func WorkflowFingerprint(w Workflow) (string, error) {
	raw, err := FormatWorkflow(w)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// MustWorkflowFingerprint is WorkflowFingerprint that returns "" on error.
func MustWorkflowFingerprint(w Workflow) string {
	fp, err := WorkflowFingerprint(w)
	if err != nil {
		return ""
	}
	return fp
}

// LoadWorkflowFile reads one workflow JSON file and attaches Path/Source.
// source should be WorkflowSourceGlobal or WorkflowSourceProject; empty leaves
// Source unset (caller may set it).
func LoadWorkflowFile(path string) (Workflow, error) {
	return LoadWorkflowFileSource(path, "")
}

// LoadWorkflowFileSource reads path and labels errors/diagnostics with source.
func LoadWorkflowFileSource(path string, source WorkflowSource) (Workflow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Workflow{}, err
	}
	w, err := ParseWorkflowSource(data, path)
	if err != nil {
		return Workflow{}, err
	}
	w.Path = path
	w.Source = source
	return w, nil
}

// LoadWorkflows reads workflows/*.json from global then project .strike roots.
// Project entries override global ones with the same name. Built-in workflows
// (plan-implement, review-fix) are always present and may be overridden by name.
// Each entry carries Source, Path (when disk-backed), and Fingerprint.
//
// Same-layer duplicate names (two files defining the same workflow name under
// one directory) fail closed with a multi-error. Cross-layer overrides are
// intentional and silent.
//
// Loading never activates a workflow — activation is a separate runtime step.
func LoadWorkflows(workDir string) ([]Workflow, error) {
	byName := map[string]Workflow{}
	order := make([]string, 0, 4)
	for _, w := range BuiltinWorkflows() {
		byName[w.Name] = w
		order = append(order, w.Name)
	}

	type layer struct {
		dir    string
		source WorkflowSource
	}
	var layers []layer
	if root := GlobalRoot(); root != "" {
		layers = append(layers, layer{filepath.Join(root, "workflows"), WorkflowSourceGlobal})
	}
	if workDir != "" {
		if root := projectRoot(workDir); root != "" {
			layers = append(layers, layer{filepath.Join(root, "workflows"), WorkflowSourceProject})
		}
	}

	var allErrs WorkflowErrors
	for _, lay := range layers {
		loaded, errs := readWorkflowDir(lay.dir, lay.source)
		allErrs = append(allErrs, errs...)
		// Apply successful loads even when some files in the dir failed, so
		// callers that only care about valid defs still see them — but if any
		// error occurred we still return the multi-error at the end.
		for _, w := range loaded {
			if _, exists := byName[w.Name]; !exists {
				order = append(order, w.Name)
			}
			byName[w.Name] = w
		}
	}
	if len(allErrs) > 0 {
		return nil, allErrs
	}
	out := make([]Workflow, 0, len(order))
	for _, name := range order {
		out = append(out, byName[name])
	}
	return out, nil
}

// readWorkflowDir loads *.json workflows from dir. Duplicate names within the
// directory are reported; the first file in sorted order wins for the returned
// slice when duplicates exist (errors still returned).
func readWorkflowDir(dir string, source WorkflowSource) ([]Workflow, WorkflowErrors) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	// ReadDir order is already sorted on most platforms; sort for determinism.
	// names is already from ReadDir which returns sorted entries.
	var (
		out    []Workflow
		errs   WorkflowErrors
		byName = map[string]string{} // name → first path
	)
	for _, name := range names {
		path := filepath.Join(dir, name)
		w, err := LoadWorkflowFileSource(path, source)
		if err != nil {
			errs = append(errs, asWorkflowErrors(err)...)
			continue
		}
		if prev, ok := byName[w.Name]; ok {
			errs = append(errs, WorkflowError{
				Source: path,
				Path:   "name",
				Msg:    fmt.Sprintf("duplicate workflow %q (also defined in %s)", w.Name, prev),
			})
			continue
		}
		byName[w.Name] = path
		out = append(out, w)
	}
	return out, errs
}

func asWorkflowErrors(err error) WorkflowErrors {
	if err == nil {
		return nil
	}
	if es, ok := err.(WorkflowErrors); ok {
		return es
	}
	if e, ok := err.(WorkflowError); ok {
		return WorkflowErrors{e}
	}
	return WorkflowErrors{{Msg: err.Error()}}
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

// WorkflowDir returns the workflows directory for scope ("global" or "project").
// workDir is required for project scope.
func WorkflowDir(scope, workDir string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "global":
		root := GlobalRoot()
		if root == "" {
			return "", fmt.Errorf("cannot resolve global .strike root")
		}
		return filepath.Join(root, "workflows"), nil
	case "project":
		if strings.TrimSpace(workDir) == "" {
			return "", fmt.Errorf("project scope requires a work directory")
		}
		root := projectRoot(workDir)
		if root == "" {
			return "", fmt.Errorf("cannot resolve project .strike root")
		}
		return filepath.Join(root, "workflows"), nil
	default:
		return "", fmt.Errorf("unknown workflow scope %q (want global or project)", scope)
	}
}

// WriteWorkflowFile formats w and writes it to path. Parents are created.
// If path exists and force is false, returns an error without writing.
// Writing never activates the workflow.
func WriteWorkflowFile(path string, w Workflow, force bool) error {
	if path == "" {
		return fmt.Errorf("empty workflow path")
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("%s already exists (pass --force to overwrite)", path)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	raw, err := FormatWorkflow(w)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
