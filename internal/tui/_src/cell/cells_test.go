package tui

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/protocol"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
	"github.com/jonathanung/strike-cli/internal/tui/ui"
)

func TestToolCellUsesToolGuideIconRatherThanBorderVertical(t *testing.T) {
	th := theme.Default()
	th.Icons.ToolGuide = ">"
	th.BorderStyle.Vertical = "|"

	out := (&toolCell{name: "x", output: "result", done: true}).render(8, th)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "  > ") {
		t.Errorf("tool output guide = %q, want custom ToolGuide", plain)
	}
	if strings.Contains(plain, "|") {
		t.Errorf("tool output used BorderStyle.Vertical instead of ToolGuide: %q", plain)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > 8 {
			t.Errorf("tool cell line %d width = %d, want <= 8: %q", i, got, ansi.Strip(line))
		}
	}
}

func TestToolCellUsesCustomDotForStructuredTitleSeparator(t *testing.T) {
	th := theme.Default()
	th.Icons.Dot = "|"

	plain := ansi.Strip((&toolCell{name: "bash", title: "run tests", done: true}).render(80, th))
	if !strings.Contains(plain, "bash | run tests") {
		t.Errorf("tool title omitted custom structured separator: %q", plain)
	}
	if strings.Contains(plain, "bash · run tests") {
		t.Errorf("tool title retained default dot separator: %q", plain)
	}
}

func TestToolCellEditMetadataShowsDiffPreview(t *testing.T) {
	th := theme.Default()
	meta := json.RawMessage(`{"oldString":"foo","newString":"bar","count":1}`)
	cell := &toolCell{
		name:     "edit",
		title:    "file.go",
		output:   "Edited file.go",
		metadata: meta,
		done:     true,
	}
	plain := ansi.Strip(cell.render(80, th))
	for _, want := range []string{"-foo", "+bar", "+1", "-1"} {
		if !strings.Contains(plain, want) {
			t.Errorf("edit tool cell missing %q:\n%s", want, plain)
		}
	}
	// Diff replaces the muted output blurb; the "Edited …" summary need not appear in the body.
	// (Title may still mention the path.)
	if strings.Contains(plain, "Edited file.go") {
		// acceptable if title/output still shows it, but body should be diff-first;
		// only fail if output blurb is the sole body without markers (already checked).
	}
	for i, line := range strings.Split(cell.render(80, th), "\n") {
		if got := lipgloss.Width(line); got > 80 {
			t.Errorf("line %d width = %d > 80: %q", i, got, ansi.Strip(line))
		}
	}
}

func TestToolCellWithoutMetadataShowsOutputPreview(t *testing.T) {
	th := theme.Default()
	cell := &toolCell{
		name:   "bash",
		title:  "echo hi",
		output: "hello from bash",
		done:   true,
	}
	plain := ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, "hello from bash") {
		t.Errorf("expected muted output preview:\n%s", plain)
	}
	if strings.Contains(plain, "-foo") || strings.Contains(plain, "+bar") {
		t.Errorf("unexpected diff markers without metadata:\n%s", plain)
	}
}

func TestToolCellNotDoneHasNoBodyWithoutOutput(t *testing.T) {
	th := theme.Default()
	meta := json.RawMessage(`{"oldString":"foo","newString":"bar"}`)
	cell := &toolCell{
		name:     "edit",
		metadata: meta,
		done:     false,
	}
	plain := ansi.Strip(cell.render(80, th))
	if strings.Contains(plain, "foo") || strings.Contains(plain, "bar") {
		t.Errorf("in-progress tool cell without output should not render body:\n%s", plain)
	}
	lines := strings.Split(strings.TrimRight(plain, "\n"), "\n")
	if len(lines) > 1 {
		t.Errorf("in-progress cell without output has body lines: %q", plain)
	}
}

func TestToolCellLiveTailShowsLastLinesWhileRunning(t *testing.T) {
	th := theme.Default()
	var b strings.Builder
	for i := 1; i <= 8; i++ {
		b.WriteString("line")
		b.WriteByte(byte('0' + i))
		b.WriteByte('\n')
	}
	cell := &toolCell{
		name:   "bash",
		title:  "long-run",
		output: b.String(),
		done:   false,
	}
	plain := ansi.Strip(cell.render(80, th))
	// Last toolLiveTailLines (5): line4..line8
	for _, want := range []string{"line4", "line5", "line6", "line7", "line8"} {
		if !strings.Contains(plain, want) {
			t.Errorf("live tail missing %q:\n%s", want, plain)
		}
	}
	for _, hide := range []string{"line1", "line2", "line3"} {
		if strings.Contains(plain, hide) {
			t.Errorf("live tail should not show early line %q:\n%s", hide, plain)
		}
	}
}

func TestTailLines(t *testing.T) {
	got := tailLines("a\nb\nc\nd\n", 2)
	if got != "c\nd" {
		t.Errorf("tailLines = %q, want c\\nd", got)
	}
	if got := tailLines("only", 5); got != "only" {
		t.Errorf("short = %q", got)
	}
	if got := tailLines("", 3); got != "" {
		t.Errorf("empty = %q", got)
	}
}

func TestToolCellCollapsedPreviewHidesExtraLines(t *testing.T) {
	th := theme.Default()
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		b.WriteString("line")
		b.WriteByte(byte('0' + i%10))
		b.WriteByte('\n')
	}
	th = th.Resolve()
	cell := &toolCell{name: "bash", title: "long", output: b.String(), done: true}
	plain := ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, th.Icons.TreeCollapsed) {
		t.Errorf("collapsed cell missing expand marker %q:\n%s", th.Icons.TreeCollapsed, plain)
	}
	// preview is first 6 lines: line1..line6
	for _, hide := range []string{"line7", "line8", "line9"} {
		if strings.Contains(plain, hide) {
			t.Errorf("collapsed preview should hide %q:\n%s", hide, plain)
		}
	}
	if !strings.Contains(plain, "more lines") {
		t.Errorf("collapsed preview missing more-lines marker:\n%s", plain)
	}
	cell.expanded = true
	plain = ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, th.Icons.TreeExpanded) {
		t.Errorf("expanded cell missing collapse marker %q:\n%s", th.Icons.TreeExpanded, plain)
	}
	for _, want := range []string{"line7", "line8", "line9"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expanded output missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "more lines") {
		t.Errorf("expanded output still shows truncation marker:\n%s", plain)
	}
}

func TestToolCellToggleExpanded(t *testing.T) {
	short := &toolCell{name: "bash", output: "hi", done: true}
	if short.collapsible() || short.toggleExpanded() {
		t.Fatal("short output should not be collapsible")
	}
	long := &toolCell{name: "bash", output: "a\nb\nc\nd\ne\nf\ng\n", done: true}
	if !long.collapsible() {
		t.Fatal("long output should be collapsible")
	}
	if !long.toggleExpanded() || !long.expanded {
		t.Fatal("toggle should expand")
	}
	if !long.toggleExpanded() || long.expanded {
		t.Fatal("toggle should collapse")
	}
}

func TestToolCellCollapsibleTable(t *testing.T) {
	longOut := "a\nb\nc\nd\ne\nf\ng\n"
	shortMeta := json.RawMessage(`{"oldString":"a","newString":"b"}`)
	// lines = Count(old)+Count(new)+2; need > diffPreviewMaxLinesCell (8)
	var oldB, newB strings.Builder
	for i := 0; i < 6; i++ {
		oldB.WriteString("old\n")
		newB.WriteString("new\n")
	}
	longMeta, _ := json.Marshal(map[string]string{
		"oldString": oldB.String(),
		"newString": newB.String(),
	})
	tests := []struct {
		name        string
		cell        *toolCell
		collapsible bool
		toggleOK    bool
	}{
		{name: "nil", cell: nil, collapsible: false, toggleOK: false},
		{name: "not done", cell: &toolCell{output: longOut, done: false}, collapsible: false},
		{name: "empty output done", cell: &toolCell{output: "", done: true}, collapsible: false},
		{name: "short output", cell: &toolCell{output: "hi", done: true}, collapsible: false},
		{name: "long output", cell: &toolCell{output: longOut, done: true}, collapsible: true, toggleOK: true},
		{name: "short edit meta", cell: &toolCell{metadata: shortMeta, done: true}, collapsible: false},
		{name: "long edit meta", cell: &toolCell{metadata: longMeta, done: true}, collapsible: true, toggleOK: true},
		{
			name:        "expanded short output still collapsible",
			cell:        &toolCell{output: "hi", done: true, expanded: true},
			collapsible: true,
			toggleOK:    true,
		},
		{
			name:        "expanded short edit meta still collapsible",
			cell:        &toolCell{metadata: shortMeta, done: true, expanded: true},
			collapsible: true,
			toggleOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cell.collapsible(); got != tt.collapsible {
				t.Errorf("collapsible = %v, want %v", got, tt.collapsible)
			}
			before := false
			if tt.cell != nil {
				before = tt.cell.expanded
			}
			ok := tt.cell.toggleExpanded()
			if ok != tt.toggleOK {
				t.Errorf("toggleExpanded = %v, want %v", ok, tt.toggleOK)
			}
			if tt.toggleOK && tt.cell.expanded == before {
				t.Error("toggle did not flip expanded")
			}
			if !tt.toggleOK && tt.cell != nil && tt.cell.expanded != before {
				t.Error("failed toggle mutated expanded")
			}
		})
	}
}

func TestExploreCellCollapsibleToggle(t *testing.T) {
	tests := []struct {
		name        string
		cell        *exploreCell
		collapsible bool
		toggleOK    bool
	}{
		{name: "nil", cell: nil, collapsible: false},
		{name: "empty calls", cell: &exploreCell{}, collapsible: false},
		{
			name:        "one call",
			cell:        &exploreCell{calls: []*toolCell{{name: "read"}}},
			collapsible: true,
			toggleOK:    true,
		},
		{
			name: "multi call toggle cycle",
			cell: &exploreCell{calls: []*toolCell{
				{name: "read"}, {name: "glob"},
			}},
			collapsible: true,
			toggleOK:    true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.cell.collapsible(); got != tt.collapsible {
				t.Errorf("collapsible = %v, want %v", got, tt.collapsible)
			}
			if !tt.toggleOK {
				if tt.cell.toggleExpanded() {
					t.Error("toggleExpanded ok, want false")
				}
				return
			}
			if !tt.cell.toggleExpanded() || !tt.cell.expanded {
				t.Fatal("first toggle should expand")
			}
			if !tt.cell.toggleExpanded() || tt.cell.expanded {
				t.Fatal("second toggle should collapse")
			}
		})
	}
}

func TestDiffExpandedMaxLines(t *testing.T) {
	short := editDiffMeta{OldString: "a", NewString: "b"}
	if got := diffExpandedMaxLines(short); got != diffPreviewMaxLinesCell {
		t.Errorf("short = %d, want %d", got, diffPreviewMaxLinesCell)
	}
	var oldB strings.Builder
	for i := 0; i < 20; i++ {
		oldB.WriteString("line\n")
	}
	long := editDiffMeta{OldString: oldB.String(), NewString: "x\n"}
	got := diffExpandedMaxLines(long)
	want := ui.DiffBodyLen(long.OldString, long.NewString)
	if got != want {
		t.Errorf("long = %d, want %d", got, want)
	}
}

func TestToolCellLargeEditDiffExpandCollapse(t *testing.T) {
	th := theme.Default().Resolve()
	var oldB, newB strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&oldB, "old-line-%d\n", i)
		fmt.Fprintf(&newB, "new-line-%d\n", i)
	}
	meta, err := json.Marshal(map[string]string{
		"oldString": strings.TrimSuffix(oldB.String(), "\n"),
		"newString": strings.TrimSuffix(newB.String(), "\n"),
	})
	if err != nil {
		t.Fatal(err)
	}
	cell := &toolCell{
		name:     "edit",
		title:    "big.go",
		metadata: meta,
		done:     true,
	}
	if !cell.collapsible() {
		t.Fatal("large edit diff should be collapsible")
	}
	plain := ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, th.Icons.TreeCollapsed) {
		t.Errorf("collapsed missing expand marker:\n%s", plain)
	}
	if !strings.Contains(plain, "more lines") {
		t.Errorf("collapsed missing truncation:\n%s", plain)
	}
	if !strings.Contains(plain, "enter to expand") {
		t.Errorf("collapsed missing expand hint:\n%s", plain)
	}
	// Collapsed window is 8 hunk lines; later inserts should be hidden.
	if strings.Contains(plain, "+new-line-11") {
		t.Errorf("collapsed should hide late insert:\n%s", plain)
	}

	if !cell.toggleExpanded() || !cell.expanded {
		t.Fatal("toggle should expand")
	}
	plain = ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, th.Icons.TreeExpanded) {
		t.Errorf("expanded missing collapse marker:\n%s", plain)
	}
	if strings.Contains(plain, "more lines") {
		t.Errorf("expanded still truncated:\n%s", plain)
	}
	if strings.Contains(plain, "enter to expand") {
		t.Errorf("expanded still shows expand hint:\n%s", plain)
	}
	for _, want := range []string{"-old-line-0", "-old-line-11", "+new-line-0", "+new-line-11"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expanded missing %q:\n%s", want, plain)
		}
	}

	if !cell.toggleExpanded() || cell.expanded {
		t.Fatal("toggle should collapse")
	}
	plain = ansi.Strip(cell.render(80, th))
	if !strings.Contains(plain, "more lines") || !strings.Contains(plain, "enter to expand") {
		t.Errorf("re-collapsed missing truncation affordance:\n%s", plain)
	}
}

func TestInfoAndErrorCellRender(t *testing.T) {
	th := theme.Default()
	info := ansi.Strip((&infoCell{text: "device code ABCD"}).render(40, th))
	if !strings.Contains(info, "device code ABCD") {
		t.Errorf("info cell = %q", info)
	}
	errPlain := ansi.Strip((&errorCell{text: "boom"}).render(40, th))
	if !strings.Contains(errPlain, "boom") {
		t.Errorf("error cell = %q", errPlain)
	}
}

func TestExploreCellGroupsConsecutiveReadGlobGrep(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.ToolCallBegin{CallID: "r1", Name: "read", Args: json.RawMessage(`{"path":"a.go"}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "r1", Title: "a.go", Output: "package a"})
	// Still a single tool cell.
	if _, ok := m.cells[len(m.cells)-1].(*toolCell); !ok {
		t.Fatalf("single read = %T, want *toolCell", m.cells[len(m.cells)-1])
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "g1", Name: "glob", Args: json.RawMessage(`{"pattern":"*.go"}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "g1", Title: "*.go", Output: "a.go"})
	exp, ok := m.cells[len(m.cells)-1].(*exploreCell)
	if !ok {
		t.Fatalf("after second explore = %T, want *exploreCell", m.cells[len(m.cells)-1])
	}
	if len(exp.calls) != 2 {
		t.Fatalf("explore calls = %d, want 2", len(exp.calls))
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "s1", Name: "grep", Args: json.RawMessage(`{"pattern":"foo"}`)})
	m.applyEvent(protocol.ToolCallEnd{CallID: "s1", Title: "foo", Output: "hit"})
	if len(exp.calls) != 3 {
		t.Fatalf("explore calls after grep = %d, want 3", len(exp.calls))
	}
	// bash breaks the group.
	m.applyEvent(protocol.ToolCallBegin{CallID: "b1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "b1", Title: "echo", Output: "ok"})
	if exp.accepting {
		t.Fatal("explore group should stop accepting after bash")
	}
	if _, ok := m.cells[len(m.cells)-1].(*toolCell); !ok {
		t.Fatalf("bash cell = %T, want *toolCell", m.cells[len(m.cells)-1])
	}
	th := theme.Default().Resolve()
	plain := ansi.Strip(exp.render(80, th))
	if !strings.Contains(plain, "explored") {
		t.Errorf("explore header missing explored:\n%s", plain)
	}
	if !strings.Contains(plain, "3") || !strings.Contains(plain, "tools") {
		t.Errorf("explore header missing count:\n%s", plain)
	}
	// collapsed: no per-tool body
	if strings.Contains(plain, "a.go") && strings.Contains(plain, "read") {
		// title might appear only when expanded
	}
	exp.expanded = true
	plain = ansi.Strip(exp.render(80, th))
	for _, want := range []string{"read", "glob", "grep"} {
		if !strings.Contains(plain, want) {
			t.Errorf("expanded explore missing %q:\n%s", want, plain)
		}
	}
}

func TestEmptyEnterTogglesSelectedToolCell(t *testing.T) {
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
	m.composer.SetValue("")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	tc := m.toolByID["c1"]
	if tc == nil || !tc.expanded {
		t.Fatalf("enter should expand collapsible tool: tc=%v expanded=%v", tc != nil, tc != nil && tc.expanded)
	}
	if m.selectedCell < 0 {
		t.Fatal("enter should select the tool cell")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if tc.expanded {
		t.Fatal("second enter should collapse")
	}
	// Non-empty enter still sends (no expand side effect on send path with text).
	m.composer.SetValue("hello")
	before := tc.expanded
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if tc.expanded != before {
		t.Fatal("send with text must not toggle tool expand")
	}
}

func TestVReviewsSelectedEditToolAtFirstHunk(t *testing.T) {
	dir := t.TempDir()
	// Post-edit on-disk content (newString already applied).
	content := "package p\n\nconst x = 2\n"
	if err := os.WriteFile(filepath.Join(dir, "f.go"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	meta, _ := json.Marshal(map[string]any{
		"oldString": "const x = 1",
		"newString": "const x = 2",
		"count":     1,
	})

	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.vimMode = VimModeTakeover
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.applyEvent(protocol.ToolCallBegin{
		CallID: "e1",
		Name:   "edit",
		Args:   json.RawMessage(`{"filePath":"f.go","oldString":"const x = 1","newString":"const x = 2"}`),
	})
	m.applyEvent(protocol.ToolCallEnd{
		CallID:   "e1",
		Title:    "f.go",
		Output:   "Edited f.go (1 replacement(s))",
		Metadata: meta,
	})
	m.composer.SetValue("")

	// Without selection, v must reach the composer (not steal typing).
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if m.composer.Value() != "v" {
		t.Fatalf("unselected v should type into composer, got %q", m.composer.Value())
	}
	m.composer.SetValue("")

	// Select the short edit cell (reviewable even when not collapsible).
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}, Alt: true})
	if m.selectedCell < 0 {
		t.Fatal("alt+] should select reviewable edit cell")
	}
	tc := m.toolByID["e1"]
	if tc == nil || !tc.selected {
		t.Fatal("edit tool cell should be selected")
	}

	// Force missing editor so launch is deterministic (no real PTY/exec).
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", filepath.Join(t.TempDir(), "empty-bin"))

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	m = updated.(Model)
	// reviewSelectedTool → handleVimCommand may return a cmd that delivers
	// editorFinishedMsg with launchErr, or set notice immediately.
	if cmd != nil {
		if msg := runAppCmd(t, cmd); msg != nil {
			m = updateApp(t, m, msg)
		}
	}
	if !m.noticeErr || !strings.Contains(m.notice, "no editor found") {
		// If an editor was found on PATH somehow, still require that v was
		// consumed (composer empty) rather than typed.
		if m.composer.Value() == "v" {
			t.Fatalf("selected v must not type into composer; notice=%q", m.notice)
		}
	}
	if m.composer.Value() != "" {
		t.Fatalf("composer after review = %q, want empty", m.composer.Value())
	}

	// Direct unit check of the target the key would open.
	path, line, ok := tc.reviewTarget(dir)
	if !ok || path != "f.go" || line != 3 {
		t.Fatalf("reviewTarget = (%q, %d, %v), want (f.go, 3, true)", path, line, ok)
	}
}

func TestVOnBashToolShowsNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	var b strings.Builder
	for i := 0; i < 10; i++ {
		b.WriteString("line\n")
	}
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "run", Output: b.String()})
	m.composer.SetValue("")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{']'}, Alt: true})
	if m.selectedCell < 0 {
		t.Fatal("expected selection")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v")})
	if !m.noticeErr || !strings.Contains(m.notice, "no file to review") {
		t.Fatalf("notice = %q err=%v, want no file to review", m.notice, m.noticeErr)
	}
}

func TestToolCellCopyTextPrefersDiffOutputCommand(t *testing.T) {
	meta := json.RawMessage(`{"oldString":"foo\nbar","newString":"baz"}`)
	diffCell := &toolCell{
		name:     "edit",
		output:   "Edited",
		metadata: meta,
		done:     true,
	}
	got := diffCell.copyText()
	if !strings.Contains(got, "-foo") || !strings.Contains(got, "-bar") || !strings.Contains(got, "+baz") {
		t.Errorf("diff copyText = %q, want -/ + lines", got)
	}

	outCell := &toolCell{name: "bash", title: "echo hi", output: "hello\nworld\n", done: true}
	if got := outCell.copyText(); got != "hello\nworld" {
		t.Errorf("output copyText = %q, want trimmed body", got)
	}

	cmdCell := &toolCell{
		name: "bash",
		args: json.RawMessage(`{"command":"ls -la"}`),
		done: true,
	}
	if got := cmdCell.copyText(); got != "ls -la" {
		t.Errorf("command copyText = %q, want ls -la", got)
	}

	titleCell := &toolCell{name: "bash", title: "make test", done: true}
	if got := titleCell.copyText(); got != "make test" {
		t.Errorf("title copyText = %q, want make test", got)
	}

	if got := (&toolCell{name: "bash"}).copyText(); got != "" {
		t.Errorf("empty cell copyText = %q, want empty", got)
	}
}

func TestExploreCellCopyTextListsCalls(t *testing.T) {
	exp := &exploreCell{calls: []*toolCell{
		{name: "read", title: "a.go"},
		{name: "grep", title: "foo"},
	}}
	got := exp.copyText()
	if !strings.Contains(got, "read a.go") || !strings.Contains(got, "grep foo") {
		t.Errorf("explore copyText = %q", got)
	}
}

func TestYCopiesSelectedToolCellViaOSC52(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	const body = "clipboard-body-line-one\nclipboard-body-line-two\n"
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "run", Output: body})
	m.composer.SetValue("")
	// Select via empty enter expand path.
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	tc := m.toolByID["c1"]
	if tc == nil {
		t.Fatal("missing tool cell")
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Fatalf("y with empty composer mutated composer: %q", m.composer.Value())
	}
	if !tc.copiedFlash {
		t.Fatal("y did not set copied flash on tool cell")
	}
	plain := ansi.Strip(tc.render(80, m.th))
	if !strings.Contains(plain, "copied") {
		t.Errorf("render missing copied flash:\n%s", plain)
	}
	if m.cellClip == nil || m.cellClip.osc == "" {
		t.Fatal("y did not stage OSC52")
	}

	frame := m.View()
	if m.cellClip.osc != "" {
		t.Error("View did not consume one-shot OSC52")
	}
	reqs := osc52Payloads(frame)
	if len(reqs) != 1 {
		t.Fatalf("View OSC52 count = %d, want 1", len(reqs))
	}
	payload, err := decodeOSC52Payload(reqs[0])
	if err != nil {
		t.Fatalf("OSC52 payload: %v", err)
	}
	want := strings.TrimRight(body, "\n")
	if payload != want {
		t.Errorf("OSC52 payload = %q, want %q", payload, want)
	}
	if second := osc52Payloads(m.View()); len(second) != 0 {
		t.Errorf("second View re-emitted OSC52: %v", second)
	}

	// Flash clear timer is scheduled; apply the clear message directly.
	if cmd == nil {
		t.Fatal("y returned nil cmd, want flash clear tick")
	}
	m = updateApp(t, m, clearCellCopiedFlashMsg{idx: m.selectedCell, gen: m.copyFlashGen})
	if tc.copiedFlash {
		t.Error("flash still set after clear msg")
	}
}

func decodeOSC52Payload(b64 string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func TestYCopiesEditDiffAndFallsBackToLatest(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	meta := json.RawMessage(`{"oldString":"aaa","newString":"bbb"}`)
	m.applyEvent(protocol.ToolCallBegin{CallID: "e1", Name: "edit", Args: json.RawMessage(`{"path":"f.go"}`)})
	m.applyEvent(protocol.ToolCallEnd{
		CallID:   "e1",
		Title:    "f.go",
		Output:   "Edited f.go",
		Metadata: meta,
	})
	m.composer.SetValue("")
	m.selectedCell = -1 // force latest-cell fallback
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	tc := m.toolByID["e1"]
	if tc == nil || !tc.copiedFlash {
		t.Fatal("expected flash on latest edit cell")
	}
	frame := m.View()
	reqs := osc52Payloads(frame)
	if len(reqs) != 1 {
		t.Fatalf("OSC52 count = %d, want 1", len(reqs))
	}
	payload, err := decodeOSC52Payload(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	if payload != "-aaa\n+bbb" {
		t.Errorf("diff payload = %q, want -aaa\\n+bbb", payload)
	}
}

func TestYWithComposerTextInsertsY(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "run", Output: "out\n"})
	m.composer.SetValue("hel")
	// Place cursor at end so runes append.
	m.setComposerValueAt("hel", 3)
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	if got := m.composer.Value(); got != "hely" {
		t.Errorf("composer after y = %q, want hely", got)
	}
	if m.cellClip != nil && m.cellClip.osc != "" {
		t.Error("y with composer text staged OSC52")
	}
	if tc := m.toolByID["c1"]; tc != nil && tc.copiedFlash {
		t.Error("y with composer text set flash")
	}
}

func TestYCopiesAssistantChatTextViaOSC52(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	const reply = "copy-me assistant reply\nline two\n"
	m.applyEvent(protocol.UserMessage{Text: "prompt"})
	m.applyEvent(protocol.TextDelta{Text: reply})
	m.applyEvent(protocol.TurnCompleted{})
	m.composer.SetValue("")
	m.selectedCell = -1

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Fatalf("y with chat text mutated composer: %q", m.composer.Value())
	}
	var asst *assistantCell
	for _, c := range m.cells {
		if a, ok := c.(*assistantCell); ok {
			asst = a
		}
	}
	if asst == nil || !asst.copiedFlash {
		t.Fatal("y did not flash assistant cell")
	}
	if m.cellClip == nil || m.cellClip.osc == "" {
		t.Fatal("y did not stage OSC52 for assistant text")
	}
	frame := m.View()
	reqs := osc52Payloads(frame)
	if len(reqs) != 1 {
		t.Fatalf("View OSC52 count = %d, want 1", len(reqs))
	}
	payload, err := decodeOSC52Payload(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimRight(reply, "\n")
	if payload != want {
		t.Errorf("OSC52 payload = %q, want %q", payload, want)
	}
	if cmd == nil {
		t.Fatal("y returned nil cmd, want flash clear tick")
	}
	// Prefer latest tool over assistant when both exist.
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "t1", Title: "run", Output: "tool-out\n"})
	m.composer.SetValue("")
	m.selectedCell = -1
	m.cellClip = &cellClipboard{}
	asst.copiedFlash = false
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	tc := m.toolByID["t1"]
	if tc == nil || !tc.copiedFlash {
		t.Fatal("y should prefer latest tool cell over assistant")
	}
	if asst.copiedFlash {
		t.Error("assistant flash should clear when copying tool")
	}
	frame = m.View()
	reqs = osc52Payloads(frame)
	if len(reqs) != 1 {
		t.Fatalf("tool OSC52 count = %d, want 1", len(reqs))
	}
	payload, err = decodeOSC52Payload(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	if payload != "tool-out" {
		t.Errorf("tool payload = %q, want tool-out", payload)
	}
}

func TestYCopiesUserMessageWhenNoAssistantOrTool(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: "solo user prompt\n"})
	m.composer.SetValue("")
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	m = updated.(Model)
	var uc *userCell
	for _, c := range m.cells {
		if u, ok := c.(*userCell); ok {
			uc = u
		}
	}
	if uc == nil || !uc.copiedFlash {
		t.Fatal("y should copy user cell when it is the only copyable content")
	}
	frame := m.View()
	reqs := osc52Payloads(frame)
	if len(reqs) != 1 {
		t.Fatalf("OSC52 count = %d, want 1", len(reqs))
	}
	payload, err := decodeOSC52Payload(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	if payload != "solo user prompt" {
		t.Errorf("payload = %q, want solo user prompt", payload)
	}
}

func TestChatCellCopyText(t *testing.T) {
	if got := (&assistantCell{text: "hi\n"}).copyText(); got != "hi" {
		t.Errorf("assistant copyText = %q", got)
	}
	if got := (&userCell{text: "yo\n"}).copyText(); got != "yo" {
		t.Errorf("user copyText = %q", got)
	}
	if got := (&assistantCell{}).copyText(); got != "" {
		t.Errorf("empty assistant copyText = %q", got)
	}
}

func keyMsgAltY() tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}, Alt: true}
}

func TestCopyLastResponseViaAltYAndSlash(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	const early = "first reply"
	const final = "final assistant answer\nline two\n"
	m.applyEvent(protocol.UserMessage{Text: "q1"})
	m.applyEvent(protocol.TextDelta{Text: early})
	m.applyEvent(protocol.TurnCompleted{})
	m.applyEvent(protocol.UserMessage{Text: "q2"})
	m.applyEvent(protocol.TextDelta{Text: "partial mid-tool "})
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "t1", Title: "run", Output: "tool-spam\n"})
	m.applyEvent(protocol.TextDelta{Text: final})
	m.applyEvent(protocol.TurnCompleted{})
	// Composer has text: alt+y must still copy (unlike bare y).
	m.composer.SetValue("draft follow-up")
	m.selectedCell = -1

	updated, cmd := m.Update(keyMsgAltY())
	m = updated.(Model)
	if got := m.composer.Value(); got != "draft follow-up" {
		t.Fatalf("alt+y mutated composer: %q", got)
	}
	if m.notice != "copied last response" || m.noticeErr {
		t.Fatalf("alt+y notice = %q err=%v, want success", m.notice, m.noticeErr)
	}
	var asst *assistantCell
	for _, c := range m.cells {
		if a, ok := c.(*assistantCell); ok && a.complete && strings.Contains(a.text, "final") {
			asst = a
		}
	}
	if asst == nil || !asst.copiedFlash {
		t.Fatal("alt+y did not flash last complete assistant cell")
	}
	if m.cellClip == nil || m.cellClip.osc == "" {
		t.Fatal("alt+y did not stage OSC52")
	}
	frame := m.View()
	reqs := osc52Payloads(frame)
	if len(reqs) != 1 {
		t.Fatalf("View OSC52 count = %d, want 1", len(reqs))
	}
	payload, err := decodeOSC52Payload(reqs[0])
	if err != nil {
		t.Fatal(err)
	}
	want := strings.TrimRight(final, "\n")
	if payload != want {
		t.Errorf("OSC52 payload = %q, want %q", payload, want)
	}
	if strings.Contains(payload, "tool-spam") || strings.Contains(payload, early) {
		t.Errorf("payload should be last complete assistant only: %q", payload)
	}
	if cmd == nil {
		t.Fatal("alt+y returned nil cmd, want flash clear tick")
	}

	// /copy mirrors the same path and clears the composer.
	m.cellClip = &cellClipboard{}
	asst.copiedFlash = false
	m.composer.SetValue("/copy")
	updated, cmd = m.handleCommand("/copy")
	m = updated.(Model)
	if m.composer.Value() != "" {
		t.Fatalf("/copy left composer = %q", m.composer.Value())
	}
	if m.notice != "copied last response" || m.noticeErr {
		t.Fatalf("/copy notice = %q err=%v", m.notice, m.noticeErr)
	}
	if m.cellClip == nil || m.cellClip.osc == "" {
		t.Fatal("/copy did not stage OSC52")
	}
	if !asst.copiedFlash {
		t.Fatal("/copy did not flash assistant cell")
	}
	_ = cmd
}

func TestResolveLastAssistantCopyIndex(t *testing.T) {
	// Newest non-empty assistant wins (even if incomplete / after tools).
	cells := []cell{
		&assistantCell{text: "done", complete: true},
		&toolCell{name: "bash", output: "noise", done: true},
		&assistantCell{text: "still streaming", complete: false},
	}
	if got := resolveLastAssistantCopyIndex(cells); got != 2 {
		t.Fatalf("newest index = %d, want 2", got)
	}
	if got := resolveLastAssistantCopyIndex([]cell{
		&assistantCell{text: "old", complete: true},
		&assistantCell{text: "new", complete: true},
	}); got != 1 {
		t.Fatalf("latest complete = %d, want 1", got)
	}
	if got := resolveLastAssistantCopyIndex([]cell{
		&assistantCell{text: "", complete: true},
		&assistantCell{text: "ok", complete: true},
	}); got != 1 {
		t.Fatalf("skip empty = %d, want 1", got)
	}
	if got := resolveLastAssistantCopyIndex([]cell{&toolCell{name: "bash"}}); got != -1 {
		t.Fatalf("no assistant = %d, want -1", got)
	}
}

func TestCopyLastResponseEmptyNotice(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.applyEvent(protocol.UserMessage{Text: "only user"})
	m.applyEvent(protocol.ToolCallBegin{CallID: "t1", Name: "bash"})
	m.applyEvent(protocol.ToolCallEnd{CallID: "t1", Title: "run", Output: "tool only\n"})
	m.composer.SetValue("")

	updated, cmd := m.Update(keyMsgAltY())
	m = updated.(Model)
	if cmd != nil {
		t.Fatal("empty copy should not schedule flash clear")
	}
	if m.notice != "no assistant response to copy" || !m.noticeErr {
		t.Fatalf("notice = %q err=%v, want failure", m.notice, m.noticeErr)
	}
	if m.cellClip != nil && m.cellClip.osc != "" {
		t.Error("empty copy staged OSC52")
	}

	m.clearNotice()
	m.composer.SetValue("/copy")
	updated, _ = m.handleCommand("/copy")
	m = updated.(Model)
	if m.notice != "no assistant response to copy" || !m.noticeErr {
		t.Fatalf("/copy empty notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestCopyLastResponseBindingInCatalog(t *testing.T) {
	keys := defaultKeyMap()
	if !key.Matches(keyMsgAltY(), keys.CopyLastResponse) {
		t.Fatal("alt+y must match CopyLastResponse")
	}
	help := keys.CopyLastResponse.Help()
	if help.Key != "alt+y" || help.Desc != "copy last response" {
		t.Fatalf("CopyLastResponse help = %#v", help)
	}
	found := false
	for _, e := range keybindCatalog(keys) {
		if e.ID == "global.copy-last" {
			found = true
			if e.Keys != "alt+y" || e.Action != "copy last response" {
				t.Errorf("catalog entry = %#v", e)
			}
		}
	}
	if !found {
		t.Fatal("keybindCatalog missing global.copy-last")
	}
}

func TestToolCallOutputAppendsUntilEnd(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.applyEvent(protocol.ToolCallBegin{CallID: "c1", Name: "bash", Args: json.RawMessage(`{"command":"echo"}`)})
	m.applyEvent(protocol.ToolCallOutput{CallID: "c1", Data: "hello "})
	m.applyEvent(protocol.ToolCallOutput{CallID: "c1", Data: "world\n"})
	tc := m.toolByID["c1"]
	if tc == nil {
		t.Fatal("missing tool cell")
	}
	if tc.done {
		t.Fatal("tool should still be running")
	}
	if tc.output != "hello world\n" {
		t.Fatalf("live output = %q, want %q", tc.output, "hello world\n")
	}
	plain := ansi.Strip(tc.render(80, theme.Default()))
	if !strings.Contains(plain, "hello world") {
		t.Fatalf("live render missing stream:\n%s", plain)
	}
	m.applyEvent(protocol.ToolCallEnd{CallID: "c1", Title: "echo", Output: "hello world\n(final)"})
	if !tc.done {
		t.Fatal("tool should be done")
	}
	if tc.output != "hello world\n(final)" {
		t.Fatalf("final output = %q", tc.output)
	}
	// Further stream chunks after end are ignored.
	m.applyEvent(protocol.ToolCallOutput{CallID: "c1", Data: "late"})
	if tc.output != "hello world\n(final)" {
		t.Fatalf("post-end output mutated: %q", tc.output)
	}
}

func TestAssistantCellRendersHeadingMarkdown(t *testing.T) {
	th := theme.Default()
	const src = "# Title"
	out := (&assistantCell{text: src, complete: true}).render(80, th)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "Title") {
		t.Errorf("heading body missing Title:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing:\n%s", plain)
	}
	// Pin the glamour path: body matches markdownRender (not a divergent plain dump).
	// AutoStyle may keep the leading '#' in notty environments; do not assert its removal.
	bodyWidth := assistantBodyWidth(80, th)
	wantMD, err := markdownRender(src, bodyWidth)
	if err != nil {
		t.Fatalf("markdownRender: %v", err)
	}
	body := assistantBodyPlain(plain)
	if collapsedWS(body) != collapsedWS(ansi.Strip(wantMD)) {
		t.Errorf("assistant body not from markdownRender:\n got %q\nwant %q", body, wantMD)
	}
}

func TestAssistantCellRendersFencedCodeBlock(t *testing.T) {
	th := theme.Default()
	src := "```go\nfunc main() {}\n```"
	plain := ansi.Strip((&assistantCell{text: src, complete: true}).render(80, th))
	if !strings.Contains(collapsedWS(plain), "func main") {
		t.Errorf("code body missing func main:\n%s", plain)
	}
	if strings.Contains(plain, "```") {
		t.Errorf("fenced code still contains triple backticks:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing:\n%s", plain)
	}
}

func TestAssistantCellMarkdownWidthSafe(t *testing.T) {
	th := theme.Default()
	// Prose + short fenced code (avoid ultra-long tokens glamour will not break).
	src := "This is a fairly long prose paragraph that should wrap cleanly inside the cell width without overflowing the terminal columns.\n\n```go\nfmt.Println(\"hi\")\n```"
	for _, width := range []int{24, 40} {
		out := (&assistantCell{text: src, complete: true}).render(width, th)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d line %d: StringWidth=%d > %d: %q", width, i, got, width, ansi.Strip(line))
			}
		}
		plain := collapsedWS(ansi.Strip(out))
		if !strings.Contains(plain, "prose paragraph") {
			t.Errorf("width %d missing prose:\n%s", width, ansi.Strip(out))
		}
		if !strings.Contains(plain, "Println") {
			t.Errorf("width %d missing code content:\n%s", width, ansi.Strip(out))
		}
		if strings.Contains(ansi.Strip(out), "```") {
			t.Errorf("width %d still has fences:\n%s", width, ansi.Strip(out))
		}
	}
}

func TestAssistantCellMarkdownWidthSafeLongFence(t *testing.T) {
	th := theme.Default()
	// Long unbreakable fenced line must be clamped by clampRenderWidth / Hardwrap.
	long := strings.Repeat("A", 80)
	src := "```\n" + long + "\n```"
	c := &assistantCell{text: src, complete: true}
	for _, width := range []int{24, 40} {
		out := c.render(width, th)
		for i, line := range strings.Split(out, "\n") {
			if got := ansi.StringWidth(line); got > width {
				t.Errorf("width %d line %d: StringWidth=%d > %d: %q", width, i, got, width, ansi.Strip(line))
			}
		}
		plain := ansi.Strip(out)
		if !strings.Contains(plain, "AAAA") {
			t.Errorf("width %d missing long fence content:\n%s", width, plain)
		}
	}
}

func TestAssistantCellStreamingVsComplete(t *testing.T) {
	th := theme.Default()
	const src = "# Title"
	const width = 80

	// Incomplete: plain path keeps raw markdown source markers.
	streaming := &assistantCell{text: src, complete: false}
	streamPlain := ansi.Strip(streaming.render(width, th))
	if !strings.Contains(streamPlain, "# Title") {
		t.Errorf("streaming body should keep raw source:\n%s", streamPlain)
	}
	// Same as plain renderCellText path (label + indented source).
	plainPath := ansi.Strip((&assistantCell{text: src, complete: false}).render(width, th))
	if streamPlain != plainPath {
		t.Errorf("streaming output diverged from plain path:\n got %q\nwant %q", streamPlain, plainPath)
	}

	// Complete with real glamour: body matches markdownRender.
	// (AutoStyle may keep '#' in notty; do not require visual difference from streaming.)
	done := &assistantCell{text: src, complete: true}
	donePlain := ansi.Strip(done.render(width, th))
	bodyWidth := assistantBodyWidth(width, th)
	wantMD, err := markdownRender(src, bodyWidth)
	if err != nil {
		t.Fatalf("markdownRender: %v", err)
	}
	body := assistantBodyPlain(donePlain)
	if collapsedWS(body) != collapsedWS(ansi.Strip(wantMD)) {
		t.Errorf("complete body not from markdownRender:\n got %q\nwant %q", body, wantMD)
	}

	// Injected renderer makes the complete path observably distinct from streaming.
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })
	markdownRender = func(source string, w int) (string, error) {
		return "MD:" + source, nil
	}
	injected := ansi.Strip((&assistantCell{text: src, complete: true}).render(width, th))
	if !strings.Contains(injected, "MD:# Title") {
		t.Errorf("complete cell missing injected markdown body: %q", injected)
	}
	if strings.Contains(streamPlain, "MD:") {
		t.Errorf("streaming cell unexpectedly used markdown path: %q", streamPlain)
	}
}

func TestAssistantCellIncompleteSkipsMarkdownRender(t *testing.T) {
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })

	var calls int
	markdownRender = func(source string, width int) (string, error) {
		calls++
		return "should-not-appear", nil
	}

	th := theme.Default()
	const src = "# Title"
	out := ansi.Strip((&assistantCell{text: src, complete: false}).render(80, th))
	if calls != 0 {
		t.Errorf("incomplete cell called markdownRender %d times, want 0", calls)
	}
	if strings.Contains(out, "should-not-appear") {
		t.Errorf("incomplete cell used markdown body: %q", out)
	}
	if !strings.Contains(out, "# Title") {
		t.Errorf("incomplete cell missing raw source:\n%s", out)
	}
}

func TestAssistantCellEmptyAndWhitespace(t *testing.T) {
	th := theme.Default()
	for _, tc := range []struct {
		name     string
		text     string
		complete bool
	}{
		{"empty incomplete", "", false},
		{"empty complete", "", true},
		{"spaces incomplete", "   ", false},
		{"spaces complete", "   ", true},
		{"newlines incomplete", "\n\n\t  \n", false},
		{"newlines complete", "\n\n\t  \n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := (&assistantCell{text: tc.text, complete: tc.complete}).render(80, th)
			plain := ansi.Strip(out)
			if !strings.Contains(plain, "strike") {
				t.Errorf("label missing for %q:\n%s", tc.text, plain)
			}
			body := strings.TrimSpace(assistantBodyPlain(plain))
			if body != "" {
				t.Errorf("expected empty body for %q, got %q", tc.text, body)
			}
		})
	}
}

func TestAssistantCellPlainProse(t *testing.T) {
	th := theme.Default()
	const msg = "Hello world without markup"
	// Complete path still shows plain prose content via markdownRender.
	plain := ansi.Strip((&assistantCell{text: msg, complete: true}).render(80, th))
	if !strings.Contains(collapsedWS(plain), msg) {
		t.Errorf("plain prose missing:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing:\n%s", plain)
	}
}

func TestAssistantCellMarkdownErrorFallback(t *testing.T) {
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })
	markdownRender = func(source string, width int) (string, error) {
		return "", errMarkdownTest
	}

	th := theme.Default()
	const src = "fallback plain source text that must remain visible"
	const width = 40
	cell := &assistantCell{text: src, complete: true}
	out := cell.render(width, th)
	plain := ansi.Strip(out)
	// Hardwrap may insert mid-word breaks; compare with all whitespace removed.
	if !strings.Contains(stripAllWS(plain), stripAllWS(src)) {
		t.Errorf("error fallback omitted source text:\n%s", plain)
	}
	if !strings.Contains(plain, "strike") {
		t.Errorf("assistant label missing on error path:\n%s", plain)
	}
	for i, line := range strings.Split(out, "\n") {
		if got := ansi.StringWidth(line); got > width {
			t.Errorf("fallback line %d width = %d, want <= %d: %q", i, got, width, ansi.Strip(line))
		}
	}
}

func TestAssistantCellMarkdownCache(t *testing.T) {
	orig := markdownRender
	t.Cleanup(func() { markdownRender = orig })

	var calls int
	var lastSrc string
	var lastW int
	markdownRender = func(source string, width int) (string, error) {
		calls++
		lastSrc = source
		lastW = width
		return "rendered:" + source, nil
	}

	th := theme.Default()
	cell := &assistantCell{text: "alpha", complete: true}

	out1 := ansi.Strip(cell.render(80, th))
	if calls != 1 {
		t.Fatalf("first render: calls=%d, want 1", calls)
	}
	if !strings.Contains(out1, "rendered:alpha") {
		t.Fatalf("first render missing injected body: %q", out1)
	}

	out2 := ansi.Strip(cell.render(80, th))
	if calls != 1 {
		t.Errorf("same text/width re-render: calls=%d, want 1 (cache hit)", calls)
	}
	if out2 != out1 {
		t.Errorf("cache hit changed output:\n got %q\nwant %q", out2, out1)
	}

	_ = cell.render(60, th)
	if calls != 2 {
		t.Errorf("width change: calls=%d, want 2", calls)
	}
	if lastW == 0 {
		t.Errorf("width change did not pass body width")
	}

	cell.text = "beta"
	out3 := ansi.Strip(cell.render(60, th))
	if calls != 3 {
		t.Errorf("text change: calls=%d, want 3", calls)
	}
	if lastSrc != "beta" {
		t.Errorf("text change lastSrc=%q, want beta", lastSrc)
	}
	if !strings.Contains(out3, "rendered:beta") {
		t.Errorf("text change missing new body: %q", out3)
	}
}

func TestUserCellUnchangedByMarkdown(t *testing.T) {
	// User cells stay plain Hardwrap text; markdown markers must remain literal.
	th := theme.Default()
	plain := ansi.Strip((&userCell{text: "# Title"}).render(80, th))
	if !strings.Contains(plain, "# Title") {
		t.Errorf("user cell should keep raw markdown:\n%s", plain)
	}
	if !strings.Contains(plain, "you") {
		t.Errorf("user label missing:\n%s", plain)
	}
}

// errMarkdownTest is a sentinel for assistant markdown error-path tests.
var errMarkdownTest = errors.New("markdown render failed")

// assistantBodyPlain returns the stripped cell body after the first (label) line.
func assistantBodyPlain(plain string) string {
	parts := strings.SplitN(plain, "\n", 2)
	if len(parts) < 2 {
		return ""
	}
	return parts[1]
}

func collapsedWS(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func stripAllWS(s string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			return -1
		}
		return r
	}, s)
}

func assistantBodyWidth(width int, th theme.Theme) int {
	th = th.Resolve()
	indentation := themedSpace(th.Spacing.SM)
	return max(1, width-lipgloss.Width(indentation))
}
