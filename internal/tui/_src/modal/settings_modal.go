package tui

import (
	"errors"
	"net/url"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

var errNoProviders = errors.New("custom providers are unavailable")

const settingsModalVisible = 10

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
	settingsFieldProvider // display-only
	settingsFieldModel    // display-only
	settingsFieldAgent    // display-only
	settingsFieldEffort
)

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

func (m *settingsModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
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

func (m *settingsModal) updateMenu(msg tea.KeyMsg) (modal, tea.Cmd) {
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
		settingsFieldProvider,
		settingsFieldModel,
		settingsFieldAgent,
		settingsFieldEffort,
	}
}

func (m *settingsModal) updateDefaults(msg tea.KeyMsg) (modal, tea.Cmd) {
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
	case settingsFieldTheme, settingsFieldVim, settingsFieldNano, settingsFieldMdRead, settingsFieldPerm, settingsFieldEffort:
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
			return "default", "new sessions"
		}
		return raw, "new sessions"
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

func (m *settingsModal) updatePick(msg tea.KeyMsg) (modal, tea.Cmd) {
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
	themeID    string
	theme      *theme.Entry
	vimMode    VimMode
	hasVim     bool
	nanoMode   NanoMode
	hasNano    bool
	mdReadMode SurfacePresentation
	hasMd      bool
}

// settingsSavedMsg reports a /settings defaults write.
type settingsSavedMsg struct {
	field settingsField
	value string
	label string
	apply settingsApply
	err   error
}

func (m *settingsModal) updateProviders(msg tea.KeyMsg) (modal, tea.Cmd) {
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
		{Label: "Defaults", Detail: "theme, editor presentation, permission mode, …"},
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
			Current: settingsValuesEqual(m.pickField, opt.value, current) || (current == "" && i == 0 && m.pickField != settingsFieldTheme),
		}
		if m.pickField == settingsFieldTheme && opt.value == current {
			items[i].Current = true
		}
		if m.pickField == settingsFieldTheme && current == "" && opt.value == theme.BuiltinID {
			items[i].Current = true
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
