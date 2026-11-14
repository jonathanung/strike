# Script schedules: run a shell command on a schedule with no agent session

Status: design note for [#1614](https://github.com/jonathanung/strike/issues/1614).
Parent: web UI epic [#1273](https://github.com/jonathanung/strike/issues/1273).
Docs only. This file changes no code; implementation is split into the
follow-up issues at the end and each of them needs its own review.

## Verdict

**Go with conditions.** A script schedule is a fourth `source` on the
existing clock (`internal/schedule`), not a second scheduler. It reuses the
calendar, the store, the run history, the HTTP routes, the Schedule page,
the bash tool's process runner, the permission service, the OS sandbox dial,
the admission pools, and the audit sink. The only new mechanism is the
fire path: instead of `rootstart.Starter.StartRoot` opening a session, the
daemon runs `bash -c <command>` through `tool.RunProcess` and records what
came back.

The conditions are the five hard constraints from the issue, restated here
as design rules with the code that enforces each:

| # | Rule | Enforced by |
|---|---|---|
| 1 | Explicit opt-in per schedule; never a default; never a silent conversion | `source: script` is a distinct enum value validated in `schedule.prepare`; `Replace` refuses to change `source` to or from `script` (delete and recreate instead) |
| 2 | No agent path creates an armed script schedule | No tool, op, MCP bridge, or SDK call reaches `schedule.Service`; the only writer is the authenticated cockpit API, and script writes additionally require `confirm: true` (the same gate as adding an allow rule). A negative test pins both. |
| 3 | Same permission and sandbox posture as agent shell | The fire path evaluates `permission.Service.Peek("bash", command)` under the resolved mode, runs the hook ruleset, compiles `sandbox.Policy` from the same dial, and acquires the same `scheduler.PoolProcess` lease. Host mode stays first-class; a sandbox is never required. |
| 4 | Every run is audit-logged | New `script_run` family in `internal/trust/audit`, written for started, finished, failed, timed out, denied, skipped, and missed. |
| 5 | Banners stay generic | Notification title and body are built from the kind, the schedule name, and the exit code only; the command and the output never enter `Notification`. A negative test pins it. |

## Where this sits

Two things in the repo are called a scheduler. This design touches both,
in different roles:

| Package | Role today | Role for script schedules |
|---|---|---|
| `internal/schedule` | The `strike serve` clock: persisted cron / interval / once / heartbeat entries in `~/.strike/schedules.json`; `Service.Tick` plans a fire and calls `rootstart.Starter` | Owns the new `source: script`, its validation, its run records, and the fire path seam |
| `harness/scheduler` | In-process admission controller (named pools `process`, `build`, `test`, `model`, `container`); the bash tool acquires a lease before starting a process | The script fire path acquires the same lease through the same classification rules, so a nightly `make test` counts against the `test` pool like an agent's would. No second admission system (docs/ARCHITECTURE.md). |
| `internal/frontend/server/schedules.go` | `/v1/schedules` CRUD, pause/resume/run/history | Grows the script fields on the spec and run DTOs, the `confirm` gate, and one route to read a run's output |
| `internal/daemon/trigger` + `triggers.go` | Inbound events that start a session | Unchanged. A trigger is a different source (an event, not a timer). A trigger never fires a script in v1. |
| `webui/src/schedule.rs` | Schedule page | Grows the explicit script type in the create form and an exit code / output tail view in run history |

The existing fire path and the proposed one, side by side:

```
tick ──► planTick ──► Action "start" ──► rootstart.Starter.StartRoot ──► root session
                                     │
                                     └─ (source: script) ──► ScriptRunner.Run ──► bash -c, no session
```

`planTick` and `planRunNow` already decide *whether* to fire (enabled, due,
overlap, missed, DST). They stay untouched. `fire` branches on `sch.Source`:
for `script` it emits a `ScriptRequest` instead of a `rootstart.Request`,
and `Service.tickOne` / `RunNow` dispatch to a new `Hooks.Script` seam when
the request is a script. Three things around them do change, all in
`service.go` (follow-up S3):

- **In-flight set.** `Service` keeps `running map[scheduleID]*scriptRun`
  for the life of the process. `observe` reports `in.Busy = true` for a
  `script` schedule while its id is in that set; the session-based
  `hooks.Busy(LastSessionID)` is never consulted for scripts because
  `LastSessionID` is never set. `startedRun` / `noteIdle` keep their
  crash-reconcile meaning and are skipped for `script` schedules, so a
  persisted `started` run is never flipped to `finished` by the idle check
  while the process is alive.
- **Detach.** A script fire does not block `tickOne`: the runner starts the
  process, records `started`, and returns; completion is written from the
  runner's goroutine. `firing[id]` is released as today; overlap is carried
  by the in-flight set, not by `firing`. `RunNow` does the same and returns
  the `started` run.
- **Skip is recorded.** When `planTick` returns `skipped` because
  `in.Busy` is set, the run record and audit record are written as for any
  other skip. The silent `return` on `begin(id)` failure stays as the guard
  against two ticks racing inside one process.

Availability: `Service.Run` ticks when either `Hooks.Starter` or
`Hooks.Script` is set (today it returns at once when `Starter` is nil). A
nil `Hooks.Script` with a due `script` schedule records `error: script runs
unavailable` on the tick path and returns `ErrUnavailable` (503) from
`RunNow`, which is what a nil `Starter` does for session fires.

## Data model

Additions to `schedule.Schedule` (persisted in `schedules.json`, mode 0600):

| Field | JSON | Type | Notes |
|---|---|---|---|
| `Source` | `source` | existing | New value `script`. `kind` is unchanged: a script schedule can be cron, interval, once, or heartbeat. |
| `Command` | `command` | string | Required for `script`. Single shell string, max 16 KiB (same cap as `prompt`). Stored verbatim, never scrubbed (the user typed it; scrubbing happens on every read-side projection). |
| `Dir` | `dir` | string | Working directory. Empty means the workspace path (`Hooks.WorkDir(workspaceId)`), which falls back to the daemon cwd. When set it must be absolute and must exist at create time; it is re-checked at fire time. |
| `Timeout` | `timeout` | Go duration | Default `30m`, minimum `10s`, maximum `6h`. A timed-out run is killed and recorded `failed` with the timeout noted. |
| `SecretRefs` | `secretRefs` | []string | Optional `secret://env/NAME` refs resolved only at exec, exactly as the bash tool's `Context.BashSecrets`. Values never enter the record, the audit log, or the UI. |
| `ArmedAt` | `armedAt` | time | Set by the server when a script schedule is created or re-armed with `confirm: true`. A script schedule with zero `ArmedAt` is never fired (belt and braces: the server never stores one, and the clock refuses one). |
| `CommandHash` | `commandHash` | string | `sha256` of `Command` at arming time. The clock refuses to fire when the stored command no longer matches; `Replace` of a script schedule re-arms (it is a `confirm: true` write). |

Validation in `schedule.prepare` for `source: script`:

- `command` required and non-blank; `prompt`, `goalId`, `workflow` must be
  empty (one source, one payload).
- `overlap` is forced to `skip`; `queue` and `allow` are rejected with
  `ErrInvalid`. Two instances of one script never run concurrently (Q3).
- `missed` keeps its existing semantics (`skip` default, `once` catches up a
  single run).
- `permissionMode` is validated as today (`rootstart.ResolveMode`).

Additions to `schedule.Run` (newest-first, capped at 50 per schedule):

| Field | JSON | Notes |
|---|---|---|
| `StartedAt` / `EndedAt` | `startedAt` / `endedAt` | Wall clock; `FiredAt` stays the tick time |
| `ExitCode` | `exitCode` | `*int`; nil when the process never started (denied, skipped, missed, start error) |
| `OutputPreview` | `outputPreview` | Last 4 KiB of combined stdout+stderr (tail, see retention), passed through `redact.ScrubToolOutput` before store |
| `OutputRef` | `outputRef` | Run id of the sidecar tail file (see retention); empty when nothing was captured |
| `Decision` | `decision` | `allow` \| `armed` \| `deny` \| `hook_block`: what the permission layer said (see fire path) |
| `Status` | existing | New values: `failed` (non-zero exit or timeout), `denied` (permission or hook refused). Existing `started`, `finished`, `skipped`, `missed`, `error`, `interrupted` keep their meaning. `SessionID` stays empty for every script run. |

Status mapping on the schedule itself (`LastStatus`, `LastError`) follows the
existing `applyResult`. `LastSessionID` is never set by a script run; busy
for a script schedule comes from the in-flight set described above, not from
`firing[id]` (released when the tick returns) and not from a persisted
`started` run (which after a restart means `interrupted`, not busy).

### Output retention (Q1)

The issue's default was a 64 KB tail on the run record. The record lives in
`schedules.json`, one file for up to 100 schedules × 50 runs; 64 KB inline
would make that file up to 320 MB and every list call would read it. The
recommendation is therefore:

- **Inline:** `outputPreview`, last 4 KiB, scrubbed. Enough for an exit
  line and a stack trace head; this is what the run history row shows.
- **Sidecar:** `~/.strike/schedules/runs/<scheduleId>/<runId>.log`, mode
  0600, directory 0700, holding the last 64 KiB of combined output,
  scrubbed on write, prefixed with `… (N bytes dropped)` when the command
  printed more. There is no unbounded full-output file: bounded retention
  is a safety property, not just a disk one.
- **Tail, not head.** `RunProcess` today keeps the *first* `MaxOutput` /
  `SpillMax` bytes (`processStreamWriter.Write` appends until full, then
  drops); that is right for a model reading a tool result and wrong for a
  nightly `make test` whose failing assertion is on the last page. The
  observer cannot help either: `Write` returns before calling `report` once
  `max` is reached, so `ProcessObserver.Output` also sees only the head.
  S3 therefore adds `ProcessSpec.TailMax int` to `harness/tool`: when set,
  `processStreamWriter` keeps a ring of the last `TailMax` bytes alongside
  the head buffer and `ProcessResult.Tail` returns it. The bash tool leaves
  it zero and is unchanged. The runner sets `TailMax: 64 KiB`,
  `MaxOutput: 4 KiB` (the head is still useful as the first line of the
  preview when the output is short), `SpillMax: 0`. A unit test on
  `processStreamWriter` with a 1 MiB write asserts `Tail` ends with the
  last byte written.
- **Lifecycle:** the sidecar is deleted when its run falls off the 50-run
  cap, when the schedule is deleted, and by `OpenStore`'s reconcile pass for
  runs that no longer exist (crash between the two writes).
- **Read:** `GET /v1/schedules/{id}/runs/{runId}/output` streams the sidecar
  as `text/plain`; 404 when there is none. Attach-only may read it (it is
  history, like `/history`).

### Working directory and environment (Q2)

- Directory: explicit `dir`, else the workspace path, else the daemon cwd.
  Recorded on every run and in every audit record.
- Environment: **not** the daemon's environment. The issue's default
  ("the daemon's environment is inherited") is rejected because it would
  make a script schedule a credential-widening path relative to agent shell:
  the bash tool deliberately passes only `bashMinimalEnvKeys` (PATH, HOME,
  locale, toolchain roots, SSH agent socket; #1030) and injects secrets only
  through resolved refs. A script schedule uses exactly that builder. A
  command that needs a token declares `secretRefs`; each resolution is
  recorded as `secret_ref_use` (class and name hash only), the same as an
  agent's bash call.
- The command does not see `STRIKE_SERVE_TOKEN` or the cookie secret. This
  matters for the threat model below.

### Shell or argv (Q5)

A single shell string executed as `bash -c <command>`, which is literally
the argv the bash tool builds. The permission layer therefore sees the same
shape it sees for an agent: `Permission: "bash"`, `Patterns: [command]`,
and `actionfacts.Analyze` runs over the same text. No argv form in v1; an
argv form would need its own permission presentation and would not be
"the same posture".

## Fire path

For a due script schedule the runner (`cmd/strike/schedule_wire.go` wiring,
`internal/schedule` seam) does, in order:

1. **Guard.** `ArmedAt` set and `sha256(Command) == CommandHash`, else
   record `error` ("script schedule is not armed") and stop. Resolve `dir`
   and check it exists, else `error`.
2. **Mode.** `rootstart.ResolveMode(sch.PermissionMode, workspaceMode)`:
   workspace mode wins, then the schedule's mode, then `plan`. The process
   `--auto` / `--dangerously-skip-permissions` flags are not inherited
   (`processBypassEnabled(skip, unattended=true)` is already false).
3. **Permission.** Build a `permission.Service` from the same layers an
   unattended root gets: managed rules, global config, the project config
   under `dir`, under the mode from step 2. Then:
   - `Peek("bash", command)` → `Deny`: record `denied` with `decision: deny`.
     Managed and config denies always win; arming cannot override them,
     exactly as "Allow project" cannot override a deny today.
   - `Allow`: run with `decision: allow`.
   - `Ask`: there is nobody to ask. The arming confirmation *is* the human's
     answer for this exact command string, so the run proceeds with
     `decision: armed`. This is the one place a script schedule differs from
     a live ask, and it is why arming requires `confirm: true`, why the
     hash must still match, and why every run records the decision.
   - Hook rules (`permission.EvaluateHooks`, shell events) run against the
     command before start; a block records `denied` with
     `decision: hook_block`, and the existing `hook` audit family gets its
     usual record.
4. **Sandbox.** `permission.Service.CompileSandbox(dial, dir)` produces
   the `sandbox.Policy`; its first argument is the daemon's resolved
   `sandbox.Mode` dial, not the permission mode. The per-run service must
   have `SetPermissionMode(mode)` applied first, because plan's write-denies
   live in the `modeLate` layer and only reach the compiled policy through
   it. The dial is the user's: `off` runs on the host (first-class, #1290
   standing rule);
   `bwrap` / `seatbelt` confine when available; degrade stays fail-closed
   unless `sandboxAllowDegrade` is set. Under `plan` with the sandbox on,
   writes are denied at the OS layer; with the sandbox off they are not,
   which is today's agent-bash posture under the same settings. The note
   says this out loud so nobody reads "plan" as "read-only shell".
5. **Admission.** Acquire the `harness/scheduler` lease for the command,
   after permission and before the process starts, so a cancelled waiter
   never starts a process. Classification is the bash tool's
   (`bashPoolsForCommand`), which is unexported and needs the compiled
   `SchedulerPolicy` to put `make test` in the `test` pool rather than
   `general`; S3 exports a small seam (`tool.PoolsForCommand(policy, cmd)`)
   and the wiring passes the daemon's `schedEff`. The schedule's `timeout`
   starts after admission (same as `RunProcess` `Timeout`).
6. **Run.** `tool.RunProcess(ctx, ProcessSpec{Argv: ["bash","-c",cmd], Dir,
   Env, Timeout, MaxOutput: 4 KiB, TailMax: 64 KiB, Combine: true,
   Sandbox: policy}, obs)`. The run is recorded `started` before exec (so a
   crash shows as `interrupted` on restart via the existing reconcile), then
   `finished` (exit 0) or `failed` (non-zero, timeout, sandbox denial).
   `ctx` is the daemon's run context; shutdown cancels the process.
7. **Record.** Mutate the schedule: run fields, `LastStatus`, `LastError`
   (exit line or timeout text), sidecar write, `UpdatedAt`.
8. **Audit and notify.** One `script_run` record and one notification (see
   below). Skipped (overlap) and missed ticks also produce a run record and
   an audit record; they produce no notification.

`RunNow` (`POST /v1/schedules/{id}/run`) takes the same path with the
calendar untouched, and returns `409` (`ErrBusy`) while the id is in the
in-flight set (overlap is always skip). The handler's 20 s request timeout
does not bound the script: the runner detaches into the daemon context and
`RunNow` returns the `started` run; the caller polls `/history` like it does
for a session fire.

### The one deviation: `Ask` is pre-answered

Step 3 is where a script schedule differs from an agent's bash call. An
unattended agent's `Ask` blocks until a cockpit reply or the context is
cancelled; a script schedule's `Ask` is answered by the
arming confirmation for that exact command string. The alternative reading
of constraint 3 is `Ask → denied`: only a command the user's rules already
`allow` may run, and arming grants nothing. That is stricter but makes the
feature useless under the `plan` default (shell asks in plan), so every
user would add an `allow bash <command>` rule by hand to use it, which is a
*wider* standing grant than the hash-pinned arming. This note recommends
`armed`, states it as the single product decision for the owner to confirm
on #1614 before S3 is filed, and keeps the fallback cheap: it is one branch
in step 3 and one row in the decision enum.

## Permission and trust implications

What changes in the daemon's posture, and what answers it:

- **Timer-initiated execution.** Today code runs because an agent asked and
  a permission flow answered. A script schedule runs because a timer fired.
  The answer is to move the human decision to arming time (`confirm: true`,
  exact command hash) and to keep every policy layer that would apply to an
  agent's bash call: denies still win, hooks still block, the sandbox dial
  still applies, admission still queues. Nothing is evaluated more
  permissively than an agent call; one ask is pre-answered for one string.
- **Persistence.** An armed script schedule is a standing capability. It is
  visible on the Schedule page, in `GET /v1/schedules`, and in the audit log
  both when its lifecycle changes and on every fire (`script_run`, below).
  Today the schedule handlers write no audit record at all (`serve_op` covers
  `/v1/ops` and WebSocket frames only), so arming, re-arming, pause, resume,
  and delete of a `script` schedule get explicit `script_run` lifecycle
  records carrying the command hash and the client IP; that is in S4. Pause
  stops fires without deleting history. Editing the command re-arms and
  re-confirms.
- **Agent reach.** No tool, `/v1/ops` op, MCP bridge, or SDK call touches
  `schedule.Service`. The agent's bash environment does not carry the serve
  token, so `curl` against `/v1/schedules` from inside a session fails auth
  unless the agent recovers the token some other way. The direct path on a
  host-mode machine is reading the `--token-file` the daemon minted or was
  given (`cmd/strike/serve.go`, `loadOrMintTokenFile`) with the `read` tool
  or `cat`; that is the same exposure every cockpit write has today, not one
  this feature adds. S4 should add a managed `deny read` / `deny bash` rule
  for the configured token file path at serve start; the path is
  user-chosen, so the basename list in `permission.ClassifyPath` (a
  correlator classification, not a deny) cannot cover it, and `Defaults()`
  has no path denies today.
  The residual path specific to this feature is social engineering of the
  person at the cockpit (below). Agent *proposals* are out of scope for v1 and gated on #1445's
  proposal pattern; when they arrive they create entries with zero
  `ArmedAt`, and only the cockpit's confirm step arms them.
- **Secrets.** Minimal env plus declared refs; refs are resolved at exec and
  recorded by class and hash only. The run record, the sidecar, the audit
  payload, and the notification body all pass through `pkg/redact`.
- **Notifications.** `notify.go` builds records from protocol events today;
  script runs emit no protocol events. The daemon adds a direct
  `Notification` with kinds `schedule_script_finished` / `_failed`, title
  from the kind, body `"<name>: exit <code>"` (name through `safeLabel`),
  href to the run in the Schedule page. The command and output are not
  parameters of that constructor, so they cannot leak by accident.

### Threat model

The shape is #1445's social engineering, made persistent and unattended.

| Attack | What the design does |
|---|---|
| A prompt-injected agent writes a schedule that runs `curl … \| sh` nightly | No agent path exists; a negative test asserts the tool registry, op set, and SDK expose nothing that reaches `schedule.Service`. The bash env carries no serve token. |
| The agent talks the user into pasting a command into the Schedule form | The form shows the full command untruncated before the confirm step, states the resolved mode and sandbox dial, and the arming is written as a `script_run` lifecycle record (`status: armed`, command, hash, client IP). Deny rules the user already has still apply at fire time. This is the same residual risk as the user running the command themselves. |
| A later edit swaps a benign command for a harmful one | `Replace` on a script schedule requires `confirm: true` again and re-arms; the re-arm record carries the new command and hash and the previous `armed` record carries the old ones, so the swap is visible in the audit log. |
| A compromised `schedules.json` on disk | File is 0600; a changed command does not match `CommandHash`, so the clock records `error` and does not fire. (An attacker who can write that file can also rewrite the hash, so this is tamper-evidence against accident and partial access, not against root.) |
| Runaway or fork-bombing command | Per-schedule timeout (default 30 m, max 6 h) kills the process group; the admission lease bounds concurrency across the process; output capture is bounded. |
| Output or env leaks credentials into history | Scrub on store for preview and sidecar; audit payload fields annotated `redact=scrub`; env is the minimal set. |
| Banner on a shared screen reveals the command | Banner constructor takes name and exit code only; test asserts the command and a marker string from the output never appear in title or body. |

## Cockpit surface

Schedule page (`webui/src/schedule.rs`), smallest change that honours the
constraints:

- Create form: `source` gains a fourth radio, **Script**, never preselected.
  Choosing it swaps the prompt textarea for a command field, a working
  directory field (placeholder: the workspace path), a timeout field, an
  optional secret refs list, and a confirm step that repeats the full
  command, the resolved mode, and the sandbox dial before `POST` with
  `confirm: true`. Editing a script schedule goes through the same confirm.
- List row: a "script" badge next to the kind; `lastStatus` shows
  `failed` / `denied` in the existing failure styling.
- Run history: exit code column, `outputPreview` in a monospace block, an
  **Open output** action that fetches `/runs/{runId}/output`. Session link
  hidden for script runs (there is none).
- Blocky theme tokens and the page's existing patterns; no new modal kinds.

API changes (`docs/web.md` Schedules section gets these rows):

| Method | Path | Change |
|---|---|---|
| `POST` | `/v1/schedules` | Accepts `source: script` with `command`, `dir`, `timeout`, `secretRefs`; requires `confirm: true` for that source, else `400 confirm required` |
| `PATCH` | `/v1/schedules/{id}` | Same gate for script schedules; refuses to change `source` to or from `script` |
| `POST` | `/v1/schedules/{id}/run` | `409` while a script run is in flight; returns the `started` run |
| `GET` | `/v1/schedules/{id}/runs/{runId}/output` | New; `text/plain` sidecar, `404` when none |
| `GET` | `/v1/schedules/{id}/history` | Run DTO gains `startedAt`, `endedAt`, `exitCode`, `outputPreview`, `outputRef`, `decision` |

Read-only servers reject all script writes as they reject every schedule
write today.

## Audit

New family in `internal/trust/audit`:

```go
FamilyScriptRun = "script_run"

type ScriptRunPayload struct {
    ScheduleID  string `json:"scheduleId" telemetry:"redact=none"`
    RunID       string `json:"runId,omitempty" telemetry:"redact=none"` // empty for lifecycle records
    Status      string `json:"status" telemetry:"redact=none"`
    // run:       started|finished|failed|denied|skipped|missed|error
    // lifecycle: armed|rearmed|paused|resumed|deleted
    Decision    string `json:"decision,omitempty" telemetry:"redact=none"` // allow|armed|deny|hook_block
    Mode        string `json:"mode,omitempty" telemetry:"redact=none"`
    Sandbox     string `json:"sandbox,omitempty" telemetry:"redact=none"`  // dial + applied/degraded
    Command     string `json:"command" telemetry:"redact=scrub"`
    CommandHash string `json:"commandHash" telemetry:"redact=none"`
    Dir         string `json:"dir" telemetry:"redact=scrub"`
    ClientIP    string `json:"clientIp,omitempty" telemetry:"redact=none"` // lifecycle records only
    StartedAt   time.Time `json:"startedAt,omitzero"`
    EndedAt     time.Time `json:"endedAt,omitzero"`
    ExitCode    *int   `json:"exitCode,omitempty"`
    Timeout     string `json:"timeout,omitempty" telemetry:"redact=none"`
    OutputTail  string `json:"outputTail,omitempty" telemetry:"redact=scrub"` // last 4 KiB
    Error       string `json:"error,omitempty" telemetry:"redact=scrub"`
}
```

One record per run outcome, including `skipped` and `missed`, plus one per
lifecycle change from the HTTP handlers (arm on create, re-arm on replace,
pause, resume, delete), via the existing
`Sink.Record(family, "", "", "", "", payload)` (no session, turn, or tool
call to correlate; `scheduleId` and `runId` are the correlation).

Two things the struct alone does not give you:

- **Redaction is per family, not per tag.** `redactPayload` in
  `internal/trust/audit/event.go` runs `telemetry.RedactRecord` only for the
  families it switches on; the `default` branch marshals unchanged, so the
  `redact=scrub` tags above are inert until S2 adds a
  `case FamilyScriptRun:` that decodes into `ScriptRunPayload` and calls
  `RedactRecord`. S2's negative test writes a sentinel token in `Command`,
  `OutputTail`, and `Error` and asserts it is absent from the segment file.
- **Which sink.** `strike serve` opens its own sink with
  `audit.Open(audit.Options{})` (`cmd/strike/serve.go`), which has **zero
  retention**; the 10 000-event / 90-day default belongs to the per-engine
  `openAuditSink(cfg)` in `cmd/strike/audit_bind.go`. A script schedule has
  no engine, so the clock would naturally be handed the unbounded one, and
  an `every: 1m` schedule behind a long command would append a `skipped`
  record per tick forever. S3's wiring passes the clock a sink opened
  through `openAuditSink(cfg)` (or moves serve's own sink onto it), and
  the clock receives it through a new `schedule.Hooks.Audit` func so the
  package stays free of the audit import. `docs/audit.md` gains the family
  row.

## Tests that carry the design

Negative first, as the issue asks:

1. **No agent path.** The tool registry, `/v1/ops` op set, SDK surface, and
   MCP bridge contain nothing that reaches `schedule.Service`; a script
   schedule cannot be created without `confirm: true`; a `PATCH` cannot turn
   a prompt schedule into a script schedule.
2. **No session.** A fired script schedule leaves `SessionID` empty on the
   run and the schedule, and the `LiveHub` has no new root (table test over
   cron, interval, once, heartbeat kinds).
3. **Overlap skip lands in audit.** Second due tick while the first run is in
   flight (process still running, tick already returned) records `skipped`
   on the schedule and a `script_run` record with `status: skipped`; no
   second process starts; the in-flight `started` run is not flipped to
   `finished` by the idle check.
4. **Banner is generic.** Notification title and body for a finished and a
   failed run contain neither the command nor a sentinel from the output.
5. **Posture parity.** A config deny for the command yields `denied` and no
   process; a hook block yields `denied` / `hook_block`; `--auto` on the
   daemon does not change the decision; a plan-mode run with the sandbox
   dial on gets a policy with writes denied; dial `off` runs on the host.
6. **Arming guard.** A stored schedule whose `command` was edited on disk
   (hash mismatch) records `error` and does not fire.
7. **Timeout.** A `sleep 60` with `timeout: 10s` is killed, recorded
   `failed`, with the timeout in `error` and the audit payload.
8. **Retention.** Preview is at most 4 KiB and scrubbed; sidecar is at most
   64 KiB; both hold the *end* of a 1 MiB output; deleting the schedule
   removes its sidecars; the 51st run drops the oldest sidecar.
9. **Audit is scrubbed and bounded.** A sentinel secret in the command,
   output, or error is absent from the written `script_run` record; the
   clock's sink has non-zero retention; arm, re-arm, pause, resume, and
   delete each write one lifecycle record with the command hash.

Evidence for the implementation PRs (screenshots at 1440×900, 1024×594,
390×844; UAT video with a passing and a failing command, the audit entry,
and the generic banner) is as listed in #1614 and belongs on the web UI
follow-up.

## Follow-up issues

Each is small enough for one PR. Headers follow the repo's
`wave` / `depends` / `blocks` / `conflicts` convention.

| # | Title | Wave | Depends | Conflicts | Tier |
|---|---|---|---|---|---|
| S1 | `internal/schedule`: `source: script` model, validation, run fields, sidecar store | 1 | — | S3 | C |
| S2 | `internal/trust/audit`: `script_run` family + payload + `redactPayload` case + docs row | 1 | — | — | C |
| S3 | Script fire path: `Hooks.Script` seam, in-flight set + detach in `service.go`, permission/hook/sandbox/admission, `ProcessSpec.TailMax` in `harness/tool`, retention-bounded sink wiring, no session | 2 | S1, S2 | S1 | C |
| S4 | `/v1/schedules` script fields, `confirm` gate, lifecycle audit records, run output route, token-file deny, `docs/web.md` | 2 | S1, S2 | — | C |
| S5 | Daemon notifications for script runs (generic title/body) | 3 | S3 | — | B |
| S6 | Schedule page: explicit Script type, run history exit code + output, evidence | 3 | S4 | — | B (webui; bundle rebuild by orchestrator) |
| S7 | Held: agent-proposed script schedules behind the #1445 proposal flow | 4 | #1445, S4 | — | design |

Wave 1 items are independent and can land in parallel. S3 and S4 touch
different packages but S4's DTO fields come from S1, and S3 edits
`service.go` / `decide.go` that S1 also touches, hence the conflict mark.
S7 is filed so the dependency is visible; it is not scheduled until #1445
has a reviewed design.

## Open questions resolved

| Q | Issue default | This note |
|---|---|---|
| Q1 output retention | 64 KB tail on the record; full output in a file; existing run-history retention | 4 KiB scrubbed preview on the record; 64 KiB scrubbed sidecar per run; no unbounded full file; sidecars follow the 50-run cap |
| Q2 working dir and env | explicit dir defaulting to the workspace; daemon env inherited; secrets posture stated | dir as proposed; env is the bash tool's minimal set plus declared secret refs, never the daemon env |
| Q3 overlap | skip, log the skip | skip only (`queue` / `allow` rejected for scripts); skip is a run record and an audit record |
| Q4 runaway | 30 m default, per-schedule | 30 m default, 10 s minimum, 6 h maximum; timeout recorded as `failed` with the duration |
| Q5 shell or argv | single shell string via the platform shell | `bash -c <command>`, identical to the bash tool, so the permission layer sees the same shape |

Two defaults were changed (Q1 size placement, Q2 env inheritance); both
changes narrow rather than widen. Everything else is as the issue proposed.
