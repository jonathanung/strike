# Isolation matrix

Strike layers several isolation mechanisms. They compose; none replaces the
others. This page is the residual map after the execution-sandbox epic
([#537](https://github.com/jonathanung/strike/issues/537), closed) and points at
container work ([#547](https://github.com/jonathanung/strike/issues/547)) and
session worktrees.

| Layer | What it isolates | Config / dial | Backend | Failure signal |
|---|---|---|---|---|
| **Permission ruleset** | *When* a tool may run (allow / ask / deny) | `permissionMode`, rules, presets | `internal/permission` | `permission_denied` on tool result + timeline |
| **OS process sandbox** | *What* bash can touch (FS + optional net) | `sandbox`: `off` \| `read-only` \| `workspace-write` | Linux `bwrap`, macOS `sandbox-exec` | `sandbox_denied` + human reason when applied; degrade warning if backend missing |
| **Session worktree** | Tool CWD / git branch per root session | `session.worktree`: `off` \| `auto` \| `always` | `git worktree` under `.strike/worktrees/` | Soft-fail to launch cwd when not a git repo |
| **Scheduler pools** | Concurrent bash/model/build/test inside one process | `scheduler.limits` / presets | `internal/scheduler` | Wait / `scheduler.canceled`; not a security boundary |
| **Process resource caps** | Optional mem/CPU on a single subprocess | `ProcessSpec.Limits` (tool/harness) | Linux `prlimit` (`RLIMIT_AS`, `RLIMIT_CPU`) | Non-zero exit / signal; **no-op on non-Linux** (documented) |
| **Wall time** | Per-bash and per-turn deadlines | bash `timeoutMs`, `TurnTimeout` | context cancel + process-group kill | `timeout` / `canceled` |
| **Containers** (in progress) | Full host isolation for the agent runtime | epic [#547](https://github.com/jonathanung/strike/issues/547) | `internal/container` CLI shell-out to docker/podman ([#582](https://github.com/jonathanung/strike/issues/582)); Zone port | Foundation shipped; launch/attach/eject follow E12.1+ — reuse `network.allow` shape |

## Two-dial model (sandbox × permission)

| Dial | Controls | Does **not** control |
|---|---|---|
| **sandbox** | OS isolation for bash (paths, optional netns) | Permission prompts |
| **permissionMode** | Interactive / ruleset asks | OS mounts or seatbelt |

`yolo` does not disable the OS sandbox. `sandbox: off` does not skip asks.
`yolo` + `sandbox: off` requires `--i-know`. See [config.md](config.md) and
[usage.md](usage.md#os-sandbox-dial).

## OS sandbox (in-place, #537)

- **Default:** `workspace-write` — host root read-only, session workdir (and
  shared scratch: `/tmp`, caches) writable.
- **read-only:** no writable workspace bind.
- **off:** argv unchanged (no launcher).
- Permission hard-denies for `write`/`edit` compile into deny-write paths/globs
  (`permission.CompileSandbox`). Network inside the bash sandbox stays **on**
  unless webfetch, websearch, and mcp are all hard-deny on `*`.
- When `bwrap` / `sandbox-exec` is missing or blocked, bash **degrades** to
  unsandboxed with a one-shot startup warning (unless mode is `off`).
- Capability blocks that surface as OS errors (`Read-only file system`,
  `Permission denied`, seatbelt deny lines, …) are classified as
  **`sandbox_denied`** on the bash tool result (stable code + human reason) and
  appear on the run timeline as `errorCode=sandbox_denied`. Ordinary non-zero
  exits without those signals stay uncoded (model sees exit code in output).

Inspect: `/sandbox`, `/sandbox explain`.

**Non-goal:** reimplementing the landlock/bwrap stack. Residual work lives on
[#799](https://github.com/jonathanung/strike/issues/799), not a reopen of #537.

## Session worktrees

Per-root-session git worktrees bind tool CWD to
`<repo>/.strike/worktrees/<session-id>/`. Project-scoped stores (history,
memory, issues) stay on the main repo root. See [config.md](config.md#session-worktrees).

Worktrees isolate **files the agent edits** across concurrent roots; they do
not replace OS sandboxing of bash syscalls.

## Containers (#547)

Full container / devcontainer isolation is epic
[#547](https://github.com/jonathanung/strike/issues/547) (absorb Zone into
strike). **E12.0 decision ([#582](https://github.com/jonathanung/strike/issues/582)):**
shell out to `docker`/`podman` via injectable `ExecFunc` — do **not** vendor the
Moby SDK. See [container.md](container.md).

Shipped today:

- `internal/container` — `Runtime` / `CLI`, naming (`strike-<repo>-<hash>`),
  `com.strike.*` labels, offline-testable `ExecFunc`.

Still planned (E12.1+): config block, Dockerfile eject, `--launch-inside-container`,
attach, isolation badge, eval pool wiring.

Until launch UX ships:

- Prefer OS sandbox + worktrees for day-to-day coding.
- `network.allow` is the shared **shape** for future container egress filters
  (application-layer webfetch today; OS bash net remains all-or-nothing).
- Scheduler pool name `container` is reserved for admission once E12.10 wires
  eval onto this runtime.

## Resource limits (compose with scheduler)

| Limit | Mechanism | Portable? |
|---|---|---|
| Concurrent bash/model | scheduler pools | yes (in-process) |
| Wall time | `timeoutMs` / turn deadline | yes |
| Address space (RSS/AS) | `ProcessSpec.Limits.MemoryBytes` → `RLIMIT_AS` | **Linux only** |
| CPU time | `ProcessSpec.Limits.CPUSeconds` → `RLIMIT_CPU` | **Linux only** |

Non-Linux builds leave mem/CPU rlimits unset (no error). Callers that need hard
caps on macOS should use wall time and/or external container isolation (#547).

`prlimit` targets the **direct child** PID after `Start`. When bash runs under
`bwrap`/`sandbox-exec`, that PID is the launcher; the inner command may already
have forked, so mem/CPU caps are best-effort for sandboxed runs. Wall-time
`Timeout` still kills the process group reliably.

## Related docs

- [config.md](config.md) — sandbox dial, scheduler, worktrees, network.allow
- [usage.md](usage.md) — `/sandbox`, `/permission`, worktree UX
- [ARCHITECTURE.md](ARCHITECTURE.md) — cancel/deadline/backpressure, package map
- [harnesses.md](harnesses.md) — external harnesses are not OS-sandboxed today
