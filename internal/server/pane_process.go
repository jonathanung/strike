package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jonathanung/strike-cli/internal/host"
)

// Frontend-neutral process pane host for the web cockpit (docs/plugin-panes.md).
// Mirrors TUI isolation budgets without importing TUI packages.

const (
	webPaneABI              = "pane/1"
	webPaneMaxLineBytes     = 256 << 10
	webPaneMaxStdoutLife    = 32 << 20
	webPaneMaxStderrBytes   = 64 << 10
	webPaneRenderRatePerSec = 10
	webPaneRenderBurst      = 20
	webPaneMalformedBudget  = 8
	webPaneMalformedWindow  = 30 * time.Second
	webPaneMaxAutoRestart   = 1
	webPaneMaxNodes         = 512
	webPaneMaxTitleLen      = 40
)

type paneHostState struct {
	Title   string
	Status  string
	Error   string
	View    json.RawMessage
	Rev     int64
	Mounted bool
}

type paneHost struct {
	mu sync.Mutex
	rt map[string]*webPaneRuntime
}

func newPaneHost() *paneHost {
	return &paneHost{rt: map[string]*webPaneRuntime{}}
}

func (h *paneHost) mount(info host.PaneInfo, width, height int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.rt[info.ID]; ok {
		if existing.alive() {
			return nil
		}
		existing.shutdown("remount")
		delete(h.rt, info.ID)
	}
	def, err := parseWebPaneDef(info.DefinitionJSON)
	if err != nil {
		return err
	}
	rt := newWebPaneRuntime(info, def)
	if err := rt.start(width, height); err != nil {
		return err
	}
	h.rt[info.ID] = rt
	return nil
}

func (h *paneHost) unmount(id, reason string) {
	h.mu.Lock()
	rt := h.rt[id]
	delete(h.rt, id)
	h.mu.Unlock()
	if rt != nil {
		rt.shutdown(reason)
	}
}

func (h *paneHost) unmountPlugin(pluginID string) {
	h.mu.Lock()
	var drop []*webPaneRuntime
	for id, rt := range h.rt {
		if rt.pluginID == pluginID {
			drop = append(drop, rt)
			delete(h.rt, id)
		}
	}
	h.mu.Unlock()
	for _, rt := range drop {
		rt.shutdown("plugin-change")
	}
}

func (h *paneHost) snapshot(id string) (paneHostState, bool) {
	h.mu.Lock()
	rt := h.rt[id]
	h.mu.Unlock()
	if rt == nil {
		return paneHostState{}, false
	}
	return rt.snapshot(), true
}

func (h *paneHost) input(id string, event map[string]any) error {
	h.mu.Lock()
	rt := h.rt[id]
	h.mu.Unlock()
	if rt == nil {
		return fmt.Errorf("pane not mounted")
	}
	return rt.send(map[string]any{
		"version": 1,
		"type":    "pane.input",
		"event":   event,
	})
}

func (h *paneHost) resize(id string, width, height int) error {
	h.mu.Lock()
	rt := h.rt[id]
	h.mu.Unlock()
	if rt == nil {
		return fmt.Errorf("pane not mounted")
	}
	if width <= 0 {
		width = 1
	}
	if height <= 0 {
		height = 1
	}
	return rt.send(map[string]any{
		"version": 1,
		"type":    "pane.resize",
		"size":    map[string]int{"width": width, "height": height},
	})
}

func (h *paneHost) pushFeeds(id string, feeds map[string]any) error {
	h.mu.Lock()
	rt := h.rt[id]
	h.mu.Unlock()
	if rt == nil {
		return nil
	}
	return rt.pushFeeds(feeds)
}

// --- definition ---

type webPaneDef struct {
	SchemaVersion int               `json:"schemaVersion"`
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	Mode          string            `json:"mode"`
	Permissions   map[string]any    `json:"permissions"`
	Subscriptions []string          `json:"subscriptions"`
	Command       string            `json:"command"`
	Args          []string          `json:"args"`
	Env           map[string]string `json:"env"`
	Timeouts      struct {
		StartMs    int `json:"startMs"`
		ShutdownMs int `json:"shutdownMs"`
	} `json:"timeouts"`
}

func parseWebPaneDef(data []byte) (webPaneDef, error) {
	var d webPaneDef
	if err := json.Unmarshal(data, &d); err != nil {
		return webPaneDef{}, err
	}
	d.Title = strings.TrimSpace(d.Title)
	d.Mode = strings.TrimSpace(d.Mode)
	d.ID = strings.TrimSpace(d.ID)
	d.Command = strings.TrimSpace(d.Command)
	if d.Timeouts.StartMs <= 0 {
		d.Timeouts.StartMs = 5000
	}
	if d.Timeouts.StartMs > 15000 {
		d.Timeouts.StartMs = 15000
	}
	if d.Timeouts.ShutdownMs <= 0 {
		d.Timeouts.ShutdownMs = 2000
	}
	if d.Timeouts.ShutdownMs > 5000 {
		d.Timeouts.ShutdownMs = 5000
	}
	return d, nil
}

func clampWebPaneTitle(title string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return "plugin"
	}
	runes := []rune(title)
	if len(runes) <= webPaneMaxTitleLen {
		return title
	}
	return string(runes[:webPaneMaxTitleLen])
}

func sanitizeWebPaneText(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\x00", "")
	if len(s) > 4<<10 {
		s = s[:4<<10]
	}
	return s
}

// --- runtime ---

type webPaneRuntime struct {
	mu sync.Mutex

	paneID        string
	pluginID      string
	pluginVersion string
	root          string
	def           webPaneDef

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	helloOK  bool
	mounted  bool
	stopping bool
	restarts int
	gen      int
	lastRev  int64
	view     json.RawMessage
	title    string
	status   string
	errState string
	stdoutN  int64

	malformed []time.Time
	renders   []time.Time
	dead      atomic.Bool
}

func newWebPaneRuntime(info host.PaneInfo, def webPaneDef) *webPaneRuntime {
	env := def.Env
	if env == nil {
		env = map[string]string{}
	}
	// Copy so we can inject STRIKE_PLUGIN_ROOT without mutating shared maps.
	envCopy := make(map[string]string, len(env)+1)
	for k, v := range env {
		envCopy[k] = v
	}
	envCopy["STRIKE_PLUGIN_ROOT"] = info.PluginRoot
	def.Env = envCopy
	return &webPaneRuntime{
		paneID:        info.ID,
		pluginID:      info.PluginID,
		pluginVersion: info.PluginVersion,
		root:          info.PluginRoot,
		def:           def,
		title:         clampWebPaneTitle(def.Title),
	}
}

func (rt *webPaneRuntime) alive() bool {
	return rt != nil && !rt.dead.Load() && rt.mounted
}

func (rt *webPaneRuntime) snapshot() paneHostState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st := paneHostState{
		Title:   rt.title,
		Status:  rt.status,
		Error:   rt.errState,
		Rev:     rt.lastRev,
		Mounted: rt.mounted && !rt.dead.Load(),
	}
	if len(rt.view) > 0 {
		st.View = append(json.RawMessage(nil), rt.view...)
	}
	return st
}

func (rt *webPaneRuntime) start(width, height int) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cmd != nil || rt.stopping {
		return nil
	}
	return rt.startLocked(width, height)
}

func (rt *webPaneRuntime) startLocked(width, height int) error {
	cmdPath := rt.def.Command
	if cmdPath == "" {
		return fmt.Errorf("process pane missing command")
	}
	if !filepath.IsAbs(cmdPath) {
		cmdPath = filepath.Join(rt.root, cmdPath)
	}
	// Path confinement: command must stay under plugin root when relative.
	if rel, err := filepath.Rel(rt.root, cmdPath); err != nil || strings.HasPrefix(rel, "..") {
		// Allow absolute only if still under root after clean.
		cleanRoot := filepath.Clean(rt.root) + string(os.PathSeparator)
		if !strings.HasPrefix(filepath.Clean(cmdPath)+string(os.PathSeparator), cleanRoot) && filepath.Clean(cmdPath) != filepath.Clean(rt.root) {
			return fmt.Errorf("pane command escapes plugin root")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	cmd := exec.CommandContext(ctx, cmdPath, rt.def.Args...)
	cmd.Dir = rt.root
	cmd.Env = webPaneProcessEnv(rt.def.Env, rt.paneID, rt.pluginID)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		cancel()
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		cancel()
		return err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		_ = stderr.Close()
		cancel()
		return err
	}
	rt.cmd = cmd
	rt.stdin = stdin
	rt.helloOK = false
	rt.mounted = true
	rt.stopping = false
	rt.gen++
	startGen := rt.gen
	rt.dead.Store(false)

	go rt.readStdout(stdout)
	go rt.readStderr(stderr)
	go rt.waitProc(cmd)

	startMsg := map[string]any{
		"version":       1,
		"type":          "pane.start",
		"paneId":        rt.paneID,
		"pluginId":      rt.pluginID,
		"pluginVersion": rt.pluginVersion,
		"size":          map[string]int{"width": maxInt(1, width), "height": maxInt(1, height)},
		"theme": map[string]any{
			"appearance": "dark",
			"roles":      []string{"title", "body", "muted", "accent", "success", "warning", "error", "danger"},
		},
		"feeds":         []string{"session.summary", "usage", "agents.roster", "clock"},
		"icons":         []string{"check", "warn", "error", "agent", "folder", "file"},
		"commands":      []string{"agents", "files", "plugin"},
		"permissions":   rt.def.Permissions,
		"subscriptions": rt.def.Subscriptions,
	}
	_ = rt.writeMsgLocked(startMsg)

	timeout := time.Duration(rt.def.Timeouts.StartMs) * time.Millisecond
	go func(gen int) {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		<-timer.C
		rt.mu.Lock()
		defer rt.mu.Unlock()
		if rt.gen != gen || rt.helloOK || rt.stopping || rt.cmd == nil {
			return
		}
		rt.failLocked("process start timeout (no pane.hello)")
	}(startGen)
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func webPaneProcessEnv(extra map[string]string, paneID, pluginID string) []string {
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
		"TERM=dumb",
		"STRIKE_PANE_ID=" + paneID,
		"STRIKE_PLUGIN_ID=" + pluginID,
		"STRIKE_PANE_ABI=" + webPaneABI,
	}
	for k, v := range extra {
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, "=\x00") {
			continue
		}
		base = append(base, k+"="+v)
	}
	return base
}

func (rt *webPaneRuntime) send(v any) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.writeMsgLocked(v)
}

func (rt *webPaneRuntime) writeMsgLocked(v any) error {
	if rt.stdin == nil {
		return fmt.Errorf("pane process not running")
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = rt.stdin.Write(b)
	return err
}

func (rt *webPaneRuntime) pushFeeds(feeds map[string]any) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.stdin == nil || !rt.helloOK {
		return nil
	}
	for _, feed := range rt.def.Subscriptions {
		feed = strings.TrimSpace(feed)
		if feed == "" {
			continue
		}
		snap, ok := feeds[feed]
		if !ok {
			continue
		}
		msg := map[string]any{
			"version":  1,
			"type":     "pane.data",
			"feed":     feed,
			"snapshot": snap,
		}
		if err := rt.writeMsgLocked(msg); err != nil {
			return err
		}
	}
	return nil
}

func (rt *webPaneRuntime) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), webPaneMaxLineBytes+1)
	for sc.Scan() {
		line := sc.Bytes()
		n := atomic.AddInt64(&rt.stdoutN, int64(len(line)+1))
		if n > webPaneMaxStdoutLife {
			rt.mu.Lock()
			rt.failLocked("stdout budget exceeded")
			rt.mu.Unlock()
			return
		}
		rt.handleLine(append([]byte(nil), line...))
	}
}

func (rt *webPaneRuntime) readStderr(r io.Reader) {
	buf := make([]byte, 4096)
	var held []byte
	for {
		n, err := r.Read(buf)
		if n > 0 {
			held = append(held, buf[:n]...)
			if len(held) > webPaneMaxStderrBytes {
				held = held[len(held)-webPaneMaxStderrBytes:]
			}
		}
		if err != nil {
			return
		}
	}
}

func (rt *webPaneRuntime) waitProc(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	err := cmd.Wait()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cmd != cmd {
		return
	}
	if rt.stopping {
		rt.dead.Store(true)
		return
	}
	msg := "process exited"
	if err != nil {
		msg = "process exited: " + err.Error()
	}
	if rt.restarts < webPaneMaxAutoRestart && rt.errState == "" {
		rt.restarts++
		rt.cleanupProcLocked()
		rt.stopping = false
		if startErr := rt.startLocked(40, 14); startErr == nil {
			return
		}
	}
	rt.errState = msg
	rt.cleanupProcLocked()
	rt.dead.Store(true)
}

func (rt *webPaneRuntime) handleLine(line []byte) {
	if len(line) > webPaneMaxLineBytes {
		rt.noteMalformed("line too large")
		return
	}
	var env struct {
		Version int             `json:"version"`
		Type    string          `json:"type"`
		PaneID  string          `json:"paneId"`
		ABI     string          `json:"abi"`
		Title   string          `json:"title"`
		Status  string          `json:"status"`
		Rev     int64           `json:"rev"`
		View    json.RawMessage `json:"view"`
		Message string          `json:"message"`
		ID      string          `json:"id"`
		Action  map[string]any  `json:"action"`
	}
	if err := json.Unmarshal(line, &env); err != nil {
		rt.noteMalformed("invalid json")
		return
	}
	if env.Version != 1 {
		rt.noteMalformed("bad version")
		return
	}
	switch env.Type {
	case "pane.hello":
		rt.mu.Lock()
		if rt.helloOK {
			rt.mu.Unlock()
			rt.noteMalformed("duplicate hello")
			return
		}
		if env.ABI != "" && env.ABI != webPaneABI {
			rt.failLocked("abi mismatch: " + env.ABI)
			rt.mu.Unlock()
			return
		}
		if env.PaneID != "" && env.PaneID != rt.paneID {
			rt.failLocked("hello paneId mismatch")
			rt.mu.Unlock()
			return
		}
		rt.helloOK = true
		rt.mu.Unlock()
	case "pane.meta":
		rt.mu.Lock()
		if env.Title != "" {
			rt.title = clampWebPaneTitle(env.Title)
		}
		rt.status = sanitizeWebPaneText(env.Status)
		rt.mu.Unlock()
	case "pane.render":
		rt.mu.Lock()
		if !rt.helloOK {
			rt.mu.Unlock()
			rt.noteMalformed("render before hello")
			return
		}
		if env.Rev > 0 && env.Rev < rt.lastRev {
			rt.mu.Unlock()
			return
		}
		if !rt.allowRenderLocked() {
			rt.mu.Unlock()
			return
		}
		if len(env.View) > webPaneMaxLineBytes {
			rt.mu.Unlock()
			rt.noteMalformed("render too large")
			return
		}
		var probe map[string]any
		if err := json.Unmarshal(env.View, &probe); err != nil {
			rt.mu.Unlock()
			rt.noteMalformed("bad view")
			return
		}
		if countJSONNodes(env.View) > webPaneMaxNodes {
			rt.mu.Unlock()
			rt.noteMalformed("view too deep/large")
			return
		}
		if env.Rev > 0 {
			rt.lastRev = env.Rev
		}
		rt.view = append(json.RawMessage(nil), env.View...)
		rt.errState = ""
		rt.mu.Unlock()
	case "pane.action":
		// Host-mediated actions: notify only (web has no slash dispatch yet).
		// Acknowledge so process panes do not hang waiting for result.
		actType, _ := env.Action["type"].(string)
		ok := true
		errMsg := ""
		switch strings.ToLower(strings.TrimSpace(actType)) {
		case "notify", "":
			// no-op ack
		default:
			ok = false
			errMsg = "action type not supported on web host"
		}
		_ = rt.send(map[string]any{
			"version": 1,
			"type":    "pane.action.result",
			"id":      env.ID,
			"ok":      ok,
			"error":   errMsg,
		})
	case "pane.error":
		msg := sanitizeWebPaneText(env.Message)
		if msg == "" {
			msg = "pane error"
		}
		rt.mu.Lock()
		rt.errState = msg
		rt.mu.Unlock()
	case "pane.exit":
		// waitProc finalizes
	default:
		rt.noteMalformed("unknown type " + env.Type)
	}
}

func countJSONNodes(raw json.RawMessage) int {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return webPaneMaxNodes + 1
	}
	return countAnyNodes(v, 0)
}

func countAnyNodes(v any, depth int) int {
	if depth > 16 {
		return webPaneMaxNodes + 1
	}
	switch t := v.(type) {
	case map[string]any:
		n := 1
		if ch, ok := t["children"].([]any); ok {
			for _, c := range ch {
				n += countAnyNodes(c, depth+1)
				if n > webPaneMaxNodes {
					return n
				}
			}
		}
		if items, ok := t["items"].([]any); ok {
			n += len(items)
		}
		return n
	case []any:
		n := 0
		for _, c := range t {
			n += countAnyNodes(c, depth+1)
			if n > webPaneMaxNodes {
				return n
			}
		}
		return n
	default:
		return 1
	}
}

func (rt *webPaneRuntime) allowRenderLocked() bool {
	now := time.Now()
	cut := now.Add(-time.Second)
	kept := rt.renders[:0]
	for _, t := range rt.renders {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	rt.renders = kept
	if len(rt.renders) >= webPaneRenderBurst {
		return false
	}
	if len(rt.renders) >= webPaneRenderRatePerSec {
		return false
	}
	rt.renders = append(rt.renders, now)
	return true
}

func (rt *webPaneRuntime) noteMalformed(reason string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := time.Now()
	cut := now.Add(-webPaneMalformedWindow)
	kept := rt.malformed[:0]
	for _, t := range rt.malformed {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	rt.malformed = append(kept, now)
	if len(rt.malformed) >= webPaneMalformedBudget {
		rt.failLocked("too many malformed messages (" + reason + ")")
	}
}

func (rt *webPaneRuntime) failLocked(msg string) {
	if rt.errState == "" {
		rt.errState = msg
	}
	rt.cleanupProcLocked()
	rt.dead.Store(true)
}

func (rt *webPaneRuntime) cleanupProcLocked() {
	rt.stopping = true
	rt.mounted = false
	rt.gen++
	if rt.cancel != nil {
		rt.cancel()
		rt.cancel = nil
	}
	if rt.stdin != nil {
		_ = rt.stdin.Close()
		rt.stdin = nil
	}
	if rt.cmd != nil && rt.cmd.Process != nil {
		_ = rt.cmd.Process.Kill()
	}
	rt.cmd = nil
}

func (rt *webPaneRuntime) shutdown(reason string) {
	rt.mu.Lock()
	if rt.cmd == nil {
		rt.stopping = true
		rt.dead.Store(true)
		rt.mu.Unlock()
		return
	}
	_ = rt.writeMsgLocked(map[string]any{
		"version": 1,
		"type":    "pane.shutdown",
		"reason":  reason,
	})
	timeout := time.Duration(rt.def.Timeouts.ShutdownMs) * time.Millisecond
	rt.stopping = true
	cmd := rt.cmd
	rt.mu.Unlock()

	go func() {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if rt.dead.Load() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if !rt.dead.Load() {
			rt.mu.Lock()
			if rt.cmd == cmd && cmd != nil && cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
			rt.cleanupProcLocked()
			rt.dead.Store(true)
			rt.mu.Unlock()
		}
	}()
}
