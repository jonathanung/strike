// Package terminal is the daemon PTY manager for the web side panel.
//
// A terminal is a user shell, not an agent tool. Opening one is allowed
// for the same reason composer "!" is allowed: the person is present, so
// the bash permission prompt is skipped. Plan mode still refuses a new
// shell. The process starts only in a registered workspace directory
// (see ResolveCWD). On Linux the watch reads the foreground process group
// (the shell behind bwrap, or a child that is currently foreground). A cwd
// outside the workspace kills the shell. An unreadable cwd kills it only
// when no OS sandbox was applied. Background helpers are not walked.
//
// The OS sandbox dial is applied with sandbox.Wrap, the same launcher the
// bash tool uses. Keystrokes are not logged.
package terminal

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"github.com/jonathanung/strike/harness/sandbox"
	"github.com/jonathanung/strike/harness/tool"
)

const (
	defaultScrollback  = 256 * 1024
	defaultMaxSessions = 8
	defaultWatch       = 200 * time.Millisecond
	defaultTicketTTL   = 30 * time.Second
	OnSessionEndKill   = "kill"
	OnSessionEndKeep   = "keep"
	ReasonCWDEscaped   = "cwd left the workspace"
	ReasonSessionEnded = "session ended"
	ReasonClosed       = "closed"
	ReasonExited       = "exited"
	OwnerUser          = "user"
)

// AgentView is the live session the shell may be sharing a directory with.
type AgentView struct {
	CWD            string
	Busy           bool
	PermissionMode string
}

// AgentProbe reads the live session bound to a terminal. Nil means no probe.
type AgentProbe func(sessionID string) AgentView

// ManagerConfig tunes the manager. Zero values use defaults.
type ManagerConfig struct {
	Scrollback    int
	MaxSessions   int
	WatchInterval time.Duration
	Sandbox       sandbox.Policy
	// Env, when set, replaces the default minimal environment.
	Env func(cwd string) []string
	// Now is the clock for ticket expiry. Nil uses time.Now.
	Now func() time.Time
	// LookPath resolves the shell. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
}

// Info is the public description of one terminal.
type Info struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	CWD             string `json:"cwd"`
	WorkspaceID     string `json:"workspaceId"`
	WorkspacePath   string `json:"workspacePath"`
	SessionID       string `json:"sessionId,omitempty"`
	Cols            int    `json:"cols"`
	Rows            int    `json:"rows"`
	OnSessionEnd    string `json:"onSessionEnd"`
	Owner           string `json:"owner"`
	Alive           bool   `json:"alive"`
	ExitReason      string `json:"exitReason,omitempty"`
	SharedWithAgent bool   `json:"sharedWithAgent"`
	SameDirectory   bool   `json:"sameDirectory"`
	AgentCWD        string `json:"agentCwd,omitempty"`
	AgentBusy       bool   `json:"agentBusy"`
	PermissionMode  string `json:"permissionMode,omitempty"`
	SandboxApplied  bool   `json:"sandboxApplied"`
	SandboxDegraded bool   `json:"sandboxDegraded,omitempty"`
	ScrollBase      int    `json:"-"`
	ScrollLen       int    `json:"-"`
}

// CreateSpec starts a shell.
type CreateSpec struct {
	WorkspaceID   string
	WorkspacePath string
	CWD           string
	SessionID     string
	Cols          int
	Rows          int
	OnSessionEnd  string
	// Sandbox, when non-nil, is the OS policy for this shell. WorkDir is
	// overwritten with the canonical workspace root. Nil uses the manager dial.
	Sandbox *sandbox.Policy
}

// Manager owns PTY sessions for one daemon process.
type Manager struct {
	cfg   ManagerConfig
	mu    sync.Mutex
	seq   int
	byID  map[string]*session
	probe AgentProbe
	now   func() time.Time

	ticketMu sync.Mutex
	tickets  map[string]ticket
}

type ticket struct {
	terminalID string
	expires    time.Time
}

type session struct {
	info Info
	root string // canonical workspace root

	mu       sync.Mutex
	pty      *os.File
	cmd      *exec.Cmd
	scroll   ring
	finished bool
	done     chan struct{}
	once     sync.Once
}

type ring struct {
	buf  []byte
	max  int
	base int
}

func (r *ring) append(p []byte) {
	if len(p) == 0 {
		return
	}
	r.buf = append(r.buf, p...)
	if r.max > 0 && len(r.buf) > r.max {
		drop := len(r.buf) - r.max
		r.buf = append([]byte(nil), r.buf[drop:]...)
		r.base += drop
	}
}

func (r *ring) since(pos int) ([]byte, int) {
	if pos < r.base {
		pos = r.base
	}
	off := pos - r.base
	if off > len(r.buf) {
		off = len(r.buf)
	}
	next := r.base + len(r.buf)
	if off == len(r.buf) {
		return nil, next
	}
	return append([]byte(nil), r.buf[off:]...), next
}

// NewManager returns an empty manager.
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.Scrollback <= 0 {
		cfg.Scrollback = defaultScrollback
	}
	if cfg.MaxSessions <= 0 {
		cfg.MaxSessions = defaultMaxSessions
	}
	if cfg.WatchInterval <= 0 {
		cfg.WatchInterval = defaultWatch
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Manager{
		cfg:     cfg,
		byID:    map[string]*session{},
		now:     now,
		tickets: map[string]ticket{},
	}
}

// SetSandbox replaces the OS sandbox policy used for new shells.
// WorkDir is overwritten with the workspace root at create time.
func (m *Manager) SetSandbox(p sandbox.Policy) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cfg.Sandbox = p
	m.mu.Unlock()
}

// SetAgentProbe installs the live-session lookup used for the shared-directory
// indicator. It may be called before any shell starts.
func (m *Manager) SetAgentProbe(p AgentProbe) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.probe = p
	m.mu.Unlock()
}

// Close kills every shell. Used on daemon shutdown.
func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	ids := make([]string, 0, len(m.byID))
	for id := range m.byID {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.Kill(id, ReasonClosed)
	}
}

// EndSession applies the kill-on-session-end policy for sessionID.
func (m *Manager) EndSession(sessionID string) {
	if m == nil || sessionID == "" {
		return
	}
	m.mu.Lock()
	var kill []string
	for id, s := range m.byID {
		s.mu.Lock()
		match := s.info.SessionID == sessionID && s.info.OnSessionEnd == OnSessionEndKill && s.info.Alive
		s.mu.Unlock()
		if match {
			kill = append(kill, id)
		}
	}
	m.mu.Unlock()
	for _, id := range kill {
		m.Kill(id, ReasonSessionEnded)
	}
}

// List returns a snapshot of every terminal, including ones that have exited
// but not yet been removed.
func (m *Manager) List() []Info {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	out := make([]Info, 0, len(m.byID))
	for _, s := range m.byID {
		out = append(out, m.decorate(s))
	}
	m.mu.Unlock()
	return out
}

// Get returns one terminal.
func (m *Manager) Get(id string) (Info, bool) {
	if m == nil {
		return Info{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.byID[id]
	if !ok {
		return Info{}, false
	}
	return m.decorate(s), true
}

func (m *Manager) decorate(s *session) Info {
	s.mu.Lock()
	info := s.info
	info.ScrollBase = s.scroll.base
	info.ScrollLen = s.scroll.base + len(s.scroll.buf)
	s.mu.Unlock()
	view := AgentView{}
	if m.probe != nil && info.SessionID != "" {
		view = m.probe(info.SessionID)
	}
	info.AgentCWD = view.CWD
	info.AgentBusy = view.Busy
	info.PermissionMode = view.PermissionMode
	info.SameDirectory = SameDirectory(info.CWD, view.CWD)
	info.SharedWithAgent = info.SameDirectory && view.Busy
	return info
}

// Create starts a shell in the workspace. The cwd must resolve inside
// WorkspacePath. Plan mode is enforced by the caller before Create.
func (m *Manager) Create(spec CreateSpec) (Info, error) {
	if m == nil {
		return Info{}, errors.New("terminal unavailable")
	}
	root, err := canonicalDir(spec.WorkspacePath)
	if err != nil {
		return Info{}, fmt.Errorf("workspace: %w", err)
	}
	cwd, err := ResolveCWD(root, spec.CWD)
	if err != nil {
		return Info{}, err
	}
	policy := spec.OnSessionEnd
	if policy == "" {
		policy = OnSessionEndKill
	}
	if policy != OnSessionEndKill && policy != OnSessionEndKeep {
		return Info{}, fmt.Errorf("onSessionEnd must be %q or %q", OnSessionEndKill, OnSessionEndKeep)
	}
	cols, rows := clampSize(spec.Cols, spec.Rows)

	m.mu.Lock()
	if len(m.byID) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		return Info{}, fmt.Errorf("terminal limit reached (%d)", m.cfg.MaxSessions)
	}
	sandboxPolicy := m.cfg.Sandbox
	if spec.Sandbox != nil {
		sandboxPolicy = *spec.Sandbox
	}
	envFn := m.cfg.Env
	look := m.cfg.LookPath
	m.seq++
	title := fmt.Sprintf("Terminal %d", m.seq)
	m.mu.Unlock()

	if look == nil {
		look = exec.LookPath
	}
	bash, err := look("bash")
	if err != nil {
		return Info{}, fmt.Errorf("bash: %w", err)
	}
	sandboxPolicy.WorkDir = root
	wrap := sandbox.WrapResult([]string{bash, "-i"}, sandboxPolicy)
	if wrap.Degraded && sandboxPolicy.Mode != sandbox.ModeOff && !sandboxPolicy.AllowDegrade {
		return Info{}, errors.New("OS sandbox is required for this shell and is unavailable")
	}
	argv := wrap.Argv
	if len(argv) == 0 {
		return Info{}, errors.New("empty shell argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	if envFn != nil {
		cmd.Env = envFn(cwd)
	} else {
		cmd.Env = defaultEnv(cwd, cols, rows)
	}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return Info{}, err
	}
	id, err := newID()
	if err != nil {
		_ = f.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		return Info{}, err
	}
	s := &session{
		root:   root,
		pty:    f,
		cmd:    cmd,
		done:   make(chan struct{}),
		scroll: ring{max: m.cfg.Scrollback},
		info: Info{
			ID:              id,
			Title:           title,
			CWD:             cwd,
			WorkspaceID:     spec.WorkspaceID,
			WorkspacePath:   root,
			SessionID:       spec.SessionID,
			Cols:            cols,
			Rows:            rows,
			OnSessionEnd:    policy,
			Owner:           OwnerUser,
			Alive:           true,
			SandboxApplied:  wrap.Applied,
			SandboxDegraded: wrap.Degraded,
		},
	}
	m.mu.Lock()
	if len(m.byID) >= m.cfg.MaxSessions {
		m.mu.Unlock()
		go s.waitLoop()
		s.finish(ReasonClosed)
		return Info{}, fmt.Errorf("terminal limit reached (%d)", m.cfg.MaxSessions)
	}
	m.byID[id] = s
	interval := m.cfg.WatchInterval
	m.mu.Unlock()

	go s.readLoop()
	go s.waitLoop()
	go s.watchLoop(interval)
	info, ok := m.Get(id)
	if !ok {
		return Info{}, errors.New("terminal not found")
	}
	return info, nil
}

func (s *session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if n > 0 {
			s.mu.Lock()
			if !s.finished {
				s.scroll.append(buf[:n])
			}
			s.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (s *session) waitLoop() {
	_ = s.cmd.Wait()
	s.finish(ReasonExited)
}

func (s *session) watchLoop(interval time.Duration) {
	if interval <= 0 {
		interval = defaultWatch
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case <-ticker.C:
			s.mu.Lock()
			alive := s.info.Alive && !s.finished
			pid := 0
			if s.cmd != nil && s.cmd.Process != nil {
				pid = s.cmd.Process.Pid
			}
			root := s.root
			s.mu.Unlock()
			if !alive || pid <= 0 {
				return
			}
			insideRoot, seen := foregroundInside(root, pid)
			s.mu.Lock()
			sandboxed := s.info.SandboxApplied
			s.mu.Unlock()
			if !shouldKillForCWD(seen, insideRoot, sandboxed, cwdUnseenIsEscape()) {
				continue
			}
			s.finish(ReasonCWDEscaped)
			return
		}
	}
}

// shouldKillForCWD reports whether the watch should stop the shell.
// An unreadable cwd is an escape only when the caller says so and no OS
// sandbox was applied. A visible cwd outside the workspace always is.
func shouldKillForCWD(seen, inside, sandboxed, unseenEscapes bool) bool {
	if !seen {
		return unseenEscapes && !sandboxed
	}
	return !inside
}

func (s *session) finish(reason string) {
	s.once.Do(func() {
		s.mu.Lock()
		s.finished = true
		s.info.Alive = false
		if s.info.ExitReason == "" {
			s.info.ExitReason = reason
		}
		if s.info.ExitReason == "" {
			s.info.ExitReason = ReasonExited
		}
		ptyFile := s.pty
		cmd := s.cmd
		s.mu.Unlock()
		if ptyFile != nil {
			_ = ptyFile.Close()
		}
		if cmd != nil && cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
		}
		close(s.done)
	})
}

// Write sends input to a live shell. data is not retained in logs.
func (m *Manager) Write(id string, data []byte) error {
	s, err := m.live(id)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err = s.pty.Write(data)
	if err != nil {
		return err
	}
	return nil
}

// Resize updates the PTY window and the stored size.
func (m *Manager) Resize(id string, cols, rows int) (Info, error) {
	cols, rows = clampSize(cols, rows)
	s, err := m.live(id)
	if err != nil {
		return Info{}, err
	}
	if err := pty.Setsize(s.pty, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		return Info{}, err
	}
	s.mu.Lock()
	s.info.Cols = cols
	s.info.Rows = rows
	s.mu.Unlock()
	info, ok := m.Get(id)
	if !ok {
		return Info{}, errors.New("terminal not found")
	}
	return info, nil
}

// Read returns output bytes since pos (a byte cursor from a previous Read
// or from Info.ScrollBase at attach). The cursor survives reconnect; bytes
// trimmed from the scrollback are skipped.
func (m *Manager) Read(id string, pos int) (chunk []byte, next int, info Info, err error) {
	m.mu.Lock()
	s, ok := m.byID[id]
	m.mu.Unlock()
	if !ok {
		return nil, 0, Info{}, errors.New("terminal not found")
	}
	s.mu.Lock()
	chunk, next = s.scroll.since(pos)
	s.mu.Unlock()
	info, ok = m.Get(id)
	if !ok {
		return nil, 0, Info{}, errors.New("terminal not found")
	}
	return chunk, next, info, nil
}

// Kill stops one shell and keeps it in the list so the client can show why.
func (m *Manager) Kill(id, reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	s := m.byID[id]
	m.mu.Unlock()
	if s == nil {
		return
	}
	if reason == "" {
		reason = ReasonClosed
	}
	s.mu.Lock()
	if s.info.ExitReason == "" {
		s.info.ExitReason = reason
	}
	s.mu.Unlock()
	s.finish(reason)
}

// Remove stops a shell and drops it from the list.
func (m *Manager) Remove(id string) {
	if m == nil {
		return
	}
	m.Kill(id, ReasonClosed)
	m.mu.Lock()
	delete(m.byID, id)
	m.mu.Unlock()
}

func (m *Manager) live(id string) (*session, error) {
	m.mu.Lock()
	s := m.byID[id]
	m.mu.Unlock()
	if s == nil {
		return nil, errors.New("terminal not found")
	}
	s.mu.Lock()
	alive := s.info.Alive && !s.finished
	s.mu.Unlock()
	if !alive {
		return nil, errors.New("terminal is not running")
	}
	return s, nil
}

// MintTicket returns a single-use ticket bound to id. The browser presents
// it as a websocket subprotocol because it cannot set Authorization.
func (m *Manager) MintTicket(id string) (string, time.Time, error) {
	if _, ok := m.Get(id); !ok {
		return "", time.Time{}, errors.New("terminal not found")
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, err
	}
	tok := hex.EncodeToString(raw)
	exp := m.now().Add(defaultTicketTTL)
	m.ticketMu.Lock()
	m.purgeTicketsLocked()
	m.tickets[tok] = ticket{terminalID: id, expires: exp}
	m.ticketMu.Unlock()
	return tok, exp, nil
}

// ConsumeTicket accepts a ticket once for terminalID. A mismatch, replay,
// or expiry is false.
func (m *Manager) ConsumeTicket(terminalID, got string) bool {
	if m == nil || got == "" || terminalID == "" {
		return false
	}
	m.ticketMu.Lock()
	defer m.ticketMu.Unlock()
	m.purgeTicketsLocked()
	var matched string
	var rec ticket
	for tok, t := range m.tickets {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(got)) == 1 {
			matched = tok
			rec = t
			break
		}
	}
	if matched == "" {
		return false
	}
	delete(m.tickets, matched)
	return rec.terminalID == terminalID
}

func (m *Manager) purgeTicketsLocked() {
	now := m.now()
	for tok, t := range m.tickets {
		if !now.Before(t.expires) {
			delete(m.tickets, tok)
		}
	}
}

func newID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func clampSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	if cols < 2 {
		cols = 2
	}
	if rows < 2 {
		rows = 2
	}
	if cols > 500 {
		cols = 500
	}
	if rows > 200 {
		rows = 200
	}
	return cols, rows
}

func defaultEnv(cwd string, cols, rows int) []string {
	keys := tool.BashMinimalEnvKeys()
	out := make([]string, 0, len(keys)+6)
	seen := map[string]struct{}{}
	for _, k := range keys {
		if k == "" || k == "TERM" || k == "PWD" {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	out = append(out,
		"TERM=xterm-256color",
		"PWD="+cwd,
		"HISTFILE=/dev/null",
		fmt.Sprintf("COLUMNS=%d", cols),
		fmt.Sprintf("LINES=%d", rows),
		"PS1="+`\w $ `,
	)
	return out
}
