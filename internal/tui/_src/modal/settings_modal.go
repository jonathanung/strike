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

// settingsModal is the /settings UI. v1 focuses on custom provider CRUD.
type settingsModal struct {
	services host.Services
	ops      chan<- protocol.Op
	th       theme.Theme
	items    []settingsItem
	cursor   int
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

func newSettingsModal(services host.Services, ops chan<- protocol.Op, th theme.Theme) *settingsModal {
	m := &settingsModal{services: services, ops: ops, th: th}
	m.reload()
	return m
}

func (m *settingsModal) reload() {
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

func (m *settingsModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		return nil, nil
	}
	if len(m.items) == 0 {
		return m, nil
	}
	switch msg.String() {
	case "up", "k":
		m.cursor = (m.cursor + len(m.items) - 1) % len(m.items)
	case "down", "j", "tab":
		m.cursor = (m.cursor + 1) % len(m.items)
	case "enter":
		return m.activate()
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

func (m *settingsModal) activate() (modal, tea.Cmd) {
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

func (m *settingsModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
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
		Hint:  dotJoin(th, "enter edit/add", "a add", "s set key", "d remove", "esc close"),
		Width: width,
	}, body)
}
