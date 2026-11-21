# Web workspace

The JavaScript cockpit this page describes has been removed. The current
client is the Rust bundle in `webui/`, served by `strike serve`. Start there:
[webui-daemon.md](webui-daemon.md). The endpoint notes below still describe
the daemon API where they have not been rewritten.

`strike serve` hosts a cohesive React **cockpit** for solo and multi-agent work
on desktop, tablet, and phone. It can drive a **live** engine session (mutations
via REST/WebSocket ops) and **attach-only** read historical session JSONL logs.

This is **not** a read-only status page: live mode exposes Chat, Code, Team,
Project, and Ops surfaces (plugins, panes, MCP, auth, reviewed file apply, team
controls) behind capability gates and attach-only / `--read-only` blocks.

**Progressive workspace contract (epic [#1069](https://github.com/jonathanung/strike/issues/1069)):**
modes (`Chat` default, `Code`, `Team`, `Project`, `Ops`), surface registry,
additive deep links, attach-only matrix, TUI→web parity inventory, and
conformance closeout live in
**[docs/web-cockpit-contract.md](web-cockpit-contract.md)** (WEBUI.1 / #1070 +
WEBUI.21 / #1090). This page is the operator reference for start flags,
endpoints, multi-session UX (#467), smokes, and security.

## Start

```sh
make serve
# equivalent (live engine defaults to echo for offline use):
./strike serve --addr 127.0.0.1:8787
```

Loopback serving is unauthenticated by default. Enable authentication explicitly;
strike mints a token when `--auth` is used without `--token`.

```sh
./strike serve --auth --token "$STRIKE_SERVE_TOKEN" --provider echo
```

| Flag | Meaning |
|---|---|
| `--addr` | Loopback bind address (default `127.0.0.1:8787`; non-loopback rejected) |
| `--auth` | Require bearer authentication; auto-mint a token when omitted |
| `--token` | Bearer token for `/v1/*`; requires `--auth` |
| `--provider` | Live engine provider (default `echo`) |
| `--model` | Optional model id |
| `--session-dir` | Sessions directory for `--attach-only`; rejected in live mode |
| `--attach-only` | No live engine — JSONL SSE attach only |
| `--read-only` | Reject mutating protocol ops (`POST /v1/ops` + WS op frames); status/SSE still work |
| `--auto` | Skip permission asks; `network.allow` still applies |
| `--dangerously-skip-permissions` | Skip permission asks and bypass `network.allow` |

Open the cockpit:

```
http://127.0.0.1:8787/attach
```

### Real-browser smokes (`make web-e2e`)

Offline Playwright harness (WEBUI.5 / #1071) against temporary `strike serve`
fixtures (echo provider + attach-only). Does **not** replace unit tests:

```sh
make web-check   # fast default: typecheck, vite build, vitest/jsdom
make web-e2e     # boots servers, installs Chromium once, runs web/e2e
```

Coverage includes bootstrap, one streamed echo turn, permission dialog,
multi-root switching, additive `?mode=`/`?surface=` deep-link safety,
attach-only mutation lockout, keyboard focus for settings/send, and
desktop/tablet/720/320 viewport overflow checks. Failure traces and
screenshots land in `web/e2e-artifacts/` (CI uploads the same path).
Honors `prefers-reduced-motion` via Playwright `reducedMotion: "reduce"`.
No real provider keys or network model calls.

## Remote access (SSH local forward)

`strike serve` binds **loopback only**. There is no cleartext LAN expose and no
built-in TLS. Non-loopback `--addr` values are rejected. Legacy `--expose` /
`--allow-cidr` flags error with a migration hint.

For phone/laptop access to a remote host, forward the loopback port over SSH:

```sh
# on the machine running strike:
./strike serve --auth --token "$STRIKE_SERVE_TOKEN" --provider echo
# on the client:
ssh -L 8787:127.0.0.1:8787 user@strike-host
# open http://127.0.0.1:8787/attach?token=...
```

### Threat model

| Risk | Notes |
|---|---|
| Session transcripts | Token holders can SSE/WS read JSONL and live events on the bound interface |
| Live control plane | Token holders can submit ops (prompts, permission replies, tools) |
| Cleartext LAN | Not supported — use SSH (encrypted tunnel) instead of binding off localhost |
| Token in URL | Cockpit open uses a one-time `?token=` handoff that sets a cookie and redirects; `/v1/*` rejects query tokens (use Bearer or cookie) |
| CSRF / CORS | Only loopback browser origins are reflected (`localhost` / `127.0.0.1` / `::1`). Public and private LAN origins are never reflected |
| Ops flood | Per-IP token bucket on `POST /v1/ops` and WS op frames (default 10/s, burst 20) → HTTP 429 / WS error |
| Ops audit | Every admitted/denied op is logged to `~/.strike/audit` as family `serve_op` (type + source IP + channel + outcome; no bodies) |
| Read-only | `--read-only` rejects mutating ops with HTTP 403 / WS error while keeping attach/SSE |

## Endpoints

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/health` | no* | `{ "ok": true, "version", "commit" }` |
| `GET` | `/` or `/attach` | no* | Cockpit HTML (composer + transcript) |
| `GET` | `/v1/bootstrap` | mode | Version, status, capabilities, agents, skills |
| `GET` | `/v1/ws` | mode | WebSocket: ops in, event envelopes out |
| `POST` | `/v1/ops` | **yes** | Submit one op envelope (JSON) |
| `GET` | `/v1/live/events` | **yes** | SSE of live engine events (+ JSONL backlog) |
| `GET` | `/v1/status` | **yes** | Live status (model, agent, mode, sandbox, cwd, busy, …) |
| `GET` | `/v1/sandbox` | **yes** | Active OS sandbox dial, backend, network.allow summary, explain profile |
| `PATCH` | `/v1/sandbox` | **yes** | Persist sandbox default (`mode`, optional `iKnow`); new sessions only |
| `GET` | `/v1/agents` | **yes** | Selectable agent names |
| `GET` | `/v1/models` | mode | `{ "models": [...] }` (`?provider=` optional). Echo (offline default) is **200** with a synthetic `echo` model, not 502. Catalog fetch / empty models.dev lists for API providers are **502**. |
| `GET` | `/v1/sessions` | **yes** | Durable session list (roots only) + `liveId` |
| `GET`/`POST` | `/v1/diag` | **yes** | Redacted prompt/config diagnostic bundle (JSON download; live only; `?root=` when multi-root). Bootstrap capability `diag`. **503** when unsupported (attach-only / no live). |
| `GET`/`POST` | `/v1/init` | **yes** | Project init (#1581): `GET` previews `{path, exists}` for AGENTS.md in the root's working directory (`?root=` when multi-root); `POST` writes it via host `ProjectInit`. **409** when AGENTS.md already exists (never overwrites; TUI `/init` confirms replace). **403** attach-only / `--read-only`; **501** without the host service; **404** unknown `?root=`. Bootstrap capability `projectInit`. |
| `GET` | `/v1/container/status` | no | Container posture (#1584, read-only): `{posture, postureSource (env\|dials), describe, ladder[{label, describe, active}], permissionMode, sandboxMode, workspace, configScope (project\|global), runtime{engine, found, available, error}, container{name, id, running, status, image, compatible, reason, haveHash, wantHash, sessionModel}, drift{state (matches\|drifted\|not-ejected\|error), path, have, want, error}, detect, errors}` for the live root's directory (`?root=`), else the daemon workdir. Same calls as `strike container status\|drift\|detect`. The workspace's own container config (which names the engine binary) is read only when the workspace is trusted (#1352). No runtime is a normal host-mode answer, not an error. **501** with no workspace; **404** unknown `?root=`. Bootstrap capability `container`; cockpit Settings → Isolation. |
| `GET`/`POST` | `/v1/workspace/trust` | **yes** | Workspace trust (#1781, on top of #1352): `GET` returns `{root, trusted, trustedAt, untrustable, summary, surfaces, mutable, restartRequired, canReload}` for the live root's workspace (`?root=` when multi-root); `surfaces` is `config.EnumerateProjectSurfaces` (names + command lines, never env/headers). `POST {"action":"grant"\|"revoke"}` updates `~/.strike/workspace-trust.json`; the workspace always comes from the live root, never the body. `restartRequired` flags a saved decision that differs from the running assembly; `POST {"action":"apply"}` (#1834, only when `canReload`) answers **202** `reloading: true`, drains HTTP, stops every root and project MCP/LSP/harness process, then re-execs `strike serve` with the same argv so the decision takes effect; it never re-execs if shutdown was incomplete. **409** when nothing changed or the daemon cannot reload (in-memory token, port `0`, Windows). **422** home / filesystem root; **403** attach-only / `--read-only`; **501** without a store path; **404** unknown `?root=`. Bootstrap capability `workspaceTrust`. The cockpit shows an "Untrusted workspace" badge and a trust card (Grant / Keep untrusted). |
| `GET` | `/v1/sessions` | **yes** | Session list + `liveId` |
| `GET` | `/v1/sessions/{id}/events` | **yes** | SSE tail of a session JSONL log |
| `GET` | `/v1/sessions/{id}/children` | **yes** | Child/subagent sessions under a root |
| `POST` | `/v1/sessions/{id}/fork` | **yes** | Fork durable session → new id |
| `GET` | `/v1/sessions/{id}/rewind_preview` | **yes** | Rewind preview (#1336): `?keep=` a turn's fork keep-count → `{turn, turns, totalTurns, prompt, files[{path,kind}], checkpointSkipped, uncovered[], beyondCheckpoints, disabled?}` summing what rewinding that turn and every later one would undo, from the record's `turn.completed` facts. Read-only; the cockpit applies the rewind with one `rewind` op per undone turn. `disabled` states why on `--read-only` cockpits and delegated child sessions. **400** bad `keep`; **409** turn no longer in history; **404** unknown session |
| `POST` | `/v1/sessions/{id}/report` | **yes** | Generate a `session_report` artifact from the session record (#1444) → **201** artifact. Facts only (turns, asks, tool counts, files, approvals, children, tokens, errors); tool args/output never copied; redacted. **404** unknown session; **501** without sessions or artifact store |
| `PATCH` | `/v1/sessions/{id}` | **yes** | Rename (`{ "title" }`) |
| `DELETE` | `/v1/sessions/{id}` | **yes** | Delete durable session (`?force=true` optional) |
| `GET` | `/v1/sessions/{id}/loops` | **yes** | Session `/loop` jobs (see [Session loops](#session-loops)) |
| `POST` | `/v1/sessions/{id}/loops` | **yes** | Start a loop (`{ "interval", "job" }`) |
| `DELETE` | `/v1/sessions/{id}/loops[/{loopID}]` | **yes** | Stop one loop, or all |
| `GET` | `/v1/roots` | **yes** | Active live roots + `activeId` (503 without LiveHub) |
| `POST` | `/v1/roots` | **yes** | Create empty live workspace; becomes active |
| `POST` | `/v1/roots/{id}/activate` | **yes** | Set hub active root (must already be live) |
| `POST` | `/v1/roots/{id}/resume` | **yes** | Resume durable root as live workspace |
| `DELETE` | `/v1/roots/{id}` | **yes** | Close/stop a live workspace (hub remove) |
| `GET` | `/v1/mcp` | **yes** | MCP server status list (`{servers:[…]}`) |
| `POST` | `/v1/mcp/retry` | **yes** | Retry one server (`{name?}`) or all non-up |
| `POST` | `/v1/mcp/disable` | **yes** | Disable server and unregister tools (`{name}`) |
| `GET` | `/v1/plugins` | **yes** | Installed plugins (host-safe; env keys only) |
| `GET` | `/v1/plugins/outdated` | **yes** | Catalog-sourced installs with newer versions (`?registry=`) |
| `GET` | `/v1/plugins/{id}` | **yes** | Inspect one plugin (`?scope=`) |
| `GET` | `/v1/plugins/{id}/trust-preview` | **yes** | Capability review before trust (no secrets) |
| `POST` | `/v1/plugins/enable` | **yes** | `{id, scope?}` |
| `POST` | `/v1/plugins/disable` | **yes** | `{id, scope?}` |
| `POST` | `/v1/plugins/remove` | **yes** | `{id, scope?, confirm:true}` |
| `POST` | `/v1/plugins/trust` | **yes** | `{id, scope?}` after trust-preview |
| `POST` | `/v1/plugins/untrust` | **yes** | `{id, scope?}` |
| `POST` | `/v1/plugins/search` | **yes** | `{registry, query}` → catalog hits |
| `POST` | `/v1/plugins/install` | **yes** | `{source, scope?, registry?}` path/git/catalog |
| `POST` | `/v1/plugins/preview-update` | **yes** | `{id, scope?, registry?}` update review |
| `POST` | `/v1/plugins/update` | **yes** | `{id, scope?, registry?, confirm:true}` |
| `GET` | `/v1/panes` | **yes** | Enabled pane/1 contributions (no PluginRoot) |
| `GET` | `/v1/panes/{id}` | **yes** | One pane descriptor + sanitized definition |
| `GET` | `/v1/panes/{id}/snapshot` | **yes** | Current view tree + host feeds |
| `POST` | `/v1/panes/{id}/mount` | **yes** | Mount static or start process pane |
| `POST` | `/v1/panes/{id}/unmount` | **yes** | Shutdown process pane |
| `POST` | `/v1/panes/{id}/input` | **yes** | `{event}` → `pane.input` |
| `POST` | `/v1/panes/{id}/resize` | **yes** | `{width, height}` cell-equivalent size |
| `GET` | `/v1/permissions/explain` | **yes** | Last-match-wins explain (`permission`, optional `pattern`) |
| `GET` | `/v1/permissions/presets` | **yes** | Shipped permission preset catalog |
| `GET` | `/v1/sessions/{id}/timeline` | **yes** | Redacted structured run timeline (JSON snapshot) |
| `GET` | `/v1/sessions/{id}/timeline/export` | **yes** | Download redacted timeline (`format=json\|jsonl`) |
| `GET` | `/v1/workflows` | **yes** | Workflow catalog (host-safe summaries) |
| `GET` | `/v1/workflows/{name}` | **yes** | One catalog entry |
| `GET` | `/v1/workflows/{name}/document` | **yes** | Editable document for builder |
| `POST` | `/v1/workflows/scaffold` | **yes** | Minimal valid draft (`{name}`) |
| `POST` | `/v1/workflows/validate` | **yes** | Validate document (`{ok,error?}`) |
| `POST` | `/v1/workflows/format` | **yes** | Canonical JSON for a document |
| `POST` | `/v1/workflows/phase-grants` | **yes** | Phase permission grants preview |
| `POST` | `/v1/workflows/save` | **yes** | Atomic save (`scope`, `force`); never activates |
| `POST` | `/v1/workflows/{name}/start` | **yes** | Start after grant review (`confirm:true`); rejects invalid |
| `POST` | `/v1/workflows/stop` | **yes** | Clear active workflow phase |
| `POST` | `/v1/workflow-drafts/review` | **yes** | Structured draft review (checks + widening) |
| `POST` | `/v1/workflow-drafts/save` | **yes** | Save draft JSON with explicit `confirm` |

With `--auth`, authenticate `/v1/*` using either of:

- `Authorization: Bearer <token>`
- HttpOnly `strike_serve_token` cookie (set automatically when you open
  `/attach?token=…` or `/?token=…` — the server redirects to a token-free URL)

Query-string `?token=` is **not** accepted on `/v1/*` (it leaks into shell
history, proxy logs, and `Referer`). Opening the cockpit URL printed by
`strike serve` (includes `?token=` on `/attach` only) performs a one-time
handoff: valid tokens become a `SameSite=Strict` HttpOnly cookie so subsequent
same-origin `fetch` / EventSource / WebSocket calls succeed without leaving the
secret in the address bar.

### Ops rate limit, audit, and read-only

- **Rate limit:** each client IP has a token bucket (default **10 ops/s**, burst
  **20**) shared by `POST /v1/ops` and WebSocket op frames. Exceeding the limit
  returns **HTTP 429** or a WS `error` frame (`ops rate limited`).
- **Audit:** every op attempt is appended to the security audit sink
  (`~/.strike/audit`, family `serve_op`) with `opType`, `sourceIp`, `channel`
  (`http`|`ws`), and `outcome` (`ok`|`rate_limited`|`read_only`|`error`). Op
  bodies are never stored. See [audit.md](audit.md).
- **`--read-only`:** rejects mutating protocol ops (same paths) with **HTTP 403**
  / WS error while leaving bootstrap, status, and SSE attach available.
  `status.get` on the WebSocket is still allowed.

### Op envelopes (client → engine)

JSON objects with a `type` and optional `data`:

| type | data |
|---|---|
| `user.input` | `{ "text": "..." }` |
| `interrupt` | _(empty)_ |
| `permission.reply` | `{ "requestId", "decision": "once\|always\|project\|reject", "message?" }` |
| `question.reply` | `{ "requestId", "answers": ["..."] }` |
| `select.agent` | `{ "name": "build" }` |
| `select.model` | `{ "provider", "model?" }` |
| `set.permission_mode` | `{ "mode": "default\|plan\|accept-edits\|yolo" }` |
| `set.autonomy` | `{ "mode": "supervised\|agent\|checks\|skip-all" }` |
| `set.verbosity` | `{ "level": "default\|concise\|detailed" }` — response style (#1369); confirmed by `verbosity.selected` `{ level, delivery: "none\|instruction" }`, reported on status as `verbosity` / `verbosityDelivery` |
| `team.spawn` | `{ "idempotencyKey", "objective", "agent?", "name?", "isolation?", "budget?", "rootSessionId?" }` |
| `team.message` | `{ "idempotencyKey", "to", "body", "kind?", "urgency?", "taskId?" }` |
| `team.broadcast` | `{ "idempotencyKey", "body", "urgency?", "taskId?" }` |
| `team.child_interrupt` | `{ "idempotencyKey", "childSessionId", "reason?" }` |
| `team.task_transition` | `{ "idempotencyKey", "delegationId", "expectedVersion", "toState", "reason?" }` |
| `team.board_create` | `{ "idempotencyKey", "title", "body?", "assignee?" }` |
| `team.board_claim` | `{ "idempotencyKey", "taskId", "expectedVersion" }` |
| `team.board_complete` | `{ "idempotencyKey", "taskId", "expectedVersion", "summary?" }` |

Human team-control Ops (WEBUI.18) are advertised on bootstrap when live:
`capabilities.teamControl: true` and the `team.*` names above appear in
`protocolOps`. HTTP waits for the engine outcome and returns structured
`{ ok, childSessionId?, taskId?, … }` or stable errors (`attach_only`,
`cross_root_denied`, `conflict` + `currentVersion`, `idempotency_conflict`,
`permission_denied`, `team_unavailable`). Full contract:
[human-orchestration-ops.md](human-orchestration-ops.md).

### OS sandbox (web parity)

Bootstrap capability `sandbox` is true for live `strike serve` (not attach-only).
Status includes `sandbox`, `sandboxBackend`, `sandboxAvailable`, and `networkAllow`.

| Method | Path | Body | Notes |
|---|---|---|---|
| `GET` | `/v1/sandbox` | — | Active mode + compiled explain text (no browser shell-out) |
| `PATCH` | `/v1/sandbox` | `{ "mode", "iKnow?" }` | Saves **default** via `host.Settings.SaveConfigDials` (TUI `/settings` parity). Active session dial is fixed at start. |
| `GET` | `/v1/settings` | mode† | Persisted defaults snapshot |
| `PATCH` | `/v1/settings` | `…, "sandbox", "iKnow?"` | Same default write path when `sandbox` is non-empty |

Safety gate: `mode=off` while live or default `permissionMode` is `yolo` requires `iKnow: true` (CLI `--i-know` equivalent). Invalid modes return 400. Missing capability returns 501.
| `set.effort` | `{ "level": "..." }` |
| `set.fast` | `{ "enabled": true\|false }` |
| `compact` | `{ "strategy": "summarize" }` |
| `inspect.prompt` | _(empty)_ — request effective-prompt / context doctor snapshot |
| `context.controls` | `{ "pinKinds?", "setPin?", "excludeKinds?", "setExclude?" }` |
| `rewind` | `{ "restoreFiles"?: true }` |
| `workflow.start` | `{ "name": "plan-implement" }` (prefer REST start after grant review) |
| `workflow.stop` | _(empty)_ |

### Context doctor (inspector **context** tab)

Always available (event-driven; not a host capability). Live events drive the surface:

| Event | UI effect |
|---|---|
| `usage.reported` | Updates used tokens when `used.known` |
| `prompt.effective` | Layer table, token-by-source attribution, pin/exclude/shed sets |
| `context.controls` | Confirms pin/exclude after a control op |
| `context.fit_warning` | Fit warning banner + context limit |

Pin/exclude send `context.controls` with full replacement sets (`setPin` /
`setExclude`). Refresh (and `/context` / `/prompt`) re-issues `inspect.prompt`.

### Cockpit slash commands & export

The composer accepts a **web-safe** slash catalog (not full TUI parity). Type `/`
for completions; `/help` lists builtins + skills. Unknown `/commands` are
rejected with a transcript notice (they are **not** sent as prompts). Skills
still pass through as `user.input`.

| Command | Behavior |
|---|---|
| `/help` | List web commands + skills |
| `/export` | Download the loaded transcript as markdown (also **Export** / header ↓) |
| `/compact` `/prompt` `/context` `/rewind` `/rewind-files` `/interrupt` | Mapped protocol ops (`/context` → `inspect.prompt`) |
| `/queue` | Focus the local prompt queue browser |
| `/rename` `/fork` | Session REST when `capabilities.sessions` |
| `/cost` `/copy` `/fast` | Client notices / clipboard / `set.fast` |
| `/agent` `/effort` `/autonomy` `/mode` `/model` `/provider` | Runtime ops (args required) |

**Prompt queue** (composer, while busy): remove, edit text, reorder ↑/↓, clear.
Queue state is UI-local (same as TUI input buffer) — not a server queue API.

**Markdown export** is client-side from the in-memory transcript (header + You /
Strike / tools). No separate export HTTP endpoint.

### MCP status and control

Bootstrap capability `mcp` is true when the host exposes `Services.MCP`. The
cockpit inspector **mcp** tab lists configured servers (state, transport,
endpoint label, tools, non-secret errors) and offers **Retry** / **Disable**
actions matching TUI `/mcp`. Empty configuration still reports the capability
when the host service is present; the panel shows a configure hint. Secrets
(headers/env) are never returned on the wire.

### Workflow authoring (web parity)

Bootstrap capabilities `workflows` / `workflowDrafts` are true when the host
exposes catalog and draft services. The cockpit inspector **workflows** tab
lists the catalog, opens a linear builder (create/edit phases, permissions,
check commands), runs validate + draft review (widening / executable checks),
and saves to an explicit `global` or `project` scope. **Save never activates.**

Start requires a grant-review dialog and `POST .../start` with
`{"confirm":true}`. Invalid catalog entries cannot be activated (422). The web
surface uses only host-safe JSON DTOs — no TUI types cross the boundary.

### Diagnostic bundle export

When bootstrap capability `diag` is true (live engine present), the cockpit
**context** inspector and **settings** dialog expose **Download diagnostics**.
That calls `GET /v1/diag` (optional `?root=<id>`), which submits
`inspect.diagnostic` to the live engine, re-scrubs the payload with `pkg/diag`
(same redaction as TUI `/diag`), and returns pretty JSON with
`Content-Disposition: attachment`. Attach-only / no-live hosts return **503**
and leave the control disabled. Complements timeline export (WEB.4); not a full
transcript dump.
### Run timeline (web)

Bootstrap capability `timeline` is always true. The inspector **timeline** tab
loads a collapsed, secret-redacted harness span list for the selected session
(`GET /v1/sessions/{id}/timeline`) and can download JSON or JSONL exports
(`…/timeline/export?format=json|jsonl`). The timeline is derived from durable
session JSONL via `pkg/timeline` — it complements the transcript, it does not
replace it. Field-level scrubbing uses `pkg/redact` (same path as TUI
`/timeline export`).

Events use the same envelopes as session JSONL (`type` + `time` + `data`).

Example — full echo turn via HTTP:

```sh
TOKEN=...
# stream live events
curl -N -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8787/v1/live/events &
# send a prompt
curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"user.input","data":{"text":"hello"}}' \
  http://127.0.0.1:8787/v1/ops
```

Permission asks appear as `permission.asked` events; resolve with
`permission.reply` (UI modal or `POST /v1/ops`). The cockpit modal offers all
four wire decisions (`once` | `always` | `project` | `reject`), shows the tool
name and patterns (not raw JSON), and — when bootstrap capability
`permissions` is true — can load host explain via
`GET /v1/permissions/explain?permission=…&pattern=…`. Attach-only hosts without
`Services.Permissions` keep the capability false and omit the explain control.

## Multi-session UX contract

Normative product contract for the web cockpit multi-session experience
(epic [#467](https://github.com/jonathanung/strike/issues/467)). Implementers of
WEBSESS.2–.6 (#917–#921) must follow this section; do not re-decide product
here without updating this doc.

For progressive **modes**, **surface registry**, **parity inventory**, and
extended deep-link grammar (`mode` / `surface` / …), see
[web-cockpit-contract.md](web-cockpit-contract.md). When the two docs disagree
on root/session transport, **this section wins**; on modes/surfaces/parity, the
cockpit contract wins.

**Status:** contract freeze for wave 1. UI and server behavior changes land in
child issues; this section is the source of truth for information architecture,
flows, attention, state isolation, and known API gaps.

### Information architecture

| Concept | Meaning | Where it lives |
|---|---|---|
| **Workspace (active root)** | A live engine session owned by `LiveHub` — can receive ops and stream events | `GET /v1/roots` → `roots[]`; rail **ACTIVE** tab |
| **History (durable JSONL)** | On-disk session log; read-only until resumed into a workspace | `GET /v1/sessions` → `sessions[]`; rail **HISTORY** tab |
| **Child / subagent** | Session with a parent root; not a concurrent workspace | Filtered out of `GET /v1/sessions` and the root switcher; inspect via `GET /v1/sessions/{id}/children` or in-transcript child status — not the rail |
| **Server active root** | Hub pointer used when `?root=` is omitted on ops/status/ws | `GET /v1/roots` → `activeId`; set by create/resume/activate |
| **Client selection** | Which workspace or history row the user is viewing | Client-only (`selectedID` + `selectedIsLive`); may differ from `activeId` only transiently — see switch rules |

Rules:

1. A **workspace** is always a live root. History rows that are also live show a
   **LIVE** badge and selecting them is a live view (not attach-only SSE).
2. **History** is durable identity. Resume promotes a history root into a
   workspace; it does not create a second durable id unless the user forks.
3. **Children never appear in the root switcher.** Resume of a child id is
   rejected by the server with a readable error.
4. When `capabilities.roots` is false (no `LiveHub`) or attach-only mode, the
   rail falls back to a single **SESSIONS** list (legacy single-live + history
   attach). Multi-root create/activate/close are unavailable.

### Identity and display

| Field | Source | Rail display |
|---|---|---|
| Root / session id | `RootSummary.id` / `Session.id` | Secondary (truncated) when title present |
| Title | `RootSummary.title` or session `title` | Primary label; fallback short id |
| Agent | `RootSummary.agent` / live status | Shown on ACTIVE rows when set |
| Busy | `RootSummary.busy` | **BUSY** badge + busy indicator |
| Server ACTIVE | `id === activeId` | **ACTIVE** badge (hub default root) |
| Recent activity | `activeAt` / `hasRecentEvent` / session `mtime` | Relative age when available |
| LIVE (history) | session id ∈ active roots | **LIVE** badge on HISTORY rows |

### Transports

| Mode | Transport | Scope |
|---|---|---|
| Live workspace | WebSocket `GET /v1/ws?root=<id>` (ops in, envelopes out) | One connection per **viewed** live root (current client pattern). Prefer explicit `?root=`; empty `root` resolves to server `activeId`. |
| Live ops (HTTP) | `POST /v1/ops?root=<id>` | Same root scoping as WS |
| Live status / files | `GET /v1/status?root=`, `GET /v1/changed-files?root=` | Same |
| Historical attach | SSE `GET /v1/sessions/{id}/events` | Read-only JSONL tail; composer disabled |
| Roots inventory | `GET /v1/roots` | Poll or refresh after lifecycle actions; used for rail + attention |

`GET /v1/live/events` remains the single-root SSE path; multi-root clients should
prefer WS with `?root=` rather than multiplexing all roots into one transcript.

### Server activate semantics

- `POST /v1/roots/{id}/activate` sets hub `activeId` to a **already-live** root.
  Unknown / non-live ids → 400.
- **Selecting a live workspace in the UI MUST call activate** (client already
  exports `activateRoot`; wire it on select — #917). After success, refresh
  roots so `activeId` badges stay correct.
- Create (`POST /v1/roots`) and resume (`POST /v1/roots/{id}/resume`) already
  make the new/resumed root active server-side; client should still select it
  and open live transport with that id.
- Ops **should always pass `?root=<selected live id>`** so a stale hub active
  pointer cannot mis-route prompts. Activate keeps default-root behavior correct
  for callers that omit `root`.

### Client state inventory (per workspace)

Partition by workspace/root id (not a single global wipe on switch) — #918:

| State | Live workspace | Historical view |
|---|---|---|
| Transcript items + seen set | Cached per id; restore on return | Rebuild from SSE; may discard when leaving |
| Status snapshot | Per id | N/A or last known if was live |
| Permission / question blocking | Per id; dialog only for **selected** id | None (read-only) |
| Composer draft, attachments | Per id | Disabled; do not bind draft to history id as send target |
| Prompt queue | Per id; drain only when that id is selected, live, and not busy | None |
| Runtime mirrors (provider/model/…) | Per id from events/status | Read-only if shown |
| Transport handle | WS per viewed live root; close previous on switch | SSE; close on switch |

Hard rules:

- Switching A → B must not append B's events into A's transcript cache.
- Permission/question **reply** must use `?root=` (or selected live id) matching
  the dialog's workspace — never the previously selected root.
- Historical selection must clear any live send target; composer stays disabled.
- Draft persistence across **browser reload** is optional / non-goal for #918.

### Attention model

Surfaced on the ACTIVE rail and a compact header summary (#919). Meanings:

| Signal | Meaning | Data today |
|---|---|---|
| **BUSY** | Engine turn in progress on that root | `RootSummary.busy` |
| **Needs you** (permission / question) | Blocking human input on that root | **Gap:** not on `RootSummary` yet — see API gaps |
| **Recent** | Activity within ~5 minutes | `hasRecentEvent` (`activeAt` freshness) |
| **ACTIVE** | Server hub default root | `activeId` |
| Selected (viewing) | Client focus | Row `active` class / aria |

Behavior:

1. Background roots that need attention show badges **without** forcing a switch.
2. Clicking a needs-attention badge **switches** to that workspace, activates it,
   and presents the blocking dialog for that root only.
3. Header "agent working" / transport line reflects the **selected** root;
   aggregate attention (e.g. count of needs-you) may appear beside it.
4. Avoid false-positive storms: idle roots clear busy; recent is soft (informational).
5. Observation strategy (**implemented**): lightweight poll of `GET /v1/roots`
   every **2s** while multi-root is enabled (busy / hasRecentEvent /
   permissionPending / questionPending). No secondary WS for background roots.

### Lifecycle flows

#### Create workspace

1. User: **+ New workspace** (hidden in attach-only).
2. Client: `POST /v1/roots` → `{ id, sessionId }`.
3. Refresh `GET /v1/roots` + sessions; select new id as live; open WS `?root=id`.
4. Server already activates the created root.

#### Switch (live → live)

1. User selects another ACTIVE row.
2. Client: `POST /v1/roots/{id}/activate`; set selection live; restore per-id UI state;
   reconnect WS to new `?root=`.
3. Do not reset unrelated workspace caches.

#### View history (attach)

1. User selects a HISTORY row that is **not** live.
2. Client: selection historical; SSE `.../sessions/{id}/events`; composer RO.
3. Do **not** call activate. Do **not** send ops.

#### Resume

1. User: **Resume as workspace** on a historical root (disabled when already live
   or attach-only / no roots capability).
2. Client: `POST /v1/roots/{id}/resume`.
3. On success: refresh roots; select returned `id` live; WS connect; land on ACTIVE tab.
4. If `wasActive: true`, treat as activate-only (already live).
5. Errors (missing id, **child session**): show server `error` string; stay on history.

#### Fork

1. User: Fork on current selection (requires `capabilities.sessions`).
2. Client: `POST /v1/sessions/{id}/fork` → new durable session.
3. Refresh HISTORY. **Default:** stay on current selection; new fork appears in
   HISTORY (user may resume it). Optional "switch to fork" is a #920 polish, not required.
4. Fork does not auto-resume into a live workspace.

#### Rename

1. `PATCH /v1/sessions/{id}` with `{ "title" }`.
2. Refresh sessions; if the id is a live root, refresh roots so title propagates
   when the hub title is updated (hub `SetTitle` is server-side on spawn/resume —
   if rename does not update live title yet, treat as follow-up gap).

#### Delete

1. Confirm; `DELETE /v1/sessions/{id}` (optional `?force=true`).
2. If the deleted id was selected: fall back to another live root, else first
   history row, else empty state.
3. If it was live, client must refresh roots; engine teardown is server-owned.
   Deleting durable JSONL while a root is live may conflict — surface 409 body.

#### Close / stop workspace (live only)

Stop the live engine for a root **without** necessarily deleting durable JSONL.

1. User: close/stop on an ACTIVE row; **confirm when `busy`**.
2. Intended API: hub remove (see gap below). After success: refresh ACTIVE +
   HISTORY; if closed id was selected, select remaining active or history fallback.
3. Closing the last live root: allow empty ACTIVE list; user may resume or create.
   Attach-only never offers close.

### Failure modes

| Situation | Expected UX |
|---|---|
| **Attach-only** (`attachOnly: true`) | No create/resume/close/live composer; HISTORY (or SESSIONS) SSE only |
| **`capabilities.roots === false`** | Single SESSIONS list; no ACTIVE/HISTORY tabs; no `/v1/roots` calls required (503 if called) |
| **Resume child session** | Server 400 with message; alert/toast; no selection change to live |
| **Activate unknown / non-live id** | 400; keep previous selection; refresh roots |
| **Deleted while viewing** | SSE/WS errors; clear transcript or show empty/error; refresh lists; pick fallback selection |
| **WS drop** | Existing reconnect backoff; transport line shows reconnecting for **selected** root |
| **Ops without live** | Composer disabled; no queue drain |
| **Token / auth failure** | Existing bootstrap error empty-state |

### Deep links

On cockpit load, after token cookie handoff (server strips `?token=` and keeps
other query params):

| Query | Behavior |
|---|---|
| `?root=<id>` | If id is a live workspace → select it, call activate, open WS `?root=`. |
| `?session=<id>` | Same resolution order: live root first, else durable HISTORY (SSE). |
| both present | `root` wins over `session`. |
| invalid id | Safe fallback: first live root, else first HISTORY row; no error page. |

Shareable example (after auth handoff): `/attach?session=<durableId>` or
`/attach?root=<liveId>`.

Additive workspace navigation (WEBUI.3 / #1073; normative detail in
`docs/web-cockpit-contract.md` §5):

| Query | Meaning |
|---|---|
| `?mode=` | `chat` \| `code` \| `team` \| `project` \| `ops` (default `chat`) |
| `?surface=` | Registry surface id (`plans`, `files`, `roster`, …) |
| `?entity=` | Opaque entity id for the surface |
| `?path=` | Workspace-relative file path (implies Code) |
| `?agent=` | Child agent id (implies Team) |
| `?pane=` | Plugin pane id (implies Ops panes) |

Example: `/attach?root=<liveId>&mode=project&surface=plans&entity=<planId>`.
Invalid or unsupported mode/surface falls back safely; `root`/`session` resolution is unchanged.


Additive mode/surface/entity deep links (`mode`, `surface`, `path`, `agent`,
`pane`, `entity`) are specified in
[web-cockpit-contract.md §5](web-cockpit-contract.md#5-deep-link-grammar-additive)
and must not break the `root` / `session` semantics above.

**Keyboard (rail focused):** `j` / `ArrowDown` next workspace, `k` / `ArrowUp`
previous. Ignored while focus is in an input/textarea (composer safe).

**Fork default:** stay on the current selection; the new durable id appears in
HISTORY (resume separately). Parent lineage is `forkedFrom` on the session list
DTO when present.

### API map and gaps

| Behavior | Existing surface | Gap? |
|---|---|---|
| List live workspaces | `GET /v1/roots` | — |
| Create workspace | `POST /v1/roots` | — |
| Activate on select | `POST /v1/roots/{id}/activate` (client on select) | — |
| Resume history → live | `POST /v1/roots/{id}/resume` | — |
| Scoped ops/events | `?root=` on ops/ws/status/files | — |
| List / fork / rename / delete durable | `/v1/sessions*` | — |
| Children listing | `GET /v1/sessions/{id}/children` | Not in root switcher (by design) |
| Close/stop live workspace | `DELETE /v1/roots/{id}` | — |
| Permission/question pending per root | `RootSummary.permissionPending` / `questionPending` + 2s roots poll | — |
| `forkedFrom` on session list | List item includes `forkedFrom` when session was created via Fork | — |
| Live title after rename | Rename hits durable meta | **Soft gap:** confirm hub title refresh path |
| Deep link query | `?root=` / `?session=` on cockpit load | — |

Do **not** invent parallel protocols (second WS multiplex schema, ad-hoc event
buses) unless a child issue records a hard gap and updates this section.

### Non-goals and sibling boundaries

| Out of scope here | Owner |
|---|---|
| Visual declutter / density of chrome | #399 (children #912–#915) |
| Broad v0.2.x feature parity (plans, goals, MCP, …) | #516 |
| Cockpit auth/TLS/rate-limit hardening | #541 |
| Harness trust UX / timeline export entry points | #809 (TUI-first; web residual under #516) |
| Subagent tree **graph** visualizer | #523 (TUI-first). Epic #1069 extends the former web non-goal to observe-first list/board/review only — graph clone remains out of scope ([web-cockpit-contract.md](web-cockpit-contract.md)) |
| Full TUI multi-agent parity | non-goal for #467; Team observe/control track is #1081–#1089 |
| Implementing UI/server in this contract issue | #917–#921 |
| Persisting drafts across full page reload | non-goal for #918 |
| Sound / desktop notifications | optional later |
| Session retention policy UI | non-goal |

### Implementation ownership

| Issue | Delivers against this contract |
|---|---|
| #917 WEBSESS.2 | Activate-on-select, rail identity, close/stop, attach-only fallback |
| #918 WEBSESS.3 | Per-workspace client state isolation + transport rules |
| #919 WEBSESS.4 | Attention badges, background observation, header aggregate |
| #920 WEBSESS.5 | History polish, resume/fork UX, deep links |
| #921 WEBSESS.6 | Tests + operator docs/smoke for the multi-session happy path |

### Manual smoke (multi-session)

Run after #917+ land; checklist expanded in #921:

1. `./strike serve --provider echo` → open cockpit.
2. Create a second workspace → two ACTIVE rows; select each → activate + isolated drafts.
3. Queue a prompt on A while busy; switch to B; return to A → queue intact.
4. On B, trigger permission (`run echo hi`); stay on A → needs-you affordance; switch → dialog; reply with correct root.
5. HISTORY → resume a prior root → lands live on ACTIVE.
6. Fork → new HISTORY id; rename; delete unused historical.
7. Close/stop one workspace → leaves ACTIVE without crashing.
8. `./strike serve --attach-only --session-dir …` → no create/close; SSE only.

### Plugin manager and panes (web parity)

Bootstrap capabilities `plugins` / `panes` are true when `Services.Plugins` /
`Services.Panes` are wired (live `strike serve`). The inspector **plugins** and
**panes** tabs provide TUI `/plugin` lifecycle parity and pane/1 rendering
without importing TUI types ([plugin-panes.md](plugin-panes.md) §14).

When `capabilities.plugins` / `capabilities.panes` are true, the inspector exposes:

- **plugins** — list/install/enable/disable/remove/trust/update with the same
  capability review posture as TUI `/plugin` (trust-preview before trust;
  `confirm:true` before remove/update; env **keys** only — never values).
- **panes** — list enabled pane/1 contributions; mount static views (client
  resolves `valueFrom` against host feeds) or process panes (server-supervised
  JSONL subprocess; browser receives view trees / errors only).
- **Forbidden on the wire:** `PluginRoot`, resolved secret env values, Go
  `window` / `tea.Msg` / lipgloss / terminal escape passthrough.
- **panes** — mount enabled `pane/1` contributions; static views bind host feeds
  client-side; process panes are supervised by the strike server and the browser
  only receives view trees / errors (no `PluginRoot`, no TUI types).

Mutations that change trust or install state require a live host (not attach-only).

## Long-session performance (WEBUI.20 / #1087)

Long historical transcripts and large team rosters stay responsive by bounding
**paint work**, not by deleting durable events:

| Mechanism | Behavior |
|---|---|
| Stream batching | WS/SSE envelopes coalesce to **≤1 React commit per animation frame** (`web/src/streamBatch.ts` → `client.events`) |
| Transcript virtualization | Windowed DOM via `VirtualList` — full `items[]` stays in memory; mounted rows capped (~80) |
| Content bounds | Markdown, fenced code, diffs, and tool output are chunked/truncated for display; session memory retains full text |
| Lazy surfaces | Team / Code / Project / Ops panels load with `React.lazy` so Chat startup does not pull those bundles |
| Team isolation | `TranscriptList` is memoized; roster/task/attention updates do not rerender the transcript when `items` is unchanged |
| Scroll anchors | Prepend/backfill preserves scroll position; jump-to-latest + unread count track while scrolled up |
| Live region | Status announcements stay batched (not per-token) to avoid screen-reader floods |

### Fixture and thresholds

Deterministic fixture: `web/src/perf/fixture.ts` (≥10 000 protocol events, mixed
stream/tool/diff cells, 50-agent roster with task/attention updates).

Committed CI gates: `web/src/perf/thresholds.ts` + `web/src/perf/perf.test.ts`
(reduce wall time, mounted-cell bound, stream batching, roster size).

Local reference profile (desktop + throttled-mobile budgets for TTI, longest
main-thread task, DOM nodes, heap trend, input/scroll latency):

```sh
cd web && npm run profile:perf
```

Browser measurement: DevTools Performance on a hydrated long session; for
mobile guidance use CPU 4× throttling. Advisory budgets live in
`PERF_THRESHOLDS_LOCAL` — CI runs the stable subset only.

### Accessibility

Virtualization keeps semantic message labels, keyboard focus order inside the
mounted window, and search/export over the full in-memory transcript. The
transcript `role="log"` does not announce token-by-token deltas.


## Progressive modes and surface registry

| Mode | Primary | Typical secondary surfaces |
|---|---|---|
| **Chat** (default) | transcript + composer | context, runtime, queue, asks |
| **Code** | file tree / read / markdown / diff | changed files, diagnostics, reviewed apply |
| **Team** | roster / agent detail | board, attention, messages, artifacts, ledger, controls |
| **Project** | plans / goals / issues / memory | workflows, project exports |
| **Ops** | providers / settings | MCP, plugins, panes, timeline, diagnostics, sandbox |

Surfaces register in `web/src/surfaces.ts` (built-ins + dynamic `pane/1`).
Capability-missing surfaces hide; deep links that name an unavailable surface
show an empty-state without attempting mutations.

### Deep links

Compatible with existing `?root=` / `?session=` / attach handoff. Additive:

| Param | Meaning |
|---|---|
| `mode` | `chat` \| `code` \| `team` \| `project` \| `ops` |
| `surface` | registry id (`roster`, `files`, `mcp`, …) |
| `entity` | path, artifact id, agent id, … |
| `agent` | child/agent session focus |
| `pane` | plugin pane id (Ops) |

Root isolation: every live query, event, and mutation is scoped to one root.
Switching roots never merges transcript caches (`ClientState.byID`).

### Attach-only and `--read-only`

| Mode | Transcript | Mutations (ops, file apply, team control, settings PATCH) |
|---|---|---|
| Live | WS stream | Allowed when capability + auth admit |
| Attach-only | SSE history | **Blocked** (HTTP 403 / UI disabled) |
| `--read-only` serve | Live or attach | **Blocked** at server admit layer |

Team human controls and reviewed file apply require public Ops, actor,
permission, audit, CAS, and idempotency (see
[human-orchestration-ops.md](human-orchestration-ops.md)).

### Security boundaries (summary)

- Loopback bind only; remote access via SSH local forward (see above).
- Auth: one-time `/attach?token=` cookie handoff or `Authorization: Bearer` — not
  long-lived `?token=` on `/v1/*`.
- Credentials redacted in exports, diag, timeline, and transcript scrapes.
- Path confinement on file read/apply; wrong-root denial on multi-root ops.
- Stale CAS / idempotency keys fail closed on team and file mutations.
- Unsupported capability → hide or 501; never a silent no-op success.

Sibling epic boundaries (not absorbed by #1069): **#541** / **#1032** (serve
hardening & security audit depth), **#1056** (human collaboration), **#1058**
(adaptive orchestration policy), **#1060** (cross-device continuity, push,
remote approval).

## Operator smoke checklist

Run against `make serve` (echo provider) unless noted.

### Chat
1. Send a turn; confirm user + assistant cells stream.
2. Queue a second prompt while busy; reorder/edit/clear queue.
3. Trigger a permission ask (`run echo hi`); Allow once; dialog dismisses.
4. `/export` or header ↓ downloads markdown of the loaded transcript.

### Code
1. Mode **Code** → files surface; tree/search/read a workspace file.
2. Open a changed-file diff; with live + `fileApply`, review and apply a confined edit (or confirm attach-only blocks apply).

### Team
1. With multi-agent activity (or deep link `?mode=team&surface=roster`), open roster/board/attention.
2. Open artifacts/ledger/review when capabilities allow.
3. Live only: team control actions show CAS/idempotency errors clearly when stale; attach-only shows controls disabled.

### Project
1. Plans / goals / issues / memory list; create/edit where not attach-only.
2. Workflows catalog; start blocked in attach-only.
3. Export memory/issues from project export surface.

### Ops
1. Settings: appearance, theme catalog preview/apply, provider auth row.
2. MCP status; plugins list; panes list/mount when caps true.
3. Timeline + diagnostics export; sandbox explain.

### Trust-boundary smoke
1. Attach-only: composer disabled; POST `/v1/ops` → 403.
2. Wrong `?root=` / activate another root: events do not leak across caches.
3. File apply outside workspace → denied; audit/permission correlation preserved.
4. Team op with stale CAS or replayed idempotency key → conflict/reject, no double apply.
5. Missing capability surface deep link → unavailable empty-state, no mutation.

### Automated browser smoke

```sh
make web-e2e
```

Covers desktop, tablet, **320 CSS px** mobile, keyboard/focus, reduced-motion
friendly paths, multi-root switch, reconnect/deep links, and attach-only
(`web/e2e/cockpit.spec.ts`, `attach-only.spec.ts`). Artifacts under
`web/e2e-artifacts/`.

### Performance

See [Long-session performance](#long-session-performance-webui20--1087). CI
enforces the stable fixture subset; full browser profile is local via
`cd web && npm run profile:perf`.

## Conformance closeout (WEBUI.21 / #1090)

Epic [#1069](https://github.com/jonathanung/strike/issues/1069) is complete when:

1. Parity matrix in [web-cockpit-contract.md](web-cockpit-contract.md) §8 has no
   unknown/partial row without owner — **done** (shipped / non-goal / deferred `/init`).
2. This operator doc covers modes, registry, deep links, capabilities, endpoints,
   root isolation, attach-only, Team observe/control, and security — **done**.
3. Stale “read-only server” / “no web plugins” descriptions corrected — **done**.
4. Operator + automated smokes above are the release gate — run before tagging.
5. Long-session thresholds from #1087 met or tracked — **committed in**
   `web/src/perf/thresholds.ts`.
6. Sibling boundaries #541, #1032, #1056, #1058, #1060 unchanged — **done**.
7. Changelog summarizes the cohesive workspace without claiming native/offline/push/collaboration features not shipped — see `CHANGELOG.md` `[Unreleased]`.

## Vite dev / production web toolchain

Production cockpit HTML is **embedded** in the Go binary
(`internal/frontend/server/static/index.html`). Optional `web/` is a Vite workspace for
local UI iteration and CI asset checks.

```sh
# terminal 1 — API (loopback)
./strike serve --provider echo

# terminal 2 — Vite dev server (proxies /health and /v1 → strike)
cd web && npm install && npm run dev
# open http://127.0.0.1:5173
```

| Env | Meaning |
|---|---|
| `STRIKE_API_ORIGIN` | Proxy target (default `http://127.0.0.1:8787`) |
| `VITE_HOST` | Vite bind host (default `127.0.0.1`; use `0.0.0.0` only with care) |
| `VITE_PORT` | Dev port (default `5173`) |

```sh
make web-build   # npm ci && npm run build → internal/frontend/server/static
```

CI runs `make web-build` when `web/package.json` is present. The Go binary does
not require Node at runtime: generated Vite assets under `internal/frontend/server/static`
are embedded at compile time. Edit sources under `web/src`, then build.

### Theme

Cockpit colors track the stock token file (`schemas/ui-tokens.json`) and
`internal/frontend/tui/theme.Default()` (royal-purple accent, dark/light
adaptive pairs). CSS variables live in `web/src/styles.css`; keep them aligned
with the token file (see `web/src/theme.test.ts` and [theme.md](theme.md)).

**Catalog (WEBUI.11):** `GET /v1/themes` lists bundled/user/project/plugin themes
with portable semantic color roles and provenance. `GET /v1/themes/{id}` returns
one entry. Selecting a theme in Settings **previews** immediately (CSS variable
overrides); **Apply** persists via `PATCH /v1/settings` `{ "theme": "<id>" }`.
Cancel/Revert restores the prior applied theme. Appearance (`auto`/`light`/`dark`)
stays a separate browser control (`data-appearance`). TUI-only glyph/chrome fields
are omitted from the wire payload.

Lifecycle: run Vite as a **sibling** process of `strike serve` (two terminals or
a process supervisor). Strike does not spawn Vite as a child.

## Security (summary)

- Bind is **loopback only** (`127.0.0.1:8787` default). Non-loopback `--addr` is rejected.
- Remote access: `ssh -L 8787:127.0.0.1:8787 user@host` (no cleartext LAN expose / no TLS terminator in-process).
- CORS reflects loopback browser origins only.
- Treat the token like a password; do not commit it.
- See **Threat model** under Remote access above.

## Layout

> **Web cockpit:** The TUI `/loop` shortcut is superseded by durable **Goals** and **Workflows** controls under Project mode (WEBUI.12). The browser does not add a session-only loop engine.


Responsive shell profiles (WEBUI.4 / #1074; contract §4):

| Profile | Width | Chrome |
|---|---|---|
| Desktop | ≥ 1024 CSS px | Rail + canvas + optional inspector drawer |
| Tablet | 600–1023 | Canvas; rail and inspector as overlay drawers + backdrop |
| Phone | ≤ 599 (down to 320) | Single canvas; bottom mode bar; rail/inspector as bottom sheets |

Fixed chrome uses `safe-area-inset-*`. Phone composer uses a sticky row and 44px primary controls; `prefers-reduced-motion` disables nonessential transitions. Escape closes overlay panels (not blocking ask dialogs).

## Manual checklist

1. `./strike serve --provider echo` → open the token-free loopback URL.
2. Send a message → streamed `text.delta` → `turn.completed`.
3. Send `run echo hi` → permission modal → allow once → tool result.
4. Switch permission mode / agent from toolbar.
5. RO attach: pick another session id → SSE transcript only.
6. Multi-session: see **Manual smoke (multi-session)** under the multi-session UX contract
   (requires WEBSESS.2+ UI; contract-only changes need no runtime check).
7. `./strike serve --auth --token test` → open printed cockpit URL; cookie handoff + live stream.
8. `./strike serve --addr 0.0.0.0:8787` → error (loopback only).
9. `./strike serve --expose` → error with SSH migration hint.

### Provider auth & policy (WEBUI.10)

| Method | Path | Notes |
|---|---|---|
| `GET` | `/v1/providers` | Provider rows with authed state + methods (`apiKey`/`device`/`oauth`); no secrets |
| `GET` | `/v1/models` | Catalog models (`?provider=`). Echo returns **200** `{ "models": [{ "id": "echo", "provider": "echo", … }] }`; genuine catalog failures stay **502** |
| `POST` | `/v1/auth/key` | Set API key (live only; attach-only 403). Storing a key does not verify it — the row reports `keyState: "stored"` until probed (#1531) |
| `POST` | `/v1/auth/probe` | Real credential probe: presents the stored key to the provider's models endpoint → `status` `valid`/`invalid`/`unreachable`/`no-key`/`unsupported`; decisive outcomes update the row's `keyState` (`verified`/`rejected`) |
| `DELETE` | `/v1/auth/{provider}` | Logout |
| `POST` | `/v1/auth/device` | Start device login → code + verification URL |
| `GET`/`DELETE` | `/v1/auth/device/{id}` | Poll status / cancel |
| `POST` | `/v1/auth/oauth` | Start paste-assisted OAuth (no invented browser callback) |
| `POST` | `/v1/auth/oauth/{id}/complete` | Paste redirect URL or code |
| `GET`/`POST`/`DELETE` | `/v1/custom-providers` | Custom provider CRUD (capability `providers`) |
| `GET`/`POST` | `/v1/scheduler/presets` | Scheduler preset catalog + apply |
| `GET` | `/v1/config-sources` | Typed config source inspection (no raw file editor) |

Attach-only permits status/catalog reads; credential and settings mutations return **403**.

### Settings dials (`GET`/`PATCH /v1/settings`)

Wire fields match `host.UserDefaults` / TUI `/settings` vocabulary. `PATCH` is
partial: omit or send empty string to leave a field unchanged.

| Group | PATCH fields | Notes |
|---|---|---|
| Runtime defaults | `provider`, `model`, `agent`, `effort`, `mode` | `mode` is permission mode (`default`\|`plan`\|`soft-approve`\|`accept-edits`\|`yolo`) |
| Theme | `theme` | TUI theme id stem |
| Config dials | `sandbox`, `notify`, `leanCode`, `deferTools`, `sessionWorktree`, `autoupdate` | Same tokens as config JSON |
| Auto-approve | `permissionAutoApproveSeconds`, `permissionAutoApproveExclude`, `maxChildDepth` | Seconds/depth are **strings** on PATCH (`off`\|`0`\|`1-60`; `default`\|`0`\|`1-8`). Exclude: omit = unchanged; `[]` clears; non-empty replaces |
| Compaction | `compactionStrategy`, `compactionModel`, `compactionThreshold`, `compactionBuffer`, `keepUserTurns`, `pruneProtectTokens`, `pruneMinimumTokens`, `pruneKeepUserTurns`, `pruneProtectTools` | String dials (`trim`\|`summarize`; threshold/buffer ints as strings; `default`/`0` resets; model/tools `-` clears) |
| Guardrails (#1577) | `turnTimeoutS`, `maxSessionCostUSD`, `maxTurnTokens`, `agentBudget{maxWallClockS,maxTokens,maxCostUsd,maxToolCalls,maxDangerousTools,stallAfterS,loopDetectN}`, `delegationPolicy{mode,tinyPromptRunes,maxPathsLocal,maxLiveChildren}`, `childHandoffRequired`, `childIsolation` | **Strings** on PATCH, numbers/bools on GET. Timeout: `default` (1800s)\|`off`\|`0`\|seconds\|duration (`30m`). Caps/budget: `off`\|`0`\|`unlimited` or a positive number. Delegation mode: `off`\|`advise`\|`enforce`\|`default`; `tinyPromptRunes`/`maxPathsLocal`: `default`\|`off`\|positive int. Handoff: `on`\|`off`\|`default`. Isolation: `shared` (children run in the parent workdir — the default)\|`worktree`\|`default` |

Successful `PATCH` returns the updated defaults snapshot (same shape as `GET`).

Guardrail dials are global (`~/.strike/config` `session.*`) gateway defaults.
`GET` reports effective values with product defaults resolved
(`turnTimeoutS` 1800, `-1` = off; caps `0` = unlimited; delegation `mode`
`enforce` when omitted; `childHandoffRequired` true when omitted;
`childIsolation` `shared`|`worktree`). The group is validated as a whole: an
invalid value returns **400** naming the field and writes none of them. They
are read when a strike process assembles, so a running `strike serve` applies
changes after restart; CLI `--turn-timeout` / `--max-cost` still override.
A project `.strike/config` that sets the same `session.*` key wins over the
global value at load.

The cockpit renders these in **Settings → Guardrails**: one control per dial
showing the daemon-reported effective value. Tightening applies at once;
loosening (raising or removing a cap, delegation `off`/`advise`, handoff
optional, children back on the host) asks for confirmation first. A rejected
value shows the daemon's 400 reason. The Git panel's worktree card points at
the Child isolation control instead of the config file.

`GET` also carries `autoupdateEffective` (the dial after
`config.EffectiveAutoupdate`: empty/unknown → `notify`) and, once a check has
run, `updateCheck` (the last `GET /v1/update` result). Reading settings never
probes the network.

### Update check (`GET /v1/update`)

Bootstrap capability `updateCheck` is true when settings are readable and
`strike serve` wired the probe. The route reuses the TUI startup probe
(`internal/product/update.StartupProbe`) with its 24h cache, kept in
`~/.strike/cache/update-check-web.json` so a web check does not consume the
TUI's once-per-release notice. It is **check-only**: the probe runs in notify
mode even when the dial is `auto`; installing stays a host-side action
(`strike upgrade`).

Response: `{ mode, state, current?, latest?, available, message?, error?, checkedAt?, canReplace }`.
`state` is `off` (dial off — no network request is made), `available`,
`current` (only after a successful check), `failed` (offline, rate limit, API
error — never reported as up to date), or `unknown`. Missing capability returns
501.

The cockpit calls the route once per gateway after bootstrap reports
`capabilities.updateCheck`, and again from **Settings → Updates** (dial change
or *Check now*). The Updates section edits the `autoupdate` dial and states the
result (`off`, available, up to date, or the failure reason). An `available`
result adds one notification-centre row keyed by the release; dismissing it
holds until a newer release. The web never installs.
The cockpit Settings dialog loads `GET`, edits sections, and saves via `PATCH`.
Browser color scheme (auto/dark/light) is local-only (`data-appearance` +
`localStorage`), not a host config key.

With `--auth`, authenticate `/v1/*` using either of:

- `Authorization: Bearer <token>`
- HttpOnly `strike_serve_token` cookie (set automatically when you open
  `/attach?token=…` or `/?token=…` — the server redirects to a token-free URL)

Query-string `?token=` is **not** accepted on `/v1/*`.


| `GET` | `/v1/goals` | **yes** | List loop-harness goals (host-safe DTOs) |
| `POST` | `/v1/goals` | **yes** | Set (create) a pending goal |
| `GET` | `/v1/goals/{id}` | **yes** | One goal with criteria matrix + spend |
| `POST` | `/v1/goals/{id}/run` | **yes** | Run/resume loop until terminal or pause (live) |
| `POST` | `/v1/goals/{id}/pause` | **yes** | Pause an active goal (live) |
| `POST` | `/v1/goals/{id}/resume` | **yes** | Mark paused/pending active without running (live) |
| `POST` | `/v1/goals/{id}/abort` | **yes** | Abort a non-terminal goal (live) |
| `GET` | `/v1/goals/{id}/log` | **yes** | Committed iteration log (`?iter=N` optional) |
`POST /v1/goals` (`description`, `criteria[]` CheckSpec strings, optional

### Goals / loop harness

Bootstrap capability `goals` is true when the host exposes `host.Goals`. The
inspector **goals** tab lists project goals (status, cost, criteria matrix),
opens detail with the iteration log, and can create a pending goal via
`POST /v1/goals` (`description`, `criteria[]` CheckSpec strings, optional
budgets). **Run / pause / resume / abort** require a live session
(`503` when attach-only); list/get/log/set work whenever the capability is on.

Default host run path is evaluate-only (empty tool allowlist) — same safety
default as `/goal run` in the TUI. Wire JSON is camelCase host-safe DTOs only.

### Session loops

`/loop` from the TUI, served by the daemon (#1588). A loop re-injects a
prompt into **one live session** on an interval until it is stopped or the
session closes. Loops are ephemeral: they do not survive a daemon restart,
a session close, or root removal. Anything that must persist or start new
sessions is a [Schedule](#schedules). Bootstrap capability `loops` is true
when a live engine is attached and the gateway is not `--read-only`.

| Method | Path | Body / response |
|---|---|---|
| `GET` | `/v1/sessions/{id}/loops` | `{loops:[...], max, minIntervalMs, maxIntervalMs}` |
| `POST` | `/v1/sessions/{id}/loops` | `{interval, job}`; `201 {loop}` |
| `DELETE` | `/v1/sessions/{id}/loops/{loopID}` | `{ok, stopped:1}`; `404` unknown id |
| `DELETE` | `/v1/sessions/{id}/loops` | stop all; `{ok, stopped:n}` |

Grammar matches the TUI exactly (`pkg/loopspec`): `interval` is a Go
duration (`30s`, `15m`, `2h`, `1h30m`) or bare minutes (`15`), between 1s
and 168h. At most 16 loops per session; ids are `l1`, `l2`, … and never
reused. Errors: `400` bad interval/job, `404` session not live
(`code: session_not_live`) or unknown loop, `409` the 17th loop, `410`
session already closed, `403` `--read-only`. Start and stop share the
`POST /v1/ops` rate limit and are audited as `loop.start` / `loop.stop`.

A loop object carries `id`, `interval`, `intervalMs`, `job`, `runs`,
`createdAt`, `nextAt`, and when set `lastRunAt` and `lastError`. A firing is
an ordinary `user.input`: while a turn is busy it waits on the same input
queue as a queued send, and it counts as a normal turn for usage, budgets,
and guardrails. `GET /v1/status` carries `loops`, the active count.

**Cockpit.** The composer takes the same grammar: `/loop <interval> <job>`,
`/loop` or `/loop list`, `/loop stop [id]` (no id stops all). The line is
handled against these routes and is never sent as a prompt; the daemon's
error sentence shows under the composer. Active loops show as rows in the
queue pane (id, interval, job, run count, next fire, Stop). The rows poll
the list route every 5 s, since loop state is not on the event stream yet.
Firings appear in the transcript as plain user prompts: `user.input` carries
no loop id, so there is no per-firing loop badge.

### Schedules

`~/.strike/schedules.json` (mode `0600`) is the clock. The daemon ticks
about every 20 seconds and starts a background root session; it does not
switch the cockpit's active session.

| Method | Path | Body |
|---|---|---|
| `GET` | `/v1/schedules` | `{schedules:[...]}` |
| `POST` | `/v1/schedules` | spec below; `201` the stored schedule. `source: script` requires `confirm: true`, else `400 confirm required` and nothing is stored |
| `GET` | `/v1/schedules/{id}` | one schedule |
| `PATCH` | `/v1/schedules/{id}` | full spec; history is kept. A script schedule needs `confirm: true` again (re-arms); `source` cannot change to or from `script` (`400`; delete and recreate) |
| `DELETE` | `/v1/schedules/{id}` | |
| `POST` | `/v1/schedules/{id}/pause` | |
| `POST` | `/v1/schedules/{id}/resume` | arms the next future slot; does not catch up |
| `POST` | `/v1/schedules/{id}/run` | one session now; `409` when overlap is skip and the last session is busy, or while a script run is in flight; returns the `started` run |
| `GET` | `/v1/schedules/{id}/history` | `{runs:[...]}` newest first. Runs carry `startedAt`, `endedAt`, `exitCode`, `outputPreview`, `outputRef`, `decision` for script runs |
| `GET` | `/v1/schedules/{id}/runs/{runId}/output` | `text/plain` scrubbed output tail of a script run; `404` when none. Read-only and attach-only servers may read it |

Spec fields: `name`, `kind` (`cron` \| `interval` \| `once` \| `heartbeat`),
`cron` (5 fields, timezone-aware), `every` (Go duration, minimum `1m`),
`at` (RFC3339), `timezone` (IANA, default `UTC`), `source` (`prompt` \|
`goal` \| `workflow` \| `script`), `prompt`, `goalId`, `workflow`, `workspaceId`,
`permissionMode` (used only when the workspace has no mode; empty means
`plan`), `overlap` (`skip` \| `queue` \| `allow`, default `skip`),
`missed` (`skip` \| `once`, default `skip`), `enabled`.

Script schedules (`source: script`, see
[design/script-schedules.md](design/script-schedules.md)) take `command`
(required, max 16 KiB), `dir` (absolute, must exist; empty means the
workspace path), `timeout` (Go duration, `10s`..`6h`, default `30m`),
`secretRefs` (`secret://env/NAME`), and `confirm: true`. `overlap` must be
`skip`. The server stamps `armedAt` and `commandHash` (sha256 of the
command) on every confirmed write and never accepts them from the client.
`command` is scrubbed on every read. Create, replace, pause, resume, and
delete of a script schedule each write one `script_run` audit record
(`armed` \| `rearmed` \| `paused` \| `resumed` \| `deleted`) with the
command, its hash, and the client IP. No tool, `/v1/ops` op, MCP bridge,
or SDK call reaches the schedule service.

At start, `strike serve` adds managed `deny` rules for `read` and `bash`
on the configured `--token-file` path to every session it hosts, so an
agent cannot read the cockpit token and call these routes itself.

A workspace permission mode wins over `permissionMode`. Heartbeat fires
compare a fingerprint of the workspace (git HEAD and status, otherwise a
walk). An unchanged tree records `quiet` and does not start a session.
Cron slots that fall in a DST gap are skipped. A repeated DST hour fires
once, at the earlier offset.

Events use the same envelopes as session JSONL (`type` + `time` + `data`).

Example — full echo turn via HTTP:

```sh
TOKEN=...
# stream live events
curl -N -H "Authorization: Bearer $TOKEN" http://127.0.0.1:8787/v1/live/events &
# send a prompt
curl -s -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{"type":"user.input","data":{"text":"hello"}}' \
  http://127.0.0.1:8787/v1/ops
```

Permission asks appear as `permission.asked` events; resolve with
`permission.reply` (UI modal or `POST /v1/ops`).



### Memory & issues write/export

| `GET` | `/v1/memory` | mode | List entries. `?scope=project` (default) or `global`. Response adds `markdownFile` and `auditFile` when the store is the file projection. |
| `PUT` | `/v1/memory/{key}` | **yes** | Put/replace entry (`{value,tags?,author?,scope?}`); query `scope` is the fallback. Values, keys, and tags are credential-scrubbed. Blocked in attach-only. |
| `DELETE` | `/v1/memory/{key}` | **yes** | Delete entry (`?scope=`); blocked in attach-only |
| `GET` | `/v1/memory/mode` | mode | Session memory mode (`normal`, `incognito`, `temporary`) for `?root=`. `wired:false` when no live engine is bound. |
| `PUT` | `/v1/memory/mode` | **yes** | Set that mode (`{mode}`) for `?root=`. Persists on the session meta sidecar. |
| `GET` | `/v1/memory/proposals` | mode | Pending lesson proposals (#1874), oldest first. A pending proposal is never auto-loaded. |
| `POST` | `/v1/memory/proposals` | **yes** | Propose a lesson (`{key,value,scope?}`; scope `project` default or `global`). Text is credential-scrubbed. The proposing session is the live root's. 403 for an incognito/temporary session or an untrusted workspace. |
| `PATCH` | `/v1/memory/proposals/{id}` | **yes** | Edit a pending proposal (`{key,value,scope}`). |
| `POST` | `/v1/memory/proposals/{id}/approve` | **yes** | Store it as a `lesson`-tagged entry in its scope and drop it from pending. 409 when the key exists unless `{replace:true}`. |
| `DELETE` | `/v1/memory/proposals/{id}` | **yes** | Dismiss a pending proposal; stores nothing. |
| `GET` | `/v1/sessions/{id}/lessons-used` | mode | Memory entries recorded as auto-loaded into that session (`{sessionId, entries:[{scope,key,value,tags,injectedAt}]}`); the "lessons used" marker. Display-only. |
| `GET` | `/v1/knowledge` | mode | Pinned paths for `?scope=project` or `global`. |
| `PUT` | `/v1/knowledge` | **yes** | Pin (`{path,note?,author?,scope?}`). Notes are scrubbed. Blocked in attach-only. |
| `DELETE` | `/v1/knowledge` | **yes** | Unpin (`{path,scope?}`). Blocked in attach-only. |
| `GET` | `/v1/memory/export` | mode | Download portable `strike-memory.json` (TUI format) |
| `GET` | `/v1/backup/export` | mode | Download `strike-backup.json`: whole-gateway archive (sessions, memory, schedules, non-secret settings) with manifest, sha256 per section, and named exclusions (#1339) |
| `POST` | `/v1/backup/restore` | live (dry run: mode) | Body is the archive. `?dryRun=1` reports create/skip/copy per record without writing; otherwise restores through each store. Never overwrites: identical records skip, differing ones restore as copies |
| `GET` | `/v1/preview/ports` | mode | Live preview (#1341): listening loopback TCP ports on the gateway host (`detection: proc` on Linux, `unsupported` elsewhere). The gateway's own port is omitted and reported as `self`. |
| `GET` | `/v1/preview` | mode | The selected preview target `{selected, port, origin, path}`. `?probe=1` adds reachability and framing (`ok`/`refused`). |
| `PUT` | `/v1/preview` | mode | Select a port (`{port}`; there is no host field). Returns the target plus probe. The proxy serves only this port. |
| `DELETE` | `/v1/preview` | mode | Stop proxying. |
| any | `/preview/<key>/…` | key | Proxies to `127.0.0.1:<selected port>`. The per-port random key is the capability (the cookie is SameSite=Strict and the frame is sandboxed). Gateway `Authorization`/`Cookie` are stripped, app `Set-Cookie` is dropped, and the response gets a `CSP sandbox` (no `allow-same-origin`) with `frame-ancestors 'self'`. Unselected ports and wrong keys get 404. |
| `POST` | `/v1/memory/import` | **yes** | Import (`{path}` or `{data}`, `replace?`); blocked in attach-only |
| `POST` | `/v1/issues` | **yes** | Create issue (`{title,body?}`); blocked in attach-only |
| `POST` | `/v1/issues/{id}/close` | **yes** | Close issue; blocked in attach-only |
| `GET` | `/v1/issues/export` | mode | Download portable `strike-issues.json` (TUI format) |
| `POST` | `/v1/issues/import` | **yes** | Import (`{path}` or `{data}`, `replace?`); blocked in attach-only |

### Artifacts & decision ledger (read-only, WEBUI.15 / #1082)

Bootstrap advertises `capabilities.artifacts` / `capabilities.ledger` only when
the host wires those stores. The only browser write route is
`POST /v1/sessions/{id}/report`, which stores a daemon-generated
`session_report` (agent tools may read that type but never write it); other
artifact create/update and ledger append/invalidate/supersede stay
agent/tool-owned.
Attach-only may read authorized rows; missing capability returns **501**.

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/v1/artifacts` | mode | Bounded list (`type`, `scope`, `sessionId`, `includeExpired`, `limit`≤200, `offset`). Visibility uses `actorSession` + `actorRoot` (default: live/`?root=` session). Owner-access rows require matching owner session; team-access requires matching owner root. |
| `GET` | `/v1/artifacts/{id}` | mode | Full artifact (`?version=` optional). Retained snapshots (last 20) replay through `?version=`. **404** when missing, not visible, or the version was dropped. Content/title are credential-scrubbed on write. |
| `GET` | `/v1/ledger` | mode | History list (`status`, `kind`, `path`, `taskId`, `authorSession`, `limit`, `offset`) or active slice (`active=1` + optional `path`/`taskId`). Includes invalidated/superseded provenance fields. |
| `GET` | `/v1/ledger/{id}` | mode | One ledger entry (any status). **404** when missing. Statements/evidence scrubbed. |

Limits default to 100 (max 200). DTOs expose version/CAS and lifecycle fields
only — never store filesystem paths or internal Go types. Concurrent agent
writes remain coherent via the underlying stores; browser reads are race-safe.

### Command discovery (WEBUI.6 / #1078)

| Method | Path | Auth | Description |
|---|---|---|---|
| `GET` | `/v1/files/search` | mode | Workspace file search (`q`, `limit`≤50) for `@file` completion; root-scoped via `host.Files.SearchFiles` |
| `GET` | `/v1/files/grep` | mode | Search in files (#1406): plain-text, case-insensitive substring search (`q`, `limit` default 200, ≤500). JSON: `matches` (`path`, 1-based `line`, trimmed `snippet`), `capped`, `filesScanned`. Same index and root scoping as `/v1/files/search` (`.gitignore` honored, `.git` and noise dirs skipped) plus the allowed-root ceiling; symlinks and binary files (NUL in the first 8000 bytes) skipped. Walk caps: 5000 files, 1 MiB per file, 64 MiB per search, 20 matches per file, 5 s; any cap hit sets `capped: true`. 501 when the host has no content search. |
| `GET` | `/v1/search` | **yes** | Global transcript search (#1355): `q` (2–200 chars) matched case-insensitively against every root session's user and assistant message text (tool I/O, reasoning and child streams excluded; text redacted via `pkg/redact` first). On-demand scan, newest session first, one hit per session `{id, title, workspaceId, updatedAt, role, snippet, matchStart, matchEnd, matches}`; at most 50 hits (`capped`), 4 s scan budget (`partial`), `skipped` counts malformed lines. **400** bad `q`; **501** without a session dir |
| `GET` | `/v1/sessions/{id}/reference` | mode | Size of an `@session:<id>` reference (#1550): `chars` of the exact quoted block the daemon attaches (summary + last 10 turns), `tokens` (~4 chars/token, `estimated`), `turns`, `omitted`, `hasSummary`, `self`. 404 for a missing/pruned session. Display only. |
| `GET` | `/v1/file/view` | mode | Document read (`path`). JSON: content, sha256, mtimeUnixNano, binary, truncated, writable, writeBlock. Work-root confined; symlinks rejected. An explicit allowed-root ceiling (workspace `AllowedRoots`, when the registry grows one) also rejects paths outside that list. |
| `GET` | `/v1/file/raw` | mode | One workspace file's bytes (`path`) for the document viewer's image/binary view (#1393). Same confinement as `/v1/file/view` (work root, symlinks rejected, allowed-root ceiling). Allowlisted images (png, jpeg, gif, webp, avif, bmp, ico, svg) are served `inline` with their MIME; everything else as `application/octet-stream` `attachment`. Capped at 1 MiB (the document-view cap): **413** `too_large` past it. Responses carry `Content-Security-Policy: default-src 'none'; …; sandbox` and `nosniff`. Normal `/v1` auth (bearer or the HttpOnly cookie); query tokens are refused. |
| `PUT` | `/v1/file` | **yes** | Save one existing text file (`path`, `content`, `sha256`, `mtimeUnixNano`). 409 `conflict` when the hash or mtime moved; the body has the new revision, not the file text. Plan mode and edit/write deny rules return 403. Does not create files. Attach-only and read-only reject. |
| `POST` | `/v1/history` | **yes** | Enqueue one prompt (`{prompt}`) into project history; blocked in attach-only. Web client calls once per accepted `user.input` (not on replay). |

Composer: Ctrl/Cmd+K opens the command palette (modes, surfaces, slash, skills). Arrow Up/Down on an empty eligible composer walks prompt history. `@` triggers file search (not email-like tokens). Quiet first-run tip teaches palette / slash / `@file`.
