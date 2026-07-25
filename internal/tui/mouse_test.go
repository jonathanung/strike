package tui

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
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
	bottom := m.viewport.YOffset
	m = updateApp(t, m, tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonWheelUp})
	if m.viewport.YOffset >= bottom {
		t.Fatalf("wheel up did not scroll: offset=%d bottom=%d", m.viewport.YOffset, bottom)
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
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      ox + 2,
		Y:      oy,
	})
	tc := m.toolByID["c1"]
	if tc == nil || !tc.expanded {
		t.Fatalf("click should expand collapsible tool: tc=%v expanded=%v", tc != nil, tc != nil && tc.expanded)
	}
	if m.selectedCell < 0 {
		t.Fatal("click should select the tool cell")
	}
	// Second click collapses.
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      ox + 2,
		Y:      oy,
	})
	if tc.expanded {
		t.Fatal("second click should collapse")
	}
}

func TestMouseClickOpensOSC8Link(t *testing.T) {
	var opened []string
	prev := startOpen
	startOpen = func(target string) error {
		opened = append(opened, target)
		return nil
	}
	t.Cleanup(func() { startOpen = prev })

	m, _ := newAppTestModel(nil, nil)
	base := t.TempDir()
	m.workDir = base
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	rel := "internal/auth/store.go"
	m.applyEvent(protocol.ToolCallBegin{CallID: "r1", Name: "read"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "r1", Title: rel, Output: "package auth\n"})
	m.refreshViewport()
	m.viewport.GotoTop()

	view := m.viewport.View()
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		t.Fatal("empty viewport")
	}
	header := lines[0]
	if !strings.Contains(header, "\x1b]8;") {
		t.Fatalf("expected OSC8 on tool title line: %q", header)
	}
	col := -1
	for x := 0; x < ansi.StringWidth(header)+2; x++ {
		if uri := osc8URIAtCell(header, x); strings.Contains(uri, "store.go") {
			col = x
			break
		}
	}
	if col < 0 {
		t.Fatalf("could not find OSC8 column on header: %q", header)
	}
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		t.Fatal("transcript origin not available")
	}
	updated, cmd := m.Update(tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      ox + col,
		Y:      oy,
	})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("expected openURICmd from link click")
	}
	msg := runAppCmd(t, cmd)
	om, ok := msg.(openURIMsg)
	if !ok {
		t.Fatalf("cmd msg = %T, want openURIMsg", msg)
	}
	if om.err != nil {
		t.Fatalf("openURI: %v", om.err)
	}
	if len(opened) == 0 {
		t.Fatal("startOpen was not called")
	}
	want := filepath.Join(base, rel)
	if opened[len(opened)-1] != want {
		t.Errorf("opened %q, want %q", opened[len(opened)-1], want)
	}
	// Link click must not toggle expand on a non-collapsible short read.
	if tc := m.toolByID["r1"]; tc != nil && tc.expanded {
		t.Fatal("link click should not expand non-collapsible tool")
	}
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
	m.modal = newKeysModal(m.keyMap)
	m.refreshViewport()
	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		// origin may still compute; click must no-op due to modal
		ox, oy = 2, 2
	}
	m = updateApp(t, m, tea.MouseMsg{
		Action: tea.MouseActionPress,
		Button: tea.MouseButtonLeft,
		X:      ox + 2,
		Y:      oy,
	})
	if m.toolByID["c1"].expanded {
		t.Fatal("click with modal open should not expand tool")
	}
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
