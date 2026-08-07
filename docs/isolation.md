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
| **Containers** (in progress) | Full host isolation for the agent runtime | epic [#547](https://github.com/jonathanung/strike/issues/547) | `internal/container` CLI + Manager ([#582](https://github.com/jonathanung/strike/issues/582)/[#583](https://github.com/jonathanung/strike/issues/583)) | Runtime shipped; config/eject/launch UX follow E12.2+ — reuse `network.allow` shape |

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

## Network egress allowlist (`network.allow`)

Config `network.allow` (host, `*.suffix`, IP, CIDR) is the single policy source
for application-layer egress:

| Surface | Enforcement |
|---|---|
| `webfetch` / `websearch` | Dial/redirect checks via `sandbox.CheckNetworkAllow` |
| bash | **v1 preflight** on known clients (`curl`, `wget`, `ssh`, `scp`, `sftp`, `nc`/`ncat`/`netcat`), including common wrappers (`env`, `timeout`, `bash -c`, …). Destinations outside the list → tool error `network_denied` (timeline `errorCode`). Unparseable destinations on those clients fail closed when the list is non-empty. |
| OS sandbox profile | **Not** per-host: host net on by default; off only when webfetch+websearch+mcp are hard-deny on `*`. No bwrap/seatbelt/Windows host allowlist in v1. |
| Containers (#547) | Planned stronger plane; reuse the same allowlist shape |

Empty/`[]` allowlist = unrestricted **public** hosts (SSRF private blocks on
webfetch unchanged). `/sandbox explain` prints the allowlist and
`egress enforcement:` line (preflight vs OS gap). Prefer `webfetch` when you
need fetch semantics; bash preflight is best-effort argv parse, not a
transparent userspace proxy.

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

- `internal/container` — `Runtime` / `CLI`, per-repo `Manager` lifecycle
  (build/launch/attach/exec/stop/restart/destroy/clean), naming
  (`strike-<repo>-<hash>`), `com.strike.*` labels, build cache under
  `.strike/container/`, offline-testable `ExecFunc`.

Still planned: config JSON block (E12.2), Dockerfile eject (E12.3),
`--launch-inside-container` (E12.4), attach UX (E12.6), isolation badge (E12.7),
eval pool wiring (E12.10).

Until launch UX ships:

- Prefer OS sandbox + worktrees for day-to-day coding.
- `network.allow` is the shared **shape** for application egress and future
  container filters. Today: `webfetch`/`websearch` + bash **preflight** for
  curl/wget/ssh/scp/sftp/nc (deny `network_denied` when outside the list).
  OS bash networking remains all-or-nothing (`NoNetwork`); there is **no**
  per-host bwrap/seatbelt/Windows filter — `/sandbox explain` labels this as
  `egress enforcement: preflight` vs `OS host filter: none`.
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

## Tool-chain correlation (#891)

Permission rules are evaluated **per tool call**. Multi-step abuse can still
pass each hop (e.g. `read` a secrets-class path → `webfetch`/`bash` egress;
write a script → immediately execute it). Strike keeps a **content-free**
rolling correlator on the permission service for the active turn:

| State retained | Not retained |
|---|---|
| Tool/permission name, step class, path class | Tool output bodies |
| Normalized path key for executable writes only | Secret file bytes |
| Denial signatures (retry storms) | Cross-session history |

### v1 rules

| Rule id | Trigger | Default action |
|---|---|---|
| `sensitive_read_egress` | `read` of a sensitive-class path, then `webfetch` / `websearch` / `bash` within the lookback window | **ask** (even under `yolo`) |
| `write_exec_bash` | `write`/`edit` of an executable/script path, then `bash` that executes/sources that path | **ask** |
| `retry_storm` | ≥ N identical permission denials in-turn (same permission + pattern) | **deny** |

Reasons and timeline fields cite **prior tool names and classes** (and a
`chainId`), never secret bytes. `permission.decided` may carry `chainId`,
`chainRule`, and `chainSummary`; the run timeline copies `chainId` onto the
permission entry.

State is cleared on **turn end** and **interrupt** (`BeginTurn` / `EndTurn`).
Pending nodes are **capped** (default 64) so correlation cannot grow without
bound.

### Path classes (heuristic)

- **sensitive** — `.env` / `.env.*`, common key material (`*.pem`, `id_ed25519`,
  …), credential basenames, `secrets/` segments, kube/aws credential paths.
- **executable** — script extensions (`.sh`, `.py`, …), paths under `bin/` or
  `scripts/`.
- **normal** — everything else.

These heuristics are intentionally shallow. Semantic **action facts** (#888)
can refine path/network classes later; correlation does not require an
external audit DB.

### Authoring / extension notes

- Rules live in `internal/permission/chain.go` (`Correlator`). Keep new rules
  **content-free**: classify inputs, do not buffer tool output for matching.
- Prefer **ask** when a legitimate workflow might match; use **deny** for
  clear retry/abuse loops.
- Compose with (do not replace) OS sandbox, ruleset denies, and engine tool
  loop detection.

### Non-goals (v1)

- Full UEBA or cross-session ML
- Storing tool output bodies for correlation
- External audit database
- MCP/network tool classification without facts
- Replacing per-call permission rules or the OS sandbox

## Related docs

- [config.md](config.md) — sandbox dial, scheduler, worktrees, network.allow
- [usage.md](usage.md) — `/sandbox`, `/permission`, worktree UX
- [ARCHITECTURE.md](ARCHITECTURE.md) — cancel/deadline/backpressure, package map
- [harnesses.md](harnesses.md) — external harnesses are not OS-sandboxed today
- [protocol.md](protocol.md) — `permission.decided` chain fields (wire 1.13+)
