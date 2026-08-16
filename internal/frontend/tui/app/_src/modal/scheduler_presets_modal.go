package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// schedulerPresetsAppliedMsg is delivered after a successful ApplyGlobalPresets
// so a parked FTUE wizard can mark the step done. err is empty on success.
type schedulerPresetsAppliedMsg struct {
	ids []string
	err string
}

// schedulerPresetsModal is a checkbox picker over the host scheduler preset
// catalog. It previews limits/rules for the current selection, shows existing
// custom global rules, and writes only on explicit apply (enter/a).
type schedulerPresetsModal struct {
	catalog host.SchedulerPresets
	items   []host.SchedulerPreset
	// selected maps preset ID → checked.
	selected map[string]bool
	// baseline is the global presets list at open (for skip/cancel comparison).
	baseline []string
	// customLimits / customCommands summarize non-preset global config.
	customLimits   map[string]int
	customCommands []host.SchedulerCommandRule
	cursor         int
	// previewScroll offsets the preview block when many rules are selected.
	previewScroll int
	flash         string
	th            theme.Theme
	// loadErr is set when Global() fails at open; apply is still attempted.
	loadErr string
}

func newSchedulerPresetsModal(catalog host.SchedulerPresets, th theme.Theme) *schedulerPresetsModal {
	m := &schedulerPresetsModal{
		catalog:  catalog,
		selected: map[string]bool{},
		th:       th,
	}
	if catalog != nil {
		m.items = catalog.List()
		if st, err := catalog.Global(); err != nil {
			m.loadErr = err.Error()
		} else {
			m.baseline = append([]string(nil), st.Presets...)
			for _, id := range st.Presets {
				m.selected[id] = true
			}
			m.customLimits = st.Limits
			m.customCommands = st.Commands
		}
	}
	return m
}

func (m *schedulerPresetsModal) clampCursor() {
	if len(m.items) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.items) {
		m.cursor = len(m.items) - 1
	}
}

func (m *schedulerPresetsModal) selectedIDs() []string {
	if len(m.items) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.items))
	for _, p := range m.items {
		if m.selected[p.ID] {
			out = append(out, p.ID)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m *schedulerPresetsModal) toggle() {
	m.clampCursor()
	if len(m.items) == 0 {
		return
	}
	id := m.items[m.cursor].ID
	if m.selected[id] {
		delete(m.selected, id)
	} else {
		m.selected[id] = true
	}
	m.flash = ""
	m.previewScroll = 0
}

func (m *schedulerPresetsModal) update(msg tea.KeyPressMsg) (modal, tea.Cmd) {
	if isEscape(msg) || msg.String() == "q" {
		// Cancel: leave global scheduler config unchanged.
		return nil, nil
	}
	m.clampCursor()
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		m.flash = ""
		return m, nil
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
		m.flash = ""
		return m, nil
	case " ", "space", "x":
		m.toggle()
		return m, nil
	case "pgup", "ctrl+u":
		if m.previewScroll > 0 {
			m.previewScroll--
		}
		return m, nil
	case "pgdown", "ctrl+d":
		m.previewScroll++
		return m, nil
	case "a", "enter":
		return m.apply()
	default:
		return m, nil
	}
}

func (m *schedulerPresetsModal) apply() (modal, tea.Cmd) {
	if m.catalog == nil {
		m.flash = "scheduler presets unavailable"
		return m, nil
	}
	// Refuse write when we could not load the current global snapshot — an
	// empty selection would otherwise clear presets the user never saw.
	if m.loadErr != "" {
		m.flash = "cannot apply: current settings failed to load"
		return m, nil
	}
	ids := m.selectedIDs()
	cat := m.catalog
	// Keep the modal until the host write completes; success closes via
	// schedulerPresetsAppliedMsg, errors re-flash on this modal.
	return m, func() tea.Msg {
		if err := cat.ApplyGlobalPresets(ids); err != nil {
			return schedulerPresetsAppliedMsg{err: err.Error()}
		}
		return schedulerPresetsAppliedMsg{ids: ids}
	}
}

func (m *schedulerPresetsModal) view(width int, th theme.Theme) string {
	th = th.Resolve()
	st := th.S()
	inner := max(1, ui.PanelInnerWidth(th, width))
	m.clampCursor()

	var b strings.Builder
	intro := "Select build systems to rate-limit. Space toggles; enter applies to global config. Custom limits and command rules are kept."
	b.WriteString(wrapToWidth(st.Muted.Render(intro), inner))
	b.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))

	if m.loadErr != "" {
		b.WriteString(wrapToWidth(st.Warning.Render("could not load current settings: "+m.loadErr), inner))
		b.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))
	}

	if custom := m.customSummary(th); custom != "" {
		b.WriteString(wrapToWidth(st.Muted.Render(custom), inner))
		b.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))
	}

	if len(m.items) == 0 {
		b.WriteString(wrapToWidth(st.Muted.Render("No scheduler presets available."), inner))
	} else {
		items := make([]ui.ListItem, 0, len(m.items))
		for _, p := range m.items {
			// [x] / [ ] so selected (added to scheduler) vs unselected is obvious.
			mark := th.Icons.CheckboxOff
			if m.selected[p.ID] {
				mark = th.Icons.CheckboxOn
			}
			detail := p.Rationale
			if n := len(p.Rules); n > 0 {
				detail = fmt.Sprintf("%d rules", n)
				if len(p.Limits) > 0 {
					detail = detailJoin(th, detail, formatLimitsShort(p.Limits))
				}
			}
			items = append(items, ui.ListItem{
				Label:  mark + themedSpace(th.Spacing.Label) + p.Name,
				Detail: detail,
			})
		}
		visible := min(len(items), 8)
		b.WriteString(ui.List(th, ui.ListOpts{
			Items:   items,
			Cursor:  m.cursor,
			Width:   inner,
			Visible: visible,
			Empty:   "no presets",
		}))
	}

	if preview := m.previewBlock(th, inner); preview != "" {
		b.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))
		b.WriteString(preview)
	}

	if m.flash != "" {
		b.WriteString(strings.Repeat("\n", max(1, th.Spacing.SM)))
		b.WriteString(wrapToWidth(st.Muted.Render(m.flash), inner))
	}

	hints := []string{"↑/↓ move", "space toggle", "enter apply", "esc cancel"}
	return ui.Dialog(th, ui.DialogOpts{
		Title: "scheduler presets",
		Hint:  dotJoin(th, hints...),
		Width: width,
	}, b.String())
}

func (m *schedulerPresetsModal) customSummary(th theme.Theme) string {
	_ = th
	var parts []string
	if n := len(m.baseline); n > 0 {
		parts = append(parts, fmt.Sprintf("%d preset(s) already enabled", n))
	}
	if n := len(m.customLimits); n > 0 {
		parts = append(parts, fmt.Sprintf("%d custom limit(s) preserved", n))
	}
	if n := len(m.customCommands); n > 0 {
		parts = append(parts, fmt.Sprintf("%d custom rule(s) preserved", n))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Current global: " + strings.Join(parts, "; ") + "."
}

func (m *schedulerPresetsModal) previewBlock(th theme.Theme, inner int) string {
	th = th.Resolve()
	st := th.S()
	ids := m.selectedIDs()
	if len(ids) == 0 {
		return wrapToWidth(st.Muted.Render("Preview: none selected — apply clears global presets (custom rules stay)."), inner)
	}

	// Merge limits (tightest/lowest cap per pool) and collect rules for display.
	limits := map[string]int{}
	var rules []host.SchedulerPresetRule
	var names []string
	for _, p := range m.items {
		if !m.selected[p.ID] {
			continue
		}
		names = append(names, p.Name)
		for k, v := range p.Limits {
			if cur, ok := limits[k]; !ok || v < cur {
				limits[k] = v
			}
		}
		rules = append(rules, p.Rules...)
	}

	var lines []string
	lines = append(lines, "Preview — "+strings.Join(names, ", "))
	if len(limits) > 0 {
		lines = append(lines, "limits: "+formatLimitsShort(limits))
	}
	const maxRules = 12
	if m.previewScroll < 0 {
		m.previewScroll = 0
	}
	if m.previewScroll > 0 && m.previewScroll >= len(rules) {
		m.previewScroll = max(0, len(rules)-1)
	}
	start := m.previewScroll
	if start > len(rules) {
		start = 0
	}
	end := start + maxRules
	if end > len(rules) {
		end = len(rules)
	}
	for _, r := range rules[start:end] {
		lines = append(lines, fmt.Sprintf("  %s → %s", r.Pattern, r.Class))
	}
	if end < len(rules) {
		lines = append(lines, fmt.Sprintf("  %s +%d more rules", th.Icons.Ellipsis, len(rules)-end))
	}

	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteByte('\n')
		}
		style := st.Muted
		if i == 0 {
			style = st.Accent
		}
		b.WriteString(wrapToWidth(style.Render(line), inner))
	}
	return b.String()
}

func formatLimitsShort(limits map[string]int) string {
	if len(limits) == 0 {
		return ""
	}
	keys := make([]string, 0, len(limits))
	for k := range limits {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, limits[k]))
	}
	return strings.Join(parts, " ")
}

// applySchedulerPresetsApplied closes the picker on success and stamps the
// parked FTUE wizard. On error, keeps the picker open with a flash.
func (m *Model) applySchedulerPresetsApplied(msg schedulerPresetsAppliedMsg) tea.Cmd {
	if msg.err != "" {
		if sm, ok := m.modal.(*schedulerPresetsModal); ok {
			sm.flash = msg.err
			return nil
		}
		m.setNotice("scheduler presets: "+msg.err, true)
		return nil
	}

	// Success: dismiss picker and promote parked wizard.
	if _, ok := m.modal.(*schedulerPresetsModal); ok {
		m.modal = nil
	}
	mark := func(mod modal) {
		f, ok := mod.(*ftueModal)
		if !ok {
			return
		}
		f.schedulerDone = true
		f.schedulerSkipped = false
		n := len(msg.ids)
		switch n {
		case 0:
			f.flash = "scheduler presets cleared"
		case 1:
			f.flash = "scheduler preset saved: " + msg.ids[0]
		default:
			f.flash = fmt.Sprintf("scheduler presets saved (%d)", n)
		}
	}
	mark(m.modal)
	for _, q := range m.modalQueue {
		mark(q)
	}
	return m.afterModalClosed()
}
