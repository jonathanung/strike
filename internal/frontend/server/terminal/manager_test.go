package terminal

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func testManager(t *testing.T, root string) *Manager {
	t.Helper()
	home := t.TempDir()
	m := NewManager(ManagerConfig{
		Scrollback:    64 * 1024,
		MaxSessions:   4,
		WatchInterval: 20 * time.Millisecond,
		Env: func(cwd string) []string {
			return []string{
				"PATH=" + os.Getenv("PATH"),
				"HOME=" + home,
				"TERM=xterm-256color",
				"HISTFILE=/dev/null",
				"PWD=" + cwd,
				"PS1=$ ",
			}
		},
	})
	t.Cleanup(m.Close)
	return m
}

func waitText(t *testing.T, m *Manager, id, needle string) string {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var all []byte
	pos := 0
	for time.Now().Before(deadline) {
		chunk, next, _, err := m.Read(id, pos)
		if err != nil {
			t.Fatal(err)
		}
		pos = next
		all = append(all, chunk...)
		if bytes.Contains(all, []byte(needle)) {
			return string(all)
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %q, have %q", needle, all)
	return ""
}

func TestTerminalStaysInWorkspaceAndReconnects(t *testing.T) {
	root := t.TempDir()
	m := testManager(t, root)
	info, err := m.Create(CreateSpec{WorkspaceID: "ws", WorkspacePath: root, Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	if info.CWD != root {
		t.Fatalf("cwd = %q, want %q", info.CWD, root)
	}
	if info.Owner != OwnerUser {
		t.Fatalf("owner = %q", info.Owner)
	}
	if info.OnSessionEnd != OnSessionEndKill {
		t.Fatalf("policy = %q", info.OnSessionEnd)
	}
	if err := m.Write(info.ID, []byte("printf 'MARKER_ok\\n'\n")); err != nil {
		t.Fatal(err)
	}
	if got := waitText(t, m, info.ID, "MARKER_ok"); !bytes.Contains([]byte(got), []byte(root)) && !bytes.Contains([]byte(got), []byte("MARKER_ok")) {
		t.Fatalf("output = %q", got)
	}
	if err := m.Write(info.ID, []byte("pwd\n")); err != nil {
		t.Fatal(err)
	}
	if got := waitText(t, m, info.ID, root); !bytes.Contains([]byte(got), []byte(root)) {
		t.Fatalf("pwd output = %q, want workspace %s", got, root)
	}

	resized, err := m.Resize(info.ID, 40, 12)
	if err != nil {
		t.Fatal(err)
	}
	if resized.Cols != 40 || resized.Rows != 12 {
		t.Fatalf("size = %dx%d", resized.Cols, resized.Rows)
	}

	again, next, snap, err := m.Read(info.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !snap.Alive {
		t.Fatalf("shell died on reconnect read: %s", snap.ExitReason)
	}
	if !bytes.Contains(again, []byte("MARKER_ok")) {
		t.Fatalf("scrollback lost marker: %q", again)
	}
	if next <= 0 {
		t.Fatal("cursor did not advance")
	}
	if err := m.Write(info.ID, []byte("printf 'STILL_ALIVE\\n'\n")); err != nil {
		t.Fatal(err)
	}
	if got := waitText(t, m, info.ID, "STILL_ALIVE"); !bytes.Contains([]byte(got), []byte("STILL_ALIVE")) {
		t.Fatalf("output = %q", got)
	}
}

func TestTerminalRejectsCWDOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	m := testManager(t, root)
	_, err := m.Create(CreateSpec{WorkspacePath: root, CWD: outside})
	if err == nil {
		t.Fatal("expected cwd outside the workspace to fail")
	}
	if _, err := m.Create(CreateSpec{WorkspacePath: root, CWD: filepath.Join(root, "..")}); err == nil {
		t.Fatal("expected parent cwd to fail")
	}
	if len(m.List()) != 0 {
		t.Fatalf("failed creates left sessions: %+v", m.List())
	}
}

func TestTerminalKillsWhenCWDLeavesWorkspace(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("cwd watch reads /proc")
	}
	root := t.TempDir()
	m := testManager(t, root)
	info, err := m.Create(CreateSpec{WorkspacePath: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Write(info.ID, []byte("cd /tmp\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		got, ok := m.Get(info.ID)
		if ok && !got.Alive && got.ExitReason == ReasonCWDEscaped {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := m.Get(info.ID)
	chunk, _, _, _ := m.Read(info.ID, 0)
	t.Fatalf("shell still alive or wrong reason: %+v\noutput:\n%s", got, chunk)
}

func TestTerminalSessionEndPolicy(t *testing.T) {
	root := t.TempDir()
	m := testManager(t, root)
	kill, err := m.Create(CreateSpec{WorkspacePath: root, SessionID: "root-a", OnSessionEnd: OnSessionEndKill})
	if err != nil {
		t.Fatal(err)
	}
	keep, err := m.Create(CreateSpec{WorkspacePath: root, SessionID: "root-a", OnSessionEnd: OnSessionEndKeep})
	if err != nil {
		t.Fatal(err)
	}
	other, err := m.Create(CreateSpec{WorkspacePath: root, SessionID: "root-b", OnSessionEnd: OnSessionEndKill})
	if err != nil {
		t.Fatal(err)
	}
	m.EndSession("root-a")
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		k, _ := m.Get(kill.ID)
		p, _ := m.Get(keep.ID)
		o, _ := m.Get(other.ID)
		if !k.Alive && k.ExitReason == ReasonSessionEnded && p.Alive && o.Alive {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	k, _ := m.Get(kill.ID)
	p, _ := m.Get(keep.ID)
	o, _ := m.Get(other.ID)
	t.Fatalf("kill=%+v keep=%+v other=%+v", k, p, o)
}

func TestTerminalSharedWithAgent(t *testing.T) {
	root := t.TempDir()
	m := testManager(t, root)
	busy := true
	m.SetAgentProbe(func(sessionID string) AgentView {
		if sessionID != "root-a" {
			return AgentView{}
		}
		return AgentView{CWD: root, Busy: busy}
	})
	info, err := m.Create(CreateSpec{WorkspacePath: root, SessionID: "root-a"})
	if err != nil {
		t.Fatal(err)
	}
	got, _ := m.Get(info.ID)
	if !got.SameDirectory || !got.SharedWithAgent || !got.AgentBusy {
		t.Fatalf("indicator = %+v", got)
	}
	busy = false
	got, _ = m.Get(info.ID)
	if !got.SameDirectory || got.SharedWithAgent || got.AgentBusy {
		t.Fatalf("idle indicator = %+v", got)
	}
}

func TestTerminalTicketSingleUse(t *testing.T) {
	root := t.TempDir()
	m := testManager(t, root)
	info, err := m.Create(CreateSpec{WorkspacePath: root})
	if err != nil {
		t.Fatal(err)
	}
	tok, _, err := m.MintTicket(info.ID)
	if err != nil || tok == "" {
		t.Fatal(err)
	}
	if !m.ConsumeTicket(info.ID, tok) {
		t.Fatal("expected ticket to validate")
	}
	if m.ConsumeTicket(info.ID, tok) {
		t.Fatal("ticket was reused")
	}
	if m.ConsumeTicket(info.ID, "nope") {
		t.Fatal("accepted an unknown ticket")
	}
}

func TestRingDropsFront(t *testing.T) {
	r := ring{max: 4}
	r.append([]byte("abcdef"))
	chunk, next := r.since(0)
	if string(chunk) != "cdef" || next != 6 {
		t.Fatalf("chunk %q next %d", chunk, next)
	}
	chunk, next = r.since(5)
	if string(chunk) != "f" || next != 6 {
		t.Fatalf("tail %q next %d", chunk, next)
	}
}
