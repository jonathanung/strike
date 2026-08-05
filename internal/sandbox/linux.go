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
	bwrapPath = path
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
