// Package container is strike's container engine backend for native
// containerization (epic #547).
//
// # Runtime decision (E12.0 / #582)
//
// Strike shells out to the docker(1) or podman(1) CLI. It does not vendor the
// Moby/Docker Go SDK.
//
// Rationale:
//   - Matches existing os/exec practice (git worktrees, eval runners).
//   - Free Podman support with the same argv surface.
//   - No Docker API version negotiation or SDK dependency bloat.
//   - Injectable ExecFunc keeps unit tests offline (same pattern as Zone's
//     network.ExecFunc and internal/eval/swebench.CLIRuntime).
//
// Zone's internal/docker used the Moby SDK; E12.1 (#583) ports lifecycle onto
// this CLI Runtime (Manager: build/launch/attach/exec/stop/restart/destroy/clean)
// with harness abstraction stripped. Config JSON layering is E12.2; Dockerfile
// eject is E12.3.
//
// Boundary: internal/frontend/tui must not import this package. Container status reaches
// the UI via host.Services and protocol events (see epic #547).
package container
