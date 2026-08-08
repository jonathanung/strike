# Web workspace

`strike serve` hosts a React workspace that can drive a **live** engine session
and read-only attach to durable session JSONL logs.

**Progressive workspace contract (epic [#1069](https://github.com/jonathanung/strike/issues/1069)):**
modes (`Chat` default, `Code`, `Team`, `Project`, `Ops`), surface registry,
additive deep links, attach-only matrix, and exhaustive TUI→web parity inventory
live in **[docs/web-cockpit-contract.md](web-cockpit-contract.md)** (WEBUI.1 /
[#1070](https://github.com/jonathanung/strike/issues/1070)). Implementers of
WEBUI.2–WEBUI.21 must follow that document; this page remains the operator
reference for start flags, endpoints, multi-session UX (#467), and security.

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
| `--auto`, `--dangerously-skip-permissions` | Auto-allow permission asks in the live engine (equivalent) |

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
| `GET` | `/v1/sessions` | **yes** | Durable session list (roots only) + `liveId` |
| `GET`/`POST` | `/v1/diag` | **yes** | Redacted prompt/config diagnostic bundle (JSON download; live only; `?root=` when multi-root). Bootstrap capability `diag`. **503** when unsupported (attach-only / no live). |
| `GET` | `/v1/sessions` | **yes** | Session list + `liveId` |
| `GET` | `/v1/sessions/{id}/events` | **yes** | SSE tail of a session JSONL log |
| `GET` | `/v1/sessions/{id}/children` | **yes** | Child/subagent sessions under a root |
| `POST` | `/v1/sessions/{id}/fork` | **yes** | Fork durable session → new id |
| `PATCH` | `/v1/sessions/{id}` | **yes** | Rename (`{ "title" }`) |
| `DELETE` | `/v1/sessions/{id}` | **yes** | Delete durable session (`?force=true` optional) |
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

## Vite dev / production web toolchain

Production cockpit HTML is **embedded** in the Go binary
(`internal/server/static/index.html`). Optional `web/` is a Vite workspace for
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
make web-build   # npm ci && npm run build → internal/server/static
```

CI runs `make web-build` when `web/package.json` is present. The Go binary does
not require Node at runtime: generated Vite assets under `internal/server/static`
are embedded at compile time. Edit sources under `web/src`, then build.

### Theme

Cockpit colors track the stock TUI palette in `internal/tui/theme.Default()`
(violet accent, solid surfaces, dark/light adaptive pairs). CSS variables live
in `web/src/styles.css`; keep them aligned when changing `theme.go` (see
`web/src/theme.test.ts` and [theme.md](theme.md)).

Lifecycle: run Vite as a **sibling** process of `strike serve` (two terminals or
a process supervisor). Strike does not spawn Vite as a child.

## Security (summary)

- Bind is **loopback only** (`127.0.0.1:8787` default). Non-loopback `--addr` is rejected.
- Remote access: `ssh -L 8787:127.0.0.1:8787 user@host` (no cleartext LAN expose / no TLS terminator in-process).
- CORS reflects loopback browser origins only.
- Treat the token like a password; do not commit it.
- See **Threat model** under Remote access above.

## Layout

| Path | Role |
|---|---|
| `cmd/strike/serve.go` | `strike serve` CLI + live engine wiring |
| `internal/server` | HTTP/SSE/WS handlers, live hub, bind helpers |
| `internal/server/static` | embedded cockpit page |
| `web/` | optional Vite dev proxy + `npm run build` |
| `pkg/protocol` | Event + Op JSON envelopes (public; `internal/protocol` re-exports) |

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

Successful `PATCH` returns the updated defaults snapshot (same shape as `GET`).
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

| `PUT` | `/v1/memory/{key}` | **yes** | Put/replace entry (`{value,tags?}`); blocked in attach-only |
| `DELETE` | `/v1/memory/{key}` | **yes** | Delete entry; blocked in attach-only |
| `GET` | `/v1/memory/export` | mode | Download portable `strike-memory.json` (TUI format) |
| `POST` | `/v1/memory/import` | **yes** | Import (`{path}` or `{data}`, `replace?`); blocked in attach-only |
| `POST` | `/v1/issues` | **yes** | Create issue (`{title,body?}`); blocked in attach-only |
| `POST` | `/v1/issues/{id}/close` | **yes** | Close issue; blocked in attach-only |
| `GET` | `/v1/issues/export` | mode | Download portable `strike-issues.json` (TUI format) |
| `POST` | `/v1/issues/import` | **yes** | Import (`{path}` or `{data}`, `replace?`); blocked in attach-only |
