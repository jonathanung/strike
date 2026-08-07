package swebench

import (
	"context"
	"fmt"

	"github.com/jonathanung/strike-cli/internal/container"
)

// ContainerRuntime adapts internal/container.CLI onto the swebench Runtime
// interface (E12.10 / #592). Eval runners share one CLI backend with Manager
// lifecycle code; scheduler pool leases stay in Runner.
type ContainerRuntime struct {
	CLI *container.CLI
}

// NewContainerRuntime returns a Runtime backed by the shared container CLI.
// engine is "docker", "podman", or empty (auto).
func NewContainerRuntime(engine string) *ContainerRuntime {
	return &ContainerRuntime{CLI: container.NewCLI(engine)}
}

// Available implements Runtime.
func (r *ContainerRuntime) Available(ctx context.Context) error {
	if r == nil || r.CLI == nil {
		return fmt.Errorf("swebench: nil container runtime")
	}
	return r.CLI.Available(ctx)
}

// Pull implements Runtime.
func (r *ContainerRuntime) Pull(ctx context.Context, image string) error {
	return r.CLI.Pull(ctx, image)
}

// Create implements Runtime.
func (r *ContainerRuntime) Create(ctx context.Context, image string, opts CreateOpts) (string, error) {
	return r.CLI.Create(ctx, image, container.CreateOpts{
		Name:       opts.Name,
		WorkDir:    opts.WorkDir,
		Env:        opts.Env,
		Entrypoint: opts.Entrypoint,
		Cmd:        opts.Cmd,
		HostBinds:  opts.HostBinds,
	})
}

// Start implements Runtime.
func (r *ContainerRuntime) Start(ctx context.Context, id string) error {
	return r.CLI.Start(ctx, id)
}

// CopyFrom implements Runtime.
func (r *ContainerRuntime) CopyFrom(ctx context.Context, id, src, dst string) error {
	return r.CLI.CopyFrom(ctx, id, src, dst)
}

// CopyTo implements Runtime.
func (r *ContainerRuntime) CopyTo(ctx context.Context, id, src, dst string) error {
	return r.CLI.CopyTo(ctx, id, src, dst)
}

// Exec implements Runtime.
func (r *ContainerRuntime) Exec(ctx context.Context, id string, cmd []string, opts ExecOpts) (string, string, int, error) {
	return r.CLI.Exec(ctx, id, cmd, container.ExecOpts{
		WorkDir: opts.WorkDir,
		User:    opts.User,
		Env:     opts.Env,
		Timeout: opts.Timeout,
	})
}

// Remove implements Runtime.
func (r *ContainerRuntime) Remove(ctx context.Context, id string) error {
	return r.CLI.Remove(ctx, id)
}

// Ensure ContainerRuntime satisfies Runtime at compile time.
var _ Runtime = (*ContainerRuntime)(nil)
