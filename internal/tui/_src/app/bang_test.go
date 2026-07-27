package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/jonathanung/strike-cli/internal/host"
)

func TestParseBangCommand(t *testing.T) {
	tests := []struct {
		in      string
		wantCmd string
		wantOK  bool
	}{
		{"pwd", "", false},
		{"!pwd", "pwd", true},
		{"!  git status  ", "git status", true},
		{"!", "", true},
		{"!   ", "", true},
		{"/help", "", false},
	}
	for _, tt := range tests {
		cmd, ok := parseBangCommand(tt.in)
		if ok != tt.wantOK || cmd != tt.wantCmd {
			t.Errorf("parseBangCommand(%q) = %q, %v; want %q, %v", tt.in, cmd, ok, tt.wantCmd, tt.wantOK)
		}
	}
}

type fakeShell struct {
	lastCmd string
	res     host.ShellResult
	err     error
	calls   int
}

func (f *fakeShell) Run(_ context.Context, command string) (host.ShellResult, error) {
	f.calls++
	f.lastCmd = command
	out := f.res
	out.Command = command
	if f.err != nil {
		if out.Output == "" {
			out.Output = f.err.Error()
		}
		return out, f.err
	}
	return out, nil
}

func applyCmds(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	for _, msg := range runAllAppCmds(t, cmd) {
		if msg == nil {
			continue
		}
		m = updateApp(t, m, msg)
	}
	return m
}

func TestBangPwdShowsInTranscriptWithoutUserInputOp(t *testing.T) {
	root := t.TempDir()
	sh := &fakeShell{res: host.ShellResult{Output: root + "\n", ExitCode: 0}}
	m, ops := newAppTestModel(nil, nil)
	m.services.Shell = sh
	m.workDir = root
	m = updateApp(t, m, tea.WindowSizeMsg{Width: 80, Height: 24})
	m.composer.SetValue("!pwd")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = applyCmds(t, updated.(Model), cmd)
	if sh.calls != 1 || sh.lastCmd != "pwd" {
		t.Fatalf("shell calls=%d cmd=%q, want 1 pwd", sh.calls, sh.lastCmd)
	}
	assertNoAppOp(t, ops)
	if got := m.composer.Value(); got != "" {
		t.Fatalf("composer still has %q", got)
	}
	var sawUser, sawBash bool
	for _, c := range m.cells {
		switch cell := c.(type) {
		case *userCell:
			if cell.text == "!pwd" {
				sawUser = true
			}
		case *toolCell:
			if cell.name == "bash" && cell.title == "pwd" && cell.done {
				sawBash = true
				if strings.TrimSpace(cell.output) != root {
					t.Errorf("bash output = %q, want %q", cell.output, root)
				}
				if cell.isError {
					t.Error("pwd marked error")
				}
			}
		}
	}
	if !sawUser || !sawBash {
		t.Fatalf("cells missing user/bash: user=%v bash=%v cells=%d", sawUser, sawBash, len(m.cells))
	}
}

func TestBangEmptyShowsNotice(t *testing.T) {
	m, ops := newAppTestModel(nil, nil)
	m.services.Shell = &fakeShell{}
	m.composer.SetValue("!")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice, "empty") {
		t.Fatalf("notice = %q, want empty command hint", m.notice)
	}
	if !m.noticeErr {
		t.Error("want noticeErr")
	}
	assertNoAppOp(t, ops)
	if len(m.cells) != 0 {
		t.Fatalf("cells = %d, want 0", len(m.cells))
	}
}

func TestBangUnavailableShell(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.Shell = nil
	m.composer.SetValue("!pwd")
	m = updateApp(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.notice, "unavailable") {
		t.Fatalf("notice = %q, want unavailable", m.notice)
	}
}

func TestBangSandboxErrorMarksToolError(t *testing.T) {
	sh := &fakeShell{
		err: bangTestErr(`destructive command path "/tmp/x" escapes workspace root "/ws"`),
		res: host.ShellResult{
			Output: `destructive command path "/tmp/x" escapes workspace root "/ws"`,
		},
	}
	m, ops := newAppTestModel(nil, nil)
	m.services.Shell = sh
	m.composer.SetValue("!rm -rf /tmp/x")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = applyCmds(t, updated.(Model), cmd)
	assertNoAppOp(t, ops)
	var tc *toolCell
	for _, c := range m.cells {
		if tcell, ok := c.(*toolCell); ok {
			tc = tcell
		}
	}
	if tc == nil || !tc.done || !tc.isError {
		t.Fatalf("tool cell = %#v, want done error", tc)
	}
	if !strings.Contains(tc.output, "escapes workspace") {
		t.Fatalf("output = %q", tc.output)
	}
}

func TestBangDoesNotRequireProvider(t *testing.T) {
	sh := &fakeShell{res: host.ShellResult{Output: "ok\n", ExitCode: 0}}
	m, ops := newAppTestModel(nil, nil)
	m.services.Shell = sh
	m.providerName = ""
	m.composer.SetValue("!echo ok")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_ = applyCmds(t, updated.(Model), cmd)
	assertNoAppOp(t, ops)
	if sh.calls != 1 {
		t.Fatalf("calls = %d", sh.calls)
	}
}

type bangTestErr string

func (e bangTestErr) Error() string { return string(e) }
