package container

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runtime is the low-level container engine backend.
// The production implementation shells out to docker or podman via ExecFunc.
// Higher-level lifecycle (build cache, attach, eject) builds on this in later
// E12 issues; eval runners (#592) should prefer this package over ad-hoc CLI.
type Runtime interface {
	// Engine returns the resolved CLI binary basename or path (e.g. "docker").
	Engine() string
	// Available reports whether the binary exists and the daemon responds.
	Available(ctx context.Context) error
	// Pull ensures image is present locally.
	Pull(ctx context.Context, image string) error
	// Create creates a stopped container and returns its id.
	Create(ctx context.Context, image string, opts CreateOpts) (string, error)
	// Start starts a created container.
	Start(ctx context.Context, id string) error
	// Stop stops a running container (graceful, then kill after timeout if set).
	Stop(ctx context.Context, id string, timeoutSec int) error
	// Remove force-removes a container.
	Remove(ctx context.Context, id string) error
	// InspectID returns the container id for nameOrID, or ErrNoContainer.
	InspectID(ctx context.Context, nameOrID string) (string, error)
	// CopyFrom copies path src inside the container to host dst.
	CopyFrom(ctx context.Context, id, src, dst string) error
	// CopyTo copies host src into the container at dst.
	CopyTo(ctx context.Context, id, src, dst string) error
	// Exec runs cmd in the container; returns stdout, stderr, exit code.
	Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (stdout, stderr string, code int, err error)
}

// CreateOpts configures container create.
type CreateOpts struct {
	Name       string
	WorkDir    string
	Env        []string
	Entrypoint []string
	Cmd        []string
	// HostBinds are "host:container[:opts]" bind mounts.
	HostBinds []string
	// Labels are applied as --label key=value.
	Labels map[string]string
	// Network is an optional user-defined network name.
	Network string
	// ExtraArgs are appended before the image (escape hatch for ports, resources).
	ExtraArgs []string
	// Platform is passed as --platform (e.g. linux/amd64 for SWE-bench images).
	Platform string
}

// ExecOpts configures container exec.
type ExecOpts struct {
	WorkDir string
	User    string
	Env     []string
	Timeout time.Duration
	// TTY requests -t (non-interactive attach still uses higher-level helpers).
	TTY bool
}

// CLI implements Runtime by shelling out to docker or podman.
type CLI struct {
	// Binary is the engine CLI ("docker", "podman", or absolute path).
	// Empty means auto-detect (docker, then podman).
	Binary string
	// ExecFn runs CLI commands; nil uses DefaultExecFunc.
	ExecFn ExecFunc
	// LookPath resolves binaries; nil uses exec.LookPath.
	LookPath func(string) (string, error)

	resolved string // cached binary after first resolve

	// Platform, when set, is passed as --platform on pull and create
	// unless CreateOpts.Platform overrides it. Used by eval so x86_64
	// SWE-bench images run under qemu on Apple Silicon.
	Platform string
}

// NewCLI returns a CLI runtime. binary may be empty for auto-detect.
func NewCLI(binary string) *CLI {
	return &CLI{Binary: strings.TrimSpace(binary)}
}

func (c *CLI) execFn() ExecFunc {
	if c != nil && c.ExecFn != nil {
		return c.ExecFn
	}
	return DefaultExecFunc
}

func (c *CLI) lookPath(file string) (string, error) {
	if c != nil && c.LookPath != nil {
		return c.LookPath(file)
	}
	return exec.LookPath(file)
}

// Engine implements Runtime.
func (c *CLI) Engine() string {
	if c == nil {
		return "docker"
	}
	if c.resolved != "" {
		return c.resolved
	}
	if c.Binary != "" {
		return c.Binary
	}
	return "docker"
}

// Resolve picks and caches the engine binary. Prefer explicit Binary, else
// docker on PATH, else podman.
func (c *CLI) Resolve() (string, error) {
	if c == nil {
		return "", ErrEngineNotFound
	}
	if c.resolved != "" {
		return c.resolved, nil
	}
	if c.Binary != "" {
		path, err := c.lookPath(c.Binary)
		if err != nil {
			// Allow absolute paths that LookPath rejects on some systems.
			if strings.Contains(c.Binary, string(os.PathSeparator)) {
				c.resolved = c.Binary
				return c.resolved, nil
			}
			return "", fmt.Errorf("%w: %s: %v", ErrEngineNotFound, c.Binary, err)
		}
		c.resolved = path
		return c.resolved, nil
	}
	for _, cand := range []string{"docker", "podman"} {
		if path, err := c.lookPath(cand); err == nil {
			c.resolved = path
			return c.resolved, nil
		}
	}
	return "", ErrEngineNotFound
}

func (c *CLI) bin(ctx context.Context) (string, error) {
	_ = ctx
	return c.Resolve()
}

func (c *CLI) run(ctx context.Context, args ...string) (stdout, stderr string, code int, err error) {
	bin, err := c.bin(ctx)
	if err != nil {
		return "", "", -1, err
	}
	return c.execFn()(ctx, bin, args...)
}

// Available implements Runtime.
func (c *CLI) Available(ctx context.Context) error {
	bin, err := c.Resolve()
	if err != nil {
		return err
	}
	_, stderr, code, err := c.execFn()(ctx, bin, "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEngineUnavailable, err)
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
		}
		return fmt.Errorf("%w: %s", ErrEngineUnavailable, msg)
	}
	return nil
}

// Pull implements Runtime.
func (c *CLI) Pull(ctx context.Context, image string) error {
	if strings.TrimSpace(image) == "" {
		return fmt.Errorf("container: pull: empty image")
	}
	args := []string{"pull"}
	if p := c.platform(); p != "" {
		args = append(args, "--platform", p)
	}
	args = append(args, image)
	_, stderr, code, err := c.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("container: pull %s: %w", image, err)
	}
	if code != 0 {
		return fmt.Errorf("container: pull %s: exit %d: %s", image, code, strings.TrimSpace(stderr))
	}
	return nil
}

func (c *CLI) platform() string {
	if c != nil && strings.TrimSpace(c.Platform) != "" {
		return strings.TrimSpace(c.Platform)
	}
	return ""
}

// Create implements Runtime.
func (c *CLI) Create(ctx context.Context, image string, opts CreateOpts) (string, error) {
	if strings.TrimSpace(image) == "" {
		return "", fmt.Errorf("container: create: empty image")
	}
	args := []string{"create"}
	platform := strings.TrimSpace(opts.Platform)
	if platform == "" {
		platform = c.platform()
	}
	if platform != "" {
		args = append(args, "--platform", platform)
	}
	if opts.Name != "" {
		args = append(args, "--name", opts.Name)
	}
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	for _, b := range opts.HostBinds {
		args = append(args, "-v", b)
	}
	for _, lv := range LabelArgs(opts.Labels) {
		args = append(args, "--label", lv)
	}
	if opts.Network != "" {
		args = append(args, "--network", opts.Network)
	}
	if len(opts.Entrypoint) > 0 {
		args = append(args, "--entrypoint", opts.Entrypoint[0])
	}
	args = append(args, opts.ExtraArgs...)
	args = append(args, image)
	if len(opts.Entrypoint) > 1 {
		args = append(args, opts.Entrypoint[1:]...)
	}
	if len(opts.Cmd) > 0 {
		args = append(args, opts.Cmd...)
	} else if len(opts.Entrypoint) == 0 {
		// Keep container alive for exec/cp when image entrypoint would exit.
		args = append(args, "sleep", "infinity")
	}
	stdout, stderr, code, err := c.run(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("container: create: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("container: create: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		return "", ErrEmptyID
	}
	return id, nil
}

// Start implements Runtime.
func (c *CLI) Start(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("container: start: empty id")
	}
	_, stderr, code, err := c.run(ctx, "start", id)
	if err != nil {
		return fmt.Errorf("container: start: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("container: start: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// Stop implements Runtime.
func (c *CLI) Stop(ctx context.Context, id string, timeoutSec int) error {
	if id == "" {
		return fmt.Errorf("container: stop: empty id")
	}
	args := []string{"stop"}
	if timeoutSec > 0 {
		args = append(args, "-t", fmt.Sprintf("%d", timeoutSec))
	}
	args = append(args, id)
	_, stderr, code, err := c.run(ctx, args...)
	if err != nil {
		return fmt.Errorf("container: stop: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("container: stop: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// Remove implements Runtime.
func (c *CLI) Remove(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("container: remove: empty id")
	}
	_, stderr, code, err := c.run(ctx, "rm", "-f", id)
	if err != nil {
		return fmt.Errorf("container: remove: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("container: remove: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// InspectID implements Runtime.
func (c *CLI) InspectID(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", ErrNoContainer
	}
	stdout, stderr, code, err := c.run(ctx, "inspect", "--format", "{{.Id}}", nameOrID)
	if err != nil {
		return "", fmt.Errorf("container: inspect: %w", err)
	}
	if code != 0 {
		msg := strings.TrimSpace(stderr)
		if msg == "" || strings.Contains(strings.ToLower(msg), "no such") {
			return "", fmt.Errorf("%w: %s", ErrNoContainer, nameOrID)
		}
		return "", fmt.Errorf("container: inspect: exit %d: %s", code, msg)
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		return "", fmt.Errorf("%w: %s", ErrNoContainer, nameOrID)
	}
	return id, nil
}

// CopyFrom implements Runtime.
func (c *CLI) CopyFrom(ctx context.Context, id, src, dst string) error {
	// Only ensure the parent exists. MkdirAll(dst) would create a directory
	// named like the destination file when dst is a file path.
	if err := os.MkdirAll(parentDir(dst), 0o755); err != nil {
		return fmt.Errorf("container: mkdir for copy: %w", err)
	}
	spec := id + ":" + src
	_, stderr, code, err := c.run(ctx, "cp", spec, dst)
	if err != nil {
		return fmt.Errorf("container: cp from: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("container: cp from: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// CopyTo implements Runtime.
func (c *CLI) CopyTo(ctx context.Context, id, src, dst string) error {
	spec := id + ":" + dst
	_, stderr, code, err := c.run(ctx, "cp", src, spec)
	if err != nil {
		return fmt.Errorf("container: cp to: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("container: cp to: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// Exec implements Runtime.
func (c *CLI) Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (string, string, int, error) {
	if len(cmd) == 0 {
		return "", "", -1, fmt.Errorf("container: exec: empty command")
	}
	if id == "" {
		return "", "", -1, fmt.Errorf("container: exec: empty id")
	}
	args := []string{"exec"}
	if opts.TTY {
		args = append(args, "-t")
	}
	if opts.WorkDir != "" {
		args = append(args, "-w", opts.WorkDir)
	}
	if opts.User != "" {
		args = append(args, "-u", opts.User)
	}
	for _, e := range opts.Env {
		args = append(args, "-e", e)
	}
	args = append(args, id)
	args = append(args, cmd...)

	runCtx := ctx
	var cancel context.CancelFunc
	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	stdout, stderr, code, err := c.run(runCtx, args...)
	if err != nil {
		return stdout, stderr, code, fmt.Errorf("container: exec: %w", err)
	}
	return stdout, stderr, code, nil
}

func parentDir(path string) string {
	dir := filepath.Dir(path)
	if dir == "" {
		return "."
	}
	return dir
}

// Ensure CLI satisfies Runtime.
var _ Runtime = (*CLI)(nil)
