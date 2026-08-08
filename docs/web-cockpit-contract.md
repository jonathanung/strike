# Web cockpit progressive workspace contract

Normative product contract for the cohesive multi-agent web cockpit
(epic [#1069](https://github.com/jonathanung/strike/issues/1069)).
Child issues WEBUI.2–WEBUI.21 (#1071–#1090) implement against this document.
Do not re-decide product here without updating this file.

**Status:** contract freeze (WEBUI.1 / #1070) + **conformance closeout**
(WEBUI.21 / #1090). Matrix rows below reflect shipped epic #1069 children;
residual gaps are explicit non-goals or deferred follow-ups with owners.

**Companion docs:**

| Doc | Relationship |
|---|---|
| [docs/web.md](web.md) | Operator start, endpoints, multi-session UX (#467), settings, security |
| Multi-session section in `docs/web.md` | Preserved; this contract **extends** it with modes/surfaces/parity |
| [docs/ARCHITECTURE.md](ARCHITECTURE.md) | Package boundaries; web stays on `pkg/protocol` + `internal/host` + `internal/server` |
| [docs/theme.md](theme.md) | TUI theme tokens; web CSS must stay aligned (#1076 closes catalog gaps) |
| [docs/protocol.md](protocol.md) | Op/Event wire schema |

---

## 1. Product decisions (normative)

1. **Progressive modes.** `Chat` is the default. `Code`, `Team`, `Project`, and
   `Ops` are capability- and activity-driven presets over a small **surface
   registry**. Modes do not invent separate data models.
2. **Responsive shell.** Desktop = rail + canvas + drawer. Tablet = canvas +
   drawer. Phone = one canvas + bottom modes/sheets with attention-first density.
   Owned by #1074; visual tokens by #1072; a11y/browser gates by #1071.
3. **Discoverability.** Command palette, slash commands, contextual help, and
   `@file` are the main power-feature entry points (#1078). Advanced operations
   do not become permanent header chrome.
4. **Extensibility.** Built-ins and bounded declarative `pane/1` contributions
   register through shared surface metadata (#1073, #1079). This is **not** an
   IDE docking framework.
5. **One data model.** Reuse `pkg/protocol`, `internal/host`, REST/SSE/WebSocket
   patterns, and the per-root reducer (`web/src/reducer.ts`). No browser-only
   event bus or duplicated orchestration truth.
6. **Root isolation.** Every live query, event, and mutation is scoped to one
   root. Existing `?root=` / `?session=` behavior remains compatible (see §5).
7. **Observe before control.** Consume existing team/delegation/message/
   artifact/ledger/verification events and read snapshots first (#1081–#1083,
   #1082, #1086). Human controls require a public Op, actor, permission, audit,
   CAS, and idempotency contract before UI (#1085 → #1088 → #1089).
8. **Trust boundaries.** Provider auth, file mutation, session/protocol changes,
   and orchestration control are **Tier C**. Attach-only stays read-only (§6).
9. **Mobile means responsive web.** No native app, service-worker session cache,
   offline execution, push notification, or remote-approval scope in this epic.
   Cross-device continuity is owned by #1060.
10. **Accessibility.** Keyboard operation, visible focus, semantic dialogs/tabs,
    320 CSS px reflow, non-color state, batched live announcements, reduced
    motion, safe areas, and virtual-keyboard behavior are required (#1071, #1072).

---

## 2. Progressive modes

Modes are **presets**: they choose a primary surface, a default secondary set,
and which surfaces may auto-disclose. They never hide Chat permanently.

| Mode | id | Primary surface | Default secondary surfaces | Disclose when |
|---|---|---|---|---|
| **Chat** | `chat` | `transcript` + `composer` | `context`, `runtime`, `queue`, `asks` | Always default on load |
| **Code** | `code` | `files` (tree/read/markdown/diff) | `changed-files`, `diagnostics`, reviewed apply | Capability `files` / `lsp`, or user opens Code, or changed-files activity |
| **Team** | `team` | `roster` / agent detail | `board`, `attention`, `timeline`, `handoffs`, `artifacts`, `decisions` | Multi-agent activity (children, roster, handoffs) or user opens Team |
| **Project** | `project` | `plans` / `goals` / `issues` / `memory` | `workflows`, project exports | Capability `plans`/`goals`/`issues`/`memory`/`workflows` or user opens Project |
| **Ops** | `ops` | `providers` / `settings` | `mcp`, `plugins`, `panes`, `diagnostics`, `context`, `timeline` | User opens Ops, or capability-gated settings/auth/mcp/plugins work |

### Mode rules

1. **Chat remains default.** Cold load with no deep link → mode `chat`, primary
   canvas = transcript + composer. Inspector/drawer starts **closed** unless a
   deep link or attention target requires it (preserves #399 declutter).
2. **Mode switch is client navigation**, not a server session. Switching modes
   must not reset transcript cache, composer draft, or root selection.
3. **Missing capability → surface hidden**, not a hard error page. Opening a
   mode whose primary surface is unavailable shows an empty-state with the
   capability name and no mutation attempt (today’s
   `CapabilityUnavailable` pattern).
4. **Activity disclosure** may badge a mode (e.g. Team when
   `permissionPending`/`questionPending` on a child, or Code when
   `changedFiles.length > 0`) without auto-switching the user away from Chat.
5. **Explicit user intent wins.** Palette / mode rail / deep link to `team`
   opens Team even if the roster is empty.
6. **Attach-only:** modes still navigate for read surfaces; composer and all
   mutations stay disabled (§6).

### Mode → surface ownership (implementation map)

| Mode | Surfaces (stable ids) | Owner issues |
|---|---|---|
| Chat | `transcript`, `composer`, `queue`, `asks`, `runtime`, `context`, `sessions-rail` | shipped |
| Code | `files`, `file-read`, `markdown`, `diff`, `changed-files`, `diagnostics`, `file-apply` | shipped |
| Team | `roster`, `agent-detail`, `board`, `team-attention`, `handoffs`, `artifacts`, `decisions`, `conflicts`, `team-controls` | #1081, #1083, #1082, #1086, #1085, #1088, #1089 |
| Project | `plans`, `goals`, `issues`, `memory`, `workflows`, `project-export` | regroup #1079; most list UIs shipped |
| Ops | `settings`, `providers`, `auth`, `mcp`, `plugins`, `panes`, `timeline`, `diag-export`, `permissions`, `sandbox` | #1077, #1076, #1079 |

---

## 3. Surface registry

WEBUI.3 (#1073) introduces a typed registry. Until then, this section is the
**normative metadata contract** every surface must satisfy when registered.

### 3.1 Metadata fields

| Field | Type | Rules |
|---|---|---|
| `id` | stable string | kebab-case; never rename without migration note |
| `mode` | `chat` \| `code` \| `team` \| `project` \| `ops` \| `any` | Primary home mode; `any` = available in multiple (e.g. `timeline`) |
| `capability` | bootstrap key or `always` | Gate visibility; missing cap → hide |
| `deepLinkKey` | query key fragment | See §5; empty if not deep-linkable alone |
| `attention` | `none` \| `badge` \| `needs-you` \| `busy` | How the shell may signal without stealing focus |
| `lazyMount` | boolean | `true` = mount on first open (default for heavy panes/plugins) |
| `attach` | `hidden` \| `read` \| `mutate-blocked` | Attach-only behavior (§6) |
| `tier` | `A` \| `B` \| `C` | Verification tier for unresolved work on this surface |
| `owner` | issue # or `shipped` or `non-goal` | Delivery owner |

### 3.2 Built-in surface registry (contract freeze)

| id | mode | capability | deepLinkKey | attention | lazyMount | attach | tier | owner |
|---|---|---|---|---|---|---|---|---|
| `sessions-rail` | any | `sessions` / `roots` | — | badge / needs-you / busy | false | read | B | shipped (#467) |
| `transcript` | chat | `always` | — | none | false | read | B | shipped |
| `composer` | chat | `live` | — | none | false | mutate-blocked | B | shipped |
| `queue` | chat | `live` | `surface=queue` | badge | true | mutate-blocked | B | shipped; polish #1078 |
| `asks` | chat | `live` | — | needs-you | false | mutate-blocked | B | shipped |
| `runtime` | chat | `live` | `surface=runtime` | none | false | mutate-blocked | B | shipped |
| `context` | chat / ops | `always` | `surface=context` | badge | true | read | B | shipped |
| `files` | code | `files` | `surface=files` | badge | true | read | B | shipped |
| `file-read` | code | `files` | `path=` | none | true | read | B | #1080 |
| `markdown` | code | `files` | `path=` | none | true | read | B | #1080 |
| `diff` | code | `files` | `path=` | none | true | read | B | shipped |
| `changed-files` | code | `files` | `surface=files` | badge | true | read | B | shipped list |
| `diagnostics` | code / ops | `lsp` | `surface=diagnostics` | badge | true | read | B | shipped |
| `file-apply` | code | `files` + live | — | none | true | mutate-blocked | **C** | #1084 |
| `roster` | team | multi-agent events / `team` | `surface=roster` | badge | true | read | B | shipped |
| `agent-detail` | team | same | `agent=` | none | true | read | B | shipped |
| `board` | team | same | `surface=board` | badge | true | read | B | #1083 |
| `team-attention` | team | same | `surface=attention` | needs-you | true | read | B | #1083 |
| `handoffs` | team | same | `surface=review` | badge | true | read | B | shipped |
| `artifacts` | team / project | future `artifacts` | `surface=artifacts` | badge | true | read | B | #1082/#1086 |
| `decisions` | team / project | future `ledger` | `surface=decisions` | badge | true | read | B | #1082/#1086 |
| `conflicts` | team | same | `surface=conflicts` | needs-you | true | read | B | #1086 |
| `team-controls` | team | live + approved ops | — | none | true | mutate-blocked | **C** | #1085/#1088/#1089 |
| `plans` | project | `plans` | `surface=plans` | none | true | read† | B | shipped; regroup #1079 |
| `goals` | project | `goals` | `surface=goals` | badge | true | read† | B | shipped; #1079 |
| `issues` | project | `issues` | `surface=issues` | none | true | read† | B | shipped |
| `memory` | project | `memory` | `surface=memory` | none | true | read† | B | shipped |
| `workflows` | project | `workflows` | `surface=workflows` | badge | true | read† | B | shipped |
| `project-export` | project | memory/issues/… | — | none | true | read | B | shipped exports |
| `settings` | ops | `settings` | `surface=settings` | none | true | mutate-blocked | B | shipped; gaps #1077 |
| `providers` | ops | `auth` / `catalog` | `surface=providers` | none | true | mutate-blocked | **C** | shipped |
| `auth` | ops | `auth` | `surface=auth` | none | true | mutate-blocked | **C** | shipped |
| `mcp` | ops | `mcp` | `surface=mcp` | badge | true | read† | B | shipped |
| `plugins` | ops | `plugins` | `surface=plugins` | none | true | read† | B | shipped |
| `panes` | ops | `panes` | `surface=panes` / `pane=` | none | true | read† | B | shipped; host #1079 |
| `timeline` | ops / team | `timeline` | `surface=timeline` | none | true | read | B | shipped |
| `diag-export` | ops | `diag` | — | none | true | mutate-blocked‡ | B | shipped |
| `permissions` | ops | `permissions` | `surface=permissions` | none | true | read | B | API shipped; UI polish #1077 |
| `sandbox` | ops | `sandbox` | `surface=sandbox` | none | true | mutate-blocked | **C** | shipped explain/default |
| `theme` | ops | `settings` / local | `surface=theme` | none | true | read (local ok) | B | appearance local; catalog #1076 |
| `command-palette` | any | `always` | — | none | true | read | B | #1078 |
| `help` | any | `always` | palette / `/help` | none | true | read | A | shipped |

† Project/Ops list **reads** allowed in attach-only when capability is on;
  **writes** fail closed (403 / UI disabled) — see §6.

‡ `GET /v1/diag` requires live engine (503 attach-only); not a silent empty.

### 3.3 Capability and activity disclosure

| Signal | Source today | Shell behavior |
|---|---|---|
| Capability missing | `bootstrap.capabilities.* === false` | Hide surface; no fetch |
| Attach-only | `bootstrap.attachOnly === true` | Read surfaces only; composer off; mutations blocked |
| Root busy | `RootSummary.busy` | BUSY badge on rail; header pulse for **selected** root |
| Needs you | `permissionPending` / `questionPending` | Needs-you badge; click switches root + shows dialog |
| Recent | `hasRecentEvent` | Soft “recent” affordance |
| Changed files | workspace `changedFiles` / `GET /v1/changed-files` | May badge Code mode |
| Children / handoffs | `child.*` events, `GET …/children` | May badge Team mode |
| Plugin panes | `capabilities.panes` + `/v1/panes` | Register dynamic surfaces with `lazyMount: true` |

**Hard rule:** disclosure badges **never** auto-navigate away from the user’s
current mode except when the user clicks the badge or a deep link targets it.

### 3.4 Lazy-mount rules

1. Transcript, composer, sessions rail, and blocking ask dialogs are **eager**.
2. Inspector/drawer bodies, plugin panes, Team board, Code tree, and heavy
   charts are **lazy** (first open or deep link).
3. Unmounting a lazy surface must not drop per-root reducer state required for
   Chat (transcript cache stays in `ClientState.byID`).
4. Long-session performance budgets are owned by #1087; registry must allow
   virtualization hooks without changing surface ids.

---

## 4. Responsive layout and attention

Owned by #1074 (shell) + #1072 (visual/a11y foundation) + #1071 (conformance).

| Breakpoint | Layout | Chrome |
|---|---|---|
| **Desktop** (≥ ~1024 CSS px) | Left **rail** (sessions) + center **canvas** + optional right **drawer** | Mode switch in chrome or rail; runtime disclosure progressive (#913 pattern) |
| **Tablet** (~600–1023) | Canvas + drawer (rail as overlay/sheet) | Mode switch compact; one secondary panel at a time |
| **Phone** (≤ ~599, down to **320**) | Single canvas; modes as **bottom bar**; secondary as **sheets** | Attention-first density; essential send/ask/switch only |

### Attention behavior (extends multi-session contract)

1. Background roots keep badges without forcing switch (shipped #919).
2. Mode-level attention (Team/Code) is **additive** to root attention — both may
   show; root needs-you outranks mode badge for click-through.
3. Live regions batch announcements; no token-by-token floods (#1072/#1075).
4. Reduced motion and safe-area insets are required on phone sheets.
5. Virtual keyboard must not permanently cover the composer send control.

### Non-goals for layout

- Native apps, PWA offline cache, push notifications → #1060 / out of epic.
- Arbitrary IDE multi-pane docking → non-goal.
- Pixel-clone of TUI split geometry → non-goal; web uses CSS layout.

---

## 5. Deep-link grammar (additive)

On cockpit load, after token cookie handoff (server strips `?token=` and keeps
other query params — see `docs/web.md`):

### 5.1 Existing parameters (unchanged)

| Query | Behavior |
|---|---|
| `?root=<id>` | Live workspace → select, activate, open WS `?root=`. |
| `?session=<id>` | Live root first, else durable HISTORY (SSE). |
| both `root` and `session` | `root` wins. |
| invalid id | Safe fallback: first live root, else first HISTORY; no error page. |

Shareable examples: `/attach?session=<durableId>`, `/attach?root=<liveId>`.

### 5.2 Additive parameters (WEBUI.3 / #1073)

All new keys are **optional**. Unknown keys are ignored (forward compatible).
Missing capability for a targeted surface → open mode with empty-state, do not
break root/session resolution.

| Query | Meaning | Default if absent |
|---|---|---|
| `mode` | `chat` \| `code` \| `team` \| `project` \| `ops` | `chat` |
| `surface` | registry `id` (e.g. `files`, `plans`, `roster`) | mode primary |
| `entity` | opaque entity id for the surface (plan id, goal id, agent id, …) | none |
| `path` | workspace-relative file path for Code read/diff/markdown | none |
| `pane` | plugin pane id (`pane/1`) | none |
| `agent` | child/agent id for Team detail | none |

**Resolution order after root/session:**

1. Resolve workspace selection (`root` / `session`) exactly as today.
2. Apply `mode` if valid; else `chat`.
3. If `surface` is registered and allowed by capability/attach → open it
   (drawer/sheet as needed).
4. Else if `pane` set and `capabilities.panes` → open panes host focused on id.
5. Else if `path` set → imply `mode=code` and open file-read when Code exists.
6. Else if `agent` set → imply `mode=team` and open agent-detail when Team exists.
7. `entity` is passed to the surface; invalid entity → surface empty-state, keep mode.

**Compatibility guarantees:**

- Old links with only `?root=` / `?session=` behave identically.
- New keys never override attach-only safety (composer stays off).
- `mode`/`surface` must not send ops or mutate server state by themselves.

Examples:

```
/attach?root=<liveId>&mode=code&surface=files&path=internal/server/api.go
/attach?session=<id>&mode=project&surface=plans&entity=<planId>
/attach?root=<liveId>&mode=team&surface=roster&agent=<childId>
/attach?root=<liveId>&mode=ops&surface=settings
```

---

## 6. Attach-only read / mutation matrix

`bootstrap.attachOnly === true` when no live engine (`!hasLive()`). Fail **closed**:
UI disables controls; server returns **403** (or **503** where already specified)
rather than silently no-oping mutations.

### 6.1 Always allowed (read)

| Action | API / transport |
|---|---|
| Bootstrap | `GET /v1/bootstrap` (`attachOnly: true`, `protocolOps: null`) |
| List durable sessions | `GET /v1/sessions` |
| Historical transcript | SSE `GET /v1/sessions/{id}/events` |
| Children list | `GET /v1/sessions/{id}/children` |
| Memory list/get/export | `GET /v1/memory`, `GET /v1/memory/export` |
| Issues list/export | `GET /v1/issues`, `GET /v1/issues/export` |
| Plans list/get (if cap) | `GET /v1/plans*` |
| Goals list/get/log (if cap) | `GET /v1/goals*` |
| MCP list (if cap) | `GET /v1/mcp` |
| Plugins list/get (if cap) | `GET /v1/plugins*` |
| Panes list/get/snapshot (if cap) | `GET /v1/panes*` |
| Timeline read (if cap) | `GET /v1/sessions/{id}/timeline*` |
| Permissions explain/presets (if cap) | `GET /v1/permissions/*` |
| Settings GET (if cap) | `GET /v1/settings` |
| Client markdown export | client-side from loaded transcript |
| Theme appearance local | `localStorage` / `data-appearance` only |

### 6.2 Always blocked (mutate)

| Action | API | Expected |
|---|---|---|
| Any protocol op | `POST /v1/ops`, WS ops | 403 read-only attach |
| Create/activate/resume/close roots | `/v1/roots*` | unavailable / 503 |
| Live WS control plane | `GET /v1/ws` ops in | no live |
| Memory put/delete/import | `PUT/DELETE/POST /v1/memory*` | 403 |
| Issues create/close/import | `POST /v1/issues*` | 403 |
| Plans create/update/status | `POST/PATCH /v1/plans*` | 403 |
| Goals run/pause/resume/abort/set | `POST /v1/goals*` | 503/403 live-required |
| Workflow start/stop/scaffold | `POST /v1/workflows*` | blocked |
| Plugin install/trust/update/… | `POST /v1/plugins/*` | blocked (reads ok) |
| Pane mount/input/resize | `POST /v1/panes/*` | blocked where mutating |
| Sandbox PATCH | `PATCH /v1/sandbox` | blocked |
| Settings PATCH | `PATCH /v1/settings` | blocked |
| Auth key / logout | `POST /v1/auth/key`, `DELETE /v1/auth/*` | blocked |
| Diag bundle | `GET/POST /v1/diag` | 503 unsupported |
| Session fork/rename/delete | session mutators | blocked or 403 |
| Composer send / queue drain | client | disabled |
| Permission/question reply | ops | impossible (no live asks) |
| File apply / write | future #1084 | blocked |
| Team controls | future #1089 | blocked |

### 6.3 UI contract

1. Composer placeholder explains inspect-only; send control disabled.
2. Mutation buttons hidden or disabled with “Attach-only is read-only” copy
   (memory/issues panels already do this).
3. Mode navigation still works for read surfaces.
4. Deep links never enable mutations in attach-only.
5. New endpoints must document attach behavior in this matrix before merge.

---

## 7. Sibling epic boundaries (normative)

| Issue | Relationship to #1069 / this contract |
|---|---|
| **#399** (closed) | Prior declutter; progressive IA **replaces** one-off declutter. Keep transcript-first default. |
| **#467** (closed) | Multi-session contract in `docs/web.md` — **consume and preserve**. |
| **#516** (closed) | v0.2 web parity baseline — **do not reopen** shipped children; extend via WEBUI.* only. |
| **#523** (closed) | TUI agent visualizer complete. Web **extends** the former “web visualizer non-goal” to **observe-first list/board/review UX only**. A **graph clone remains out of scope**. |
| **#541** (open) | Serve transport/auth/rate hardening — coordinate remote-capable mutations; do not duplicate hardening here. |
| **#937** (closed) | Basic child detail shipped; Team mode **builds on** it (#1081/#1083/#1086). |
| **#1032** (closed) | Security audit coverage — new auth/file/team ops integrate with audit families. |
| **#1056** (open) | Human sharing, comments, roles, collaboration — **out of** progressive shell scope; no share UI here. |
| **#1058** (open) | Adaptive orchestration defaults — web **displays** decisions; does **not** choose policy. |
| **#1060** (open) | Cross-device sessions, notifications, remote approvals, controller arbitration — **out of** this epic. |

### Browser non-goals (explicit)

| Non-goal | Rationale |
|---|---|
| Full TUI graph visualizer clone | #523 TUI-first; web uses list/board/review (#1083/#1086) |
| Embedded vim/nano/PTY terminal | Terminal-only; Code mode is read/diff/reviewed-apply |
| Pets pane | TUI delight; no browser analogue required |
| Local CPU/RAM telemetry pane | Host metrics; optional later, not parity-blocking |
| Exact terminal keymaps | Browser shortcuts subset; document in help (#1078) |
| Blocking FTUE wizard clone | Contextual onboarding in #1078 instead of modal gauntlet |
| `/loop` as terminal cron UX | Map durable intent to Goals/Workflows in #1079 |
| IDE docking / freeform multi-split | Bounded surface registry only |
| Native mobile apps / SW offline exec | Responsive web only; continuity → #1060 |
| Browser-only event bus | One protocol + reducer |
| Human session sharing UI | #1056 |
| Remote approval push | #1060 |
| Choosing orchestration policy | #1058 |

---

## 8. TUI → web parity inventory

**Status vocabulary:**

| Status | Meaning |
|---|---|
| `shipped` | Present in `web/` + server; preserve + regression-test |
| `partial` | Some path exists; owner issue closes the gap |
| `missing` | No acceptable web path yet; owner issue required |
| `non-goal` | Browser non-goal with rationale |

**Tier:** verification tier for remaining work (`A` docs, `B` normal web/server,
`C` auth/file/session/orchestration). Shipped rows list the tier used when last
touched; unresolved rows **must** keep the listed tier.

**Attach column:** `read` / `mutate-blocked` / `hidden` / `n/a` (non-goal).

### 8.1 Chat, transcript, composer, sessions

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| Live transcript stream | `internal/tui/_src/app`, cells | `web/src/Transcript.tsx`, WS `/v1/ws`, reducer | shipped | preserve | B | read (SSE history) |
| Historical JSONL attach | session nav | SSE `/v1/sessions/{id}/events` | shipped | preserve | B | read |
| Composer + send | input package | `App.tsx` composer, `user.input` op | shipped | preserve | B | mutate-blocked |
| Image attachments | input | composer images → op | shipped | preserve | B | mutate-blocked |
| Prompt queue | `queue_modal.go`, queue window | `queueOps.ts`, queue UI | shipped | polish #1078 | B | mutate-blocked |
| Permission asks | `question`/`permission` modals | `PermissionDialog`, `permission.reply` | shipped | preserve | B | n/a (no live) |
| Question asks | `question_modal.go` | question dialog + reply op | shipped | preserve | B | n/a |
| Multi-root workspaces | `root_switch.go` | `/v1/roots*`, rail ACTIVE/HISTORY | shipped | #467 preserve | B | mutate-blocked lifecycle |
| Session fork/rename/delete | session modal | `/v1/sessions*` | shipped | preserve | B | mutate-blocked |
| Resume / close workspace | root switch | resume/DELETE roots | shipped | preserve | B | mutate-blocked |
| Rewind / undo preview | `undo_modal`, rewind | `undoPreview.ts`, rewind op | shipped | polish #1075 | B | mutate-blocked |
| Compact | `/compact` | slash + `compact` op | shipped | preserve | B | mutate-blocked |
| Interrupt | keybind / op | `/interrupt`, op | shipped | preserve | B | mutate-blocked |
| Thinking display | `/think` | THINK toggle + transcript | shipped | polish #1075 | B | read |
| Tool cards / expand | cells + keys | `Transcript.tsx` tool cards + exploration groups | shipped | #1075 | B | read |
| Diff in transcript | `edit_meta`, apply diff modal | `DiffViewer` in tool cards | shipped | #1075 | B | read |
| Cost / token chrome | `cost_modal.go`, usage | context inspector + `/cost` + usage.reported | shipped | #1075 | B | read |
| Markdown export | `/export`, cell export | `exportMarkdown.ts`, header ↓ | shipped | preserve | B | read (client) |
| Copy last assistant | `/copy` | slash `/copy` | shipped | preserve | B | read |
| `@file` attach in prompt | TUI input | composer `@` + `/v1/files/search` | shipped | #1078 | B | mutate-blocked |
| Composer history (up/down) | input history | ↑/↓ browse + `/v1/history` | shipped | #1078 | B | read list / mutate-blocked send |
| Command palette | `palette.go` | `CommandPalette.tsx` (⌘/Ctrl+K) | shipped | #1078 | B | read |
| Slash catalog parity | `commands.go` | `slash.ts` + palette catalog | shipped | #1078 | B | per-command |
| Contextual help / legend | `help_modal`, `legend_modal` | `/help`, palette, onboarding tip | shipped | #1078/#1090 | A/B | read |
| FTUE wizard | `ftue_modal.go` | non-goal blocking wizard | non-goal | contextual #1078 | B | n/a |
| `/init` AGENTS.md | `init_modal.go` | not exposed on serve (`projectInit` false) | deferred | follow-up: TUI `/init` remains; web non-blocking | B | mutate-blocked |

### 8.2 Runtime, auth, settings, theme

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| Provider select | `/provider`, provider modal | runtime Field + auth settings | shipped | #1077 | C | mutate-blocked |
| Model select | `/model`, model modal | runtime + `/v1/models` + rates | shipped | #1077 | B | mutate-blocked |
| Agent select | `/agent`, agent modal | runtime Field | shipped | polish #1077 | B | mutate-blocked |
| Effort | `/effort` | runtime + op | shipped | preserve | B | mutate-blocked |
| Autonomy | `/autonomy` | runtime + op | shipped | preserve | B | mutate-blocked |
| Permission mode | `/mode` | runtime + op | shipped | preserve | B | mutate-blocked |
| Fast tier | `/fast` | FAST toggle + op | shipped | preserve | B | mutate-blocked |
| Sandbox dial + explain | `/sandbox` | `/v1/sandbox` GET/PATCH | shipped | preserve | C | read explain; PATCH blocked |
| Permission presets/explain | `/permission` | explain dialog + `/v1/permissions/*` | shipped | #1077 | B | read |
| Scheduler presets | `scheduler_presets_modal.go` | Settings + `/v1/scheduler/presets` | shipped | #1077 | B | mutate-blocked |
| Provider auth login | `auth.go` modal | key/OAuth/device flows in Settings | shipped | #1077 | **C** | mutate-blocked |
| Provider logout | auth modal | Settings provider rows + DELETE | shipped | #1077 | **C** | mutate-blocked |
| Custom providers | `custom_provider_modal.go` | Settings custom provider CRUD | shipped | #1077 | C | mutate-blocked |
| Settings dials | `settings_modal.go` | `Settings.tsx`, `/v1/settings` | shipped | gaps #1077 | B | GET read; PATCH blocked |
| Config file picker/edit | `/config`, config modal | missing (no embedded editor) | non-goal / host files | #1077 docs | B | n/a |
| Theme catalog + preview | `theme_modal.go` | Settings catalog preview/apply | shipped | #1076 | B | local appearance ok |
| Theme provenance | theme package | catalog provenance labels | shipped | #1076 | B | read |
| Keybind editor | `keybind_editor.go` | non-goal (browser shortcuts subset) | non-goal | help docs | A | n/a |

### 8.3 Code and files

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| Changed files list | `files_window.go` | inspector files + `/v1/changed-files` | shipped | preserve | B | read |
| File tree browse | files window | `CodeExplorer` tree + search | shipped | #1080 | B | read |
| File read | `/v1/file` server exists | Code explorer read pane | shipped | #1080 | B | read |
| Markdown preview | `markdown_window.go`, `/md-read` | Code explorer markdown toggle | shipped | #1080 | B | read |
| Diff review | apply diff modal | Code explorer + changed-files diffs | shipped | #1075/#1080 | B | read |
| Reviewed file apply | apply diff modal | confined apply API + Code UI | shipped | #1084 | **C** | mutate-blocked |
| LSP status | `/lsp` | Diagnostics panel + `/v1/lsp` | shipped | #1080/#1079 | B | read |
| Diagnostics pane | `diagnostics_window.go` | `Diagnostics.tsx`, `/v1/diagnostics` | shipped | preserve | B | read |
| Embedded vim/nano | `/vim`, `/nano`, terminal window | — | non-goal | browser non-goal | — | n/a |
| PTY shell | `terminal_window.go` | — | non-goal | browser non-goal | — | n/a |

### 8.4 Project data (plans, goals, memory, issues, workflows)

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| Plans CRUD/status | `/plan`, plans window | `Plans.tsx`, `/v1/plans*` | shipped | regroup #1079 | B | read; writes blocked |
| Goals / loop harness | `/goal`, goal commands | `Goals.tsx`, `/v1/goals*` | shipped | #1079 | B | list/get/log read; run* blocked |
| `/loop` recurring jobs | `loop.go` | — | non-goal as TUI cron | map via goals/workflows #1079 | B | n/a |
| Memory list/write/export | `/memory`, memory window | memory panel + API | shipped | preserve | B | read/export; writes 403 |
| Issues list/write/export | `/issues`, issues window | issues panel + API | shipped | preserve | B | read/export; writes 403 |
| Workflows catalog/start | `/workflow`, builder modal | `Workflows.tsx`, `/v1/workflows*` | shipped | #1079 | B | read; start blocked attach |
| Workflow drafts | workflow builder | draft APIs when cap | shipped | #1079 | B | per API |
| Context doctor | `/context`, context window | context inspector tab | shipped | preserve | B | read |
| Timeline | `/timeline`, timeline modal | `Timeline.tsx` | shipped | preserve | B | read |
| Diag bundle | `/diag` | `GET /v1/diag` download | shipped | preserve | B | 503 |

### 8.5 Team / multi-agent

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| Child agent list | agents window, `team_ui.go` | `ChildAgents.tsx` + Team roster | shipped | #1081/#1083 | B | read |
| Child transcript open | session nav | open child historical SSE | shipped | #1083 | B | read |
| Handoff quality chips | team_ui / visualizer | Team review + child quality | shipped | #1086 | B | read |
| Roster / task board | visualizer + team events | `Team.tsx` roster + board | shipped | #1083 | B | read |
| Team attention rollup | visualizer | Team attention + mode badge | shipped | #1083 | B | read |
| Path ownership / conflicts | visualizer, patch_collab | Team review path overlaps | shipped | #1086 | B | read |
| Artifacts read API | artifact tools / host | `GET /v1/artifacts*` | shipped | #1082 | B | read |
| Decision ledger read API | ledger tools / host | `GET /v1/ledger*` | shipped | #1082 | B | read |
| Artifact/decision review UI | TUI panes | `ArtifactsReview.tsx` | shipped | #1086 | B | read |
| Human orchestration ops | engine/team tools | `docs/human-orchestration-ops.md` + protocol Ops | shipped | #1085 | **C** | mutate-blocked |
| Implement approved ops | protocol/engine | team.* Ops in engine/server | shipped | #1088 | **C** | mutate-blocked |
| Safe Team control UI | TUI controls | Team controls tab + CAS/idempotency | shipped | #1089 | **C** | mutate-blocked |
| Graph visualizer clone | `visualizer_window.go` | — | non-goal | list/board/review only | — | n/a |
| Pets | `pets_window.go` | — | non-goal | — | — | n/a |

### 8.6 Ops: MCP, plugins, panes, telemetry

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| MCP status/retry/disable | `/mcp` | `MCP.tsx`, `/v1/mcp*` | shipped | #1079 | B | read; mutate blocked |
| Plugin manager | `/plugin`, plugin modal | `Plugins.tsx`, `/v1/plugins*` | shipped | #1079 | B | read; trust/install blocked |
| Plugin panes `pane/1` | plugin pane windows | `Panes.tsx`, `/v1/panes*` | shipped | host polish #1079 | B | read; input blocked |
| Telemetry CPU/RAM | `/telemetry`, telemetry window | capability flag only | non-goal | browser non-goal | — | n/a |
| Doctor modal | `doctor_modal.go` | context doctor + diag export | shipped | #1079 | B | read |
| Upgrade in-app | `/upgrade` | — | non-goal | CLI/release | — | n/a |
| Quit/exit | `/exit` | — | non-goal | close browser tab | — | n/a |

### 8.7 Performance, a11y, conformance

| Feature | TUI entry/file | Web file/API today | Status | Owner | Tier | Attach-only |
|---|---|---|---|---|---|---|
| Long transcript virtualization | paint budgets | VirtualList + stream batch + bounds | shipped | #1087 | B | read |
| Large team roster perf | visualizer bounds | memo transcript + roster scroll region | shipped | #1087 | B | read |
| Visual + a11y foundation | theme + tui/ui | `web/src/ui/*` + tokens | shipped | #1072 | B | n/a |
| Surface registry + mode shell | window registry analogue | `surfaces.ts` + mode shell + deep links | shipped | #1073 | B | n/a |
| Responsive shell | layout package | desktop/tablet/phone shell | shipped | #1074 | B | n/a |
| Real-browser a11y/responsive tests | — | Playwright `web/e2e` + `make web-e2e` | shipped | #1071 | B | n/a |
| Final parity conformance | — | this document + `docs/web.md` § Conformance | shipped | #1090 | A/B | matrix closeout |

### 8.8 Coverage checklist (acceptance) — closed by #1090

Every major family resolves to `shipped`, `non-goal` with rationale, or an
explicit deferred follow-up (no unknown/partial without owner):

| Family | Resolution |
|---|---|
| Themes | **shipped** (#1076 catalog/preview/provenance) |
| Config sources | settings API **shipped**; embedded file editor **non-goal** |
| FTUE/init | blocking FTUE **non-goal**; contextual tip **shipped** (#1078); `/init` **deferred** (TUI remains; `projectInit` not on serve) |
| Loop | `/loop` **non-goal**; goals/workflows **shipped** (#1079) |
| Scheduler presets | **shipped** (#1077 Settings + API) |
| Permission presets | **shipped** (explain UI + API) |
| Cost | **shipped** (#1075) |
| Telemetry (host metrics) | **non-goal** |
| Provider auth/logout | **shipped** (#1077, Tier C) |
| Files | explorer + reviewed apply **shipped** (#1080/#1084) |
| Agents / team | observation + board + review + controls **shipped** (#1081–#1089) |
| Artifacts / ledger | read APIs + review UI **shipped** (#1082/#1086) |
| Help | palette + slash + tip **shipped** (#1078) |
| Exports | markdown/memory/issues/diag/timeline **shipped** |
| Plugins / panes | **shipped** (#1079) |
| Long session / large team perf | **shipped** (#1087) |
| Terminal-only (vim/nano/PTY/pets/exact keymaps) | **non-goal** |
| Native app / offline / push / remote approval | **non-goal** (→ #1060) |
| Human collaboration comments/roles | **non-goal** (→ #1056) |
| Adaptive orchestration policy choice | **non-goal** (→ #1058; web displays decisions only) |
| Serve transport/auth hardening beyond shipped | coordinate **#541** / **#1032** — not absorbed |

### 8.9 Conformance evidence (WEBUI.21)

| Gate | Evidence |
|---|---|
| Child delivery | All #1070–#1089 closed; PRs #1101–#1123 (see GitHub epic #1069) |
| Unit / component | `cd web && npm test` (includes perf fixture CI subset) |
| Typecheck + embed | `make web-check` → `tsc` + Vite build into `internal/server/static` |
| Go | `make test && make vet && make build` |
| Real browser | `make web-e2e` — desktop/tablet/320px, keyboard, multi-root, deep links, attach-only |
| Trust boundaries | attach-only e2e + server tests for auth redaction, path confinement, CAS/idempotency on team/file ops |
| Performance | #1087 thresholds in `web/src/perf/thresholds.ts`; `npm run profile:perf` |
| Operator docs | `docs/web.md` (modes, registry, deep links, endpoints, smokes) |

**Deferred (explicit, non-blocking for epic close):** web `/init` / `projectInit`
capability remains off on `strike serve`. Operators use TUI `/init` or host
files. Track only if product prioritizes browser project bootstrap.

---

## 9. Delivery ownership (epic children)

| Issue | WEBUI | Role |
|---|---|---|
| #1070 | .1 | **This contract** (Tier A) |
| #1072 | .2 | Visual + accessibility foundation |
| #1073 | .3 | Surface registry, mode shell, deep links |
| #1074 | .4 | Responsive desktop/tablet/phone shell |
| #1071 | .5 | Real-browser responsive + a11y tests |
| #1078 | .6 | Command discovery, composer history, `@file` |
| #1075 | .7 | Transcript, tool, diff, cost interactions |
| #1080 | .8 | Scoped Code explorer |
| #1084 | .9 | Reviewed confined browser file apply (Tier C) |
| #1077 | .10 | Provider auth, model, policy, settings gaps (Tier C where auth) |
| #1076 | .11 | Theme catalog, preview, appearance parity |
| #1079 | .12 | Regroup Project, Ops, plugin, pane surfaces |
| #1081 | .13 | Root-scoped multi-agent observation → web state |
| #1083 | .14 | Observe-first Team workspace |
| #1082 | .15 | Read-only artifacts + decision ledger APIs |
| #1086 | .16 | Artifacts, decisions, handoff, conflict review |
| #1085 | .17 | Public human orchestration operations (spec) |
| #1088 | .18 | Implement approved orchestration ops (Tier C) |
| #1089 | .19 | Safe Team task/agent controls (Tier C) |
| #1087 | .20 | Long transcript + large team performance |
| #1090 | .21 | Final parity conformance + operator docs |

Critical path: **#1070 → #1072/#1073 → #1074 → #1078 → #1081 → #1083 → #1085 → #1088 → #1089 → #1090**.

---

## 10. Verification

### 10.1 Tier rules (from AGENTS.md)

| Tier | When | Local gate |
|---|---|---|
| **A** | Docs/skills/markdown only | gofmt if any `.go`; no full test suite |
| **B** | Normal React/server presentation | `make web-check` if `web/` → `make test && make vet && make build` |
| **C** | Auth, file mutation, session/protocol, orchestration concurrency | Tier B + `go test -race ./... -count=1` + focused package tests |

This contract issue is **Tier A**. Unresolved matrix rows carry the tier the
implementing child must meet. #1090 is the final conformance gate for the epic.

### 10.2 Cross-check sources (authors of matrix updates)

When editing the parity table, re-verify against:

- `internal/tui/_src/app/commands.go` — slash/command families
- `internal/tui/_src/modal/`, `internal/tui/_src/window/` — surfaces
- `internal/host/` — host services
- `pkg/protocol/` — Op/Event wire
- `internal/server/` — HTTP/SSE/WS + capabilities
- `web/src/` — cockpit implementation
- `docs/web.md` — operator + multi-session contract

### 10.3 Manual smoke scenarios (contract-level)

Run after shell/registry land (#1073/#1074); contract-only changes need no
runtime proof. Operators should still be able to execute:

1. **Mode switch** — From default Chat, open Code / Team / Project / Ops via
   mode control; transcript and root selection preserved; return to Chat.
2. **Deep link** — Load
   `/attach?root=<liveId>&mode=code&surface=files&path=<file>` (and a
   `mode=team` / `mode=project` variant); root resolves; mode+surface apply;
   unknown `surface` does not break root.
3. **Unsupported capability** — Host without `plans` (or force cap off): Project
   plans surface hidden or empty-state; no failed mutation toast loops.
4. **Attach-only fallback** —
   `./strike serve --attach-only --session-dir …` → composer disabled; memory
   write controls disabled; `POST /v1/ops` 403; SSE history works; mode nav
   still opens read surfaces.

Additional multi-session smokes remain in `docs/web.md` (Manual smoke
multi-session). Final epic smokes (keyboard, 320px, permission, multi-root) are
owned by #1071 and closed under #1090.

---

## 11. Document maintenance

1. Child issues cite this file as the product source of truth.
2. Status transitions (`missing` → `partial` → `shipped`) update the matrix in
   the **same PR** as the implementation (or in #1090 closeout).
3. New surfaces require a registry row (§3.2) before UI merge.
4. New mutations require an attach-only row (§6) and a tier label.
5. Conflicts with `docs/web.md` multi-session rules: multi-session section wins
   for root/session transport; this file wins for modes/surfaces/parity.
