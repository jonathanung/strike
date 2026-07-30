package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

type statefulTestWindow struct {
	windowID    string
	windowTitle string
	updates     []string
	width       int
	height      int
}

func (w statefulTestWindow) id() string    { return w.windowID }
func (w statefulTestWindow) title() string { return w.windowTitle }
func (w statefulTestWindow) init() tea.Cmd { return nil }
func (w statefulTestWindow) update(msg tea.Msg) (window, tea.Cmd) {
	if km, ok := msg.(tea.KeyPressMsg); ok {
		w.updates = append(w.updates, km.String())
	}
	return w, nil
}
func (w statefulTestWindow) resize(width, height int) window {
	w.width, w.height = width, height
	return w
}
func (w statefulTestWindow) view(theme.Theme) string { return w.windowID }

var _ window = statefulTestWindow{}

func testWindow(t *testing.T, w window) statefulTestWindow {
	t.Helper()
	got, ok := w.(statefulTestWindow)
	if !ok {
		t.Fatalf("window = %T, want statefulTestWindow", w)
	}
	return got
}

func TestWindowRegistryPreservesOrderAndValueStateAcrossCycleUpdateAndResize(t *testing.T) {
	r := windowRegistry{windows: []window{
		statefulTestWindow{windowID: "one", windowTitle: "One"},
		statefulTestWindow{windowID: "two", windowTitle: "Two"},
	}}
	if got := r.active().id(); got != "one" {
		t.Fatalf("initial active window = %q, want one", got)
	}
	r = r.cycle()
	if got := r.active().id(); got != "two" {
		t.Fatalf("cycled active window = %q, want two", got)
	}
	r, _ = r.update(tea.KeyPressMsg{Text: "x"})
	if got := testWindow(t, r.active()).updates; len(got) != 1 || got[0] != "x" {
		t.Fatalf("active update = %q, want [x]", got)
	}
	r = r.resize(23, 7)
	if got := testWindow(t, r.active()); got.width != 23 || got.height != 7 || len(got.updates) != 1 {
		t.Errorf("active resize replacement = %+v, want dimensions and prior state", got)
	}
	r = r.cycle()
	if got := testWindow(t, r.active()); got.windowID != "one" || len(got.updates) != 0 || got.width != 23 || got.height != 7 {
		t.Errorf("inactive window was changed or not resized: %+v", got)
	}
	r = r.cycle()
	if got := testWindow(t, r.active()); got.windowID != "two" || len(got.updates) != 1 || got.updates[0] != "x" {
		t.Errorf("active state did not survive cycling away and back: %+v", got)
	}
}

func TestModelsHaveIsolatedWindowRegistryState(t *testing.T) {
	left, _ := newAppTestModel(nil, nil)
	right, _ := newAppTestModel(nil, nil)
	left.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "left"}}}
	right.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "right"}}}
	left.focus = focusRight
	left = updateApp(t, left, tea.KeyPressMsg{Text: "a"})
	left = updateApp(t, left, tea.WindowSizeMsg{Width: 11, Height: 5})

	if got := testWindow(t, left.windows.active()); len(got.updates) != 1 || got.width != 11 || got.height != 3 {
		t.Errorf("left model window = %+v, want its update and compact right-pane body dimensions 11x3", got)
	}
	if got := testWindow(t, right.windows.active()); len(got.updates) != 0 || got.width != 0 || got.height != 0 {
		t.Errorf("right model shared left window state: %+v", got)
	}
}

func TestRightWindowResizeUsesActualPanelInnerHeight(t *testing.T) {
	for _, tt := range []struct {
		name          string
		width, height int
		wantHeight    int
	}{
		{"one-column unbordered pane", 1, 40, 38},
		{"canonical bordered pane", 80, 40, 36},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.focus = focusRight
			m.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "right"}}}
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})

			got := testWindow(t, m.windows.active())
			if got.width != ui.PanelInnerWidth(m.th, tt.width) || got.height != tt.wantHeight {
				t.Errorf("%dx%d right window dimensions = %dx%d, want %dx%d", tt.width, tt.height, got.width, got.height, ui.PanelInnerWidth(m.th, tt.width), tt.wantHeight)
			}
		})
	}
}

func TestCompactRightPaneIsBorderlessAndUsesFullBodyDimensionsAtThresholds(t *testing.T) {
	for _, tt := range []struct {
		name                        string
		width, height, wantW, wantH int
		borderless                  bool
	}{
		{"59x30 compact width", 59, 30, 59, 28, true},
		{"80x19 compact height", 80, 19, 80, 17, true},
		// Solid chrome: PanelInnerWidth drops only horizontal pad (XS=1 each side).
		{"60x20 solid threshold", 60, 20, 58, 16, false},
		{"93x60 canonical split", 93, 60, 30, 56, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.focus = focusRight
			m.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "right", windowTitle: "context"}}}
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})

			got := testWindow(t, m.windows.active())
			if got.width != tt.wantW || got.height != tt.wantH {
				t.Errorf("registry dimensions = %dx%d, want %dx%d", got.width, got.height, tt.wantW, tt.wantH)
			}
			view := viewString(m)
			rows := strings.Split(view, "\n")
			if len(rows) != tt.height {
				t.Fatalf("canvas rows = %d, want %d", len(rows), tt.height)
			}
			for i, row := range rows {
				if got := lipgloss.Width(row); got != tt.width {
					t.Errorf("canvas row %d width = %d, want %d", i, got, tt.width)
				}
			}
			plain := strings.Join(rows, "\n")
			if tt.borderless && (strings.ContainsAny(plain, "╭╰") || strings.Contains(plain, "context")) {
				t.Errorf("compact right pane retained panel chrome: %q", plain)
			}
			if !tt.borderless && !strings.Contains(plain, "context") {
				t.Errorf("solid right pane omitted its title: %q", plain)
			}
		})
	}
}

func TestDefaultWindowRegistryHasUniqueWidthSafeWindows(t *testing.T) {
	r := newWindowRegistry()
	if len(r.windows) != 10 {
		t.Fatalf("window count = %d, want 10", len(r.windows))
	}
	wantIDs := []string{"context", "activity", "telemetry", "agents", "visualizer", "files", "memory", "issues", "markdown", "editor"}
	seenIDs, seenTitles := map[string]bool{}, map[string]bool{}
	for i, w := range r.windows {
		if w.id() != wantIDs[i] {
			t.Errorf("window[%d] id = %q, want %q", i, w.id(), wantIDs[i])
		}
		if w.id() == "" || w.title() == "" || seenIDs[w.id()] || seenTitles[w.title()] {
			t.Errorf("window identity is missing or not unique: id=%q title=%q", w.id(), w.title())
		}
		seenIDs[w.id()], seenTitles[w.title()] = true, true

		switch w.id() {
		case "context":
			if _, ok := w.(contextWindow); !ok {
				t.Errorf("window = %#v, want a contextWindow", w)
			}
			resized := w.resize(80, 12)
			updated, _ := resized.update(contextStateMsg{
				WorkDir: "/tmp/proj", SessionID: "sess-1",
				Provider: "echo", Model: "echo-1",
				Used:         protocol.KnownTokens(215_000),
				ContextLimit: 1_000_000, ContextLimitKnown: true,
			})
			view := updated.view(theme.Default())
			if !strings.Contains(view, "directory") || !strings.Contains(ansi.Strip(view), "215k") {
				t.Errorf("context window missing expected content: %q", view)
			}
			view = updated.resize(8, 12).view(theme.Default())
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 8 {
					t.Errorf("context line width = %d, want <= 8: %q", got, line)
				}
			}
		case "memory":
			if _, ok := w.(memoryWindow); !ok {
				t.Errorf("window = %#v, want a memoryWindow", w)
			}
			view := w.resize(24, 6).view(theme.Default())
			if !strings.Contains(ansi.Strip(view), "unavailable") {
				t.Errorf("unbound memory window = %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 24 {
					t.Errorf("memory line width = %d, want <= 24: %q", got, line)
				}
			}
		case "issues":
			if _, ok := w.(issuesWindow); !ok {
				t.Errorf("window = %#v, want an issuesWindow", w)
			}
			view := w.resize(24, 6).view(theme.Default())
			if !strings.Contains(ansi.Strip(view), "unavailable") {
				t.Errorf("unbound issues window = %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 24 {
					t.Errorf("issues line width = %d, want <= 24: %q", got, line)
				}
			}
		case "activity":
			if _, ok := w.(namedWindow); !ok {
				t.Errorf("window = %#v, want a namedWindow", w)
			}
			// Resize is width-safe: dimensions stick and view stays within width.
			for _, size := range []struct{ w, h int }{{80, 3}, {12, 5}, {1, 1}} {
				resized := w.resize(size.w, size.h)
				nw, ok := resized.(namedWindow)
				if !ok || nw.width != size.w || nw.height != size.h {
					t.Errorf("resize(%d,%d) = %#v, want namedWindow %dx%d", size.w, size.h, resized, size.w, size.h)
				}
			}
		case "telemetry":
			if _, ok := w.(telemetryWindow); !ok {
				t.Errorf("window = %#v, want a telemetryWindow", w)
			}
			view := w.resize(32, 4).view(theme.Default())
			if !strings.Contains(ansi.Strip(view), "unavailable") {
				t.Errorf("unbound telemetry window = %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 32 {
					t.Errorf("telemetry line width = %d, want <= 32: %q", got, line)
				}
			}
		case "agents":
			if _, ok := w.(agentsWindow); !ok {
				t.Errorf("window = %#v, want an agentsWindow", w)
			}
			view := w.resize(24, 6).view(theme.Default())
			if !strings.Contains(ansi.Strip(view), "no subagents") {
				t.Errorf("empty agents window = %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 24 {
					t.Errorf("agents line width = %d, want <= 24: %q", got, line)
				}
			}
		case "visualizer":
			if _, ok := w.(visualizerWindow); !ok {
				t.Errorf("window = %#v, want a visualizerWindow", w)
			}
			view := w.resize(24, 8).view(theme.Default())
			if !strings.Contains(ansi.Strip(view), "select a session") {
				t.Errorf("empty visualizer = %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 24 {
					t.Errorf("visualizer line width = %d, want <= 24: %q", got, line)
				}
			}
		case "files":
			fw, ok := w.(filesWindow)
			if !ok {
				t.Fatalf("files window = %T, want filesWindow", w)
			}
			view := fw.resize(12, 3).view(theme.Default())
			if !strings.Contains(ansi.Strip(view), "unavailable") && !strings.Contains(ansi.Strip(view), "no workspace") {
				t.Errorf("files empty state missing prompt: %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 12 {
					t.Errorf("files line width %d > 12: %q", got, line)
				}
			}
		case "editor":
			tw, ok := w.(terminalWindow)
			if !ok {
				t.Fatalf("editor window = %T, want terminalWindow", w)
			}
			view := tw.resize(12, 3).view(theme.Default())
			if !strings.Contains(view, "No editor") || !strings.Contains(view, "/vim") {
				t.Errorf("editor empty state missing prompt: %q", view)
			}
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 12 {
					t.Errorf("editor line width %d > 12: %q", got, line)
				}
			}
		case "markdown":
			mw, ok := w.(markdownWindow)
			if !ok {
				t.Fatalf("markdown window = %T, want markdownWindow", w)
			}
			wide := mw.resize(40, 3).view(theme.Default())
			if !strings.Contains(wide, "No file open") {
				t.Errorf("markdown empty state missing prompt: %q", wide)
			}
			view := mw.resize(8, 3).view(theme.Default())
			for _, line := range strings.Split(view, "\n") {
				if got := lipgloss.Width(line); got > 8 {
					t.Errorf("markdown empty line width = %d, want <= 8: %q", got, line)
				}
			}
		default:
			t.Errorf("unexpected window id %q", w.id())
		}
	}
	if !seenIDs["context"] || !seenIDs["activity"] || !seenIDs["telemetry"] || !seenIDs["agents"] || !seenIDs["visualizer"] || !seenIDs["files"] || !seenIDs["memory"] || !seenIDs["issues"] || !seenIDs["markdown"] || !seenIDs["editor"] {
		t.Errorf("default registry ids = %v, want context, activity, telemetry, agents, visualizer, files, memory, issues, markdown, and editor", seenIDs)
	}

	// Full Model.View at split size shows real context content, not a placeholder.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 93, Height: 40})
	plain := ansi.Strip(viewString(m))
	if strings.Contains(plain, "Context window placeholder.") {
		t.Errorf("split view still shows placeholder copy:\n%s", plain)
	}
	if !strings.Contains(plain, "none") && !strings.Contains(plain, "/provider") {
		t.Errorf("split view missing real context content (none or /provider):\n%s", plain)
	}
	if !strings.Contains(plain, "context") {
		t.Errorf("split view missing context window title:\n%s", plain)
	}
}

func TestWindowRegistryActivateByID(t *testing.T) {
	r := newWindowRegistry()
	if got := r.active().id(); got != "context" {
		t.Fatalf("initial active = %q, want context", got)
	}
	next, ok := r.activate("markdown")
	if !ok || next.active().id() != "markdown" {
		t.Fatalf("activate(markdown) = ok=%v active=%q", ok, next.active().id())
	}
	if r.active().id() != "context" {
		t.Errorf("activate mutated original registry active to %q", r.active().id())
	}
	_, ok = next.activate("missing")
	if ok {
		t.Error("activate(missing) returned ok=true")
	}
	if next.active().id() != "markdown" {
		t.Errorf("failed activate changed active to %q", next.active().id())
	}
}

func TestWindowRegistryReplaceByID(t *testing.T) {
	r := newWindowRegistry()
	replacement := statefulTestWindow{windowID: "markdown", windowTitle: "Replaced"}

	withActivate, ok := r.replace("markdown", replacement, true)
	if !ok {
		t.Fatal("replace(markdown, activate) returned ok=false")
	}
	if withActivate.active().id() != "markdown" || withActivate.active().title() != "Replaced" {
		t.Errorf("activated replace active = %q/%q", withActivate.active().id(), withActivate.active().title())
	}

	without, ok := r.replace("markdown", replacement, false)
	if !ok {
		t.Fatal("replace(markdown, no activate) returned ok=false")
	}
	if without.active().id() != "context" {
		t.Errorf("replace without activate changed active to %q", without.active().id())
	}
	found := false
	for _, w := range without.windows {
		if w.id() == "markdown" && w.title() == "Replaced" {
			found = true
		}
	}
	if !found {
		t.Error("replace without activate did not swap the markdown window")
	}

	_, ok = r.replace("missing", replacement, true)
	if ok {
		t.Error("replace(missing) returned ok=true")
	}
}

func TestWindowRegistryCycleIncludesFilesAndMarkdown(t *testing.T) {
	r := newWindowRegistry()
	var order []string
	// Telemetry on by default — 10 cycleable windows + wrap.
	for range 11 {
		order = append(order, r.active().id())
		r = r.cycle()
	}
	wantOn := []string{"context", "activity", "telemetry", "agents", "visualizer", "files", "memory", "issues", "markdown", "editor", "context"}
	if !stringsEqual(order, wantOn) {
		t.Errorf("cycle with telemetry = %q, want %q", order, wantOn)
	}
	r, _ = setTelemetryEnabled(newWindowRegistry(), false)
	order = nil
	for range 10 {
		order = append(order, r.active().id())
		r = r.cycle()
	}
	wantOff := []string{"context", "activity", "agents", "visualizer", "files", "memory", "issues", "markdown", "editor", "context"}
	if !stringsEqual(order, wantOff) {
		t.Errorf("cycle without telemetry = %q, want %q", order, wantOff)
	}
}

func TestWindowRegistryPreservesMarkdownScrollAcrossCycle(t *testing.T) {
	r := newWindowRegistry()
	mw := newMarkdownWindow()
	mw.renderMarkdown = func(source string, width int) (string, error) {
		var b strings.Builder
		for i := 0; i < 80; i++ {
			b.WriteString("scroll-line\n")
		}
		return b.String(), nil
	}
	mw = mw.resize(40, 5).(markdownWindow)
	mw = mw.load("tall.md", "# tall")
	r, ok := r.replace(markdownWindowID, mw, true)
	if !ok {
		t.Fatal("replace markdown failed")
	}
	r, _ = r.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	r, _ = r.update(tea.KeyPressMsg{Code: tea.KeyPgDown})
	active := r.active().(markdownWindow)
	wantOffset := active.vp.YOffset()
	if wantOffset == 0 {
		t.Fatal("setup did not scroll markdown content")
	}

	// Cycle through remaining windows and back around to markdown (telemetry on).
	r = r.cycle() // editor
	r = r.cycle() // context
	r = r.cycle() // activity
	r = r.cycle() // telemetry
	r = r.cycle() // agents
	r = r.cycle() // visualizer
	r = r.cycle() // files
	r = r.cycle() // memory
	r = r.cycle() // issues
	r = r.cycle() // markdown again
	got := r.active().(markdownWindow)
	if got.vp.YOffset() != wantOffset {
		t.Errorf("YOffset after cycle away/back = %d, want %d", got.vp.YOffset(), wantOffset)
	}
}

func stringsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestDefaultWindowGroupsPairRelatedPanes(t *testing.T) {
	r := newWindowRegistry()
	want := []struct {
		id      string
		members []string
	}{
		{"session", []string{"context", "activity", "telemetry"}},
		{"agents", []string{"agents", "visualizer"}},
		{"files", []string{"files"}},
		{"project", []string{"memory", "issues"}},
		{"markdown", []string{"markdown"}},
		{"editor", []string{"editor"}},
	}
	if len(r.groups) != len(want) {
		t.Fatalf("groups = %d, want %d", len(r.groups), len(want))
	}
	for i, g := range r.groups {
		if g.id != want[i].id {
			t.Errorf("group[%d] id = %q, want %q", i, g.id, want[i].id)
		}
		got := make([]string, len(g.members))
		for j, mi := range g.members {
			got[j] = r.windows[mi].id()
		}
		if !stringsEqual(got, want[i].members) {
			t.Errorf("group %q members = %q, want %q", g.id, got, want[i].members)
		}
	}
	if g := r.activeGroup(); g.id != "session" {
		t.Errorf("initial active group = %q, want session", g.id)
	}
	r, _ = setTelemetryEnabled(r, false)
	got := make([]string, 0, 2)
	for _, g := range r.groups {
		if g.id != "session" {
			continue
		}
		for _, mi := range g.members {
			got = append(got, r.windows[mi].id())
		}
	}
	if !stringsEqual(got, []string{"context", "activity"}) {
		t.Errorf("session without telemetry = %q", got)
	}
}

func TestWindowRegistryFocusCycleIsDeterministicAcrossGroups(t *testing.T) {
	r := newWindowRegistry()
	var order []string
	for range 13 {
		order = append(order, r.active().id())
		r = r.cycleBy(1)
	}
	want := []string{
		"context", "activity", "telemetry", "agents", "visualizer", "files", "memory",
		"issues", "markdown", "editor", "context", "activity", "telemetry",
	}
	if !stringsEqual(order, want) {
		t.Errorf("cycle order = %q, want %q", order, want)
	}
	// Reverse stays on the same ring.
	r = newWindowRegistry()
	r, _ = r.activate("editor")
	var back []string
	for range 3 {
		back = append(back, r.active().id())
		r = r.cycleBy(-1)
	}
	if !stringsEqual(back, []string{"editor", "markdown", "issues"}) {
		t.Errorf("reverse cycle = %q", back)
	}
}

func TestComputeMemberSlotsStableUnderResizeStorm(t *testing.T) {
	for _, tt := range []struct {
		name           string
		w, h, g, n     int
		pairHorizontal bool
		wantNil        bool
		wantSumW       int
		wantSumH       int
	}{
		{"too short vertical", 32, 5, 1, 2, false, true, 0, 0},
		{"too narrow horizontal", 5, 20, 1, 2, true, true, 0, 0},
		{"vertical pair 40", 32, 40, 1, 2, false, false, 32 * 2, 40},
		{"horizontal pair 40", 40, 20, 1, 2, true, false, 40, 20 * 2},
		{"odd height remainder", 28, 25, 1, 2, false, false, 28 * 2, 25},
		{"odd width remainder", 25, 18, 1, 2, true, false, 25, 18 * 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			// Storm: repeat geometry for stability.
			var first []memberSlot
			for i := 0; i < 50; i++ {
				got := computeMemberSlots(tt.w, tt.h, tt.g, tt.n, tt.pairHorizontal)
				if tt.wantNil {
					if got != nil {
						t.Fatalf("slots = %+v, want nil", got)
					}
					return
				}
				if got == nil || len(got) != tt.n {
					t.Fatalf("slots len = %d, want %d (%+v)", len(got), tt.n, got)
				}
				if i == 0 {
					first = append([]memberSlot(nil), got...)
				} else if !memberSlotsEqual(first, got) {
					t.Fatalf("slot storm diverged at iter %d: %+v vs %+v", i, first, got)
				}
			}
			sumW, sumH := 0, 0
			for _, s := range first {
				if s.width < minStackMemberOuter || s.height < minStackMemberOuter {
					t.Errorf("slot too small: %+v", s)
				}
				sumW += s.width
				sumH += s.height
			}
			if tt.pairHorizontal {
				// widths + gutters == total width; heights identical
				if sumW+tt.g*(tt.n-1) != tt.w {
					t.Errorf("width sum+gutter = %d, want %d", sumW+tt.g*(tt.n-1), tt.w)
				}
				if first[0].height != tt.h || first[1].height != tt.h {
					t.Errorf("horizontal pair heights = %d/%d, want %d", first[0].height, first[1].height, tt.h)
				}
			} else {
				if sumH+tt.g*(tt.n-1) != tt.h {
					t.Errorf("height sum+gutter = %d, want %d", sumH+tt.g*(tt.n-1), tt.h)
				}
				if first[0].width != tt.w || first[1].width != tt.w {
					t.Errorf("vertical pair widths = %d/%d, want %d", first[0].width, first[1].width, tt.w)
				}
			}
		})
	}
}

func memberSlotsEqual(a, b []memberSlot) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestStackedRightPaneShowsPairedGroupTitles(t *testing.T) {
	for _, tt := range []struct {
		name       string
		activate   string
		telemetry  bool
		want       []string
		wantAbsent []string
	}{
		{"session", "context", true, []string{"context", "activity", "system"}, nil},
		{"session-no-telemetry", "context", false, []string{"context", "activity"}, []string{"system"}},
		{"agents", "agents", true, []string{"agents", "visualizer"}, nil},
		{"project", "memory", true, []string{"memory", "issues"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
			if !tt.telemetry {
				m.windows, _ = setTelemetryEnabled(m.windows, false)
			}
			var ok bool
			m.windows, ok = m.windows.activate(tt.activate)
			if !ok {
				t.Fatalf("activate(%s) failed", tt.activate)
			}
			m.reflow()
			plain := ansi.Strip(viewString(m))
			for _, title := range tt.want {
				if !strings.Contains(plain, title) {
					t.Errorf("split view missing %q title:\n%s", title, plain)
				}
			}
			for _, title := range tt.wantAbsent {
				if strings.Contains(plain, title) {
					t.Errorf("split view unexpectedly has %q:\n%s", title, plain)
				}
			}
		})
	}
}

func TestStackedRightPaneCollapsesWhenCompact(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.focus = focusRight
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 50, Height: 18})
	// Session group active (context); compact must not paint the pair partner title chrome.
	plain := ansi.Strip(viewString(m))
	if strings.Contains(plain, "╭") {
		t.Errorf("compact view retained panel chrome:\n%s", plain)
	}
	// Cycle still walks full focus order one pane at a time.
	start := m.windows.active().id()
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl})
	if m.windows.active().id() == start {
		t.Error("compact cycle did not advance focus")
	}
}

func TestStackedAgentsGroupResizesBothMembers(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	var ok bool
	m.windows, ok = m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.reflow()
	g := m.windows.activeGroup()
	if g.id != "agents" || len(g.members) != 2 {
		t.Fatalf("active group = %+v", g)
	}
	var agentsH, vizH, fullH int
	for i, w := range m.windows.windows {
		switch tw := w.(type) {
		case agentsWindow:
			if containsInt(g.members, i) {
				agentsH = tw.height
			}
		case visualizerWindow:
			if containsInt(g.members, i) {
				vizH = tw.height
			}
		case filesWindow:
			fullH = tw.height // singleton keeps full pane height
		}
	}
	if agentsH <= 0 || vizH <= 0 {
		t.Fatalf("stacked member heights agents=%d visualizer=%d", agentsH, vizH)
	}
	if absInt(agentsH-vizH) > 1 {
		t.Errorf("uneven stack heights agents=%d visualizer=%d", agentsH, vizH)
	}
	if fullH > 0 && agentsH+vizH >= fullH {
		t.Errorf("stacked heights %d+%d should each be under full pane %d", agentsH, vizH, fullH)
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
