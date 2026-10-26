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
would not be a second memory store and it would not embed a model.

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

Shipped. A session records `workspaceId` on its sidecar. See
[webui-daemon.md](webui-daemon.md) for how one daemon serves several
workspaces and what stays shared.

- Web-created roots (`POST /v1/roots`) use the active workspace directory,
  load that workspace's rules file into the prompt for that session, and
  start in its stored permission mode.
- Opening a workspace (`POST /v1/workspaces/{id}/activate`) applies the
  stored mode to the active live root with `set.permission_mode` when a
  mode is set. The composer chip can still change it afterwards.
- Delegated children whose working directory is outside the workspace, and
  outside `extraRoots`, are rejected. An empty `WorkspaceRoots` list (no
  workspace bound, including an empty registry) does not check, so plain
  `strike serve` is unchanged.
- The first engine started by `strike serve` stays in the process directory.
  If that directory is inside a registered workspace, the session records
  the id and the child check uses that workspace's roots. It does not jump
  to a different active workspace.
- Resume uses the recorded workspace directory. A missing workspace falls
  back to the process directory with the check off.
- Context-bundle `allowed_paths` is still a separate, per-spawn filesystem
  scope. It is not the workspace registry.
- Memory, history, plans, artifacts, and the ledger stay keyed by the
  process project, not by workspace id. The storage stream owns moving
  those.

Per-child git worktrees stay the existing opt-in: `session.childIsolation:
worktree` or spawn `isolation: "worktree"`. Default remains shared. Do not
add a second flag for the same behavior. A worktree that resolves outside
the workspace roots is rejected when the session is bound.

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
would have to be the real `!` shell path, which today is TUI-only.

## Composer triggers

The web composer does not parse `/command`, `@file`, `$skill`, or `!shell`.
The TUI expands `@file` and runs `!` shell. Dragging a file inserts `@path`
as plain text. Planned: the same expansion on `user.input` in the daemon,
so every client gets it, plus a `+` menu whose entries are only those real
actions. Upload and screenshot stay out until the daemon accepts those
attachments on the op.

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
