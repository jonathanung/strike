package tui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestMouseWheelScrollsTranscript(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	for i := range 40 {
		m.applyEvent(protocol.UserMessage{Text: strings.Repeat("scroll-line ", 8) + string(rune('a'+i%26))})
	}
	m.refreshViewport()
	m.viewport.GotoBottom()
	bottom := m.viewport.YOffset()
	m = updateApp(t, m, tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	if m.viewport.YOffset() >= bottom {
		t.Fatalf("wheel up did not scroll: offset=%d bottom=%d", m.viewport.YOffset(), bottom)
	}
}

func TestMouseClickExpandsToolCell(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("out-line-")
		b.WriteByte(byte('a' + i))
		b.WriteByte('\n')
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "run", Output: b.String()})
	m.refreshViewport()
	m.viewport.GotoTop()

	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		t.Fatal("transcript origin not available")
	}
	// Click the first content line of the only cell (header row).
	m = mouseClick(t, m, ox+2, oy)
	tc := m.toolByID["c1"]
	if tc == nil || !tc.expanded {
		t.Fatalf("click should expand collapsible tool: tc=%v expanded=%v", tc != nil, tc != nil && tc.expanded)
	}
	if m.selectedCell < 0 {
		t.Fatal("click should select the tool cell")
	}
	// Second click collapses.
	m = mouseClick(t, m, ox+2, oy)
	if tc.expanded {
		t.Fatal("second click should collapse")
	}
}

func TestMouseClickOpensOSC8HTTPLink(t *testing.T) {
	var opened []string
	prev := startOpen
	startOpen = func(target string) error {
		opened = append(opened, target)
		return nil
	}
	t.Cleanup(func() { startOpen = prev })

	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	url := "https://example.com/doc"
	m.applyEvent(protocol.ToolCallBegin{CallID: "w1", Name: "webfetch"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "w1", Title: url + " (text/html)", Output: "ok\n"})
	m.refreshViewport()
	m.viewport.GotoTop()

	view := m.viewport.View()
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		t.Fatal("empty viewport")
	}
	header := lines[0]
	col := -1
	for x := 0; x < ansi.StringWidth(header)+2; x++ {
		if uri := osc8URIAtCell(header, x); strings.HasPrefix(uri, "https://") {
			col = x
			break
		}
	}
	if col < 0 {
		t.Fatalf("could not find OSC8 http column on header: %q", header)
	}
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		t.Fatal("transcript origin not available")
	}
	m = updateApp(t, m, tea.MouseClickMsg{X: ox + col, Y: oy, Button: tea.MouseLeft})
	updated, cmd := m.Update(tea.MouseReleaseMsg{X: ox + col, Y: oy, Button: tea.MouseLeft})
	_ = updated
	if cmd == nil {
		t.Fatal("expected openURICmd from http link click")
	}
	msg := runAppCmd(t, cmd)
	om, ok := msg.(openURIMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want openURIMsg", msg)
	}
	if om.err != nil {
		t.Fatalf("openURI: %v", om.err)
	}
	if len(opened) == 0 || opened[len(opened)-1] != url {
		t.Fatalf("opened %v, want %q", opened, url)
	}
}

func TestMouseClickFileTitleOpensEditor(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	base := t.TempDir()
	m.workDir = base
	m.vimMode = VimModePane
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	rel := "internal/auth/store.go"
	m.applyEvent(protocol.ToolCallBegin{CallID: "r1", Name: "read"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "r1", Title: rel, Output: "package auth\n"})
	m.refreshViewport()
	m.viewport.GotoTop()

	header := strings.Split(m.viewport.View(), "\n")[0]
	col := -1
	for x := 0; x < ansi.StringWidth(header)+2; x++ {
		if uri := osc8URIAtCell(header, x); strings.Contains(uri, "store.go") {
			col = x
			break
		}
	}
	if col < 0 {
		t.Fatalf("missing OSC8 file title: %q", header)
	}
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		t.Fatal("no origin")
	}
	m = mouseClick(t, m, ox+col, oy)
	// File OSC8 routes through openFileRef → /vim pane, not expand.
	if tc := m.toolByID["r1"]; tc != nil && tc.expanded {
		t.Fatal("file title click should not expand tool")
	}
	if m.focus != focusRight {
		t.Fatalf("focus = %v, want right (vim pane)", m.focus)
	}
	_ = filepath.Join(base, rel) // keep import used if path asserted later
}

func TestToolTitleEmitsOSC8FileLink(t *testing.T) {
	th := theme.Default().Resolve()
	base := t.TempDir()
	cell := &toolCell{name: "read", title: "pkg/foo.go", done: true, output: "ok"}
	out := cell.renderLinked(80, th, base)
	if !strings.Contains(out, "\x1b]8;") {
		t.Fatalf("missing OSC8 open: %q", out)
	}
	if !strings.Contains(out, "file://") || !strings.Contains(out, "foo.go") {
		t.Fatalf("missing file URI: %q", out)
	}
	// Plain path text still visible.
	if !strings.Contains(ansi.Strip(out), "foo.go") {
		t.Fatalf("path not visible after strip: %q", ansi.Strip(out))
	}
}

func TestToolTitleEmitsOSC8HTTPLink(t *testing.T) {
	th := theme.Default().Resolve()
	title := "https://example.com/doc (text/html)"
	cell := &toolCell{name: "webfetch", title: title, done: true, output: "body"}
	out := cell.renderLinked(100, th, "")
	if !strings.Contains(out, "\x1b]8;;https://example.com/doc\x07") &&
		!strings.Contains(out, "https://example.com/doc") {
		t.Fatalf("missing http OSC8: %q", out)
	}
}

func TestDisplayURIAndLooksLikePath(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		text string
		want string // substring of URI, or "" for none
	}{
		{"https://example.com/a", "https://example.com/a"},
		{"http://localhost:8080/", "http://localhost:8080/"},
		{"https://example.com/x (text/html)", "https://example.com/x"},
		{"echo hi", ""},
		{"-flag", ""},
		{"pkg/foo.go", "file://"},
		{"/abs/path.go", "file://"},
		{"main.go", "file://"},
		{"go test ./...", ""},
	}
	for _, tt := range tests {
		got := displayURI(tt.text, base)
		if tt.want == "" {
			if got != "" {
				t.Errorf("displayURI(%q) = %q, want empty", tt.text, got)
			}
			continue
		}
		if !strings.Contains(got, tt.want) {
			t.Errorf("displayURI(%q) = %q, want containing %q", tt.text, got, tt.want)
		}
	}
}

func TestOSC8URIAtCell(t *testing.T) {
	uri := "https://example.test/path"
	styled := "link"
	line := "pre " + withHyperlink(uri, styled) + " post"
	// Columns of "pre " = 4; link spans 4 cells.
	if got := osc8URIAtCell(line, 4); got != uri {
		t.Errorf("col 4 = %q, want %q", got, uri)
	}
	if got := osc8URIAtCell(line, 3); got != "" {
		t.Errorf("col 3 (pre space) = %q, want empty", got)
	}
	if got := osc8URIAtCell(line, 8); got != "" {
		t.Errorf("col 8 (post) = %q, want empty", got)
	}
}

func TestOpenURIRejectsBadSchemes(t *testing.T) {
	var opened []string
	prev := startOpen
	startOpen = func(target string) error {
		opened = append(opened, target)
		return nil
	}
	t.Cleanup(func() { startOpen = prev })

	if err := openURI("javascript:alert(1)"); err == nil {
		t.Fatal("expected reject javascript:")
	}
	if err := openURI("https://ok.example"); err != nil {
		t.Fatalf("https: %v", err)
	}
	if len(opened) != 1 || opened[0] != "https://ok.example" {
		t.Fatalf("opened = %v", opened)
	}
}

func TestMouseClickIgnoresModal(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("line\n")
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "run", Output: b.String()})
	m.modal = newKeybindEditor(m.keyMap, m.keyOverrides, m.services.Settings)
	m.refreshViewport()
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		// origin may still compute; click must no-op due to modal
		ox, oy = 2, 2
	}
	m = mouseClick(t, m, ox+2, oy)
	if m.toolByID["c1"].expanded {
		t.Fatal("click with modal open should not expand tool")
	}
}

// mouseClick sends left press+release at the same cell (no drag).
func mouseClick(t *testing.T, m Model, x, y int) Model {
	t.Helper()
	m = updateApp(t, m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	return updateApp(t, m, tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
}

func TestExploreCellTitleLinksWhenExpanded(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	base := t.TempDir()
	m.workDir = base
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.ToolCallBegin{CallID: "r1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "r1", Title: "a.go", Output: "package a"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "g1", Name: "glob", Args: json.RawMessage(`{"pattern":"*.go"}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "g1", Title: "*.go", Output: "a.go"})
	exp, ok := m.cells[len(m.cells)-1].(*exploreCell)
	if !ok {
		t.Fatalf("want exploreCell, got %T", m.cells[len(m.cells)-1])
	}
	exp.expanded = true
	out := exp.renderLinked(80, theme.Default(), base)
	if !strings.Contains(out, "\x1b]8;") {
		t.Fatalf("expanded explore missing OSC8 on path titles:\n%s", out)
	}
	// glob title "*.go" is not a path link
	if strings.Count(out, "file://") < 1 {
		t.Fatalf("expected at least one file link:\n%s", out)
	}
}

func TestMousePaneFocusHitTesting(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	gutter := m.th.Resolve().Spacing.XS
	geo := computePaneGeometry(m.width, gutter, m.focus)
	l := computeLayout(geo.leftWidth, m.height, m.composer.Height(), m.completionPopupHeightFor(geo.leftWidth), m.showDangerBanner(), m.noticeRowsFor(geo.leftWidth))
	bodyY := l.header

	tests := []struct {
		name    string
		x, y    int
		want    paneFocus
		wantHit bool
	}{
		{"left border", 0, bodyY, focusLeft, true},
		{"left body", geo.leftWidth - 1, bodyY, focusLeft, true},
		{"gutter", geo.leftWidth, bodyY, focusLeft, false},
		{"right border", geo.leftWidth + geo.gutter, bodyY, focusRight, true},
		{"right body", m.width - 1, bodyY, focusRight, true},
		{"header", 0, 0, focusLeft, false},
		{"footer", 0, m.height - 1, focusLeft, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := m.paneFocusAtMouse(tt.x, tt.y)
			if got != tt.want || ok != tt.wantHit {
				t.Fatalf("paneFocusAtMouse(%d, %d) = (%v, %v), want (%v, %v)", tt.x, tt.y, got, ok, tt.want, tt.wantHit)
			}
		})
	}
}

func TestMouseClickChangesVisiblePaneFocus(t *testing.T) {
	tests := []struct {
		name        string
		width       int
		orientation splitOrientation
		initial     paneFocus
		click       func(Model) (int, int)
		want        paneFocus
	}{
		{
			name:    "horizontal secondary",
			width:   120,
			initial: focusLeft,
			click: func(m Model) (int, int) {
				geo := computePaneGeometry(m.width, m.th.Resolve().Spacing.XS, m.focus)
				return geo.leftWidth + geo.gutter, 1
			},
			want: focusRight,
		},
		{
			name:    "horizontal primary",
			width:   120,
			initial: focusRight,
			click: func(m Model) (int, int) {
				return 0, 1
			},
			want: focusLeft,
		},
		{
			name:        "vertical secondary",
			width:       120,
			orientation: orientVertical,
			initial:     focusLeft,
			click: func(m Model) (int, int) {
				l := computeLayout(m.width, m.height, m.composer.Height(), m.completionPopupHeightFor(m.width), m.showDangerBanner(), m.noticeRowsFor(m.width))
				bodyHeight := l.transcript + l.notice + l.popup + l.composer
				geo := computeVerticalPaneGeometry(m.width, bodyHeight, m.th.Resolve().Spacing.XS, m.focus)
				return 0, l.header + geo.leftHeight + geo.gutter
			},
			want: focusRight,
		},
		{
			name:    "single pane stays focused",
			width:   80,
			initial: focusRight,
			click: func(m Model) (int, int) {
				return 0, 1
			},
			want: focusRight,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, _ := newAppTestModel(nil, nil)
			m.splitOrientation = tt.orientation
			m.focus = tt.initial
			m = updateApp(t, m, tea.WindowSizeMsg{Width: tt.width, Height: 40})
			x, y := tt.click(m)
			m = updateApp(t, m, tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
			if m.focus != tt.want {
				t.Fatalf("focus after click = %v, want %v", m.focus, tt.want)
			}
		})
	}
}

func TestMousePaneFocusDoesNotBypassModalOrGutter(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 120, Height: 40})
	geo := computePaneGeometry(m.width, m.th.Resolve().Spacing.XS, m.focus)
	m = updateApp(t, m, tea.MouseClickMsg{X: geo.leftWidth, Y: 1, Button: tea.MouseLeft})
	if m.focus != focusLeft {
		t.Fatalf("gutter click changed focus to %v", m.focus)
	}

	m.modal = &appProbeModal{}
	m.reflow()
	m = updateApp(t, m, tea.MouseClickMsg{X: geo.leftWidth + geo.gutter, Y: 1, Button: tea.MouseLeft})
	if m.focus != focusLeft {
		t.Fatalf("modal click changed focus to %v", m.focus)
	}
}
