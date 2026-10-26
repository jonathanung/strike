# Web UI planned panels

The Rust client (`webui/`) ships the surfaces the daemon can drive today:
tabbed live sessions, a chat transcript over `/v1/live/events`, and an
Activity panel over `GET /v1/team` plus `team.spawn` / `team.child_interrupt`.
Blocked children (`schema:handoff`, #1201) show as `blocked`.

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

## Not planned as a copy

Stall is already a real badge when the team roster sets `budget.stall`.
Retry is `team.spawn` of the same objective, which starts a new child; it
does not resume the old session. Stop is `team.child_interrupt`. None of
these need a new panel.
