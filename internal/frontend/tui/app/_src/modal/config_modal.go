package tui

import (
	"fmt"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

const configModalVisible = 14

// configModal is the /config file picker: fixed primary slots plus existing
// agents/skills/themes/workflows under global and project .strike roots.
type configModal struct {
	entries  []host.ConfigFileRef
	filtered []host.ConfigFileRef
	cursor   int
	filter   string
	// forceNano opens with nano resolution instead of $VISUAL/$EDITOR.
	forceNano bool
	// returnToSettings reopens /settings menu on esc when launched from there.
	returnToSettings bool
	services         host.Services
	ops              chan<- protocol.Op
	workDir          string
	th               theme.Theme
}

func newConfigModal(services host.Services, ops chan<- protocol.Op, th theme.Theme, workDir string, forceNano, returnToSettings bool) *configModal {
	m := &configModal{
		forceNano:        forceNano,
		returnToSettings: returnToSettings,
		services:         services,
		ops:              ops,
		workDir:          workDir,
		th:               th,
	}
	m.reload()
	return m
}

func (m *configModal) reload() {
	if m.services.ConfigFiles != nil {
		m.entries = m.services.ConfigFiles.List(m.workDir)
	} else {
		m.entries = nil
	}
	m.refilter()
}

func (m *configModal) refilter() {
	q := strings.ToLower(strings.TrimSpace(m.filter))
	if q == "" {
		m.filtered = append([]host.ConfigFileRef(nil), m.entries...)
	} else {
		out := make([]host.ConfigFileRef, 0, len(m.entries))
		for _, e := range m.entries {
			hay := strings.ToLower(e.Label + " " + e.Display + " " + e.Slot + " " + e.Kind + " " + string(e.Scope))
			if strings.Contains(hay, q) {
				out = append(out, e)
			}
		}
		m.filtered = out
	}
	if m.cursor >= len(m.filtered) {
		m.cursor = max(0, len(m.filtered)-1)
	}
}

func (m *configModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		if m.returnToSettings {
			return newSettingsModal(m.services, m.ops, m.th, m.workDir), nil
		}
		return nil, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n", "tab":
		if m.cursor < len(m.filtered)-1 {
			m.cursor++
		}
		return m, nil
	case "backspace":
		if m.filter != "" {
			m.filter = m.filter[:len(m.filter)-1]
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	case "n":
		return m, m.openSelectedCmd(true)
	case "enter":
		return m, m.openSelectedCmd(m.forceNano)
	default:
		if len(msg.Text) > 0 {
			m.filter += msg.Text
			m.cursor = 0
			m.refilter()
		}
		return m, nil
	}
}

func (m *configModal) openSelectedCmd(forceNano bool) tea.Cmd {
	if m.cursor < 0 || m.cursor >= len(m.filtered) {
		return nil
	}
	ref := m.filtered[m.cursor]
	return func() tea.Msg {
		return configFileOpenMsg{ref: ref, forceNano: forceNano}
	}
}

func (m *configModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := m.listItems(th)
	body := ui.List(th, ui.ListOpts{
		Items:      items,
		Cursor:     m.listCursor(),
		Width:      inner,
		Visible:    configModalVisible,
		ShowFilter: true,
		Filter:     m.filter,
		Total:      len(m.entries),
		Empty:      "no config files",
	})
	hintParts := []string{"enter open", "n nano", "esc close"}
	if m.returnToSettings {
		hintParts = []string{"enter open", "n nano", "esc back"}
	}
	if m.forceNano {
		hintParts = []string{"enter open (nano)", "esc close"}
		if m.returnToSettings {
			hintParts = []string{"enter open (nano)", "esc back"}
		}
	}
	hint := dotJoin(th, hintParts...)
	return ui.Dialog(th, ui.DialogOpts{
		Title: "config files",
		Hint:  hint,
		Width: width,
	}, body)
}

// configListRow is one rendered list row (section header or file).
type configListRow struct {
	header bool
	ref    host.ConfigFileRef
	label  string
	detail string
}

func (m *configModal) rows(th theme.Theme) []configListRow {
	th = th.Resolve()
	var out []configListRow
	var lastScope host.ConfigFileScope
	var haveScope bool
	for _, e := range m.filtered {
		if !haveScope || e.Scope != lastScope {
			header := "Global (~/.strike)"
			if e.Scope == host.ConfigScopeProject {
				header = "Project (./.strike)"
			}
			out = append(out, configListRow{header: true, label: header})
			lastScope = e.Scope
			haveScope = true
		}
		detail := e.Display
		if !e.Exists {
			if e.CanCreate {
				detail = "missing - enter creates"
			} else {
				detail = "missing"
			}
		}
		out = append(out, configListRow{
			ref:    e,
			label:  configRowLabel(th, e),
			detail: detail,
		})
	}
	return out
}

func (m *configModal) listItems(th theme.Theme) []ui.ListItem {
	rows := m.rows(th)
	items := make([]ui.ListItem, len(rows))
	for i, r := range rows {
		if r.header {
			items[i] = ui.ListItem{Label: r.label, Disabled: true}
			continue
		}
		items[i] = ui.ListItem{Label: r.label, Detail: r.detail}
	}
	return items
}

// listCursor maps the file-row cursor onto the rendered list (skipping headers).
func (m *configModal) listCursor() int {
	if len(m.filtered) == 0 {
		return 0
	}
	fileIdx := 0
	listIdx := 0
	var lastScope host.ConfigFileScope
	var haveScope bool
	for _, e := range m.filtered {
		if !haveScope || e.Scope != lastScope {
			listIdx++ // header
			lastScope = e.Scope
			haveScope = true
		}
		if fileIdx == m.cursor {
			return listIdx
		}
		fileIdx++
		listIdx++
	}
	return max(0, listIdx-1)
}

func configRowLabel(th theme.Theme, e host.ConfigFileRef) string {
	th = th.Resolve()
	name := e.Label
	if e.Slot != "" && e.Slot != "config" {
		if base := filepath.Base(e.Path); base != "" && base != "." {
			name = base
		}
	}
	mark := th.Icons.CheckboxOff
	if e.Exists {
		mark = th.Icons.Dot
	}
	if mark == "" {
		return name
	}
	return mark + " " + name
}

// configFileOpenMsg asks the root model to ensure + launch the editor.
type configFileOpenMsg struct {
	ref       host.ConfigFileRef
	forceNano bool
}

// parseConfigCommandArgs interprets /config arguments.
// Grammar: [nano] [global|project] [<slot>]
// Bare → open picker. Slot alone opens that primary when unambiguous.
func parseConfigCommandArgs(args []string) (forceNano bool, scope host.ConfigFileScope, slot string, err error) {
	for _, raw := range args {
		a := strings.ToLower(strings.TrimSpace(raw))
		if a == "" {
			continue
		}
		switch a {
		case "nano":
			forceNano = true
		case "global":
			scope = host.ConfigScopeGlobal
		case "project":
			scope = host.ConfigScopeProject
		case "config", "mcp", "providers", "keybinds":
			if slot != "" {
				return false, "", "", fmt.Errorf("usage: /config [nano] [global|project] [config|mcp|providers|keybinds]")
			}
			slot = a
		default:
			return false, "", "", fmt.Errorf("unknown config slot %q - try config, mcp, providers, keybinds", raw)
		}
	}
	return forceNano, scope, slot, nil
}

// findConfigRef locates a primary slot in refs, optionally scoped.
func findConfigRef(refs []host.ConfigFileRef, scope host.ConfigFileScope, slot string) (host.ConfigFileRef, bool) {
	var matches []host.ConfigFileRef
	for _, r := range refs {
		if r.Slot != slot {
			continue
		}
		if scope != "" && r.Scope != scope {
			continue
		}
		matches = append(matches, r)
	}
	if len(matches) == 1 {
		return matches[0], true
	}
	if len(matches) == 0 {
		return host.ConfigFileRef{}, false
	}
	// Ambiguous without scope: prefer global for bare slot shortcuts.
	if scope == "" {
		for _, r := range matches {
			if r.Scope == host.ConfigScopeGlobal {
				return r, true
			}
		}
		return matches[0], true
	}
	return host.ConfigFileRef{}, false
}
