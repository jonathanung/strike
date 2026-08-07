package protocol

import (
	"strings"
)

// Isolation posture ladder (E12.7). Labels state what the posture *is*;
// they do not grade it (no green checkmark semantics).
//
// Ordered roughly least → most host isolation:
//
//	host+yolo → host+default → host+sandbox → container → container+no-network
const (
	IsolationHostYolo       = "host+yolo"
	IsolationHostDefault    = "host+default"
	IsolationHostSandbox    = "host+sandbox"
	IsolationContainer      = "container"
	IsolationContainerNoNet = "container+no-network"
)

// IsolationEnvKey is the process env key set at launch (never infer from /.dockerenv).
const IsolationEnvKey = "STRIKE_ISOLATION"

// ComputeIsolation returns the ladder label for the current process posture.
//
// insideContainer should come from STRIKE_ISOLATION already starting with
// "container" (or an explicit launch path), not from /.dockerenv.
// containerNoNetwork is true when the managed container has network.mode=none.
// permMode and sandboxMode are the host dials (ignored when insideContainer).
func ComputeIsolation(insideContainer, containerNoNetwork bool, permMode PermissionMode, sandboxMode string) string {
	if insideContainer {
		if containerNoNetwork {
			return IsolationContainerNoNet
		}
		return IsolationContainer
	}
	pm := permMode.Normalize()
	if pm == PermissionModeYolo {
		return IsolationHostYolo
	}
	sb := strings.ToLower(strings.TrimSpace(sandboxMode))
	switch sb {
	case "", "workspace-write", "read-only", "readonly", "workspace_write":
		// Default product sandbox is workspace-write → host+sandbox.
		if sb == "" {
			// Unknown/empty: treat as default sandbox dial when not yolo.
			return IsolationHostSandbox
		}
		if sb == "off" || sb == "none" {
			return IsolationHostDefault
		}
		return IsolationHostSandbox
	case "off", "none":
		return IsolationHostDefault
	default:
		// Any other non-off sandbox mode counts as sandboxed host.
		return IsolationHostSandbox
	}
}

// ParseIsolationEnv reads a STRIKE_ISOLATION value. ok is false when empty/unknown.
func ParseIsolationEnv(v string) (posture string, ok bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", false
	}
	// Allow exact ladder labels and container* prefix.
	switch v {
	case IsolationHostYolo, IsolationHostDefault, IsolationHostSandbox,
		IsolationContainer, IsolationContainerNoNet:
		return v, true
	}
	if strings.HasPrefix(v, "container") {
		if strings.Contains(v, "no-network") || strings.Contains(v, "none") {
			return IsolationContainerNoNet, true
		}
		return IsolationContainer, true
	}
	if strings.HasPrefix(v, "host") {
		if strings.Contains(v, "yolo") {
			return IsolationHostYolo, true
		}
		if strings.Contains(v, "sandbox") {
			return IsolationHostSandbox, true
		}
		return IsolationHostDefault, true
	}
	return "", false
}

// IsolationDescribe is a short human sentence for /container and notices.
// Descriptive only — no value judgment.
func IsolationDescribe(posture string) string {
	switch posture {
	case IsolationHostYolo:
		return "agent on host; permission asks skipped (yolo)"
	case IsolationHostDefault:
		return "agent on host; OS sandbox off; normal permission dial"
	case IsolationHostSandbox:
		return "agent on host; OS process sandbox on for bash"
	case IsolationContainer:
		return "agent inside managed container; default container network"
	case IsolationContainerNoNet:
		return "agent inside managed container; network mode none"
	default:
		if posture == "" {
			return "isolation posture unknown"
		}
		return "isolation posture: " + posture
	}
}

// IsolationShort is the badge label (same as posture; kept for API symmetry).
func IsolationShort(posture string) string {
	if posture == "" {
		return "?"
	}
	return posture
}
