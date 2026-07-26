package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
			m.focus = tt.focus
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: tt.height})
			view := m.View()
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
					if strings.Contains(plain, "get started") && strings.Contains(plain, "context") {
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
						if strings.Contains(plain, "get started") {
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
				// Right panel top is on the first body row (under the header).
				// Use display columns: logo bolt is double-width so rune index ≠ column.
				rightStart := tt.left + tt.gutter
				topCh := displayColRune(ansi.Strip(rows[1]), rightStart)
				bottomCh := displayColRune(ansi.Strip(rows[bodyHeight]), rightStart)
				if topCh != '╭' || bottomCh != '╰' {
					t.Errorf("right panel does not span left stack body height %d: top=%q bottom=%q", bodyHeight, string(topCh), string(bottomCh))
				}
			}
		})
	}
}

func TestC2FocusToggleCollapsesToActivePaneImmediately(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	if m.focus != focusRight || computePaneGeometry(m.width, m.th.Spacing.XS, m.focus).rightWidth != 80 {
		t.Fatalf("right focus did not claim collapsed width: focus=%d geometry=%+v", m.focus, computePaneGeometry(m.width, m.th.Spacing.XS, m.focus))
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlH})
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
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
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
	if m.composer.Value() != "draft survives" || !strings.Contains(ansi.Strip(m.View()), "transcript survives") {
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
	for _, row := range strings.Split(m.View(), "\n")[1:3] {
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
	th.BorderFocus = fixedColor("#010203")
	th.BorderMuted = fixedColor("#040506")
	th.OverlayScrim = fixedColor("#070809")
	m, _ := newAppTestModelWithOptions(Options{Theme: &th})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 80})
	leftRows, rightRows := rowsContaining(m.View(), "get started"), rowsContaining(m.View(), "context")
	if !strings.Contains(strings.Join(leftRows, "\n"), rgbSGR("#010203")) || !strings.Contains(strings.Join(rightRows, "\n"), rgbSGR("#040506")) {
		t.Fatal("left focus/right dim tokens are not visible on their respective panes")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	leftRows, rightRows = rowsContaining(m.View(), "get started"), rowsContaining(m.View(), "context")
	if !strings.Contains(strings.Join(leftRows, "\n"), rgbSGR("#040506")) || !strings.Contains(strings.Join(rightRows, "\n"), rgbSGR("#010203")) {
		t.Fatal("focus toggle did not swap pane focus/dim tokens")
	}
	m.modal = &appProbeModal{}
	m.reflow()
	view := m.View()
	if strings.Contains(view, rgbSGR("#010203")) || strings.Contains(view, rgbSGR("#040506")) || !strings.Contains(view, rgbSGR("#070809")) {
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
	leftHints := ansi.Strip(m.hintsView(80))
	for _, binding := range []ui.KeyHint{keyHint(m.keyMap.FocusLeft), keyHint(m.keyMap.FocusRight), keyHint(m.keyMap.CycleWindowNext), keyHint(m.keyMap.CycleWindowPrev)} {
		if !strings.Contains(leftHints, binding.Key) {
			t.Errorf("left hints omit binding-derived key %q: %q", binding.Key, leftHints)
		}
	}
	if wideLeftHints := ansi.Strip(m.hintsView(160)); !strings.Contains(wideLeftHints, keyHint(m.keyMap.Send).Label) {
		t.Errorf("wide left hints omit left-only send binding: %q", wideLeftHints)
	}
	welcome := ansi.Strip(m.welcomeKeys())
	for _, binding := range []ui.KeyHint{keyHint(m.keyMap.FocusLeft), keyHint(m.keyMap.FocusRight), keyHint(m.keyMap.Send)} {
		if !strings.Contains(welcome, binding.Key) || !strings.Contains(welcome, binding.Label) {
			t.Errorf("welcome keys omit binding-derived %q %q: %q", binding.Key, binding.Label, welcome)
		}
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyCtrlL})
	rightHints := ansi.Strip(m.hintsView(160))
	if strings.Contains(rightHints, keyHint(m.keyMap.Send).Label) {
		t.Errorf("right/global hints retained left-only send binding: %q", rightHints)
	}
}
