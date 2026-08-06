package tui

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

	tea "charm.land/bubbletea/v2"
)

// Process protocol budgets (docs/plugin-panes.md §10–11).
const (
	paneMaxLineBytes      = 256 << 10
	paneMaxStdoutLifetime = 32 << 20
	paneMaxStderrBytes    = 64 << 10
	paneRenderRatePerSec  = 10
	paneRenderBurst       = 20
	paneMalformedBudget   = 8
	paneMalformedWindow   = 30 * time.Second
	paneMaxAutoRestart    = 1
)

// pluginPaneMsg is delivered from a process runtime to the Bubble Tea loop.
type pluginPaneMsg struct {
	paneID string
	kind   string // render | meta | error | exit | notice | action
	title  string
	status string
	view   json.RawMessage
	rev    int64
	err    string
	action paneHostAction
	actID  string
}

type paneHostAction struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	Level  string `json:"level,omitempty"`
	Target string `json:"target,omitempty"`
	Name   string `json:"name,omitempty"`
}

// pluginPaneRuntime owns one process pane subprocess. Shared across window
// value copies (same pattern as terminalWindow.sess).
type pluginPaneRuntime struct {
	mu sync.Mutex

	paneID        string
	pluginID      string
	pluginVersion string
	root          string
	def           paneDef

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	cancel context.CancelFunc

	helloOK   bool
	mounted   bool
	stopping  bool
	restarts  int
	gen       int // bumps on each start/cleanup to drop stale timers
	lastRev   int64
	view      json.RawMessage
	title     string
	status    string
	errState  string
	stderrBuf []byte
	stdoutN   int64

	malformed []time.Time
	renders   []time.Time

	// notify is closed (and replaced) when state changes for listenCmd.
	notifyCh chan struct{}
	pending  []pluginPaneMsg
	dead     atomic.Bool
}

func newPluginPaneRuntime(paneID, pluginID, pluginVersion, root string, def paneDef) *pluginPaneRuntime {
	return &pluginPaneRuntime{
		paneID:        paneID,
		pluginID:      pluginID,
		pluginVersion: pluginVersion,
		root:          root,
		def:           def,
		title:         def.Title,
		notifyCh:      make(chan struct{}, 1),
	}
}

func (rt *pluginPaneRuntime) signal() {
	select {
	case rt.notifyCh <- struct{}{}:
	default:
	}
}

func (rt *pluginPaneRuntime) push(msg pluginPaneMsg) {
	rt.mu.Lock()
	rt.pending = append(rt.pending, msg)
	rt.mu.Unlock()
	rt.signal()
}

// pushLocked queues a message while rt.mu is already held.
func (rt *pluginPaneRuntime) pushLocked(msg pluginPaneMsg) {
	rt.pending = append(rt.pending, msg)
	// signal after unlock by callers, or best-effort now (non-blocking).
	rt.signal()
}

func (rt *pluginPaneRuntime) takePending() []pluginPaneMsg {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if len(rt.pending) == 0 {
		return nil
	}
	out := rt.pending
	rt.pending = nil
	return out
}

// start launches the process and begins the read loop. Safe to call once.
func (rt *pluginPaneRuntime) start(width, height int) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.cmd != nil || rt.stopping {
		return nil
	}
	return rt.startLocked(width, height)
}

func (rt *pluginPaneRuntime) startLocked(width, height int) error {
	cmdPath := rt.def.Command
	if !filepath.IsAbs(cmdPath) {
		cmdPath = filepath.Join(rt.root, cmdPath)
	}
	ctx, cancel := context.WithCancel(context.Background())
	rt.cancel = cancel
	cmd := exec.CommandContext(ctx, cmdPath, rt.def.Args...)
	cmd.Dir = rt.root
	cmd.Env = paneProcessEnv(rt.def.Env, rt.paneID, rt.pluginID)
	configurePluginPaneCmd(cmd)

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

	// Send pane.start; hello may arrive before or after start.
	startMsg := map[string]any{
		"version":       1,
		"type":          "pane.start",
		"paneId":        rt.paneID,
		"pluginId":      rt.pluginID,
		"pluginVersion": rt.pluginVersion,
		"size":          map[string]int{"width": max(1, width), "height": max(1, height)},
		"theme": map[string]any{
			"appearance": "dark",
			"roles":      []string{"title", "body", "muted", "accent", "success", "warning", "error", "danger"},
		},
		"feeds":         []string{"session.summary", "usage", "agents.roster", "clock"},
		"icons":         []string{"check", "warn", "error", "agent", "folder", "file"},
		"commands":      []string{"agents", "files", "theme", "plugin"},
		"permissions":   rt.def.Permissions,
		"subscriptions": rt.def.Subscriptions,
	}
	_ = rt.writeMsgLocked(startMsg)

	// Hello deadline.
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

func paneProcessEnv(extra map[string]string, paneID, pluginID string) []string {
	// Minimal filtered env — no host secrets by default.
	base := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"LANG=" + os.Getenv("LANG"),
		"TERM=dumb",
		"STRIKE_PANE_ID=" + paneID,
		"STRIKE_PLUGIN_ID=" + pluginID,
		"STRIKE_PANE_ABI=" + paneABI,
		"STRIKE_PLUGIN_ROOT=", // set by caller via Dir; also export root below
	}
	// STRIKE_PLUGIN_ROOT filled when we know root — caller patches via extra.
	for k, v := range extra {
		k = strings.TrimSpace(k)
		if k == "" || strings.ContainsAny(k, "=\x00") {
			continue
		}
		// Never pass obvious secret-looking values from definition literals
		// that look like refs unresolved — host should resolve refs earlier.
		base = append(base, k+"="+v)
	}
	return base
}

func (rt *pluginPaneRuntime) writeMsg(v any) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.writeMsgLocked(v)
}

func (rt *pluginPaneRuntime) writeMsgLocked(v any) error {
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

func (rt *pluginPaneRuntime) readStdout(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), paneMaxLineBytes+1)
	for sc.Scan() {
		line := sc.Bytes()
		n := atomic.AddInt64(&rt.stdoutN, int64(len(line)+1))
		if n > paneMaxStdoutLifetime {
			rt.mu.Lock()
			rt.failLocked("stdout budget exceeded")
			rt.mu.Unlock()
			return
		}
		rt.handleLine(append([]byte(nil), line...))
	}
}

func (rt *pluginPaneRuntime) readStderr(r io.Reader) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			rt.mu.Lock()
			rt.stderrBuf = append(rt.stderrBuf, buf[:n]...)
			if len(rt.stderrBuf) > paneMaxStderrBytes {
				rt.stderrBuf = rt.stderrBuf[len(rt.stderrBuf)-paneMaxStderrBytes:]
			}
			rt.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (rt *pluginPaneRuntime) waitProc(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	err := cmd.Wait()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	// Ignore waits for superseded processes.
	if rt.cmd != cmd {
		return
	}
	if rt.stopping {
		rt.dead.Store(true)
		rt.signal()
		return
	}
	msg := "process exited"
	if err != nil {
		msg = "process exited: " + err.Error()
	}
	// Auto-restart at most once per mount.
	if rt.restarts < paneMaxAutoRestart && rt.errState == "" {
		rt.restarts++
		rt.cleanupProcLocked()
		rt.stopping = false
		if startErr := rt.startLocked(0, 0); startErr == nil {
			return
		}
	}
	rt.errState = msg
	rt.cleanupProcLocked()
	rt.dead.Store(true)
	rt.pushLocked(pluginPaneMsg{paneID: rt.paneID, kind: "exit", err: msg})
}

func (rt *pluginPaneRuntime) handleLine(line []byte) {
	if len(line) > paneMaxLineBytes {
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
		Action  paneHostAction  `json:"action"`
		Code    int             `json:"code"`
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
		if env.ABI != "" && env.ABI != paneABI {
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
			rt.title = clampPaneTitle(env.Title)
		}
		rt.status = sanitizePaneText(env.Status)
		title, status := rt.title, rt.status
		rt.mu.Unlock()
		rt.push(pluginPaneMsg{paneID: rt.paneID, kind: "meta", title: title, status: status})
	case "pane.render":
		rt.mu.Lock()
		if !rt.helloOK {
			rt.mu.Unlock()
			rt.noteMalformed("render before hello")
			return
		}
		if env.Rev > 0 && env.Rev < rt.lastRev {
			rt.mu.Unlock()
			return // stale
		}
		if !rt.allowRenderLocked() {
			rt.mu.Unlock()
			return // rate limited — keep last good view
		}
		if len(env.View) > paneMaxLineBytes {
			rt.mu.Unlock()
			rt.noteMalformed("render too large")
			return
		}
		// Basic tree budget.
		var probe paneViewNode
		if err := json.Unmarshal(env.View, &probe); err != nil {
			rt.mu.Unlock()
			rt.noteMalformed("bad view")
			return
		}
		if countPaneNodes(probe) > paneMaxNodes {
			rt.mu.Unlock()
			rt.noteMalformed("view too deep/large")
			return
		}
		if env.Rev > 0 {
			rt.lastRev = env.Rev
		}
		rt.view = append(json.RawMessage(nil), env.View...)
		view := rt.view
		rev := rt.lastRev
		rt.mu.Unlock()
		rt.push(pluginPaneMsg{paneID: rt.paneID, kind: "render", view: view, rev: rev})
	case "pane.action":
		rt.push(pluginPaneMsg{
			paneID: rt.paneID,
			kind:   "action",
			actID:  env.ID,
			action: env.Action,
		})
	case "pane.error":
		msg := sanitizePaneText(env.Message)
		if msg == "" {
			msg = "pane error"
		}
		rt.mu.Lock()
		rt.errState = msg
		rt.mu.Unlock()
		rt.push(pluginPaneMsg{paneID: rt.paneID, kind: "error", err: msg})
	case "pane.exit":
		// Clean signal; waitProc will finalize.
	default:
		rt.noteMalformed("unknown type " + env.Type)
	}
}

func (rt *pluginPaneRuntime) allowRenderLocked() bool {
	now := time.Now()
	// Drop timestamps older than 1s.
	cut := now.Add(-time.Second)
	kept := rt.renders[:0]
	for _, t := range rt.renders {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	rt.renders = kept
	if len(rt.renders) >= paneRenderBurst {
		return false
	}
	// Sustained 10/s: if we already have 10 in the last second, drop.
	if len(rt.renders) >= paneRenderRatePerSec {
		return false
	}
	rt.renders = append(rt.renders, now)
	return true
}

func (rt *pluginPaneRuntime) noteMalformed(reason string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	now := time.Now()
	cut := now.Add(-paneMalformedWindow)
	kept := rt.malformed[:0]
	for _, t := range rt.malformed {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	rt.malformed = append(kept, now)
	if len(rt.malformed) >= paneMalformedBudget {
		rt.failLocked("too many malformed messages (" + reason + ")")
	}
}

func (rt *pluginPaneRuntime) failLocked(msg string) {
	if rt.errState == "" {
		rt.errState = msg
	}
	rt.cleanupProcLocked()
	rt.dead.Store(true)
	rt.pushLocked(pluginPaneMsg{paneID: rt.paneID, kind: "error", err: rt.errState})
}

func (rt *pluginPaneRuntime) cleanupProcLocked() {
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
		_ = killPluginPaneProcess(rt.cmd)
	}
	rt.cmd = nil
}

// shutdown requests a clean stop.
func (rt *pluginPaneRuntime) shutdown(reason string) {
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
	// Mark stopping so waitProc does not treat exit as crash/restart.
	rt.stopping = true
	cmd := rt.cmd
	rt.mu.Unlock()

	// Give the process a grace period, then force-kill. waitProc owns Wait().
	done := make(chan struct{})
	go func() {
		// Poll dead flag set by waitProc after Wait returns.
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			if rt.dead.Load() {
				close(done)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(done)
	}()
	<-done
	if !rt.dead.Load() {
		rt.mu.Lock()
		if rt.cmd == cmd {
			rt.cleanupProcLocked()
		}
		rt.dead.Store(true)
		rt.mu.Unlock()
	}
	rt.signal()
}

func (rt *pluginPaneRuntime) sendResize(w, h int) {
	_ = rt.writeMsg(map[string]any{
		"version": 1,
		"type":    "pane.resize",
		"size":    map[string]int{"width": max(1, w), "height": max(1, h)},
	})
}

func (rt *pluginPaneRuntime) sendFocus(focused bool) {
	_ = rt.writeMsg(map[string]any{
		"version": 1,
		"type":    "pane.focus",
		"focused": focused,
	})
}

func (rt *pluginPaneRuntime) sendData(feed string, snapshot map[string]any) {
	_ = rt.writeMsg(map[string]any{
		"version":  1,
		"type":     "pane.data",
		"feed":     feed,
		"snapshot": snapshot,
	})
}

func (rt *pluginPaneRuntime) sendInput(key string, mods []string) {
	_ = rt.writeMsg(map[string]any{
		"version": 1,
		"type":    "pane.input",
		"event": map[string]any{
			"kind": "key",
			"key":  key,
			"mods": mods,
		},
	})
}

func (rt *pluginPaneRuntime) sendActionResult(id string, ok bool, errMsg string) {
	msg := map[string]any{
		"version": 1,
		"type":    "pane.action.result",
		"id":      id,
		"ok":      ok,
	}
	if !ok && errMsg != "" {
		msg["error"] = errMsg
	}
	_ = rt.writeMsg(msg)
}

func (rt *pluginPaneRuntime) snapshot() (title, status, errState string, view json.RawMessage, hello bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.title, rt.status, rt.errState, append(json.RawMessage(nil), rt.view...), rt.helloOK
}

// listenCmd waits for runtime activity.
func (rt *pluginPaneRuntime) listenCmd() tea.Cmd {
	if rt == nil {
		return nil
	}
	ch := rt.notifyCh
	id := rt.paneID
	return func() tea.Msg {
		<-ch
		return pluginPaneWakeMsg{paneID: id}
	}
}

// pluginPaneWakeMsg tells the model to drain pending msgs for a pane.
type pluginPaneWakeMsg struct {
	paneID string
}
