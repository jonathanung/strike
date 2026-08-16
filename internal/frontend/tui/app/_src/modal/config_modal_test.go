package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
)

func TestParseConfigCommandArgs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		args      []string
		nano      bool
		scope     host.ConfigFileScope
		slot      string
		wantErr   bool
		errSubstr string
	}{
		{args: nil},
		{args: []string{"nano"}, nano: true},
		{args: []string{"mcp"}, slot: "mcp"},
		{args: []string{"global", "keybinds"}, scope: host.ConfigScopeGlobal, slot: "keybinds"},
		{args: []string{"project", "config"}, scope: host.ConfigScopeProject, slot: "config"},
		{args: []string{"nano", "global", "providers"}, nano: true, scope: host.ConfigScopeGlobal, slot: "providers"},
		{args: []string{"nope"}, wantErr: true, errSubstr: "unknown config slot"},
		{args: []string{"mcp", "providers"}, wantErr: true, errSubstr: "usage"},
	}
	for _, tt := range tests {
		nano, scope, slot, err := parseConfigCommandArgs(tt.args)
		if tt.wantErr {
			if err == nil {
				t.Fatalf("args %v: want error", tt.args)
			}
			if tt.errSubstr != "" && !strings.Contains(err.Error(), tt.errSubstr) {
				t.Fatalf("args %v: err = %v, want substr %q", tt.args, err, tt.errSubstr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("args %v: %v", tt.args, err)
		}
		if nano != tt.nano || scope != tt.scope || slot != tt.slot {
			t.Fatalf("args %v: got nano=%v scope=%q slot=%q want nano=%v scope=%q slot=%q",
				tt.args, nano, scope, slot, tt.nano, tt.scope, tt.slot)
		}
	}
}

func TestFindConfigRefPrefersGlobal(t *testing.T) {
	t.Parallel()
	refs := []host.ConfigFileRef{
		{Slot: "mcp", Scope: host.ConfigScopeGlobal, Path: "/g/mcp.jsonc"},
		{Slot: "mcp", Scope: host.ConfigScopeProject, Path: "/p/mcp.jsonc"},
	}
	got, ok := findConfigRef(refs, "", "mcp")
	if !ok || got.Scope != host.ConfigScopeGlobal {
		t.Fatalf("got %#v ok=%v", got, ok)
	}
	got, ok = findConfigRef(refs, host.ConfigScopeProject, "mcp")
	if !ok || got.Scope != host.ConfigScopeProject {
		t.Fatalf("project got %#v ok=%v", got, ok)
	}
	_, ok = findConfigRef(refs, "", "nope")
	if ok {
		t.Fatal("expected miss")
	}
}

func TestConfigModalFilterAndOpen(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	fc := &fakeConfigFiles{
		refs: []host.ConfigFileRef{
			{Slot: "config", Scope: host.ConfigScopeGlobal, Label: "Main config", Path: "/tmp/g/config", Display: "~/.strike/config", Exists: true, CanCreate: true},
			{Slot: "mcp", Scope: host.ConfigScopeGlobal, Label: "MCP servers", Path: "/tmp/g/mcp.jsonc", Display: "~/.strike/mcp.jsonc", Exists: false, CanCreate: true},
			{Slot: "config", Scope: host.ConfigScopeProject, Label: "Main config", Path: "/tmp/p/config", Display: "./.strike/config", Exists: false, CanCreate: true},
			{Kind: "agents", Scope: host.ConfigScopeGlobal, Label: "agents/commit.md", Path: "/tmp/g/agents/commit.md", Display: "~/.strike/agents/commit.md", Exists: true},
		},
	}
	m.services.ConfigFiles = fc

	cm := newConfigModal(m.services, m.ops, m.th, m.workDir, false, false)
	view := ansi.Strip(cm.view(80, m.th))
	for _, want := range []string{"config files", "Global", "Project", "Main config", "mcp", "agents/commit.md", "missing"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "auth.json") {
		t.Fatalf("auth.json leaked into picker:\n%s", view)
	}

	// Filter to mcp
	next, _ := cm.update(tea.KeyPressMsg{Text: "m"})
	cm = next.(*configModal)
	// "m" alone may match Main + mcp — type more
	cm.filter = "mcp"
	cm.refilter()
	if len(cm.filtered) != 1 || cm.filtered[0].Slot != "mcp" {
		t.Fatalf("filtered = %#v", cm.filtered)
	}

	_, cmd := cm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected open cmd")
	}
	msg := cmd()
	open, ok := msg.(configFileOpenMsg)
	if !ok || open.ref.Slot != "mcp" || open.forceNano {
		t.Fatalf("open msg = %#v", msg)
	}

	cmd = cm.openSelectedCmd(true)
	open = cmd().(configFileOpenMsg)
	if !open.forceNano || open.ref.Slot != "mcp" {
		t.Fatalf("nano open = %#v", open)
	}
}

func TestConfigModalReturnToSettings(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.ConfigFiles = &fakeConfigFiles{refs: []host.ConfigFileRef{
		{Slot: "config", Scope: host.ConfigScopeGlobal, Label: "Main config", Path: "/x", Exists: true, CanCreate: true},
	}}
	cm := newConfigModal(m.services, m.ops, m.th, m.workDir, false, true)
	next, _ := cm.update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if _, ok := next.(*settingsModal); !ok {
		t.Fatalf("esc return = %T, want settingsModal", next)
	}
}

func TestConfigCommandOpensPicker(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.ConfigFiles = &fakeConfigFiles{refs: []host.ConfigFileRef{
		{Slot: "config", Scope: host.ConfigScopeGlobal, Label: "Main config", Path: "/x", Display: "~/.strike/config", Exists: true, CanCreate: true},
	}}
	m.composer.SetValue("/config")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := m.modal.(*configModal); !ok {
		t.Fatalf("modal = %T", m.modal)
	}
}

func TestConfigCommandDirectSlot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newAppTestModel(nil, nil)
	fc := &fakeConfigFiles{
		refs: []host.ConfigFileRef{
			{Slot: "config", Scope: host.ConfigScopeGlobal, Label: "Main config", Path: path, Display: "~/.strike/config", Exists: true, CanCreate: true},
		},
		ensurePath: path,
	}
	m.services.ConfigFiles = fc
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	t.Setenv("PATH", dir) // no nvim/vim/nano

	m.composer.SetValue("/config config")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if len(fc.ensured) != 1 || fc.ensured[0].Slot != "config" {
		t.Fatalf("ensured = %#v", fc.ensured)
	}
}

func TestConfigCommandUnknownSlot(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.ConfigFiles = &fakeConfigFiles{}
	m.composer.SetValue("/config nope")
	m = updateApp(t, m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if !m.noticeErr || !strings.Contains(m.notice, "unknown") {
		t.Fatalf("notice = %q err=%v", m.notice, m.noticeErr)
	}
}

func TestConfigOpenForcesOverlayMode(t *testing.T) {
	dir := t.TempDir()
	nano := filepath.Join(dir, "nano")
	if err := os.WriteFile(nano, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config")
	if err := os.WriteFile(cfgPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")

	m, _ := newAppTestModel(nil, nil)
	m.width, m.height = 120, 40
	m.vimMode = VimModePane
	m.services.ConfigFiles = &fakeConfigFiles{
		refs: []host.ConfigFileRef{
			{Slot: "config", Scope: host.ConfigScopeGlobal, Path: cfgPath, Display: "config", Exists: true, CanCreate: true},
		},
		ensurePath: cfgPath,
	}

	next, _ := m.openConfigFileRef(host.ConfigFileRef{
		Slot: "config", Scope: host.ConfigScopeGlobal, Path: cfgPath, Display: "config", Exists: true, CanCreate: true,
	}, true)
	nm := next.(Model)
	if nm.vimMode != VimModePane {
		t.Fatalf("session vimMode mutated to %q", nm.vimMode)
	}
	if _, ok := nm.modal.(*terminalModal); ok {
		return
	}
	if nm.notice == "" && nm.modal == nil {
		t.Fatal("expected overlay modal or notice after config open")
	}
}

func TestSettingsMenuOpenConfigFiles(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.ConfigFiles = &fakeConfigFiles{refs: []host.ConfigFileRef{
		{Slot: "config", Scope: host.ConfigScopeGlobal, Label: "Main config", Path: "/x", Exists: true, CanCreate: true},
	}}
	sm := newSettingsModal(m.services, m.ops, m.th, m.workDir)
	view := ansi.Strip(sm.view(80, m.th))
	if !strings.Contains(view, "Open config files") {
		t.Fatalf("menu missing open config:\n%s", view)
	}
	sm.cursor = int(settingsMenuOpenConfig)
	next, _ := sm.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if _, ok := next.(*configModal); !ok {
		t.Fatalf("next = %T", next)
	}
	cm := next.(*configModal)
	if !cm.returnToSettings {
		t.Fatal("expected returnToSettings")
	}
}

func TestFinishEditorSessionConfigReloadKeybinds(t *testing.T) {
	dir := t.TempDir()
	strike := filepath.Join(dir, ".strike")
	if err := os.MkdirAll(strike, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(strike, "keybinds.jsonc")
	if err := os.WriteFile(path, []byte(`{"nav.jump-bottom":["ctrl+b"]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotFile(path)
	if err := os.WriteFile(path, []byte(`{"nav.jump-bottom":["ctrl+x"]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.services.ConfigFiles = &fakeConfigFiles{
		keybinds: map[string][]string{"nav.jump-bottom": {"ctrl+x"}},
	}
	next, _ := m.finishEditorSession(path, ".strike/keybinds.jsonc", before, true, nil)
	nm := next.(Model)
	if !strings.Contains(nm.notice, "edited") || !strings.Contains(nm.notice, "keybinds") {
		t.Fatalf("notice = %q", nm.notice)
	}
	if got := nm.keyOverrides["nav.jump-bottom"]; len(got) != 1 || got[0] != "ctrl+x" {
		t.Fatalf("keyOverrides = %#v", nm.keyOverrides)
	}
}

func TestReloadAfterConfigEditIgnoresNonStrikeBasename(t *testing.T) {
	dir := t.TempDir()
	// Ordinary project file named "config" must not trigger presentation reload.
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := newAppTestModel(nil, nil)
	m.workDir = dir
	m.vimMode = VimModePane
	if extra := m.reloadAfterConfigEdit(path); extra != "" {
		t.Fatalf("unexpected reload for non-strike path: %q", extra)
	}
	if m.vimMode != VimModePane {
		t.Fatalf("vimMode mutated")
	}
}

func TestConfigModalPathAllowlistNoAuth(t *testing.T) {
	m, _ := newAppTestModel(nil, nil)
	m.services.ConfigFiles = &fakeConfigFiles{refs: []host.ConfigFileRef{
		{Slot: "config", Scope: host.ConfigScopeGlobal, Label: "Main config", Path: "/ok", Exists: true, CanCreate: true},
	}}
	cm := newConfigModal(m.services, m.ops, m.th, "", false, false)
	for _, e := range cm.entries {
		if strings.Contains(strings.ToLower(e.Path), "auth.json") || strings.Contains(e.Label, "auth") {
			t.Fatalf("auth leaked: %#v", e)
		}
	}
}

func TestCommandCatalogIncludesConfig(t *testing.T) {
	catalog := commandCatalog(nil)
	found := false
	for _, c := range catalog {
		if c.Name == "/config" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("command catalog missing /config")
	}
}
