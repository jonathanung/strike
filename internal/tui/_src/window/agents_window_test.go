package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestAgentsWindowEmptyState(t *testing.T) {
	w := newAgentsWindow().resize(32, 6).(agentsWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "no subagents") {
		t.Fatalf("empty view = %q", plain)
	}
	spawn := defaultAgentsKeyMap().Spawn.Help()
	if !strings.Contains(plain, spawn.Key) || !strings.Contains(plain, spawn.Desc) {
		t.Fatalf("empty view missing spawn help %q %q: %q", spawn.Key, spawn.Desc, plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > 32 {
			t.Errorf("line width %d > 32: %q", got, line)
		}
	}
}

func TestAgentsPaneFooterDerivesFromKeyMap(t *testing.T) {
	footer := ansi.Strip(agentsPaneFooter(theme.Default(), 120))
	ak := defaultAgentsKeyMap()
	for _, b := range []key.Binding{ak.Spawn, ak.Open, ak.Interrupt, ak.Rename, ak.Hide, ak.Move, ak.Filter} {
		h := b.Help()
		if !strings.Contains(footer, h.Key) {
			t.Errorf("footer missing key %q: %q", h.Key, footer)
		}
		if h.Desc != "" && !strings.Contains(footer, h.Desc) {
			t.Errorf("footer missing desc %q: %q", h.Desc, footer)
		}
	}
	// Help must document non-destructive hide (not delete/interrupt).
	hide := ak.Hide.Help()
	if !strings.Contains(hide.Desc, "hide") || strings.Contains(hide.Desc, "delete") {
		t.Errorf("Hide help = %q, want non-destructive hide wording", hide.Desc)
	}
}

func TestAgentsPaneFooterSingleLineAtNarrowWidths(t *testing.T) {
	th := theme.Default()
	for _, width := range []int{80, 60, 40, 32, 24, 16} {
		footer := agentsPaneFooter(th, width)
		if footer == "" && width >= 8 {
			t.Errorf("width %d: empty footer", width)
			continue
		}
		if strings.Contains(footer, "\n") {
			t.Errorf("width %d: footer wrapped: %q", width, ansi.Strip(footer))
		}
		if w := lipgloss.Width(footer); w > width {
			t.Errorf("width %d: footer display width %d exceeds budget: %q", width, w, ansi.Strip(footer))
		}
		// At least the first binding should survive when there is room.
		if width >= 8 {
			spawn := defaultAgentsKeyMap().Spawn.Help().Key
			if !strings.Contains(ansi.Strip(footer), spawn) {
				t.Errorf("width %d: missing lead key %q in %q", width, spawn, ansi.Strip(footer))
			}
		}
	}
}

func TestAgentsWindowMultiRootTree(t *testing.T) {
	w := newAgentsWindow().resize(48, 10).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID:  "root-a",
		viewingID: "root-a",
		roots: []agentsRootSnap{
			{
				ID:    "root-a",
				Title: "first task",
				State: theme.AgentStateWorking,
				Children: []childActivity{
					{sessionID: "child-a", parentID: "root-a", agent: "explore", prompt: "scan", status: "running"},
				},
			},
			{
				ID:    "root-b",
				Title: "second task",
				State: theme.AgentStateReady,
			},
		},
	})
	w = next.(agentsWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "first task") {
		t.Errorf("missing root-a: %q", plain)
	}
	if !strings.Contains(plain, "second task") {
		t.Errorf("missing root-b: %q", plain)
	}
	if !strings.Contains(plain, "explore") && !strings.Contains(plain, "scan") {
		t.Errorf("missing child: %q", plain)
	}
	if !strings.Contains(plain, "working") && !strings.Contains(plain, "ready") {
		t.Errorf("missing status detail: %q", plain)
	}
	for _, line := range strings.Split(plain, "\n") {
		if got := lipgloss.Width(line); got > 48 {
			t.Errorf("line width %d > 48: %q", got, line)
		}
	}
}

func TestAgentsWindowEnterOpensRoot(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "root-a",
		roots: []agentsRootSnap{
			{ID: "root-a", Title: "a", State: theme.AgentStateReady},
			{ID: "root-b", Title: "b", State: theme.AgentStateReady},
		},
	})
	w = next.(agentsWindow)
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyDown})
	w = next.(agentsWindow)
	next, cmd := w.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no cmd")
	}
	msg := cmd()
	om, ok := msg.(agentsOpenMsg)
	if !ok || om.sessionID != "root-b" {
		t.Fatalf("open msg = %#v, want agentsOpenMsg{root-b}", msg)
	}
	_ = next
}

func TestAgentsWindowSpawnKey(t *testing.T) {
	w := newAgentsWindow().resize(40, 4).(agentsWindow)
	_, cmd := w.update(tea.KeyPressMsg{Code: 'n', Text: string([]rune{'n'})})
	if cmd == nil {
		t.Fatal("n produced no cmd")
	}
	if _, ok := cmd().(agentsSpawnMsg); !ok {
		t.Fatalf("want agentsSpawnMsg, got %#v", cmd())
	}
}

func TestAgentsWindowEnterOpensChild(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "root",
		roots: []agentsRootSnap{
			{
				ID:    "root",
				Title: "main",
				State: theme.AgentStateReady,
				Children: []childActivity{
					{sessionID: "child-1", parentID: "root", agent: "explore", status: "running"},
					{sessionID: "child-2", parentID: "root", agent: "general", status: string(protocol.ChildStatusCompleted)},
				},
			},
		},
	})
	w = next.(agentsWindow)
	// cursor 0 = root; down to first child; down to second
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyDown})
	w = next.(agentsWindow)
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyDown})
	w = next.(agentsWindow)
	next, cmd := w.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter produced no cmd")
	}
	msg := cmd()
	om, ok := msg.(agentsOpenMsg)
	if !ok || om.sessionID != "child-2" {
		t.Fatalf("open msg = %#v, want agentsOpenMsg{child-2}", msg)
	}
	_ = next
}

func TestAgentsWindowLiveStatusViaModel(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "parent-1"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg
	m.focus = focusRight

	m.applyEvent(protocol.ChildStarted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "parent-1", Depth: 1},
		Agent:       "explore",
		Prompt:      "one",
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())
	m.applyEvent(protocol.ChildStarted{
		Correlation: protocol.Correlation{SessionID: "c2", ParentSessionID: "parent-1", Depth: 1},
		Agent:       "general",
		Prompt:      "two",
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())

	aw := agentsWindowFrom(t, m)
	plain := ansi.Strip(aw.view(theme.Default()))
	if !strings.Contains(plain, "explore") || !strings.Contains(plain, "general") {
		t.Fatalf("running rows missing: %q", plain)
	}

	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c1", ParentSessionID: "parent-1", Depth: 1},
		Status:      protocol.ChildStatusCompleted,
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())
	m.applyEvent(protocol.ChildCompleted{
		Correlation: protocol.Correlation{SessionID: "c2", ParentSessionID: "parent-1", Depth: 1},
		Status:      protocol.ChildStatusFailed,
	})
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())

	aw = agentsWindowFrom(t, m)
	plain = ansi.Strip(aw.view(theme.Default()))
	if !strings.Contains(plain, "completed") && !strings.Contains(plain, "done") && !strings.Contains(plain, "failed") {
		// Tree detail uses raw status "completed"/"failed"
		if !strings.Contains(plain, "failed") {
			t.Errorf("missing failed status: %q", plain)
		}
	}
}

func TestAgentsOpenMsgNavigatesTranscript(t *testing.T) {
	fs := newFakeSessions()
	childLog := mustSessionJSONL(t,
		protocol.UserMessage{Text: "child work"},
		protocol.TextDelta{Text: "child reply here"},
	)
	fs.put(host.Session{ID: "child-nav", ParentID: "root", Title: "explore: child work"}, childLog)

	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root"
	m.services.Sessions = fs
	m.children = []childActivity{{
		sessionID: "child-nav",
		agent:     "explore",
		status:    string(protocol.ChildStatusCompleted),
	}}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})

	m = updateApp(t, m, agentsOpenMsg{sessionID: "child-nav"})
	if !m.viewingChild() || m.viewingID != "child-nav" {
		t.Fatalf("viewingID = %q, want child-nav", m.viewingID)
	}
	plain := ansi.Strip(viewString(m))
	if !strings.Contains(plain, "child reply here") {
		t.Errorf("view missing child transcript:\n%s", plain)
	}
}

func TestAgentsAgeLabel(t *testing.T) {
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	ch := childActivity{
		startedAt: now.Add(-65 * time.Second),
		endedAt:   now.Add(-5 * time.Second),
		status:    string(protocol.ChildStatusCompleted),
	}
	if got := agentsAgeLabel(ch, now); got != "1m 0s" {
		t.Errorf("completed age = %q, want 1m 0s", got)
	}
	running := childActivity{startedAt: now.Add(-12 * time.Second), status: "running"}
	if got := agentsAgeLabel(running, now); got != "12s" {
		t.Errorf("running age = %q, want 12s", got)
	}
	if got := agentsAgeLabel(childActivity{}, now); got != "" {
		t.Errorf("zero start = %q, want empty", got)
	}
}

func TestMultiRootSwitchTranscript(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
		dirs:   map[string]string{"root-a": "/a", "root-b": "/b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.workDir = "/a"
	m.titleTopic = "alpha"
	m.cells = []cell{&userCell{text: "from a"}}
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {
			sessionID:  "root-b",
			workDir:    "/b",
			titleTopic: "beta",
			cells:      []cell{&userCell{text: "from b"}},
			toolByID:   map[string]*toolCell{},
		},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updateApp(t, m, agentsOpenMsg{sessionID: "root-b"})
	if m.sessionID != "root-b" {
		t.Fatalf("sessionID = %q, want root-b", m.sessionID)
	}
	if fr.active != "root-b" {
		t.Fatalf("roots active = %q", fr.active)
	}
	if len(m.cells) != 1 {
		t.Fatalf("cells = %d", len(m.cells))
	}
	if uc, ok := m.cells[0].(*userCell); !ok || uc.text != "from b" {
		t.Fatalf("cell = %#v, want from b", m.cells[0])
	}
	// Stashed root-a still has its transcript.
	if p := m.roots["root-a"]; p == nil || len(p.cells) != 1 {
		t.Fatalf("stashed root-a = %#v", m.roots["root-a"])
	}
}

func TestBackgroundRootEventUpdatesPane(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {sessionID: "root-b", toolByID: map[string]*toolCell{}},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 30})
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "root-b"},
	}})
	p := m.roots["root-b"]
	if p == nil || !p.turnRunning {
		t.Fatalf("background turn not tracked: %#v", p)
	}
	if m.turnRunning {
		t.Fatal("active root should not be running")
	}
	if st := m.rootAgentState("root-b"); st != theme.AgentStateWorking {
		t.Fatalf("root-b state = %v, want working", st)
	}
}

func agentsWindowFrom(t *testing.T, m Model) agentsWindow {
	t.Helper()
	for _, w := range m.windows.windows {
		if aw, ok := w.(agentsWindow); ok {
			return aw
		}
	}
	t.Fatal("agents window missing from registry")
	return agentsWindow{}
}

// fakeRoots is a scriptable host.Roots for TUI tests.
type fakeRoots struct {
	active      string
	live        []string
	dirs        map[string]string
	err         error
	interrupted []string
}

func (f *fakeRoots) ActiveID() string { return f.active }
func (f *fakeRoots) LiveIDs() []string {
	out := append([]string(nil), f.live...)
	return out
}
func (f *fakeRoots) Activate(id string) error {
	if f.err != nil {
		return f.err
	}
	for _, live := range f.live {
		if live == id {
			f.active = id
			return nil
		}
	}
	return errFake("not live")
}
func (f *fakeRoots) Spawn() (string, error) {
	if f.err != nil {
		return "", f.err
	}
	id := "root-new"
	f.live = append(f.live, id)
	f.active = id
	return id, nil
}
func (f *fakeRoots) Open(id string) error {
	if f.err != nil {
		return f.err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errFake("session id is empty")
	}
	for _, live := range f.live {
		if live == id {
			f.active = id
			return nil
		}
	}
	// Match production hub.Open: bring a durable id live without process restart.
	f.live = append(f.live, id)
	f.active = id
	return nil
}
func (f *fakeRoots) Interrupt(id string) error {
	if f.err != nil {
		return f.err
	}
	f.interrupted = append(f.interrupted, id)
	return nil
}
func (f *fakeRoots) WorkDir(id string) string {
	if f.dirs != nil {
		return f.dirs[id]
	}
	return ""
}

type fakeError string

func (e fakeError) Error() string { return string(e) }
func errFake(s string) error      { return fakeError(s) }

func agentsFixtureTree() agentsStateMsg {
	return agentsStateMsg{
		activeID:  "root-a",
		viewingID: "root-a",
		roots: []agentsRootSnap{
			{
				ID:    "root-a",
				Title: "needs you",
				State: theme.AgentStateAttention,
				Children: []childActivity{
					{sessionID: "child-run", parentID: "root-a", agent: "explore", prompt: "scan", status: "running"},
					{sessionID: "child-done", parentID: "root-a", agent: "general", prompt: "done work", status: string(protocol.ChildStatusCompleted)},
				},
			},
			{
				ID:    "root-b",
				Title: "busy parent",
				State: theme.AgentStateWorking,
				Children: []childActivity{
					{sessionID: "child-b-run", parentID: "root-b", agent: "build", prompt: "compile", status: "running"},
				},
			},
			{
				ID:    "root-c",
				Title: "idle parent",
				State: theme.AgentStateReady,
			},
		},
	}
}

func loadAgentsFixture(t *testing.T) agentsWindow {
	t.Helper()
	w := newAgentsWindow().resize(56, 12).(agentsWindow)
	next, _ := w.update(agentsFixtureTree())
	return next.(agentsWindow)
}

func agentsVisibleIDs(w agentsWindow) []string {
	w.nodes = w.buildNodes(theme.Default())
	rows := ui.FlattenTree(w.nodes)
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}

func TestAgentsWindowFilterAttention(t *testing.T) {
	w := loadAgentsFixture(t)
	w.viewFilter = agentsFilterAttention
	ids := agentsVisibleIDs(w)
	want := map[string]bool{"root-a": true}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("attention view unexpected id %q in %v", id, ids)
		}
		delete(want, id)
	}
	for id := range want {
		t.Errorf("attention view missing %q; got %v", id, ids)
	}
	plain := ansi.Strip(w.view(theme.Default()))
	if strings.Contains(plain, "busy parent") || strings.Contains(plain, "idle parent") {
		t.Errorf("attention view leaked non-blocking roots: %q", plain)
	}
	if strings.Contains(plain, "scan") || strings.Contains(plain, "compile") {
		t.Errorf("attention view should not list working children: %q", plain)
	}
}

func TestAgentsWindowFilterWorking(t *testing.T) {
	w := loadAgentsFixture(t)
	w.viewFilter = agentsFilterWorking
	ids := agentsVisibleIDs(w)
	want := map[string]bool{
		"root-a":      true, // container for running child
		"child-run":   true,
		"root-b":      true,
		"child-b-run": true,
	}
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	for id := range want {
		if !got[id] {
			t.Errorf("working view missing %q; got %v", id, ids)
		}
	}
	if got["root-c"] || got["child-done"] {
		t.Errorf("working view includes idle nodes: %v", ids)
	}
}

func TestAgentsWindowFilterReady(t *testing.T) {
	w := loadAgentsFixture(t)
	w.viewFilter = agentsFilterReady
	ids := agentsVisibleIDs(w)
	got := map[string]bool{}
	for _, id := range ids {
		got[id] = true
	}
	if !got["root-c"] {
		t.Errorf("ready view missing idle root; got %v", ids)
	}
	if !got["child-done"] {
		t.Errorf("ready view missing completed child; got %v", ids)
	}
	// root-a is container for completed child
	if !got["root-a"] {
		t.Errorf("ready view missing container root-a; got %v", ids)
	}
	if got["root-b"] || got["child-run"] || got["child-b-run"] {
		t.Errorf("ready view includes in-flight nodes: %v", ids)
	}
}

func TestAgentsWindowFilterRootsOnly(t *testing.T) {
	w := loadAgentsFixture(t)
	w.viewFilter = agentsFilterRoots
	ids := agentsVisibleIDs(w)
	if len(ids) != 3 {
		t.Fatalf("roots-only len = %d (%v), want 3 parents", len(ids), ids)
	}
	for _, id := range ids {
		if strings.HasPrefix(id, "child-") {
			t.Errorf("roots-only leaked child %q", id)
		}
	}
	plain := ansi.Strip(w.view(theme.Default()))
	if strings.Contains(plain, "explore") || strings.Contains(plain, "scan") {
		t.Errorf("roots-only shows children: %q", plain)
	}
}

func TestAgentsWindowFilterEmptyStates(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "r1",
		roots: []agentsRootSnap{
			{ID: "r1", Title: "only ready", State: theme.AgentStateReady},
		},
	})
	w = next.(agentsWindow)

	cases := []struct {
		filter agentsViewFilter
		want   string
	}{
		{agentsFilterAttention, "no agents need attention"},
		{agentsFilterWorking, "no agents working"},
	}
	for _, tt := range cases {
		w.viewFilter = tt.filter
		w.nodes = w.buildNodes(theme.Default())
		plain := ansi.Strip(w.view(theme.Default()))
		if !strings.Contains(plain, tt.want) {
			t.Errorf("filter %s empty = %q, want %q", tt.filter.label(), plain, tt.want)
		}
	}

	w.viewFilter = agentsFilterAll
	w.textFilter = "zzz-nope"
	w.nodes = w.buildNodes(theme.Default())
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "no matches") {
		t.Errorf("text filter empty = %q", plain)
	}
}

func TestAgentsWindowCycleFilterKey(t *testing.T) {
	w := loadAgentsFixture(t)
	if w.viewFilter != agentsFilterAll {
		t.Fatalf("start filter = %v", w.viewFilter)
	}
	seq := []agentsViewFilter{
		agentsFilterAttention,
		agentsFilterWorking,
		agentsFilterReady,
		agentsFilterRoots,
		agentsFilterAll,
	}
	for _, want := range seq {
		next, _ := w.update(tea.KeyPressMsg{Code: 'f', Text: string([]rune{'f'})})
		w = next.(agentsWindow)
		if w.viewFilter != want {
			t.Fatalf("after f: filter = %v, want %v", w.viewFilter, want)
		}
	}
	if !strings.Contains(w.title(), "agents") {
		t.Errorf("title = %q", w.title())
	}
}

func TestAgentsWindowTitleAttentionCount(t *testing.T) {
	w := loadAgentsFixture(t)
	th := theme.Default().Resolve()
	wantNeed := dotJoin(th, "agents", "1 need you")
	if got := w.title(); got != wantNeed {
		t.Errorf("title = %q, want %q", got, wantNeed)
	}
	w.viewFilter = agentsFilterWorking
	wantWorking := dotJoin(th, "agents", "working")
	if got := w.title(); got != wantWorking {
		t.Errorf("working title = %q, want %q", got, wantWorking)
	}
	w.viewFilter = agentsFilterAttention
	if got := w.title(); got != wantNeed {
		t.Errorf("attention title = %q, want %q", got, wantNeed)
	}
}

func TestAgentsWindowTextFilter(t *testing.T) {
	w := loadAgentsFixture(t)
	next, _ := w.update(tea.KeyPressMsg{Code: '/', Text: string([]rune{'/'})})
	w = next.(agentsWindow)
	if !w.filterEdit {
		t.Fatal(" / did not enter filter edit")
	}
	for _, r := range "idle" {
		next, _ = w.update(tea.KeyPressMsg{Text: string([]rune{r})})
		w = next.(agentsWindow)
	}
	ids := agentsVisibleIDs(w)
	if len(ids) != 1 || ids[0] != "root-c" {
		t.Fatalf("text filter ids = %v, want [root-c]", ids)
	}
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "filter:") {
		t.Errorf("missing filter header: %q", plain)
	}
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyEsc})
	w = next.(agentsWindow)
	if w.filterEdit || w.textFilter != "" {
		t.Fatalf("esc should clear filter: edit=%v text=%q", w.filterEdit, w.textFilter)
	}
}

func TestAgentsWindowMultiRootWithFilters(t *testing.T) {
	// ≥2 parents from #176 remain visible under all / roots-only.
	w := loadAgentsFixture(t)
	all := agentsVisibleIDs(w)
	if len(all) < 5 {
		t.Fatalf("all view too small: %v", all)
	}
	w.viewFilter = agentsFilterRoots
	roots := agentsVisibleIDs(w)
	if len(roots) != 3 {
		t.Fatalf("roots = %v", roots)
	}
}

func TestAgentsWindowHideKeyEmitsMsg(t *testing.T) {
	w := newAgentsWindow().resize(40, 6).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID: "root-a",
		roots: []agentsRootSnap{
			{ID: "root-a", Title: "a", State: theme.AgentStateReady},
			{ID: "root-b", Title: "b", State: theme.AgentStateReady},
		},
	})
	w = next.(agentsWindow)
	next, _ = w.update(tea.KeyPressMsg{Code: tea.KeyDown})
	w = next.(agentsWindow)
	next, cmd := w.update(tea.KeyPressMsg{Code: 'd', Text: string([]rune{'d'})})
	if cmd == nil {
		t.Fatal("d produced no cmd")
	}
	msg := cmd()
	hm, ok := msg.(agentsHideMsg)
	if !ok || hm.sessionID != "root-b" {
		t.Fatalf("hide msg = %#v, want agentsHideMsg{root-b}", msg)
	}
	_ = next
}

func TestAgentsHideRemovesFromPaneKeepsSession(t *testing.T) {
	fs := newFakeSessions()
	rootLog := mustSessionJSONL(t,
		protocol.UserMessage{Text: "done work"},
		protocol.TextDelta{Text: "finished reply"},
	)
	fs.put(host.Session{ID: "root-a", Title: "active"}, nil)
	fs.put(host.Session{ID: "root-b", Title: "completed task"}, rootLog)

	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.services.Roots = fr
	m.services.Sessions = fs
	m.roots = map[string]*rootPane{
		"root-b": {
			sessionID:  "root-b",
			titleTopic: "completed task",
			toolByID:   map[string]*toolCell{},
			cells:      []cell{&userCell{text: "done work"}},
		},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg
	m.focus = focusRight
	m.windows, _ = m.windows.broadcast(m.agentsStateSnapshot())

	// Hide completed background root.
	m = updateApp(t, m, agentsHideMsg{sessionID: "root-b"})
	if !m.isAgentHidden("root-b") {
		t.Fatal("root-b should be hidden")
	}
	if m.noticeErr || !strings.Contains(m.notice, "session kept") {
		t.Fatalf("notice = %q err=%v", m.notice, m.noticeErr)
	}
	// Gone from pane snapshot without restart.
	snap := m.agentsStateSnapshot()
	for _, r := range snap.roots {
		if r.ID == "root-b" {
			t.Fatalf("hidden root still in snapshot: %+v", snap.roots)
		}
	}
	aw := agentsWindowFrom(t, m)
	plain := ansi.Strip(aw.view(theme.Default()))
	if strings.Contains(plain, "completed task") {
		t.Errorf("agents pane still shows hidden root: %q", plain)
	}
	// Other root unaffected.
	foundA := false
	for _, r := range snap.roots {
		if r.ID == "root-a" {
			foundA = true
		}
	}
	if !foundA {
		t.Fatalf("active root missing from snapshot: %+v", snap.roots)
	}
	// Persistence invariants: no delete, no interrupt, JSONL intact.
	if _, ok := fs.byID["root-b"]; !ok {
		t.Fatal("session deleted from store")
	}
	data, err := fs.ReplayJSONL("root-b")
	if err != nil || !strings.Contains(string(data), "finished reply") {
		t.Fatalf("ReplayJSONL = %q err=%v", data, err)
	}
	if len(fr.interrupted) != 0 {
		t.Fatalf("interrupt called: %v", fr.interrupted)
	}
	// Still listed by /session.
	next, _ := m.handleCommand("/session")
	nm := next.(Model)
	sm, ok := nm.modal.(*sessionModal)
	if !ok || sm == nil {
		t.Fatalf("modal = %T", nm.modal)
	}
	found := false
	for _, s := range sm.all {
		if s.ID == "root-b" {
			found = true
			if s.Title != "completed task" {
				t.Errorf("session title = %q", s.Title)
			}
		}
	}
	if !found {
		t.Fatalf("/session missing root-b: %+v", sm.all)
	}
	// LiveIDs unchanged (still in-process; only pane filter).
	lives := fr.LiveIDs()
	if len(lives) != 2 {
		t.Fatalf("LiveIDs = %v, want both roots still live", lives)
	}
}

func TestAgentsHideChildCompleted(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.children = []childActivity{
		{sessionID: "child-done", parentID: "root-a", agent: "explore", status: string(protocol.ChildStatusCompleted)},
		{sessionID: "child-run", parentID: "root-a", agent: "build", status: "running"},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updateApp(t, m, agentsHideMsg{sessionID: "child-done"})
	if !m.isAgentHidden("child-done") {
		t.Fatal("child-done not hidden")
	}
	snap := m.agentsStateSnapshot()
	if len(snap.roots) != 1 {
		t.Fatalf("roots = %+v", snap.roots)
	}
	for _, ch := range snap.roots[0].Children {
		if ch.sessionID == "child-done" {
			t.Fatal("hidden child still in snapshot")
		}
		if ch.sessionID == "child-run" {
			// ok
		}
	}
	// Sibling still present.
	foundRun := false
	for _, ch := range snap.roots[0].Children {
		if ch.sessionID == "child-run" {
			foundRun = true
		}
	}
	if !foundRun {
		t.Fatal("running sibling removed")
	}
}

func TestAgentsHideIneligibleRunningAndActive(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {sessionID: "root-b", toolByID: map[string]*toolCell{}, turnRunning: true},
	}
	m.children = []childActivity{
		{sessionID: "child-run", parentID: "root-a", agent: "explore", status: "running"},
	}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Active root blocked.
	m = updateApp(t, m, agentsHideMsg{sessionID: "root-a"})
	if m.isAgentHidden("root-a") {
		t.Fatal("active root must not hide")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "active") {
		t.Fatalf("active notice = %q", m.notice)
	}

	// Running background root blocked.
	m = updateApp(t, m, agentsHideMsg{sessionID: "root-b"})
	if m.isAgentHidden("root-b") {
		t.Fatal("running root must not hide")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "running") {
		t.Fatalf("running root notice = %q", m.notice)
	}
	if len(fr.interrupted) != 0 {
		t.Fatalf("interrupt on ineligible hide: %v", fr.interrupted)
	}

	// Running child blocked.
	m = updateApp(t, m, agentsHideMsg{sessionID: "child-run"})
	if m.isAgentHidden("child-run") {
		t.Fatal("running child must not hide")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "running") {
		t.Fatalf("running child notice = %q", m.notice)
	}
}

func TestAgentsHideRevealWhenBusy(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {sessionID: "root-b", toolByID: map[string]*toolCell{}},
	}
	m.agentsHidden = map[string]bool{"root-b": true}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})

	// Background turn starts → auto-reveal.
	m = updateApp(t, m, engineEventMsg{ev: protocol.TurnStarted{
		Correlation: protocol.Correlation{SessionID: "root-b"},
	}})
	_ = m.broadcastAgentsState()
	if m.isAgentHidden("root-b") {
		t.Fatal("busy hidden root should auto-reveal")
	}
	snap := m.agentsStateSnapshot()
	found := false
	for _, r := range snap.roots {
		if r.ID == "root-b" {
			found = true
		}
	}
	if !found {
		t.Fatalf("revealed root missing: %+v", snap.roots)
	}
}

func TestAgentsHideUnhideOnActivate(t *testing.T) {
	fr := &fakeRoots{
		active: "root-a",
		live:   []string{"root-a", "root-b"},
	}
	m, _ := newAppTestModel(nil, nil)
	m.sessionID = "root-a"
	m.services.Roots = fr
	m.roots = map[string]*rootPane{
		"root-b": {
			sessionID:  "root-b",
			titleTopic: "beta",
			toolByID:   map[string]*toolCell{},
			cells:      []cell{&userCell{text: "from b"}},
		},
	}
	m.agentsHidden = map[string]bool{"root-b": true}
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m = updateApp(t, m, agentsOpenMsg{sessionID: "root-b"})
	if m.sessionID != "root-b" {
		t.Fatalf("sessionID = %q", m.sessionID)
	}
	if m.isAgentHidden("root-b") {
		t.Fatal("activated root should unhide")
	}
}

func TestAgentsOrchChipsBlockedConflictBudgetVerify(t *testing.T) {
	th := theme.Default().Resolve()
	// Blocked child.
	blocked := childActivity{
		sessionID:   "b1",
		status:      string(protocol.ChildStatusBlocked),
		blockReason: "permission",
	}
	suf := ansi.Strip(agentsOrchSuffix(th, blocked))
	if !strings.Contains(suf, "blocked") {
		t.Fatalf("blocked chip missing: %q", suf)
	}

	// Conflict.
	conflict := childActivity{
		sessionID: "c1",
		status:    "running",
		pathOverlaps: []childPathOverlap{
			{path: "a.go", policy: "block", blocked: true},
		},
	}
	suf = ansi.Strip(agentsOrchSuffix(th, conflict))
	if !strings.Contains(suf, "conflict") {
		t.Fatalf("conflict chip missing: %q", suf)
	}

	// Escalated / over-budget.
	esc := childActivity{
		sessionID:    "e1",
		status:       "running",
		escalateKind: "tool_calls",
	}
	suf = ansi.Strip(agentsOrchSuffix(th, esc))
	if !strings.Contains(suf, "escalated") {
		t.Fatalf("escalated chip missing: %q", suf)
	}
	over := childActivity{
		sessionID: "o1",
		status:    "running",
		budget:    &protocol.AgentBudgetView{MaxToolCalls: 3, ToolCalls: 3},
	}
	suf = ansi.Strip(agentsOrchSuffix(th, over))
	if !strings.Contains(suf, "over budget") {
		t.Fatalf("over budget chip missing: %q", suf)
	}

	// Claimed / unverified (terminal verification).
	claimed := childActivity{
		sessionID: "v1",
		status:    string(protocol.ChildStatusCompleted),
		verification: &childVerificationSummary{
			claimed: true, verified: false, passed: false,
		},
	}
	suf = ansi.Strip(agentsOrchSuffix(th, claimed))
	if !strings.Contains(suf, "claimed") {
		t.Fatalf("claimed chip missing: %q", suf)
	}
	// Verified success stays quiet.
	ok := childActivity{
		sessionID: "v2",
		status:    string(protocol.ChildStatusCompleted),
		verification: &childVerificationSummary{
			claimed: true, verified: true, passed: true,
		},
	}
	if got := agentsOrchSuffix(th, ok); got != "" {
		t.Fatalf("verified success should be quiet, got %q", ansi.Strip(got))
	}
	// No report → no chip.
	if got := agentsOrchSuffix(th, childActivity{sessionID: "n", status: "completed"}); got != "" {
		t.Fatalf("no-report should be quiet, got %q", ansi.Strip(got))
	}
}

func TestAgentsOrchChipsInTreeView(t *testing.T) {
	w := newAgentsWindow().resize(64, 12).(agentsWindow)
	next, _ := w.update(agentsStateMsg{
		activeID:  "root",
		viewingID: "root",
		roots: []agentsRootSnap{
			{
				ID:    "root",
				Title: "lead",
				State: theme.AgentStateWorking,
				Children: []childActivity{
					{
						sessionID:   "child-block",
						parentID:    "root",
						agent:       "build",
						status:      string(protocol.ChildStatusBlocked),
						blockReason: "ask",
						pathOverlaps: []childPathOverlap{
							{path: "x.go", policy: "warn"},
						},
					},
					{
						sessionID: "child-plain",
						parentID:  "root",
						agent:     "explore",
						status:    "running",
					},
				},
			},
		},
	})
	w = next.(agentsWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "blocked") {
		t.Fatalf("tree missing blocked chip:\n%s", plain)
	}
	if !strings.Contains(plain, "conflict") {
		t.Fatalf("tree missing conflict chip:\n%s", plain)
	}
	// Plain running child has no orch chips.
	// Filters still work.
	w.viewFilter = agentsFilterWorking
	w.nodes = w.buildNodes(theme.Default())
	ids := agentsVisibleIDs(w)
	// blocked maps to attention-ish; working filter may exclude blocked
	// Ensure cycle still functions.
	w.viewFilter = agentsFilterAll
	w.nodes = w.buildNodes(theme.Default())
	ids = agentsVisibleIDs(w)
	found := false
	for _, id := range ids {
		if id == "child-block" {
			found = true
		}
	}
	if !found {
		t.Fatalf("child-block missing under all filter: %v", ids)
	}
}

func TestAgentsOrchChipsDropOnNarrowWidth(t *testing.T) {
	// Tree drops suffix when it cannot fit; row must stay width-safe.
	msg := agentsStateMsg{
		activeID:  "root",
		viewingID: "root",
		roots: []agentsRootSnap{
			{
				ID:    "root",
				Title: "lead-with-a-long-title",
				State: theme.AgentStateWorking,
				Children: []childActivity{
					{
						sessionID:    "child",
						parentID:     "root",
						agent:        "implementer-with-long-name",
						status:       string(protocol.ChildStatusBlocked),
						blockReason:  "permission",
						escalateKind: "stall",
						pathOverlaps: []childPathOverlap{{path: "a.go", blocked: true, policy: "block"}},
						verification: &childVerificationSummary{claimed: true, verified: false, passed: false},
					},
				},
			},
		},
	}
	for _, width := range []int{12, 20, 32, 48, 80} {
		w := newAgentsWindow().resize(width, 10).(agentsWindow)
		next, _ := w.update(msg)
		view := next.view(theme.Default())
		for i, line := range strings.Split(view, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width %d line %d width %d: %q", width, i, got, ansi.Strip(line))
			}
		}
	}
}

func TestAgentsOrchChipBound(t *testing.T) {
	th := theme.Default().Resolve()
	// All four flags → at most agentsMaxOrchChips badges.
	ch := childActivity{
		sessionID:    "x",
		status:       string(protocol.ChildStatusBlocked),
		blockReason:  "ask",
		escalateKind: "loop",
		pathOverlaps: []childPathOverlap{{path: "a.go", blocked: true}},
		verification: &childVerificationSummary{claimed: true, verified: false, passed: false},
	}
	suf := agentsOrchSuffix(th, ch)
	// Count badge-ish tokens by label presence; max 3 of the 4.
	n := 0
	plain := ansi.Strip(suf)
	for _, label := range []string{"blocked", "conflict", "loop", "escalated", "claimed", "unverified", "over budget", "stall"} {
		if strings.Contains(plain, label) {
			n++
		}
	}
	if n > agentsMaxOrchChips {
		t.Fatalf("chip count %d > max %d: %q", n, agentsMaxOrchChips, plain)
	}
	if n < 1 {
		t.Fatalf("expected some chips, got %q", plain)
	}
}
