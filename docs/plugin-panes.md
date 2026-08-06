# Plugin pane contribution ABI

Normative contract for **user pane contributions** (epic
[#451](https://github.com/jonathanung/strike/issues/451), PLUGIN.8
[#522](https://github.com/jonathanung/strike/issues/522)).

Builds on the versioned plugin bundle contract
([plugins.md](plugins.md)). This document freezes pane identity, definition
shape, execution model, render/input primitives, permissions, lifecycle,
sizing, failure isolation, versioning, and web mapping **before** TUI host
([#731](https://github.com/jonathanung/strike/issues/731)) or web parity
([#732](https://github.com/jonathanung/strike/issues/732)) land.

| Status | Meaning |
|---|---|
| **Contract (this doc)** | Normative. Pane hosts and loaders must conform. |
| **Not implemented yet** | TUI window adapter, process supervisor, web renderer — later issues. |
| **Out of scope forever (v1 model)** | Exposing the private Go `window` interface; in-process Go/`plugin.Open` panes; unrestricted terminal escape / raw PTY output from plugins; Node plugin hosts. |

Related: [plugins.md](plugins.md), [theme.md](theme.md), [harnesses.md](harnesses.md),
[protocol.md](protocol.md), [web.md](web.md), [ARCHITECTURE.md](ARCHITECTURE.md),
[secrets.md](secrets.md).

---

## 1. Goals and non-goals

### 1.1 Goals

1. Let plugin bundles contribute **right-pane surfaces** users can focus, cycle,
   and layout alongside built-in windows.
2. Keep the ABI **frontend-neutral**: the same definition and process protocol
   map to TUI and a future web cockpit without importing TUI types.
3. Bound what a pane may draw and request so a malicious or buggy pane cannot
   take down or starve the host.
4. Align trust with [plugins.md](plugins.md#5-trust-model): no silent executable
   startup; path confinement; no secrets in bundles.

### 1.2 Non-goals

- Implementing the TUI host or web renderer (this change is contract-only).
- Unrestricted ANSI/VT escape streams or embedding arbitrary PTYs from plugins.
- Replacing built-in windows (`context`, `agents`, `files`, …) or the private
  `window` Go interface.
- A second plugin format for web.

---

## 2. Execution model (chosen)

Strike panes use a **declarative render tree** delivered either:

| Mode | Process? | Trust class | When to use |
|---|---|---|---|
| **`static`** | No | **Passive** (like themes) | Fixed or host-subscription-driven UI described entirely in JSON. |
| **`process`** | Yes — one supervised subprocess | **Executable** (like MCP/harness) | Dynamic logic, polling external tools, custom state machines. |

Both modes speak the same **view model** (§6). The host owns layout chrome,
focus, keybind routing, theme token resolution, and painting. The contribution
never receives a Go `window` value, Bubble Tea model, lipgloss style, or DOM
node.

**Rationale:** harnesses already prove external-process JSONL works for
language-neutral extensions; themes prove passive JSON contributions. Panes
combine both: structured UI (not raw terminal bytes) plus optional process
logic under the same trust gates as other executables.

```text
┌─────────────────────────────────────────────────────────────┐
│ Host (TUI #731 or web #732)                                 │
│  layout · focus · theme tokens · rate limits · sandboxing   │
└───────────────┬────────────────────────────▲────────────────┘
                │ pane.* host→contrib msgs   │ pane.* contrib→host
                ▼                            │
     ┌────────────────────┐    ┌─────────────────────────────┐
     │ static definition  │ or │ process (JSONL stdio v1)    │
     │ + host data feeds  │    │ command under plugin root   │
     └────────────────────┘    └─────────────────────────────┘
                │                            │
                └──────── view tree ─────────┘
                         (bounded primitives)
```

---

## 3. Identity and naming

| Term | Rule |
|---|---|
| **Pane ID** | Stable string `contributions.panes[].id`. Pattern: same as plugin-scoped slugs — `^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)*$`, max 64 chars. Recommended: `<plugin-id-suffix>.<pane>` (e.g. `acme.status`). |
| **Uniqueness** | Pane IDs must be unique across **enabled** plugins. Collision → **fail closed** for the colliding contribution; diagnostic names both plugins (same posture as MCP/harness name collision in [plugins.md](plugins.md#44-collisions)). |
| **Title** | Human label for chrome / window cycle UI (1–40 chars). From definition `title`, overridable at runtime by `pane.meta` (process) within the same length cap. |
| **Provenance** | Diagnostics and plugin manager show `plugin=<id>@<version> pane=<pane-id> mode=<static\|process>`. |
| **Slash / jump** | Hosts MAY expose `/pane <id>` or catalog entries; IDs are the stable handle. Built-in window IDs remain reserved and must not be claimed by plugins (`context`, `activity`, `agents`, `visualizer`, `files`, `diagnostics`, `memory`, `issues`, `plans`, `markdown`, `editor`, `pets`, `system`, and any future built-in listed in docs). |

Built-in windows are **not** plugin panes and do not use this ABI.

---

## 4. Manifest contribution entry

Extends [plugins.md](plugins.md) §7.9. `schemaVersion` of the **plugin**
manifest stays `1`; pane **definition** files carry their own
`schemaVersion` (this ABI starts at **1**).

### 4.1 Entry fields

| Field | Required | Type | Rules |
|---|---|---|---|
| `id` | yes | string | Pane ID (§3). |
| `path` | yes | string | Relative path to the pane definition JSON/JSONC under plugin root. Path confinement per [plugins.md](plugins.md#9-path-confinement). |
| `abi` | yes | string | Must be `"pane/1"` for this contract. Legacy placeholder `"reserved"` is **rejected** once pane loaders exist; until then loaders ignore pane activation. |

Unknown entry fields: rejected on strict decode (plugin manifest posture).

### 4.2 Capability tags

Plugins that contribute panes SHOULD declare capability tags:

| Tag | Meaning |
|---|---|
| `panes` | Any pane contribution (static or process). |
| `panes.process` | At least one `mode: process` pane (executable trust). |
| `panes.host.<name>` | Optional finer tags for host data feeds requested (e.g. `panes.host.session.summary`). |

### 4.3 Example manifest fragment

```jsonc
"contributions": {
  "panes": [
    {
      "id": "acme.status",
      "path": "panes/status.json",
      "abi": "pane/1"
    }
  ]
}
```

---

## 5. Pane definition file (`schemaVersion: 1`)

File at `contributions.panes[].path`. JSON or JSONC. Strict unknown-field
reject.

### 5.1 Top-level fields

| Field | Required | Type | Rules |
|---|---|---|---|
| `schemaVersion` | yes | integer | `1` for this ABI. |
| `id` | yes | string | Must equal manifest `contributions.panes[].id`. |
| `title` | yes | string | Display title (1–40 chars). |
| `mode` | yes | string | `"static"` or `"process"`. |
| `permissions` | yes | object | §8. Required even for static (declares host feeds). |
| `sizing` | no | object | §9 defaults apply when omitted. |
| `subscriptions` | no | string[] | Host data feeds to push (§7). Static panes typically list these; process panes may also subscribe. |
| `view` | static: yes | object | Initial / only view tree (§6). Ignored as sole UI source in process mode (process sends `pane.render`). |
| `command` | process: yes | string | Relative executable under plugin root (or reviewed absolute — same rules as MCP/harness). |
| `args` | no | string[] | Process argv after command. |
| `env` | no | object | Env map; values secret refs or non-secret literals ([plugins.md](plugins.md#10-secret-handling)). |
| `timeouts` | no | object | §10 overrides. |
| `group` | no | string | Optional stack-group hint (§9.3). |

### 5.2 Static definition example

```jsonc
// panes/status.json — static pane driven by host subscriptions
{
  "schemaVersion": 1,
  "id": "acme.status",
  "title": "Acme Status",
  "mode": "static",
  "permissions": {
    "host": ["session.summary", "usage"],
    "fs": "none",
    "network": "none",
    "command": "none"
  },
  "subscriptions": ["session.summary", "usage"],
  "sizing": {
    "minWidth": 24,
    "minHeight": 6,
    "preferredHeight": 10
  },
  "view": {
    "type": "column",
    "gap": 1,
    "children": [
      {
        "type": "text",
        "text": "Session",
        "style": "title"
      },
      {
        "type": "kv",
        "entries": [
          { "key": "cwd", "valueFrom": "session.summary.cwd" },
          { "key": "model", "valueFrom": "session.summary.model" },
          { "key": "agent", "valueFrom": "session.summary.agent" }
        ]
      },
      {
        "type": "text",
        "text": "Usage",
        "style": "title"
      },
      {
        "type": "meter",
        "label": "context",
        "valueFrom": "usage.used",
        "maxFrom": "usage.limit",
        "unknownMax": "usage.limitKnown"
      },
      {
        "type": "text",
        "text": "Press r to refresh bindings",
        "style": "muted"
      }
    ]
  }
}
```

Static `valueFrom` paths resolve against the latest snapshot for each
subscription (§7). Missing paths render as empty / `—`; they must not crash
the host.

### 5.3 Process definition example

```jsonc
// panes/board.json — external process pane
{
  "schemaVersion": 1,
  "id": "acme.board",
  "title": "Review Board",
  "mode": "process",
  "command": "bin/review-board-pane",
  "args": ["--jsonl"],
  "permissions": {
    "host": ["session.summary", "agents.roster"],
    "fs": "read-workspace",
    "network": "none",
    "command": "none"
  },
  "subscriptions": ["session.summary", "agents.roster"],
  "sizing": {
    "minWidth": 28,
    "minHeight": 8,
    "preferredHeight": 16,
    "group": "plugin"
  },
  "timeouts": {
    "startMs": 5000,
    "renderMs": 100,
    "shutdownMs": 2000
  }
}
```

---

## 6. View model (bounded, versioned primitives)

### 6.1 Rules

1. A **view** is a single JSON object tree with a root node.
2. Only types listed in §6.2 are valid at `schemaVersion` / protocol `version` 1.
3. Unknown node `type` → treat the node as an error placeholder (muted text
   `unsupported: <type>`); do **not** kill the pane process on first sight —
   count toward malformed-update budget (§10).
4. Max tree nodes: **512**. Max JSON depth: **16**. Max string payload per
   text field: **4 KiB**. Max total `pane.render` line: **256 KiB**.
5. **No raw terminal escapes.** Hosts strip or reject CSI/OSC sequences in
   text fields (replace with safe placeholders). Web hosts HTML-escape.
6. **No host theme objects.** Nodes reference **semantic style roles** only
   (`title`, `body`, `muted`, `accent`, `success`, `warning`, `error`,
   `danger`). Hosts map roles through [theme.md](theme.md) tokens (TUI) or CSS
   variables (web).
7. Glyphs: prefer plain Unicode; hosts MAY substitute [theme icons](theme.md)
   when a node sets `"icon": "<name>"` from the **closed** icon name set
   advertised at start (`pane.start.icons`). Unknown icon names → omit icon.

### 6.2 Node types (`pane/1`)

| `type` | Purpose | Key fields |
|---|---|---|
| `column` | Vertical stack | `children[]`, `gap?` (0–4) |
| `row` | Horizontal stack | `children[]`, `gap?`, `wrap?` bool |
| `text` | Single run of text | `text` or `textFrom`, `style?`, `truncate?` (`end`\|`middle`) |
| `markdown` | Constrained markdown | `text` or `textFrom` — **subset**: paragraphs, emphasis, inline code, fenced code, links (web: open via host; TUI: show URL muted). No HTML, no images, no raw HTML blocks. |
| `kv` | Key/value list | `entries[{key, value\|valueFrom}]` |
| `list` | Selectable or static list | `items[{id, label, detail?, icon?}]`, `selectable?`, `selectedId?` |
| `table` | Compact table | `columns[{id, header, width?}]`, `rows[{cells{colId: text}}]`, max 32 cols / 200 rows shown (host may virtualize) |
| `meter` | Progress / fraction | `label?`, `value`\|`valueFrom`, `max`\|`maxFrom`, `unknownMax?` path or bool |
| `badge` | Pill label | `text`, `tone?` (`neutral`\|`accent`\|`success`\|`warning`\|`error`) |
| `spacer` | Flexible empty space | `flex?` (default 1), `min?` rows/px hint 0–8 |
| `divider` | Horizontal rule | (none) |
| `empty` | Empty-state copy | `text`, `hint?` |

**Static bindings:** `textFrom` / `valueFrom` / `maxFrom` are dot-paths into
subscription snapshots. **Process mode** usually inlines concrete `text` /
`value` after computing them; `*From` is still allowed and resolved by the host
before paint.

### 6.3 Actions (declarative hooks on nodes)

Interactive nodes MAY declare actions. Actions never run plugin code inside the
host; they emit **host-mediated** intents.

| Action `type` | Effect | Notes |
|---|---|---|
| `copy` | Copy `text` / resolved value to clipboard | Host implements; may no-op if unsupported |
| `open` | Open `target` | `target` is `https?://…` (web/OS open) or workspace-relative path via `host.Files` open — requires matching permission |
| `command` | Request host slash/command | `name` must be in the allowlist advertised at start; pane cannot invent new engine ops |
| `notify` | Show host notice | `text`, `level` (`info`\|`warn`\|`error`) |
| `pane.emit` | Deliver payload back to process | Process mode only; host forwards as `pane.action` result |

Example list item:

```json
{
  "type": "list",
  "selectable": true,
  "selectedId": "item-1",
  "items": [
    {
      "id": "item-1",
      "label": "Open README",
      "actions": {
        "submit": { "type": "open", "target": "README.md" }
      }
    }
  ]
}
```

---

## 7. Host data feeds (subscriptions)

The host pushes **read-only snapshots**. Panes cannot subscribe to arbitrary
engine internals — only named feeds below (versioned additively).

| Feed ID | Snapshot fields (informative) | Permission bit |
|---|---|---|
| `session.summary` | `cwd`, `sessionId`, `sessionTitle`, `provider`, `model`, `agent`, `agentState` | `host: session.summary` |
| `usage` | `input`, `output`, `used`, `limit`, `limitKnown`, `source` | `host: usage` |
| `agents.roster` | `activeId`, `roots[{id,title,state,children[]}]` (redacted prompts) | `host: agents.roster` |
| `clock` | `unixMs` at low frequency (≤1 Hz) | `host: clock` |

Rules:

1. Snapshots are JSON objects. Secret-shaped strings are redacted before push
   ([secrets.md](secrets.md)).
2. Push only when the pane is **mounted** (visible or kept warm — host policy)
   and the feed is listed in both `subscriptions` and `permissions.host`.
3. Hosts coalesce bursts: minimum interval **100ms** per feed per pane unless
   the feed is `clock` (1s).
4. Future feeds require an ABI minor bump advertisement in `pane.start`
   `feeds` array; unknown subscription IDs in the definition → fail pane load
   with diagnostic (fail closed).

Static panes re-resolve `*From` bindings on each snapshot. Process panes
receive `pane.data`.

---

## 8. Permissions

Declared in the definition; enforced by the host. **Default deny** for every
axis not granted.

### 8.1 Axes

| Axis | Values | Meaning |
|---|---|---|
| `host` | string[] of feed IDs | Which §7 feeds may be subscribed. |
| `fs` | `none` (default) \| `read-workspace` \| `read-write-workspace` | Workspace files via **host.Files** only — never direct host FS APIs from TUI code paths. Process panes with `fs` other than `none` still perform I/O in their **own** process; trust review must show this. Static panes cannot use `read-write-workspace`. |
| `network` | `none` (default) \| `host-mediated` | `none`: process should not need net (OS still applies). `host-mediated`: may call allowlisted host HTTP helper if implemented later; **v1 hosts may reject `host-mediated`** with a clear diagnostic until implemented. No raw “open any socket” grant in the ABI. |
| `command` | `none` (default) \| `allowlisted` | `allowlisted` reserved for future host-brokered commands; **v1 must reject** definitions with `command` ≠ `none` or treat as load failure. Subprocess panes that need tools use their own binary under executable trust — not a shell escape hatch from the pane ABI. |

### 8.2 Trust class mapping

| Mode | Trust |
|---|---|
| `static` with `fs=none`, `network=none`, `command=none` | **Passive** — loads when plugin enabled (like themes). |
| `static` with any broader grant | **Invalid** for v1 — reject definition. |
| `process` | **Executable** — blocked until trust record matches plugin source + digest + capabilities including `panes.process` ([plugins.md](plugins.md#5-trust-model)). |

### 8.3 What permissions are not

- Not a sandbox stronger than OS defaults for the child process (same honesty
  as [harnesses.md](harnesses.md) warning). Trust review is mandatory because
  process panes are native code.
- Not access to auth stores, secret plaintext, or provider keys.
- Not permission to register tools, hooks, or MCP servers (those are separate
  contribution types).

---

## 9. Sizing, layout, and focus

### 9.1 Sizing hints

| Field | Default | Rule |
|---|---|---|
| `minWidth` | 20 | Columns (TUI) / CSS `ch` hint (web). Clamped 12–80. |
| `minHeight` | 4 | Rows / approx lines. Clamped 3–40. |
| `preferredHeight` | 12 | Host stack splitter hint. Clamped ≥ `minHeight`. |
| `preferredWidth` | omit | Optional; hosts may ignore in side-column layouts. |
| `flex` | 1 | Relative weight vs siblings in a plugin group. |

Hosts **always** win: if the terminal is smaller than `min*`, the host still
mounts the pane and the view must tolerate narrow widths (truncate text,
collapse tables). A pane that sends renders assuming infinite width is
malformed under §10 when lines overflow host clip budgets repeatedly.

### 9.2 Chrome

- Host draws panel chrome, title, focus border, and optional footer key hints.
- Contribution view paints **inner content only**.
- Title comes from definition / `pane.meta`; host prefixes provenance in
  plugin-manager UIs, not necessarily in the tight title bar.

### 9.3 Groups

| `group` value | Behavior |
|---|---|
| omitted / `"plugin"` | Host places the pane in a shared **Plugin** stack group (or per-plugin subgroup). Exact grouping is host UX (#731) but must participate in existing cycle commands (`ctrl+o` / `ctrl+p`, group cycle). |
| other string | Hint only; host may sanitize to slug and cluster matching hints. |

Plugin panes **must not** inject themselves into built-in groups (`session`,
`agents`, `files`, `project`) in v1 — avoids surprising splits next to
`context`/`files`. Hosts ignore or remap such requests with a diagnostic.

### 9.4 Focus and input routing

When the pane is the active right-pane window and focus is on the right column:

1. Host translates key/pointer events into `pane.input` messages (§11).
2. Global / navigation keybinds (`ctrl+h/l`, `ctrl+o/p`, quit, command palette,
   …) are **not** delivered to the pane.
3. Process panes do not read the TTY directly; stdin is the JSONL control
   channel only.

---

## 10. Timeouts, rate limits, failure isolation

Normative host behavior for #731 / #732. Defaults apply unless definition
`timeouts` lowers them (definitions cannot raise above host ceilings).

### 10.1 Defaults and ceilings

| Budget | Default | Host ceiling | On exceed |
|---|---|---|---|
| Process start until first valid `pane.hello` | 5s | 15s | Kill process; pane error state |
| Shutdown after `pane.shutdown` | 2s | 5s | Kill process |
| Time to handle `pane.input` before next input coalesced | 50ms soft | — | Coalesce; no kill |
| `pane.render` apply time on host | 50ms soft target | — | Drop frame; count slow-render |
| Renders accepted | **10/s** sustained, burst 20 | — | Drop extras; `pane.notice` rate-limit once/5s |
| Malformed messages | 8 in 30s | — | Enter error state; stop process |
| Stdout line size | 256 KiB | 256 KiB | Drop line; malformed++ |
| Aggregate stdout per pane lifetime | 32 MiB | 64 MiB | Stop process |
| Process RSS / CPU | host-defined optional cgroup | — | Stop process if enforced |

### 10.2 Failure classes

| Class | Host behavior |
|---|---|
| **Crash** (non-zero exit, signal) | Pane shows error empty-state with provenance + “disable plugin pane”; **does not** exit Strike; other panes unaffected. Auto-restart at most **once** per mount unless user re-enables. |
| **Hang** (no hello / stuck, ignore shutdown) | Kill after timeout; same error state as crash. |
| **Malformed update** | Drop message; increment budget; on exceed → stop + error state. |
| **High-frequency updates** | Drop over quota; keep last good view. |
| **Oversized / deep tree** | Reject render; keep last good view; malformed++. |
| **Permission violation** in action | Deny action; `pane.notice` to process if running; user-visible notice optional. |
| **Plugin disabled / removed** | Teardown (§12); remove from registry; persist layout without dangling IDs. |

### 10.3 Isolation guarantees

1. Pane process stdout/stderr are not mixed into the session transcript or
   model context.
2. Stderr is captured to a ring buffer for doctor/plugin inspect (redacted),
   max 64 KiB.
3. A panic in host render adaptation must be recovered per pane (#731 tests);
   never abort the TUI event loop.
4. Web: a pane iframe/worker failure must not unmount the cockpit shell (#732).

---

## 11. Process protocol (JSONL, `version: 1`)

Mirrors the harness transport style ([harnesses.md](harnesses.md#private-transport)):
one process per mounted pane instance, NDJSON lines, stdout = protocol only,
stderr = diagnostics.

### 11.1 Process model

1. Host starts `command` + `args` with cwd = plugin root (or host-defined
   scratch with `STRIKE_PLUGIN_ROOT` set).
2. Env: filtered minimal env + declared `env` (resolved secret refs) +
   `STRIKE_PANE_ID`, `STRIKE_PLUGIN_ID`, `STRIKE_PANE_ABI=pane/1`.
3. Host writes messages to stdin; reads stdout lines.
4. One active instance per `(plugin id, pane id)` per frontend session unless
   the host documents multi-attach (v1: single).

### 11.2 Host → pane messages

#### `pane.start`

```json
{
  "version": 1,
  "type": "pane.start",
  "paneId": "acme.board",
  "pluginId": "acme.review-pack",
  "pluginVersion": "1.2.0",
  "size": { "width": 36, "height": 14 },
  "theme": {
    "appearance": "dark",
    "roles": ["title", "body", "muted", "accent", "success", "warning", "error", "danger"]
  },
  "feeds": ["session.summary", "usage", "agents.roster", "clock"],
  "icons": ["check", "warn", "error", "agent", "folder", "file"],
  "commands": ["agents", "files", "theme"],
  "permissions": {
    "host": ["session.summary", "agents.roster"],
    "fs": "read-workspace",
    "network": "none",
    "command": "none"
  },
  "subscriptions": ["session.summary", "agents.roster"]
}
```

#### `pane.resize`

```json
{
  "version": 1,
  "type": "pane.resize",
  "size": { "width": 40, "height": 18 }
}
```

#### `pane.focus`

```json
{
  "version": 1,
  "type": "pane.focus",
  "focused": true
}
```

#### `pane.data`

```json
{
  "version": 1,
  "type": "pane.data",
  "feed": "session.summary",
  "snapshot": {
    "cwd": "/home/me/proj",
    "sessionId": "…",
    "sessionTitle": "…",
    "provider": "echo",
    "model": "echo",
    "agent": "build",
    "agentState": "idle"
  }
}
```

#### `pane.input`

```json
{
  "version": 1,
  "type": "pane.input",
  "event": {
    "kind": "key",
    "key": "enter",
    "mods": []
  }
}
```

`kind` is `key` | `pointer`.  

`key` values are **normalized names** (`enter`, `esc`, `up`, `down`, `left`,
`right`, `tab`, `backspace`, `space`, `r`, …) — not terminal sequences.  

`pointer`: `{ "kind": "pointer", "button": "left", "x", "y", "action": "click"|"scroll", "deltaY"? }` in content-cell coordinates.

#### `pane.shutdown`

```json
{ "version": 1, "type": "pane.shutdown", "reason": "unmount" }
```

`reason`: `unmount` | `disable` | `error` | `host_exit`.

### 11.3 Pane → host messages

#### `pane.hello` (required first)

```json
{
  "version": 1,
  "type": "pane.hello",
  "paneId": "acme.board",
  "abi": "pane/1"
}
```

#### `pane.meta`

```json
{
  "version": 1,
  "type": "pane.meta",
  "title": "Review Board",
  "status": "3 open"
}
```

#### `pane.render`

```json
{
  "version": 1,
  "type": "pane.render",
  "rev": 1,
  "view": {
    "type": "column",
    "gap": 1,
    "children": [
      { "type": "text", "text": "Open reviews", "style": "title" },
      {
        "type": "list",
        "selectable": true,
        "selectedId": "r1",
        "items": [
          {
            "id": "r1",
            "label": "PR #12",
            "detail": "lint clean",
            "actions": {
              "submit": { "type": "open", "target": "https://example.com/p/12" }
            }
          }
        ]
      }
    ]
  }
}
```

`rev` is monotonic; hosts may ignore stale revs.

#### `pane.action` (process requests host to run a declared action)

```json
{
  "version": 1,
  "type": "pane.action",
  "id": "act-1",
  "action": { "type": "notify", "text": "Refreshed", "level": "info" }
}
```

Host replies:

```json
{
  "version": 1,
  "type": "pane.action.result",
  "id": "act-1",
  "ok": true
}
```

or `ok: false` with `error` string.

#### `pane.error`

```json
{
  "version": 1,
  "type": "pane.error",
  "message": "board backend unavailable"
}
```

Surfaces in pane empty-state; does not crash host.

#### `pane.exit`

```json
{ "version": 1, "type": "pane.exit", "code": 0 }
```

Optional clean exit signal before process ends.

### 11.4 Validation

Hosts reject: missing `version`/`type`, `version !== 1`, unknown critical
types for start handshake, non-object lines, duplicate `pane.hello`, renders
before hello. Same reliability posture as harnesses (not a security boundary
alone — trust + permissions are).

---

## 12. Lifecycle

```text
manifest valid → plugin enabled
       │
       ├─ mode=static ──► register pane descriptor (passive)
       │
       └─ mode=process ──► require trust → register descriptor
                │
user focuses / host mounts pane
                │
       ├─ static: resolve view + subscriptions; paint
       └─ process: start → hello → start msg → data/input/resize loop
                │
user navigates away (host MAY keep warm or freeze)
                │
disable / remove / host exit
                │
       send shutdown → wait → kill → drop registry entry → persist layout
```

| Event | Static | Process |
|---|---|---|
| Plugin enable | Available on next host load (wave-1: restart OK) | Available when trusted |
| First mount | Bind subscriptions | Start process |
| Hide / unfocus | Keep last view; may pause data | `pane.focus` false; host MAY stop data pushes |
| Unmount | Drop bindings | `pane.shutdown`; kill on timeout |
| Disable plugin | Unregister | Shutdown + unregister |
| Definition/trust invalidate | Unregister | Shutdown + require re-trust |

Hot reload without restart is a non-goal for early waves (same as
[plugins.md](plugins.md#42-enablement)); hosts MAY reload on explicit user
action later without changing this ABI.

---

## 13. Versioning

| Layer | Field | Policy |
|---|---|---|
| Plugin manifest | `schemaVersion` | Unchanged by this ABI; still `1`. |
| Pane definition | `schemaVersion` | Integer; this doc = **1**. |
| Process messages | `version` | Integer; this doc = **1**. |
| Manifest `abi` | `pane/1` | Major in the string; `pane/2` only with breaking change. |

Compatibility:

| Host \ Contribution | Behavior |
|---|---|
| Host implements `pane/1`, definition `schemaVersion` 1 | Load. |
| Definition `schemaVersion` > host max | Skip pane; diagnostic to upgrade Strike. |
| Process speaks `version` > host | Kill after hello mismatch; error state. |
| Additive node types in future `pane/1.x` advertisement | Host ignores unknown nodes per §6.1.3 only when the start handshake lists `viewFeatures` containing that type; otherwise placeholder. |
| Removing fields | New definition schemaVersion or `pane/2`. |

---

## 14. Web mapping (frontend-neutral)

**#732 must not import TUI packages.** Mapping rules:

| ABI concept | TUI host (#731) | Web host (#732) |
|---|---|---|
| View tree | Adapt to `internal/tui/ui` + theme tokens | Adapt to React components + CSS variables from the same role names |
| Size `width`/`height` | Terminal cells | Content box in `ch`/`rem`; host still sends integer width/height in **cell-equivalent** units documented as “approximate monospace columns/rows” |
| Input | Key/pointer → `pane.input` | DOM events normalized to the same `key` names |
| Chrome | `ui.Panel` | Cockpit card/shell |
| Process | Child process on strike server | Same process supervised by backend; browser only receives rendered trees / action bridges over existing auth’d APIs |
| Static | In-process bind+paint | Same JSON resolved server-side or in trusted client mapper — **no** eval |
| Failure | Per-window error view | Per-card error boundary |

**Forbidden in web APIs:** Go `window` interface, `tea.Msg`, lipgloss styles,
terminal escape passthrough, Bubble Tea commands.

---

## 15. Complete end-to-end example

Bundle layout:

```text
acme-review-pack/
  plugin.json
  panes/
    status.json          # static (§5.2)
    board.json           # process (§5.3)
  bin/
    review-board-pane    # process executable
```

`plugin.json` (excerpt):

```json
{
  "schemaVersion": 1,
  "id": "acme.review-pack",
  "version": "1.2.0",
  "name": "Acme Review Pack",
  "strike": { "min": "0.2.0" },
  "capabilities": ["panes", "panes.process", "agents", "skills"],
  "contributions": {
    "panes": [
      {
        "id": "acme.status",
        "path": "panes/status.json",
        "abi": "pane/1"
      },
      {
        "id": "acme.board",
        "path": "panes/board.json",
        "abi": "pane/1"
      }
    ]
  }
}
```

### 15.1 Static path (data → render)

1. User enables `acme.review-pack` (passive OK for `acme.status`).
2. Host registers pane id `acme.status`, title “Acme Status”.
3. On mount, host subscribes to `session.summary` and `usage`.
4. Host resolves `view` bindings and paints `column` / `kv` / `meter` with theme
   roles.
5. On each usage push, meter updates — no subprocess.
6. On plugin disable, pane disappears from cycle order; layout forgets the id.

### 15.2 Process path (input → render → teardown)

Message trace (illustrative):

```jsonl
// host → pane
{"version":1,"type":"pane.start","paneId":"acme.board","pluginId":"acme.review-pack","pluginVersion":"1.2.0","size":{"width":36,"height":14},"theme":{"appearance":"dark","roles":["title","body","muted","accent","success","warning","error","danger"]},"feeds":["session.summary","usage","agents.roster","clock"],"icons":["check","warn","error","agent"],"commands":["agents","files"],"permissions":{"host":["session.summary","agents.roster"],"fs":"read-workspace","network":"none","command":"none"},"subscriptions":["session.summary","agents.roster"]}
// pane → host
{"version":1,"type":"pane.hello","paneId":"acme.board","abi":"pane/1"}
{"version":1,"type":"pane.meta","title":"Review Board","status":"loading"}
{"version":1,"type":"pane.render","rev":1,"view":{"type":"empty","text":"Loading reviews…"}}
// host → pane
{"version":1,"type":"pane.data","feed":"session.summary","snapshot":{"cwd":"/work/proj","sessionId":"s1","sessionTitle":"lint","provider":"echo","model":"echo","agent":"build","agentState":"idle"}}
// pane → host
{"version":1,"type":"pane.render","rev":2,"view":{"type":"column","gap":1,"children":[{"type":"text","text":"Reviews","style":"title"},{"type":"list","selectable":true,"selectedId":"r1","items":[{"id":"r1","label":"PR #12","detail":"open","actions":{"submit":{"type":"notify","text":"Opened PR #12","level":"info"}}}]}]}}
// user presses enter (host → pane)
{"version":1,"type":"pane.input","event":{"kind":"key","key":"enter","mods":[]}}
// pane → host
{"version":1,"type":"pane.action","id":"a1","action":{"type":"notify","text":"Opened PR #12","level":"info"}}
// host → pane
{"version":1,"type":"pane.action.result","id":"a1","ok":true}
// user disables plugin (host → pane)
{"version":1,"type":"pane.shutdown","reason":"disable"}
// pane → host
{"version":1,"type":"pane.exit","code":0}
```

This exercises **data**, **rendering**, **input**, and **teardown** without
touching the private Go `window` interface.

---

## 16. Implementation map

| Stage | Issue | Touchpoints |
|---|---|---|
| Bundle contract | #725 | `panes/` tree; reserved entries → this ABI |
| Pane ABI | #522 (this doc) | Definition, process protocol, permissions, isolation |
| Passive/exec loaders | #726 #728 | Register descriptors; trust for `panes.process` |
| Plugin manager | #730 | Enable/disable, provenance, recovery actions |
| TUI host | #731 | Adapter window, supervisor, focus/layout, isolation tests |
| Web parity | #732 | Same trees over host APIs; no TUI types |

---

## 17. Acceptance mapping (#522)

| AC | Section |
|---|---|
| No private Go `window` interface; no in-process compiled plugins | §1.2, §2, §14 |
| Rendering and interaction primitives bounded and versioned | §6, §13 |
| FS, network, command, host-service access explicitly permissioned | §7, §8 |
| Crash, hang, malformed update, high-frequency update defined | §10 |
| Complete example: data, rendering, input, teardown | §15 |

---

## 18. Non-goals (restated)

- Implementing TUI or web hosts in this change.
- Unrestricted terminal escape output or plugin PTYs.
- In-process Go pane plugins or OpenCode-style Node pane hosts.
- Weakening [plugins.md](plugins.md) path confinement, digest trust, or secret rules.
