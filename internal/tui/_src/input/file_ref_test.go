package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestFindFileRefSpans(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []fileRef
	}{
		{
			name: "simple",
			in:   "see internal/tui/app.go:42 for details",
			want: []fileRef{{Path: "internal/tui/app.go", Line: 42}},
		},
		{
			name: "with column",
			in:   "app.go:10:5: error",
			want: []fileRef{{Path: "app.go", Line: 10}},
		},
		{
			name: "grep style",
			in:   "internal/tui/cells.go:99: func render",
			want: []fileRef{{Path: "internal/tui/cells.go", Line: 99}},
		},
		{
			name: "multiple",
			in:   "a.go:1 and b/c.go:2",
			want: []fileRef{{Path: "a.go", Line: 1}, {Path: "b/c.go", Line: 2}},
		},
		{
			name: "skip url port",
			in:   "http://example.com:8080/path",
			want: nil,
		},
		{
			name: "skip host port",
			in:   "connect example.com:8080 now",
			want: nil,
		},
		{
			name: "skip bare number colon",
			in:   "ratio 1:2 is fine",
			want: nil,
		},
		{
			name: "backtick wrapped",
			in:   "look at `foo/bar.go:7` please",
			want: []fileRef{{Path: "foo/bar.go", Line: 7}},
		},
		{
			name: "absolute",
			in:   "/tmp/proj/main.go:3",
			want: []fileRef{{Path: "/tmp/proj/main.go", Line: 3}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans := findFileRefSpans(tt.in)
			if len(spans) != len(tt.want) {
				t.Fatalf("spans = %#v, want %#v", spans, tt.want)
			}
			for i := range tt.want {
				if spans[i].Path != tt.want[i].Path || spans[i].Line != tt.want[i].Line {
					t.Errorf("span[%d] = %s:%d, want %s:%d", i, spans[i].Path, spans[i].Line, tt.want[i].Path, tt.want[i].Line)
				}
				token := tt.in[spans[i].Start:spans[i].End]
				if !strings.Contains(token, spans[i].Path) || !strings.Contains(token, itoa(spans[i].Line)) {
					t.Errorf("token %q does not cover path:line", token)
				}
			}
		})
	}
}

func TestFileRefAtColumn(t *testing.T) {
	line := "prefix internal/tui/app.go:42 suffix"
	col := ansi.StringWidth("prefix ")
	ref, ok := fileRefAtColumn(line, col)
	if !ok || ref.Path != "internal/tui/app.go" || ref.Line != 42 {
		t.Fatalf("got ok=%v ref=%+v", ok, ref)
	}
	if _, ok := fileRefAtColumn(line, 0); ok {
		t.Fatal("column 0 should miss")
	}
	if _, ok := fileRefAtColumn(line, ansi.StringWidth(line)-1); ok {
		t.Fatal("suffix column should miss")
	}
}

func TestPostLinkifyRenderedAddsOSC8(t *testing.T) {
	th := theme.Default()
	in := "see app.go:12 here"
	out := postLinkifyRendered(in, th, "/work")
	if !strings.Contains(out, "\x1b]8;") {
		t.Fatalf("expected OSC8 hyperlink, got %q", out)
	}
	if !strings.Contains(ansi.Strip(out), "app.go:12") {
		t.Fatalf("plain text lost: %q", ansi.Strip(out))
	}
	if !strings.Contains(out, "file://") {
		t.Fatalf("expected file:// URI, got %q", out)
	}
}

func TestLastFileRef(t *testing.T) {
	ref, ok := lastFileRef([]string{"nope", "a.go:1", "tail", "b.go:9 done"})
	if !ok || ref.Path != "b.go" || ref.Line != 9 {
		t.Fatalf("got ok=%v ref=%+v", ok, ref)
	}
	if _, ok := lastFileRef([]string{"plain"}); ok {
		t.Fatal("expected no ref")
	}
}

func TestCollectFileRefsFromTranscript(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: t.TempDir()})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.TextDelta{Text: "bug in note.go:1 and other.go:2"})
	m.refreshViewport()

	refs := m.collectFileRefs()
	if len(refs) != 2 {
		t.Fatalf("collectFileRefs = %#v", refs)
	}
	if refs[0].Path != "note.go" || refs[0].Line != 1 {
		t.Errorf("first = %+v", refs[0])
	}
	if refs[1].Path != "other.go" || refs[1].Line != 2 {
		t.Errorf("second = %+v", refs[1])
	}

	ref, ok := m.fileRefForEnter()
	if !ok || ref.Path != "other.go" || ref.Line != 2 {
		t.Fatalf("fileRefForEnter = ok=%v ref=%+v", ok, ref)
	}
}

func TestEmptyEnterOpensFileRefWhenNoToolExpand(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: t.TempDir(), VimMode: VimModeTakeover})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.TextDelta{Text: "see target.go:4"})
	m.refreshViewport()

	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir())

	// Empty enter should consume the key via open-at-line (no collapsible tools).
	handled, _ := m.handleToolCellKeys(tea.KeyMsg{Type: tea.KeyEnter})
	if !handled {
		t.Fatal("empty enter should open file ref when no tool cells")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		t.Fatalf("want missing-editor notice, got err=%v notice=%q", m.noticeErr, m.notice)
	}
}

func TestFileRefAtMouseHit(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: t.TempDir()})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.TextDelta{Text: "see hit.go:3 please"})
	m.refreshViewport()

	ox, oy, ok := m.transcriptContentOrigin()
	if !ok {
		t.Fatal("expected transcript origin")
	}
	var lineIdx, col int
	found := false
	for i, line := range m.transcriptPlainLines {
		if sp := findFileRefSpans(line); len(sp) > 0 {
			lineIdx = i
			col = ansi.StringWidth(line[:sp[0].Start])
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no ref in plain lines: %#v", m.transcriptPlainLines)
	}
	msg := tea.MouseMsg{
		X:      ox + col,
		Y:      oy + (lineIdx - m.viewport.YOffset),
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	}
	ref, ok := m.fileRefAtMouse(msg)
	if !ok || ref.Path != "hit.go" || ref.Line != 3 {
		t.Fatalf("fileRefAtMouse = ok=%v ref=%+v (origin=%d,%d lineIdx=%d col=%d yOff=%d plain=%q)",
			ok, ref, ox, oy, lineIdx, col, m.viewport.YOffset, m.transcriptPlainLines[lineIdx])
	}
}

func TestOpenFileRefUsesPathLineArgs(t *testing.T) {
	m, _ := newAppTestModelWithOptions(Options{WorkDir: t.TempDir(), VimMode: VimModeTakeover})
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", t.TempDir())
	updated, _ := m.openFileRef(fileRef{Path: "x.go", Line: 9})
	mm := updated.(Model)
	if !mm.noticeErr || !strings.Contains(mm.notice, "no editor found") {
		t.Fatalf("expected missing-editor notice, got err=%v notice=%q", mm.noticeErr, mm.notice)
	}
}
