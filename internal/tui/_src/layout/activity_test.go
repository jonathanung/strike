package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestProjectActivityEntriesNewestFirst(t *testing.T) {
	cells := []cell{
		&toolCell{callID: "a", name: "tool-a", title: "A", done: true},
		&toolCell{callID: "b", name: "tool-b", title: "B", done: true},
		&toolCell{callID: "c", name: "tool-c", title: "C", done: true},
	}
	got := projectActivityEntries(cells, nil, false)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []string{"C", "B", "A"}
	for i, w := range want {
		if got[i].Label != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i].Label, w)
		}
	}
}

func TestProjectActivityEntriesNewEventAtTop(t *testing.T) {
	cells := []cell{
		&toolCell{callID: "a", name: "tool-a", title: "A", done: true},
		&toolCell{callID: "b", name: "tool-b", title: "B", done: true},
		&toolCell{callID: "c", name: "tool-c", title: "C", done: true},
	}
	before := projectActivityEntries(cells, nil, false)
	if before[0].Label != "C" {
		t.Fatalf("before top = %q, want C", before[0].Label)
	}
	cells = append(cells, &toolCell{callID: "d", name: "tool-d", title: "D", done: false})
	after := projectActivityEntries(cells, nil, false)
	if after[0].Label != "D" {
		t.Fatalf("after top = %q, want D", after[0].Label)
	}
	if after[1].Label != "C" || after[2].Label != "B" || after[3].Label != "A" {
		t.Errorf("tail order = %q %q %q, want C B A", after[1].Label, after[2].Label, after[3].Label)
	}
}

func TestProjectActivityEntriesChildAndToolOrder(t *testing.T) {
	cells := []cell{
		&toolCell{callID: "x", name: "x", title: "X", done: true},
	}
	children := []childActivity{
		{sessionID: "c1", agent: "alpha", status: "running"},
		{sessionID: "c2", agent: "beta", status: "running"},
	}
	got := projectActivityEntries(cells, children, false)
	// Newest is last child (c2), then c1, then tool x.
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if !strings.Contains(got[0].Label, "beta") {
		t.Errorf("newest = %q, want beta child", got[0].Label)
	}
	if !strings.Contains(got[1].Label, "alpha") {
		t.Errorf("mid = %q, want alpha child", got[1].Label)
	}
	if got[2].Label != "X" {
		t.Errorf("oldest = %q, want X", got[2].Label)
	}
}

func TestProjectActivityEntriesIncludesAttentionAndChildren(t *testing.T) {
	cells := []cell{
		&toolCell{callID: "t1", name: "bash", title: "run", done: true},
	}
	children := []childActivity{
		{sessionID: "child-1", agent: "explore", prompt: "scan", status: "running"},
	}
	got := projectActivityEntries(cells, children, true)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].Kind != activityAttention || got[0].ID != "attention" {
		t.Errorf("top = %+v, want attention", got[0])
	}
	if got[1].Kind != activityChild {
		t.Errorf("mid kind = %v, want child", got[1].Kind)
	}
	if got[2].Kind != activityTool {
		t.Errorf("bottom kind = %v, want tool", got[2].Kind)
	}
}

func TestProjectActivityEntriesExploreCallsNewestFirst(t *testing.T) {
	cells := []cell{
		&exploreCell{calls: []*toolCell{
			{callID: "r1", name: "read", title: "R1", done: true},
			{callID: "g1", name: "grep", title: "G1", done: true},
		}},
		&toolCell{callID: "b1", name: "bash", title: "B1", done: true},
	}
	got := projectActivityEntries(cells, nil, false)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	want := []string{"B1", "G1", "R1"}
	for i, w := range want {
		if got[i].Label != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i].Label, w)
		}
	}
}

func TestActivityPaneBodyRendersNewestFirst(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.cells = []cell{
		&toolCell{callID: "a", name: "tool-a", title: "Alpha", done: true},
		&toolCell{callID: "b", name: "tool-b", title: "Bravo", done: true},
		&toolCell{callID: "c", name: "tool-c", title: "Charlie", done: true},
	}
	body := ansi.Strip(m.activityPaneBody(48, 10))
	ai := strings.Index(body, "Alpha")
	bi := strings.Index(body, "Bravo")
	ci := strings.Index(body, "Charlie")
	if ai < 0 || bi < 0 || ci < 0 {
		t.Fatalf("missing labels in body: %q", body)
	}
	if !(ci < bi && bi < ai) {
		t.Errorf("order not newest-first (C,B,A): Charlie@%d Bravo@%d Alpha@%d\n%s", ci, bi, ai, body)
	}
}

func TestActivityPaneBodyNewEventAtTop(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.cells = []cell{
		&toolCell{callID: "a", name: "a", title: "A", done: true},
		&toolCell{callID: "b", name: "b", title: "B", done: true},
	}
	m.cells = append(m.cells, &toolCell{callID: "d", name: "d", title: "Delta", done: false})
	body := ansi.Strip(m.activityPaneBody(48, 8))
	lines := nonEmptyLines(body)
	if len(lines) == 0 || !strings.Contains(lines[0], "Delta") {
		t.Errorf("first line = %q, want Delta on top\n%s", activityFirstLine(lines), body)
	}
}

func TestActivityCursorSticksToNewestAndAnchorsSelection(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.focus = focusRight
	var ok bool
	m.windows, ok = m.windows.activate("activity")
	if !ok || m.windows.active() == nil || m.windows.active().id() != "activity" {
		t.Fatal("could not focus activity window")
	}

	m.cells = []cell{
		&toolCell{callID: "a", name: "a", title: "A", done: true},
		&toolCell{callID: "b", name: "b", title: "B", done: true},
		&toolCell{callID: "c", name: "c", title: "C", done: true},
	}
	m.activityStickNewest = true
	entries := projectActivityEntries(m.cells, nil, false)
	if m.activityDisplayCursor(entries) != 0 || entries[0].Label != "C" {
		t.Fatalf("stick newest cursor=%d top=%q", m.activityDisplayCursor(entries), entries[0].Label)
	}

	// Move down to B (index 1).
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	entries = projectActivityEntries(m.cells, nil, false)
	cur := m.activityDisplayCursor(entries)
	if cur != 1 || entries[cur].Label != "B" {
		t.Fatalf("after j cursor=%d label=%q, want B", cur, entries[cur].Label)
	}
	if m.activityStickNewest {
		t.Fatal("expected stick-newest cleared after leaving top")
	}
	anchor := m.activityAnchorID

	// New event D arrives: selection stays on B.
	m.cells = append(m.cells, &toolCell{callID: "d", name: "d", title: "D", done: false})
	entries = projectActivityEntries(m.cells, nil, false)
	cur = m.activityDisplayCursor(entries)
	if entries[cur].ID != anchor || entries[cur].Label != "B" {
		t.Fatalf("anchor lost: cursor=%d id=%q label=%q want B (%s)", cur, entries[cur].ID, entries[cur].Label, anchor)
	}
	if entries[0].Label != "D" {
		t.Errorf("newest top = %q, want D", entries[0].Label)
	}

	// Return to top and stick again.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("g")})
	if !m.activityStickNewest {
		t.Fatal("g should stick to newest")
	}
	m.cells = append(m.cells, &toolCell{callID: "e", name: "e", title: "Echo", done: false})
	entries = projectActivityEntries(m.cells, nil, false)
	if m.activityDisplayCursor(entries) != 0 || entries[0].Label != "Echo" {
		t.Errorf("stuck newest: cursor=%d top=%q", m.activityDisplayCursor(entries), entries[0].Label)
	}
}

func TestActivityDetailKeepsChronologicalBody(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.focus = focusRight
	m.windows, _ = m.windows.activate("activity")
	m.cells = []cell{
		&toolCell{
			callID: "t1",
			name:   "bash",
			title:  "run",
			done:   true,
			output: "line-one\nline-two\nline-three",
		},
	}
	m.activityStickNewest = true
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !m.activityDetail {
		t.Fatal("enter should open detail")
	}
	body := ansi.Strip(m.activityPaneBody(48, 10))
	one := strings.Index(body, "line-one")
	two := strings.Index(body, "line-two")
	three := strings.Index(body, "line-three")
	if one < 0 || two < 0 || three < 0 {
		t.Fatalf("detail missing lines: %q", body)
	}
	if !(one < two && two < three) {
		t.Errorf("detail body reversed: one@%d two@%d three@%d\n%s", one, two, three, body)
	}
}

func TestActivityPaneLongListAndResize(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.focus = focusRight
	m.windows, _ = m.windows.activate("activity")
	for i := 0; i < 30; i++ {
		id := "t" + itoa(i)
		m.cells = append(m.cells, &toolCell{
			callID: id,
			name:   "tool",
			title:  "Item-" + itoa(i),
			done:   true,
		})
	}
	// Cursor on an older item mid-list.
	entries := projectActivityEntries(m.cells, nil, false)
	m.setActivityCursor(entries, 15)
	anchor := m.activityAnchorID

	for _, h := range []int{3, 5, 12, 20} {
		body := m.activityPaneBody(24, h)
		plain := ansi.Strip(body)
		lines := nonEmptyLines(plain)
		if len(lines) == 0 {
			t.Fatalf("height=%d empty body", h)
		}
		if len(lines) > h {
			t.Errorf("height=%d lines=%d exceeds height", h, len(lines))
		}
		// Width-safe.
		for i, line := range strings.Split(body, "\n") {
			if line == "" {
				continue
			}
			if got := ansi.StringWidth(ansi.Strip(line)); got > 24 {
				t.Errorf("height=%d line %d width=%d: %q", h, i, got, ansi.Strip(line))
			}
		}
	}
	// Anchor preserved across resizes.
	entries = projectActivityEntries(m.cells, nil, false)
	if got := entries[m.activityDisplayCursor(entries)].ID; got != anchor {
		t.Errorf("anchor after resize = %q, want %q", got, anchor)
	}
}

func TestSessionTreeChildrenNewestFirst(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.titleTopic = "main"
	m.children = []childActivity{
		{sessionID: "c1", parentID: "root", agent: "first", prompt: "A", status: "running"},
		{sessionID: "c2", parentID: "root", agent: "second", prompt: "B", status: "running"},
		{sessionID: "c3", parentID: "root", agent: "third", prompt: "C", status: "running"},
	}
	nodes := m.sessionTreeNodes()
	if len(nodes) != 1 || len(nodes[0].Children) != 3 {
		t.Fatalf("tree kids = %d, want 3", len(nodes[0].Children))
	}
	// Newest spawn (c3) first under root.
	if nodes[0].Children[0].ID != "c3" {
		t.Errorf("first child = %q, want c3", nodes[0].Children[0].ID)
	}
	if nodes[0].Children[2].ID != "c1" {
		t.Errorf("last child = %q, want c1", nodes[0].Children[2].ID)
	}
}

func TestReverseNavChildren(t *testing.T) {
	in := []navChild{{id: "a"}, {id: "b"}, {id: "c"}}
	out := reverseNavChildren(in)
	if out[0].id != "c" || out[1].id != "b" || out[2].id != "a" {
		t.Errorf("reversed = %+v", out)
	}
	// Input not mutated.
	if in[0].id != "a" {
		t.Error("input mutated")
	}
	if reverseNavChildren(nil) != nil {
		t.Error("nil in should stay nil-ish empty")
	}
	if len(reverseNavChildren([]navChild{{id: "only"}})) != 1 {
		t.Error("single element")
	}
}

func TestActivityReplayMatchesLiveOrdering(t *testing.T) {
	// Same cells/children projection whether built live or from replayed state.
	liveCells := []cell{
		&toolCell{callID: "1", name: "read", title: "one", done: true},
		&toolCell{callID: "2", name: "bash", title: "two", done: true},
	}
	liveKids := []childActivity{
		{sessionID: "ch", agent: "explore", status: string(protocol.ChildStatusCompleted)},
	}
	live := projectActivityEntries(liveCells, liveKids, false)

	// Replay path: identical reconstructed slices.
	replayCells := []cell{
		&toolCell{callID: "1", name: "read", title: "one", done: true},
		&toolCell{callID: "2", name: "bash", title: "two", done: true},
	}
	replayKids := []childActivity{
		{sessionID: "ch", agent: "explore", status: string(protocol.ChildStatusCompleted)},
	}
	replay := projectActivityEntries(replayCells, replayKids, false)
	if len(live) != len(replay) {
		t.Fatalf("len live=%d replay=%d", len(live), len(replay))
	}
	for i := range live {
		if live[i].ID != replay[i].ID || live[i].Label != replay[i].Label {
			t.Errorf("[%d] live=%+v replay=%+v", i, live[i], replay[i])
		}
	}
}

func activityFirstLine(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
