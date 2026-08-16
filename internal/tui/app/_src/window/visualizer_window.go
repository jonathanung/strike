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

// visualizerMaxOverlapsShown caps path-overlap rows in the detail list.
const visualizerMaxOverlapsShown = 4

// visualizerMaxOverlapHolders caps holder names shown per overlap row.
const visualizerMaxOverlapHolders = 3

// visualizerPathOverlap is one path-overlap warning on a selected node.
type visualizerPathOverlap struct {
	Path    string
	Policy  string
	Blocked bool
	Warning string
	// Holders are other claimants (names or short ids); empty when unknown.
	Holders []string
}

// visualizerVerification is claim-vs-verified state when a report was observed.
// Nil on the parent msg means unknown (no report) — never invent success.
type visualizerVerification struct {
	Claimed  bool
	Verified bool
	Passed   bool
	Summary  string
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

	// Multi-agent observability (#922). Empty/nil = unknown; do not invent zeros.
	// VIZ.2 (#923) renders Objective/LastAction/BlockReason/FilesTouched.
	Objective      string
	LastAction     string
	BlockReason    string
	FilesTouched   []string
	Budget         *protocol.AgentBudgetView
	EscalateKind   string
	EscalateReason string
	EscalateAction string
	PathOverlaps   []visualizerPathOverlap
	Verification   *visualizerVerification
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

	// Per-agent budget meters + escalation chrome (#924). Omitted when no
	// limits and no escalation — never draw full meters from zero limits.
	lines = append(lines, visualizerBudgetLines(th, w.width, s, dash)...)

	// Verification + path-conflict signals (#925). Omit when no report / no
	// overlaps — never invent a verified badge.
	lines = append(lines, visualizerVerificationLines(th, w.width, s, dash)...)
	lines = append(lines, visualizerConflictLines(th, w.width, s, dash)...)

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

// visualizerBudgetLines renders per-agent remaining/used affordances and
// escalation chrome. Only dimensions with limits appear; unlimited stays omitted
// (never 0% from a zero max). Session catalog cost is separate (cost row).
func visualizerBudgetLines(th theme.Theme, width int, s visualizerStateMsg, dash string) []string {
	th = th.Resolve()
	st := th.S()
	b := s.Budget
	hasLimits := visualizerBudgetHasLimits(b)
	hasEsc := visualizerHasEscalation(s)
	if !hasLimits && !hasEsc {
		return nil
	}

	var lines []string
	if esc := visualizerEscalationLine(th, width, s, dash); esc != "" {
		lines = append(lines, esc)
	}
	if !hasLimits {
		return lines
	}

	// Section header once we know at least one meter will render.
	meters := visualizerBudgetMeterRows(th, width, b)
	if len(meters) == 0 {
		return lines
	}
	lines = append(lines, wrapWindowText(st.Muted.Render("budget"), width))
	lines = append(lines, meters...)
	return lines
}

// visualizerBudgetHasLimits is true when any hard dimension is configured.
// Zero limits mean unlimited on the wire — do not treat as 0% used.
func visualizerBudgetHasLimits(b *protocol.AgentBudgetView) bool {
	if b == nil {
		return false
	}
	return b.MaxWallClockS > 0 ||
		b.MaxTokens > 0 ||
		b.MaxCostUSD > 0 ||
		b.MaxToolCalls > 0 ||
		b.MaxDangerousTools > 0
}

// visualizerHasEscalation is true when ChildEscalated fields or budget flags set.
func visualizerHasEscalation(s visualizerStateMsg) bool {
	if strings.TrimSpace(s.EscalateKind) != "" || strings.TrimSpace(s.EscalateReason) != "" {
		return true
	}
	if strings.TrimSpace(s.EscalateAction) != "" {
		return true
	}
	if s.Budget == nil {
		return false
	}
	return s.Budget.Escalated || s.Budget.Stall || s.Budget.Loop ||
		strings.TrimSpace(s.Budget.EscalateKind) != "" ||
		strings.TrimSpace(s.Budget.EscalateReason) != ""
}

// visualizerEscalationLine surfaces stall / loop / escalated with kind+reason.
func visualizerEscalationLine(th theme.Theme, width int, s visualizerStateMsg, dash string) string {
	th = th.Resolve()
	st := th.S()
	if !visualizerHasEscalation(s) {
		return ""
	}

	kind := strings.TrimSpace(s.EscalateKind)
	reason := strings.TrimSpace(s.EscalateReason)
	action := strings.TrimSpace(s.EscalateAction)
	if b := s.Budget; b != nil {
		if kind == "" {
			kind = strings.TrimSpace(b.EscalateKind)
		}
		if reason == "" {
			reason = strings.TrimSpace(b.EscalateReason)
		}
		// Prefer explicit stall/loop flags when kind empty.
		if kind == "" {
			switch {
			case b.Stall:
				kind = "stall"
			case b.Loop:
				kind = "loop"
			case b.Escalated:
				kind = "budget"
			}
		}
	}

	// Prefer human reason as the value (badge already marks escalated/stall).
	// Fall back to kind + action when reason is empty.
	var body string
	switch {
	case reason != "":
		body = sanitizeDisplayData(reason)
	case kind != "" && action != "":
		body = dotJoin(th, sanitizeDisplayData(kind), sanitizeDisplayData(action))
	case kind != "":
		body = sanitizeDisplayData(kind)
	case action != "":
		body = sanitizeDisplayData(action)
	default:
		body = dash
	}

	// Error tone when hard-interrupted; warning for soft finalizing / stall / loop.
	style := st.Warning
	tone := ui.ToneWarning
	if strings.EqualFold(action, protocol.EscalateActionInterrupted) {
		style = st.Error
		tone = ui.ToneError
	}
	// Badge carries kind when known (tool_calls / stall / loop); else "escalated".
	label := "escalated"
	if kind != "" {
		label = kind
	}
	val := style.Render(body)
	badge := ui.Badge(th, tone, label)
	return contextKVLine(th, width, "escalation", badge+themedSpace(th.Spacing.XS)+val)
}

// visualizerBudgetMeterRows builds one row per limited dimension.
func visualizerBudgetMeterRows(th theme.Theme, width int, b *protocol.AgentBudgetView) []string {
	if b == nil {
		return nil
	}
	th = th.Resolve()
	st := th.S()
	barWidth := min(10, max(4, width/4))
	if width < 18 {
		barWidth = 0
	}

	type dim struct {
		key   string
		pair  string
		ratio float64
		hot   bool // at/over limit
	}
	var dims []dim

	if b.MaxToolCalls > 0 {
		used, rem := budgetUsedRemaining(b.ToolCalls, b.MaxToolCalls, b.ToolCallsRemaining)
		dims = append(dims, dim{
			key:   "tools",
			pair:  budgetPairInt(used, b.MaxToolCalls, rem),
			ratio: budgetRatio(used, b.MaxToolCalls),
			hot:   used >= b.MaxToolCalls || (rem != nil && *rem <= 0),
		})
	}
	if b.MaxDangerousTools > 0 {
		used, rem := budgetUsedRemaining(b.DangerousTools, b.MaxDangerousTools, b.DangerousRemaining)
		dims = append(dims, dim{
			key:   "danger",
			pair:  budgetPairInt(used, b.MaxDangerousTools, rem),
			ratio: budgetRatio(used, b.MaxDangerousTools),
			hot:   used >= b.MaxDangerousTools || (rem != nil && *rem <= 0),
		})
	}
	if b.MaxTokens > 0 {
		used, rem := budgetUsedRemaining(b.TokensUsed, b.MaxTokens, b.TokensRemaining)
		dims = append(dims, dim{
			key:   "tok",
			pair:  budgetPairTokens(used, b.MaxTokens, rem),
			ratio: budgetRatio(used, b.MaxTokens),
			hot:   used >= b.MaxTokens || (rem != nil && *rem <= 0),
		})
	}
	if b.MaxCostUSD > 0 {
		used := b.CostUSDUsed
		var rem *float64
		if b.CostUSDRemaining != nil {
			r := *b.CostUSDRemaining
			rem = &r
			// Prefer remaining-derived used when wire used is zero but remaining set.
			if used <= 0 && r >= 0 && r <= b.MaxCostUSD {
				used = b.MaxCostUSD - r
			}
		}
		pair := formatSessionCostUSD(used) + "/" + formatSessionCostUSD(b.MaxCostUSD)
		if rem != nil {
			pair = formatSessionCostUSD(used) + "/" + formatSessionCostUSD(b.MaxCostUSD) +
				" (" + formatSessionCostUSD(*rem) + " left)"
		}
		ratio := 0.0
		if b.MaxCostUSD > 0 {
			ratio = used / b.MaxCostUSD
			if ratio > 1 {
				ratio = 1
			}
			if ratio < 0 {
				ratio = 0
			}
		}
		dims = append(dims, dim{
			key:   "cost",
			pair:  pair,
			ratio: ratio,
			hot:   used >= b.MaxCostUSD || (rem != nil && *rem <= 0),
		})
	}
	if b.MaxWallClockS > 0 {
		used, rem := budgetUsedRemaining(b.ElapsedS, b.MaxWallClockS, b.WallClockRemainingS)
		dims = append(dims, dim{
			key:   "wall",
			pair:  budgetPairDuration(used, b.MaxWallClockS, rem),
			ratio: budgetRatio(used, b.MaxWallClockS),
			hot:   used >= b.MaxWallClockS || (rem != nil && *rem <= 0),
		})
	}

	lines := make([]string, 0, len(dims))
	for _, d := range dims {
		pairStyle := st.Text
		if d.hot || b.Escalated {
			pairStyle = st.Warning
			if b.Escalated && d.hot {
				pairStyle = st.Error
			}
		}
		val := pairStyle.Render(d.pair)
		if barWidth > 0 {
			val = ui.Meter(th, barWidth, d.ratio) + themedSpace(th.Spacing.XS) + val
		}
		lines = append(lines, contextKVLine(th, width, d.key, val))
	}
	return lines
}

// budgetUsedRemaining prefers explicit remaining when present.
func budgetUsedRemaining(used, max int, remaining *int) (int, *int) {
	if remaining != nil {
		r := *remaining
		// Derive used from remaining when it is consistent with the max.
		if max > 0 && r >= 0 && r <= max {
			derived := max - r
			if used <= 0 || derived >= used {
				used = derived
			}
		}
		return used, remaining
	}
	return used, nil
}

func budgetRatio(used, max int) float64 {
	if max <= 0 {
		return -1 // hollow / unknown
	}
	r := float64(used) / float64(max)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func budgetPairInt(used, max int, remaining *int) string {
	pair := strconv.Itoa(used) + "/" + strconv.Itoa(max)
	if remaining != nil {
		pair += " (" + strconv.Itoa(*remaining) + " left)"
	}
	return pair
}

func budgetPairTokens(used, max int, remaining *int) string {
	pair := ui.FormatTokens(used) + "/" + ui.FormatTokens(max)
	if remaining != nil {
		pair += " (" + ui.FormatTokens(*remaining) + " left)"
	}
	return pair
}

func budgetPairDuration(usedS, maxS int, remaining *int) string {
	pair := formatBudgetSeconds(usedS) + "/" + formatBudgetSeconds(maxS)
	if remaining != nil {
		pair += " (" + formatBudgetSeconds(*remaining) + " left)"
	}
	return pair
}

func formatBudgetSeconds(sec int) string {
	if sec < 0 {
		sec = 0
	}
	if sec < 60 {
		return strconv.Itoa(sec) + "s"
	}
	if sec < 3600 {
		return strconv.Itoa(sec/60) + "m" + strconv.Itoa(sec%60) + "s"
	}
	h := sec / 3600
	m := (sec % 3600) / 60
	return strconv.Itoa(h) + "h" + strconv.Itoa(m) + "m"
}

// visualizerVerificationLines renders claim-vs-verified chrome when a report
// exists. Nil Verification ⇒ no badge (unknown stays unknown).
func visualizerVerificationLines(th theme.Theme, width int, s visualizerStateMsg, dash string) []string {
	th = th.Resolve()
	st := th.S()
	v := s.Verification
	if v == nil {
		return nil
	}
	label, tone, ok := visualizerVerificationLabel(v)
	if !ok {
		return nil
	}
	badge := ui.Badge(th, tone, label)
	summary := strings.TrimSpace(v.Summary)
	val := badge
	if summary != "" && !strings.EqualFold(summary, label) {
		// Distinct claimed vs verified: badge carries vocabulary; summary elaborates.
		sumStyle := st.Text
		switch tone {
		case ui.ToneWarning:
			sumStyle = st.Warning
		case ui.ToneError:
			sumStyle = st.Error
		case ui.ToneSuccess:
			sumStyle = st.Success
		}
		val = badge + themedSpace(th.Spacing.XS) + sumStyle.Render(sanitizeDisplayData(summary))
	}
	return []string{contextKVLine(th, width, "verify", val)}
}

// visualizerVerificationLabel mirrors header chip vocabulary (#809):
// verified / claimed / unverified. ok false when nothing should render.
func visualizerVerificationLabel(v *visualizerVerification) (label string, tone ui.Tone, ok bool) {
	if v == nil {
		return "", ui.ToneMuted, false
	}
	// Same precedence as verificationBadgeLabel (harness_trust.go).
	switch {
	case v.Verified && v.Passed:
		return "verified", ui.ToneSuccess, true
	case v.Claimed && !v.Verified:
		return "claimed", ui.ToneWarning, true
	case !v.Passed:
		return "unverified", ui.ToneError, true
	case v.Passed:
		return "verified", ui.ToneSuccess, true
	default:
		// Report observed but no decisive flags — still surface as unverified
		// so a present pointer never looks like silent success.
		return "unverified", ui.ToneError, true
	}
}

// visualizerConflictLines renders latest path overlaps with policy + holders.
// Conflicting state uses warn/error tokens; omitted when no overlaps.
func visualizerConflictLines(th theme.Theme, width int, s visualizerStateMsg, dash string) []string {
	th = th.Resolve()
	st := th.S()
	list := s.PathOverlaps
	if len(list) == 0 {
		return nil
	}
	total := len(list)
	show := list
	extra := 0
	if total > visualizerMaxOverlapsShown {
		show = list[:visualizerMaxOverlapsShown]
		extra = total - visualizerMaxOverlapsShown
	}

	// Header badge: blocked (error) when any overlap blocked, else warn.
	anyBlocked := false
	for _, po := range list {
		if po.Blocked || strings.EqualFold(strings.TrimSpace(po.Policy), "block") {
			anyBlocked = true
			break
		}
	}
	headerTone := ui.ToneWarning
	headerLabel := "conflict"
	if anyBlocked {
		headerTone = ui.ToneError
		headerLabel = "blocked"
	}
	header := "conflicts"
	if total > 1 {
		header = "conflicts (" + strconv.Itoa(total) + ")"
	}
	lines := []string{
		wrapWindowText(
			st.Muted.Render(header)+themedSpace(th.Spacing.XS)+ui.Badge(th, headerTone, headerLabel),
			width,
		),
	}
	for _, po := range show {
		lines = append(lines, visualizerConflictRow(th, width, po, dash)...)
	}
	if extra > 0 {
		more := dash + " +" + strconv.Itoa(extra) + " more"
		lines = append(lines, wrapWindowText(st.Muted.Render(more), width))
	}
	return lines
}

func visualizerConflictRow(th theme.Theme, width int, po visualizerPathOverlap, dash string) []string {
	th = th.Resolve()
	st := th.S()
	pathStyle := st.Warning
	policyTone := ui.ToneWarning
	policyLabel := strings.TrimSpace(po.Policy)
	if policyLabel == "" {
		if po.Blocked {
			policyLabel = "block"
		} else {
			policyLabel = "warn"
		}
	}
	if po.Blocked || strings.EqualFold(policyLabel, "block") {
		pathStyle = st.Error
		policyTone = ui.ToneError
	}
	path := strings.TrimSpace(po.Path)
	if path == "" {
		path = dash
	} else {
		path = sanitizeDisplayData(path)
	}
	prefix := themedSpace(th.Spacing.SM)
	badge := ui.Badge(th, policyTone, policyLabel)
	// path + policy badge on one row; truncate path to fit.
	suffix := themedSpace(th.Spacing.XS) + badge
	budget := max(0, width-ansi.StringWidth(prefix)-ansi.StringWidth(suffix))
	pathShown := welcomeTruncate(path, budget, th.Icons.Ellipsis)
	row := st.Text.Render(prefix) + pathStyle.Render(pathShown) + suffix
	lines := []string{wrapWindowText(row, width)}

	// Holders (or count) on a muted sub-row when present.
	if holders := visualizerHolderSummary(po.Holders); holders != "" {
		hPrefix := themedSpace(th.Spacing.MD)
		hBudget := max(0, width-ansi.StringWidth(hPrefix))
		hText := welcomeTruncate("holders "+holders, hBudget, th.Icons.Ellipsis)
		lines = append(lines, wrapWindowText(st.Muted.Render(hPrefix+hText), width))
	} else if w := strings.TrimSpace(po.Warning); w != "" {
		// Fall back to warning text when holders unknown.
		hPrefix := themedSpace(th.Spacing.MD)
		hBudget := max(0, width-ansi.StringWidth(hPrefix))
		hText := welcomeTruncate(sanitizeDisplayData(w), hBudget, th.Icons.Ellipsis)
		lines = append(lines, wrapWindowText(st.Muted.Render(hPrefix+hText), width))
	}
	return lines
}

// visualizerHolderSummary formats holder names (bounded) or a count.
func visualizerHolderSummary(holders []string) string {
	clean := make([]string, 0, len(holders))
	for _, h := range holders {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		clean = append(clean, sanitizeDisplayData(h))
	}
	if len(clean) == 0 {
		return ""
	}
	show := clean
	extra := 0
	if len(clean) > visualizerMaxOverlapHolders {
		show = clean[:visualizerMaxOverlapHolders]
		extra = len(clean) - visualizerMaxOverlapHolders
	}
	out := strings.Join(show, ", ")
	if extra > 0 {
		out += " +" + strconv.Itoa(extra)
	}
	return out
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
