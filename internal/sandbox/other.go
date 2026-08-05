//go:build !linux && !darwin

package sandbox

import (
	"fmt"
	"strings"
)

func clearLauncherForTest() {}

func probePlatform() availInfo {
	return availInfo{
		warn: "OS process sandbox is not supported on this platform; bash runs unsandboxed",
	}
}

func wrapPlatform(argv []string, policy Policy) []string {
	_ = policy
	// Unreachable when Available is false; Wrap degrades before calling.
	return cloneArgv(argv)
}

func profileText(policy Policy) string {
	if policy.Mode == ModeOff {
		return "(none — sandbox off)\n"
	}
	var b strings.Builder
	b.WriteString("(logical profile — no OS backend on this platform)\n")
	fmt.Fprintf(&b, "  mode: %s\n", policy.Mode)
	fmt.Fprintf(&b, "  workspace-write: %v\n", policy.WorkspaceWritable())
	fmt.Fprintf(&b, "  network: %v\n", policy.Network)
	if policy.NoWorkspaceWrite {
		b.WriteString("  no-workspace-write: true\n")
	}
	for _, g := range policy.DenyWriteGlobs {
		if g = strings.TrimSpace(g); g != "" {
			fmt.Fprintf(&b, "  deny-write glob: %s\n", g)
		}
	}
	for _, p := range policy.DenyWritePaths {
		if p = strings.TrimSpace(p); p != "" {
			fmt.Fprintf(&b, "  deny-write path: %s\n", p)
		}
	}
	return b.String()
}
