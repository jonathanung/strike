package tui

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedactSecrets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "anthropic",
			in:   "key sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345",
			want: "key [REDACTED_ANTHROPIC_KEY]",
		},
		{
			name: "openai-ish",
			in:   "token sk-abcdefghijklmnopqrstuvwxyz0123456789",
			want: "token [REDACTED_API_KEY]",
		},
		{
			name: "bearer",
			in:   "Authorization: Bearer abcdefghijklmnopqrstuvwxyz0123",
			want: "Authorization: Bearer [REDACTED]",
		},
		{
			name: "github",
			in:   "export GITHUB_TOKEN=ghp_abcdefghijklmnopqrstuvwxyz0123456789",
			want: "export GITHUB_TOKEN=[REDACTED_GITHUB_TOKEN]",
		},
		{
			name: "assignment",
			in:   `api_key=supersecretvalue123`,
			want: "api_key=[REDACTED]",
		},
		{
			name: "plain prose",
			in:   "please review the auth package",
			want: "please review the auth package",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactSecrets(tt.in); got != tt.want {
				t.Fatalf("redactSecrets = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseExportArgs(t *testing.T) {
	tests := []struct {
		args    []string
		path    string
		open    bool
		wantErr bool
	}{
		{nil, "", false, false},
		{[]string{"out.md"}, "out.md", false, false},
		{[]string{"--open"}, "", true, false},
		{[]string{"-o", "out.md"}, "out.md", true, false},
		{[]string{"out.md", "--open"}, "out.md", true, false},
		{[]string{"--open", "out.md"}, "out.md", true, false},
		{[]string{"a.md", "b.md"}, "", false, true},
		{[]string{"--nope"}, "", false, true},
	}
	for _, tt := range tests {
		path, open, err := parseExportArgs(tt.args)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("parseExportArgs(%v) err = nil, want error", tt.args)
			}
			continue
		}
		if err != nil {
			t.Fatalf("parseExportArgs(%v) err = %v", tt.args, err)
		}
		if path != tt.path || open != tt.open {
			t.Fatalf("parseExportArgs(%v) = (%q, %v), want (%q, %v)", tt.args, path, open, tt.path, tt.open)
		}
	}
}

func TestDefaultExportPath(t *testing.T) {
	now := time.Date(2026, 7, 26, 15, 4, 5, 0, time.UTC)
	got := defaultExportPath("/proj", "sess-abcdef123456", now)
	want := filepath.Join("/proj", ".strike", "exports", "strike-abcdef123456-20260726-150405.md")
	if got != want {
		t.Fatalf("default with workDir = %q, want %q", got, want)
	}
	tmp := defaultExportPath("", "id", now)
	if !strings.HasPrefix(tmp, os.TempDir()) {
		t.Fatalf("default without workDir = %q, want under temp", tmp)
	}
	if !strings.HasSuffix(tmp, ".md") {
		t.Fatalf("default without workDir missing .md: %q", tmp)
	}
}

func TestResolveExportPath(t *testing.T) {
	root := t.TempDir()
	got, err := resolveExportPath(root, "notes/out.md")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "notes", "out.md")
	if got != want {
		t.Fatalf("relative = %q, want %q", got, want)
	}
	if _, err := resolveExportPath(root, "../escape.md"); err == nil {
		t.Fatal("expected escape rejection")
	}
	abs := filepath.Join(t.TempDir(), "abs.md")
	got, err = resolveExportPath(root, abs)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Clean(abs) {
		t.Fatalf("abs = %q, want %q", got, abs)
	}
}

func TestWriteTranscriptMarkdownRoundTrip(t *testing.T) {
	cells := []cell{
		&userCell{text: "hello with sk-ant-api03-abcdefghijklmnopqrstuvwxyz012345"},
		&assistantCell{text: "world **bold**", complete: true},
		&toolCell{
			name:   "bash",
			args:   json.RawMessage(`{"command":"echo hi"}`),
			title:  "echo hi",
			output: "hi\n",
			done:   true,
		},
		&exploreCell{calls: []*toolCell{
			{name: "read", title: "main.go", done: true},
			{name: "grep", title: "pattern", done: true},
		}},
		&infoCell{text: "note"},
		&errorCell{text: "boom"},
	}
	meta := exportMeta{
		SessionID: "sess-1",
		Title:     "demo",
		Exported:  time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC),
		Provider:  "echo",
		Model:     "echo",
		Agent:     "build",
	}
	var buf bytes.Buffer
	if err := writeTranscriptMarkdown(&buf, cells, meta); err != nil {
		t.Fatal(err)
	}
	got := buf.String()
	for _, want := range []string{
		"# Strike session export",
		"**Session:** `sess-1`",
		"**Title:** demo",
		"**Model:** echo / echo",
		"**Agent:** build",
		"## You",
		"[REDACTED_ANTHROPIC_KEY]",
		"## Strike",
		"world **bold**",
		"### Tool: `bash` (ok)",
		"echo hi",
		"### Exploring (2)",
		"- read - main.go",
		"### Info",
		"note",
		"### Error",
		"boom",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markdown missing %q\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-ant-api03-") {
		t.Errorf("unredacted key leaked:\n%s", got)
	}
}

func TestWriteExportMarkdownCreatesParents(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "dir", "out.md")
	cells := []cell{
		&userCell{text: "u"},
		&assistantCell{text: "a", complete: true},
	}
	meta := exportMeta{SessionID: "s1", Exported: time.Unix(0, 0).UTC()}
	if err := writeExportMarkdown(path, cells, meta); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "## You") || !strings.Contains(string(data), "## Strike") {
		t.Fatalf("file content unexpected:\n%s", data)
	}
}

func TestWriteExportMarkdownEmptyTranscript(t *testing.T) {
	var buf bytes.Buffer
	if err := writeTranscriptMarkdown(&buf, nil, exportMeta{SessionID: "x"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "_Empty transcript._") {
		t.Fatalf("got %q", buf.String())
	}
}

func TestHandleExportCommandWritesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "chat.md")
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.sessionID = "session-export-1"
	m.titleTopic = "export demo"
	m.cells = []cell{
		&userCell{text: "ping"},
		&assistantCell{text: "pong", complete: true},
	}

	next, cmd := m.handleCommand("/export " + path)
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected export cmd")
	}
	if !strings.Contains(m.notice, "exporting") {
		t.Fatalf("notice = %q, want exporting", m.notice)
	}
	msg := runAppCmd(t, cmd)
	finished, ok := msg.(exportFinishedMsg)
	if !ok {
		t.Fatalf("msg = %T, want exportFinishedMsg", msg)
	}
	if finished.err != nil || finished.path != path {
		t.Fatalf("finished = %+v", finished)
	}
	next, cmd = m.applyExportFinished(finished)
	m = next.(Model)
	if cmd != nil {
		t.Fatalf("unexpected follow-up cmd %T", cmd)
	}
	if m.noticeErr || !strings.Contains(m.notice, "exported to") {
		t.Fatalf("notice = %q err=%v", m.notice, m.noticeErr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "ping") || !strings.Contains(body, "pong") {
		t.Fatalf("export body missing turns:\n%s", body)
	}
	if !strings.Contains(body, "export demo") {
		t.Fatalf("export missing title:\n%s", body)
	}
}

func TestHandleExportCommandDefaultPath(t *testing.T) {
	dir := t.TempDir()
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.sessionID = "abcdef123456"
	m.cells = []cell{&userCell{text: "hi"}}

	next, cmd := m.handleCommand("/export")
	m = next.(Model)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	msg := runAppCmd(t, cmd)
	finished, ok := msg.(exportFinishedMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if finished.err != nil {
		t.Fatal(finished.err)
	}
	rel, err := filepath.Rel(dir, finished.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rel, filepath.Join(".strike", "exports")+string(os.PathSeparator)) {
		t.Fatalf("default path %q not under .strike/exports", finished.path)
	}
	if _, err := os.Stat(finished.path); err != nil {
		t.Fatal(err)
	}
	next, _ = m.applyExportFinished(finished)
	m = next.(Model)
	if m.noticeErr {
		t.Fatalf("notice err: %q", m.notice)
	}
}

func TestHandleExportCommandEmpty(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	next, cmd := m.handleCommand("/export")
	m = next.(Model)
	if cmd != nil {
		t.Fatalf("cmd = %v, want nil", cmd)
	}
	if !m.noticeErr || !strings.Contains(m.notice, "empty") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestHandleExportCommandOpenFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "open-me.md")
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.cells = []cell{&userCell{text: "x"}}

	_, cmd := m.handleCommand("/export --open " + path)
	if cmd == nil {
		t.Fatal("expected cmd")
	}
	msg := runAppCmd(t, cmd)
	finished, ok := msg.(exportFinishedMsg)
	if !ok {
		t.Fatalf("msg = %T", msg)
	}
	if finished.err != nil || !finished.open || finished.path != path {
		t.Fatalf("finished = %+v", finished)
	}
}

func TestExportFinishedMsgViaUpdate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "via-update.md")
	if err := os.WriteFile(path, []byte("# ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m = updateApp(t, m, exportFinishedMsg{path: path})
	if m.noticeErr || !strings.Contains(m.notice, "exported to") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestExportFinishedErrorViaUpdate(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m = updateApp(t, m, exportFinishedMsg{err: os.ErrPermission})
	if !m.noticeErr || !strings.Contains(m.notice, "export failed") {
		t.Fatalf("notice = %q", m.notice)
	}
}

func TestSummarizeToolOutputTruncates(t *testing.T) {
	var lines []string
	for i := 0; i < exportToolOutputMaxLines+10; i++ {
		lines = append(lines, "line")
	}
	got := summarizeToolOutput(strings.Join(lines, "\n"))
	if !strings.Contains(got, "truncated") {
		t.Fatalf("expected truncation marker: %q", got)
	}
	if countLines(got) > exportToolOutputMaxLines+1 {
		t.Fatalf("still too many lines: %d", countLines(got))
	}
}

func TestApplyExportFinishedOpenReturnsEditorCmd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ed.md")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	// Point EDITOR at a missing binary so resolveEditor still returns a path
	// without requiring PATH tools; launchEditorCmd builds tea.ExecProcess.
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", filepath.Join(dir, "no-such-editor"))
	_, cmd := m.applyExportFinished(exportFinishedMsg{path: path, open: true})
	if cmd == nil {
		t.Fatal("expected editor launch cmd when --open")
	}
}

func TestHandleExportPathEscape(t *testing.T) {
	dir := t.TempDir()
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.cells = []cell{&userCell{text: "x"}}
	next, cmd := m.handleCommand("/export ../escape.md")
	m = next.(Model)
	if cmd != nil {
		t.Fatal("expected no write on escape")
	}
	if !m.noticeErr || !strings.Contains(m.notice, "escapes") {
		t.Fatalf("notice = %q", m.notice)
	}
}
