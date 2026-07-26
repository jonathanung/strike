package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

const visualizerWindowID = "visualizer"

// visualizerTool is one recent tool row for the activity strip.
type visualizerTool struct {
	Name    string
	Done    bool
	IsError bool
}

// visualizerStateMsg is a snapshot of the selected session/agent node for the
// right-pane visualizer. Model owns live stats; the window only renders.
type visualizerStateMsg struct {
	SessionID         string
	Label             string
	Kind              string // "root" | "child" | ""
	State             theme.AgentState
	StatusLabel       string
	Input             protocol.TokenCount
	Output            protocol.TokenCount
	Used              protocol.TokenCount
	Source            string
	ContextLimit      int
	ContextLimitKnown bool
	// Cost from catalog rates + known token parts; CostOK=false means unknown.
	CostUSD     float64
	CostOK      bool
	CostPartial bool
	// Activity samples for the sparkline; empty means no known activity.
	Activity []float64
	Tools    []visualizerTool
}

// visualizerWindow shows status glyphs, token/cost (when known), an activity
// sparkline, and a recent-tool strip for the selected tree node.
type visualizerWindow struct {
	state  visualizerStateMsg
	width  int
	height int
}

func newVisualizerWindow() visualizerWindow {
	return visualizerWindow{}
}

func (w visualizerWindow) id() string { return visualizerWindowID }

func (w visualizerWindow) title() string { return "visualizer" }

func (w visualizerWindow) init() tea.Cmd { return nil }

func (w visualizerWindow) update(msg tea.Msg) (window, tea.Cmd) {
	if s, ok := msg.(visualizerStateMsg); ok {
		w.state = s
	}
	return w, nil
}

func (w visualizerWindow) resize(width, height int) window {
	w.width, w.height = max(0, width), max(0, height)
	return w
}

func (w visualizerWindow) view(th theme.Theme) string {
	if w.width <= 0 {
		return ""
	}
	th = th.Resolve()
	st := th.S()
	s := w.state
	dash := th.Icons.DetailSeparator
	ic := iconsFor(th)

	lines := make([]string, 0, 16)

	if strings.TrimSpace(s.SessionID) == "" && strings.TrimSpace(s.Label) == "" {
		lines = append(lines, wrapWindowText(st.Muted.Render("select a session in agents"), w.width))
		return visualizerFit(lines, w.height)
	}

	label := strings.TrimSpace(s.Label)
	if label == "" {
		label = shortSessionID(s.SessionID)
	}
	if label == "" {
		label = dash
	} else {
		label = sanitizeDisplayData(label)
	}
	lines = append(lines, contextKVLine(th, w.width, "node", label))

	if kind := strings.TrimSpace(s.Kind); kind != "" {
		lines = append(lines, contextKVLine(th, w.width, "kind", kind))
	}

	statusLabel := s.StatusLabel
	if statusLabel == "" {
		statusLabel = s.State.Label()
	}
	glyph := visualizerStateGlyph(th, s.State, statusLabel)
	statusStyle := th.AgentStateStyle(s.State)
	statusVal := statusStyle.Render(glyph + themedSpace(th.Spacing.XS) + sanitizeDisplayData(statusLabel))
	lines = append(lines, contextKVLine(th, w.width, "status", statusVal))

	// Tokens: never print measured zero for unknown sides.
	inStr := formatTokenCount(s.Input, dash)
	outStr := formatTokenCount(s.Output, dash)
	tokVal := st.Text.Render(dotJoin(th, "in "+inStr, "out "+outStr))
	lines = append(lines, contextKVLine(th, w.width, "tokens", tokVal))

	pair := formatContextTokenPair(s.Used, s.ContextLimit, s.ContextLimitKnown, dash)
	ratio := contextUsageRatio(s.Used, s.ContextLimit, s.ContextLimitKnown)
	barWidth := min(10, max(4, w.width/4))
	if w.width < 18 {
		barWidth = 0
	}
	ctxVal := st.Text.Render(pair)
	if barWidth > 0 {
		ctxVal = ui.Meter(th, barWidth, ratio) + themedSpace(th.Spacing.XS) + ctxVal
	}
	lines = append(lines, contextKVLine(th, w.width, "context", ctxVal))

	costVal := dash
	switch {
	case s.CostOK:
		costVal = formatSessionCostUSD(s.CostUSD)
		if s.CostPartial {
			costVal += " (partial)"
		}
	case s.Input.Known || s.Output.Known || s.Used.Known:
		// Have tokens but no catalog rate / incomplete pricing.
		costVal = dash + " (no rate)"
	}
	lines = append(lines, contextKVLine(th, w.width, "cost", st.Text.Render(costVal)))

	if s.Source != "" {
		lines = append(lines, contextKVLine(th, w.width, "source", s.Source))
	}

	// Activity sparkline — hollow when no known samples.
	sparkW := min(w.width, max(8, w.width-2))
	if sparkW > 24 {
		sparkW = 24
	}
	if w.width >= 8 {
		lines = append(lines, "")
		lines = append(lines, wrapWindowText(st.Muted.Render("activity"), w.width))
		lines = append(lines, wrapWindowText(ui.Sparkline(th, sparkW, s.Activity), w.width))
	}

	// Recent tool strip.
	if w.width >= 8 {
		lines = append(lines, "")
		lines = append(lines, wrapWindowText(st.Muted.Render("tools"), w.width))
		if len(s.Tools) == 0 {
			lines = append(lines, wrapWindowText(st.Muted.Render(dash+" none yet"), w.width))
		} else {
			for _, tool := range s.Tools {
				if w.height > 0 && len(lines) >= w.height {
					break
				}
				lines = append(lines, visualizerToolLine(th, w.width, tool, ic))
			}
		}
	}

	return visualizerFit(lines, w.height)
}

func visualizerFit(lines []string, height int) string {
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

func visualizerStateGlyph(th theme.Theme, state theme.AgentState, status string) string {
	th = th.Resolve()
	ic := iconsFor(th)
	switch state {
	case theme.AgentStateWorking:
		return ic.Ellipsis
	case theme.AgentStateAttention:
		return ic.Info
	case theme.AgentStateError:
		return ic.Err
	case theme.AgentStateDead:
		return ic.Dot
	default:
		// Child terminal statuses may still map Ready with a done/canceled label.
		switch status {
		case string(protocol.ChildStatusCompleted), "done":
			return ic.OK
		case string(protocol.ChildStatusFailed):
			return ic.Err
		case string(protocol.ChildStatusCanceled):
			return ic.Info
		case "running":
			return ic.Ellipsis
		default:
			return ic.OK
		}
	}
}

func visualizerToolLine(th theme.Theme, width int, tool visualizerTool, ic theme.Icons) string {
	th = th.Resolve()
	st := th.S()
	space := themedSpace(th.Spacing.XS)
	status := ic.Ellipsis
	statusStyle := st.Muted
	if tool.Done {
		if tool.IsError {
			status, statusStyle = ic.Err, st.Error
		} else {
			status, statusStyle = ic.OK, st.Success
		}
	}
	name := sanitizeDisplayData(tool.Name)
	if name == "" {
		name = "tool"
	}
	prefix := ic.Tool + space
	suffix := space + status
	budget := max(0, width-ansi.StringWidth(prefix)-ansi.StringWidth(suffix))
	line := st.ToolLabel.Render(prefix) +
		st.Text.Render(welcomeTruncate(name, budget, ic.Ellipsis)) +
		statusStyle.Render(suffix)
	return wrapWindowText(line, width)
}
