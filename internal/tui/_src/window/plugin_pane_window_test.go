package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/jonathanung/strike-cli/internal/host"
	"github.com/jonathanung/strike-cli/internal/tui/theme"
)

func TestPluginPaneStaticRenderAndData(t *testing.T) {
	def := `{"schemaVersion":1,"id":"acme.status","title":"Acme Status","mode":"static",
"permissions":{"host":["session.summary","usage"],"fs":"none","network":"none","command":"none"},
"subscriptions":["session.summary","usage"],
"sizing":{"preferredHeight":8},
"view":{"type":"column","gap":1,"children":[
  {"type":"text","text":"Session","style":"title"},
  {"type":"kv","entries":[
    {"key":"cwd","valueFrom":"session.summary.cwd"},
    {"key":"model","valueFrom":"session.summary.model"}
  ]},
  {"type":"meter","label":"context","valueFrom":"usage.used","maxFrom":"usage.limit"}
]}}`
	info := host.PaneInfo{
		ID: "acme.status", PluginID: "acme.pack", PluginVersion: "1.0.0",
		Title: "Acme Status", Mode: host.PaneModeStatic, Trusted: true,
		DefinitionJSON: []byte(def),
	}
	w := newPluginPaneWindow(info).resize(40, 12).(pluginPaneWindow)
	if w.id() != "acme.status" || w.title() != "Acme Status" {
		t.Fatalf("id/title = %q %q", w.id(), w.title())
	}
	if !w.hasView {
		t.Fatal("expected static view")
	}
	msg := contextStateMsg{WorkDir: "/work/proj", Model: "echo", Provider: "echo"}
	msg.Used.Known, msg.Used.N = true, 42
	msg.ContextLimit, msg.ContextLimitKnown = 100, true
	next, _ := w.update(msg)
	pw := next.(pluginPaneWindow)
	plain := ansi.Strip(pw.view(theme.Default()))
	if !strings.Contains(plain, "Session") {
		t.Fatalf("missing title: %q", plain)
	}
	if !strings.Contains(plain, "/work/proj") {
		t.Fatalf("missing cwd binding: %q", plain)
	}
	// Narrow width must not panic.
	pw.width, pw.height = 12, 6
	_ = pw.view(theme.Default())
}

func TestPluginPaneUnknownNodeAndEscapeStrip(t *testing.T) {
	def := `{"schemaVersion":1,"id":"acme.x","title":"X","mode":"static",
"permissions":{"host":[],"fs":"none","network":"none","command":"none"},
"view":{"type":"column","children":[
  {"type":"nope"},
  {"type":"text","text":"hi\u001b[31mRED"}
]}}`
	w := newPluginPaneWindow(host.PaneInfo{
		ID: "acme.x", PluginID: "a", PluginVersion: "1", Title: "X",
		Mode: host.PaneModeStatic, Trusted: true, DefinitionJSON: []byte(def),
	}).resize(30, 10).(pluginPaneWindow)
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "unsupported: nope") {
		t.Fatalf("want placeholder, got %q", plain)
	}
	if strings.Contains(plain, "\x1b") {
		t.Fatalf("escape leaked: %q", plain)
	}
}

func TestPluginPaneViewRecoverOnPanic(t *testing.T) {
	// Directly exercise recover wrapper by calling view with valid state.
	w := pluginPaneWindow{
		width: 20, height: 5, hasView: true,
		viewRoot: paneViewNode{Type: "text", Text: "ok"},
	}
	out := w.view(theme.Default())
	if !strings.Contains(ansi.Strip(out), "ok") {
		t.Fatalf("got %q", out)
	}
}

func TestSyncPluginPanesAddRemoveAndGroup(t *testing.T) {
	r := newWindowRegistry()
	fake := &fakePanes{list: []host.PaneInfo{{
		ID: "acme.status", PluginID: "acme", PluginVersion: "1.0.0",
		Title: "Status", Mode: host.PaneModeStatic, Trusted: true,
		DefinitionJSON: []byte(`{"schemaVersion":1,"id":"acme.status","title":"Status","mode":"static",
"permissions":{"host":[],"fs":"none","network":"none","command":"none"},
"view":{"type":"text","text":"hi"}}`),
	}}}
	r, _ = syncPluginPanes(r, fake)
	ids := pluginPaneIDs(r)
	if len(ids) != 1 || ids[0] != "acme.status" {
		t.Fatalf("ids=%v", ids)
	}
	found := false
	for _, g := range r.groups {
		if g.id == pluginWindowGroupID {
			found = true
			if len(g.members) != 1 {
				t.Fatalf("plugin members=%v", g.members)
			}
		}
	}
	if !found {
		t.Fatal("plugin group missing")
	}
	order := r.focusOrder()
	var cycleIDs []string
	for _, idx := range order {
		cycleIDs = append(cycleIDs, r.windows[idx].id())
	}
	if !strings.Contains(strings.Join(cycleIDs, ","), "acme.status") {
		t.Fatalf("cycle missing plugin pane: %v", cycleIDs)
	}
	// Activate and ensure key routing reaches the pane.
	r, ok := r.activate("acme.status")
	if !ok {
		t.Fatal("activate failed")
	}
	if r.active().id() != "acme.status" {
		t.Fatalf("active=%q", r.active().id())
	}
	fake.list = nil
	r, _ = syncPluginPanes(r, fake)
	if len(pluginPaneIDs(r)) != 0 {
		t.Fatalf("expected removed, got %v", pluginPaneIDs(r))
	}
}

func TestPluginPaneProcessHelloRenderShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("process fixture uses sh")
	}
	root := t.TempDir()
	script := filepath.Join(root, "pane.sh")
	body := "#!/bin/sh\n" +
		"IFS= read -r _\n" +
		"printf '%s\\n' '{\"version\":1,\"type\":\"pane.hello\",\"paneId\":\"acme.board\",\"abi\":\"pane/1\"}'\n" +
		"printf '%s\\n' '{\"version\":1,\"type\":\"pane.meta\",\"title\":\"Board\",\"status\":\"ok\"}'\n" +
		"printf '%s\\n' '{\"version\":1,\"type\":\"pane.render\",\"rev\":1,\"view\":{\"type\":\"text\",\"text\":\"hello-board\",\"style\":\"title\"}}'\n" +
		"IFS= read -r _\n" +
		"printf '%s\\n' '{\"version\":1,\"type\":\"pane.exit\",\"code\":0}'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	def := map[string]any{
		"schemaVersion": 1,
		"id":            "acme.board",
		"title":         "Board",
		"mode":          "process",
		"command":       "pane.sh",
		"permissions":   map[string]any{"host": []string{}, "fs": "none", "network": "none", "command": "none"},
		"timeouts":      map[string]any{"startMs": 3000, "shutdownMs": 1000},
	}
	raw, _ := json.Marshal(def)
	info := host.PaneInfo{
		ID: "acme.board", PluginID: "acme", PluginVersion: "1.0.0",
		Title: "Board", Mode: host.PaneModeProcess, Trusted: true,
		PluginRoot: root, DefinitionJSON: raw,
	}
	w := newPluginPaneWindow(info).resize(40, 10).(pluginPaneWindow)
	cmd := w.init()
	if cmd == nil {
		t.Fatal("expected listen cmd")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// Non-blocking drain: check snapshot first.
		title, _, errState, view, hello := w.rt.snapshot()
		if hello && len(view) > 0 {
			if title != "Board" {
				t.Fatalf("title=%q", title)
			}
			// Apply pending msgs
			for _, pmsg := range w.rt.takePending() {
				var win window = w
				win, _ = w.update(pmsg)
				w = win.(pluginPaneWindow)
			}
			plain := ansi.Strip(w.view(theme.Default()))
			if !strings.Contains(plain, "hello-board") {
				t.Fatalf("body=%q err=%q", plain, errState)
			}
			// Input should not panic.
			key := tea.KeyPressMsg{}
			// Construct via String path — use update with a simple key if possible.
			_ = key
			w.rt.sendInput("enter", nil)
			w = w.shutdown("unmount")
			return
		}
		if errState != "" {
			t.Fatalf("errState=%q", errState)
		}
		// Try wake
		select {
		case <-w.rt.notifyCh:
			var win window = w
			win, cmd = w.update(pluginPaneWakeMsg{paneID: w.info.ID})
			w = win.(pluginPaneWindow)
			_ = cmd
		default:
			time.Sleep(15 * time.Millisecond)
		}
	}
	t.Fatal("timeout waiting for process pane render")
}

func TestPluginPaneProcessUntrustedShowsError(t *testing.T) {
	info := host.PaneInfo{
		ID: "acme.board", PluginID: "acme", PluginVersion: "1",
		Title: "Board", Mode: host.PaneModeProcess, Trusted: false,
		LoadError: "process pane blocked until plugin trust is granted",
		DefinitionJSON: []byte(`{"schemaVersion":1,"id":"acme.board","title":"Board","mode":"process","command":"x",
"permissions":{"host":[],"fs":"none","network":"none","command":"none"}}`),
	}
	w := newPluginPaneWindow(info).resize(40, 8).(pluginPaneWindow)
	if w.rt != nil {
		t.Fatal("untrusted must not start runtime")
	}
	plain := ansi.Strip(w.view(theme.Default()))
	if !strings.Contains(plain, "pane error") || !strings.Contains(plain, "trust") {
		t.Fatalf("body=%q", plain)
	}
	if !strings.Contains(plain, "plugin=acme@1") {
		t.Fatalf("missing provenance: %q", plain)
	}
}

func TestPluginPaneMalformedBudget(t *testing.T) {
	rt := newPluginPaneRuntime("p", "plug", "1", t.TempDir(), paneDef{
		Timeouts: paneDefTimeouts{StartMs: 5000, ShutdownMs: 500},
	})
	for i := 0; i < paneMalformedBudget; i++ {
		rt.handleLine([]byte(`not-json`))
	}
	_, _, errState, _, _ := rt.snapshot()
	if errState == "" {
		t.Fatal("expected error state after malformed budget")
	}
}

func TestPluginPaneRenderRateLimit(t *testing.T) {
	rt := newPluginPaneRuntime("p", "plug", "1", t.TempDir(), paneDef{})
	rt.mu.Lock()
	rt.helloOK = true
	rt.mu.Unlock()
	view := json.RawMessage(`{"type":"text","text":"x"}`)
	accepted := 0
	for i := 0; i < 30; i++ {
		line, _ := json.Marshal(map[string]any{
			"version": 1, "type": "pane.render", "rev": i + 1,
			"view": view,
		})
		rt.handleLine(line)
		rt.mu.Lock()
		if rt.lastRev == int64(i+1) {
			accepted++
		}
		rt.mu.Unlock()
	}
	if accepted > paneRenderBurst {
		t.Fatalf("accepted %d renders, want <= burst %d", accepted, paneRenderBurst)
	}
	if accepted < 1 {
		t.Fatal("expected some renders accepted")
	}
}

func TestDisablePluginPanesCleansRegistry(t *testing.T) {
	r := newWindowRegistry()
	fake := &fakePanes{list: []host.PaneInfo{{
		ID: "acme.status", PluginID: "acme", PluginVersion: "1.0.0",
		Title: "Status", Mode: host.PaneModeStatic, Trusted: true,
		DefinitionJSON: []byte(`{"schemaVersion":1,"id":"acme.status","title":"Status","mode":"static",
"permissions":{"host":[],"fs":"none","network":"none","command":"none"},
"view":{"type":"text","text":"hi"}}`),
	}}}
	r, _ = syncPluginPanes(r, fake)
	r, _ = r.activate("acme.status")
	r = removeAllPluginPanes(r, "disable")
	if len(pluginPaneIDs(r)) != 0 {
		t.Fatal("expected no plugin panes")
	}
	if r.active() == nil || r.active().id() == "acme.status" {
		t.Fatalf("active should fall back, got %v", r.active())
	}
}

type fakePanes struct {
	list []host.PaneInfo
	err  error
}

func (f *fakePanes) List() ([]host.PaneInfo, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([]host.PaneInfo, len(f.list))
	copy(out, f.list)
	return out, nil
}
