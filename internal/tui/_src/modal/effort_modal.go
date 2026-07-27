package tui

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// effortChoice is one row in the effort picker: either a fixed ladder level or
// a config model variant that maps onto an effort.
type effortChoice struct {
	Label  string
	Detail string
	Effort protocol.Effort
}

// effortModal is the centered reasoning-effort picker. When the active model
// declares config variants, those appear first (resolved to effort levels);
// otherwise the fixed protocol ladder is shown. Enter switches, ctrl+d saves
// the level as a global default.
type effortModal struct {
	current  protocol.Effort
	choices  []effortChoice
	cursor   int
	ops      chan<- protocol.Op
	settings host.Settings
}

func newEffortModal(current protocol.Effort, ops chan<- protocol.Op, settings host.Settings) *effortModal {
	return newEffortModalWithChoices(current, ops, settings, ladderEffortChoices())
}

func newEffortModalWithChoices(current protocol.Effort, ops chan<- protocol.Op, settings host.Settings, choices []effortChoice) *effortModal {
	if len(choices) == 0 {
		choices = ladderEffortChoices()
	}
	m := &effortModal{current: current, choices: choices, ops: ops, settings: settings}
	for i, c := range choices {
		if c.Effort == current {
			m.cursor = i
			break
		}
	}
	return m
}

func ladderEffortChoices() []effortChoice {
	levels := protocol.Efforts()
	out := make([]effortChoice, len(levels))
	for i, level := range levels {
		out[i] = effortChoice{Label: string(level), Detail: level.Describe(), Effort: level}
	}
	return out
}

// loadEffortChoicesCmd builds picker rows: config variants (when present) plus
// the standard ladder. Variants without a resolvable effort are skipped.
func loadEffortChoicesCmd(catalog host.Catalog, provider, model string, current protocol.Effort, ops chan<- protocol.Op, settings host.Settings) tea.Cmd {
	return func() tea.Msg {
		choices := ladderEffortChoices()
		if catalog != nil && provider != "" && model != "" {
			infos, err := catalog.Models(context.Background(), provider)
			if err == nil {
				for _, info := range infos {
					if info.ID != model || len(info.VariantIDs) == 0 {
						continue
					}
					var variants []effortChoice
					for _, id := range info.VariantIDs {
						effort, ok, err := catalog.ResolveVariant(context.Background(), provider, model, id)
						if err != nil || !ok {
							// Fall back to variant id as effort name when it matches the ladder.
							if level, parseOK := protocol.ParseEffort(id); parseOK && level != protocol.EffortDefault {
								variants = append(variants, effortChoice{
									Label:  id,
									Detail: "variant → " + string(level),
									Effort: level,
								})
							}
							continue
						}
						level, parseOK := protocol.ParseEffort(effort)
						if !parseOK || level == protocol.EffortDefault {
							continue
						}
						variants = append(variants, effortChoice{
							Label:  id,
							Detail: "variant → " + string(level),
							Effort: level,
						})
					}
					if len(variants) > 0 {
						choices = append(variants, choices...)
					}
					break
				}
			}
		}
		return effortChoicesLoadedMsg{
			modal: newEffortModalWithChoices(current, ops, settings, choices),
		}
	}
}

type effortChoicesLoadedMsg struct {
	modal *effortModal
}

func (m *effortModal) update(msg tea.KeyMsg) (modal, tea.Cmd) {
	if isEscape(msg) {
		return nil, nil
	}
	switch msg.String() {
	case "up", "k", "ctrl+p":
		if m.cursor > 0 {
			m.cursor--
		}
		return m, nil
	case "down", "j", "ctrl+n":
		if m.cursor < len(m.choices)-1 {
			m.cursor++
		}
		return m, nil
	case "enter":
		if m.cursor >= len(m.choices) {
			return m, nil
		}
		ops, level := m.ops, m.choices[m.cursor].Effort
		return nil, func() tea.Msg {
			ops <- protocol.SetEffort{Level: level}
			return nil
		}
	case "ctrl+d":
		if m.cursor >= len(m.choices) {
			return m, nil
		}
		level := m.choices[m.cursor].Effort
		return m, saveDefaultsThroughCmd(m.settings, "", "", "", string(level), "", "effort "+string(level))
	default:
		return m, nil
	}
}

func (m *effortModal) view(width int, th theme.Theme) string {
	inner := max(1, ui.PanelInnerWidth(th, width))
	items := make([]ui.ListItem, len(m.choices))
	for i, c := range m.choices {
		label := c.Label
		if label == "" {
			label = string(c.Effort)
		}
		detail := c.Detail
		if detail == "" {
			detail = c.Effort.Describe()
		}
		items[i] = ui.ListItem{
			Label:   label,
			Detail:  detail,
			Current: c.Effort == m.current && (c.Label == string(c.Effort) || strings.TrimSpace(c.Label) == ""),
		}
		// Mark current when the choice effort matches, preferring ladder rows
		// for the Current glyph when both variant and ladder share a level.
		if c.Effort == m.current {
			items[i].Current = true
		}
	}
	// Prefer a single Current mark: first matching choice wins (variants first).
	seen := false
	for i := range items {
		if items[i].Current {
			if seen {
				items[i].Current = false
			}
			seen = true
		}
	}
	body := ui.List(th, ui.ListOpts{
		Items:   items,
		Cursor:  m.cursor,
		Width:   inner,
		Visible: min(12, max(len(m.choices), 1)),
	})
	return ui.Dialog(th, ui.DialogOpts{
		Title: "Reasoning effort",
		Hint:  dotJoin(th, "up/down/j/k move", "enter select", "ctrl+d set default", "esc close"),
		Width: width,
	}, body)
}
