# Container runtime

Native containerization (epic
[#547](https://github.com/jonathanung/strike/issues/547)) lives in
`internal/container`. This page records the **E12.0** engine decision and the
package boundary. User-facing launch/attach/eject land in later E12 issues.

## Decision (E12.0 / #582): CLI shell-out

**Choice:** shell out to `docker` or `podman` via an injectable `ExecFunc`.
**Not chosen:** vendor the Moby/Docker Go SDK (`github.com/docker/docker/client`).

| Criterion | CLI shell-out | Moby SDK |
|---|---|---|
| Dependencies | stdlib `os/exec` only | large API + transitive tree |
| Podman | same argv when compatible | Docker API only (socket quirks) |
| API version negotiation | none (CLI handles it) | required |
| Matches strike practice | git worktrees, eval runners | new pattern |
| Interactive TTY attach | `docker exec -it` / PTY | hijack API (Zone still shelled out) |
| Unit tests | fake `ExecFunc` | mock generated client |

Zone's `internal/docker` used the SDK; the port (**E12.1 / #583**) rewrites
those call sites onto this CLI runtime rather than copying the SDK client.

Eval runners already shell out (`internal/eval/swebench.CLIRuntime`). **E12.10
/#592** should migrate them onto `internal/container` so one backend owns
create/exec/rm and scheduler pool leases.

## Package surface (foundation)

```text
internal/container
  ExecFunc / DefaultExecFunc   injectable CLI runner
  Runtime (interface)          Available, Pull, Create, Start, Stop,
                               Remove, InspectID, CopyFrom/To, Exec
  CLI                          production Runtime (field ExecFn ExecFunc)
  ContainerName / NetworkName  strike-<repo>-<sha256[:16]>
  Labels                       com.strike.* (was com.zone.*)
```

Higher-level Manager (build cache, Dockerfile materialize, launch-inside,
attach) is **not** in E12.0 — see E12.1–E12.6.

## Binary selection

1. Explicit `CLI.Binary` if set (`docker`, `podman`, or absolute path).
2. Else first of `docker`, `podman` on `PATH`.
3. `Available` runs `info --format {{.ServerVersion}}` and maps failures to
   `ErrEngineNotFound` / `ErrEngineUnavailable`.

## Architecture boundary

- `internal/tui` **must not** import `internal/container` (enforced by
  architecture boundary tests once wired).
- Status and isolation posture reach the UI through `host.Services` and
  protocol events (E12.7).
- Credentials are forwarded as env at launch time — never baked into images
  (E12.4).

## Related

- [isolation.md](isolation.md) — how containers compose with OS sandbox and worktrees
- [ARCHITECTURE.md](ARCHITECTURE.md) — package table
- [config.md](config.md) — future `container` / `execution` keys (E12.2, E12.4)
