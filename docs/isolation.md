# Isolation matrix

Strike layers several isolation mechanisms. They compose; none replaces the
others. This page is the residual map after the execution-sandbox epic
([#537](https://github.com/jonathanung/strike/issues/537), closed) and points at
container work ([#547](https://github.com/jonathanung/strike/issues/547)) and
session worktrees.

| Layer | What it isolates | Config / dial | Backend | Failure signal |
|---|---|---|---|---|
| **Permission ruleset** | *When* a tool may run (allow / ask / deny) | `permissionMode`, rules, presets | `internal/permission` + `internal/actionfacts` | `permission_denied` on tool result + timeline |
| **OS process sandbox** | *What* bash can touch (FS + optional net) | `sandbox`: `off` \| `read-only` \| `workspace-write` | Linux `bwrap`, macOS `sandbox-exec` | `sandbox_denied` + human reason when applied; degrade warning if backend missing |
| **Session worktree** | Tool CWD / git branch per root session | `session.worktree`: `off` \| `auto` \| `always` | `git worktree` under `.strike/worktrees/` | Soft-fail to launch cwd when not a git repo |
| **Scheduler pools** | Concurrent bash/model/build/test inside one process | `scheduler.limits` / presets | `internal/scheduler` | Wait / `scheduler.canceled`; not a security boundary |
| **Process resource caps** | Optional mem/CPU on a single subprocess | `ProcessSpec.Limits` (tool/harness) | Linux `prlimit` (`RLIMIT_AS`, `RLIMIT_CPU`) | Non-zero exit / signal; **no-op on non-Linux** (documented) |
| **Wall time** | Per-bash and per-turn deadlines | bash `timeoutMs`, `TurnTimeout` | context cancel + process-group kill | `timeout` / `canceled` |
| **Containers** (planned) | Full host isolation for the agent runtime | epic [#547](https://github.com/jonathanung/strike/issues/547) | Docker/devcontainer (Zone port) | Not shipped — reuse `network.allow` shape |

## Action facts (semantic permission projection, #888)

Permission rules still match **globs** over the tool pattern (bash command
string, path, URL). For bash and selected tools, Strike also projects the
input into bounded **action facts** (`internal/actionfacts`): commands, paths,
and network hosts — without eval/exec.

| Parse outcome | Permission effect |
|---|---|
| **Authoritative** + enforcement-eligible | Each rule may match fact keys (program, `prog *` class, paths, `host:name`) **or** the raw pattern — never both for the same rule (no dual-eval deny). |
| Partial / unsupported / invalid / limit | Facts are diagnostic only; evaluation uses the **raw pattern** path. Deny never rests on non-authoritative facts. |

Bypass-shaped input (`eval`, `$()`, backticks, `base64 \| bash`, opaque
scripts) is classified non-authoritative so pattern-only policy applies (usually
default **ask**), rather than inventing a false deny.

`/permission explain` and `permission.decided` include `evalPath`
(`pattern`\|`facts`) and a short `factSummary` (programs/hosts/counts — not full
command text). Fact-backed rules compose with existing **last-match-wins**
layers (defaults → preset → config → … → managed ceiling).

**Non-goals (v1):** PowerShell/CMD parity; OPA/Rego; serializing raw facts into
public telemetry without redaction; OS egress filtering (see #892).

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

Full container / devcontainer isolation is a separate epic (absorb Zone runtime).
Until shipped:

- Prefer OS sandbox + worktrees for day-to-day coding.
- `network.allow` is the shared **shape** for future container egress filters
  (application-layer webfetch today; OS bash net remains all-or-nothing).
- Scheduler pool name `container` is reserved for future admission, not a
  running runtime.

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
