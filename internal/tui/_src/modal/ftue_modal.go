package tui

import (
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// ftueStep is one setup-wizard stage. Order matches the guided flow.
type ftueStep int

const (
	ftueStepProvider ftueStep = iota
	ftueStepModel
	ftueStepInit
	ftueStepReady
	ftueStepCount
)

// ftueChildKind selects which existing modal/flow the wizard opens as a child.
type ftueChildKind int

const (
	ftueChildProvider ftueChildKind = iota
	ftueChildModel
	ftueChildInit
)

// ftueSpawnChildMsg parks the wizard on the modal queue and opens a child flow
// (provider picker, model picker, or /init). The wizard step/cursor are kept so
// successful or canceled children return to the same position with refreshed
// status.
type ftueSpawnChildMsg struct {
	resume *ftueModal
	kind   ftueChildKind
}

// ftueFinishedMsg closes onboarding and focuses the composer. Emitted only on
// explicit Finish — never by merely opening /ftue.
type ftueFinishedMsg struct{}

// ftueModal is the manually invokable setup wizard opened by /ftue. It composes
// existing host services and child modals; opening it never writes settings.
type ftueModal struct {
	cursor   int // 0..ftueStepCount-1
	services host.Services
	provider string
	model    string
	th       theme.Theme

	// initSkipped is session-local (not persisted); optional project init.
	initSkipped bool
	// flash is a one-line status under the steps (child cancel, unavailable, …).
	flash string
}

func newFTUEModal(services host.Services, provider, model string, th theme.Theme) *ftueModal {
	m := &ftueModal{
		services: services,
		provider: provider,
		model:    model,
		th:       th,
	}
	m.focusFirstIncomplete()
	return m
}

// syncFrom refreshes live provider/model labels from the app model without
// moving the cursor (wizard position is preserved across child flows).
func (m *ftueModal) syncFrom(provider, model string) {
	if m == nil {
		return
	}
	m.provider = strings.TrimSpace(provider)
	m.model = strings.TrimSpace(model)
}

func (m *ftueModal) focusFirstIncomplete() {
	for i := 0; i < int(ftueStepCount); i++ {
		if !m.stepComplete(ftueStep(i)) {
			m.cursor = i
			return
		}
	}
	m.cursor = int(ftueStepReady)
}

func (m *ftueModal) clampCursor() {
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= int(ftueStepCount) {
		m.cursor = int(ftueStepCount) - 1
	}
}

func (m *ftueModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		// Dismiss: close without writing provider/model settings, but acknowledge
		// global onboarding so auto-open does not loop on the next launch.
		return nil, acknowledgeOnboardingCmd(m.services)
	}
	m.clampCursor()
	switch msg.String() {
	case "up", "k", "ctrl+p", "shift+tab":
		if m.cursor > 0 {
			m.cursor--
		}
		m.flash = ""
		return m, nil
	case "down", "j", "ctrl+n", "tab":
		if m.cursor < int(ftueStepCount)-1 {
			m.cursor++
		}
		m.flash = ""
		return m, nil
	case "s":
		// Skip optional project init only.
		if ftueStep(m.cursor) == ftueStepInit {
			m.initSkipped = true
			m.flash = "project init skipped"
			if m.cursor < int(ftueStepCount)-1 {
				m.cursor++
			}
		}
		return m, nil
	case "f":
		return m.finish()
	case "enter":
		return m.activate()
	default:
		return m, nil
	}
}

func (m *ftueModal) finish() (modal, tea.Cmd) {
	return nil, tea.Batch(
		func() tea.Msg { return ftueFinishedMsg{} },
		acknowledgeOnboardingCmd(m.services),
	)
}

// acknowledgeOnboardingCmd persists global onboarding acknowledgement when the
// host exposes Onboarding. Nil service is a no-op (tests without the capability).
func acknowledgeOnboardingCmd(services host.Services) tea.Cmd {
	if services.Onboarding == nil {
		return nil
	}
	ob := services.Onboarding
	return func() tea.Msg {
		return onboardingAckMsg{err: ob.Acknowledge()}
	}
}

func (m *ftueModal) activate() (modal, tea.Cmd) {
	switch ftueStep(m.cursor) {
	case ftueStepProvider:
		return nil, func() tea.Msg {
			return ftueSpawnChildMsg{resume: m, kind: ftueChildProvider}
		}
	case ftueStepModel:
		return nil, func() tea.Msg {
			return ftueSpawnChildMsg{resume: m, kind: ftueChildModel}
		}
	case ftueStepInit:
		return nil, func() tea.Msg {
			return ftueSpawnChildMsg{resume: m, kind: ftueChildInit}
		}
	case ftueStepReady:
		return m.finish()
	default:
		return m, nil
	}
}

func (m *ftueModal) stepComplete(step ftueStep) bool {
	switch step {
	case ftueStepProvider:
		return m.providerReady()
	case ftueStepModel:
		return m.modelReady()
	case ftueStepInit:
		return m.initReady()
	case ftueStepReady:
		// Ready is complete when the user can send (provider+model).
		return m.providerReady() && m.modelReady()
	default:
		return false
	}
}

func (m *ftueModal) providerReady() bool {
	name := strings.TrimSpace(m.provider)
	if name == "" {
		return false
	}
	if m.services.Auth == nil {
		// Capability missing: treat a selected provider as ready (degrade).
		return true
	}
	for _, s := range m.services.Auth.Statuses() {
		if s.Name != name {
			continue
		}
		return s.Authed || s.Builtin
	}
	// Selected provider not in status list — still count as chosen.
	return true
}

func (m *ftueModal) modelReady() bool {
	return strings.TrimSpace(m.provider) != "" && strings.TrimSpace(m.model) != ""
}

func (m *ftueModal) initReady() bool {
	if m.initSkipped {
		return true
	}
	if m.services.Init == nil {
		// Unavailable: optional step is satisfied (cannot run).
		return true
	}
	exists, _, err := m.services.Init.Exists()
	if err != nil {
		return false
	}
	return exists
}

func (m *ftueModal) stepTitle(step ftueStep) string {
	switch step {
	case ftueStepProvider:
		return "Connect a provider"
	case ftueStepModel:
		return "Choose a model"
	case ftueStepInit:
		return "Project setup (optional)"
	case ftueStepReady:
		return "Send your first prompt"
	default:
		return ""
	}
}

func (m *ftueModal) stepDetail(step ftueStep) string {
	switch step {
	case ftueStepProvider:
		return m.providerDetail()
	case ftueStepModel:
		return m.modelDetail()
	case ftueStepInit:
		return m.initDetail()
	case ftueStepReady:
		if m.providerReady() && m.modelReady() {
			return "type below, enter to send"
		}
		return "finish provider and model first"
	default:
		return ""
	}
}

func (m *ftueModal) providerDetail() string {
	name := strings.TrimSpace(m.provider)
	if name == "" {
		if m.services.Auth == nil {
			return "auth unavailable"
		}
		authed := 0
		for _, s := range m.services.Auth.Statuses() {
			if s.Authed || s.Builtin {
				authed++
			}
		}
		if authed == 0 {
			return "no provider connected"
		}
		return "not selected"
	}
	if m.services.Auth != nil {
		for _, s := range m.services.Auth.Statuses() {
			if s.Name != name {
				continue
			}
			if s.Authed || s.Builtin {
				detail := strings.TrimSpace(s.Detail)
				if detail != "" && detail != "none" {
					return name + " - " + detail
				}
				return name
			}
			return name + " - needs auth"
		}
	}
	return name
}

func (m *ftueModal) modelDetail() string {
	p := strings.TrimSpace(m.provider)
	mod := strings.TrimSpace(m.model)
	switch {
	case p == "":
		return "select a provider first"
	case mod == "":
		return "not selected"
	default:
		return p + "/" + mod
	}
}

func (m *ftueModal) initDetail() string {
	if m.initSkipped {
		return "skipped"
	}
	if m.services.Init == nil {
		return "unavailable"
	}
	exists, path, err := m.services.Init.Exists()
	if err != nil {
		return "error: " + err.Error()
	}
	if exists {
		display := path
		if base := filepath.Base(display); base != "" && base != "." {
			display = base
		}
		if display == "" {
			display = "AGENTS.md"
		}
		return display + " present"
	}
	return "optional — writes AGENTS.md"
}

func (m *ftueModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	m.clampCursor()

	intro := st.Muted.Render("Setup guide — open steps to reuse /provider, /model, and /init. Opening this wizard does not change settings.")
	items := make([]ui.ListItem, 0, int(ftueStepCount))
	for i := 0; i < int(ftueStepCount); i++ {
		step := ftueStep(i)
		done := m.stepComplete(step)
		mark := th.Icons.Info
		if done {
			mark = th.Icons.OK
		}
		items = append(items, ui.ListItem{
			Label:  mark + themedSpace(th.Spacing.Label) + m.stepTitle(step),
			Detail: m.stepDetail(step),
		})
	}

	body := wrapToWidth(intro, inner) + strings.Repeat("\n", max(1, th.Spacing.SM)) +
		ui.List(th, ui.ListOpts{
			Items:   items,
			Cursor:  m.cursor,
			Width:   inner,
			Visible: int(ftueStepCount),
			Empty:   "no steps",
		})

	if m.flash != "" {
		body += strings.Repeat("\n", max(1, th.Spacing.SM)) +
			wrapToWidth(st.Muted.Render(m.flash), inner)
	}

	hints := []string{"↑/↓ move", "enter open step", "s skip init", "f finish", "esc cancel"}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "setup",
		Hint:  dotJoin(th, hints...),
		Width: width,
	}, body)
}

// applyFTUESpawnChild parks the wizard and opens the requested child flow.
func (m *Model) applyFTUESpawnChild(msg ftueSpawnChildMsg) tea.Cmd {
	if msg.resume == nil {
		return nil
	}
	msg.resume.syncFrom(m.providerName, m.modelName)
	msg.resume.flash = ""
	m.enqueueModal(msg.resume)

	switch msg.kind {
	case ftueChildProvider:
		m.modal = newProviderModal(m.services, m.providerName, m.ops, m.th)
		return nil
	case ftueChildModel:
		if strings.TrimSpace(m.providerName) == "" {
			msg.resume.flash = "select a provider first"
			m.modal = nil
			return m.afterModalClosed()
		}
		m.modal = newModelModal(m.providerName, m.modelName, m.ops, m.services.Settings)
		providers := authenticatedModelProviders(m.services.Auth, m.providerName)
		return loadModelsCmd(m.services.Catalog, providers, m.providerName)
	case ftueChildInit:
		return m.openInitFromFTUE(msg.resume)
	default:
		m.modal = nil
		return m.afterModalClosed()
	}
}

// openInitFromFTUE runs the same confirmation-before-overwrite path as /init
// while the wizard stays queued for promotion.
func (m *Model) openInitFromFTUE(resume *ftueModal) tea.Cmd {
	if m.services.Init == nil {
		if resume != nil {
			resume.flash = "project init is unavailable"
		}
		m.modal = nil
		return m.afterModalClosed()
	}
	exists, path, err := m.services.Init.Exists()
	if err != nil {
		if resume != nil {
			resume.flash = "init failed: " + err.Error()
		}
		m.modal = nil
		return m.afterModalClosed()
	}
	if exists {
		m.modal = newInitConfirmModal(path, m.services.Init)
		return nil
	}
	// Create path: no modal; write then promote wizard via applyInitResult.
	m.modal = nil
	init := m.services.Init
	return func() tea.Msg {
		path, created, err := init.Write(false)
		if err != nil {
			return initResultMsg{err: err.Error()}
		}
		return initResultMsg{path: path, created: created}
	}
}

// applyFTUEFinished focuses the composer after an explicit Finish.
func (m *Model) applyFTUEFinished() tea.Cmd {
	// Drop any parked wizard copies (should already be gone).
	if len(m.modalQueue) > 0 {
		out := m.modalQueue[:0]
		for _, q := range m.modalQueue {
			if _, ok := q.(*ftueModal); ok {
				continue
			}
			out = append(out, q)
		}
		if len(out) == 0 {
			m.modalQueue = nil
		} else {
			m.modalQueue = out
		}
	}
	m.setNotice("setup complete — type a message below", false)
	return m.setPaneFocus(focusLeft)
}

// syncFTUEState pushes live provider/model into any visible or queued wizard.
func (m *Model) syncFTUEState() {
	sync := func(mod modal) {
		if f, ok := mod.(*ftueModal); ok {
			f.syncFrom(m.providerName, m.modelName)
		}
	}
	sync(m.modal)
	for _, q := range m.modalQueue {
		sync(q)
	}
}
