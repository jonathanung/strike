// Package keepawake holds an opt-in OS sleep-inhibition assertion for the
// gateway host so a phone-steered daemon does not sleep mid-turn (#1496).
//
// The assertion is a child process that owns the OS primitive for as long as
// it runs: systemd-inhibit on Linux, caffeinate on macOS. Both are bound to
// the daemon pid so a hard-killed daemon still releases the lock; a clean
// toggle-off or Close kills the child. The service never escalates and never
// overrides lid-close policy — when the OS primitive is missing or refuses,
// Status says so instead of pretending.
package keepawake

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// State is the effective inhibition state exposed on the gateway surface.
type State string

const (
	// StateOff means the preference is off and nothing is held.
	StateOff State = "off"
	// StateOn means the inhibitor child is running.
	StateOn State = "on"
	// StateUnsupported means this OS has no usable primitive (no backend or
	// its binary is not in PATH). The toggle should render disabled.
	StateUnsupported State = "unsupported"
	// StateBlocked means the preference is on but the primitive failed to
	// start or exited; Reason carries the OS error.
	StateBlocked State = "blocked"
)

// Status is the wire shape (GET /v1/gateway → keepAwake).
type Status struct {
	// Enabled mirrors the persisted preference.
	Enabled bool `json:"enabled"`
	// Supported is false when no primitive is usable on this host.
	Supported bool  `json:"supported"`
	State     State `json:"state"`
	// Backend names the primitive ("systemd-inhibit", "caffeinate").
	Backend string `json:"backend,omitempty"`
	// Reason explains unsupported/blocked, or what an active assertion covers.
	Reason string `json:"reason,omitempty"`
	// Since is RFC3339 UTC of the current assertion start (on only).
	Since string `json:"since,omitempty"`
}

// Process is a started inhibitor child. The default is an os/exec wrapper;
// tests substitute an in-memory fake so CI never inhibits sleep.
type Process interface {
	// Wait blocks until the child exits and reports why (stderr folded in).
	Wait() error
	// Kill terminates the child (and its process group where supported).
	Kill() error
}

// Starter launches one inhibitor child by resolved path and argv.
type Starter func(path string, args []string) (Process, error)

// Backend is the per-OS primitive description.
type Backend struct {
	// Name is the executable looked up in PATH.
	Name string
	// Detail describes what the assertion covers while on.
	Detail string
	// Args builds argv (without Name) bound to the daemon pid.
	Args func(pid int) []string
}

// why is the human-readable inhibitor reason shown by the OS (systemd-inhibit
// --list, pmset -g assertions).
const why = "strike gateway keep-awake (opt-in)"

// backendFor returns the primitive for goos, or an unsupported reason.
func backendFor(goos string) (Backend, string) {
	switch goos {
	case "linux":
		return Backend{
			Name:   "systemd-inhibit",
			Detail: "systemd-inhibit sleep:idle (block); lid-close policy is not overridden",
			Args: func(pid int) []string {
				// tail --pid exits when the daemon dies, releasing the lock
				// even if the daemon never ran Close.
				return []string{"--what=sleep:idle", "--who=strike", "--why=" + why, "--mode=block",
					"tail", "--pid=" + fmt.Sprint(pid), "-f", "/dev/null"}
			},
		}, ""
	case "darwin":
		return Backend{
			Name:   "caffeinate",
			Detail: "caffeinate -s (prevents system sleep on AC power only)",
			Args: func(pid int) []string {
				// -w releases the assertion when the daemon exits.
				return []string{"-s", "-w", fmt.Sprint(pid)}
			},
		}, ""
	case "windows":
		return Backend{}, "keep-awake is not implemented on windows (SetThreadExecutionState pending)"
	default:
		return Backend{}, "no sleep inhibitor known for " + goos
	}
}

// Options configures a Service. The zero value is the production host.
type Options struct {
	// Backend overrides the runtime.GOOS primitive (tests).
	Backend *Backend
	// Unsupported, when Backend is nil and GOOS has no primitive, is the
	// reason reported; tests may force an unsupported host by setting both
	// Backend to a zero Backend and this reason.
	Unsupported string
	// Start launches the child. Nil uses os/exec.
	Start Starter
	// LookPath resolves Backend.Name. Nil uses exec.LookPath.
	LookPath func(string) (string, error)
	// PID is the daemon pid the child is bound to. Zero uses os.Getpid().
	PID int
	// Grace bounds how long SetEnabled waits for an immediate child exit so
	// a refused inhibitor reports blocked on the same call. Zero uses 250ms.
	Grace time.Duration
}

// Service is the preference→assertion state machine. Safe for concurrent use.
type Service struct {
	backend     Backend
	supported   bool
	unsupported string
	start       Starter
	pid         int
	grace       time.Duration

	mu      sync.Mutex
	enabled bool
	proc    Process
	gen     int
	since   time.Time
	failure string
}

// New builds a Service; the host primitive is resolved once here.
func New(opts Options) *Service {
	s := &Service{start: opts.Start, pid: opts.PID, grace: opts.Grace}
	if s.start == nil {
		s.start = execStart
	}
	if s.pid == 0 {
		s.pid = os.Getpid()
	}
	if s.grace <= 0 {
		s.grace = 250 * time.Millisecond
	}
	lookPath := opts.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if opts.Backend != nil {
		s.backend, s.unsupported = *opts.Backend, opts.Unsupported
	} else {
		s.backend, s.unsupported = backendFor(runtime.GOOS)
	}
	if s.backend.Name == "" {
		if s.unsupported == "" {
			s.unsupported = "no sleep inhibitor configured"
		}
		return s
	}
	if _, err := lookPath(s.backend.Name); err != nil {
		s.unsupported = s.backend.Name + " not found in PATH"
		return s
	}
	s.supported = true
	return s
}

// SetEnabled applies the preference: on acquires the assertion (idempotent
// while held), off releases it. The returned Status reflects an immediate
// child failure when one happens within the grace window.
func (s *Service) SetEnabled(on bool) Status {
	s.mu.Lock()
	s.enabled = on
	if !on {
		s.stopLocked()
		st := s.statusLocked()
		s.mu.Unlock()
		return st
	}
	var done chan struct{}
	if s.supported && s.proc == nil {
		done = s.acquireLocked()
	}
	s.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(s.grace):
		}
	}
	return s.Status()
}

// Status reports the effective state.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// Close releases any held assertion. The preference is left as persisted so a
// restart re-applies it.
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

// acquireLocked starts the child and a watcher; the returned channel closes
// when the child exits. On start failure the channel is pre-closed.
func (s *Service) acquireLocked() chan struct{} {
	done := make(chan struct{})
	p, err := s.start(s.backend.Name, s.backend.Args(s.pid))
	if err != nil {
		s.failure = "start " + s.backend.Name + ": " + err.Error()
		close(done)
		return done
	}
	s.gen++
	gen := s.gen
	s.proc = p
	s.since = time.Now()
	s.failure = ""
	go func() {
		err := p.Wait()
		s.mu.Lock()
		if s.gen == gen && s.proc == p {
			s.proc = nil
			s.failure = s.backend.Name + " exited"
			if err != nil {
				s.failure += ": " + err.Error()
			}
		}
		s.mu.Unlock()
		close(done)
	}()
	return done
}

func (s *Service) stopLocked() {
	if s.proc != nil {
		_ = s.proc.Kill()
		s.proc = nil
		s.gen++
	}
	s.failure = ""
}

func (s *Service) statusLocked() Status {
	st := Status{Enabled: s.enabled, Supported: s.supported, Backend: s.backend.Name}
	switch {
	case !s.supported:
		st.State = StateUnsupported
		st.Reason = s.unsupported
	case !s.enabled:
		st.State = StateOff
	case s.proc != nil:
		st.State = StateOn
		st.Reason = s.backend.Detail
		st.Since = s.since.UTC().Format(time.RFC3339)
	default:
		st.State = StateBlocked
		st.Reason = s.failure
		if st.Reason == "" {
			st.Reason = s.backend.Name + " is not running"
		}
	}
	return st
}
