package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/protocol"
)

func TestFindFileMentions(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{name: "simple", in: "see @pkg/main.go please", want: []string{"pkg/main.go"}},
		{name: "start", in: "@a.go", want: []string{"a.go"}},
		{name: "multi", in: "@a.go and @b/c.go", want: []string{"a.go", "b/c.go"}},
		{name: "skip email", in: "mail user@example.com ok", want: nil},
		{name: "multiline", in: "line1\n@dir/file.go\nend", want: []string{"dir/file.go"}},
		{name: "reject dotdot", in: "x @../secret y", want: nil},
		{name: "no path", in: "just @ alone", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spans := findFileMentions(tt.in)
			var got []string
			for _, sp := range spans {
				got = append(got, sp.Path)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("paths = %v, want %v", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("paths[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExpandFileMentionsAttachesContents(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{
		"pkg/main.go": []byte("package main\n"),
		"bin.dat":     {0x00, 0x01},
	}}
	expanded, notices := expandFileMentions("look at @pkg/main.go thanks", ff)
	if !strings.Contains(expanded, "package main") {
		t.Fatalf("expanded missing contents: %q", expanded)
	}
	if !strings.Contains(expanded, "--- file: pkg/main.go ---") {
		t.Fatalf("expanded missing fence: %q", expanded)
	}
	if !strings.HasPrefix(expanded, "look at @pkg/main.go thanks") {
		t.Fatalf("prompt prefix lost: %q", expanded)
	}
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}

	expanded, notices = expandFileMentions("bad @bin.dat", ff)
	if strings.Contains(expanded, "\x00") {
		t.Fatal("binary content leaked into expanded text")
	}
	if len(notices) != 1 || !strings.Contains(notices[0], "binary") {
		t.Fatalf("notices = %v", notices)
	}

	got, n := expandFileMentions("plain prompt", ff)
	if got != "plain prompt" || len(n) != 0 {
		t.Fatalf("plain = %q notices=%v", got, n)
	}

	got, n = expandFileMentions("@pkg/main.go", nil)
	if got != "@pkg/main.go" || len(n) != 0 {
		t.Fatalf("nil files = %q %v", got, n)
	}
}

func TestExpandFileMentionsDedupesPaths(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{"a.go": []byte("A")}}
	expanded, _ := expandFileMentions("@a.go and again @a.go", ff)
	if strings.Count(expanded, "--- file: a.go ---") != 1 {
		t.Fatalf("expected one attachment block: %q", expanded)
	}
}

func TestAtFileCompletionActivation(t *testing.T) {
	paths := []string{"internal/tui/app.go", "pkg/main.go"}
	tests := []struct {
		name     string
		value    string
		row, col int
		wantOpen bool
	}{
		{name: "at only", value: "@", col: 1, wantOpen: true},
		{name: "partial", value: "@app", col: 4, wantOpen: true},
		{name: "mid line", value: "see @pkg", col: 8, wantOpen: true},
		{name: "email", value: "a@b.com", col: 3, wantOpen: false},
		{name: "second line", value: "hi\n@main", row: 1, col: 5, wantOpen: true},
		{name: "after space token", value: "@a.go more", col: 10, wantOpen: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := atFileCompletion(tt.value, tt.row, tt.col, paths, "")
			if (c != nil) != tt.wantOpen {
				t.Fatalf("open = %v, want %v", c != nil, tt.wantOpen)
			}
		})
	}
}

func TestApplyFileCompletionInsertsMention(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{
		"internal/tui/app.go": []byte("package tui"),
		"pkg/main.go":         []byte("package main"),
	}}
	m, _ := newAppTestModel(nil, nil)
	m.services.Files = ff
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.setComposerValueAt("check @app", len([]rune("check @app")))
	m.recomputeCompletion()
	if m.completion == nil || !m.completion.fileMention {
		t.Fatalf("completion = %#v, want file mention", m.completion)
	}
	for i, c := range m.completion.Candidates {
		if c.Path == "internal/tui/app.go" {
			m.completion.Selected = i
			break
		}
	}
	m.applyCompletion()
	got := m.composer.Value()
	if !strings.Contains(got, "@internal/tui/app.go") {
		t.Fatalf("composer = %q, want @internal/tui/app.go", got)
	}
	if m.completion != nil {
		t.Fatal("completion stayed open")
	}
}

func TestSubmitExpandsFileMentionsForModel(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{
		"note.go": []byte("secret body"),
	}}
	m, ops := newAppTestModel(nil, nil)
	m.services.Files = ff
	m.providerName = "echo"
	m.modelName = "echo"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.setComposerValueAt("read @note.go", len([]rune("read @note.go")))
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = updated.(Model)
	_ = runAllAppCmds(t, cmd)

	op := receiveAppOp(t, ops)
	in, ok := op.(protocol.UserInput)
	if !ok {
		t.Fatalf("op = %T, want UserInput", op)
	}
	if !strings.Contains(in.Text, "secret body") {
		t.Fatalf("model text missing file body: %q", in.Text)
	}
	if !strings.Contains(in.Text, "--- file: note.go ---") {
		t.Fatalf("model text missing attachment fence: %q", in.Text)
	}
	if m.composer.Value() != "" {
		t.Fatalf("composer not cleared: %q", m.composer.Value())
	}
}

func TestSubmitWithoutMentionUnchanged(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{"note.go": []byte("x")}}
	m, ops := newAppTestModel(nil, nil)
	m.services.Files = ff
	m.providerName = "echo"
	m.modelName = "echo"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setComposerValueAt("hello world", len([]rune("hello world")))
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
	_ = runAllAppCmds(t, cmd)
	op := receiveAppOp(t, ops)
	in := op.(protocol.UserInput)
	if in.Text != "hello world" {
		t.Fatalf("UserInput.Text = %q", in.Text)
	}
}

func TestFileCompletionDismissEsc(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{"a.go": []byte("a")}}
	m, ops := newAppTestModel(nil, nil)
	m.services.Files = ff
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m = typeAppText(t, m, "@")
	if m.completion == nil {
		t.Fatal("expected @ completion")
	}
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.completion != nil {
		t.Fatal("esc should dismiss file completion")
	}
	assertNoAppOp(t, ops)
}

func TestFileMentionWorksWithMultiline(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{"z.go": []byte("Z")}}
	m, ops := newAppTestModel(nil, nil)
	m.services.Files = ff
	m.providerName = "echo"
	m.modelName = "echo"
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.setComposerValueAt("line one\nsee @z.go", len([]rune("line one\nsee @z.go")))
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = updated
	_ = runAllAppCmds(t, cmd)
	op := receiveAppOp(t, ops)
	in := op.(protocol.UserInput)
	if !strings.Contains(in.Text, "Z") || !strings.Contains(in.Text, "line one") {
		t.Fatalf("multiline submit = %q", in.Text)
	}
}

func TestAtFileCompletionEmptyStateExplains(t *testing.T) {
	ff := &fakeFiles{files: map[string][]byte{}}
	m, _ := newAppTestModel(nil, nil)
	m.services.Files = ff
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 100, Height: 40})
	m.setComposerValueAt("@nope", len([]rune("@nope")))
	m.recomputeCompletion()
	if m.completion == nil || !m.completion.fileMention {
		t.Fatalf("completion = %#v, want empty file-mention popup", m.completion)
	}
	if len(m.completion.Candidates) != 0 {
		t.Fatalf("candidates = %#v, want empty", m.completion.Candidates)
	}
	if !strings.Contains(m.completion.emptyHint, "no files match") {
		t.Fatalf("emptyHint = %q", m.completion.emptyHint)
	}
	plain := ansi.Strip(m.completion.view(80, 4, m.th))
	if !strings.Contains(plain, "no files match") {
		t.Fatalf("empty view = %q", plain)
	}
}

func TestExpandFileMentionsFolder(t *testing.T) {
	ff := &folderFiles{listing: "directory listing (immediate children only):\nmain.go\nsub/\n"}
	expanded, notices := expandFileMentions("see @pkg/", ff)
	if len(notices) != 0 {
		t.Fatalf("notices = %v", notices)
	}
	if !strings.Contains(expanded, "--- folder: pkg/ ---") {
		t.Fatalf("expanded missing folder fence: %q", expanded)
	}
	if !strings.Contains(expanded, "main.go") {
		t.Fatalf("expanded missing listing: %q", expanded)
	}
}

// folderFiles implements host.Files with a single directory ReadScoped.
type folderFiles struct {
	listing string
}

func (f *folderFiles) ReadFile(string) ([]byte, error) { return nil, fmt.Errorf("unused") }
func (f *folderFiles) ListDir(string) ([]host.DirEntry, error) {
	return nil, fmt.Errorf("unused")
}
func (f *folderFiles) SearchFiles(string, int) ([]string, error) { return nil, nil }
func (f *folderFiles) ReadScoped(path string) (host.FileContent, error) {
	path = strings.TrimSpace(path)
	path = strings.ReplaceAll(path, "\\", "/")
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return host.FileContent{Path: path, Content: f.listing}, nil
}
func (f *folderFiles) ApplyEdit(host.EditApply) (host.EditApplyResult, error) {
	return host.EditApplyResult{}, fmt.Errorf("unused")
}
func (f *folderFiles) ApplyPatch(string) (string, error) { return "", fmt.Errorf("unused") }
