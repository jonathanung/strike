# RFC: NixOS microVM sandbox for agents

Status: **accepted for Phase 1 (go with conditions)**
Issue: [#1290](https://github.com/jonathanung/strike/issues/1290)
Inputs: the five angle notes and the Phase 0 spike under
[`docs/design/sandbox/`](../design/sandbox/) —
[architecture](../design/sandbox/architecture.md),
[runtime](../design/sandbox/runtime.md),
[security](../design/sandbox/security.md),
[self-config](../design/sandbox/self-config.md),
[product](../design/sandbox/product.md),
[phase0-report](../design/sandbox/phase0-report.md).

This RFC closes #1290. It does not repeat the angle notes. It settles the
two points where they disagree, maps the result onto code that exists
today (`harness/sandbox`), and turns the MVP into issues.

## Standing rule

From the owner, on #1290 (2026-10-26): **anything VM-related is a user
setting, on or off. VM sandboxing is never the only way to run agents.**
Host execution stays first class. A VM-related change that makes the VM
mandatory, removes the host path, or hides the toggle is a **blocking**
review finding. Every section below assumes this.

## Decision

**Go with conditions**, for a Phase 1 that is smaller than the
architecture note's MVP:

> The engine stays on the host. Commands that `harness/sandbox.Wrap`
> already wraps (bash tool, terminal panel) run inside a warm NixOS
> microVM for the workspace instead of under bwrap or sandbox-exec. The
> VM is a second sandbox **runtime** next to the bwrap / sandbox-exec launchers, selected by a user setting that
> defaults to today's behavior.

Conditions (all of them):

1. **Off by default.** New key `sandboxRuntime`: `process` (default,
   today's bwrap / sandbox-exec) | `microvm`. Only the user sets it (config,
   CLI, cockpit). The model cannot set it. Setting it back to `process`
   restores today's behavior exactly. (Not `sandboxBackend`: that name is
   already the probed launcher, `bwrap` / `sandbox-exec`, in the live
   status payload and the TUI.)
2. **Linux + working KVM only.** The probe has to create a vCPU, not just
   find `/dev/kvm` (the runtime note's host had a device node and still
   could not boot a guest). Everywhere else `microvm` reports `unavailable`
   with the reason.
3. **No silent host fallback.** If the user picked `microvm` and the VM is
   not ready, wrapped commands fail with a typed error. `sandboxAllowDegrade`
   does not apply to this runtime. Running on the host means the user
   switches the runtime to `process`.
4. **Nothing from `~/.strike` enters the guest.** No auth file, daemon token,
   session log, audit sink, host Nix store, or nix-daemon socket. Guest
   network is off by default.
5. **Measure before committing a budget.** Phase 0 ran under QEMU TCG
   (software emulation) and its timings are not a budget. Issue P1-a
   measures on a KVM host. If those numbers miss the kill thresholds in
   [Performance budget](#performance-budget), stop after P1-a.

### What this settles between the angle notes

| Question | Architecture note | Security / product notes | This RFC |
|---|---|---|---|
| Where the engine runs | Guest (option A). Option B (host engine, tools in guest) rejected as "a different product" | Host for the MVP. A guest engine needs a credential proxy (XL) | **Host engine for Phase 1.** A guest engine is a later decision, re-opened only after the egress proxy exists (see [Later](#later-not-filed)) |
| Config key | Workspace `runtime: host\|vm` | A stronger sandbox setting next to `off/read-only/workspace-write` | **`sandboxRuntime: process\|microvm`**, orthogonal to `sandbox` mode. Per-workspace override is P1-e |
| `session.childIsolation` | Unchanged. `vm` reserved for a later sibling slot | Unchanged | **Unchanged** (`off`/`shared`/`worktree`). No `vm` value in Phase 1 |
| VM grain | One per workspace, long-lived | One per run, stopped at session end | **One per workspace, booted on first wrapped exec, stopped when the last session on that workspace ends.** No idle keep-warm |
| Guest RAM | 4096 MiB / 2 vCPU | 2048 MiB placeholder | **2048 MiB / 2 vCPU default**, user-configurable. Re-set from P1-a's data |

Why the host engine, against the architecture note: it is the only shape
where Phase 1 needs **no** credential proxy, **no** host store clients in
the guest, and **no** protocol change on the session bus. It also attaches
at an existing seam (`Wrap`), so `process` vs `microvm` is one branch,
not a second engine path. The architecture note's objection is valid:
the language servers, `nix` rebuilds, and the model loop are not in the
guest. Phase 1 accepts that. Self-config (the agent editing its flake) only
needs `nix develop` inside the guest, and that is a wrapped command.

## How it maps onto `harness/sandbox`

Today (`harness/sandbox/sandbox.go`, `harness/tool/process.go`):

- `Policy` carries the filesystem posture (`Mode` off / read-only /
  workspace-write), `WorkDir`, deny paths and globs, `NoNetwork`, and
  `AllowDegrade`.
- `WrapResult(argv, policy)` rewrites argv with a platform launcher —
  `bwrap …` (`linux.go`) or `sandbox-exec -f <profile>` (`darwin.go`) —
  and returns `Applied`, `Backend`, `Degraded`.
- Two callers: `RunProcess` (bash tool and other subprocess tools) and the
  terminal manager (`internal/frontend/server/terminal/manager.go`, PTY
  `bash -i`). `RunProcess` fails closed on `Degraded` unless `AllowDegrade`.
- Availability is probed once per process (`avail.go`).

The microVM runtime keeps that contract. **Wrap still only rewrites argv.**

```
process runtime (today):
  bwrap --ro-bind / / --bind $WD $WD … --die-with-parent -- bash -c "<cmd>"

microvm runtime (proposed):
  strike vm exec --slot <slot> --cwd $WD [--no-workspace-write] -- bash -c "<cmd>"
```

`strike vm exec` is a short host-side shim (the same `strike` binary).
It dials the workspace slot's VSOCK port, sends one exec request (argv,
cwd, a filtered env, tty flag), copies stdin/stdout/stderr, forwards
SIGINT/SIGTERM/SIGKILL to the guest process group, and exits with the
guest's exit code. `RunProcess` and the terminal manager do not change:
they `exec` the shim exactly as they exec bwrap today, and the existing
process-group kill in `process_unix.go` reaches the shim, which forwards it.

Details that make the mapping hold:

| Concern | Rule |
|---|---|
| Paths | The guest mounts each workspace root over virtiofs **at the same absolute path** as on the host. `cmd.Dir`, argv paths, and tool output need no translation. File tools (`read`/`write`/`edit`) keep running in-process on the host against the same directory, so the host and guest see one tree (virtiofs cache mode must not hide writes; see runtime note). Only the roots are mounted at those paths: guest `HOME` is a fresh guest directory, never the host user's home, even when the workspace sits under `/home/<user>`. |
| `Mode` | `workspace-write` → workspace root mounted read-write. `read-only` / `NoWorkspaceWrite` (plan mode) → the exec request asks for the read-only mount view. This **closes** the known plan-mode gap where bash could still write on the host (security note, "Mounts"). `off` with `microvm` selected → treated as a config error at load; the user picks `process` to turn isolation off. |
| `DenyWritePaths` | Applied inside the guest by the guest exec agent as read-only bind mounts. bwrap inside the guest is optional extra isolation, not the boundary. |
| `NoNetwork` / `NetworkAllow` | The guest has no NIC in Phase 1, so `NoNetwork` is always effectively true. `NetworkAllow` preflight keeps running on the host as it does today. Networking in the guest waits for the egress proxy. |
| Env | `spec.Env` is filtered before it leaves the host. `BashSecrets` values are **not** forwarded by default; this matches condition 4. A per-secret opt-in is a later decision. |
| Probe | Availability for `microvm` is **static**: KVM vCPU probe + driver (`sandboxd` socket or `microvm-run`) present. It does not require a running slot, because the slot is booted lazily by the first exec. Probed once per process, as today; `/sandbox explain` does not probe or boot (same rule as `BackendUnprobed`). |
| `Result` | Available → `Applied: true`, `Backend: "microvm"`. Unavailable → `Degraded: true`, and `RunProcess` fails closed with `ErrSandboxDenied` **ignoring `AllowDegrade`** for this runtime (condition 3). The terminal manager refuses the same way. |
| Runtime failure | A boot, probe, hello, or attach failure happens after Wrap returned, inside the shim. The shim reports it on a status pipe the caller passes as an extra fd (`ExtraFiles`), which guest output cannot write to, and exits non-zero. `RunProcess` and the terminal manager map a status-pipe failure to `ErrSandboxDenied` so the model and the UI see a sandbox denial, not a command that exited 1. An exit code alone cannot carry this, because a guest program can return any value 0–255; guest exit codes pass through unchanged. |
| Explain | `ProfileText` prints the shim argv, slot, mounts, guest image hash, memory/vCPU. |
| Isolation ladder | `pkg/protocol/isolation.go` gains `vm` (and `vm+no-network` once the guest has a network). The host sets it, because the host booted the VM; never inferred from inside the guest. |

What does **not** go through `Wrap`: MCP servers, LSP, and plugin pane
processes still run on the host in Phase 1. `/sandbox explain` and the
cockpit text must say so instead of implying everything is in the VM.

## Plugin and service surface

| Layer | Owns | Must not own |
|---|---|---|
| `harness/sandbox` | `sandboxRuntime` selection, the `microvm` argv rewrite, explain text | `/dev/kvm`, systemd, NixOS |
| `internal/integrate/vm` (new, sibling of `container`) | `Driver` interface (`Boot`, `List`, `Kill`) with `dryrun`, `subprocess`, `systemd` backends; slot state | HTTP, TUI, provider calls |
| `cmd/strike` | Wires driver into `serve` and the shim; `strike vm exec` and `strike vm agent` subcommands | Hypervisor argv in UI code |
| `strike-sandboxd` (root system unit) + NixOS module `nix/modules/strike-sandbox.nix` | microvm.nix slot units, virtiofsd, `/run/strike/sandbox.sock` (0660 root:strike) | The tool loop, `~/.strike` |
| Guest image (flake output) | NixOS closure with the pinned `strike` package; `guest-agent` on VSOCK; no `wheel` for the agent user | Provider keys, host paths outside the workspace roots |
| Agent Plugin (optional, later) | A pane or skill that **reads** `/v1/vms` | Booting, KVM, secrets |

It is a **system service plus a sandbox runtime**, not an Agent Plugin.
`docs/plugins.md` trust classes do not cover opening `/dev/kvm` or
installing a root unit, and should not be stretched to. The user daemon
never opens `/dev/kvm`; it asks `sandboxd` over the unix socket.
Non-NixOS Linux uses the `subprocess` driver (a `microvm-run` built from
the flake, with the user in the `kvm` group). That is a dev path, documented
as such.

Proposed daemon routes (token-gated, host HTTP, not engine ops — the
architecture note's table, trimmed): `GET /v1/vms`, `GET /v1/vms/{slot}`,
`POST /v1/vms/{slot}/stop`, `GET /v1/vms/{slot}/console` (SSE log tail). One
new event, `sandbox.status` (`booting|ready|stopped|failed`, `bootMs`),
emitted by the host into the session JSONL. Additive; minor
`protocol.Version` bump when it lands.

## Boot / attach lifecycle

```
absent ──first wrapped exec──▶ booting ──guest-agent hello──▶ ready
   ▲                              │ timeout / probe fail          │
   │                              ▼                               │ last session on
   └──────── stopped ◀── stopping ◀── failed                       │ workspace ends,
                ▲                                                  │ or user Stop
                └──────────────────────────────────────────────────┘
```

1. **Boot.** Lazy: the first wrapped exec for a workspace with
   `sandboxRuntime: microvm` calls `Driver.Boot`. The shim waits on slot
   state with a deadline (default 120 s, configurable; Peasant had to raise
   their 30 s systemd timeout). `sandboxd` resets virtiofsd **before**
   starting the unit, as its own command (Peasant's deadlock).
2. **Probe.** Before `ready`, the guest runs the negative probe from the
   security note: no host canary, no `~/.strike`, no daemon or container
   socket, no default route. Any hit → `failed`, slot killed, typed error.
3. **Hello.** `guest-agent` sends `{protocolVersion, imageHash, slot}`.
   A version mismatch with the host → `failed` (a flake-lock problem, not
   a runtime one).
4. **Attach.** Each wrapped exec is one VSOCK connection. Several engines
   (live roots, children with `worktree` isolation) share one slot. Children's
   worktrees live under the workspace root, so they are already mounted.
5. **Interrupt.** Signal → shim → guest process group. If the guest does
   not answer within the grace period, the shim exits non-zero; repeated
   failure marks the slot `failed`, and `sandboxd` stops the unit.
6. **Stop.** On last session close for that workspace, user Stop, or daemon
   shutdown. Disk volume (guest store overlay, guest `HOME`) is kept;
   the next boot reuses it. No snapshots in Phase 1.
7. **Daemon restart.** `serve` calls `Driver.List` at start and either
   reattaches a `ready` slot whose hello still matches, or kills it. Never
   two slots for one workspace.
8. **Unattended runs** (schedules, triggers) on a `microvm` workspace with
   KVM missing are **skipped** with a reason, never run on the host
   (product note).

## Trust model

Summarized from the security note; it stays the normative source.

- **Untrusted:** everything in the guest — the agent's commands, the
  repository's code, anything they download.
- **Trusted computing base added by this RFC:** the hypervisor (Cloud
  Hypervisor; QEMU as the alternate), KVM, virtiofsd, `strike-sandboxd`
  (root), the guest image build, and the shim's VSOCK parser. The shim and
  guest-agent wire protocol is a parser on the boundary; it gets fuzz tests
  and length limits.
- **Host-only:** engine, provider HTTP client, `auth.json`, daemon token,
  session JSONL, audit sink, MCP clients, permission service. Model traffic
  never originates in the guest in Phase 1, so no credential proxy is needed.
- **Mounts:** an explicit allow-list — the session's workspace roots after
  the same symlink-safe canonicalization the host tools use. Nothing else.
- **Unchanged:** permission asks, audit, and content guard all run on the
  host, before the exec ever reaches the shim. A VM does not make prompt
  injection harmless. It limits what a successful injection can reach on
  the host.
- **Not fixed by the VM:** exfiltration through the workspace itself (the
  agent can write anything into the repo the user later pushes),
  exfiltration through host-side tools (MCP, `webfetch`), and anything the
  user pastes into a terminal. The cockpit text says this plainly.
- **`yolo`** does not widen mounts, turn on networking, or change the
  runtime.

## Performance budget

Phase 0 numbers (QEMU TCG, no KVM: 49.7 s to first turn, 882 MiB VMM RSS)
are **not** a basis for any budget. Peasant's KVM run reached guest
userspace in about 10 s on their hardware. The budgets below are targets
for P1-a to confirm or reject on a KVM host, with the bwrap path as the
baseline.

| Metric | Target (p50) | Kill threshold | Baseline today |
|---|---|---|---|
| Cold boot, `Boot` → `ready` (incl. probe) | ≤ 10 s | > 30 s | n/a |
| Warm exec overhead, `true` through shim vs bwrap | ≤ 50 ms | > 200 ms | measure (Phase 0's 35 ms was a probe script under bwrap, not exec overhead) |
| VMM host RSS at idle, guest configured 2048 MiB | ≤ configured + 128 MiB | > 2× configured | bwrap tree ~37 MiB |
| `go test ./...` on this repo, guest vs bwrap | ≤ 1.3× | > 2× | measure |
| Stop → process gone | ≤ 5 s | > 45 s (Phase 0 TCG shutdown hung) | n/a |

Admission: before `Boot`, `sandboxd` refuses if host `MemAvailable` is
below guest memory + 512 MiB, with a typed error. Phase 1 allows **one** slot
per daemon by default (product note); a second workspace queues.

If P1-a misses a kill threshold, the RFC flips to **no-go for interactive
use**. Container (`internal/integrate/container`) and bwrap stay the
isolation story, and P1-b onward are closed with a pointer.

## Recommendation

**Go**, scoped to Phase 1 below, behind the opt-in. Effort: **L** total
(one M probe/config slice, two M driver/bridge slices, S/M surface slices).
P1-a is the gate: P1-c onward wait on its numbers. P1-b boots no VM (setting, probe, fail-closed) and may land in parallel; until P1-d lands, `microvm` is always reported `unavailable`.

Not a go for: an in-guest engine, a credential proxy, per-child VMs,
snapshots, macOS/WSL microVMs, host Nix store sharing, agent-driven
`nixos-rebuild`. Each needs its own RFC with Phase 1 running first.

## Phase 1 issues

Filed as follow-ups of #1290 with `wave`/`depends` headers.

| Id | Issue | Wave | Depends | Tier |
|---|---|---|---|---|
| P1-a | [#1791](https://github.com/jonathanung/strike/issues/1791) KVM boot + exec measurement on a real host (spike) | 1 | none | A (evidence) |
| P1-b | [#1792](https://github.com/jonathanung/strike/issues/1792) `sandboxRuntime` setting, `microvm` probe, fail-closed, explain (no VM yet) | 1 | none | C |
| P1-c | [#1793](https://github.com/jonathanung/strike/issues/1793) `internal/integrate/vm` Driver (dryrun/subprocess), guest flake, `strike-sandboxd` + NixOS module | 2 | P1-a | C |
| P1-d | [#1794](https://github.com/jonathanung/strike/issues/1794) Exec bridge: `strike vm exec` shim + guest-agent over VSOCK; `Wrap` microvm runtime | 2 | P1-b, P1-c | C |
| P1-e | [#1796](https://github.com/jonathanung/strike/issues/1796) Boot-time negative probe, audit rows, `vm` isolation label, per-workspace override | 3 | P1-d | C |
| P1-f | [#1797](https://github.com/jonathanung/strike/issues/1797) Daemon `/v1/vms` routes + `sandbox.status` event | 3 | P1-d | C |
| P1-g | [#1798](https://github.com/jonathanung/strike/issues/1798) Web cockpit: runtime toggle, VM state chip, console tail | 4 | P1-f, #1532 | B |

### Later (not filed)

- **Guest engine** (architecture option A). Reopen once the egress proxy
  exists and Phase 1 has run long enough to show which tools users want in
  the guest (LSP, `nix` rebuilds).
- **Egress proxy / guest network** with host credential injection (security
  note, L). Needed for guest networking and for a guest engine.
- **Self-config beyond `nix develop`** (self-config note MVP: env diff on the
  approval card, ask-even-under-yolo for lockfile changes).
- **Sibling VM per child** (`childIsolation: vm`), snapshots, macOS vfkit, WSL2.
