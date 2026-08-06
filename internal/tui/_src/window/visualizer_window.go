package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
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

// visualizerMaxFilesShown caps how many file paths render in the detail list.
// Remaining count is summarized as "+N more" so layout stays width-safe.
const visualizerMaxFilesShown = 5

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
	// Live multi-agent detail (roster / observability). Empty means unknown.
	Objective    string
	LastAction   string
	BlockReason  string
	FilesTouched []string
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

	// Detail block: objective / last action / block reason / files.
	// Children always show objective + last action (muted placeholder when
	// unknown) so selection is never an empty status-only card. Roots only
	// surface non-empty detail so the token stack stays primary.
	lines = append(lines, visualizerDetailLines(th, w.width, s, dash)...)

	// Root-oriented usage stack. Children keep unknown tokens as dashes.
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

	// Tokens-per-turn sparkline — labeled metric + scale so the graph is readable.
	// Hollow when no known samples (never fabricate zeros from missing usage).
	sparkW := min(w.width, max(8, w.width-2))
	if sparkW > 24 {
		sparkW = 24
	}
	if w.width >= 8 {
		lines = append(lines, "")
		lines = append(lines, wrapWindowText(st.Muted.Render(visualizerActivityHeading(th, s.Activity)), w.width))
		if scale := visualizerActivityScale(th, s.Activity); scale != "" {
			lines = append(lines, wrapWindowText(st.Muted.Render(scale), w.width))
		}
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

// visualizerDetailLines renders objective / last action / block / files rows.
// Never fabricates content: empty fields use the muted unknown marker or omit.
func visualizerDetailLines(th theme.Theme, width int, s visualizerStateMsg, dash string) []string {
	th = th.Resolve()
	st := th.S()
	isChild := strings.EqualFold(strings.TrimSpace(s.Kind), "child")
	var lines []string

	objective := strings.TrimSpace(s.Objective)
	if isChild || objective != "" {
		val := st.Muted.Render(dash)
		if objective != "" {
			val = st.Text.Render(sanitizeDisplayData(objective))
		}
		lines = append(lines, contextKVLine(th, width, "objective", val))
	}

	action := visualizerLastActionHint(s)
	if isChild || action != "" {
		val := st.Muted.Render(dash)
		if action != "" {
			val = st.Text.Render(sanitizeDisplayData(action))
		}
		lines = append(lines, contextKVLine(th, width, "action", val))
	}

	if block := visualizerBlockLine(th, width, s, dash); block != "" {
		lines = append(lines, block)
	}

	lines = append(lines, visualizerFilesLines(th, width, s.FilesTouched, dash)...)
	return lines
}

// visualizerLastActionHint prefers roster lastAction; falls back to an
// in-flight tool name, then the most recent tool — never invents labels.
func visualizerLastActionHint(s visualizerStateMsg) string {
	if a := strings.TrimSpace(s.LastAction); a != "" {
		return a
	}
	for _, tool := range s.Tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			continue
		}
		if !tool.Done {
			return name
		}
	}
	if len(s.Tools) > 0 {
		return strings.TrimSpace(s.Tools[0].Name)
	}
	return ""
}

// visualizerBlockLine surfaces blockReason for blocked / needs-attention nodes.
// Empty reason with an attention/blocked status still shows a muted placeholder.
func visualizerBlockLine(th theme.Theme, width int, s visualizerStateMsg, dash string) string {
	th = th.Resolve()
	st := th.S()
	reason := strings.TrimSpace(s.BlockReason)
	needs := reason != "" || visualizerNeedsBlockRow(s)
	if !needs {
		return ""
	}
	val := st.Muted.Render(dash)
	if reason != "" {
		val = st.Warning.Render(sanitizeDisplayData(reason))
	}
	return contextKVLine(th, width, "blocked", val)
}

func visualizerNeedsBlockRow(s visualizerStateMsg) bool {
	// Attention (needs you) always gets a block/reason row. Plain Error/failed
	// without a reason does not — a failed child is not "blocked".
	if s.State == theme.AgentStateAttention {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(s.StatusLabel)) {
	case string(protocol.ChildStatusBlocked), "needs you", "needs_attention", "attention":
		return true
	default:
		return false
	}
}

// visualizerFilesLines renders a bounded, width-safe files-touched section.
// Omits entirely when unknown (no fabricated paths).
func visualizerFilesLines(th theme.Theme, width int, files []string, dash string) []string {
	th = th.Resolve()
	st := th.S()
	clean := make([]string, 0, len(files))
	for _, f := range files {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		clean = append(clean, sanitizeDisplayData(f))
	}
	if len(clean) == 0 {
		return nil
	}
	total := len(clean)
	show := clean
	extra := 0
	if total > visualizerMaxFilesShown {
		show = clean[:visualizerMaxFilesShown]
		extra = total - visualizerMaxFilesShown
	}
	header := "files"
	if total > 1 || extra > 0 {
		header = "files (" + strconv.Itoa(total) + ")"
	}
	lines := []string{wrapWindowText(st.Muted.Render(header), width)}
	for _, f := range show {
		// Indent with theme spacing; truncate long paths.
		prefix := themedSpace(th.Spacing.SM)
		budget := max(0, width-ansi.StringWidth(prefix))
		path := welcomeTruncate(f, budget, th.Icons.Ellipsis)
		lines = append(lines, wrapWindowText(st.Text.Render(prefix+path), width))
	}
	if extra > 0 {
		more := dash + " +" + strconv.Itoa(extra) + " more"
		lines = append(lines, wrapWindowText(st.Muted.Render(more), width))
	}
	return lines
}

func visualizerFit(lines []string, height int) string {
	if height > 0 && len(lines) > height {
		lines = lines[:height]
	}
	return strings.Join(lines, "\n")
}

// visualizerActivityHeading names the sparkline metric (tokens per completed turn).
func visualizerActivityHeading(th theme.Theme, samples []float64) string {
	if len(samples) == 0 {
		return "tokens/turn"
	}
	n := len(samples)
	unit := "turns"
	if n == 1 {
		unit = "turn"
	}
	return dotJoin(th, "tokens/turn", strconv.Itoa(n)+" "+unit)
}

// visualizerActivityScale summarizes peak and latest sample in token units.
// Empty when there are no samples (heading alone is enough).
func visualizerActivityScale(th theme.Theme, samples []float64) string {
	if len(samples) == 0 {
		return ""
	}
	peak := samples[0]
	for _, v := range samples[1:] {
		if v > peak {
			peak = v
		}
	}
	last := samples[len(samples)-1]
	return dotJoin(th, "peak "+ui.FormatTokens(int(peak)), "last "+ui.FormatTokens(int(last)))
}

func visualizerStateGlyph(th theme.Theme, state theme.AgentState, status string) string {
	th = th.Resolve()
	ic := iconsFor(th)
	switch state {
	case theme.AgentStateWorking:
		return ic.Ellipsis
	case theme.AgentStateAttention:
		return ic.Bolt
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
		case string(protocol.ChildStatusBlocked):
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
