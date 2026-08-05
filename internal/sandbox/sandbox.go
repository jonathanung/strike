// Package sandbox wraps subprocess argv with OS-level isolation primitives.
//
// Linux uses bubblewrap (bwrap); macOS uses seatbelt via sandbox-exec.
// When the backend is missing or cannot run (e.g. locked-down user
// namespaces), Wrap returns the original argv and emits a one-shot warning.
//
// Policy.Mode is the config/CLI sandbox dial (off|read-only|workspace-write).
// E1.5 will compile permission rules into the generated profile.
package sandbox

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// Mode is the filesystem posture applied by Wrap.
type Mode int

const (
	// ModeOff disables OS sandboxing (argv unchanged).
	ModeOff Mode = iota
	// ModeReadOnly mounts the host read-only (no writable workspace bind).
	ModeReadOnly
	// ModeWorkspaceWrite mounts the host read-only and re-binds WorkDir writable.
	ModeWorkspaceWrite
)

// DefaultMode is the product default when config/CLI omit sandbox.
const DefaultMode = ModeWorkspaceWrite

// String returns the canonical config/CLI token for m.
func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeReadOnly:
		return "read-only"
	case ModeWorkspaceWrite:
		return "workspace-write"
	default:
		return "off"
	}
}

// ParseMode resolves a user/config sandbox dial value.
// Empty input yields DefaultMode (workspace-write). Unrecognized values report false.
func ParseMode(value string) (Mode, bool) {
	normalized := strings.ToLower(strings.TrimSpace(value))
	normalized = strings.ReplaceAll(normalized, "_", "-")
	normalized = strings.ReplaceAll(normalized, " ", "")
	switch normalized {
	case "":
		return DefaultMode, true
	case "off", "none", "disable", "disabled", "false", "0", "no":
		return ModeOff, true
	case "read-only", "readonly", "ro", "read":
		return ModeReadOnly, true
	case "workspace-write", "workspacewrite", "write", "ws-write", "workspace":
		return ModeWorkspaceWrite, true
	default:
		return 0, false
	}
}

// ResolveMode returns ParseMode(value) or DefaultMode when value is empty/invalid.
// Prefer ParseMode when rejecting unknown config tokens.
func ResolveMode(value string) Mode {
	if m, ok := ParseMode(value); ok {
		return m
	}
	return DefaultMode
}

// ModeNames is the pipe-joined list of canonical mode tokens for error text.
func ModeNames() string {
	return "off|read-only|workspace-write"
}

// YoloWithoutSandboxError is returned when permissionMode yolo is combined with
// sandbox off without an explicit --i-know override.
const YoloWithoutSandboxError = "permissionMode yolo with sandbox off requires --i-know (OS isolation disabled)"

// CheckYoloSandbox reports an error when yolo is requested while the OS sandbox
// dial is off and iKnow is false. permMode should be a normalized permission
// mode string (e.g. "yolo"); other modes always pass.
func CheckYoloSandbox(permMode, sandboxMode string, iKnow bool) error {
	if strings.ToLower(strings.TrimSpace(permMode)) != "yolo" {
		return nil
	}
	if ResolveMode(sandboxMode) != ModeOff {
		return nil
	}
	if iKnow {
		return nil
	}
	return fmt.Errorf("%s", YoloWithoutSandboxError)
}

// Policy describes how Wrap should isolate a subprocess.
type Policy struct {
	// Mode selects the sandbox posture. Zero value is ModeOff.
	Mode Mode
	// WorkDir is the session workspace root. Required for ModeWorkspaceWrite
	// writable bind / seatbelt subpath; ignored when empty or Mode is Off.
	WorkDir string
}

// Result is the outcome of Wrap.
type Result struct {
	// Argv is the possibly-rewritten argument vector for exec.
	Argv []string
	// Applied is true when an OS sandbox launcher prefixes Argv.
	Applied bool
	// Backend is "bwrap", "sandbox-exec", or empty when not applied.
	Backend string
	// Degraded is true when Mode requested a sandbox but none was applied.
	Degraded bool
}

var (
	warnMu   sync.Mutex
	warned   bool
	warnSink io.Writer = os.Stderr
)

// SetWarnWriter redirects degradation warnings (tests). Nil resets to stderr.
func SetWarnWriter(w io.Writer) {
	warnMu.Lock()
	defer warnMu.Unlock()
	if w == nil {
		warnSink = os.Stderr
	} else {
		warnSink = w
	}
}

// ResetWarnForTest clears the one-shot warning latch (tests only).
func ResetWarnForTest() {
	warnMu.Lock()
	defer warnMu.Unlock()
	warned = false
	resetAvailabilityForTest()
}

// Available reports whether the platform sandbox backend is present and
// functional (probed once per process).
func Available() bool {
	return availability().ok
}

// BackendName returns "bwrap", "sandbox-exec", or "" when unavailable.
func BackendName() string {
	return availability().name
}

// Wrap prefixes argv with the platform sandbox launcher according to policy.
// ModeOff, empty argv, or an unavailable backend return argv unchanged
// (copy when non-nil). Degradation emits a single stderr warning per process.
func Wrap(argv []string, policy Policy) []string {
	return WrapResult(argv, policy).Argv
}

// WrapResult is like Wrap but reports whether the sandbox was applied.
func WrapResult(argv []string, policy Policy) Result {
	if len(argv) == 0 || policy.Mode == ModeOff {
		return Result{Argv: cloneArgv(argv)}
	}
	info := availability()
	if !info.ok {
		warnUnavailable(info)
		return Result{
			Argv:     cloneArgv(argv),
			Degraded: true,
		}
	}
	wrapped := wrapPlatform(argv, policy)
	if len(wrapped) == 0 {
		// Platform refused after a successful probe (e.g. profile write failure).
		// Surface the same one-shot warning path as an unavailable backend.
		warnUnavailable(availInfo{
			warn: "OS process sandbox failed to build launcher argv; bash runs unsandboxed",
		})
		return Result{
			Argv:     cloneArgv(argv),
			Degraded: true,
		}
	}
	return Result{
		Argv:    wrapped,
		Applied: true,
		Backend: info.name,
	}
}

// WarnUnavailable writes a startup warning when the OS sandbox cannot run.
// Safe to call multiple times; only the first degradation/startup notice is
// printed (shared latch with Wrap degradation).
func WarnUnavailable() {
	info := availability()
	if info.ok {
		return
	}
	warnUnavailable(info)
}

func warnUnavailable(info availInfo) {
	warnMu.Lock()
	defer warnMu.Unlock()
	if warned {
		return
	}
	warned = true
	w := warnSink
	if w == nil {
		w = os.Stderr
	}
	msg := info.warn
	if msg == "" {
		msg = "OS process sandbox unavailable; bash runs unsandboxed"
	}
	fmt.Fprintf(w, "strike: warning: %s\n", msg)
}

func cloneArgv(argv []string) []string {
	if argv == nil {
		return nil
	}
	out := make([]string, len(argv))
	copy(out, argv)
	return out
}
