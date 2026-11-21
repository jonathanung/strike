package sandbox

import (
	"fmt"
	"strings"
	"sync"
)

// Runtime selects what isolates a wrapped command: the host process launcher
// (bwrap / sandbox-exec) or a microVM. Orthogonal to Mode (the filesystem
// posture). Config: sandboxRuntime. Only the user sets it (config / CLI); the
// model has no tool that writes it.
type Runtime int

const (
	// RuntimeProcess is today's bwrap / sandbox-exec launcher (default).
	RuntimeProcess Runtime = iota
	// RuntimeMicroVM runs wrapped commands in a KVM microVM. It never falls
	// back to the host: when unavailable, wraps are Degraded and callers fail
	// closed regardless of AllowDegrade.
	RuntimeMicroVM
)

// DefaultRuntime is the product default when config/CLI omit sandboxRuntime.
const DefaultRuntime = RuntimeProcess

// BackendMicroVM is Result.Backend for the microvm runtime.
const BackendMicroVM = "microvm"

// String returns the canonical config/CLI token for r.
func (r Runtime) String() string {
	if r == RuntimeMicroVM {
		return "microvm"
	}
	return "process"
}

// ParseRuntime resolves a sandboxRuntime value. Empty yields DefaultRuntime.
// Unrecognized values report false.
func ParseRuntime(value string) (Runtime, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	switch normalized {
	case "", "process":
		return RuntimeProcess, true
	case "microvm", "micro-vm":
		return RuntimeMicroVM, true
	default:
		return 0, false
	}
}

// ResolveRuntime returns ParseRuntime(value), or DefaultRuntime when empty.
// An unrecognized value resolves to RuntimeMicroVM: a typo in a stronger
// isolation request must fail closed, never silently pick the host. Config
// and CLI reject unknown tokens before this is reached.
func ResolveRuntime(value string) Runtime {
	if r, ok := ParseRuntime(value); ok {
		return r
	}
	return RuntimeMicroVM
}

// RuntimeNames is the pipe-joined list of canonical runtime tokens for error text.
func RuntimeNames() string {
	return "process|microvm"
}

// MicroVMWithSandboxOffError is the config error for sandbox off + microvm.
const MicroVMWithSandboxOffError = "sandboxRuntime microvm requires sandbox read-only or workspace-write (to turn isolation off, set sandboxRuntime: process and sandbox: off)"

// CheckRuntimeMode reports a config error when the microvm runtime is combined
// with sandbox off. Turning isolation off means picking the process runtime.
func CheckRuntimeMode(mode Mode, rt Runtime) error {
	if rt == RuntimeMicroVM && mode == ModeOff {
		return fmt.Errorf("%s", MicroVMWithSandboxOffError)
	}
	return nil
}

// DenyDegraded reports whether a Degraded wrap must fail closed. The microvm
// runtime always fails closed (sandboxAllowDegrade does not apply); the
// process runtime fails closed for non-off modes unless AllowDegrade.
func (p Policy) DenyDegraded(r Result) bool {
	if !r.Degraded {
		return false
	}
	if p.Runtime == RuntimeMicroVM {
		return true
	}
	return p.Mode != ModeOff && !p.AllowDegrade
}

// DeniedMessage is the user-facing reason for a fail-closed wrap.
func (p Policy) DeniedMessage(r Result) string {
	if p.Runtime == RuntimeMicroVM {
		reason := strings.TrimSpace(r.Reason)
		if reason == "" {
			reason = "unavailable"
		}
		return "sandboxRuntime microvm is unavailable: " + reason + " (no host fallback; set sandboxRuntime: process to run on the host)"
	}
	return "OS sandbox required but unavailable (set sandboxAllowDegrade: true to permit unsandboxed fallback, or sandbox: off)"
}

// MicroVMStatus is the static availability of the microvm runtime.
type MicroVMStatus struct {
	Available bool
	// Reason explains why the runtime is unavailable (empty when available).
	Reason string
}

var (
	microvmMu     sync.Mutex
	microvmProbed bool
	microvmCached MicroVMStatus
	microvmProbe  = probeMicroVM
)

// MicroVMAvailability returns the microvm runtime status, probing once per
// process. Only call this when the microvm runtime is selected: the probe
// opens /dev/kvm. Never called for the process runtime.
func MicroVMAvailability() MicroVMStatus {
	microvmMu.Lock()
	defer microvmMu.Unlock()
	if !microvmProbed {
		microvmCached = microvmProbe()
		microvmProbed = true
	}
	return microvmCached
}

// MicroVMStatusIfProbed returns the cached status without probing. ok is
// false when no probe has run yet (explain must not probe or boot).
func MicroVMStatusIfProbed() (MicroVMStatus, bool) {
	microvmMu.Lock()
	defer microvmMu.Unlock()
	return microvmCached, microvmProbed
}

// ForceMicroVMStatusForTest pins the microvm probe result (tests only).
func ForceMicroVMStatusForTest(st MicroVMStatus) {
	microvmMu.Lock()
	defer microvmMu.Unlock()
	microvmCached = st
	microvmProbed = true
}

func resetMicroVMForTest() {
	microvmMu.Lock()
	defer microvmMu.Unlock()
	microvmProbed = false
	microvmCached = MicroVMStatus{}
}

// wrapMicroVM handles WrapResult for the microvm runtime. No VM exec bridge
// exists yet (P1-d), so even an available runtime cannot rewrite argv: the
// wrap is Degraded and callers fail closed. Never returns host argv as
// Applied.
func wrapMicroVM(argv []string, policy Policy) Result {
	degraded := Result{Argv: cloneArgv(argv), Degraded: true}
	if err := CheckRuntimeMode(policy.Mode, RuntimeMicroVM); err != nil {
		degraded.Reason = err.Error()
		return degraded
	}
	st := MicroVMAvailability()
	if !st.Available {
		degraded.Reason = st.Reason
		return degraded
	}
	degraded.Reason = "microvm exec bridge is not implemented in this build"
	return degraded
}

// explainRuntime returns the runtime lines for Explain. Empty for the process
// runtime so default explain output is unchanged.
func explainRuntime(p Policy) string {
	if p.Runtime != RuntimeMicroVM {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "sandbox runtime: %s\n", p.Runtime)
	if err := CheckRuntimeMode(p.Mode, p.Runtime); err != nil {
		fmt.Fprintf(&b, "microvm status: config error: %s\n", err)
		return b.String()
	}
	st, ok := MicroVMStatusIfProbed()
	switch {
	case !ok:
		b.WriteString("microvm status: unknown (checked when bash first runs)\n")
	case st.Available:
		b.WriteString("microvm status: available\n")
	default:
		fmt.Fprintf(&b, "microvm status: unavailable: %s\n", st.Reason)
	}
	b.WriteString("microvm fallback: none (commands fail closed; sandboxAllowDegrade does not apply)\n")
	return b.String()
}

// microvmProfileText is ProfileText for the microvm runtime.
func microvmProfileText(p Policy) string {
	var b strings.Builder
	b.WriteString("(microvm runtime — no VM exec bridge in this build; wrapped commands are denied)\n")
	fmt.Fprintf(&b, "  mode: %s\n", p.Mode)
	fmt.Fprintf(&b, "  workspace-write: %v\n", p.WorkspaceWritable())
	return b.String()
}
