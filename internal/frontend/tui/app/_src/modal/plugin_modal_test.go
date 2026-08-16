package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/frontend/host"
	"github.com/jonathanung/strike-cli/internal/frontend/tui/theme"
)

type fakePlugins struct {
	list    []host.PluginInfo
	err     error
	enable  []string
	disable []string
	remove  []string
	trust   []string
	untrust []string
	// confirm gates
	lastRemoveConfirm bool
	lastUpdateConfirm bool
	trustPrev         host.PluginTrustPreview
	updateRev         host.PluginUpdateReview
	catalog           []host.PluginCatalogHit
}

func (f *fakePlugins) List() ([]host.PluginInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]host.PluginInfo, len(f.list))
	copy(out, f.list)
	return out, nil
}
func (f *fakePlugins) Inspect(id, scope string) (host.PluginInfo, error) {
	for _, p := range f.list {
		if p.ID == id && (scope == "" || p.Scope == scope) {
			return p, nil
		}
	}
	return host.PluginInfo{}, errors.New("not found")
}
func (f *fakePlugins) Enable(id, scope string) error {
	f.enable = append(f.enable, id)
	for i := range f.list {
		if f.list[i].ID == id {
			f.list[i].Enabled = true
			f.list[i].Status = "enabled"
		}
	}
	return nil
}
func (f *fakePlugins) Disable(id, scope string) error {
	f.disable = append(f.disable, id)
	for i := range f.list {
		if f.list[i].ID == id {
			f.list[i].Enabled = false
			f.list[i].Status = "disabled"
		}
	}
	return nil
}
func (f *fakePlugins) Remove(id, scope string, confirm bool) error {
	f.lastRemoveConfirm = confirm
	if !confirm {
		return errors.New("remove requires confirmation")
	}
	f.remove = append(f.remove, id)
	out := f.list[:0]
	for _, p := range f.list {
		if p.ID != id {
			out = append(out, p)
		}
	}
	f.list = out
	return nil
}
func (f *fakePlugins) TrustPreview(id, scope string) (host.PluginTrustPreview, error) {
	if f.trustPrev.ID != "" {
		return f.trustPrev, nil
	}
	return host.PluginTrustPreview{
		ID:           id,
		Scope:        scope,
		Capabilities: []string{"mcp.stdio"},
		MCP:          []host.PluginMCP{{Name: "demo", Command: "bin/server", EnvKeys: []string{"TOKEN"}}},
		ReviewLines: []string{
			"Grant executable trust for " + id + "?",
			"capabilities: mcp.stdio",
			"MCP servers (1) — contribution type: mcp:",
			"  - demo command: bin/server (env keys: TOKEN)",
		},
	}, nil
}
func (f *fakePlugins) Trust(id, scope string) error {
	f.trust = append(f.trust, id)
	for i := range f.list {
		if f.list[i].ID == id {
			f.list[i].TrustState = host.PluginTrustTrusted
		}
	}
	return nil
}
func (f *fakePlugins) Untrust(id, scope string) error {
	f.untrust = append(f.untrust, id)
	return nil
}
func (f *fakePlugins) Search(ctx context.Context, registry, query string) ([]host.PluginCatalogHit, error) {
	return append([]host.PluginCatalogHit(nil), f.catalog...), nil
}
func (f *fakePlugins) Install(ctx context.Context, source, scope, registry string) (host.PluginInstallResult, error) {
	return host.PluginInstallResult{ID: "new.plug", Version: "1.0.0", Scope: scope, Enabled: true}, nil
}
func (f *fakePlugins) CheckOutdated(ctx context.Context, registry string) ([]host.PluginInfo, error) {
	return nil, nil
}
func (f *fakePlugins) PreviewUpdate(ctx context.Context, id, scope, registry string) (host.PluginUpdateReview, error) {
	if f.updateRev.ID != "" {
		return f.updateRev, nil
	}
	return host.PluginUpdateReview{
		ID:         id,
		OldVersion: "1.0.0",
		NewVersion: "1.1.0",
		Summary:    "Update review: " + id + "\n  version:  1.0.0 → 1.1.0\n",
	}, nil
}
func (f *fakePlugins) Update(ctx context.Context, id, scope, registry string, confirm bool) (host.PluginInstallResult, error) {
	f.lastUpdateConfirm = confirm
	if !confirm {
		return host.PluginInstallResult{}, errors.New("update requires confirmation")
	}
	return host.PluginInstallResult{ID: id, Version: "1.1.0", Scope: scope, Enabled: true}, nil
}

func samplePlugins() []host.PluginInfo {
	return []host.PluginInfo{
		{
			ID: "acme.ui", Version: "1.0.0", Name: "acme.ui", DisplayName: "UI Pack",
			Format: "agent-plugins", Schema: "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
			Scope:   host.PluginScopeGlobal,
			Enabled: true, Status: "enabled", TrustState: host.PluginTrustPassiveOnly,
			Skills: 2, SourceType: "local", SourceLabel: "local:/tmp/x",
		},
		{
			ID: "acme.exec", Version: "2.0.0", Name: "acme.exec", Format: "agent-plugins",
			Scope:   host.PluginScopeProject,
			Enabled: true, Status: "enabled", TrustState: host.PluginTrustNone,
			HasExecutable: true, Capabilities: []string{"mcp.stdio"},
			MCP:        []host.PluginMCP{{Name: "demo", Command: "bin/server", EnvKeys: []string{"SECRET"}}},
			SourceType: "catalog", SourceLabel: "catalog:https://example.com#acme.exec@2.0.0",
		},
		{
			ID: "acme.legacy", Version: "1.0.0", Name: "Legacy Pack", Format: "legacy",
			Scope:   host.PluginScopeGlobal,
			Enabled: true, Status: "enabled", TrustState: host.PluginTrustPassiveOnly,
			Agents: 1, SourceType: "local", SourceLabel: "local:/tmp/legacy",
		},
	}
}

func TestPluginModalBrowseAndDetail(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	if m.phase != pluginPhaseBrowse || len(m.all) != 3 {
		t.Fatalf("init: phase=%v n=%d", m.phase, len(m.all))
	}
	// Plain letters filter; actions use ctrl+ chords.
	nm, _ := m.update(keyMsg("e"))
	m = nm.(*pluginModal)
	if m.filter != "e" || m.phase != pluginPhaseBrowse {
		t.Fatalf("plain e should filter, got filter=%q phase=%v", m.filter, m.phase)
	}
	m.filter = ""
	m.cursor = 0
	nm, _ = m.update(ctrlKey('e'))
	m = nm.(*pluginModal)
	// first plugin is enabled — ctrl+e toggles to disable confirm
	if m.phase != pluginPhaseConfirm || m.confirmKind != pluginConfirmDisable {
		t.Fatalf("phase=%v kind=%v", m.phase, m.confirmKind)
	}
	// cancel
	nm, _ = m.update(keyMsg("esc"))
	m = nm.(*pluginModal)
	if m.phase != pluginPhaseBrowse {
		t.Fatalf("after cancel phase=%v", m.phase)
	}

	// Move to second and open detail
	nm, _ = m.update(keyMsg("down"))
	m = nm.(*pluginModal)
	nm, _ = m.update(keyMsg("enter"))
	m = nm.(*pluginModal)
	if m.phase != pluginPhaseDetail {
		t.Fatalf("detail phase=%v", m.phase)
	}
	view := m.view(72, theme.Default())
	if strings.Contains(view, "should-not") {
		t.Fatal("unexpected secret-like content")
	}
	// Env values never present; keys may appear in detail
	if strings.Contains(view, "SECRET=") {
		t.Fatal("env value rendered")
	}
	plain := stripANSI(view)
	if !strings.Contains(plain, "acme.exec") {
		t.Fatalf("detail missing id:\n%s", plain)
	}
}

func TestPluginModalRemoveRequiresConfirm(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	nm, _ := m.update(ctrlKey('x'))
	m = nm.(*pluginModal)
	if m.phase != pluginPhaseConfirm || m.confirmKind != pluginConfirmRemove {
		t.Fatalf("phase=%v kind=%v", m.phase, m.confirmKind)
	}
	// Confirm
	nm, cmd := m.update(keyMsg("y"))
	m = nm.(*pluginModal)
	if m.phase != pluginPhaseBusy || cmd == nil {
		t.Fatalf("expected busy+cmd phase=%v", m.phase)
	}
	msg := cmd()
	done, ok := msg.(pluginOpDoneMsg)
	if !ok || done.err != nil || done.kind != "remove" {
		t.Fatalf("msg=%T %+v", msg, msg)
	}
	if !fp.lastRemoveConfirm {
		t.Fatal("remove called without confirm=true")
	}
	m.applyMsg(done)
	if len(fp.list) != 2 {
		t.Fatalf("list after remove: %+v", fp.list)
	}
}

func TestPluginModalTrustShowsCapabilityReview(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	// select exec plugin
	nm, _ := m.update(keyMsg("down"))
	m = nm.(*pluginModal)
	nm, cmd := m.update(ctrlKey('t'))
	m = nm.(*pluginModal)
	if m.phase != pluginPhaseBusy || cmd == nil {
		t.Fatalf("phase=%v", m.phase)
	}
	msg := cmd()
	m.applyMsg(msg)
	if m.phase != pluginPhaseConfirm || m.confirmKind != pluginConfirmTrust {
		t.Fatalf("phase=%v kind=%v status=%q", m.phase, m.confirmKind, m.status)
	}
	body := strings.Join(m.confirmBody, "\n")
	if !strings.Contains(body, "bin/server") {
		t.Fatalf("missing command:\n%s", body)
	}
	if !strings.Contains(body, "mcp") {
		t.Fatalf("missing contrib type:\n%s", body)
	}
	if strings.Contains(body, "should-not-leak") {
		t.Fatal("secret in trust review")
	}
	// confirm trust
	nm, cmd = m.update(keyMsg("y"))
	m = nm.(*pluginModal)
	msg = cmd()
	m.applyMsg(msg)
	if len(fp.trust) != 1 || fp.trust[0] != "acme.exec" {
		t.Fatalf("trust calls: %v", fp.trust)
	}
}

func TestPluginModalUpdateRequiresConfirm(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	nm, _ := m.update(keyMsg("down"))
	m = nm.(*pluginModal)
	nm, cmd := m.update(keyMsg("U"))
	m = nm.(*pluginModal)
	if cmd == nil {
		t.Fatal("expected preview cmd")
	}
	m.applyMsg(cmd())
	if m.phase != pluginPhaseConfirm || m.confirmKind != pluginConfirmUpdate {
		t.Fatalf("phase=%v kind=%v status=%q", m.phase, m.confirmKind, m.status)
	}
	nm, cmd = m.update(keyMsg("y"))
	m = nm.(*pluginModal)
	msg := cmd()
	done := msg.(pluginOpDoneMsg)
	if !fp.lastUpdateConfirm {
		t.Fatal("update without confirm")
	}
	m.applyMsg(done)
	if done.err != nil {
		t.Fatal(done.err)
	}
}

func TestPluginModalNarrowWidth(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	// Must not panic at constrained sizes.
	for _, w := range []int{20, 40, 60, 80} {
		_ = m.view(w, theme.Default())
	}
	nm, _ := m.update(keyMsg("enter"))
	m = nm.(*pluginModal)
	_ = m.view(24, theme.Default())
}

func TestPluginModalNilHost(t *testing.T) {
	m := newPluginModal(nil)
	if m.loadErr == "" {
		t.Fatal("expected load error")
	}
	v := m.view(60, theme.Default())
	if !strings.Contains(stripANSI(v), "unavailable") {
		t.Fatalf("view=%s", stripANSI(v))
	}
}

func TestPluginModalEscCloses(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	nm, _ := m.update(keyMsg("esc"))
	if nm != nil {
		t.Fatalf("want nil close, got %T", nm)
	}
}

func TestPluginModalNoSecretInListView(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	v := stripANSI(m.view(80, theme.Default()))
	if strings.Contains(v, "SECRET=") || strings.Contains(strings.ToLower(v), "password") {
		t.Fatalf("secret-like content:\n%s", v)
	}
}

func keyMsg(s string) tea.KeyPressMsg {
	switch s {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "U":
		return tea.KeyPressMsg{Text: "U", Code: 'U'}
	default:
		if len(s) == 1 {
			r := []rune(s)[0]
			return tea.KeyPressMsg{Text: s, Code: r}
		}
		return tea.KeyPressMsg{Text: s}
	}
}

func ctrlKey(r rune) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl}
}

func TestPluginModalListsAPSByNameAndLegacyDeprecation(t *testing.T) {
	fp := &fakePlugins{list: samplePlugins()}
	m := newPluginModal(fp)
	browse := stripANSI(m.view(72, theme.Default()))
	if !strings.Contains(browse, "acme.ui") {
		t.Fatalf("browse must list APS plugin by name:\n%s", browse)
	}
	if strings.Contains(browse, "UI Pack") {
		t.Fatalf("browse must not prefer displayName over APS name:\n%s", browse)
	}
	if !strings.Contains(browse, "legacy (deprecated)") {
		t.Fatalf("browse missing legacy deprecation:\n%s", browse)
	}

	m.cursor = 0
	nm, _ := m.update(keyMsg("enter"))
	m = nm.(*pluginModal)
	detail := stripANSI(m.view(72, theme.Default()))
	if !strings.Contains(detail, "acme.ui") || !strings.Contains(detail, "agent-plugins") {
		t.Fatalf("APS detail missing identity:\n%s", detail)
	}
	if !strings.Contains(detail, "UI Pack") {
		t.Fatalf("APS detail missing displayName:\n%s", detail)
	}

	nm, _ = m.update(keyMsg("esc"))
	m = nm.(*pluginModal)
	m.cursor = 2
	nm, _ = m.update(keyMsg("enter"))
	m = nm.(*pluginModal)
	legacy := stripANSI(m.view(72, theme.Default()))
	if !strings.Contains(legacy, "legacy (deprecated)") {
		t.Fatalf("legacy detail missing deprecation:\n%s", legacy)
	}
}

func stripANSI(s string) string { return ansi.Strip(s) }
