package tui

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

var errNoProviders = errors.New("custom providers are unavailable")

// settingsModalVisible is the Defaults/Providers list viewport height.
// Keep ≥ len(defaultsFields) so provider/model/agent rows stay reachable
// without scrolling on a typical dialog.
const settingsModalVisible = 14

// settingsPage is which list /settings is showing.
type settingsPage int

const (
	settingsPageMenu settingsPage = iota
	settingsPageDefaults
	settingsPageProviders
	settingsPagePick
)

// settingsField identifies a defaults row that opens a value picker.
type settingsField int

const (
	settingsFieldTheme settingsField = iota
	settingsFieldVim
	settingsFieldNano
	settingsFieldMdRead
	settingsFieldPerm
	settingsFieldAutoApproveSecs
	settingsFieldAutoApproveExclude
	settingsFieldSandbox
	settingsFieldNotify
	settingsFieldLeanCode
	settingsFieldDeferTools
	settingsFieldWorktree
	settingsFieldMaxChildDepth
	settingsFieldProvider // display-only
	settingsFieldModel    // display-only
	settingsFieldAgent    // display-only
	settingsFieldEffort
)

// settingsAutoApproveExcludeChoices are high-traffic permission names users
// commonly pin out of auto-allow. Enter toggles membership and saves.
var settingsAutoApproveExcludeChoices = []string{
	"bash", "write", "edit", "webfetch", "task", "skill",
}

// settingsModal is the /settings UI: defaults editor + custom provider CRUD.
type settingsModal struct {
	services host.Services
	ops      chan<- protocol.Op
	th       theme.Theme
	workDir  string

	page   settingsPage
	cursor int

	// defaults snapshot (refreshed on open and after saves).
	defaults host.UserDefaults

	// providers page
	items []settingsItem

	// pick page
	pickField   settingsField
	pickOptions []settingsPickOption
	pickCursor  int
}

type settingsItemKind int

const (
	settingsItemAdd settingsItemKind = iota
	settingsItemCustom
)

type settingsItem struct {
	kind     settingsItemKind
	provider host.CustomProvider
}

type settingsPickOption struct {
	value  string
	label  string
	detail string
}

type settingsMenuKind int

const (
	settingsMenuDefaults settingsMenuKind = iota
	settingsMenuProviders
)

func newSettingsModal(services host.Services, ops chan<- protocol.Op, th theme.Theme, workDir string) *settingsModal {
	m := &settingsModal{services: services, ops: ops, th: th, workDir: workDir, page: settingsPageMenu}
	m.reloadDefaults()
	return m
}

func (m *settingsModal) reloadDefaults() {
	if m.services.Settings != nil {
		m.defaults = m.services.Settings.Defaults()
	}
}

func (m *settingsModal) reloadProviders() {
	m.items = []settingsItem{{kind: settingsItemAdd}}
	if m.services.Providers != nil {
		for _, p := range m.services.Providers.List() {
			m.items = append(m.items, settingsItem{kind: settingsItemCustom, provider: p})
		}
	}
	if m.cursor >= len(m.items) {
		m.cursor = max(0, len(m.items)-1)
	}
}

// reload refreshes the active page after external provider mutations.
func (m *settingsModal) reload() {
	switch m.page {
	case settingsPageProviders:
		m.reloadProviders()
	case settingsPageDefaults, settingsPagePick:
		m.reloadDefaults()
	default:
		m.reloadDefaults()
	}
}

func (m *settingsModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		switch m.page {
		case settingsPageMenu:
			return nil, nil
		case settingsPagePick:
			m.page = settingsPageDefaults
			m.cursor = int(m.pickField)
			return m, nil
		default:
			m.page = settingsPageMenu
			m.cursor = 0
			return m, nil
		}
	}
	switch m.page {
	case settingsPageMenu:
		return m.updateMenu(msg)
	case settingsPageDefaults:
		return m.updateDefaults(msg)
	case settingsPageProviders:
		return m.updateProviders(msg)
	case settingsPagePick:
		return m.updatePick(msg)
	}
	return m, nil
}

func (m *settingsModal) updateMenu(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	const n = 2
	switch msg.String() {
	case "up", "k":
		m.cursor = (m.cursor + n - 1) % n
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % n
	case "enter":
		switch settingsMenuKind(m.cursor) {
		case settingsMenuDefaults:
			m.page = settingsPageDefaults
			m.reloadDefaults()
			m.cursor = 0
		case settingsMenuProviders:
			m.page = settingsPageProviders
			m.reloadProviders()
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *settingsModal) defaultsFields() []settingsField {
	return []settingsField{
		settingsFieldTheme,
		settingsFieldVim,
		settingsFieldNano,
		settingsFieldMdRead,
		settingsFieldPerm,
		settingsFieldAutoApproveSecs,
		settingsFieldAutoApproveExclude,
		settingsFieldSandbox,
		settingsFieldNotify,
		settingsFieldLeanCode,
		settingsFieldDeferTools,
		settingsFieldWorktree,
		settingsFieldMaxChildDepth,
		settingsFieldProvider,
		settingsFieldModel,
		settingsFieldAgent,
		settingsFieldEffort,
	}
}

func (m *settingsModal) updateDefaults(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	fields := m.defaultsFields()
	if len(fields) == 0 {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		m.cursor = (m.cursor + len(fields) - 1) % len(fields)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(fields)
	case "enter":
		field := fields[m.cursor]
		if !settingsFieldEditable(field) {
			return m, nil
		}
		m.openPick(field)
	}
	return m, nil
}

func settingsFieldEditable(f settingsField) bool {
	switch f {
	case settingsFieldTheme, settingsFieldVim, settingsFieldNano, settingsFieldMdRead,
		settingsFieldPerm, settingsFieldAutoApproveSecs, settingsFieldAutoApproveExclude,
		settingsFieldSandbox, settingsFieldNotify, settingsFieldLeanCode,
		settingsFieldDeferTools, settingsFieldWorktree, settingsFieldMaxChildDepth,
		settingsFieldEffort:
		return true
	default:
		return false
	}
}

func (m *settingsModal) openPick(field settingsField) {
	m.pickField = field
	m.pickOptions = m.pickOptionsFor(field)
	m.pickCursor = 0
	current := m.fieldValue(field)
	if field == settingsFieldAutoApproveExclude {
		// Start on clear when empty, else first excluded name.
		if current == "" {
			m.pickCursor = 0
		} else {
			for i, opt := range m.pickOptions {
				if opt.value != "__clear__" && settingsExcludeContains(m.defaults.PermissionAutoApproveExclude, opt.value) {
					m.pickCursor = i
					break
				}
			}
		}
		m.page = settingsPagePick
		return
	}
	for i, opt := range m.pickOptions {
		if opt.value == current || (current == "" && i == 0) {
			m.pickCursor = i
			break
		}
	}
	// Match aliases (embedded↔pane, modal↔overlay).
	if m.pickCursor == 0 && current != "" {
		for i, opt := range m.pickOptions {
			if settingsValuesEqual(field, opt.value, current) {
				m.pickCursor = i
				break
			}
		}
	}
	m.page = settingsPagePick
}

func settingsValuesEqual(field settingsField, a, b string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	b = strings.ToLower(strings.TrimSpace(b))
	if a == b {
		return true
	}
	switch field {
	case settingsFieldVim, settingsFieldNano:
		return normalizeEditorPick(a) == normalizeEditorPick(b)
	case settingsFieldMdRead:
		return normalizeMdPick(a) == normalizeMdPick(b)
	default:
		return false
	}
}

func normalizeEditorPick(v string) string {
	switch v {
	case "embedded", "pane", "":
		return "pane"
	case "modal", "overlay":
		return "overlay"
	case "takeover":
		return "takeover"
	default:
		return v
	}
}

func normalizeMdPick(v string) string {
	switch v {
	case "pane", "embedded", "":
		return "embedded"
	case "overlay", "modal":
		return "modal"
	default:
		return v
	}
}

func (m *settingsModal) pickOptionsFor(field settingsField) []settingsPickOption {
	switch field {
	case settingsFieldTheme:
		entries := theme.Catalog(m.workDir)
		out := make([]settingsPickOption, 0, len(entries))
		for _, e := range entries {
			detail := e.Source
			if e.Name != e.ID {
				detail = e.ID
			}
			out = append(out, settingsPickOption{value: e.ID, label: e.Name, detail: detail})
		}
		return out
	case settingsFieldVim, settingsFieldNano:
		return []settingsPickOption{
			{value: "pane", label: "pane", detail: "embedded right pane (default)"},
			{value: "overlay", label: "overlay", detail: "large modal with scrim"},
			{value: "takeover", label: "takeover", detail: "full-screen editor handoff"},
		}
	case settingsFieldMdRead:
		return []settingsPickOption{
			{value: "embedded", label: "embedded", detail: "right-pane markdown window (default)"},
			{value: "modal", label: "modal", detail: "large modal with scrim"},
		}
	case settingsFieldPerm:
		modes := protocol.PermissionModes()
		out := make([]settingsPickOption, len(modes))
		for i, mode := range modes {
			out[i] = settingsPickOption{value: string(mode), label: string(mode), detail: mode.Describe()}
		}
		return out
	case settingsFieldSandbox:
		// Two-dial model: sandbox = what OS isolation makes possible.
		return []settingsPickOption{
			{value: "workspace-write", label: "workspace-write", detail: "default — host ro + workspace writable"},
			{value: "read-only", label: "read-only", detail: "no writable workspace bind"},
			{value: "off", label: "off", detail: "no OS sandbox (yolo needs --i-know)"},
		}
	case settingsFieldNotify:
		return []settingsPickOption{
			{value: "unfocused-only", label: "unfocused-only", detail: "default — when terminal unfocused"},
			{value: "on", label: "on", detail: "always notify (attention + long turns)"},
			{value: "off", label: "off", detail: "never notify"},
		}
	case settingsFieldLeanCode:
		return []settingsPickOption{
			{value: "lite", label: "lite", detail: "default — YAGNI ladder guidance"},
			{value: "full", label: "full", detail: "stronger lean-code overlays"},
			{value: "off", label: "off", detail: "no lean-code guidance"},
		}
	case settingsFieldDeferTools:
		return []settingsPickOption{
			{value: "off", label: "off", detail: "default — full tools[] schemas"},
			{value: "on", label: "on", detail: "defer non-core tools until toolsearch"},
		}
	case settingsFieldWorktree:
		return []settingsPickOption{
			{value: "off", label: "off", detail: "default — shared launch cwd"},
			{value: "auto", label: "auto", detail: "worktree when a second root opens"},
			{value: "always", label: "always", detail: "every new root gets a worktree"},
		}
	case settingsFieldAutoApproveSecs:
		return []settingsPickOption{
			{value: "off", label: "off", detail: "disabled (default) — soft-approve mode still uses 15s"},
			{value: "5", label: "5s", detail: "countdown auto-allow once"},
			{value: "10", label: "10s", detail: "countdown auto-allow once"},
			{value: "15", label: "15s", detail: "same as soft-approve default"},
			{value: "30", label: "30s", detail: "countdown auto-allow once"},
			{value: "45", label: "45s", detail: "countdown auto-allow once"},
			{value: "60", label: "60s", detail: "max countdown"},
		}
	case settingsFieldAutoApproveExclude:
		out := make([]settingsPickOption, 0, len(settingsAutoApproveExcludeChoices)+1)
		out = append(out, settingsPickOption{
			value: "__clear__", label: "(none)", detail: "clear exclude list — all permissions may auto-allow",
		})
		for _, name := range settingsAutoApproveExcludeChoices {
			detail := "enter toggles — excluded permissions never auto-allow"
			if settingsExcludeContains(m.defaults.PermissionAutoApproveExclude, name) {
				detail = "excluded — enter to allow auto-approve again"
			}
			out = append(out, settingsPickOption{value: name, label: name, detail: detail})
		}
		return out
	case settingsFieldMaxChildDepth:
		out := []settingsPickOption{
			{value: "default", label: "default", detail: "engine default (1 — children cannot nest tasks)"},
		}
		for n := 1; n <= 8; n++ {
			detail := "nested task spawn ceiling"
			switch n {
			case 1:
				detail = "children cannot spawn further tasks"
			case 8:
				detail = "hard ceiling"
			}
			out = append(out, settingsPickOption{
				value: strconv.Itoa(n), label: strconv.Itoa(n), detail: detail,
			})
		}
		return out
	case settingsFieldEffort:
		levels := protocol.Efforts()
		out := make([]settingsPickOption, len(levels))
		for i, level := range levels {
			out[i] = settingsPickOption{value: string(level), label: string(level), detail: level.Describe()}
		}
		return out
	default:
		return nil
	}
}

func settingsExcludeContains(list []string, name string) bool {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, n := range list {
		if strings.ToLower(strings.TrimSpace(n)) == want {
			return true
		}
	}
	return false
}

func settingsToggleExclude(list []string, name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return nil
	}
	out := make([]string, 0, len(list)+1)
	found := false
	for _, n := range list {
		n = strings.ToLower(strings.TrimSpace(n))
		if n == "" {
			continue
		}
		if n == name {
			found = true
			continue
		}
		out = append(out, n)
	}
	if !found {
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *settingsModal) fieldValue(field settingsField) string {
	d := m.defaults
	switch field {
	case settingsFieldTheme:
		return d.Theme
	case settingsFieldVim:
		return d.VimMode
	case settingsFieldNano:
		return d.NanoMode
	case settingsFieldMdRead:
		return d.MdReadMode
	case settingsFieldPerm:
		return d.PermissionMode
	case settingsFieldAutoApproveSecs:
		if d.PermissionAutoApproveSeconds <= 0 {
			return "off"
		}
		return strconv.Itoa(d.PermissionAutoApproveSeconds)
	case settingsFieldAutoApproveExclude:
		if len(d.PermissionAutoApproveExclude) == 0 {
			return ""
		}
		return strings.Join(d.PermissionAutoApproveExclude, ", ")
	case settingsFieldSandbox:
		return d.Sandbox
	case settingsFieldNotify:
		return d.Notify
	case settingsFieldLeanCode:
		return d.LeanCode
	case settingsFieldDeferTools:
		return d.DeferTools
	case settingsFieldWorktree:
		return d.SessionWorktree
	case settingsFieldMaxChildDepth:
		if d.MaxChildDepth <= 0 {
			return "default"
		}
		return strconv.Itoa(d.MaxChildDepth)
	case settingsFieldProvider:
		return d.Provider
	case settingsFieldModel:
		return d.Model
	case settingsFieldAgent:
		return d.Agent
	case settingsFieldEffort:
		return d.Effort
	default:
		return ""
	}
}

func (m *settingsModal) fieldLabel(field settingsField) string {
	switch field {
	case settingsFieldTheme:
		return "Theme"
	case settingsFieldVim:
		return "Vim mode"
	case settingsFieldNano:
		return "Nano mode"
	case settingsFieldMdRead:
		return "Md-read mode"
	case settingsFieldPerm:
		return "Permission mode"
	case settingsFieldAutoApproveSecs:
		return "Auto-approve"
	case settingsFieldAutoApproveExclude:
		return "Auto-approve exclude"
	case settingsFieldSandbox:
		return "Sandbox"
	case settingsFieldNotify:
		return "Notify"
	case settingsFieldLeanCode:
		return "Lean code"
	case settingsFieldDeferTools:
		return "Defer tools"
	case settingsFieldWorktree:
		return "Session worktree"
	case settingsFieldMaxChildDepth:
		return "Max child depth"
	case settingsFieldProvider:
		return "Provider"
	case settingsFieldModel:
		return "Model"
	case settingsFieldAgent:
		return "Agent"
	case settingsFieldEffort:
		return "Effort"
	default:
		return ""
	}
}

func (m *settingsModal) fieldDisplay(field settingsField) (value, detail string) {
	raw := m.fieldValue(field)
	switch field {
	case settingsFieldTheme:
		if raw == "" {
			return "strike", "default palette"
		}
		return raw, "color theme id"
	case settingsFieldVim, settingsFieldNano:
		if raw == "" {
			return "pane", "default"
		}
		return normalizeEditorPick(raw), editorModeDetail(raw)
	case settingsFieldMdRead:
		if raw == "" {
			return "embedded", "default"
		}
		return normalizeMdPick(raw), mdModeDetail(raw)
	case settingsFieldPerm:
		if raw == "" {
			return "default", "when asked (new sessions)"
		}
		return raw, "when asked (new sessions)"
	case settingsFieldAutoApproveSecs:
		if raw == "" || raw == "off" {
			return "off", "permission countdown (live)"
		}
		return raw + "s", "permission countdown (live)"
	case settingsFieldAutoApproveExclude:
		if raw == "" {
			return "(none)", "never auto-allow these (live)"
		}
		return raw, "never auto-allow these (live)"
	case settingsFieldSandbox:
		if raw == "" {
			return "workspace-write", "what is possible (new sessions)"
		}
		return raw, "what is possible (new sessions)"
	case settingsFieldNotify:
		if raw == "" {
			return "unfocused-only", "desktop notifications"
		}
		return raw, "desktop notifications"
	case settingsFieldLeanCode:
		if raw == "" {
			return "lite", "new sessions"
		}
		return raw, "new sessions"
	case settingsFieldDeferTools:
		if raw == "" {
			return "off", "new sessions"
		}
		return raw, "new sessions"
	case settingsFieldWorktree:
		if raw == "" {
			return "off", "new root sessions"
		}
		return raw, "new root sessions"
	case settingsFieldMaxChildDepth:
		if raw == "" || raw == "default" {
			return "default", "task nesting (new sessions)"
		}
		return raw, "task nesting (new sessions)"
	case settingsFieldEffort:
		if raw == "" {
			return "(unset)", "provider default"
		}
		return raw, "new sessions"
	case settingsFieldProvider, settingsFieldModel, settingsFieldAgent:
		if raw == "" {
			return "(unset)", "set via picker + ctrl+d"
		}
		return raw, "set via picker + ctrl+d"
	default:
		return raw, ""
	}
}

func editorModeDetail(v string) string {
	switch normalizeEditorPick(v) {
	case "overlay":
		return "modal with scrim"
	case "takeover":
		return "full-screen handoff"
	default:
		return "right pane"
	}
}

func mdModeDetail(v string) string {
	if normalizeMdPick(v) == "modal" {
		return "modal with scrim"
	}
	return "right pane"
}

func (m *settingsModal) updatePick(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if len(m.pickOptions) == 0 {
		return m, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.pickCursor > 0 {
			m.pickCursor--
		}
	case "down", "j", "ctrl+n", "tab":
		if m.pickCursor < len(m.pickOptions)-1 {
			m.pickCursor++
		}
	case "enter":
		opt := m.pickOptions[m.pickCursor]
		return m, m.savePickCmd(opt)
	}
	return m, nil
}

func (m *settingsModal) savePickCmd(opt settingsPickOption) tea.Cmd {
	settings := m.services.Settings
	field := m.pickField
	workDir := m.workDir
	excludeSnapshot := append([]string(nil), m.defaults.PermissionAutoApproveExclude...)
	return func() tea.Msg {
		if settings == nil {
			return settingsSavedMsg{err: errNoSettings}
		}
		var err error
		var apply settingsApply
		switch field {
		case settingsFieldTheme:
			err = settings.SaveTheme(opt.value)
			if err == nil {
				if entry, ok := theme.Lookup(theme.Catalog(workDir), opt.value); ok {
					apply.theme = &entry
				}
				apply.themeID = opt.value
			}
		case settingsFieldVim:
			err = settings.SavePresentation(opt.value, "", "")
			if err == nil {
				if mode, ok := ParseVimMode(opt.value); ok {
					apply.vimMode = mode
					apply.hasVim = true
				}
			}
		case settingsFieldNano:
			err = settings.SavePresentation("", opt.value, "")
			if err == nil {
				if mode, ok := ParseNanoMode(opt.value); ok {
					apply.nanoMode = mode
					apply.hasNano = true
				}
			}
		case settingsFieldMdRead:
			err = settings.SavePresentation("", "", opt.value)
			if err == nil {
				if mode, ok := ParseSurfacePresentation(opt.value); ok {
					apply.mdReadMode = mode
					apply.hasMd = true
				}
			}
		case settingsFieldPerm:
			err = settings.SaveDefaults("", "", "", "", opt.value)
		case settingsFieldAutoApproveSecs:
			err = settings.SaveAutoApproveDials(opt.value, nil, "")
			if err == nil {
				secs := 0
				if opt.value != "off" && opt.value != "0" {
					secs, _ = strconv.Atoi(opt.value)
				}
				apply.autoApproveSecs = secs
				apply.hasAutoApproveSecs = true
			}
		case settingsFieldAutoApproveExclude:
			var next []string
			if opt.value == "__clear__" {
				next = nil
			} else {
				// Toggle against the snapshot captured when the cmd was built.
				// Re-read is not available here; caller passes via closure below.
				next = settingsToggleExclude(excludeSnapshot, opt.value)
			}
			err = settings.SaveAutoApproveDials("", &next, "")
			if err == nil {
				apply.autoApproveExclude = next
				apply.hasAutoApproveExclude = true
			}
		case settingsFieldMaxChildDepth:
			err = settings.SaveAutoApproveDials("", nil, opt.value)
		case settingsFieldSandbox:
			err = settings.SaveConfigDials(opt.value, "", "", "", "")
		case settingsFieldNotify:
			err = settings.SaveConfigDials("", opt.value, "", "", "")
			if err == nil {
				if mode, ok := ParseNotifyMode(opt.value); ok {
					apply.notifyMode = mode
					apply.hasNotify = true
				}
			}
		case settingsFieldLeanCode:
			err = settings.SaveConfigDials("", "", opt.value, "", "")
		case settingsFieldDeferTools:
			err = settings.SaveConfigDials("", "", "", opt.value, "")
		case settingsFieldWorktree:
			err = settings.SaveConfigDials("", "", "", "", opt.value)
		case settingsFieldEffort:
			err = settings.SaveDefaults("", "", "", opt.value, "")
		default:
			return settingsSavedMsg{err: errors.New("unknown settings field")}
		}
		label := opt.label
		if label == "" {
			label = opt.value
		}
		return settingsSavedMsg{field: field, value: opt.value, label: label, apply: apply, err: err}
	}
}

// settingsApply carries live session updates after a successful defaults save.
type settingsApply struct {
	themeID               string
	theme                 *theme.Entry
	vimMode               VimMode
	hasVim                bool
	nanoMode              NanoMode
	hasNano               bool
	mdReadMode            SurfacePresentation
	hasMd                 bool
	notifyMode            NotifyMode
	hasNotify             bool
	autoApproveSecs       int
	hasAutoApproveSecs    bool
	autoApproveExclude    []string
	hasAutoApproveExclude bool
}

// settingsSavedMsg reports a /settings defaults write.
type settingsSavedMsg struct {
	field settingsField
	value string
	label string
	apply settingsApply
	err   error
}

func (m *settingsModal) updateProviders(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if len(m.items) == 0 {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.items)
	case "enter":
		return m.activateProvider()
	case "a":
		return newCustomProviderFormModal(m.services, m.ops, m.th, nil, false, m), nil
	case "e":
		if it := m.items[m.cursor]; it.kind == settingsItemCustom {
			p := it.provider
			return newCustomProviderFormModal(m.services, m.ops, m.th, &p, false, m), nil
		}
	case "d", "x", "delete", "backspace":
		if it := m.items[m.cursor]; it.kind == settingsItemCustom {
			return m, m.removeCmd(it.provider.Name)
		}
	case "s":
		if it := m.items[m.cursor]; it.kind == settingsItemCustom && m.services.Auth != nil {
			return newAPIKeyModal(it.provider.Name, m.services.Auth, m.th, false), nil
		}
	}
	return m, nil
}

func (m *settingsModal) activateProvider() (modal, tea.Cmd) {
	it := m.items[m.cursor]
	switch it.kind {
	case settingsItemAdd:
		return newCustomProviderFormModal(m.services, m.ops, m.th, nil, false, m), nil
	case settingsItemCustom:
		p := it.provider
		return newCustomProviderFormModal(m.services, m.ops, m.th, &p, false, m), nil
	}
	return m, nil
}

func (m *settingsModal) removeCmd(name string) tea.Cmd {
	providers, auth := m.services.Providers, m.services.Auth
	return func() tea.Msg {
		if providers == nil {
			return customProviderSavedMsg{name: name, err: errNoProviders}
		}
		if err := providers.Remove(name); err != nil {
			return customProviderSavedMsg{name: name, err: err}
		}
		if auth != nil {
			_ = auth.Logout(name)
		}
		return customProviderRemovedMsg{name: name}
	}
}

type customProviderRemovedMsg struct {
	name string
	err  error
}

// afterSettingsSaved refreshes the defaults list after a successful pick save.
func (m *settingsModal) afterSettingsSaved(msg settingsSavedMsg) {
	if msg.err != nil {
		return
	}
	m.reloadDefaults()
	// Exclude is a multi-toggle list: stay on the pick page so users can flip
	// several names without re-opening the row.
	if msg.field == settingsFieldAutoApproveExclude {
		m.page = settingsPagePick
		m.pickField = settingsFieldAutoApproveExclude
		m.pickOptions = m.pickOptionsFor(settingsFieldAutoApproveExclude)
		// Keep cursor on the toggled option when possible.
		for i, opt := range m.pickOptions {
			if opt.value == msg.value {
				m.pickCursor = i
				break
			}
		}
		return
	}
	m.page = settingsPageDefaults
	m.cursor = int(msg.field)
}

func (m *settingsModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	switch m.page {
	case settingsPageMenu:
		return m.viewMenu(width, th)
	case settingsPageDefaults:
		return m.viewDefaults(width, th)
	case settingsPageProviders:
		return m.viewProviders(width, th)
	case settingsPagePick:
		return m.viewPick(width, th)
	default:
		return m.viewMenu(width, th)
	}
}

func (m *settingsModal) viewMenu(width int, th theme.Theme) string {
	items := []ui.ListItem{
		{Label: "Defaults", Detail: "theme, sandbox, permission mode, notify, …"},
		{Label: "Custom providers", Detail: "OpenAI-/Anthropic-compatible endpoints"},
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: len(items),
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Settings",
		Hint:  dotJoin(th, "enter open", "esc close"),
		Width: width,
	}, body)
}

func (m *settingsModal) viewDefaults(width int, th theme.Theme) string {
	fields := m.defaultsFields()
	items := make([]ui.ListItem, len(fields))
	for i, f := range fields {
		val, detail := m.fieldDisplay(f)
		items[i] = ui.ListItem{
			Label:    m.fieldLabel(f),
			Detail:   detailJoin(th, val, detail),
			Disabled: !settingsFieldEditable(f),
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: settingsModalVisible,
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Settings", "Defaults"),
		Hint:  dotJoin(th, "enter edit", "esc back"),
		Width: width,
	}, body)
}

func (m *settingsModal) viewPick(width int, th theme.Theme) string {
	items := make([]ui.ListItem, len(m.pickOptions))
	current := m.fieldValue(m.pickField)
	for i, opt := range m.pickOptions {
		items[i] = ui.ListItem{
			Label:   opt.label,
			Detail:  opt.detail,
			Current: settingsValuesEqual(m.pickField, opt.value, current) || (current == "" && i == 0 && m.pickField != settingsFieldTheme && m.pickField != settingsFieldAutoApproveExclude),
		}
		if m.pickField == settingsFieldTheme && opt.value == current {
			items[i].Current = true
		}
		if m.pickField == settingsFieldTheme && current == "" && opt.value == theme.BuiltinID {
			items[i].Current = true
		}
		if m.pickField == settingsFieldAutoApproveExclude {
			if opt.value == "__clear__" {
				items[i].Current = len(m.defaults.PermissionAutoApproveExclude) == 0
			} else {
				items[i].Current = settingsExcludeContains(m.defaults.PermissionAutoApproveExclude, opt.value)
			}
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.pickCursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: settingsModalVisible,
		Empty:   "no options",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Settings", m.fieldLabel(m.pickField)),
		Hint:  dotJoin(th, "enter save", "esc back"),
		Width: width,
	}, body)
}

func (m *settingsModal) viewProviders(width int, th theme.Theme) string {
	items := make([]ui.ListItem, len(m.items))
	for i, it := range m.items {
		switch it.kind {
		case settingsItemAdd:
			items[i] = ui.ListItem{Label: "Add custom provider" + th.Icons.Ellipsis, Detail: "name, URL, wire api, key, models"}
		case settingsItemCustom:
			hostName := it.provider.BaseURL
			if u, err := url.Parse(it.provider.BaseURL); err == nil && u.Host != "" {
				hostName = u.Host
			}
			detail := detailJoin(th, it.provider.API, hostName)
			if len(it.provider.Models) > 0 {
				detail = detailJoin(th, detail, strings.Join(it.provider.Models, ", "))
			}
			items[i] = ui.ListItem{Label: it.provider.Name, Detail: detail}
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: settingsModalVisible,
		Empty:   "no custom providers",
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Settings", "Providers"),
		Hint:  dotJoin(th, "enter edit/add", "a add", "s set key", "d remove", "esc back"),
		Width: width,
	}, body)
}
