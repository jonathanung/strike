# Config

`~/.strike/config` (global) merged with `./.strike/config` (project), both
JSON:

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-5",
  "effort": "high",
  "defaultAgent": "build",
  "leanCode": "lite",
  "theme": "strike",
  "vimMode": "pane",
  "notify": "unfocused-only",
  "permissionMode": "default",
  "permissionAutoApproveSeconds": 0,
  "permissionAutoApproveExclude": ["bash"],
  "compactionStrategy": "trim",
  "compactionModel": "",
  "session": {
    "worktree": "off",
    "worktreeCleanup": "keep"
  },
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" },
    { "permission": "write", "pattern": "**/*.env", "action": "deny" }
  ]
}
```

Rules concatenate across layers; the last matching rule wins, so project
config overrides global, and session "always" grants override both.

**Permission mode dial:** `permissionMode` sets the default tool-permission
posture for **new** sessions: `default` | `plan` | `soft-approve` |
`accept-edits` | `yolo` (see [usage.md](usage.md)). Session changes via
Shift+Tab or `/mode` persist in the session JSONL, not back into this file.
Distinct from `/autonomy` (workflow exit gates).

**Lean code:** `leanCode` is `off` | `lite` (default) | `full`. Injects
agent-scoped efficiency guidance into the system prompt (strict ladder for
build/general/debugger; softer scaling-aware lean for plan/orchestrator;
none for explore/reviewer/tester/validator/commit). Inspired by
[ponytail](https://github.com/DietrichGebert/ponytail) (clean-room wording).
Details: [agents-skills.md](agents-skills.md#lean-code-ponytail-lite).

**Permission soft-approve / auto-approve:** session mode `soft-approve`
(`permissionMode`, `/mode`, Shift+Tab) arms a **visible** 15s countdown on
permission asks and submits **allow once** at zero if the user does nothing.
Esc, reject, or any explicit once/session/project choice cancels the timer.
Hard deny rules always win. Queued/hidden asks (behind another modal) do not
count down or auto-approve. Disabled by default (mode `default`, seconds `0`).

`permissionAutoApproveSeconds` (1–60) optionally sets/overrides the countdown
duration without selecting soft-approve mode; when soft-approve is active and
seconds is unset/`0`, the default is **15**. Names in
`permissionAutoApproveExclude` (case-insensitive) never auto-approve.

## Desktop notifications (`notify`)

When the terminal is unfocused, strike can ring the bell and emit OSC 9
desktop notifications for **needs attention** (permission / question) and
**long turn complete** (≥30s). Notification text is fixed labels only — never
paths, prompts, or secrets.

| Value | Behavior |
|---|---|
| `unfocused-only` (default) | notify when unfocused; if the terminal never reports focus, use the same path for attention + long turns |
| `on` | always notify (attention + long turns), even when focused |
| `off` | never notify |

Unknown values are ignored at load time.

## Session worktrees

When concurrent root sessions would otherwise share one working tree, strike
can bind each session's tool CWD to its own `git worktree` under
`<repo>/.strike/worktrees/<session-id>/` (gitignored via `*/worktrees`).

| `session.worktree` | Behavior |
|---|---|
| `off` (default) | launch cwd; single-session default |
| `auto` | worktree when a second root session starts in-process |
| `always` | every new root session gets a worktree (git repos only) |

| `session.worktreeCleanup` | Behavior |
|---|---|
| `keep` (default) | leave the worktree and branch after session close |
| `delete` | `git worktree remove` + delete the branch on close |

CLI: `strike --worktree` forces a worktree for that invocation (same as always
for one session). Non-git directories and `git worktree add` failures return a
clear error and do not leave a half-bound session. Project-scoped state
(history, memory, issues) stays keyed to the main repo, not the worktree path.
Tools (`bash`, `read`, `write`, …) resolve paths inside the session worktree.

**ctrl+d saves defaults**: on the main screen it persists the current
provider/model/agent/effort/theme to `~/.strike/config`; in the provider
picker it saves the highlighted provider; in the model picker it saves
provider + model; in the effort picker it saves the highlighted level; in
the theme picker it saves the highlighted theme id.

## Theme

`theme` is a color-theme id: bundled JSON themes plus files under
`~/.strike/themes` and `./.strike/themes`. Empty means the stock `strike`
palette. In the TUI, bare `/theme` opens a picker; `/theme <id>` applies one;
`/theme dark|light|auto` only adjusts session appearance (forced background
detect), not the color-theme id.

## Keybinds

Remap app-level chords without recompiling. Ids match the in-app cheatsheet
(`/keys` / `f1`). Values are a key string or an array of alternate sequences:

```json
{
  "keybinds": {
    "nav.jump-bottom": "ctrl+b",
    "global.palette": ["ctrl+p", "ctrl+k"],
    "composer.newline": ["alt+enter", "ctrl+j"]
  }
}
```

Layers merge last-wins per id (project overrides global). Unknown binding ids
and invalid/empty chords fail config load with a clear error. Critical
`global.quit` and `global.interrupt` cannot be cleared.

Shared chords across different actions are allowed (context-specific routing
in the TUI decides the winner). `/keys` shows the effective map; `/keys reset`
restores built-in defaults for the current session only — delete the
`keybinds` object from config to persist defaults.

List/permission modal conventions (`lists.*`, `perm.*`) and agents-pane local
controls (`agents.*`) are not remappable.

## MCP servers (stdio + HTTP)

Connect external [Model Context Protocol](https://modelcontextprotocol.io)
servers so their tools appear in the model registry as `mcp_<server>_<tool>`.
Supported transports: **stdio** (local subprocess) and **streamable HTTP**
(remote endpoint; JSON or SSE responses).

### Stdio (local)

```json
{
  "mcp": {
    "servers": {
      "github": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-github"],
        "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "…" }
      }
    }
  }
}
```

### Streamable HTTP (remote)

```json
{
  "mcp": {
    "servers": {
      "remote": {
        "type": "http",
        "url": "https://mcp.example.com/mcp",
        "headers": { "Authorization": "Bearer …" }
      }
    }
  }
}
```

| Field | Required | Notes |
|---|---|---|
| `servers.<name>` | yes | short letter-led slug (`[A-Za-z][A-Za-z0-9_-]*`) |
| `type` | no | `stdio` (default) or `http` (`sse` is accepted as an alias for `http`) |
| `command` | stdio | executable on `PATH` or absolute path |
| `args` | no | argv after the command |
| `env` | no | stdio env overlay; **never logged** |
| `url` | http | MCP endpoint URL (if set without `type`, transport is `http`) |
| `headers` | no | HTTP request headers (e.g. `Authorization`); **never logged or shown in `/mcp`** |

Layers: when a layer sets `mcp.servers` (including `{}`), it **replaces** the
previous layer's server map. Omitted `mcp` leaves the lower layer unchanged.

Lifecycle: servers start with the session (after the tool worktree is bound),
list tools once, and shut down on exit. A crashed or unreachable server does
not take down strike — its tools error cleanly; `/mcp` shows `up` / `down` /
`error` / `disabled`.

Control from the TUI:

- `/mcp` — status (transport, endpoint label, tools, errors)
- `/mcp retry [name]` — reconnect one server, or every non-up server
- `/mcp disable <name>` — stop a server and unregister its tools

Permissions: every MCP tool call asks with permission name `mcp` and pattern
`<server>/<tool>` (default action **ask**). Allow a server or tool in config:

```json
{
  "permissions": [
    { "permission": "mcp", "pattern": "github/*", "action": "allow" },
    { "permission": "mcp", "pattern": "github/delete_*", "action": "deny" }
  ]
}
```

Treat project-local MCP config like shell hooks: stdio runs local commands;
HTTP may send secrets via `headers`. Prefer global `~/.strike/config` for shared
servers; review `command`/`args`/`env`/`url`/`headers` before trusting a
project's `.strike/config`.

## Custom providers

Add OpenAI-compatible (chat completions) or Anthropic-compatible (messages)
endpoints via **`providers.jsonc`** (preferred) or the `providers` array in
config. Layers merge last-wins by name:

`defaults → ~/.strike/config → ~/.strike/providers.jsonc → ./.strike/config → ./.strike/providers.jsonc`

(`.json` is accepted as well as `.jsonc`.) Credentials never live in these
files — use env refs and/or `/auth` / the auth store.

### `providers.jsonc` (OpenCode-style)

```jsonc
// ~/.strike/providers.jsonc or ./.strike/providers.jsonc
{
  "kimi": {
    "npm": "@ai-sdk/openai-compatible", // optional; hints wire dialect only (not loaded)
    "name": "Kimi",
    "options": {
      "baseURL": "https://api.example.com/v1",
      "apiKey": "{env:KIMI_API_KEY}"
    },
    "models": ["kimi-latest"]
  },
  "claude-proxy": {
    "npm": "@ai-sdk/anthropic",
    "options": {
      "baseURL": "$ANTHROPIC_BASE_URL",
      "apiKey": "${ANTHROPIC_AUTH_TOKEN}"
    }
  }
}
```

| Field | Required | Notes |
|---|---|---|
| map key | yes | provider id (lowercased slug); not `anthropic`/`openai`/`xai`/`echo` |
| `options.baseURL` | yes | absolute `http`/`https` URL, or `{env:VAR}` / `$VAR` / `${VAR}` |
| `options.apiKey` | no | env ref only (`{env:NAME}`, `$NAME`, `${NAME}`) → checked before auth store |
| `npm` | no | ignored at runtime; `anthropic` in the name → anthropic wire, else openai |
| `api` | no | strike override: `openai` or `anthropic` (wins over npm hint) |
| `models` | no | listed in `/model`; first is the default when unset |
| `options.headers` | no | extra HTTP headers (values may use env refs) |

### Config `providers` array (legacy)

```json
{
  "providers": [
    {
      "name": "kimi",
      "baseURL": "https://api.example.com/v1",
      "api": "openai",
      "apiKeyEnv": "KIMI_API_KEY",
      "models": ["kimi-latest"],
      "headers": { "X-Custom": "optional" }
    }
  ]
}
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | lowercase slug (`[a-z][a-z0-9_-]{0,63}`); not builtins |
| `baseURL` | yes | absolute URL or env ref template |
| `api` | yes | wire dialect: `openai` or `anthropic` |
| `apiKeyEnv` | no | env var name (or `{env:NAME}` / `$NAME`) checked before the auth store |
| `models` | no | listed in `/model`; first is the default when unset |
| `headers` | no | extra HTTP headers on every request (values may use env refs) |

**Env interpolation:** `{env:NAME}`, `$NAME`, and `${NAME}` expand from the
process environment (vars exported to the strike process, e.g. via bashrc).

**TUI:** `/settings` CRUD and `/provider` → “Add custom provider…”. Custom
names appear in `/provider` like built-ins. **Logout** (`ctrl+x` or
`/auth logout <name>`) of a custom provider **deletes** its definition from
config/providers.jsonc and clears credentials; `/settings` `d` does the same.
Built-in logout only clears credentials.

## Embedded editor (`vimMode`)

`/vim [path[:line]]` opens a file in an editor. `vimMode` selects how:

| Value | Behavior |
|---|---|
| `pane` (default) | embed nvim/vim in the right-pane `editor` window (PTY) |
| `overlay` | embed in a centered modal overlay |
| `takeover` | full-screen handoff via `tea.ExecProcess` |

Unknown values are ignored at load time. GUI `$EDITOR` values always take
over the terminal regardless of `vimMode`. Leave the embedded editor with
`ctrl+g`.

## Hooks

Lifecycle hooks live in the same JSON config under `hooks` (global then
project **concatenate**). Each entry is either a **declarative rule**
(`action`) or a **shell command** (`command`) — not both.

```json
{
  "hooks": [
    {
      "event": "pre_tool_use",
      "matcher": "bash",
      "action": "log"
    },
    {
      "event": "pre_tool_use",
      "matcher": "write",
      "action": "block",
      "message": "writes blocked by policy"
    },
    {
      "event": "post_tool_use",
      "matcher": "edit",
      "command": "echo ok",
      "timeoutMs": 10000
    }
  ]
}
```

| Field | Notes |
|---|---|
| `event` | `pre_tool_use`, `post_tool_use`, `turn_start`, `turn_end` |
| `matcher` | doublestar on tool name; empty/`*` = all (turn events: empty/`*` only) |
| `action` | `log`, `block`, or `notify` (block only on `pre_tool_use`) |
| `message` | optional block/notify text |
| `command` | `bash -c` with event JSON on stdin (shell hooks: tool events) |
| `timeoutMs` | shell bound; default 30000, max 120000 |

Invalid rows are dropped at load. Peer event-name mapping (CC/OpenCode/Crush):
[peer-ecosystem.md](peer-ecosystem.md#hooks-alignment).

## History compaction

`/compact` and automatic threshold/overflow compaction shrink model-facing
history while keeping a recent tail.

| Field | Values | Default |
|---|---|---|
| `compactionStrategy` | `trim` (drop older turns) or `summarize` (model-authored summary of dropped turns) | `trim` |
| `compactionModel` | optional model id for the summarize call (same provider as the session) | session model |

On summarize failure the engine falls back to trim and emits a notice. The
summary path never re-runs tools.

## Reasoning effort

`/effort` sets how much internal reasoning the model spends before answering.
The ladder is normalized across vendors and each adapter maps it to its own
wire fields — Anthropic to adaptive thinking plus `output_config.effort`, the
OpenAI family to a `reasoning_effort` string. With no level set, strike sends
no reasoning fields at all and each provider's own default applies.

The two ends of the ladder are requests, not guarantees, because the vendor
ladders differ in length: `off` disables thinking outright on Anthropic but
floors at `minimal` on the OpenAI family (which has no zero setting), and
`xhigh`/`max` clamp down to `high` there for the same reason.

| Level | Meaning |
|---|---|
| `off` | least reasoning the provider allows — fastest and cheapest |
| `low` | minimal reasoning for short, scoped tasks |
| `medium` | balanced reasoning for routine work |
| `high` | thorough reasoning — the provider default |
| `xhigh` | deeper reasoning, best for coding and agentic work |
| `max` | maximum reasoning when correctness beats cost |

Agents, skills, and workflows (including `.claude` / `.opencode` discovery
roots and merge order): [agents-skills.md](agents-skills.md). Peer import
inventory: [peer-ecosystem.md](peer-ecosystem.md).
