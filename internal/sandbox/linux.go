//go:build linux

package sandbox

import (
	"os/exec"
	"path/filepath"
	"strings"
)

const backendBwrap = "bwrap"

var bwrapPath string // absolute path from LookPath when probe succeeds

func clearLauncherForTest() { bwrapPath = "" }

func probePlatform() availInfo {
	path, err := exec.LookPath(backendBwrap)
	if err != nil {
		return availInfo{
			warn: "bwrap not found on PATH; bash runs unsandboxed",
		}
	}
	// Prefer absolute path so later exec does not re-resolve via a caller PATH.
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	// Probe the default Wrap shape (host networking shared — product default).
	// Avoid --unshare-net here: some environments allow shared-net bwrap but
	// fail loopback setup inside a new netns; default policy does not unshare.
	cmd := exec.Command(path,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--die-with-parent",
		"--", "true",
	)
	if err := cmd.Run(); err != nil {
		return availInfo{
			warn: "bwrap is present but cannot run (user namespaces or bubblewrap blocked); bash runs unsandboxed",
		}
	}
	bwrapPath = path
	return availInfo{ok: true, name: backendBwrap}
}

// wrapPlatform builds:
//
//	bwrap --ro-bind / / [--bind $WORKDIR $WORKDIR] [--bind $SHARED ...] \
//	      [--ro-bind $DENY $DENY ...] --dev /dev --proc /proc [--unshare-net] \
//	      --die-with-parent -- <argv...>
func wrapPlatform(argv []string, policy Policy) []string {
	if len(argv) == 0 {
		return nil
	}
	launcher := bwrapPath
	if launcher == "" {
		// Probe normally pins an absolute path. Fall back to LookPath, then the
		// bare name (unit tests that force Available without a real probe).
		if p, err := exec.LookPath(backendBwrap); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				launcher = abs
			} else {
				launcher = p
			}
		} else {
			launcher = backendBwrap
		}
	}
	out := []string{
		launcher,
		"--ro-bind", "/", "/",
	}
	if policy.WorkspaceWritable() {
		if wd := absWorkDir(policy.WorkDir); wd != "" {
			out = append(out, "--bind", wd, wd)
		}
	}
	// Temp always; caches only when workspace is writable (builds/package managers).
	for _, p := range SharedWritablePaths(policy.WorkDir, policy.WorkspaceWritable()) {
		out = append(out, "--bind", p, p)
	}
	// Remount deny paths read-only over the writable binds.
	for _, d := range existingDenyPaths(policy.DenyWritePaths) {
		out = append(out, "--ro-bind", d, d)
	}
	out = append(out,
		"--dev", "/dev",
		"--proc", "/proc",
	)
	if policy.NoNetwork {
		out = append(out, "--unshare-net")
	}
	out = append(out, "--die-with-parent", "--")
	out = append(out, argv...)
	return out
}

func profileText(policy Policy) string {
	if policy.Mode == ModeOff {
		return "(none — sandbox off)\n"
	}
	var b strings.Builder
	b.WriteString("bwrap \\\n")
	b.WriteString("  --ro-bind / / \\\n")
	if policy.WorkspaceWritable() {
		wd := absWorkDir(policy.WorkDir)
		if wd == "" {
			wd = "$WORKDIR"
		}
		b.WriteString("  --bind " + wd + " " + wd + " \\\n")
	}
	for _, p := range SharedWritablePaths(policy.WorkDir, policy.WorkspaceWritable()) {
		b.WriteString("  --bind " + p + " " + p + " \\\n")
	}
	for _, d := range existingDenyPaths(policy.DenyWritePaths) {
		b.WriteString("  --ro-bind " + d + " " + d + " \\\n")
	}
	for _, g := range policy.DenyWriteGlobs {
		// Globs without expanded paths still appear as comments for explain.
		if g = strings.TrimSpace(g); g != "" {
			b.WriteString("  # deny-write glob (expanded paths above when present): " + g + "\n")
		}
	}
	b.WriteString("  --dev /dev \\\n")
	b.WriteString("  --proc /proc \\\n")
	if policy.NoNetwork {
		b.WriteString("  --unshare-net \\\n")
	} else {
		b.WriteString("  # network shared with host\n")
	}
	b.WriteString("  --die-with-parent \\\n")
	b.WriteString("  -- <command>\n")
	return b.String()
}
