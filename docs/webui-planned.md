# Web UI planned panels

The Rust client (`webui/`) ships the surfaces the daemon can drive today:
a session list (live roots, saved sessions, client-side unread and pins,
delegated children nested under the open session), a transcript over
`/v1/live/events`, a composer that can steer or queue and set the real
permission mode, and a side panel with Activity, Files, and Changes.
Blocked children (`schema:handoff`, #1201) show as `blocked`. Unread and
pinned are browser storage, not server fields.

These panels are not in the client. Drawing empty versions of them would
pretend the daemon already has the Kiro-shaped workflow. Each one below is
the Strike-specific shape, using stores and events that already exist or are
the next real slice.

## Schedule

Recurring work is not a daemon resource yet. Strike has goal loops
(`GET/POST /v1/goals`, pause/resume/abort, a per-goal log) and workflow
documents (`/v1/workflows`), not a clock.

Planned: a Schedule list of goal or workflow runs with a cron or interval,
the next fire time, last status, and a link to the session the run wrote.
Creating one would persist beside `~/.strike` and the daemon would start a
root session when it fires. There is no scheduler process in `strike serve`
today, so the panel stays out until that exists.

## Memory

`GET/PUT/DELETE /v1/memory` is a project key/value store. The client does not
show it. A Memory panel would list those keys, the value, and who wrote it,
and would save through the same routes. It would not invent embeddings,
lessons, or cross-session search. Those are not in the store.

## Knowledge

No knowledge or document index is exposed on `/v1`. A Knowledge panel would
be a read view over files the daemon already serves (`/v1/files`,
`/v1/changed-files`) plus any future pinned paths from a context bundle. It
would not be a second memory store and it would not embed a model. The
document pane (`GET /v1/file/view`, `PUT /v1/file`) is that file read and
edit surface; it is not a knowledge index.

## Artifacts

`GET /v1/artifacts` and `artifact.updated` events exist, and completion
handoffs carry `artifact_refs`. The client does not list them. An Artifacts
panel would show id, type, version, and the producing child session, and
would open the body from `GET /v1/artifacts/{id}`. Version history is the
CAS version already on the artifact, not a new kind of object.

## Task runner

Plans exist (`/v1/plans`: sections, status, reopen). Delegations exist on
`GET /v1/team`. There is no spec → plan → run → checkpoint pipeline.

Planned: one plan opened as a checklist. Run would mean spawning a child
(`team.spawn`) per section, with that child's Activity row and handoff as
the checkpoint. Checkpoints would be the plan section body plus the child
verification report, not a hidden snapshot format. Until a run is defined as
that sequence, the client does not show a Task Runner board.

## Workspace binding

The registry (`~/.strike/workspaces.json`) now stores a directory, an
optional rules file inside that directory, and an optional permission mode.
That is the named workspace. It is not wired through to a running session:

- A session does not record which workspace it belongs to. The live engine
  working directory is still the directory `strike serve` was started in.
- Activating a workspace does not load its rules file into the prompt and
  does not call `set.permission_mode`.
- A delegated child's working directory is not checked against registered
  workspace directories. Adding that check before sessions run inside those
  directories would reject ordinary `strike serve` (the registry is often
  empty, and the serve directory may be unregistered).
- Context-bundle `allowed_paths` can already limit child filesystem tools.
  That is a bundle, not a workspace-registry check.

Planned: a session records a workspace id. The engine for that session uses
the workspace directory. Child spawns whose working directory is outside
that directory, and outside any extra roots recorded on the workspace, are
rejected. The rules file is injected as project rules for that session only.
The stored permission mode becomes the session's starting posture, still
changeable with `set.permission_mode`.

Per-child git worktrees stay the existing opt-in: `session.childIsolation:
worktree` or spawn `isolation: "worktree"`. Default remains shared. Do not
add a second flag for the same behavior.

## Onboarding

No first-run flow is wired to the web client. Strike has host onboarding
state, but the client does not show a privacy step, a theme picker, or a
coach-mark tour. Planned: a first-run card that states which local files
the daemon reads, then a single accent choice stored in browser settings.
It would not send telemetry. There is no telemetry export on this client.

## Gateway hub

Saved daemon URLs in the Daemon view are a bookmark list in
`localStorage`. They are not a hub. Planned: one switcher that reaches
several loopback gateways over SSH port forwards, with a short-lived token
minted on the host. Credentials stay on that host. The client must not open
a public listener or store provider keys.

## Memory mode

There is no incognito or temporary transcript mode on the daemon. A pill
for one would be a lie. Planned: a session flag that skips history and
memory writes for that root only, enforced in the engine, not only hidden
in the page.

## Side chat

A second read-only chat is not a daemon resource. Planned: a transcript
view that subscribes to events and cannot post `user.input`, so it cannot
approve a tool. Until that view exists, the only composer is the one with
the approval chip.

## Extra side tabs

Git status beyond `GET /v1/changed-files`, an embedded browser, a terminal,
and a subagent board that is not the Activity list are not in the client.
Activity is the subagent list. Changes is the diff list. A terminal tab
would be a persistent shell. Composer `!` already runs one command through
the daemon permission model.

## Composer triggers

`user.input` and `steer` expand in the daemon (`internal/composer/triggers`,
wired by `Options.PrepareInput`). `/command`, `@file`, `$skill`, and `!shell`
behave the same for every client. The web composer shows an autocomplete
popup and a `+` menu whose entries are only those real actions. The catalog
is `composerCommands` plus `skills` on `GET /v1/bootstrap`. `!` runs through
the live bash permission ask and does not start a model turn.

Upload and screenshot stay out. `UserInput.Images` exists on the wire, and
the web composer still does not send images.

Dragging a file inserts `@path`. Send attaches the file. A `---` file or
folder fence is opaque: only the user prefix is mention-scanned, so `@`
tokens inside an attached body are not new mentions. A `!` the TUI already
ran is not sent as `user.input`. Composer `!` during a live turn marks that
turn's checkpoint uncovered (`bash`), the same signal as the bash tool, and
does not take per-file snapshots.

## Context window hold

`status.contextUsed` is reported on bootstrap. The client does not block
Send when the window is full. Planned: a hold that refuses `user.input`
when the live status says the next turn would not fit, with the number
from the daemon. The client will not invent a limit.

## Not planned as a copy

Stall is already a real badge when the team roster sets `budget.stall`.
Retry is `team.spawn` of the same objective, which starts a new child; it
does not resume the old session. Stop is `team.child_interrupt`. None of
these need a new panel.
