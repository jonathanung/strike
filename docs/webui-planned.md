# Web UI planned panels

The Rust client (`webui/`) ships the surfaces the daemon can drive today:
a session list (live roots, saved sessions, server-backed pins, archive,
and organisational folders, search across titles and recent transcript
text, delegated children nested in the conductor view), a transcript over
`/v1/live/events`, a composer that can steer or queue and set the real
permission mode, and a side panel with Activity, Files, Changes, Git, Worktrees,
Memory, Knowledge, and Artifacts.
Blocked children (`schema:handoff`, #1201) show as `blocked`. Folders group
sessions in the list; they are not directories on disk. Unread is still the
browser seen-map until the status stream (#1263) publishes a server field.
The list goes through `unread_flag(server, ...)` so that field can replace
the seen-map without a second model; the panel passes none until then.

The top bar and command palette are the shell in `webui/src/shell/`. Other
streams register a slot or a command by adding a file under
`webui/src/shell/contrib/`. The contract is [webui-shell.md](webui-shell.md).

The sections below that are still unshipped are not drawn. An empty panel
would pretend the daemon already has that workflow. Each one is the
Strike-specific shape, using stores and events that already exist or are
the next real slice. Memory, Knowledge, Artifacts, Memory mode, and
Triggers are the exception: they are in the client, and the notes under
those headings say what shipped.

## Goals and autopilot

The Goals view is in the client (`webui/src/goals.rs`). It creates a goal
with a verification command and budgets (turns, wall clock, tokens), then
runs `POST /v1/goals/{id}/run?background=1` and reads the log until the goal
leaves `pending` or `active`. The page planner is evaluate-only: Autopilot
stores an allow list for a planner that emits tool calls, and this page does
not emit them. Deny rules still block. Stall,
repeated verification failure, and a hard budget stop are enforced in the
goal harness, not only in the page.

A goal is not a schedule and not a task runner. A schedule, when it exists,
fires on a clock and may start a goal; it must not skip the goal guards.
A task runner walks a plan section by section and checkpoints each section.
The Goals view does not draw either of those.

## Schedule

Shipped. `strike serve` persists schedules in `~/.strike/schedules.json`
and exposes them at `/v1/schedules`. Kinds are cron, interval, one-shot,
and heartbeat. A fire starts a root session from a prompt, a goal, or a
workflow through `internal/rootstart` (the same seam inbound triggers
should call). The Schedule page can create, pause, resume, run now, edit,
delete, and open the session a run wrote.

Unattended runs use the workspace permission mode when that workspace has
one. Otherwise they use plan, which denies writes. They do not inherit
`strike serve --auto` or `--dangerously-skip-permissions`. Overlap defaults
to skip. A restart with missed fires defaults to skip (record one miss and
arm the next future slot); `missed: once` catches up a single run. A
heartbeat records a quiet baseline and does not start a session until the
workspace fingerprint changes. The first observation is that baseline.

The goal loop (`POST /v1/goals/{id}/run`) is still the evaluate-only
harness. A scheduled goal starts a root session whose prompt is the goal
text; it does not call that loop.

## Triggers

The Triggers page and `POST /hooks/{id}` are in the daemon. A webhook
(HMAC over `timestamp + "." + body`), a GitHub `issues` / `pull_request` /
comment event, or an in-process channel message can start or continue a
root. The channel adapter interface is `trigger.Adapter`; this build does
not dial Slack or any other chat network. Unattended runs resolve an empty
permission mode to plan. See [gateway.md](gateway.md).

## Memory

Shipped. The Memory tab lists `GET /v1/memory` for the project scope and for
a separate global scope (`scope=global`). Save and delete use the same
`PUT`/`DELETE /v1/memory/{key}` routes, with `author` and `scope`. Each write
is scrubbed with `pkg/redact` before it hits disk. The JSON file stays the
indexed store. A plain markdown projection and an append-only audit log are
rewritten beside it so the file can be opened and diffed. The agent still
reads the tagged subset (`preference`, `instruction`, `project-convention`)
at each turn, including the first turn of a session. Embeddings, lessons as
a separate object, and cross-session search are not in the store. Named
workspace scope is still [Workspace binding](#workspace-binding).

## Knowledge

Shipped as pinned paths, not a second memory store and not embeddings.
`GET/PUT/DELETE /v1/knowledge` records a path, a short note, and who pinned
it, in the project scope or the global scope. The panel can open the path
through `GET /v1/file`. The agent sees the pin list (path and note, not the
file body) when a session starts, under the same size cap as memory.
The document pane (`GET /v1/file/view`, `PUT /v1/file`) is a file read and
edit surface; it is not a knowledge index.

## Artifacts

Shipped as a read panel. `GET /v1/artifacts` lists id, type, version, and
owner for the open session. Opening a row loads `GET /v1/artifacts/{id}`,
and retained versions are the same artifact (`?version=`), capped at the
last 20 snapshots. `artifact.updated` refreshes the list. Title and content
are scrubbed on write. Browser create, edit, and delete stay out: those
writes remain on the agent tools. There is no browser write route.

## Task runner

Shipped as the Tasks view. One plan (`/v1/plans`) opens as a checklist.
`POST /v1/plans/{id}/run` with `{"action":"open","ownerRoot":"..."}` creates
the durable run (project file under `taskruns/`, same key as plans). Run
(`"action":"start"`) spawns a child (`team.spawn`) for every section whose
dependencies are approved or skipped. The child's handoff and verification
report are the checkpoint. The user approves, retries, skips, or reorders
from that board.

A section runs only after the user approves the previous checkpoint (or
skips it). A failed child, a `schema:handoff` block (#1201), or an
incomplete handoff marks the section failed and blocks dependants. Start
will not spawn those dependants. The run file survives a daemon restart;
an unbound claim (spawn never returned a child id) returns to pending.

Other streams start a run with the same route. `expectedVersion` is the
CAS token from the last response.

```
POST /v1/plans/{id}/run
{"action":"open|start|approve|retry|skip|reorder","ownerRoot":"...","sectionId":"...","sectionIds":[],"expectedVersion":1}
GET /v1/plans/{id}/run
```

`reorder` is a permutation of section ids. It re-chains only rows that
still depend on pending sections, so it cannot route around a failure.
Scheduler (#1276) and goals (#1284) should call `start` on this route
rather than spawning sections themselves.


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

The header and the Daemon view share one Gateways switcher. Profiles are
names and URLs in `localStorage` (`strike.gateways`). Bearers are
`sessionStorage` only. `strike connect` opens the SSH local-forward and
injects a short-lived grant on the laptop's loopback. The daemon still
refuses a public bind. See [gateway.md](gateway.md).

Not in this client: tailnet discovery, a browser-started SSH session, and
syncing CLI profiles from the daemon. The switcher shows an `ssh`
destination as a hint. It does not open that connection. A public tunnel
is still something you run yourself, and it is a public edge.

The desktop shell in `desktop/` (see [desktop.md](desktop.md)) is a separate
window. A Tailscale or other VPN URL is an explicit saved gateway: HTTPS
uses Web PKI or a pinned certificate, and cleartext HTTP is refused until
the user allows it. SSH runs `strike connect`, so the short-lived grant
stays in that process. The shell does not draw this switcher or the status
chip, and it does not exchange pairing codes.

## Memory mode

Shipped as a session flag, not a new protocol op. The composer pill and the
command palette set `PUT /v1/memory/mode?root=`. The engine enforces it:

- Normal reads and writes memory.
- Incognito still reads memory into the prompt. Agent writes are refused.
  The transcript is kept.
- Temporary does not load memory or knowledge pins into the prompt, and
  agent reads and writes are refused.

The flag is stored on the session meta sidecar and restored when that
session is opened again. The panel itself stays editable in every mode, so
a person can still change what the next normal session will see. History
skip for the transcript is not part of this flag.

## Side chat

Shipped as a drawer opened from the Activity header, an Activity row, the
composer chip, Ctrl+K, or Alt+S. It mirrors the session events the page
already subscribes to and posts questions to `POST /v1/sidechat/ask`. That
route builds a transcript from the session JSONL and makes a separate model
call with no tools, using the live root's current provider and model.
`focusSessionId` is accepted only for that root or a child on its roster.
Switching roots clears the drawer's focus. Turns are stored in
`sidechat/<session-id>.jsonl`, not the session log, so they are not
`user.input` and the session list does not treat the file as a root.

`POST /v1/sidechat/ops` refuses every protocol op. A `permission.reply`
from this channel cannot approve a tool; the daemon returns
`sidechat_read_only` and does not submit the op. The panel has no approval
control. Attach-only serve has no side-chat model, so ask returns 503 while
the ops refusal still holds. The echo provider answers by echoing the
question; a question that starts with `run ` is refused when the model
tries to call a tool.

`make webui-sidechat-uat` drives those paths in headless Chrome (composer,
Activity row, Enter / Escape / Alt+S / Ctrl+K, and the tool-call refusal)
and writes shots under `webui-uat-artifacts/side-chat/`. `make webui-uat`
stays the cockpit pass.

## Extra side tabs

Git status, branch, log, commit, and worktree list/create/remove are in the
client (`/v1/git/*`). The Git panel reuses `GET /v1/changed-files` for the
unstaged diff. Changing `session.childIsolation` stays in config; the
Worktrees panel shows the effective value and does not write a second flag.
An embedded browser, a terminal, and a subagent board that is not the
Activity list are not in the client. Activity is the subagent list. Changes
is the diff list. A terminal tab would be a persistent shell. Composer `!`
already runs one command through the daemon permission model.

## Composer triggers

`user.input` and `steer` expand in the daemon (`internal/composer/triggers`,
wired by `Options.PrepareInput`). `/command`, `@file`, `$skill`, and `!shell`
behave the same for every client. The web composer shows an autocomplete
popup and a `+` menu whose entries are only those real actions. The catalog
is `composerCommands` plus `skills` on `GET /v1/bootstrap`. `!` runs through
the live bash permission ask and does not start a model turn.

Image and file upload is `POST /v1/attachments` (5 MiB, eight per message,
filename redaction, optional PNG/JPEG region paint) and `images` on
`user.input` and `steer`. Screenshot capture is still out.

Dragging a file inserts `@path`. Send attaches the file. A `---` file or
folder fence is opaque: only the user prefix is mention-scanned, so `@`
tokens inside an attached body are not new mentions. A `!` the TUI already
ran is not sent as `user.input`. Composer `!` during a live turn marks that
turn's checkpoint uncovered (`bash`), the same signal as the bash tool, and
does not take per-file snapshots.

## Context window hold

The model menu reads `status.contextUsed`. The denominator is
`status.contextLimit` when a fit warning has named the window, otherwise
the catalog `context` on the active model when that field is non-zero.
Both numbers come from the daemon. If neither is known the meter says the
window is not reported.

Send (`user.input` and `steer`) is held when `status.contextFit` is
`critical`, or when a known window is already full (`contextUsed` ≥ limit).
A `warn` fit, or last-turn usage at or above 80% of a known window, is a
warning and Send stays available. `contextFit` clears on
`compaction.completed` (usage clears with it; the limit stays) and when a
later usage report falls under 80% of the daemon limit.

Effort is offered when the catalog row reports `reasoning`, when it lists
variants, or when the live session already has an effort. Published
variants are the choices (a variant id that is not an effort level is
shown disabled). The protocol ladder is the fallback when no variants are
named. Otherwise the menu says reasoning is not reported. False capability
flags are omitted.
They mean unknown or unsupported; #1054 does not yet split those.

Pins and the "this session / global default" mark are browser state plus
`PATCH /v1/settings`. `select.model` and `set.effort` change only the
live session.

## Chat mode

Conversation layout is a presentation of the same session shell. Cockpit
stays the default (`strike.layout` in this browser). Conversation centers
one column and opens the existing session list and side panel as drawers.
It does not fork the transcript or the composer.

The composer can attach files the daemon accepts. Names that look like
credentials are redacted before they are stored. The transcript does not
yet draw those refs back as images; that stays with session UX. Voice is
the browser speech API when `SpeechRecognition` exists. Audio may leave
the machine (browser vendor). There is no local model. Follow-up chips
under a reply are three fixed prompts, not model suggestions.

The service worker caches the app shell after a successful load and serves
it when the network is down. `/v1/` and `/health` are never cached. A
dropped event stream shows a reconnecting note and opens the stream again.
The column stays up during the gap. When the new stream opens, the column
is replaced and the daemon's log replay fills it. Events from a socket that
has already been closed are ignored, so the log is not appended twice.
## Notifications

The centre is in the client. `GET /v1/notifications` and
`GET /v1/notifications/stream` project an allowlist from session JSONL:
approval needed, a delegated child finished or blocked, and a root turn
finished. Bytes appended while the tab was closed are included on the next
read. Names and one-line summaries are scrubbed and clamped. Permission
patterns, tool output, handoff files, and verification reports are not
copied.

Per-session mute and quiet hours live in
`PUT /v1/notifications/prefs`. They suppress browser banners. The record
stays in the centre until this browser marks it read.

Not built: a third-party web-push service, and an operating-system banner
while the browser process is quit. Nothing can wake a quit browser without
that push service. The next open replays the daemon log into the centre.
A gateway drop is detected by the page (the daemon cannot deliver "I am
gone"); it is not a JSONL record.

## Find in transcript

Shipped (#1342): Ctrl/Cmd+F, the Find chip, or the palette command opens
a find bar over the open session's transcript — case-insensitive match
count, Enter / Shift+Enter (or Prev / Next) with wrap-around, matches
highlighted in place, Escape closes and restores the prior scroll.
Follow-up, deliberately not in the first slice: matches inside
collapsed content (child-card details and long tool dumps the
transcript folds away) are not searched and find does not expand
collapsed cards to reveal a match; a case-sensitive toggle is likewise
deferred until someone asks for it.

## Not planned as a copy

Stall is already a real badge when the team roster sets `budget.stall`.
Retry is `team.spawn` of the same objective, which starts a new child; it
does not resume the old session. Stop is `team.child_interrupt`. None of
these need a new panel.
