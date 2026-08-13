package swebench

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Runtime is the container backend used for workspace materialization and
// grading. The CLI implementation shells out to docker(1). #592 may supply an
// internal/container-backed Runtime without changing the runner.
type Runtime interface {
	// Available reports whether the backend can run (docker binary + daemon).
	Available(ctx context.Context) error
	// Pull ensures image is present locally.
	Pull(ctx context.Context, image string) error
	// Create creates a stopped container; returns container id.
	Create(ctx context.Context, image string, opts CreateOpts) (string, error)
	// Start starts a created container.
	Start(ctx context.Context, id string) error
	// CopyFrom copies path src inside the container to host dst (file or dir).
	CopyFrom(ctx context.Context, id, src, dst string) error
	// CopyTo copies host src into the container at dst.
	CopyTo(ctx context.Context, id, src, dst string) error
	// Exec runs cmd in the container; returns stdout, stderr, exit code.
	Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (stdout, stderr string, code int, err error)
	// Remove force-removes the container.
	Remove(ctx context.Context, id string) error
}

// CreateOpts configures docker create.
type CreateOpts struct {
	Name       string
	WorkDir    string // container workdir
	Env        []string
	Entrypoint []string
	Cmd        []string
	// HostBinds are "host:container[:opts]" bind mounts.
	HostBinds []string
}

// ExecOpts configures docker exec.
type ExecOpts struct {
	WorkDir string
	User    string
	Env     []string
	Timeout time.Duration
}

// EvalImagePlatform is the OCI platform for official SWE-bench eval images.
// They are published as linux/amd64 only (sweb.eval.x86_64.*). Apple Silicon
// hosts must pull/create with this platform so Docker uses qemu instead of
// failing with "no matching manifest for linux/arm64".
const EvalImagePlatform = "linux/amd64"

// DockerImageName returns the Docker Hub image for a SWE-bench instance.
// Mirrors mini-swe-agent / SWE-bench convention:
//
//	docker.io/swebench/sweb.eval.x86_64.<id with __ → _1776_>:latest
func DockerImageName(instanceID string) string {
	id := strings.ReplaceAll(instanceID, "__", "_1776_")
	return fmt.Sprintf("docker.io/swebench/sweb.eval.x86_64.%s:latest", strings.ToLower(id))
}

// dockerPlatformArgs returns --platform linux/amd64 for official SWE-bench
// x86_64 images so Docker on Apple Silicon does not look for an arm64 manifest.
func dockerPlatformArgs(image string) []string {
	if !needsAmd64Platform(image) {
		return nil
	}
	return []string{"--platform", EvalImagePlatform}
}

func needsAmd64Platform(image string) bool {
	s := strings.ToLower(image)
	return strings.Contains(s, "x86_64") || strings.Contains(s, "amd64") || strings.Contains(s, "sweb.eval")
}

// CLIRuntime implements Runtime via the docker CLI.
type CLIRuntime struct {
	// Docker is the docker binary path (default "docker").
	Docker string
	// LookPath resolves binaries; defaults to exec.LookPath.
	LookPath func(string) (string, error)
	// Run runs a command; defaults to exec.CommandContext.
	// Tests inject fakes here.
	Run func(ctx context.Context, name string, args ...string) (stdout, stderr string, code int, err error)
}

func (r *CLIRuntime) bin() string {
	if r != nil && r.Docker != "" {
		return r.Docker
	}
	return "docker"
}

func (r *CLIRuntime) lookPath(file string) (string, error) {
	if r != nil && r.LookPath != nil {
		return r.LookPath(file)
	}
	return exec.LookPath(file)
}

func (r *CLIRuntime) run(ctx context.Context, name string, args ...string) (string, string, int, error) {
	if r != nil && r.Run != nil {
		return r.Run(ctx, name, args...)
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
			err = nil
		} else {
			return stdout.String(), stderr.String(), -1, err
		}
	}
	return stdout.String(), stderr.String(), code, nil
}

// Available implements Runtime.
func (r *CLIRuntime) Available(ctx context.Context) error {
	if _, err := r.lookPath(r.bin()); err != nil {
		return fmt.Errorf("swebench: docker not found on PATH: %w", err)
	}
	_, stderr, code, err := r.run(ctx, r.bin(), "info", "--format", "{{.ServerVersion}}")
	if err != nil {
		return fmt.Errorf("swebench: docker info: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("swebench: docker daemon not available: %s", strings.TrimSpace(stderr))
	}
	return nil
}

// Pull implements Runtime.
func (r *CLIRuntime) Pull(ctx context.Context, image string) error {
	args := append([]string{"pull"}, dockerPlatformArgs(image)...)
	args = append(args, image)
	_, stderr, code, err := r.run(ctx, r.bin(), args...)
	if err != nil {
		return fmt.Errorf("swebench: docker pull %s: %w", image, err)
	}
	if code != 0 {
		return fmt.Errorf("swebench: docker pull %s: exit %d: %s", image, code, strings.TrimSpace(stderr))
	}
	return nil
}

// Create implements Runtime.
func (r *CLIRuntime) Create(ctx context.Context, image string, opts CreateOpts) (string, error) {
	args := []string{"create"}
	args = append(args, dockerPlatformArgs(image)...)
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
	if len(opts.Entrypoint) > 0 {
		args = append(args, "--entrypoint", opts.Entrypoint[0])
		// docker create --entrypoint only takes the binary; remaining via cmd.
	}
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
	stdout, stderr, code, err := r.run(ctx, r.bin(), args...)
	if err != nil {
		return "", fmt.Errorf("swebench: docker create: %w", err)
	}
	if code != 0 {
		return "", fmt.Errorf("swebench: docker create: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	id := strings.TrimSpace(stdout)
	if id == "" {
		return "", fmt.Errorf("swebench: docker create: empty container id")
	}
	return id, nil
}

// Start implements Runtime.
func (r *CLIRuntime) Start(ctx context.Context, id string) error {
	_, stderr, code, err := r.run(ctx, r.bin(), "start", id)
	if err != nil {
		return fmt.Errorf("swebench: docker start: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("swebench: docker start: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// CopyFrom implements Runtime.
func (r *CLIRuntime) CopyFrom(ctx context.Context, id, src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		// dst may be a file path; parent dir is enough.
		if err2 := os.MkdirAll(parentDir(dst), 0o755); err2 != nil {
			return fmt.Errorf("swebench: mkdir for copy: %w", err)
		}
	}
	spec := id + ":" + src
	_, stderr, code, err := r.run(ctx, r.bin(), "cp", spec, dst)
	if err != nil {
		return fmt.Errorf("swebench: docker cp from: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("swebench: docker cp from: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// CopyTo implements Runtime.
func (r *CLIRuntime) CopyTo(ctx context.Context, id, src, dst string) error {
	spec := id + ":" + dst
	_, stderr, code, err := r.run(ctx, r.bin(), "cp", src, spec)
	if err != nil {
		return fmt.Errorf("swebench: docker cp to: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("swebench: docker cp to: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

// Exec implements Runtime.
func (r *CLIRuntime) Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (string, string, int, error) {
	if len(cmd) == 0 {
		return "", "", -1, fmt.Errorf("swebench: docker exec: empty command")
	}
	args := []string{"exec"}
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
	stdout, stderr, code, err := r.run(runCtx, r.bin(), args...)
	if err != nil {
		return stdout, stderr, code, fmt.Errorf("swebench: docker exec: %w", err)
	}
	return stdout, stderr, code, nil
}

// Remove implements Runtime.
func (r *CLIRuntime) Remove(ctx context.Context, id string) error {
	_, stderr, code, err := r.run(ctx, r.bin(), "rm", "-f", id)
	if err != nil {
		return fmt.Errorf("swebench: docker rm: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("swebench: docker rm: exit %d: %s", code, strings.TrimSpace(stderr))
	}
	return nil
}

func parentDir(path string) string {
	i := strings.LastIndex(path, "/")
	if i <= 0 {
		return "."
	}
	return path[:i]
}
