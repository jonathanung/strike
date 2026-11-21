# Web UI planned panels

The Rust client (`webui/`) ships the surfaces the daemon can drive today:
a session list (live roots, saved sessions, server-backed pins, archive,
and organisational folders, search across titles and recent transcript
text, delegated children nested in the conductor view), a transcript over
`/v1/live/events`, a composer that can steer or queue (split send,
visible engine queue, stop, context hold) and set the real
permission mode, and a side panel with Activity, Files, Changes, Git, Review, Worktrees,
Memory, Knowledge, and Artifacts.
Apps is a separate surface: a launcher and one queue, GitHub triage
(`docs/webui-apps.md`).
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

## Apps

GitHub triage is registered by the daemon and rendered by the client.
Inbox and schedule results are the same queue shape, and they are not
registered: there is no inbox store, and schedule runs have their own page
(see Schedule). The launcher does not draw a card for an app the daemon
did not return.

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

A fourth source, `script` (a shell command that fires with no session), is
designed in [design/script-schedules.md](design/script-schedules.md) (#1614)
and not shipped.

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

## Phone

The phone shell (`webui/mobile.css`, bottom nav) shows one pane at a
time: sessions, chat, activity, files, and the daemon view. Memory,
Knowledge, and Artifacts are the same side-panel tabs as on desktop
(under Files), not extra bottom-nav buttons.

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

Shipped. A first-run card (`strike.onboarded` in localStorage) lists the
paths from `GET /v1/local-files`, states that telemetry is off, and offers
the bundled themes as the accent. Strike is the default. Light or dark,
type size, and density stay in the browser (`strike.scheme`, `strike.font`,
`strike.density`). Continue writes the theme id with `PATCH /v1/settings`
`{"theme": ...}` into `~/.strike/config` when the user picked a swatch, or
when the settings read succeeded and the stored id is empty or already that
accent. A custom theme id is left in place. A failed settings read does not
overwrite it. The card embeds the Providers page instead of a second
provider form.

Settings is a Menu row, a command-palette command, and Ctrl/Cmd+comma.
`?` lists shortcuts. It shows the saved model,
permission mode, and notify dial from `GET /v1/settings` and does not edit
them. Those links set the location hash for the streams that own the pages:

| Hash | Stream |
|---|---|
| `#models` | #1259, and the link also opens Chat, where the model chip lives |
| `#approvals` | #1266, and the link also opens Chat, where the approval chip lives |
| `#notifications` | #1264 |
| `#gateway` | #1265, and the link also opens Daemon, which holds the token today |
| `#providers` | the Providers page, also linked from the Menu |

`strike serve` does not call `host.Onboarding`. The TUI wizard remains
`~/.strike/onboarding.json`, and that file is not in the daemon read list.

## Gateway hub

The header and the Daemon view share one Gateways switcher. Profiles are
names and URLs in `localStorage` (`strike.gateways`). Bearers are
`sessionStorage` only. `strike connect` opens the SSH local-forward and
injects a short-lived grant on the laptop's loopback. The daemon still
refuses a public bind. See [gateway.md](gateway.md).

Open on phone is a separate palette command. It shows a QR and a one-time
`#pair=` link. Scanning another gateway's QR adds that base URL to the same
profile list and stores the paired session in `sessionStorage`. It does not
store the daemon token.

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
The Terminal tab is a daemon PTY (`POST /v1/terminals`, stream
`GET /v1/terminals/{id}/stream`). It starts in the active workspace
directory, uses the same `/v1` authentication as the rest of the daemon,
and stays on loopback unless `strike serve --terminal-remote` is set with
`--auth`. See [gateway.md](gateway.md). An embedded browser and a subagent
board that is not the Activity list are not in the client. Activity is the
subagent list. Changes is the diff list. Composer `!` already runs one
command through the daemon permission model.

## Customize

Customize (`/v1/customize`) lists skills, agents, MCP servers, plugins, hooks,
rules files, and the roster stall/handoff gate. Editable writes are strike-native
files only, after a preview diff and a confirm. MCP env and header values are
masked. Permission rules are shown and preserved; editing them is a different
page. Plugin install and removal stay on `/v1/plugins`. The composer expands
`$skill` through the daemon (see Composer triggers); this page is where those
files are inspected and edited.

## Powers

The daemon side is in place but no chip is drawn yet (#1347, #1867).
`internal/integrate/powers` loads packs from
`<workspace>/.strike/powers/<pack>/` and `~/.strike/powers/<pack>/`, and
the workspace copy wins on a name clash. Workspace packs load only in a
trusted workspace. Each pack is an Agent Plugins package: `plugin.json`
holds `name` and `keywords`, `mcp.json` holds `mcpServers`, and
`skills/<name>/SKILL.md` holds skills.

A pack is suggested when a keyword appears in the prompt as a whole word
or phrase. Packs that are muted or already active are not suggested, and
nothing activates on its own. Activation is per session and
reference-counted: packs that share a server keep it until the last one is
deactivated, and ending one session never disconnects another session's
servers. If a server fails to connect, it is listed as failed and left
out of the session, and the chip and the tool set read the same result.
If a pack names a server the permission rules deny (`mcp` `<server>/*`),
the whole pack stays inactive and the reason is shown.

`mcp.PackConnector` attaches pack servers through the MCP manager, with
the same admission scan as configured servers, and detaches them when the
last session releases them. A server already configured in `mcp.json` is
borrowed and never detached. The dial runs outside the Powers lock, so a
slow server does not stall other sessions.

Routes: `GET /v1/powers?session=`, `POST /v1/powers/suggest`
`{session, prompt}`, `POST /v1/powers/activate` and `/deactivate`
`{session, name}` (activate needs a live session, a denied pack is 403),
and `POST /v1/powers/mute` `{name, muted}`. The mutating routes refuse
`--read-only`. Closing a root or deleting a session unloads its Powers.

Not built yet (#1943):
- skills loaded into context per session;
- per-session tool scoping (the MCP registry is process-wide, so a pack's
  tools are visible to every root until the last session releases them);
- the composer chip row with context cost (#1259);
- pack membership on Customize;
- goal-declared Powers (#1284);
- persisted mute and last-used time.

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
window is not reported. The client does not invent a limit. Echo has no
context window, so the hold stays hidden there.

Send (`user.input` and `steer`) is held when `status.contextFit` is
`critical`, or when a known window is already full (`contextUsed` ≥ limit).
A `warn` fit, or last-turn usage at or above 80% of a known window, is a
warning and Send stays available. `contextFit` clears on
`compaction.completed` (usage clears with it; the limit stays), when a
later usage report falls under 80% of the daemon limit, and when a turn
finishes while the level is still `critical` so a stale warning does not
hold the next draft. Send anyway, or Alt+Enter, sends the held draft once.

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
`PUT /v1/notifications/prefs`. They suppress browser banners.

Read state is daemon-side (#1392): `GET /v1/notifications` returns
`seen: {upTo, ids}` and `POST /v1/notifications/seen` takes `{"ids":[...]}`
(one row opened) or `{"upTo": seq}` ("Mark all read" up to the newest row
the browser showed). A row is read when `seq <= upTo` or its id is listed.
Reads are monotonic and shared by every browser on the gateway; ids are
pruned to the 200-row window. Each browser keeps its own copy in
`localStorage` as an offline cache and pushes reads made offline on the next
load. Dismissing a row ("Clear read") stays per-browser. Not built yet:
collapsing consecutive same-kind, same-session rows into one counted row.

Web Push (#1564): Notification settings has a "Turn on push" toggle that
asks the browser for permission, subscribes through `PushManager` (helper in
`register.js`), and registers the subscription with
`POST /v1/notifications/push-subscriptions` (`DELETE` removes it;
`GET /v1/notifications/push-key` is the VAPID public key). When a new centre
row would banner (same enabled / quiet hours / mute policy) and needs you —
an approval or a blocked run — the daemon encrypts `{type, kind, href, tag}`
(RFC 8291 aes128gcm, VAPID RFC 8292, standard library only) and posts it to
each subscription. `sw.js` shows fixed text by kind; names, summaries, prompts
and tool text are never sent. 404/410 from the push service prunes the
subscription; `GET /v1/notifications` reports `push.lastResult` so the
toggle can say a push failed. The VAPID key is `push/vapid.json` beside the
session dir (0600). Unsupported browsers, blocked permission, and iOS
without Home Screen install are named in the toggle. Finished and failed
runs (`session.finished`, `child.finished`) push only after "Also push
finished and failed runs" is checked under Push notifications (#1917): the
daemon-side `pushFinished` pref in `NotifyPrefs`, default off, with the same
generic payload.
A gateway drop is detected by the page (the daemon cannot deliver "I am
gone"); it is not a JSONL record.

## Share a session by link

Shipped (#1565, first slice of #1056): the transcript header's Share button
opens a dialog that mints a read-only link for the root session
(`POST /v1/sessions/{id}/shares`, expiry 24h / 7 days (default) / 30 days,
optional label), lists live links (`GET`), and revokes one
(`DELETE /v1/sessions/{id}/shares/{shareId}`). The link is
`<origin>/#share=<token>`: the token rides in the fragment, so it never
reaches server logs or a Referer header. A browser opening it mounts only a
read-only transcript shell, which posts the token in a JSON body to
`POST /share/v1/transcript` (outside `/v1`; the token is not a gateway
credential and every `/v1` route refuses it). It reads exactly the session
bound at mint, live as it grows; no composer, approvals, panels, session list,
or attachment downloads. Tokens are stored as SHA-256 hashes
(`shares.json`, 0600); mint, revoke, and each first read are audit-logged
without the token. Expired, revoked, unknown links and deleted sessions get
one identical dead end. Children are not included (root only).

Not built: redaction or selective disclosure, snapshot (frozen) links,
accounts or comments — all stay with #1056. Links only work for someone who
can reach the gateway (same as pairing links).

## Offline outbox

Shipped (#1325, first slice): when the gateway is unreachable, a text send
from the composer is queued in a per-gateway `localStorage` outbox (survives
a tab close or PWA restart) and shown above the composer as "Queued — will
send when reconnected", with Edit and Delete. On reconnect items flush one at
a time in the order written, before any new send for the same session. Each
send carries `X-Strike-Client-Message-Id`; `POST /v1/ops` remembers delivered
ids per live root (memory, 24h, bounded) and answers a repeat with
`{"ok":true,"duplicate":true}` without submitting, or 409 while the first
attempt is still in flight. A send that loses its connection mid-request is
queued under the same id, so a half-open retry cannot post twice. The op
(`user.input` or `steer`) is fixed at send time. Approvals are never queued.
Items older than 24h are held for confirmation (Send) instead of auto-sent;
a refused send (4xx) holds that session's later items until it is retried or
deleted.

Not built: queuing attachments (they need an upload while online), a
daemon-restart-proof dedupe (ids are memory-only), and a hold for sessions
that finished while the message was queued.

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

## Review queue

The Review side tab (`webui/src/review_queue.rs`, daemon routes in
`internal/frontend/server/review.go`, #1324) lists the live session's
unstaged working-tree changes as files and hunks from the same `git diff`
the Git panel reads. Each hunk can be accepted (a mark kept with the
session), rejected (reverted at once with `git apply -R` of that one hunk,
after a second confirming tap), or commented on with a file + line range.
Comments, accept marks, and rejections are stored per session under
`<session dir>/review/<id>.json` until **Send review** delivers every
comment and rejection as one user turn. That turn opens with a fixed
header, and the transcript labels it "Review" instead of showing it as
typed text. Reject and Send wait for the session to be idle; read-only
attach shows the queue disabled with the daemon's reason.

Not in this slice:

- Untracked (new) files and staged changes: the queue reads unstaged
  `git diff` only. Review a new file in the Git panel.
- Delegated children's worktrees: the queue follows the open root's
  working tree. Reviewing a child's isolated worktree needs a child-scoped
  cwd on these routes.
- Binary, mode-only, and git-quoted paths are listed with a note and no
  hunk actions.
- Accept is a review mark only; it does not stage the hunk.

## Markdown Review

The document pane's rendered Markdown view takes anchored comments
(#1366, `webui/src/markdown_review.rs`; anchor logic in the
`webui/md-review` crate). Select a passage and the composer quotes it.
The comment is stored with its anchor: the quoted text (whitespace
normalised), the nearest heading, and an offset hint. Line numbers are not
used because they go wrong on the first edit. Comments list under their headings in
document order, can be edited or deleted, and live in browser storage per
session + file (`strike.mdReview.v1:<session>:<path>`), so a reload keeps
them. **Send N to agent** sends every unsent comment for the file as one
`user.input` turn (path, section, quoted passage, comment). Sent comments
clear on success and stay with the error shown on failure. Sending is
refused while a turn runs. When the file changes and a quoted passage no
longer appears, that comment is marked "Anchor moved/lost" and is sent
flagged as such. It is never re-attached to another passage.

Not in this slice: comments on code files or diffs (see the review queue),
daemon-side storage for cross-device review, multi-user comments (#1056).

## Debug mode

Shipped (#1345, #1820). `harness/debugmode` holds one debug session: 2–5
ranked hypotheses with attached evidence, each open / confirmed / refuted
(confirming requires evidence). In the `hypothesis` phase the gate allows
reads and only edits whose every changed line carries the
`debugmode.Marker` tag; untagged edits, whole-file writes, deletes and
moves are denied with a reason. Fix edits unlock when a hypothesis is
confirmed or the user gives an override reason. Ending the session
requires the user to mark the bug fixed; it then sweeps every whole line
containing the marker under the workspace (skipping `.git`,
`node_modules`, `target`, binaries and files over 4 MiB) and closes only
when a rescan finds zero.

Engine: the session lives on the engine; `execToolCall` runs the gate
before hooks and Execute, so a denied call settles as blocked tool
feedback with the reason in the transcript. Delegated children are gated
by the parent's session. Wire: the `debug.control` op (start, hypotheses,
evidence, mark, unlock, fixed, end, scan) and the `debug.updated` event
(phase, hypotheses, override, live instrumentation, sweep report, error).
Resume restores the last successful `debug.updated`.

Cockpit: palette Debug → Start debug session. The Debug panel above the
composer (`webui/src/debug_mode_ui.rs`, rules in `webui/debug-mode`) has
the hypothesis input and cards (confirm / refute, evidence), the
reproduce checklist with an evidence form, the live instrumentation list
with Rescan, Unlock fix / Override and unlock / Mark bug fixed / End, and
a closing summary (root cause, evidence, cleanup verified). Start, fix
unlock (naming any override reason), mark fixed and the summary are also
transcript notices. At phone width the panel collapses to a status chip
that opens a bottom sheet.

Not drawn yet: the agent cannot write hypotheses or attach evidence
itself (no model-facing tool; the user does it in the panel). Bash is not
inspected by the gate; the permission layer still governs it.
## Steer acknowledgement

Shipped (#1343): a steered message carries a marker driven only by the
engine's durable `turn.steered` event (`webui/src/steer_ack.rs`). `boundary`
reads "Steer acknowledged. The agent has it from its next step.",
`cancel_restart` reads "The reply restarted with it.", and a
`queued_fallback` steer marks the later message it was sent as. The marker
survives reload because the event is in the JSONL. Pending (sent, not yet
applied) is the composer's steer note: the steer has no transcript line
until the engine applies it.

Planned, not drawn: the agent's own one-line "now doing X instead of Y"
summary and a conflict state ("this steer conflicts with my plan"). Both
need an agent-emitted event, for example `turn.steer_ack {targetTurnId,
summary, conflict, reason}` emitted by a model-facing acknowledgement tool,
that does not exist yet. The cockpit's protocol gate drops unknown events,
so nothing renders them until the engine and schema carry the event. The
marker never derives a summary or a conflict from timing or a plan diff.

## Container actions (#1584 follow-up)

Settings → Isolation (`GET /v1/container/status`) is read-only: posture
ladder, engine probe, workspace container (`strike container status`),
ejected-Dockerfile drift and detected manifests. Not drawn: eject,
drift fix, and container start/stop/build from the web, and the
`strike container ls --all` list of every managed container on the engine.
Host mode stays a first-class posture (#1290); nothing here changes a
default.

## Run with N models

Orchestration core shipped, card not drawn (#1346).
`internal/daemon/bestofn` validates a comparison (2–4 distinct
model/effort pairs, non-empty prompt, non-negative per-run budget), shows
the N× cost multiplier, and builds one spawn spec per model: same prompt,
the parent's permission mode passed through (never widened), and a
worktree per candidate when the prompt may edit files (shared otherwise).
A candidate whose spawn fails, fails mid-run, or is cancelled is recorded
as such and the comparison completes with the rest; cancelling all
interrupts only running children. Picking a winner is a separate call
that requires every candidate to have stopped and the winner to have
finished; it returns which child to keep and which to archive. "Keep
none" archives all. A run is decided once.

Not drawn yet: the Spawner wired to `team.spawn` (a per-child model and
effort field on the spawn op), worktree merge/cleanup on adopt, parent
commentary, the comparison card and model picker, and verification
reuse (#780).

## MCP elicitation

Shipped (#1353): `elicitation.requested` renders a card above the composer
(`webui/src/elicitation.rs`, `elicitation_ui.rs`) with the server name, its
message, and fields from the requested schema, or a URL with Open / Done.
Answers go back as one `elicitation.reply` (accept with content, decline,
cancel) and are never posted as chat text.

Not built yet:

- The card sits with the approval cards above the composer, not inline in
  the tool timeline where the call paused.
- Streamable HTTP MCP servers: the capability is declared on stdio only;
  HTTP does not route server requests yet.
- TUI and ACP rendering: both cancel with a note.
- Property order follows the field names (the cockpit's JSON maps are
  sorted), not the server's key order.

## Ship to PR

The Git tab's "Create pull request" action (#1340) previews the current
branch against its base (`GET /v1/git/pr`: commits, diffstat, uncommitted
changes, whether a push is needed, an existing open PR, and an editable
title/body drafted from the session title and commits). `POST /v1/git/pr`
pushes `refs/heads/<branch>` to origin only with `confirmPush` (never a
force-push), needs `allowDirty` when the tree has uncommitted changes, then
runs the host's `gh pr create` (draft by default) and links the PR to the
session (`prUrl` / `prNumber` / `prState` in the session meta). It refuses a
detached HEAD, the default branch / main / master, and a non-GitHub origin
with a plain message; an existing open PR is returned (the action becomes
"Open pull request"), never duplicated. No token crosses to the browser.
Not in this slice: non-GitHub forges, pushing to a remote other than
origin, and the live checks chip (PR watch, #1356, reads the same session
fields).

## Agent questions

Shipped (#1338): the `question` tool's `question.asked` renders as a card
above the composer (`webui/src/questions.rs`, `questions_ui.rs`). One
question shows at a time with Back / Next; each choice question also offers
"Your own answer". Send answers posts one `question.reply` with every answer
in order. Dismiss (or Escape) posts empty answers, which the engine treats as
a rejection and ends the turn. The session row's "Question" waiting state
comes from `questionPending`; the daemon also raises an `approval.needed`
notification titled "Question from the agent" that carries no question text.
`strike serve --provider echo`: send `ask <text>` to get a real question.

Not built:

- Multi-select. `QuestionPrompt` has no multi-select flag and the reply is
  one string per prompt, so the card offers one choice or your own text, the
  same as the TUI modal. Adding it needs a protocol field first.
- Answering from the notification or session list without opening the
  session. Recommended default: no; the card is the only answer surface.
- Timeouts. An unanswered question blocks until answered, dismissed, or the
  turn is stopped (recommended default: keep blocking).

## Pre-push review

"Review this branch" in the Git tab (#1487) sizes the branch diff first
(`GET /v1/git/review`: committed `base...HEAD` plus uncommitted changes,
untracked file names, one model turn, warn above 60 KB, refuse above the
200 KB diff cap). "Run review" (`POST /v1/git/review {confirm:true}`) spawns
a fresh-context child through the normal `team.spawn` path with the
built-in `branch-reviewer` agent, whose denies (write, edit, bash, task,
verify, browser, web, MCP, store writes) the engine enforces for children,
fed the diff, the repo's AGENTS.md and the findings format. The daemon parses
`FINDING | severity | path:line | scenario` lines and the `VERDICT:` line from
the child's transcript (`GET /v1/git/review/{id}`); each finding opens its
file at the line. No review store, no autofix, no merge gate. Not in this
slice: the `/review` composer trigger (#1275), a cross-model reviewer (team
spawn has no per-child model), and Ship to PR's warn-on-blocking hook.

## Security review mode

"Review this branch" has a Security mode (#1620): the same read-only
`branch-reviewer` child, fed a security instruction set (injection, authn/
authz bypass, committed secrets, SSRF/redirects, unsafe deserialization,
vulnerable dependency changes, entry→sink tracing), the repo's AGENTS.md and
an optional `.strike/security-rules.md`. Findings carry an ID, CWE, severity,
confidence, attack path, file:line and a draft fix. When the reviewer
finishes, a second fresh child (`POST /v1/git/review/{id}/verify`) tries to
refute each finding; only confirmed findings render as confirmed, refuted
ones collapse under "Not verified" with the reason, and only a confirmed
critical/high finding makes the verdict blocking. Dismissing a finding
requires a one-line reason and suppresses it (same file + CWE, anchor ±3
lines) on later security reviews of the same branch
(`/v1/git/review-suppressions`, stored in the session dir); suppressions are
listed and removable. Not in this slice: SARIF/JSONL export, PR-scoped
suppressions (#1356 follow-up), an authoring UI for security rules.

## PR watch

The Git tab shows the open session's linked pull request (#1356): a chip
with the PR number, state and check rollup, an expandable per-check list
(queued / running / passed / failed / skipped), and "Open on GitHub". Data
comes from `GET /v1/sessions/{id}/pr`, which runs the host's own `gh pr view`
(the browser never sees a token; gh stderr is never returned). `/v1/sessions`
also carries `prUrl` / `prNumber` / `prState`. The cockpit polls every 60s
while the PR is open and a client is connected; the daemon caches a poll for
30s, stops on merged/closed, and stops on errors retrying cannot fix (gh not
installed, gh signed out, no GitHub remote, non-GitHub PR) with an actionable
"PR tracking unavailable" line. A failed check or a changes-requested review
raises a `pr.attention` notification once per distinct failure set.

"Fix in this session" asks `POST /v1/sessions/{id}/pr/fix-draft` for a
draft (failing check names, the last 100 lines of each failed Actions job
log via `gh run view --log-failed`, redacted), shows it, and only after the
user confirms posts it as a normal `user.input` op. No automatic fix turns,
push or merge. Not in this slice: the chip on session-list rows, marking
checks that also fail on the base branch, and any autonomous fix loop.

## Not planned as a copy

Stall is already a real badge when the team roster sets `budget.stall`.
Retry is `team.spawn` of the same objective, which starts a new child; it
does not resume the old session. Stop is `team.child_interrupt`. None of
these need a new panel.

## Instance backup

Settings → Backup ships export and dry-run-first restore for sessions,
memory, knowledge pins, schedules and non-secret settings (#1339, see
`docs/instance-backup.md`). Artifacts, attachments,
goals/autopilot state, workspaces and their rules, issues, plans and the
ledger are not in archive version 1; the manifest names them as
`not-yet-included`. Passphrase encryption is not offered: the file says
`"encrypted": false` and secrets are excluded regardless.

## Read replies aloud

Assistant replies have a **Read aloud** control (#1384) that uses the
browser's own `speechSynthesis` (on-device voices when available). Voice and
speed are in Settings → Voice and stored in the browser. Not in slice 1: a
gateway-hosted Piper endpoint, auto-play, reading tool output, and stopping
playback on a gateway disconnect (a page unload stops it).

## Live preview

The side panel **Preview** tab (#1341) proxies one user-picked loopback port
through the daemon (`/preview/<key>/`, see `docs/web.md`). Known limits of
slice 1:

- Apps that load assets from absolute paths (`/src/main.ts`) need their base
  path set to the prefix shown in the pane (Vite `base`, Next `basePath`).
  The prefix stays the same for a given port for the life of the daemon.
- The frame runs in an opaque origin (sandbox without `allow-same-origin`).
  Apps that need cookies, `localStorage`, or a same-origin HMR websocket may
  partly fail. Websocket upgrades are proxied, but a dev server that checks
  `Origin` sees `null`.
- An app that refuses framing (`X-Frame-Options: DENY`, `frame-ancestors
  'none'`) is reported in the pane with an open-in-browser link. The pane
  does not strip those headers.
- The chosen port is not persisted per workspace yet. The agent cannot
  navigate the pane or read it.
