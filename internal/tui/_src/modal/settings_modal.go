package tui

import (
	"errors"
	"net/url"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

var errNoProviders = errors.New("custom providers are unavailable")

// settingsModalVisible is the Defaults/Providers/Compaction list viewport height.
// Keep ≥ len(defaultsFields) so provider/model/agent rows stay reachable
// without scrolling on a typical dialog.
const settingsModalVisible = 14

// settingsPage is which list /settings is showing.
type settingsPage int

const (
	settingsPageMenu settingsPage = iota
	settingsPageDefaults
	settingsPageCompaction
	settingsPageProviders
	settingsPagePick
	settingsPageInput
)

// settingsField identifies a defaults/compaction row that opens a value picker
// or free-text input.
type settingsField int

const (
	settingsFieldTheme settingsField = iota
	settingsFieldVim
	settingsFieldNano
	settingsFieldMdRead
	settingsFieldPerm
	settingsFieldSandbox
	settingsFieldNotify
	settingsFieldLeanCode
	settingsFieldDeferTools
	settingsFieldWorktree
	settingsFieldProvider // display-only
	settingsFieldModel    // display-only
	settingsFieldAgent    // display-only
	settingsFieldEffort
	// Compaction / prune (settings → Compaction page).
	settingsFieldCompactionStrategy
	settingsFieldCompactionModel
	settingsFieldCompactionThreshold
	settingsFieldCompactionBuffer
	settingsFieldKeepUserTurns
	settingsFieldPruneProtectTokens
	settingsFieldPruneMinimumTokens
	settingsFieldPruneKeepUserTurns
	settingsFieldPruneProtectTools
)

// settingsModal is the /settings UI: defaults editor, compaction dials, and
// custom provider CRUD.
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
	pickField      settingsField
	pickOptions    []settingsPickOption
	pickCursor     int
	pickReturnPage settingsPage

	// free-text input page (compaction model / prune protect tools)
	inputField      settingsField
	input           textinput.Model
	inputErr        string
	inputReturnPage settingsPage
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
	settingsMenuCompaction
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
	case settingsPageDefaults, settingsPageCompaction, settingsPagePick, settingsPageInput:
		m.reloadDefaults()
	default:
		m.reloadDefaults()
	}
}

func (m *settingsModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || (msg.String() == "q" && m.page != settingsPageInput) {
		switch m.page {
		case settingsPageMenu:
			return nil, nil
		case settingsPagePick:
			m.page = m.pickReturnPage
			if m.page == 0 {
				m.page = settingsPageDefaults
			}
			m.cursor = m.cursorIndexForField(m.pickField)
			return m, nil
		case settingsPageInput:
			m.page = m.inputReturnPage
			if m.page == 0 {
				m.page = settingsPageCompaction
			}
			m.cursor = m.cursorIndexForField(m.inputField)
			m.inputErr = ""
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
	case settingsPageCompaction:
		return m.updateCompaction(msg)
	case settingsPageProviders:
		return m.updateProviders(msg)
	case settingsPagePick:
		return m.updatePick(msg)
	case settingsPageInput:
		return m.updateInput(msg)
	}
	return m, nil
}

func (m *settingsModal) updateMenu(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	const n = 3
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
		case settingsMenuCompaction:
			m.page = settingsPageCompaction
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
		settingsFieldSandbox,
		settingsFieldNotify,
		settingsFieldLeanCode,
		settingsFieldDeferTools,
		settingsFieldWorktree,
		settingsFieldProvider,
		settingsFieldModel,
		settingsFieldAgent,
		settingsFieldEffort,
	}
}

func (m *settingsModal) compactionFields() []settingsField {
	return []settingsField{
		settingsFieldCompactionStrategy,
		settingsFieldCompactionModel,
		settingsFieldCompactionThreshold,
		settingsFieldCompactionBuffer,
		settingsFieldKeepUserTurns,
		settingsFieldPruneProtectTokens,
		settingsFieldPruneMinimumTokens,
		settingsFieldPruneKeepUserTurns,
		settingsFieldPruneProtectTools,
	}
}

func (m *settingsModal) cursorIndexForField(field settingsField) int {
	if settingsFieldIsCompaction(field) {
		for i, f := range m.compactionFields() {
			if f == field {
				return i
			}
		}
		return 0
	}
	for i, f := range m.defaultsFields() {
		if f == field {
			return i
		}
	}
	return 0
}

func settingsFieldIsCompaction(f settingsField) bool {
	switch f {
	case settingsFieldCompactionStrategy, settingsFieldCompactionModel,
		settingsFieldCompactionThreshold, settingsFieldCompactionBuffer,
		settingsFieldKeepUserTurns, settingsFieldPruneProtectTokens,
		settingsFieldPruneMinimumTokens, settingsFieldPruneKeepUserTurns,
		settingsFieldPruneProtectTools:
		return true
	default:
		return false
	}
}

func settingsFieldUsesInput(f settingsField) bool {
	switch f {
	case settingsFieldCompactionModel, settingsFieldPruneProtectTools:
		return true
	default:
		return false
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
		m.openPick(field, settingsPageDefaults)
	}
	return m, nil
}

func (m *settingsModal) updateCompaction(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	fields := m.compactionFields()
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
		if settingsFieldUsesInput(field) {
			m.openInput(field, settingsPageCompaction)
			return m, nil
		}
		m.openPick(field, settingsPageCompaction)
	}
	return m, nil
}

func settingsFieldEditable(f settingsField) bool {
	switch f {
	case settingsFieldTheme, settingsFieldVim, settingsFieldNano, settingsFieldMdRead,
		settingsFieldPerm, settingsFieldSandbox, settingsFieldNotify, settingsFieldLeanCode,
		settingsFieldDeferTools, settingsFieldWorktree, settingsFieldEffort,
		settingsFieldCompactionStrategy, settingsFieldCompactionModel,
		settingsFieldCompactionThreshold, settingsFieldCompactionBuffer,
		settingsFieldKeepUserTurns, settingsFieldPruneProtectTokens,
		settingsFieldPruneMinimumTokens, settingsFieldPruneKeepUserTurns,
		settingsFieldPruneProtectTools:
		return true
	default:
		return false
	}
}

func (m *settingsModal) openPick(field settingsField, returnPage settingsPage) {
	m.pickField = field
	m.pickReturnPage = returnPage
	m.pickOptions = m.pickOptionsFor(field)
	m.pickCursor = 0
	current := m.fieldValue(field)
	for i, opt := range m.pickOptions {
		if opt.value == current || (current == "" && i == 0) {
			m.pickCursor = i
			break
		}
	}
	// Match aliases (embedded↔pane, modal↔overlay) and numeric defaults.
	if m.pickCursor == 0 && current != "" {
		for i, opt := range m.pickOptions {
			if settingsValuesEqual(field, opt.value, current) {
				m.pickCursor = i
				break
			}
		}
	}
	// Prefer exact match for "default" sentinel when stored value is zero/empty.
	if current == "" || current == "0" || current == "0.0" {
		for i, opt := range m.pickOptions {
			if opt.value == "default" || opt.value == "0" {
				m.pickCursor = i
				break
			}
		}
	}
	m.page = settingsPagePick
}

func (m *settingsModal) openInput(field settingsField, returnPage settingsPage) {
	m.inputField = field
	m.inputReturnPage = returnPage
	m.inputErr = ""
	th := m.th.Resolve()
	placeholder := "value"
	seed := m.fieldValue(field)
	switch field {
	case settingsFieldCompactionModel:
		placeholder = "model id (empty = session model)"
		if seed == "" {
			seed = m.defaults.CompactionModel
		}
	case settingsFieldPruneProtectTools:
		placeholder = "comma-separated tools (empty = none)"
		seed = strings.Join(m.defaults.PruneProtectTools, ", ")
	}
	in := newTextInput(th, placeholder)
	in.SetValue(seed)
	in.CursorEnd()
	in.Focus()
	m.input = in
	m.page = settingsPageInput
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
	case settingsFieldCompactionThreshold, settingsFieldCompactionBuffer,
		settingsFieldKeepUserTurns, settingsFieldPruneProtectTokens,
		settingsFieldPruneMinimumTokens, settingsFieldPruneKeepUserTurns:
		// "default"/"0" match empty or zero stored values.
		if isDefaultDialToken(a) && isDefaultDialToken(b) {
			return true
		}
		return a == b
	default:
		return false
	}
}

func isDefaultDialToken(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "0.0", "default":
		return true
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
	case settingsFieldEffort:
		levels := protocol.Efforts()
		out := make([]settingsPickOption, len(levels))
		for i, level := range levels {
			out[i] = settingsPickOption{value: string(level), label: string(level), detail: level.Describe()}
		}
		return out
	case settingsFieldCompactionStrategy:
		return []settingsPickOption{
			{value: "trim", label: "trim", detail: "default — drop older turns"},
			{value: "summarize", label: "summarize", detail: "model-authored summary of dropped turns"},
		}
	case settingsFieldCompactionThreshold:
		return []settingsPickOption{
			{value: "default", label: "default (0.70)", detail: "engine default occupancy trigger"},
			{value: "0.60", label: "0.60", detail: "earlier pressure response"},
			{value: "0.70", label: "0.70", detail: "balanced"},
			{value: "0.80", label: "0.80", detail: "later compaction"},
			{value: "0.85", label: "0.85", detail: "late compaction"},
			{value: "1", label: "off", detail: "disable threshold compaction (>=1)"},
		}
	case settingsFieldCompactionBuffer:
		return []settingsPickOption{
			{value: "default", label: "default (4096)", detail: "engine default headroom"},
			{value: "1024", label: "1024", detail: "tight headroom"},
			{value: "2048", label: "2048", detail: "moderate"},
			{value: "4096", label: "4096", detail: "balanced"},
			{value: "8192", label: "8192", detail: "generous headroom"},
		}
	case settingsFieldKeepUserTurns:
		return []settingsPickOption{
			{value: "default", label: "default (2)", detail: "engine default trailing user turns"},
			{value: "1", label: "1", detail: "keep last user turn only"},
			{value: "2", label: "2", detail: "balanced"},
			{value: "3", label: "3", detail: "more context"},
			{value: "4", label: "4", detail: "max recommended"},
		}
	case settingsFieldPruneProtectTokens:
		return []settingsPickOption{
			{value: "default", label: "default (40000)", detail: "engine default recent tool tokens"},
			{value: "10000", label: "10000", detail: "tight reclaim (MCP-heavy)"},
			{value: "20000", label: "20000", detail: "moderate"},
			{value: "40000", label: "40000", detail: "balanced"},
			{value: "80000", label: "80000", detail: "protect more tool output"},
		}
	case settingsFieldPruneMinimumTokens:
		return []settingsPickOption{
			{value: "default", label: "default (20000)", detail: "engine default min free before prune"},
			{value: "5000", label: "5000", detail: "aggressive reclaim"},
			{value: "10000", label: "10000", detail: "moderate"},
			{value: "20000", label: "20000", detail: "balanced"},
			{value: "40000", label: "40000", detail: "avoid thrash on short sessions"},
		}
	case settingsFieldPruneKeepUserTurns:
		return []settingsPickOption{
			{value: "default", label: "default (2)", detail: "engine default prune keep turns"},
			{value: "1", label: "1", detail: "keep last user turn tools"},
			{value: "2", label: "2", detail: "balanced"},
			{value: "3", label: "3", detail: "more recent tools intact"},
			{value: "4", label: "4", detail: "max recommended"},
		}
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
	case settingsFieldProvider:
		return d.Provider
	case settingsFieldModel:
		return d.Model
	case settingsFieldAgent:
		return d.Agent
	case settingsFieldEffort:
		return d.Effort
	case settingsFieldCompactionStrategy:
		return d.CompactionStrategy
	case settingsFieldCompactionModel:
		return d.CompactionModel
	case settingsFieldCompactionThreshold:
		if d.CompactionThreshold == 0 {
			return "default"
		}
		return formatCompactionFloat(d.CompactionThreshold)
	case settingsFieldCompactionBuffer:
		if d.CompactionBuffer == 0 {
			return "default"
		}
		return strconv.Itoa(d.CompactionBuffer)
	case settingsFieldKeepUserTurns:
		if d.KeepUserTurns == 0 {
			return "default"
		}
		return strconv.Itoa(d.KeepUserTurns)
	case settingsFieldPruneProtectTokens:
		if d.PruneProtectTokens == 0 {
			return "default"
		}
		return strconv.Itoa(d.PruneProtectTokens)
	case settingsFieldPruneMinimumTokens:
		if d.PruneMinimumTokens == 0 {
			return "default"
		}
		return strconv.Itoa(d.PruneMinimumTokens)
	case settingsFieldPruneKeepUserTurns:
		if d.PruneKeepUserTurns == 0 {
			return "default"
		}
		return strconv.Itoa(d.PruneKeepUserTurns)
	case settingsFieldPruneProtectTools:
		return strings.Join(d.PruneProtectTools, ", ")
	default:
		return ""
	}
}

func formatCompactionFloat(v float64) string {
	// Prefer compact forms used by pick options (0.70 not 0.7).
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if v >= 1 {
		return s
	}
	// Two-decimal style for occupancy fractions when clean.
	if t := strconv.FormatFloat(v, 'f', 2, 64); t != s {
		// Keep shortest accurate form unless it is a common pick value.
		switch s {
		case "0.6":
			return "0.60"
		case "0.7":
			return "0.70"
		case "0.8":
			return "0.80"
		case "0.85":
			return "0.85"
		}
	}
	switch s {
	case "0.6":
		return "0.60"
	case "0.7":
		return "0.70"
	case "0.8":
		return "0.80"
	}
	return s
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
	case settingsFieldProvider:
		return "Provider"
	case settingsFieldModel:
		return "Model"
	case settingsFieldAgent:
		return "Agent"
	case settingsFieldEffort:
		return "Effort"
	case settingsFieldCompactionStrategy:
		return "Strategy"
	case settingsFieldCompactionModel:
		return "Summarize model"
	case settingsFieldCompactionThreshold:
		return "Threshold"
	case settingsFieldCompactionBuffer:
		return "Buffer"
	case settingsFieldKeepUserTurns:
		return "Keep user turns"
	case settingsFieldPruneProtectTokens:
		return "Prune protect tokens"
	case settingsFieldPruneMinimumTokens:
		return "Prune minimum tokens"
	case settingsFieldPruneKeepUserTurns:
		return "Prune keep user turns"
	case settingsFieldPruneProtectTools:
		return "Prune protect tools"
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
	case settingsFieldCompactionStrategy:
		if raw == "" {
			return "trim", "drop older turns (new sessions)"
		}
		return raw, "new sessions"
	case settingsFieldCompactionModel:
		if raw == "" {
			return "(session)", "summarize uses session model"
		}
		return raw, "summarize model id"
	case settingsFieldCompactionThreshold:
		if raw == "" || raw == "default" {
			return "0.70", "engine default (new sessions)"
		}
		if raw == "1" || raw == "1.0" {
			return "off", "threshold disabled"
		}
		return raw, "occupancy trigger (new sessions)"
	case settingsFieldCompactionBuffer:
		if raw == "" || raw == "default" {
			return "4096", "engine default headroom"
		}
		return raw, "token headroom (new sessions)"
	case settingsFieldKeepUserTurns:
		if raw == "" || raw == "default" {
			return "2", "engine default trailing turns"
		}
		return raw, "trailing user turns kept"
	case settingsFieldPruneProtectTokens:
		if raw == "" || raw == "default" {
			return "40000", "engine default recent tool tokens"
		}
		return raw, "recent tool tokens protected"
	case settingsFieldPruneMinimumTokens:
		if raw == "" || raw == "default" {
			return "20000", "engine default min free"
		}
		return raw, "min tokens freed before prune"
	case settingsFieldPruneKeepUserTurns:
		if raw == "" || raw == "default" {
			return "2", "engine default prune keep turns"
		}
		return raw, "user turns with intact tools"
	case settingsFieldPruneProtectTools:
		if raw == "" {
			return "(none)", "plus built-in skill"
		}
		return raw, "extra tools never blanked"
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

func (m *settingsModal) updateInput(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch msg.String() {
	case "enter":
		return m, m.saveInputCmd(m.input.Value())
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.inputErr = ""
	return m, cmd
}

func (m *settingsModal) saveInputCmd(raw string) tea.Cmd {
	settings := m.services.Settings
	field := m.inputField
	return func() tea.Msg {
		if settings == nil {
			return settingsSavedMsg{err: errNoSettings}
		}
		value := strings.TrimSpace(raw)
		var err error
		switch field {
		case settingsFieldCompactionModel:
			// Empty clears to session model.
			saveVal := value
			if saveVal == "" {
				saveVal = "-"
			}
			err = settings.SaveCompactionDials(host.CompactionDials{Model: saveVal})
		case settingsFieldPruneProtectTools:
			saveVal := value
			if saveVal == "" {
				saveVal = "-"
			}
			err = settings.SaveCompactionDials(host.CompactionDials{PruneProtectTools: saveVal})
		default:
			return settingsSavedMsg{err: errors.New("unknown settings input field")}
		}
		label := value
		if label == "" {
			label = "(cleared)"
		}
		return settingsSavedMsg{field: field, value: value, label: label, err: err}
	}
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
		case settingsFieldCompactionStrategy:
			err = settings.SaveCompactionDials(host.CompactionDials{Strategy: opt.value})
		case settingsFieldCompactionThreshold:
			err = settings.SaveCompactionDials(host.CompactionDials{Threshold: opt.value})
		case settingsFieldCompactionBuffer:
			err = settings.SaveCompactionDials(host.CompactionDials{Buffer: opt.value})
		case settingsFieldKeepUserTurns:
			err = settings.SaveCompactionDials(host.CompactionDials{KeepUserTurns: opt.value})
		case settingsFieldPruneProtectTokens:
			err = settings.SaveCompactionDials(host.CompactionDials{PruneProtectTokens: opt.value})
		case settingsFieldPruneMinimumTokens:
			err = settings.SaveCompactionDials(host.CompactionDials{PruneMinimumTokens: opt.value})
		case settingsFieldPruneKeepUserTurns:
			err = settings.SaveCompactionDials(host.CompactionDials{PruneKeepUserTurns: opt.value})
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
	notifyMode NotifyMode
	hasNotify  bool
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

// afterSettingsSaved refreshes the list after a successful pick/input save.
func (m *settingsModal) afterSettingsSaved(msg settingsSavedMsg) {
	if msg.err != nil {
		if m.page == settingsPageInput {
			m.inputErr = msg.err.Error()
		}
		return
	}
	m.reloadDefaults()
	if settingsFieldIsCompaction(msg.field) {
		m.page = settingsPageCompaction
	} else {
		m.page = settingsPageDefaults
	}
	m.cursor = m.cursorIndexForField(msg.field)
	m.inputErr = ""
}

func (m *settingsModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	switch m.page {
	case settingsPageMenu:
		return m.viewMenu(width, th)
	case settingsPageDefaults:
		return m.viewDefaults(width, th)
	case settingsPageCompaction:
		return m.viewCompaction(width, th)
	case settingsPageProviders:
		return m.viewProviders(width, th)
	case settingsPagePick:
		return m.viewPick(width, th)
	case settingsPageInput:
		return m.viewInput(width, th)
	default:
		return m.viewMenu(width, th)
	}
}

func (m *settingsModal) viewMenu(width int, th theme.Theme) string {
	items := []ui.ListItem{
		{Label: "Defaults", Detail: "theme, sandbox, permission mode, notify, …"},
		{Label: "Compaction", Detail: "history compact + prune dials"},
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

func (m *settingsModal) viewCompaction(width int, th theme.Theme) string {
	fields := m.compactionFields()
	items := make([]ui.ListItem, len(fields))
	for i, f := range fields {
		val, detail := m.fieldDisplay(f)
		items[i] = ui.ListItem{
			Label:  m.fieldLabel(f),
			Detail: detailJoin(th, val, detail),
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   max(1, ui.PanelInnerWidth(th, width)),
		Visible: settingsModalVisible,
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Settings", "Compaction"),
		Hint:  dotJoin(th, "enter edit", "esc back"),
		Width: width,
	}, body)
}

func (m *settingsModal) viewInput(width int, th theme.Theme) string {
	st := th.S()
	inner := ui.PanelInnerWidth(th, width)
	if width < 4 {
		inner = max(1, width)
	}
	sizeInput(&m.input, inner)
	lines := []string{
		st.Muted.Render(m.fieldLabel(m.inputField)),
		m.input.View(),
	}
	if m.inputErr != "" {
		lines = append(lines, st.Error.Render(sanitizeDisplayData(m.inputErr)))
	}
	body := wrapToWidth(strings.Join(lines, "\n"), inner)
	if width < 4 {
		return body
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: detailJoin(th, "Settings", m.fieldLabel(m.inputField)),
		Hint:  dotJoin(th, "type value", "enter save", "esc back"),
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
