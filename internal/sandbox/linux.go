//go:build linux

package sandbox

import (
	"os/exec"
	"path/filepath"
	"strings"
)

const backendBwrap = "bwrap"

func probePlatform() availInfo {
	path, err := exec.LookPath(backendBwrap)
	if err != nil {
		return availInfo{
			warn: "bwrap not found on PATH; bash runs unsandboxed",
		}
	}
	// Probe the same flag shape Wrap emits (minus workdir bind) so locked-down
	// environments (no user namespaces) degrade at startup, not mid-command.
	cmd := exec.Command(path,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-net",
		"--die-with-parent",
		"--", "true",
	)
	if err := cmd.Run(); err != nil {
		return availInfo{
			warn: "bwrap is present but cannot run (user namespaces or bubblewrap blocked); bash runs unsandboxed",
		}
	}
	return availInfo{ok: true, name: backendBwrap}
}

// wrapPlatform builds:
//
//	bwrap --ro-bind / / [--bind $WORKDIR $WORKDIR] --dev /dev --proc /proc \
//	      --unshare-net --die-with-parent -- <argv...>
func wrapPlatform(argv []string, policy Policy) []string {
	if len(argv) == 0 {
		return nil
	}
	out := []string{
		backendBwrap,
		"--ro-bind", "/", "/",
	}
	if policy.Mode == ModeWorkspaceWrite {
		if wd := absWorkDir(policy.WorkDir); wd != "" {
			out = append(out, "--bind", wd, wd)
		}
	}
	out = append(out,
		"--dev", "/dev",
		"--proc", "/proc",
		"--unshare-net",
		"--die-with-parent",
		"--",
	)
	out = append(out, argv...)
	return out
}

func absWorkDir(workDir string) string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return ""
	}
	clean := filepath.Clean(workDir)
	abs, err := filepath.Abs(clean)
	if err != nil {
		return clean
	}
	// Prefer the real path so the bind matches the process Dir after symlink eval.
	if real, err := filepath.EvalSymlinks(abs); err == nil && real != "" {
		return real
	}
	return abs
}
