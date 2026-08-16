package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestC2PaneGeometryCanonicalThresholdsAndClamping(t *testing.T) {
	tests := []struct {
		name                    string
		width, gutter           int
		focus                   paneFocus
		mode                    paneMode
		left, wantGutter, right int
	}{
		{"93 default", 93, 1, focusLeft, paneSplit, 60, 1, 32},
		{"120 default", 120, 1, focusLeft, paneSplit, 80, 1, 39},
		{"160 default", 160, 1, focusLeft, paneSplit, 106, 1, 53},
		{"92 left single", 92, 1, focusLeft, paneSingle, 92, 0, 0},
		{"80 left focused", 80, 1, focusLeft, paneSingle, 80, 0, 0},
		{"80 right focused", 80, 1, focusRight, paneSingle, 0, 0, 80},
		{"custom zero gutter threshold", 92, 0, focusLeft, paneSplit, 60, 0, 32},
		{"custom three gutter threshold", 95, 3, focusLeft, paneSplit, 60, 3, 32},
		{"custom zero gutter below", 91, 0, focusLeft, paneSingle, 91, 0, 0},
		{"custom three gutter below", 94, 3, focusLeft, paneSingle, 94, 0, 0},
		{"negative dimensions clamp", -10, -3, focusLeft, paneSingle, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computePaneGeometry(tt.width, tt.gutter, tt.focus)
			if got.mode != tt.mode || got.leftWidth != tt.left || got.gutter != tt.wantGutter || got.rightWidth != tt.right {
				t.Fatalf("geometry = %+v, want mode=%d left=%d gutter=%d right=%d", got, tt.mode, tt.left, tt.wantGutter, tt.right)
			}
			if got.leftWidth+got.gutter+got.rightWidth != max(0, tt.width) {
				t.Errorf("geometry does not allocate terminal width: %+v", got)
			}
		})
	}
	if got := computeLayout(93, 20, composerMinHeight, 0, false).compact; got {
		t.Error("93-column left pane at supported height unexpectedly compact")
	}
}

func TestC2VerticalBudgetIsExact(t *testing.T) {
	for _, tt := range []struct{ width, height, rows, popup int }{{93, 60, 2, 0}, {120, 80, 4, 3}, {160, 106, 8, 0}} {
		l := computeLayout(tt.width, tt.height, tt.rows, tt.popup, true)
		body := l.transcript + l.notice + l.popup + l.composer
		if body != tt.height-l.header-l.hints-l.danger {
			t.Errorf("%dx%d body = %d, want %d", tt.width, tt.height, body, tt.height-l.header-l.hints-l.danger)
		}
		if l.header+body+l.hints+l.danger != tt.height {
			t.Errorf("%dx%d total budget is not exact: %+v", tt.width, tt.height, l)
		}
	}
}

func TestC2ViewGeometryAndActivePanes(t *testing.T) {
	cases := []struct {
		name                string
		width, height       int
		focus               paneFocus
		left, gutter, right int
	}{
		{"93 split", 93, 60, focusLeft, 60, 1, 32},
		{"92 left single", 92, 60, focusLeft, 92, 0, 0},
		{"80 left", 80, 40, focusLeft, 80, 0, 0},
		{"80 right", 80, 40, focusRight, 0, 0, 80},
		{"120 split", 120, 80, focusLeft, 80, 1, 39},
		{"160 split", 160, 106, focusLeft, 106, 1, 53},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.firstRun = true // full view renders welcome dashboard
			m.focus = tt.focus
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})
			view := viewString(m)
			rows := strings.Split(view, "\n")
			if len(rows) != tt.height {
				t.Fatalf("line count = %d, want %d", len(rows), tt.height)
			}
			for i, row := range rows {
				if got := ansi.StringWidth(row); got > tt.width {
					t.Errorf("row %d width = %d, want <= %d", i, got, tt.width)
				}
			}
			plain := ansi.Strip(view)
			if tt.right > 0 && !strings.Contains(plain, "context") {
				t.Error("split/right-active view omitted right-pane title")
			}
			if tt.right == 0 && (strings.Contains(plain, "No active todos") || strings.Contains(plain, "directory")) {
				t.Error("left-only view rendered inactive right pane")
			}
			if tt.left == 0 && (strings.Contains(plain, "welcome") || strings.Contains(plain, "prompt")) {
				t.Error("right-only view rendered inactive left pane")
			}
			if tt.left > 0 && tt.right > 0 {
				// Welcome may paint a logo band above the card grid; find the
				// first body row that carries both left and right panel titles.
				panelRowIdx := -1
				for i, row := range rows {
					plain := ansi.Strip(row)
					if strings.Contains(plain, "first run") && strings.Contains(plain, "context") {
						panelRowIdx = i
						break
					}
				}
				if panelRowIdx < 0 {
					// Right pane title may sit on the logo band while left cards
					// start below; accept any row pair that exposes both titles.
					hasLeft, hasRight := false, false
					for _, row := range rows {
						plain := ansi.Strip(row)
						if strings.Contains(plain, "first run") {
							hasLeft = true
						}
						if strings.Contains(plain, "context") {
							hasRight = true
						}
					}
					if !hasLeft || !hasRight {
						t.Errorf("split view missing panel titles left=%v right=%v", hasLeft, hasRight)
					}
				} else {
					panelRow := rows[panelRowIdx]
					if got := ansi.StringWidth(panelRow); got != tt.width {
						t.Errorf("split outer row width = %d, want %d", got, tt.width)
					}
				}
				l := computeLayout(tt.left, tt.height, m.composer.Height(), m.completionPopupHeightFor(tt.left), false)
				bodyHeight := l.transcript + l.notice + l.popup + l.composer
				// Right panel top chrome is on the first body row (under the header).
				// Solid chrome carries the window title; bottom chrome is a surface bar.
				rightStart := tt.left + tt.gutter
				topPlain := ansi.Strip(rows[1])
				bottomPlain := ansi.Strip(rows[bodyHeight])
				titleIdx := strings.Index(topPlain, "context")
				if titleIdx < 0 {
					t.Errorf("right panel top chrome missing title on body start row: %q", topPlain)
				} else if titleCol := ansi.StringWidth(topPlain[:titleIdx]); titleCol < rightStart-1 || titleCol > rightStart+4 {
					// Soft chrome prefixes title with ╭─ (+2–3 cells); solid is near rightStart.
					t.Errorf("right panel title at col %d, want near rightStart %d: %q", titleCol, rightStart, topPlain)
				}
				if bottomCh := displayColRune(bottomPlain, rightStart); bottomCh == 0 {
					t.Errorf("right panel does not span left stack body height %d: bottom empty at col %d", bodyHeight, rightStart)
				}
			}
		})
	}
}

func TestC2FocusToggleCollapsesToActivePaneImmediately(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if m.focus != focusRight || computePaneGeometry(m.width, m.th.Spacing.XS, m.focus).rightWidth != 80 {
		t.Fatalf("right focus did not claim collapsed width: focus=%d geometry=%+v", m.focus, computePaneGeometry(m.width, m.th.Spacing.XS, m.focus))
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'h', Mod: tea.ModCtrl})
	if m.focus != focusLeft || computePaneGeometry(m.width, m.th.Spacing.XS, m.focus).leftWidth != 80 {
		t.Fatalf("left focus did not reclaim collapsed width: focus=%d geometry=%+v", m.focus, computePaneGeometry(m.width, m.th.Spacing.XS, m.focus))
	}
}

func TestC2RegistryReceivesRightPanelInnerBodyDimensionsAndRetainsStateAcrossResizeStorm(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.windows = windowRegistry{windows: []window{statefulTestWindow{windowID: "one", windowTitle: "one"}, statefulTestWindow{windowID: "two", windowTitle: "two"}}}
	m.focus = focusRight
	m.setComposerValueAt("draft survives", len("draft survives"))
	m.applyEvent(protocol.UserMessage{Text: "transcript survives"})
	m.windows = m.windows.cycle()
	m = updateApp(t, m, tea.KeyPressMsg{Text: "x"})
	for _, size := range []tea.WindowSizeMsg{{Width: 120, Height: 80}, {Width: 93, Height: 60}, {Width: 92, Height: 60}, {Width: 80, Height: 40}, {Width: 160, Height: 106}, {Width: 93, Height: 60}} {
		m = updateApp(t, m, size)
		g := computePaneGeometry(size.Width, m.th.Spacing.XS, focusRight)
		l := computeLayout(g.leftCandidateWidth(size.Width), size.Height, m.composer.Height(), m.completionPopupHeightFor(g.leftCandidateWidth(size.Width)), false)
		rightOuter := g.rightWidth
		if rightOuter == 0 {
			rightOuter = size.Width
		}
		wantWidth := ui.PanelInnerWidth(m.th, rightOuter)
		wantHeight := l.transcript + l.notice + l.popup + l.composer - 2
		for _, w := range m.windows.windows {
			got := testWindow(t, w)
			if got.width != wantWidth || got.height != wantHeight {
				t.Errorf("%dx%d window dimensions = %dx%d, want inner/body %dx%d", size.Width, size.Height, got.width, got.height, wantWidth, wantHeight)
			}
		}
	}
	if m.composer.Value() != "draft survives" || !strings.Contains(ansi.Strip(viewString(m)), "transcript survives") {
		t.Error("resize storm lost left-pane draft or transcript")
	}
	if got := testWindow(t, m.windows.active()); got.windowID != "two" || len(got.updates) != 1 || got.updates[0] != "x" {
		t.Errorf("resize storm lost active window identity/state: %+v", got)
	}
}

func TestC2CustomGutterIsPaintedAtExactWidth(t *testing.T) {
	th := theme.Default()
	th.Spacing = th.Spacing.WithXS(3)
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 95, Height: 60})
	g := computePaneGeometry(m.width, m.th.Spacing.XS, m.focus)
	if g.leftWidth != 60 || g.gutter != 3 || g.rightWidth != 32 {
		t.Fatalf("custom-gutter geometry = %+v", g)
	}
	// Slice by display columns: the welcome logo bolt is double-width, so
	// rune-index offsets misalign the gutter on logo rows.
	for _, row := range strings.Split(viewString(m), "\n")[1:3] {
		gutter := sliceDisplayCols(ansi.Strip(row), g.leftWidth, g.leftWidth+g.gutter)
		if strings.TrimSpace(gutter) != "" {
			t.Errorf("gutter cells are not blank: %q", gutter)
		}
	}
}

// sliceDisplayCols returns the substring of s covering display columns [start, end).
func sliceDisplayCols(s string, start, end int) string {
	if start < 0 {
		start = 0
	}
	if end <= start {
		return ""
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		w := ansi.StringWidth(string(r))
		if w < 1 {
			w = 1
		}
		next := col + w
		if col < end && next > start {
			b.WriteRune(r)
		}
		col = next
		if col >= end {
			break
		}
	}
	return b.String()
}

// displayColRune returns the rune whose display cells cover column col, or 0.
func displayColRune(s string, col int) rune {
	if col < 0 {
		return 0
	}
	at := 0
	for _, r := range s {
		w := ansi.StringWidth(string(r))
		if w < 1 {
			w = 1
		}
		if at <= col && col < at+w {
			return r
		}
		at += w
	}
	return 0
}

func TestC2PaneFocusAndModalUseFocusAndMutedThemeTokens(t *testing.T) {
	setTUITrueColor(t)
	th := theme.Default()
	th.SurfaceFocus = fixedColor("#010203")
	th.SurfaceMuted = fixedColor("#040506")
	th.OverlayScrim = fixedColor("#070809")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})
	// Left focus highlights the prompt box (mode title "chat") — not right panes.
	leftRows, rightRows := rowsContaining(viewString(m), "chat"), rowsContaining(viewString(m), "context")
	if !strings.Contains(strings.Join(leftRows, "\n"), rgbBGSGR("#010203")) || !strings.Contains(strings.Join(rightRows, "\n"), rgbBGSGR("#040506")) {
		t.Fatal("left focus/right dim surface tokens are not visible on their respective panes")
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	leftRows, rightRows = rowsContaining(viewString(m), "chat"), rowsContaining(viewString(m), "context")
	if !strings.Contains(strings.Join(leftRows, "\n"), rgbBGSGR("#040506")) || !strings.Contains(strings.Join(rightRows, "\n"), rgbBGSGR("#010203")) {
		t.Fatal("focus toggle did not swap pane focus/dim surface tokens")
	}
	m.modal = &appProbeModal{}
	m.reflow()
	view := viewString(m)
	if strings.Contains(view, rgbBGSGR("#010203")) || strings.Contains(view, rgbBGSGR("#040506")) || !strings.Contains(view, rgbSGR("#070809")) {
		t.Error("modal did not scrim both panes with OverlayScrim")
	}
	if rows := strings.Split(view, "\n"); len(rows) != 80 {
		t.Errorf("modal overlay line count = %d, want 80", len(rows))
	}
	for i, row := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(row); w > 120 {
			t.Errorf("modal row %d width = %d, want <= 120", i, w)
		}
	}
}

func TestC2HintsAndWelcomeKeysDeriveFromBindings(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	// Context-sensitive footer (#679): left focus is composer-oriented.
	leftHints := ansi.Strip(m.hintsView(160))
	if !strings.Contains(leftHints, keyHint(m.keyMap.Send).Label) {
		t.Errorf("left hints omit send: %q", leftHints)
	}
	if !strings.Contains(leftHints, keyHint(m.keyMap.ExternalEditor).Key) {
		t.Errorf("left hints omit external editor: %q", leftHints)
	}
	welcome := ansi.Strip(m.welcomeKeys())
	for _, binding := range []ui.KeyHint{keyHint(m.keyMap.FocusLeft), keyHint(m.keyMap.FocusRight), keyHint(m.keyMap.ExternalEditor)} {
		if !strings.Contains(welcome, binding.Key) || !strings.Contains(welcome, binding.Label) {
			t.Errorf("welcome keys omit binding-derived %q %q: %q", binding.Key, binding.Label, welcome)
		}
	}
	if strings.Contains(welcome, keyHint(m.keyMap.Send).Label) {
		t.Errorf("welcome keys repeat composer send binding: %q", welcome)
	}
	m = updateApp(t, m, tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	rightHints := ansi.Strip(m.hintsView(160))
	if strings.Contains(rightHints, keyHint(m.keyMap.Send).Label) {
		t.Errorf("right/global hints retained left-only send binding: %q", rightHints)
	}
	if !strings.Contains(rightHints, "select") {
		t.Errorf("right hints missing select: %q", rightHints)
	}
}

func TestAgentsPaneFooterOnlyWhenAgentsWindow(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	m.focus = focusRight

	// Context (default) must not advertise agents-pane open/interrupt chrome.
	ak := defaultAgentsKeyMap()
	openHint := ak.Open.Help().Key + " " + ak.Open.Help().Desc
	contextPane := ansi.Strip(m.rightPaneView(40, 20, false))
	if strings.Contains(contextPane, openHint) {
		t.Errorf("context pane leaked agents footer: %q", contextPane)
	}

	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg
	// Wide enough that Panel footer chrome can fit the full agents hint row
	// (n/enter/x/d/j/k/f including "hide from pane" + "cycle filter").
	agentsPane := ansi.Strip(m.rightPaneSingle(120, 12, false, m.windows.active()))
	for _, b := range []key.Binding{ak.Spawn, ak.Open, ak.Interrupt, ak.Rename, ak.Hide, ak.Move, ak.Filter} {
		h := b.Help()
		if !strings.Contains(agentsPane, h.Key) {
			t.Errorf("agents pane missing key %q: %q", h.Key, agentsPane)
		}
		if h.Desc != "" && !strings.Contains(agentsPane, h.Desc) {
			t.Errorf("agents pane missing desc %q: %q", h.Desc, agentsPane)
		}
	}
	// Compact/borderless drops panel chrome footer (open/interrupt not in empty body).
	compact := ansi.Strip(m.rightPaneSingle(120, 12, true, m.windows.active()))
	if strings.Contains(compact, openHint) {
		t.Errorf("compact agents pane still shows chrome footer: %q", compact)
	}
}

func TestPaneKeybindFootersSingleLineAndVisibleUnfocused(t *testing.T) {
	// #316: pane keybind chrome stays one line and remains visible when dim.
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	reg, ok := m.windows.activate(agentsWindowID)
	if !ok {
		t.Fatal("activate agents")
	}
	m.windows = reg

	ak := defaultAgentsKeyMap()
	spawnKey := ak.Spawn.Help().Key
	sendKey := keyHint(m.keyMap.Send).Key

	for _, width := range []int{80, 60, 40, 32} {
		// Agents pane dim (left focus / unfocused right chrome).
		agentsDim := m.rightPaneSingle(width, 10, false, m.windows.active(), false, true)
		agentsFocus := m.rightPaneSingle(width, 10, false, m.windows.active(), true, false)
		for name, out := range map[string]string{"dim": agentsDim, "focused": agentsFocus} {
			plain := ansi.Strip(out)
			if !strings.Contains(plain, spawnKey) {
				t.Errorf("width %d agents %s missing %q: %q", width, name, spawnKey, plain)
			}
			lines := strings.Split(out, "\n")
			if len(lines) != 10 {
				t.Errorf("width %d agents %s lines=%d, want 10 (no footer wrap)", width, name, len(lines))
			}
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w > width {
					t.Errorf("width %d agents %s line %d ww=%d: %q", width, name, i, w, ansi.Strip(ln))
				}
			}
			footer := ansi.Strip(lines[len(lines)-1])
			if strings.TrimSpace(footer) == "" {
				t.Errorf("width %d agents %s empty footer row", width, name)
			}
		}

		// Composer footer always present when unfocused (right focus dims left).
		m.focus = focusRight
		comp := m.composerView(false, width, 5)
		compPlain := ansi.Strip(comp)
		if !strings.Contains(compPlain, sendKey) {
			t.Errorf("width %d unfocused composer missing send %q: %q", width, sendKey, compPlain)
		}
		compLines := strings.Split(comp, "\n")
		if len(compLines) != 5 {
			t.Errorf("width %d composer lines=%d, want 5", width, len(compLines))
		}
		for i, ln := range compLines {
			if w := lipgloss.Width(ln); w > width {
				t.Errorf("width %d composer line %d ww=%d: %q", width, i, w, ansi.Strip(ln))
			}
		}
		m.focus = focusLeft
	}
}
