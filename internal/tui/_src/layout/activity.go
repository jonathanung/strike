package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

// activityKind classifies one row in the activity pane feed.
type activityKind int

const (
	activityTool activityKind = iota
	activityChild
	activityAttention
)

// activityEntry is one projected activity-pane row. Higher Seq is newer.
// DetailBody stays chronological (never reversed) for expanded views.
type activityEntry struct {
	ID         string
	Kind       activityKind
	Seq        int64
	Label      string
	Status     string
	Done       bool
	IsError    bool
	DetailBody string // tool output / attention detail; chrono order
}

// projectActivityEntries builds the flat activity feed newest-first.
// Order is stable for equal Seq (by ID). Covers parent tools, child lifecycle
// rows, and an attention row while a permission/question is pending.
// Replay and live paths share this projection (same cells/children inputs).
func projectActivityEntries(cells []cell, children []childActivity, awaitingPermission bool) []activityEntry {
	entries := make([]activityEntry, 0, len(cells)+len(children)+1)
	var seq int64

	// Tools in transcript order; later cells/calls get higher seq.
	for _, c := range cells {
		switch tc := c.(type) {
		case *toolCell:
			seq++
			entries = append(entries, toolActivityEntry(tc, seq))
		case *exploreCell:
			for _, call := range tc.calls {
				if call == nil {
					continue
				}
				seq++
				entries = append(entries, toolActivityEntry(call, seq))
			}
		}
	}

	// Children in spawn order; later spawns get higher seq.
	for i, ch := range children {
		seq++
		id := strings.TrimSpace(ch.sessionID)
		if id == "" || id == "child" {
			id = "child-ephemeral-" + itoa(i)
		} else {
			id = "child:" + id
		}
		label := strings.TrimSpace(ch.agent)
		if label == "" {
			label = "subagent"
		}
		if p := strings.TrimSpace(ch.prompt); p != "" {
			label = label + " " + p
		}
		entries = append(entries, activityEntry{
			ID:     id,
			Kind:   activityChild,
			Seq:    seq,
			Label:  label,
			Status: ch.status,
		})
	}

	if awaitingPermission {
		seq++
		entries = append(entries, activityEntry{
			ID:         "attention",
			Kind:       activityAttention,
			Seq:        seq,
			Label:      "needs you",
			Status:     "needs you",
			DetailBody: "permission or question pending",
		})
	}

	// Newest first; equal Seq → stable by ID.
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].Seq != entries[j].Seq {
			return entries[i].Seq > entries[j].Seq
		}
		return entries[i].ID < entries[j].ID
	})
	return entries
}

func toolActivityEntry(tc *toolCell, seq int64) activityEntry {
	id := strings.TrimSpace(tc.callID)
	if id == "" {
		id = "tool-seq-" + itoa(int(seq))
	} else {
		id = "tool:" + id
	}
	label := tc.name
	if tc.title != "" {
		label = tc.title
	}
	status := "running"
	if tc.done {
		if tc.isError {
			status = "error"
		} else {
			status = "done"
		}
	}
	return activityEntry{
		ID:         id,
		Kind:       activityTool,
		Seq:        seq,
		Label:      label,
		Status:     status,
		Done:       tc.done,
		IsError:    tc.isError,
		DetailBody: tc.output,
	}
}

// reverseNavChildren returns a copy of kids newest-first (spawn order reversed).
func reverseNavChildren(kids []navChild) []navChild {
	if len(kids) < 2 {
		return kids
	}
	out := make([]navChild, len(kids))
	for i := range kids {
		out[i] = kids[len(kids)-1-i]
	}
	return out
}

// activityDisplayCursor resolves the list index for the current anchor without
// mutating Model. Stick-to-newest (or empty anchor) always yields 0 so new
// events appear at the top without forcing scroll away from newest.
func (m Model) activityDisplayCursor(entries []activityEntry) int {
	if len(entries) == 0 {
		return 0
	}
	if m.activityStickNewest || m.activityAnchorID == "" {
		return 0
	}
	for i, e := range entries {
		if e.ID == m.activityAnchorID {
			return i
		}
	}
	// Lost selection → newest.
	return 0
}

func (m *Model) setActivityCursor(entries []activityEntry, idx int) {
	if len(entries) == 0 {
		m.activityCursor = 0
		m.activityAnchorID = ""
		m.activityStickNewest = true
		m.activityDetail = false
		return
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= len(entries) {
		idx = len(entries) - 1
	}
	m.activityCursor = idx
	m.activityAnchorID = entries[idx].ID
	m.activityStickNewest = idx == 0
}

// handleActivityKeys navigates the activity feed when the activity window is
// focused on the right pane. Returns true when the key was consumed.
func (m *Model) handleActivityKeys(msg tea.KeyPressMsg) bool {
	if m.windows.active() == nil || m.windows.active().id() != "activity" {
		return false
	}
	entries := m.activityEntries()
	if len(entries) == 0 {
		return false
	}
	// Re-resolve cursor from anchor before navigating.
	m.activityCursor = m.activityDisplayCursor(entries)
	if m.activityStickNewest || m.activityAnchorID == "" {
		m.activityAnchorID = entries[0].ID
	} else if m.activityDisplayCursor(entries) == 0 && entries[0].ID != m.activityAnchorID {
		// Anchor missing: snap to newest.
		m.activityStickNewest = true
		m.activityAnchorID = entries[0].ID
		m.activityCursor = 0
		m.activityDetail = false
	}

	if m.activityDetail {
		switch msg.String() {
		case "enter", "esc", "q", "left", "h":
			m.activityDetail = false
			return true
		case "up", "k":
			if m.activityCursor > 0 {
				m.setActivityCursor(entries, m.activityCursor-1)
			}
			return true
		case "down", "j":
			if m.activityCursor < len(entries)-1 {
				m.setActivityCursor(entries, m.activityCursor+1)
			}
			return true
		default:
			return false
		}
	}

	switch msg.String() {
	case "up", "k":
		if m.activityCursor > 0 {
			m.setActivityCursor(entries, m.activityCursor-1)
		}
		return true
	case "down", "j":
		if m.activityCursor < len(entries)-1 {
			m.setActivityCursor(entries, m.activityCursor+1)
		}
		return true
	case "enter", "right", "l":
		e := entries[m.activityCursor]
		if e.DetailBody != "" || e.Kind == activityAttention {
			m.activityDetail = true
		}
		return true
	case "g":
		m.setActivityCursor(entries, 0)
		return true
	case "G":
		m.setActivityCursor(entries, len(entries)-1)
		return true
	default:
		return false
	}
}

// activityEntries projects the flat feed for the current model. Children with
// real session ids appear in the session tree instead of the flat list.
func (m Model) activityEntries() []activityEntry {
	showTree := len(m.liveRootIDs()) > 1 || len(m.listChildren(m.sessionID)) > 0
	var kids []childActivity
	if !showTree {
		// Ephemeral / id-less children only in the flat feed.
		kids = m.children
	}
	return projectActivityEntries(m.cells, kids, m.awaitingPermission)
}

// activityPaneBody shows a session tree when subagents exist, then the newest-
// first activity feed (tools, children, attention), then idle tips.
// Expanded detail keeps chronological body order. Never renders placeholder
// copy or child transcript text.
func (m Model) activityPaneBody(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	th := m.th.Resolve()
	st := th.S()
	ellipsis := th.Icons.Ellipsis

	entries := m.activityEntries()
	cursor := m.activityDisplayCursor(entries)

	lines := make([]string, 0, height)

	// Session tree when multiple roots or any subagent is known.
	treeNodes := m.sessionTreeNodes()
	showTree := len(m.liveRootIDs()) > 1 || len(m.listChildren(m.sessionID)) > 0
	if showTree && len(treeNodes) > 0 {
		treeH := height
		if height > 4 {
			treeH = min(height, max(2, len(ui.FlattenTree(treeNodes))+1))
			if treeH > height-1 {
				treeH = height - 1
			}
		}
		// Leave room for at least one activity row when the feed is non-empty.
		if len(entries) > 0 && treeH >= height && height > 1 {
			treeH = height - 1
		}
		tree := ui.Tree(th, ui.TreeOpts{
			Nodes:   treeNodes,
			Cursor:  -1,
			Width:   width,
			Visible: treeH,
			Empty:   "",
		})
		if tree != "" {
			for _, row := range strings.Split(tree, "\n") {
				if len(lines) >= height {
					break
				}
				lines = append(lines, row)
			}
		}
	}

	remain := height - len(lines)
	if remain > 0 && len(entries) > 0 {
		if m.activityDetail && cursor >= 0 && cursor < len(entries) {
			detail := renderActivityDetail(th, entries[cursor], width, remain)
			if detail != "" {
				for _, row := range strings.Split(detail, "\n") {
					if len(lines) >= height {
						break
					}
					lines = append(lines, row)
				}
			}
		} else {
			focused := m.focus == focusRight && m.modal == nil &&
				m.windows.active() != nil && m.windows.active().id() == "activity"
			listCursor := -1
			if focused {
				listCursor = cursor
			}
			items := make([]ui.ListItem, len(entries))
			for i, e := range entries {
				items[i] = activityListItem(th, e)
			}
			list := ui.List(th, ui.ListOpts{
				Items:   items,
				Cursor:  listCursor,
				Width:   width,
				Visible: remain,
				Empty:   "",
			})
			if list != "" {
				for _, row := range strings.Split(list, "\n") {
					if len(lines) >= height {
						break
					}
					lines = append(lines, row)
				}
			}
		}
	}

	if len(lines) > 0 {
		return strings.Join(lines, "\n")
	}

	// Idle tips when there is no tree and no activity feed.
	tips := []ui.KeyHint{
		{Key: "/", Label: "commands"},
		keyHint(m.keyMap.Palette),
		keyHint(m.keyMap.Agent),
		keyHint(m.keyMap.CycleWindowNext),
		keyHint(m.keyMap.ToggleOrientation),
		keyHint(m.keyMap.Newline),
		{Key: "ctrl+x down", Label: "subagent"},
	}
	if len(tips) > height {
		tips = tips[:height]
	}
	gap := themedSpace(th.Spacing.SM)
	out := make([]string, 0, len(tips))
	for _, tip := range tips {
		keyText := welcomeTruncate(tip.Key, width, ellipsis)
		budget := max(0, width-ansi.StringWidth(keyText)-ansi.StringWidth(gap))
		line := st.Accent.Render(keyText)
		if budget > 0 {
			line += st.Muted.Render(gap + welcomeTruncate(tip.Label, budget, ellipsis))
		}
		if pad := width - ansi.StringWidth(ansi.Strip(line)); pad > 0 {
			line += themedSpace(pad)
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func activityListItem(th theme.Theme, e activityEntry) ui.ListItem {
	th = th.Resolve()
	ic := iconsFor(th)
	st := th.S()
	space := themedSpace(th.Spacing.XS)

	switch e.Kind {
	case activityChild:
		glyph := ic.Ellipsis
		suffixStyle := st.Muted
		switch e.Status {
		case "running":
			suffixStyle = st.AccentAlt
			glyph = ic.Ellipsis
		case string(protocol.ChildStatusCompleted):
			suffixStyle = st.Success
			glyph = ic.OK
		case string(protocol.ChildStatusFailed):
			suffixStyle = st.Error
			glyph = ic.Err
		case string(protocol.ChildStatusCanceled):
			suffixStyle = st.Muted
			glyph = ic.Info
		}
		return ui.ListItem{
			Label:  sanitizeDisplayData(e.Label),
			Detail: e.Status,
			Suffix: suffixStyle.Render(space + glyph),
		}
	case activityAttention:
		return ui.ListItem{
			Label:  sanitizeDisplayData(e.Label),
			Detail: e.Status,
			Suffix: st.Warning.Render(space + ic.Bolt),
		}
	default: // tool
		glyph := ic.Ellipsis
		suffixStyle := st.Muted
		if e.Done {
			if e.IsError {
				glyph, suffixStyle = ic.Err, st.Error
			} else {
				glyph, suffixStyle = ic.OK, st.Success
			}
		}
		return ui.ListItem{
			Label:  sanitizeDisplayData(e.Label),
			Detail: e.Status,
			Suffix: suffixStyle.Render(space + glyph),
		}
	}
}

func renderActivityDetail(th theme.Theme, e activityEntry, width, height int) string {
	th = th.Resolve()
	st := th.S()
	ellipsis := th.Icons.Ellipsis
	if width <= 0 || height <= 0 {
		return ""
	}
	head := st.Accent.Render(welcomeTruncate(sanitizeDisplayData(e.Label), width, ellipsis))
	body := strings.TrimRight(e.DetailBody, "\n")
	if body == "" {
		body = e.Status
	}
	// Chronological body lines — never reverse.
	wrapped := wrapToWidth(st.Text.Render(sanitizeDisplayData(body)), width)
	parts := []string{head}
	if wrapped != "" {
		parts = append(parts, strings.Split(wrapped, "\n")...)
	}
	if len(parts) > height {
		parts = parts[:height]
	}
	return strings.Join(parts, "\n")
}
