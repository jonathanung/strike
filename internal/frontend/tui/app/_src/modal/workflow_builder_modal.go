package tui

import (
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// workflowBuilderSavedMsg reports a builder Save outcome to the app model.
type workflowBuilderSavedMsg struct {
	name string
	path string
	err  error
	// closeAfter is true when the user confirmed discard-or-save on exit.
	closeAfter bool
}

// workflowBuilderResultMsg is delivered when the builder closes without a
// successful save that already notified (cancel / discard).
type workflowBuilderResultMsg struct {
	canceled bool
	saved    bool
	name     string
}

const (
	wfBuilderFocusMeta   = 0
	wfBuilderFocusPhases = 1
	wfBuilderFocusFields = 2
	wfBuilderFocusPerms  = 3
	wfBuilderFocusJSON   = 4
)

const (
	wfMetaName = iota
	wfMetaDesc
	wfMetaScope
	wfMetaCount
)

const (
	wfFieldName = iota
	wfFieldDesc
	wfFieldAgent
	wfFieldGate
	wfFieldCommand
	wfFieldContext
	wfFieldCount
)

var (
	wfGateChoices   = []string{"agent", "user", "check"}
	wfActionChoices = []string{"allow", "ask", "deny"}
	wfScopeChoices  = []string{host.WorkflowScopeProject, host.WorkflowScopeGlobal}
)

// workflowBuilderModal is the TUI-first linear workflow editor.
// Create/reorder/remove phases; edit agents, context, permissions, check
// commands; preview JSON + validation + phase grants; save to an explicit
// scope without activation.
type workflowBuilderModal struct {
	workflows host.Workflows
	drafts    host.WorkflowDrafts // optional; #717 review path for widening
	agents    []string
	th        theme.Theme

	doc      host.WorkflowDocument
	baseline string // Format(doc) at open / last save — dirty detection
	scope    string // global | project
	creating bool   // new workflow (name editable)

	focus       int // meta | phases | fields | perms | json
	metaCursor  int
	phaseCursor int
	permCursor  int
	permPart    int // 0 permission name, 1 pattern, 2 action
	fieldCursor int
	showPreview bool

	// text editing
	editing  bool
	input    textinput.Model
	editKind string // meta.name|meta.desc|phase.*|perm.permission|perm.pattern

	// overlays
	unsavedPrompt   bool
	overwritePrompt bool
	status          string // transient validation/save feedback
	statusErr       bool
}

const (
	wfPermPartName = iota
	wfPermPartPattern
	wfPermPartAction
	wfPermPartCount
)

func newWorkflowBuilderModal(
	workflows host.Workflows,
	agents []string,
	doc host.WorkflowDocument,
	scope string,
	creating bool,
	th theme.Theme,
	drafts ...host.WorkflowDrafts,
) *workflowBuilderModal {
	th = th.Resolve()
	if doc.SchemaVersion == 0 {
		doc.SchemaVersion = 1
	}
	if scope == "" {
		scope = host.WorkflowScopeProject
	}
	m := &workflowBuilderModal{
		workflows:   workflows,
		agents:      append([]string(nil), agents...),
		th:          th,
		doc:         cloneWorkflowDoc(doc),
		scope:       scope,
		creating:    creating,
		focus:       wfBuilderFocusPhases,
		showPreview: true,
		input:       newTextInput(th, ""),
	}
	if len(drafts) > 0 {
		m.drafts = drafts[0]
	}
	if len(m.doc.Phases) == 0 {
		m.doc.Phases = []host.WorkflowPhaseDocument{{
			Name: "step-one",
			Gate: "agent",
		}}
	}
	m.baseline = m.snapshot()
	m.refreshStatus()
	return m
}

func cloneWorkflowDoc(doc host.WorkflowDocument) host.WorkflowDocument {
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

func (m *workflowBuilderModal) snapshot() string {
	if m.workflows == nil {
		return fmt.Sprintf("%s\x00%s\x00%#v", m.scope, m.doc.Name, m.doc)
	}
	s, err := m.workflows.Format(m.doc)
	if err != nil {
		return fmt.Sprintf("%s\x00%s\x00%#v", m.scope, m.doc.Name, m.doc)
	}
	return m.scope + "\x00" + s
}

func (m *workflowBuilderModal) dirty() bool {
	return m.snapshot() != m.baseline
}

func (m *workflowBuilderModal) refreshStatus() {
	if m.workflows == nil {
		m.status = "workflows unavailable"
		m.statusErr = true
		return
	}
	if err := m.workflows.Validate(m.doc); err != nil {
		m.status = err.Error()
		m.statusErr = true
		return
	}
	m.status = "valid"
	m.statusErr = false
}

func (m *workflowBuilderModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if m.editing {
		return m.updateEditing(msg)
	}
	if m.unsavedPrompt {
		return m.updateUnsavedPrompt(msg)
	}
	if m.overwritePrompt {
		return m.updateOverwritePrompt(msg)
	}
	if isEscape(msg) || msg.String() == "q" {
		if m.dirty() {
			m.unsavedPrompt = true
			return m, nil
		}
		return nil, func() tea.Msg {
			return workflowBuilderResultMsg{canceled: true}
		}
	}
	switch msg.String() {
	case "tab":
		m.focus = (m.focus + 1) % 5
		return m, nil
	case "shift+tab":
		m.focus = (m.focus + 4) % 5
		return m, nil
	case "p":
		m.showPreview = !m.showPreview
		return m, nil
	case "ctrl+s", "alt+s":
		return m.trySave(false)
	case "up", "k", "ctrl+p":
		return m.move(-1)
	case "down", "j", "ctrl+n":
		return m.move(1)
	case "left", "h":
		if m.focus == wfBuilderFocusPerms {
			m.permPart = clampCursor(m.permPart-1, wfPermPartCount)
			return m, nil
		}
		if m.focus > 0 {
			m.focus--
		}
		return m, nil
	case "right", "l":
		if m.focus == wfBuilderFocusPerms {
			m.permPart = clampCursor(m.permPart+1, wfPermPartCount)
			return m, nil
		}
		if m.focus < wfBuilderFocusJSON {
			m.focus++
		}
		return m, nil
	case "a":
		return m.addPhase()
	case "d", "x":
		return m.deleteCurrent()
	case "K", "ctrl+k":
		return m.reorderPhase(-1)
	case "J", "ctrl+j":
		return m.reorderPhase(1)
	case "enter", "e":
		return m.beginEdit()
	case "g":
		// cycle scope when on meta scope or anytime with g
		m.cycleScope()
		return m, nil
	case "r":
		// add permission rule on current phase
		return m.addPerm()
	}
	return m, nil
}

func (m *workflowBuilderModal) move(delta int) (modal, tea.Cmd) {
	switch m.focus {
	case wfBuilderFocusMeta:
		m.metaCursor = clampCursor(m.metaCursor+delta, wfMetaCount)
	case wfBuilderFocusPhases:
		m.phaseCursor = clampCursor(m.phaseCursor+delta, len(m.doc.Phases))
	case wfBuilderFocusFields:
		m.fieldCursor = clampCursor(m.fieldCursor+delta, wfFieldCount)
	case wfBuilderFocusPerms:
		n := 0
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			n = len(m.doc.Phases[m.phaseCursor].Permissions)
		}
		m.permCursor = clampCursor(m.permCursor+delta, max(1, n))
	case wfBuilderFocusJSON:
		// no cursor
	}
	return m, nil
}

func clampCursor(v, n int) int {
	if n <= 0 {
		return 0
	}
	if v < 0 {
		return 0
	}
	if v >= n {
		return n - 1
	}
	return v
}

func (m *workflowBuilderModal) cycleScope() {
	for i, s := range wfScopeChoices {
		if s == m.scope {
			m.scope = wfScopeChoices[(i+1)%len(wfScopeChoices)]
			m.refreshStatus()
			return
		}
	}
	m.scope = host.WorkflowScopeProject
	m.refreshStatus()
}

func (m *workflowBuilderModal) addPhase() (modal, tea.Cmd) {
	n := len(m.doc.Phases) + 1
	name := fmt.Sprintf("step-%d", n)
	// ensure unique
	for m.phaseNameTaken(name, -1) {
		n++
		name = fmt.Sprintf("step-%d", n)
	}
	m.doc.Phases = append(m.doc.Phases, host.WorkflowPhaseDocument{
		Name: name,
		Gate: "agent",
	})
	m.phaseCursor = len(m.doc.Phases) - 1
	m.focus = wfBuilderFocusPhases
	m.refreshStatus()
	return m, nil
}

func (m *workflowBuilderModal) phaseNameTaken(name string, except int) bool {
	for i, p := range m.doc.Phases {
		if i == except {
			continue
		}
		if p.Name == name {
			return true
		}
	}
	return false
}

func (m *workflowBuilderModal) deleteCurrent() (modal, tea.Cmd) {
	switch m.focus {
	case wfBuilderFocusPhases:
		if len(m.doc.Phases) <= 1 {
			m.status = "workflow needs at least one phase"
			m.statusErr = true
			return m, nil
		}
		if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
			return m, nil
		}
		m.doc.Phases = append(m.doc.Phases[:m.phaseCursor], m.doc.Phases[m.phaseCursor+1:]...)
		if m.phaseCursor >= len(m.doc.Phases) {
			m.phaseCursor = len(m.doc.Phases) - 1
		}
		m.refreshStatus()
	case wfBuilderFocusPerms:
		if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
			return m, nil
		}
		perms := m.doc.Phases[m.phaseCursor].Permissions
		if m.permCursor < 0 || m.permCursor >= len(perms) {
			return m, nil
		}
		m.doc.Phases[m.phaseCursor].Permissions = append(perms[:m.permCursor], perms[m.permCursor+1:]...)
		if m.permCursor >= len(m.doc.Phases[m.phaseCursor].Permissions) && m.permCursor > 0 {
			m.permCursor--
		}
		m.refreshStatus()
	}
	return m, nil
}

func (m *workflowBuilderModal) reorderPhase(delta int) (modal, tea.Cmd) {
	if m.focus != wfBuilderFocusPhases && m.focus != wfBuilderFocusFields {
		return m, nil
	}
	i := m.phaseCursor
	j := i + delta
	if i < 0 || i >= len(m.doc.Phases) || j < 0 || j >= len(m.doc.Phases) {
		return m, nil
	}
	m.doc.Phases[i], m.doc.Phases[j] = m.doc.Phases[j], m.doc.Phases[i]
	m.phaseCursor = j
	m.refreshStatus()
	return m, nil
}

func (m *workflowBuilderModal) addPerm() (modal, tea.Cmd) {
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		return m, nil
	}
	p := &m.doc.Phases[m.phaseCursor]
	p.Permissions = append(p.Permissions, host.WorkflowPermission{
		Permission: "bash",
		Pattern:    "*",
		Action:     "deny",
	})
	m.permCursor = len(p.Permissions) - 1
	m.focus = wfBuilderFocusPerms
	m.refreshStatus()
	return m, nil
}

func (m *workflowBuilderModal) beginEdit() (modal, tea.Cmd) {
	switch m.focus {
	case wfBuilderFocusMeta:
		switch m.metaCursor {
		case wfMetaName:
			if !m.creating {
				m.status = "name locked when editing existing workflow"
				m.statusErr = true
				return m, nil
			}
			return m.startTextEdit("meta.name", m.doc.Name, "workflow name")
		case wfMetaDesc:
			return m.startTextEdit("meta.desc", m.doc.Description, "description")
		case wfMetaScope:
			m.cycleScope()
			return m, nil
		}
	case wfBuilderFocusPhases:
		m.focus = wfBuilderFocusFields
		m.fieldCursor = 0
		return m, nil
	case wfBuilderFocusFields:
		return m.beginFieldEdit()
	case wfBuilderFocusPerms:
		return m.beginPermEdit()
	case wfBuilderFocusJSON:
		// read-only
	}
	return m, nil
}

func (m *workflowBuilderModal) beginFieldEdit() (modal, tea.Cmd) {
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		return m, nil
	}
	p := m.doc.Phases[m.phaseCursor]
	switch m.fieldCursor {
	case wfFieldName:
		return m.startTextEdit("phase.name", p.Name, "phase name")
	case wfFieldDesc:
		return m.startTextEdit("phase.desc", p.Description, "phase description")
	case wfFieldAgent:
		return m.cycleAgent()
	case wfFieldGate:
		return m.cycleGate()
	case wfFieldCommand:
		return m.startTextEdit("phase.command", p.GateCommand, "check command")
	case wfFieldContext:
		return m.startTextEdit("phase.context", p.Context, "phase context")
	}
	return m, nil
}

func (m *workflowBuilderModal) beginPermEdit() (modal, tea.Cmd) {
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		return m, nil
	}
	perms := m.doc.Phases[m.phaseCursor].Permissions
	if len(perms) == 0 {
		return m.addPerm()
	}
	if m.permCursor < 0 || m.permCursor >= len(perms) {
		m.permCursor = 0
	}
	r := m.doc.Phases[m.phaseCursor].Permissions[m.permCursor]
	switch m.permPart {
	case wfPermPartName:
		return m.startTextEdit("perm.permission", r.Permission, "permission name (e.g. write)")
	case wfPermPartPattern:
		pat := r.Pattern
		if pat == "" {
			pat = "*"
		}
		return m.startTextEdit("perm.pattern", pat, "glob pattern")
	default: // action
		rp := &m.doc.Phases[m.phaseCursor].Permissions[m.permCursor]
		for i, a := range wfActionChoices {
			if a == strings.ToLower(rp.Action) {
				rp.Action = wfActionChoices[(i+1)%len(wfActionChoices)]
				m.refreshStatus()
				return m, nil
			}
		}
		rp.Action = "allow"
		m.refreshStatus()
		return m, nil
	}
}

func (m *workflowBuilderModal) cycleGate() (modal, tea.Cmd) {
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		return m, nil
	}
	p := &m.doc.Phases[m.phaseCursor]
	cur := strings.ToLower(strings.TrimSpace(p.Gate))
	if cur == "" {
		cur = "agent"
	}
	for i, g := range wfGateChoices {
		if g == cur {
			p.Gate = wfGateChoices[(i+1)%len(wfGateChoices)]
			m.refreshStatus()
			return m, nil
		}
	}
	p.Gate = "agent"
	m.refreshStatus()
	return m, nil
}

func (m *workflowBuilderModal) cycleAgent() (modal, tea.Cmd) {
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		return m, nil
	}
	p := &m.doc.Phases[m.phaseCursor]
	// choices: empty (keep current) + known agents
	choices := make([]string, 0, len(m.agents)+1)
	choices = append(choices, "")
	choices = append(choices, m.agents...)
	if len(choices) == 1 {
		// no agents — free-text edit
		return m.startTextEdit("phase.agent", p.Agent, "agent name")
	}
	cur := strings.TrimSpace(p.Agent)
	idx := 0
	for i, c := range choices {
		if c == cur {
			idx = i
			break
		}
	}
	p.Agent = choices[(idx+1)%len(choices)]
	m.refreshStatus()
	return m, nil
}

func (m *workflowBuilderModal) startTextEdit(kind, value, placeholder string) (modal, tea.Cmd) {
	m.editing = true
	m.editKind = kind
	m.input = newTextInput(m.th, placeholder)
	m.input.SetValue(value)
	m.input.CursorEnd()
	m.input.Focus()
	return m, nil
}

func (m *workflowBuilderModal) updateEditing(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		m.editing = false
		m.editKind = ""
		m.input.Blur()
		return m, nil
	}
	if msg.String() == "enter" {
		return m.commitTextEdit()
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *workflowBuilderModal) commitTextEdit() (modal, tea.Cmd) {
	val := m.input.Value()
	kind := m.editKind
	m.editing = false
	m.editKind = ""
	m.input.Blur()

	switch kind {
	case "meta.name":
		m.doc.Name = strings.TrimSpace(val)
	case "meta.desc":
		m.doc.Description = val
	case "phase.name":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			m.doc.Phases[m.phaseCursor].Name = strings.TrimSpace(val)
		}
	case "phase.desc":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			m.doc.Phases[m.phaseCursor].Description = val
		}
	case "phase.agent":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			m.doc.Phases[m.phaseCursor].Agent = strings.TrimSpace(val)
		}
	case "phase.command":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			m.doc.Phases[m.phaseCursor].GateCommand = val
		}
	case "phase.context":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			m.doc.Phases[m.phaseCursor].Context = val
		}
	case "perm.permission":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			perms := m.doc.Phases[m.phaseCursor].Permissions
			if m.permCursor >= 0 && m.permCursor < len(perms) {
				m.doc.Phases[m.phaseCursor].Permissions[m.permCursor].Permission = strings.TrimSpace(val)
			}
		}
	case "perm.pattern":
		if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) {
			perms := m.doc.Phases[m.phaseCursor].Permissions
			if m.permCursor >= 0 && m.permCursor < len(perms) {
				pat := strings.TrimSpace(val)
				if pat == "" {
					pat = "*"
				}
				m.doc.Phases[m.phaseCursor].Permissions[m.permCursor].Pattern = pat
			}
		}
	}
	m.refreshStatus()
	return m, nil
}

func (m *workflowBuilderModal) updateUnsavedPrompt(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		// discard
		m.unsavedPrompt = false
		return nil, func() tea.Msg {
			return workflowBuilderResultMsg{canceled: true}
		}
	case "n", "N", "esc":
		m.unsavedPrompt = false
		return m, nil
	case "s", "S":
		m.unsavedPrompt = false
		return m.trySave(true)
	}
	return m, nil
}

func (m *workflowBuilderModal) updateOverwritePrompt(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		m.overwritePrompt = false
		return m.doSave(true, false)
	case "n", "N", "esc":
		m.overwritePrompt = false
		m.status = "save canceled"
		m.statusErr = false
		return m, nil
	}
	return m, nil
}

func (m *workflowBuilderModal) trySave(closeAfter bool) (modal, tea.Cmd) {
	if m.workflows == nil {
		m.status = "workflows unavailable"
		m.statusErr = true
		return m, nil
	}
	m.refreshStatus()
	if m.statusErr {
		// cannot save invalid
		return m, nil
	}
	// Probe existence via Save force=false first in cmd.
	return m.doSave(false, closeAfter)
}

func (m *workflowBuilderModal) doSave(force, closeAfter bool) (modal, tea.Cmd) {
	if m.workflows == nil {
		m.status = "workflows unavailable"
		m.statusErr = true
		return m, nil
	}
	wf, doc, scope := m.workflows, cloneWorkflowDoc(m.doc), m.scope
	return m, func() tea.Msg {
		path, err := wf.Save(doc, scope, force)
		if err != nil {
			if !force && errors.Is(err, host.ErrWorkflowExists) {
				// signal overwrite needed via special path empty + exists err
				return workflowBuilderSavedMsg{name: doc.Name, err: err, closeAfter: closeAfter}
			}
			return workflowBuilderSavedMsg{name: doc.Name, err: err, closeAfter: closeAfter}
		}
		return workflowBuilderSavedMsg{name: doc.Name, path: path, closeAfter: closeAfter}
	}
}

// onSaved applies a save result inside the modal (called from app Update).
// Returns (nextModal, cmd). nextModal nil means close.
func (m *workflowBuilderModal) onSaved(msg workflowBuilderSavedMsg) (modal, tea.Cmd) {
	if msg.err != nil {
		if errors.Is(msg.err, host.ErrWorkflowExists) {
			m.overwritePrompt = true
			m.status = "file exists - overwrite?"
			m.statusErr = true
			return m, nil
		}
		m.status = msg.err.Error()
		m.statusErr = true
		return m, nil
	}
	m.baseline = m.snapshot()
	m.creating = false
	m.status = "saved " + sanitizeDisplayData(msg.path)
	m.statusErr = false
	if msg.closeAfter {
		name := msg.name
		return nil, func() tea.Msg {
			return workflowBuilderResultMsg{saved: true, name: name}
		}
	}
	return m, nil
}

func (m *workflowBuilderModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	if m.unsavedPrompt {
		body := wrapToWidth(st.Warning.Render(
			"Unsaved changes. "+dotJoin(th, "y discard", "s save", "n/esc stay"),
		), inner)
		return ui.Dialog(th, ui.DialogOpts{
			Title: "workflow builder",
			Hint:  dotJoin(th, "y discard", "s save", "n stay"),
			Width: width,
			Tone:  ui.ToneWarning,
		}, body)
	}
	if m.overwritePrompt {
		body := wrapToWidth(st.Warning.Render(
			"Workflow file already exists. Overwrite? y/n",
		), inner)
		return ui.Dialog(th, ui.DialogOpts{
			Title: "workflow builder",
			Hint:  dotJoin(th, "y overwrite", "n cancel"),
			Width: width,
			Tone:  ui.ToneWarning,
		}, body)
	}
	if m.editing {
		label := m.editKind
		body := strings.Join([]string{
			wrapToWidth(st.Muted.Render("editing "+label), inner),
			m.input.View(),
		}, "\n")
		return ui.Dialog(th, ui.DialogOpts{
			Title: "workflow builder",
			Hint:  dotJoin(th, "enter commit", "esc cancel"),
			Width: width,
		}, body)
	}

	var parts []string

	// Header meta
	dirtyMark := ""
	if m.dirty() {
		dirtyMark = " *"
	}
	titleLine := st.Title.Render(fmt.Sprintf("%s%s", sanitizeDisplayData(m.doc.Name), dirtyMark))
	parts = append(parts, wrapToWidth(titleLine, inner))

	metaLines := m.renderMeta(th, st, inner)
	parts = append(parts, metaLines)

	// Phases + fields side by side when wide enough
	phaseBlock := m.renderPhases(th, st, inner)
	fieldBlock := m.renderFields(th, st, inner)
	permBlock := m.renderPerms(th, st, inner)

	if inner >= 60 {
		half := max(1, (inner-th.Spacing.SM)/2)
		left := clipBlockWidth(phaseBlock+"\n"+permBlock, half)
		right := clipBlockWidth(fieldBlock, half)
		parts = append(parts, lipglossJoinHorizontal(th, left, right))
	} else {
		parts = append(parts, phaseBlock, fieldBlock, permBlock)
	}

	if m.showPreview {
		parts = append(parts, m.renderPreview(th, st, inner))
	}

	// status
	if m.status != "" {
		style := st.Success
		if m.statusErr {
			style = st.Error
		}
		parts = append(parts, wrapToWidth(style.Render(sanitizeDisplayData(truncateRunes(m.status, inner*3))), inner))
	}

	gap := strings.Repeat("\n", max(1, th.Spacing.SM))
	body := strings.Join(parts, gap)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "workflow builder",
		Hint:  dotJoin(th, "tab focus", "a add phase", "r add rule", "J/K reorder", "enter edit", "ctrl+s save", "p preview", "esc cancel"),
		Width: width,
	}, body)
}

func (m *workflowBuilderModal) renderMeta(th theme.Theme, st theme.Styles, inner int) string {
	rows := []struct {
		label, value string
	}{
		{"name", sanitizeDisplayData(m.doc.Name)},
		{"description", sanitizeDisplayData(truncateRunes(m.doc.Description, 48))},
		{"scope", m.scope},
	}
	var lines []string
	for i, r := range rows {
		label := r.label
		val := r.value
		if val == "" {
			val = th.Icons.DetailSeparator
		}
		line := fmt.Sprintf("%s: %s", label, val)
		focused := m.focus == wfBuilderFocusMeta && m.metaCursor == i
		if focused {
			lines = append(lines, wrapToWidth(st.SelectedUnderline.Render(line), inner))
		} else if m.focus == wfBuilderFocusMeta {
			lines = append(lines, wrapToWidth(st.Text.Render(line), inner))
		} else {
			lines = append(lines, wrapToWidth(st.Muted.Render(line), inner))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *workflowBuilderModal) renderPhases(th theme.Theme, st theme.Styles, inner int) string {
	headerStyle := st.Muted
	if m.focus == wfBuilderFocusPhases {
		headerStyle = st.Accent
	}
	lines := []string{headerStyle.Render("Phases")}
	if len(m.doc.Phases) == 0 {
		lines = append(lines, st.Muted.Render("(none)"))
		return strings.Join(lines, "\n")
	}
	items := make([]ui.ListItem, len(m.doc.Phases))
	for i, p := range m.doc.Phases {
		gate := p.Gate
		if gate == "" {
			gate = "agent"
		}
		detail := gate
		if p.Agent != "" {
			detail = detailJoin(th, p.Agent, gate)
		}
		items[i] = ui.ListItem{
			Label:  sanitizeDisplayData(p.Name),
			Detail: sanitizeDisplayData(detail),
		}
	}
	w := inner
	if inner >= 60 {
		w = max(1, (inner-th.Spacing.SM)/2)
	}
	list := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.phaseCursor,
		Width:   w,
		Visible: min(8, max(3, len(items))),
		Empty:   "no phases",
	})
	// Dim list when unfocused
	if m.focus != wfBuilderFocusPhases {
		// still show cursor position but muted header already set
	}
	lines = append(lines, list)
	return strings.Join(lines, "\n")
}

func (m *workflowBuilderModal) renderFields(th theme.Theme, st theme.Styles, inner int) string {
	headerStyle := st.Muted
	if m.focus == wfBuilderFocusFields {
		headerStyle = st.Accent
	}
	lines := []string{headerStyle.Render("Phase fields")}
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		lines = append(lines, st.Muted.Render("select a phase"))
		return strings.Join(lines, "\n")
	}
	p := m.doc.Phases[m.phaseCursor]
	rows := []struct {
		label, value string
	}{
		{"name", p.Name},
		{"description", truncateRunes(p.Description, 40)},
		{"agent", emptyDash(p.Agent, th)},
		{"gate", emptyDash(p.Gate, th)},
		{"command", emptyDash(p.GateCommand, th)},
		{"context", truncateRunes(p.Context, 40)},
	}
	w := inner
	if inner >= 60 {
		w = max(1, (inner-th.Spacing.SM)/2)
	}
	for i, r := range rows {
		line := fmt.Sprintf("%s: %s", r.label, sanitizeDisplayData(r.value))
		focused := m.focus == wfBuilderFocusFields && m.fieldCursor == i
		if focused {
			lines = append(lines, wrapToWidth(st.SelectedUnderline.Render(line), w))
		} else {
			lines = append(lines, wrapToWidth(st.Text.Render(line), w))
		}
	}
	return strings.Join(lines, "\n")
}

func (m *workflowBuilderModal) renderPerms(th theme.Theme, st theme.Styles, inner int) string {
	headerStyle := st.Muted
	if m.focus == wfBuilderFocusPerms {
		headerStyle = st.Accent
	}
	partHint := "name"
	switch m.permPart {
	case wfPermPartPattern:
		partHint = "pattern"
	case wfPermPartAction:
		partHint = "action"
	}
	lines := []string{headerStyle.Render("Permissions (" + dotJoin(th, "r add", "←/→ field="+partHint, "enter edit", "d del") + ")")}
	if m.phaseCursor < 0 || m.phaseCursor >= len(m.doc.Phases) {
		return lines[0]
	}
	perms := m.doc.Phases[m.phaseCursor].Permissions
	if len(perms) == 0 {
		lines = append(lines, st.Muted.Render("no phase permission overrides"))
		return strings.Join(lines, "\n")
	}
	w := inner
	if inner >= 60 {
		w = max(1, (inner-th.Spacing.SM)/2)
	}
	for i, r := range perms {
		pat := r.Pattern
		if pat == "" {
			pat = "*"
		}
		// Highlight the active sub-field when this row is selected.
		act, perm, pattern := r.Action, r.Permission, pat
		line := fmt.Sprintf("%s %s %s", act, perm, pattern)
		if m.focus == wfBuilderFocusPerms && m.permCursor == i {
			switch m.permPart {
			case wfPermPartName:
				line = fmt.Sprintf("%s [%s] %s", act, perm, pattern)
			case wfPermPartPattern:
				line = fmt.Sprintf("%s %s [%s]", act, perm, pattern)
			default:
				line = fmt.Sprintf("[%s] %s %s", act, perm, pattern)
			}
		}
		tone := st.Text
		switch strings.ToLower(r.Action) {
		case "deny":
			tone = st.Error
		case "allow":
			tone = st.Success
		case "ask":
			tone = st.Warning
		}
		focused := m.focus == wfBuilderFocusPerms && m.permCursor == i
		rendered := tone.Render(sanitizeDisplayData(line))
		if focused {
			rendered = st.SelectedUnderline.Render(sanitizeDisplayData(line))
		}
		lines = append(lines, wrapToWidth(rendered, w))
	}
	return strings.Join(lines, "\n")
}

func (m *workflowBuilderModal) renderPreview(th theme.Theme, st theme.Styles, inner int) string {
	headerStyle := st.Muted
	if m.focus == wfBuilderFocusJSON {
		headerStyle = st.Accent
	}
	lines := []string{headerStyle.Render("Preview")}

	// Validation
	if m.workflows != nil {
		if err := m.workflows.Validate(m.doc); err != nil {
			lines = append(lines, wrapToWidth(st.Error.Render("validation: "+sanitizeDisplayData(err.Error())), inner))
		} else {
			lines = append(lines, wrapToWidth(st.Success.Render("validation: ok"), inner))
		}
	}

	// Phase grants + optional draft-path widening (#717) for CLI-equivalent review.
	if m.phaseCursor >= 0 && m.phaseCursor < len(m.doc.Phases) && m.workflows != nil {
		grants := m.workflows.PhaseGrants(m.doc, m.phaseCursor)
		p := m.doc.Phases[m.phaseCursor]
		gate := p.Gate
		if gate == "" {
			gate = "agent"
		}
		grantHeader := fmt.Sprintf("phase %s grants / exit %s", sanitizeDisplayData(p.Name), gate)
		if p.GateCommand != "" && gate == "check" {
			grantHeader += " (" + sanitizeDisplayData(truncateRunes(p.GateCommand, 32)) + ")"
		}
		lines = append(lines, wrapToWidth(st.Accent.Render(grantHeader), inner))
		if len(grants) == 0 {
			lines = append(lines, wrapToWidth(st.Muted.Render("No phase permission overrides (config/agent rules unchanged)."), inner))
		} else {
			for _, r := range grants {
				pat := r.Pattern
				if pat == "" {
					pat = "*"
				}
				line := fmt.Sprintf("  %s %s %s", r.Action, r.Permission, pat)
				tone := st.Text
				switch strings.ToLower(r.Action) {
				case "deny":
					tone = st.Error
				case "allow":
					tone = st.Success
				case "ask":
					tone = st.Warning
				}
				lines = append(lines, wrapToWidth(tone.Render(sanitizeDisplayData(line)), inner))
			}
		}
		// Effective widening via WorkflowDrafts.Review when available.
		if m.drafts != nil {
			if raw, err := m.workflows.Format(m.doc); err == nil {
				rev := m.drafts.Review(raw)
				if m.phaseCursor < len(rev.Phases) {
					w := rev.Phases[m.phaseCursor].Widening
					if len(w) == 0 {
						lines = append(lines, wrapToWidth(st.Muted.Render("Effective widening vs defaults: none"), inner))
					} else {
						lines = append(lines, wrapToWidth(st.Warning.Render("Effective widening vs defaults:"), inner))
						for _, r := range w {
							pat := r.Pattern
							if pat == "" {
								pat = "*"
							}
							line := fmt.Sprintf("  %s %s %s", r.Action, r.Permission, pat)
							lines = append(lines, wrapToWidth(st.Warning.Render(sanitizeDisplayData(line)), inner))
						}
					}
					if rev.Phases[m.phaseCursor].CheckHighlighted && p.GateCommand != "" {
						lines = append(lines, wrapToWidth(st.AccentAlt.Render(
							"check command: "+sanitizeDisplayData(p.GateCommand),
						), inner))
					}
				}
			}
		}
	}

	// JSON
	if m.workflows != nil {
		raw, err := m.workflows.Format(m.doc)
		if err != nil {
			lines = append(lines, wrapToWidth(st.Error.Render("format: "+err.Error()), inner))
		} else {
			// show first ~12 lines of JSON
			jsonLines := strings.Split(strings.TrimRight(raw, "\n"), "\n")
			const maxJSONLines = 12
			for i, jl := range jsonLines {
				if i >= maxJSONLines {
					lines = append(lines, wrapToWidth(st.Muted.Render(th.Icons.Ellipsis), inner))
					break
				}
				lines = append(lines, wrapToWidth(st.Muted.Render(sanitizeDisplayData(jl)), inner))
			}
		}
	}
	return strings.Join(lines, "\n")
}

func emptyDash(s string, th theme.Theme) string {
	if strings.TrimSpace(s) == "" {
		return th.Resolve().Icons.DetailSeparator
	}
	return s
}

func clipBlockWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = wrapToWidth(line, width)
		// wrapToWidth may introduce newlines; take first visual row only for join
		if idx := strings.IndexByte(lines[i], '\n'); idx >= 0 {
			lines[i] = lines[i][:idx]
		}
	}
	return strings.Join(lines, "\n")
}

// lipglossJoinHorizontal joins two blocks side by side with theme spacing.
func lipglossJoinHorizontal(th theme.Theme, left, right string) string {
	gap := themedSpace(th.Spacing.SM)
	leftLines := strings.Split(left, "\n")
	rightLines := strings.Split(right, "\n")
	n := max(len(leftLines), len(rightLines))
	// pad left to equal width
	leftW := 0
	for _, l := range leftLines {
		if w := visibleWidth(l); w > leftW {
			leftW = w
		}
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		l, r := "", ""
		if i < len(leftLines) {
			l = leftLines[i]
		}
		if i < len(rightLines) {
			r = rightLines[i]
		}
		pad := leftW - visibleWidth(l)
		if pad < 0 {
			pad = 0
		}
		b.WriteString(l)
		b.WriteString(strings.Repeat(" ", pad))
		b.WriteString(gap)
		b.WriteString(r)
	}
	return b.String()
}

func visibleWidth(s string) int {
	return ansi.StringWidth(s)
}
