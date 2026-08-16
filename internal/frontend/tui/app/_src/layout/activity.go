package tui

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/ui"
)

// activityKind classifies one row in the activity pane feed.
type activityKind int

const (
	activityTool activityKind = iota
	activityChild
	activityAttention
	activityTeamMsg
	activityQueue
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
	// NavSessionID is a teammate session to open on enter (team messages).
	NavSessionID string
}

// projectActivityEntries builds the flat activity feed newest-first.
// Order is stable for equal Seq (by ID). Covers parent tools, child lifecycle
// rows, recent team messages, and an attention row while a permission/question
// is pending. Replay and live paths share this projection.
func projectActivityEntries(cells []cell, children []childActivity, messages []teamMessage, awaitingPermission bool) []activityEntry {
	return projectActivityEntriesNamed(cells, children, messages, awaitingPermission, nil, "", nil)
}

// projectActivityEntriesNamed is projectActivityEntries with optional teammate
// name resolution for message rows (lead UI) and optional root queue state.
func projectActivityEntriesNamed(cells []cell, children []childActivity, messages []teamMessage, awaitingPermission bool, resolveName func(id string) string, rootQueueLabel string, rootQueuePools []string) []activityEntry {
	entries := make([]activityEntry, 0, len(cells)+len(children)+len(messages)+2)
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
		// Prefer stable teammate alias / title; else agent + prompt (activity
		// readability); agents tree uses childViewTitle for compact rows.
		label := strings.TrimSpace(ch.name)
		if label == "" {
			label = strings.TrimSpace(ch.title)
		}
		if label == "" {
			label = strings.TrimSpace(ch.agent)
			if label == "" {
				label = "subagent"
			}
			if p := strings.TrimSpace(ch.prompt); p != "" {
				label = label + " " + p
			}
		}
		status := ch.status
		if ch.rosterState != "" {
			status = ch.rosterState
		}
		if q := childQueueDetail(ch); q != "" {
			status = q
		}
		entries = append(entries, activityEntry{
			ID:           id,
			Kind:         activityChild,
			Seq:          seq,
			Label:        label,
			Status:       status,
			NavSessionID: strings.TrimSpace(ch.sessionID),
		})
	}

	// Root queue wait (model/bash admission) — identify constrained pool.
	if rootQ := queueActivityStatus(rootQueueLabel, rootQueuePools); rootQ != "" {
		seq++
		entries = append(entries, activityEntry{
			ID:     "queue:root",
			Kind:   activityQueue,
			Seq:    seq,
			Label:  rootQueueLabelOrPools(rootQueueLabel, rootQueuePools),
			Status: rootQ,
		})
	}

	// Team messages in arrival order; later deliveries get higher seq.
	for i, msg := range messages {
		seq++
		id := strings.TrimSpace(msg.id)
		if id == "" {
			id = "team-msg-" + itoa(i)
		} else {
			id = "msg:" + id
		}
		label := teamMsgActivityLabel(msg, resolveName)
		entries = append(entries, activityEntry{
			ID:           id,
			Kind:         activityTeamMsg,
			Seq:          seq,
			Label:        label,
			Status:       "message",
			DetailBody:   msg.body,
			NavSessionID: firstNonEmpty(msg.from, msg.to),
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
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
// focused on the right pane. handled is true when the key was consumed.
// cmd may open a teammate transcript (enter on child/message rows).
func (m *Model) handleActivityKeys(msg tea.KeyPressMsg) (handled bool, cmd tea.Cmd) {
	if m.windows.active() == nil || m.windows.active().id() != "activity" {
		return false, nil
	}
	entries := m.activityEntries()
	if len(entries) == 0 {
		return false, nil
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
			return true, nil
		case "up", "k":
			if m.activityCursor > 0 {
				m.setActivityCursor(entries, m.activityCursor-1)
			}
			return true, nil
		case "down", "j":
			if m.activityCursor < len(entries)-1 {
				m.setActivityCursor(entries, m.activityCursor+1)
			}
			return true, nil
		default:
			return false, nil
		}
	}

	switch msg.String() {
	case "up", "k":
		if m.activityCursor > 0 {
			m.setActivityCursor(entries, m.activityCursor-1)
		}
		return true, nil
	case "down", "j":
		if m.activityCursor < len(entries)-1 {
			m.setActivityCursor(entries, m.activityCursor+1)
		}
		return true, nil
	case "enter", "right", "l":
		e := entries[m.activityCursor]
		switch e.Kind {
		case activityTeamMsg:
			nav := ""
			if msgID := strings.TrimPrefix(e.ID, "msg:"); msgID != "" {
				for _, tm := range m.teamMessages {
					if tm.id == msgID {
						nav = m.resolveTeamMsgNav(tm.from, tm.to)
						break
					}
				}
			}
			if nav == "" {
				nav = m.resolveTeamMsgNav(e.NavSessionID, "")
			}
			if nav != "" {
				return true, m.openSessionView(nav)
			}
			if e.DetailBody != "" {
				m.activityDetail = true
			}
			return true, nil
		case activityChild:
			if id := strings.TrimSpace(e.NavSessionID); id != "" && id != "child" {
				return true, m.openSessionView(id)
			}
			return true, nil
		default:
			if e.DetailBody != "" || e.Kind == activityAttention {
				m.activityDetail = true
			}
			return true, nil
		}
	case "g":
		m.setActivityCursor(entries, 0)
		return true, nil
	case "G":
		m.setActivityCursor(entries, len(entries)-1)
		return true, nil
	default:
		return false, nil
	}
}

// activityEntries projects the flat feed for the current model. Children with
// real session ids appear in the session tree instead of the flat list.
// Recent team messages always surface so the lead sees peer mail without
// reading tool JSON.
func (m Model) activityEntries() []activityEntry {
	showTree := len(m.liveRootIDs()) > 1 || len(m.listChildren(m.sessionID)) > 0
	var kids []childActivity
	if !showTree {
		// Ephemeral / id-less children only in the flat feed.
		kids = m.children
	}
	rootLabel := strings.TrimSpace(m.queueLabel)
	return projectActivityEntriesNamed(m.cells, kids, m.teamMessages, m.awaitingPermission, m.teamMemberLabel, rootLabel, m.queuePools)
}

// queueActivityStatus is the status chip for a root queue row.
func queueActivityStatus(label string, pools []string) string {
	if strings.TrimSpace(label) == "" && len(pools) == 0 {
		return ""
	}
	return queueDetailLabel(rootQueueLabelOrPools(label, pools))
}

func rootQueueLabelOrPools(label string, pools []string) string {
	if s := strings.TrimSpace(label); s != "" {
		return s
	}
	if len(pools) == 0 {
		return "scheduler"
	}
	return strings.Join(pools, ",")
}

// activityEmptyMessage is the idle body when the activity feed has nothing yet.
const activityEmptyMessage = "nothing here yet :)"

// activityPaneBody shows a session tree when subagents exist, then the newest-
// first activity feed (tools, children, attention), then an empty-state line.
// Expanded detail keeps chronological body order. Never renders placeholder
// copy or child transcript text.
func (m Model) activityPaneBody(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	th := m.th.Resolve()
	st := th.S()

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

	// Empty feed (and no session tree): plain empty-state, not keybind chrome.
	return wrapWindowText(st.Muted.Render(activityEmptyMessage), width)
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
		status := strings.ToLower(strings.TrimSpace(e.Status))
		switch {
		case strings.HasPrefix(status, "queued"):
			suffixStyle = st.AccentAlt
			glyph = ic.Ellipsis
		case status == "running" || status == "working" || status == "starting":
			suffixStyle = st.AccentAlt
			glyph = ic.Ellipsis
		case status == "needs you" || status == "needs_attention":
			suffixStyle = st.Warning
			glyph = ic.Bolt
		case status == "completed" || status == "done":
			suffixStyle = st.Success
			glyph = ic.OK
		case status == "failed" || status == "error":
			suffixStyle = st.Error
			glyph = ic.Err
		case status == "canceled" || status == "cancelled":
			suffixStyle = st.Muted
			glyph = ic.Info
		}
		return ui.ListItem{
			Label:  sanitizeDisplayData(e.Label),
			Detail: e.Status,
			Suffix: suffixStyle.Render(space + glyph),
		}
	case activityQueue:
		return ui.ListItem{
			Label:  sanitizeDisplayData(e.Label),
			Detail: e.Status,
			Suffix: st.AccentAlt.Render(space + ic.Ellipsis),
		}
	case activityTeamMsg:
		return ui.ListItem{
			Label:  sanitizeDisplayData(e.Label),
			Detail: e.Status,
			Suffix: st.Accent.Render(space + ic.Info),
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
