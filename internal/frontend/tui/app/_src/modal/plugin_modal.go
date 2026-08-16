package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

const pluginModalVisible = 10

type pluginModalPhase int

const (
	pluginPhaseBrowse pluginModalPhase = iota
	pluginPhaseDetail
	pluginPhaseConfirm
	pluginPhaseInput // registry / install source / catalog query
	pluginPhaseBusy
	pluginPhaseCatalog
)

type pluginConfirmKind int

const (
	pluginConfirmRemove pluginConfirmKind = iota
	pluginConfirmTrust
	pluginConfirmUntrust
	pluginConfirmUpdate
	pluginConfirmDisable
)

type pluginInputKind int

const (
	pluginInputRegistry pluginInputKind = iota
	pluginInputInstall
	pluginInputCatalogQuery
	pluginInputCatalogRegistry
)

// pluginModal is the centered /plugin manager: browse installed plugins,
// inspect capabilities, and run lifecycle actions through host.Plugins.
// Destructive and trust-changing actions require an explicit confirm phase.
// No secret or executable environment values are rendered.
type pluginModal struct {
	plugins host.Plugins
	all     []host.PluginInfo
	filter  string
	cursor  int
	phase   pluginModalPhase
	loadErr string
	status  string // transient non-fatal message
	busy    string

	// Confirm state.
	confirmKind pluginConfirmKind
	confirmID   string
	confirmSc   string
	confirmBody []string
	updateRev   host.PluginUpdateReview
	trustPrev   host.PluginTrustPreview

	// Input state (registry / install / search).
	inputKind pluginInputKind
	inputBuf  string
	inputHint string
	registry  string // last used catalog registry

	// Catalog browse.
	catalogHits  []host.PluginCatalogHit
	catCursor    int
	catFilter    string
	detailScroll int

	// Pending install source when collecting registry first.
	pendingInstall string
}

func newPluginModal(plugins host.Plugins) *pluginModal {
	m := &pluginModal{plugins: plugins}
	if plugins == nil {
		m.loadErr = "plugin manager unavailable"
		return m
	}
	m.reload()
	return m
}

func (m *pluginModal) reload() {
	m.loadErr = ""
	if m.plugins == nil {
		m.loadErr = "plugin manager unavailable"
		m.all = nil
		return
	}
	list, err := m.plugins.List()
	if err != nil {
		m.loadErr = err.Error()
		m.all = nil
		return
	}
	m.all = list
	if m.cursor >= len(m.filtered()) {
		m.cursor = max(0, len(m.filtered())-1)
	}
}

func (m *pluginModal) filtered() []host.PluginInfo {
	if m.filter == "" {
		return m.all
	}
	q := strings.ToLower(m.filter)
	out := make([]host.PluginInfo, 0, len(m.all))
	for _, p := range m.all {
		if pluginMatches(p, q) {
			out = append(out, p)
		}
	}
	return out
}

func pluginMatches(p host.PluginInfo, q string) bool {
	fields := []string{
		p.ID, p.Name, p.DisplayName, p.Version, p.Scope, p.Status, p.TrustState,
		p.SourceType, p.SourceLabel, p.UpdateAvailable, p.Format, p.Schema,
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), q) {
			return true
		}
	}
	for _, c := range p.Capabilities {
		if strings.Contains(strings.ToLower(c), q) {
			return true
		}
	}
	return false
}

func (m *pluginModal) selected() (host.PluginInfo, bool) {
	list := m.filtered()
	if m.cursor < 0 || m.cursor >= len(list) {
		return host.PluginInfo{}, false
	}
	return list[m.cursor], true
}

func (m *pluginModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	switch m.phase {
	case pluginPhaseBusy:
		// Ignore keys while an async op runs (esc still cancels UI only).
		if isEscape(msg) {
			// Cannot cancel in-flight host ops; stay busy.
			return m, nil
		}
		return m, nil
	case pluginPhaseConfirm:
		return m.updateConfirm(msg)
	case pluginPhaseInput:
		return m.updateInput(msg)
	case pluginPhaseDetail:
		return m.updateDetail(msg)
	case pluginPhaseCatalog:
		return m.updateCatalog(msg)
	default:
		return m.updateBrowse(msg)
	}
}

func (m *pluginModal) updateBrowse(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	list := m.filtered()
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.cursor > 0 {
			m.cursor--
		}
	case "down", "ctrl+n", "j":
		if m.cursor < len(list)-1 {
			m.cursor++
		}
	case "backspace":
		if m.filter != "" {
			runes := []rune(m.filter)
			m.filter = string(runes[:len(runes)-1])
			m.cursor = 0
		}
	case "enter":
		if len(list) == 0 {
			return m, nil
		}
		m.phase = pluginPhaseDetail
		m.detailScroll = 0
		m.status = ""
	// Action chords use ctrl/shift so plain typing still filters (session modal pattern).
	case "ctrl+e":
		return m.toggleEnable()
	case "ctrl+t":
		return m.beginTrustConfirm()
	case "ctrl+u":
		return m.beginUntrustConfirm()
	case "ctrl+x":
		return m.beginRemoveConfirm()
	case "U", "ctrl+shift+u":
		return m.beginUpdatePreview()
	case "ctrl+i":
		m.phase = pluginPhaseInput
		m.inputKind = pluginInputInstall
		m.inputBuf = ""
		m.inputHint = "path | git URL | catalog:pkg[@ver]"
		m.status = ""
	case "ctrl+s":
		return m.beginCatalog()
	case "ctrl+o":
		return m.beginOutdated()
	case "ctrl+r":
		m.reload()
		m.status = "refreshed"
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
		}
	}
	return m, nil
}

func (m *pluginModal) updateDetail(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" || msg.String() == "enter" || msg.String() == "backspace" {
		m.phase = pluginPhaseBrowse
		m.detailScroll = 0
		return m, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
	case "down", "j", "ctrl+n":
		m.detailScroll++
	case "ctrl+e":
		return m.toggleEnable()
	case "ctrl+t":
		return m.beginTrustConfirm()
	case "ctrl+u":
		return m.beginUntrustConfirm()
	case "ctrl+x":
		return m.beginRemoveConfirm()
	case "U", "ctrl+shift+u":
		return m.beginUpdatePreview()
	}
	return m, nil
}

func (m *pluginModal) updateConfirm(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "n" {
		m.phase = pluginPhaseBrowse
		m.confirmBody = nil
		m.status = "canceled"
		return m, nil
	}
	switch msg.String() {
	case "y", "enter":
		return m.runConfirm()
	case "up", "k", "ctrl+p":
		if m.detailScroll > 0 {
			m.detailScroll--
		}
	case "down", "j", "ctrl+n":
		m.detailScroll++
	}
	return m, nil
}

func (m *pluginModal) updateInput(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		m.phase = pluginPhaseBrowse
		m.inputBuf = ""
		m.status = "canceled"
		return m, nil
	}
	switch msg.String() {
	case "backspace":
		if m.inputBuf != "" {
			runes := []rune(m.inputBuf)
			m.inputBuf = string(runes[:len(runes)-1])
		}
	case "enter":
		return m.submitInput()
	default:
		if len(msg.Text) > 0 {
			m.inputBuf += msg.Text
		}
	}
	return m, nil
}

func (m *pluginModal) updateCatalog(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		m.phase = pluginPhaseBrowse
		m.catalogHits = nil
		m.catFilter = ""
		return m, nil
	}
	list := m.filteredCatalog()
	switch msg.String() {
	case "up", "ctrl+p", "k":
		if m.catCursor > 0 {
			m.catCursor--
		}
	case "down", "ctrl+n", "j":
		if m.catCursor < len(list)-1 {
			m.catCursor++
		}
	case "backspace":
		if m.catFilter != "" {
			runes := []rune(m.catFilter)
			m.catFilter = string(runes[:len(runes)-1])
			m.catCursor = 0
		}
	case "enter":
		if len(list) == 0 {
			return m, nil
		}
		hit := list[m.catCursor]
		src := "catalog:" + hit.ID + "@" + hit.Version
		if hit.Registry != "" {
			m.registry = hit.Registry
		}
		return m.startInstall(src, host.PluginScopeGlobal)
	default:
		if len(msg.Text) > 0 {
			m.catFilter += msg.Text
			m.catCursor = 0
		}
	}
	return m, nil
}

func (m *pluginModal) filteredCatalog() []host.PluginCatalogHit {
	if m.catFilter == "" {
		return m.catalogHits
	}
	q := strings.ToLower(m.catFilter)
	out := make([]host.PluginCatalogHit, 0, len(m.catalogHits))
	for _, h := range m.catalogHits {
		if strings.Contains(strings.ToLower(h.ID), q) ||
			strings.Contains(strings.ToLower(h.Name), q) ||
			strings.Contains(strings.ToLower(h.Description), q) {
			out = append(out, h)
		}
	}
	return out
}

func (m *pluginModal) toggleEnable() (modal, tea.Cmd) {
	p, ok := m.selected()
	if !ok || m.plugins == nil {
		return m, nil
	}
	if p.Enabled {
		// Disable is reversible but sticky — confirm.
		return m.beginDisableConfirm()
	}
	m.phase = pluginPhaseBusy
	m.busy = "enabling " + p.ID
	id, scope := p.ID, p.Scope
	plugins := m.plugins
	return m, func() tea.Msg {
		err := plugins.Enable(id, scope)
		return pluginOpDoneMsg{kind: "enable", id: id, err: err}
	}
}

func (m *pluginModal) beginDisableConfirm() (modal, tea.Cmd) {
	p, ok := m.selected()
	if !ok {
		return m, nil
	}
	m.phase = pluginPhaseConfirm
	m.confirmKind = pluginConfirmDisable
	m.confirmID = p.ID
	m.confirmSc = p.Scope
	m.detailScroll = 0
	m.confirmBody = []string{
		"Disable plugin " + p.ID + "?",
		"Files are preserved; contributions stop on next launch.",
		"Re-enable anytime with e.",
	}
	return m, nil
}

func (m *pluginModal) beginRemoveConfirm() (modal, tea.Cmd) {
	p, ok := m.selected()
	if !ok {
		return m, nil
	}
	m.phase = pluginPhaseConfirm
	m.confirmKind = pluginConfirmRemove
	m.confirmID = p.ID
	m.confirmSc = p.Scope
	m.detailScroll = 0
	m.confirmBody = []string{
		"Remove plugin " + p.ID + "?",
		"Deletes install files and lockfile entry (" + p.Scope + ").",
		"This cannot be undone without reinstalling.",
	}
	return m, nil
}

func (m *pluginModal) beginTrustConfirm() (modal, tea.Cmd) {
	p, ok := m.selected()
	if !ok || m.plugins == nil {
		return m, nil
	}
	if !p.HasExecutable {
		m.status = p.ID + " has no executable contributions"
		return m, nil
	}
	m.phase = pluginPhaseBusy
	m.busy = "loading trust review"
	id, scope := p.ID, p.Scope
	plugins := m.plugins
	return m, func() tea.Msg {
		prev, err := plugins.TrustPreview(id, scope)
		return pluginTrustPreviewMsg{id: id, scope: scope, preview: prev, err: err}
	}
}

func (m *pluginModal) beginUntrustConfirm() (modal, tea.Cmd) {
	p, ok := m.selected()
	if !ok {
		return m, nil
	}
	if p.TrustState == host.PluginTrustPassiveOnly || p.TrustState == host.PluginTrustNone {
		m.status = p.ID + " has no trust grant"
		return m, nil
	}
	m.phase = pluginPhaseConfirm
	m.confirmKind = pluginConfirmUntrust
	m.confirmID = p.ID
	m.confirmSc = p.Scope
	m.detailScroll = 0
	m.confirmBody = []string{
		"Revoke executable trust for " + p.ID + "?",
		"MCP/harness/shell-hook/process-pane activation stops on next launch.",
		"Passive contributions are unaffected.",
	}
	return m, nil
}

func (m *pluginModal) beginUpdatePreview() (modal, tea.Cmd) {
	p, ok := m.selected()
	if !ok || m.plugins == nil {
		return m, nil
	}
	if p.SourceType != "" && p.SourceType != "catalog" && p.UpdateAvailable == "" {
		// Still try; host may resolve registry from lockfile.
	}
	m.phase = pluginPhaseBusy
	m.busy = "previewing update for " + p.ID
	id, scope, reg := p.ID, p.Scope, m.registry
	plugins := m.plugins
	return m, func() tea.Msg {
		rev, err := plugins.PreviewUpdate(context.Background(), id, scope, reg)
		return pluginUpdatePreviewMsg{id: id, scope: scope, review: rev, err: err}
	}
}

func (m *pluginModal) beginCatalog() (modal, tea.Cmd) {
	if m.registry == "" {
		m.phase = pluginPhaseInput
		m.inputKind = pluginInputCatalogRegistry
		m.inputBuf = ""
		m.inputHint = "catalog registry URL"
		return m, nil
	}
	m.phase = pluginPhaseInput
	m.inputKind = pluginInputCatalogQuery
	m.inputBuf = ""
	m.inputHint = "search query (empty = all)"
	return m, nil
}

func (m *pluginModal) beginOutdated() (modal, tea.Cmd) {
	if m.plugins == nil {
		return m, nil
	}
	m.phase = pluginPhaseBusy
	m.busy = "checking outdated"
	plugins := m.plugins
	reg := m.registry
	return m, func() tea.Msg {
		list, err := plugins.CheckOutdated(context.Background(), reg)
		return pluginOutdatedMsg{list: list, err: err}
	}
}

func (m *pluginModal) submitInput() (modal, tea.Cmd) {
	val := strings.TrimSpace(m.inputBuf)
	switch m.inputKind {
	case pluginInputRegistry:
		if val == "" {
			m.status = "registry URL required"
			return m, nil
		}
		m.registry = val
		if src := strings.TrimSpace(m.pendingInstall); src != "" {
			m.pendingInstall = ""
			return m.startInstall(src, host.PluginScopeGlobal)
		}
		m.phase = pluginPhaseBrowse
		m.status = "registry set"
		return m, nil
	case pluginInputCatalogRegistry:
		if val == "" {
			m.status = "registry URL required"
			return m, nil
		}
		m.registry = val
		m.inputKind = pluginInputCatalogQuery
		m.inputBuf = ""
		m.inputHint = "search query (empty = all)"
		m.status = "registry set"
		return m, nil
	case pluginInputCatalogQuery:
		if m.plugins == nil {
			return m, nil
		}
		m.phase = pluginPhaseBusy
		m.busy = "searching catalog"
		plugins := m.plugins
		reg := m.registry
		q := val
		return m, func() tea.Msg {
			hits, err := plugins.Search(context.Background(), reg, q)
			return pluginCatalogMsg{hits: hits, err: err}
		}
	case pluginInputInstall:
		if val == "" {
			m.status = "install source required"
			return m, nil
		}
		if strings.HasPrefix(val, "catalog:") && m.registry == "" {
			m.pendingInstall = val
			m.inputKind = pluginInputRegistry
			m.inputBuf = ""
			m.inputHint = "catalog registry URL"
			m.status = "registry required for catalog install"
			return m, nil
		}
		return m.startInstall(val, host.PluginScopeGlobal)
	}
	return m, nil
}

func (m *pluginModal) startInstall(source, scope string) (modal, tea.Cmd) {
	if m.plugins == nil {
		return m, nil
	}
	m.phase = pluginPhaseBusy
	m.busy = "installing " + source
	plugins := m.plugins
	reg := m.registry
	return m, func() tea.Msg {
		res, err := plugins.Install(context.Background(), source, scope, reg)
		return pluginOpDoneMsg{kind: "install", id: res.ID, version: res.Version, err: err}
	}
}

func (m *pluginModal) runConfirm() (modal, tea.Cmd) {
	if m.plugins == nil {
		m.phase = pluginPhaseBrowse
		return m, nil
	}
	id, scope := m.confirmID, m.confirmSc
	plugins := m.plugins
	kind := m.confirmKind
	m.phase = pluginPhaseBusy
	switch kind {
	case pluginConfirmRemove:
		m.busy = "removing " + id
		return m, func() tea.Msg {
			err := plugins.Remove(id, scope, true)
			return pluginOpDoneMsg{kind: "remove", id: id, err: err}
		}
	case pluginConfirmTrust:
		m.busy = "trusting " + id
		return m, func() tea.Msg {
			err := plugins.Trust(id, scope)
			return pluginOpDoneMsg{kind: "trust", id: id, err: err}
		}
	case pluginConfirmUntrust:
		m.busy = "untrusting " + id
		return m, func() tea.Msg {
			err := plugins.Untrust(id, scope)
			return pluginOpDoneMsg{kind: "untrust", id: id, err: err}
		}
	case pluginConfirmDisable:
		m.busy = "disabling " + id
		return m, func() tea.Msg {
			err := plugins.Disable(id, scope)
			return pluginOpDoneMsg{kind: "disable", id: id, err: err}
		}
	case pluginConfirmUpdate:
		m.busy = "updating " + id
		reg := m.registry
		return m, func() tea.Msg {
			res, err := plugins.Update(context.Background(), id, scope, reg, true)
			return pluginOpDoneMsg{kind: "update", id: id, version: res.Version, err: err}
		}
	default:
		m.phase = pluginPhaseBrowse
		return m, nil
	}
}

// applyMsg handles async results delivered via Model.Update.
func (m *pluginModal) applyMsg(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case pluginOpDoneMsg:
		m.phase = pluginPhaseBrowse
		m.busy = ""
		m.confirmBody = nil
		if msg.err != nil {
			m.status = msg.err.Error()
			// Prior state preserved by host; just reload to reflect truth.
			m.reload()
			return nil
		}
		m.reload()
		switch msg.kind {
		case "install":
			m.status = fmt.Sprintf("installed %s %s (next launch)", msg.id, msg.version)
		case "update":
			m.status = fmt.Sprintf("updated %s → %s (next launch; re-trust if needed)", msg.id, msg.version)
		case "remove":
			m.status = "removed " + msg.id
		case "enable":
			m.status = "enabled " + msg.id + " (next launch)"
		case "disable":
			m.status = "disabled " + msg.id + " (next launch)"
		case "trust":
			m.status = "trusted " + msg.id + " (executables on next launch)"
		case "untrust":
			m.status = "untrusted " + msg.id
		default:
			m.status = msg.kind + " ok: " + msg.id
		}
	case pluginTrustPreviewMsg:
		m.busy = ""
		if msg.err != nil {
			m.phase = pluginPhaseBrowse
			m.status = msg.err.Error()
			return nil
		}
		m.phase = pluginPhaseConfirm
		m.confirmKind = pluginConfirmTrust
		m.confirmID = msg.id
		m.confirmSc = msg.scope
		m.trustPrev = msg.preview
		m.confirmBody = append([]string(nil), msg.preview.ReviewLines...)
		m.detailScroll = 0
	case pluginUpdatePreviewMsg:
		m.busy = ""
		if msg.err != nil {
			m.phase = pluginPhaseBrowse
			m.status = msg.err.Error()
			return nil
		}
		m.phase = pluginPhaseConfirm
		m.confirmKind = pluginConfirmUpdate
		m.confirmID = msg.id
		m.confirmSc = msg.scope
		m.updateRev = msg.review
		m.confirmBody = strings.Split(strings.TrimSuffix(msg.review.Summary, "\n"), "\n")
		m.detailScroll = 0
	case pluginCatalogMsg:
		m.busy = ""
		if msg.err != nil {
			m.phase = pluginPhaseBrowse
			m.status = msg.err.Error()
			return nil
		}
		m.catalogHits = msg.hits
		m.catCursor = 0
		m.catFilter = ""
		m.phase = pluginPhaseCatalog
		if len(msg.hits) == 0 {
			m.status = "no catalog matches"
		} else {
			m.status = fmt.Sprintf("%d catalog hit(s)", len(msg.hits))
		}
	case pluginOutdatedMsg:
		m.busy = ""
		m.phase = pluginPhaseBrowse
		if msg.err != nil {
			m.status = msg.err.Error()
			return nil
		}
		if len(msg.list) == 0 {
			m.status = "all catalog plugins up to date"
			return nil
		}
		// Merge update-available into list rows.
		byKey := map[string]string{}
		for _, p := range msg.list {
			byKey[p.ID+"@"+p.Scope] = p.UpdateAvailable
		}
		for i := range m.all {
			if v := byKey[m.all[i].ID+"@"+m.all[i].Scope]; v != "" {
				m.all[i].UpdateAvailable = v
			}
		}
		m.status = fmt.Sprintf("%d update(s) available — select and press U", len(msg.list))
	}
	return nil
}

func (m *pluginModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))

	switch m.phase {
	case pluginPhaseBusy:
		body := wrapToWidth(st.Muted.Render(m.busy+th.Icons.Ellipsis), inner)
		return ui.Dialog(th, ui.DialogOpts{
			Title: "Plugins",
			Hint:  "working" + th.Icons.Ellipsis,
			Width: width,
		}, body)
	case pluginPhaseConfirm:
		return m.viewConfirm(width, th, inner)
	case pluginPhaseInput:
		return m.viewInput(width, th, inner)
	case pluginPhaseDetail:
		return m.viewDetail(width, th, inner)
	case pluginPhaseCatalog:
		return m.viewCatalog(width, th, inner)
	default:
		return m.viewBrowse(width, th, inner)
	}
}

func (m *pluginModal) viewBrowse(width int, th theme.Theme, inner int) string {
	st := th.S()
	var body string
	switch {
	case m.loadErr != "":
		body = wrapToWidth(st.Error.Render(sanitizeDisplayData(m.loadErr)), inner)
	default:
		list := m.filtered()
		if m.cursor >= len(list) {
			m.cursor = max(0, len(list)-1)
		}
		items := make([]ui.ListItem, len(list))
		for i, p := range list {
			items[i] = ui.ListItem{
				Label:    sanitizeDisplayData(pluginListLabel(p)),
				Detail:   sanitizeDisplayData(pluginListDetail(th, p)),
				Current:  false,
				Disabled: p.Status == "invalid",
			}
		}
		empty := "no plugins installed — i install, c catalog"
		if m.filter != "" {
			empty = "no matches for \"" + sanitizeDisplayData(m.filter) + "\""
		}
		body = ui.List(th, ui.ListOpts{
			Items:      items,
			Cursor:     m.cursor,
			Width:      inner,
			Visible:    pluginModalVisible,
			ShowFilter: true,
			Filter:     sanitizeDisplayData(m.filter),
			Total:      len(m.all),
			Empty:      empty,
		})
		if m.status != "" {
			body = body + "\n" + wrapToWidth(st.Muted.Render(sanitizeDisplayData(m.status)), inner)
		}
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Plugins",
		Hint:  dotJoin(th, "↑/↓", "enter detail", "ctrl+e enable", "ctrl+t trust", "ctrl+x remove", "U update", "ctrl+i install", "ctrl+s catalog", "esc"),
		Width: width,
	}, body)
}

func pluginListLabel(p host.PluginInfo) string {
	// APS packages are listed by Agent Plugins name (host ID). DisplayName is
	// detail-only so identity stays stable.
	name := p.ID
	if p.Format != "agent-plugins" && p.Name != "" && p.Name != p.ID {
		name = p.Name
	}
	ver := p.Version
	if ver == "" {
		ver = "?"
	}
	return name + "  " + ver
}

func pluginListDetail(th theme.Theme, p host.PluginInfo) string {
	parts := []string{p.Scope, p.Status}
	if p.Format != "" {
		label := p.Format
		if p.Format == "legacy" {
			label = "legacy (deprecated)"
		}
		parts = append(parts, label)
	}
	parts = append(parts, "trust:"+p.TrustState)
	if p.SourceType != "" {
		parts = append(parts, p.SourceType)
	}
	if n := pluginContribCount(p); n > 0 {
		parts = append(parts, fmt.Sprintf("%d contrib", n))
	}
	if p.UpdateAvailable != "" {
		parts = append(parts, "↑"+p.UpdateAvailable)
	}
	return strings.Join(parts, themedSpace(th.Spacing.XS)+th.Icons.Dot+themedSpace(th.Spacing.XS))
}

func pluginFormatLabel(p host.PluginInfo) string {
	switch p.Format {
	case "legacy":
		return "legacy (deprecated)"
	default:
		return p.Format
	}
}

func pluginContribCount(p host.PluginInfo) int {
	return p.Agents + p.Skills + p.Workflows + p.Themes + p.Providers +
		len(p.MCP) + len(p.Harnesses) + p.Hooks + p.Panes
}

func (m *pluginModal) viewDetail(width int, th theme.Theme, inner int) string {
	p, ok := m.selected()
	if !ok {
		m.phase = pluginPhaseBrowse
		return m.viewBrowse(width, th, inner)
	}
	lines := pluginDetailLines(th, p)
	const maxBody = 18
	if m.detailScroll > max(0, len(lines)-maxBody) {
		m.detailScroll = max(0, len(lines)-maxBody)
	}
	visible := lines
	if len(lines) > maxBody {
		end := min(len(lines), m.detailScroll+maxBody)
		visible = lines[m.detailScroll:end]
	}
	body := wrapToWidth(strings.Join(visible, "\n"), inner)
	hint := dotJoin(th, "↑/↓ scroll", "ctrl+e/t/x"+themedSpace(th.Spacing.XS)+th.Icons.Dot+themedSpace(th.Spacing.XS)+"U actions", "enter back", "esc back")
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Plugin " + sanitizeDisplayData(p.ID),
		Hint:  hint,
		Width: width,
	}, body)
}

func pluginDetailLines(th theme.Theme, p host.PluginInfo) []string {
	st := th.S()
	var lines []string
	kv := func(k, v string) {
		if strings.TrimSpace(v) == "" {
			return
		}
		lines = append(lines, st.Muted.Render(k)+themedSpace(th.Spacing.SM)+st.Text.Render(sanitizeDisplayData(v)))
	}
	kv("id", p.ID)
	kv("name", p.Name)
	kv("displayName", p.DisplayName)
	kv("format", pluginFormatLabel(p))
	kv("$schema", p.Schema)
	kv("version", p.Version)
	kv("scope", p.Scope)
	kv("status", p.Status)
	kv("trust", p.TrustState)
	kv("digest", p.Digest)
	kv("source", p.SourceLabel)
	if p.UpdateAvailable != "" {
		kv("update", p.UpdateAvailable+" available")
	}
	if p.LoadError != "" {
		lines = append(lines, st.Error.Render(sanitizeDisplayData(p.LoadError)))
	}
	if p.Format == "legacy" {
		lines = append(lines, st.Warning.Render("deprecated: Strike-native plugin.json (migrate with strike plugin migrate)"))
	}
	// Contribution counts
	var counts []string
	add := func(label string, n int) {
		if n > 0 {
			counts = append(counts, fmt.Sprintf("%s=%d", label, n))
		}
	}
	add("agents", p.Agents)
	add("skills", p.Skills)
	add("workflows", p.Workflows)
	add("themes", p.Themes)
	add("providers", p.Providers)
	add("mcp", len(p.MCP))
	add("harnesses", len(p.Harnesses))
	add("hooks", p.Hooks)
	add("panes", p.Panes)
	if len(counts) > 0 {
		kv("contrib", strings.Join(counts, " "))
	}
	if len(p.Capabilities) > 0 {
		kv("caps", strings.Join(p.Capabilities, ", "))
	}
	for _, m := range p.MCP {
		cmd := m.Command
		if cmd == "" {
			cmd = m.URL
		}
		line := "mcp " + m.Name
		if m.Transport != "" {
			line += " [" + m.Transport + "]"
		}
		if cmd != "" {
			line += " → " + cmd
		}
		if len(m.EnvKeys) > 0 {
			line += " env:" + strings.Join(m.EnvKeys, ",")
		}
		lines = append(lines, st.Text.Render(sanitizeDisplayData(line)))
	}
	for _, h := range p.Harnesses {
		line := "harness " + h.Name
		if h.Command != "" {
			line += " → " + h.Command
		}
		lines = append(lines, st.Text.Render(sanitizeDisplayData(line)))
	}
	if len(p.Findings) > 0 {
		lines = append(lines, st.Title.Render("Findings"))
		for _, f := range p.Findings {
			lines = append(lines, st.Warning.Render(sanitizeDisplayData(f)))
		}
	}
	lines = append(lines, st.Muted.Render("Changes apply on next Strike launch."))
	return lines
}

func (m *pluginModal) viewConfirm(width int, th theme.Theme, inner int) string {
	st := th.S()
	title := "Confirm"
	tone := ui.ToneWarning
	switch m.confirmKind {
	case pluginConfirmRemove:
		title = "Remove plugin"
		tone = ui.ToneDanger
	case pluginConfirmTrust:
		title = "Trust plugin"
	case pluginConfirmUntrust:
		title = "Untrust plugin"
	case pluginConfirmUpdate:
		title = "Update plugin"
	case pluginConfirmDisable:
		title = "Disable plugin"
	}
	lines := m.confirmBody
	if len(lines) == 0 {
		lines = []string{"Confirm action on " + m.confirmID + "?"}
	}
	const maxBody = 16
	if m.detailScroll > max(0, len(lines)-maxBody) {
		m.detailScroll = max(0, len(lines)-maxBody)
	}
	visible := lines
	if len(lines) > maxBody {
		end := min(len(lines), m.detailScroll+maxBody)
		visible = lines[m.detailScroll:end]
	}
	styled := make([]string, 0, len(visible)+1)
	for i, line := range visible {
		s := st.Text
		if i == 0 {
			s = st.WarningStrong
		}
		styled = append(styled, s.Render(sanitizeDisplayData(line)))
	}
	body := wrapToWidth(strings.Join(styled, "\n"), inner)
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, "y/enter confirm", "n/esc cancel", "↑/↓ scroll"),
		Width: width,
		Tone:  tone,
	}, body)
}

func (m *pluginModal) viewInput(width int, th theme.Theme, inner int) string {
	st := th.S()
	title := "Input"
	switch m.inputKind {
	case pluginInputRegistry, pluginInputCatalogRegistry:
		title = "Catalog registry"
	case pluginInputInstall:
		title = "Install plugin"
	case pluginInputCatalogQuery:
		title = "Catalog search"
	}
	lines := []string{
		st.Muted.Render(sanitizeDisplayData(m.inputHint)),
		st.Input.Render(sanitizeDisplayData(m.inputBuf)) + st.InputCursor.Render(th.Icons.InputCursor),
	}
	if m.status != "" {
		lines = append(lines, st.Muted.Render(sanitizeDisplayData(m.status)))
	}
	body := wrapToWidth(strings.Join(lines, "\n"), inner)
	return ui.Dialog(th, ui.DialogOpts{
		Title: title,
		Hint:  dotJoin(th, "type value", "enter submit", "esc cancel"),
		Width: width,
	}, body)
}

func (m *pluginModal) viewCatalog(width int, th theme.Theme, inner int) string {
	st := th.S()
	list := m.filteredCatalog()
	if m.catCursor >= len(list) {
		m.catCursor = max(0, len(list)-1)
	}
	items := make([]ui.ListItem, len(list))
	for i, h := range list {
		label := h.ID
		if h.Name != "" && h.Name != h.ID {
			label = h.Name
		}
		detail := h.Version
		if h.Description != "" {
			detail = detailJoin(th, detail, h.Description)
		}
		items[i] = ui.ListItem{
			Label:  sanitizeDisplayData(label),
			Detail: sanitizeDisplayData(detail),
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.catCursor,
		Width:      inner,
		Visible:    pluginModalVisible,
		ShowFilter: true,
		Filter:     sanitizeDisplayData(m.catFilter),
		Total:      len(m.catalogHits),
		Empty:      "no catalog packages",
	})
	if m.status != "" {
		body = body + "\n" + wrapToWidth(st.Muted.Render(sanitizeDisplayData(m.status)), inner)
	}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Plugin catalog",
		Hint:  dotJoin(th, "type filter", "enter install", "esc back"),
		Width: width,
	}, body)
}

// --- async messages ---

type pluginOpDoneMsg struct {
	kind    string
	id      string
	version string
	err     error
}

type pluginTrustPreviewMsg struct {
	id      string
	scope   string
	preview host.PluginTrustPreview
	err     error
}

type pluginUpdatePreviewMsg struct {
	id     string
	scope  string
	review host.PluginUpdateReview
	err    error
}

type pluginCatalogMsg struct {
	hits []host.PluginCatalogHit
	err  error
}

type pluginOutdatedMsg struct {
	list []host.PluginInfo
	err  error
}
