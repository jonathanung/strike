package container

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// LaunchInsideOpts configures re-exec of strike inside the managed container.
type LaunchInsideOpts struct {
	// StrikeBinary is the host path to the strike binary to mount/copy.
	StrikeBinary string
	// Args are argv for strike inside the container (without the binary name).
	// Must not include --launch-inside-container (stripped by caller).
	Args []string
	// WorkDirHost is the host path to mount as workspace (repo or session worktree).
	WorkDirHost string
	// ForceRebuild forces image rebuild.
	ForceRebuild bool
	// Replace replaces a drifted running container.
	Replace bool
	// Version for build hash / preflight.
	Version string
	// SkipPreflight for tests that inject a ready Manager.
	SkipPreflight bool
}

// LaunchInside builds/starts the repo container and exec's strike with a TTY.
// It replaces the current process semantics by running docker exec -it and
// returning the exit code via error (*ExitError) or nil.
func (m *Manager) LaunchInside(ctx context.Context, opts LaunchInsideOpts) error {
	if m == nil {
		return fmt.Errorf("container: nil manager")
	}
	cfg := m.Cfg
	if opts.WorkDirHost != "" {
		cfg.Workspace.HostPath = opts.WorkDirHost
	}
	m.Cfg = cfg

	if !opts.SkipPreflight {
		if err := Preflight(ctx, m.RT, m.Cfg, m.RepoDir, PreflightOpts{
			RequireDockerfile: true,
			CheckDrift:        true,
			Version:           opts.Version,
		}); err != nil {
			return err
		}
	}

	// Ensure image + running container (headless).
	id, err := m.Launch(ctx, LaunchOpts{
		ForceRebuild: opts.ForceRebuild,
		Replace:      opts.Replace,
		Headless:     true,
	})
	if err != nil {
		return err
	}

	bin := opts.StrikeBinary
	if bin == "" {
		bin, err = os.Executable()
		if err != nil {
			return fmt.Errorf("container: resolve strike binary: %w", err)
		}
	}
	bin, err = filepath.EvalSymlinks(bin)
	if err != nil {
		bin = opts.StrikeBinary
		if bin == "" {
			bin, _ = os.Executable()
		}
	}

	// Copy strike binary into the container at /usr/local/bin/strike (ephemeral).
	if err := m.RT.CopyTo(ctx, id, bin, "/usr/local/bin/strike"); err != nil {
		// fallback: try /tmp/strike
		if err2 := m.RT.CopyTo(ctx, id, bin, "/tmp/strike"); err2 != nil {
			return fmt.Errorf("container: copy strike binary: %w", err)
		}
		return m.execStrikeTTY(ctx, id, "/tmp/strike", opts.Args)
	}
	// ensure executable
	_, _, _, _ = m.RT.Exec(ctx, id, []string{"chmod", "+x", "/usr/local/bin/strike"}, ExecOpts{})
	return m.execStrikeTTY(ctx, id, "/usr/local/bin/strike", opts.Args)
}

func (m *Manager) execStrikeTTY(ctx context.Context, id, strikePath string, args []string) error {
	engine, err := m.RT.Resolve()
	if err != nil {
		return err
	}
	// docker exec -it -w workspace -e STRIKE_ISOLATION=container …
	work := m.Cfg.mountPath()
	argv := []string{"exec", "-it", "-w", work, "-e", "STRIKE_ISOLATION=container"}
	// Forward common credential env that CollectForwardedEnv would inject at create;
	// re-pass selected keys so late-set env still reaches the inner process.
	for _, e := range os.Environ() {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			continue
		}
		if shouldForwardInner(key, m.Cfg.Auth.ForwardEnv) {
			argv = append(argv, "-e", key)
		}
	}
	argv = append(argv, id, strikePath)
	argv = append(argv, args...)

	cmd := exec.CommandContext(ctx, engine, argv...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func shouldForwardInner(key string, patterns []string) bool {
	if key == "SSH_AUTH_SOCK" {
		return true
	}
	for _, p := range patterns {
		ok, err := filepath.Match(p, key)
		if err == nil && ok {
			return true
		}
	}
	return false
}
