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
  "nanoMode": "pane",
  "mdReadMode": "embedded",
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
Each `bash` invocation is a fresh process whose cwd is that session workdir
(workspace root, or the bound git worktree root). A `cd` inside one command
does not affect later bash calls or other tools; chain with `&&` or
`(cd subdir && …)` when a single command needs a subdirectory.

**ctrl+d saves defaults**: on the main screen it persists the current
provider/model/agent/effort/theme to `~/.strike/config`; in the provider
picker it saves the highlighted provider; in the model picker it saves
provider + model; in the effort picker it saves the highlighted level; in
the theme picker it saves the highlighted theme id.

**/settings Defaults**: interactive editor for theme, vimMode, nanoMode,
mdReadMode, and permissionMode (plus a read-only view of provider/model/
agent/effort). Changes write `~/.strike/config` and apply theme/editor
presentation to the current session immediately.

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
    "global.palette": "ctrl+k",
    "composer.newline": ["ctrl+j", "alt+enter"],
    "nav.window-next": "ctrl+o",
    "nav.window-prev": "ctrl+p",
    "nav.tool-expand": "alt+enter"
  }
}
```

Layers merge last-wins per id (project overrides global). Unknown binding ids
and invalid/empty chords fail config load with a clear error. Critical
`global.quit` and `global.interrupt` cannot be cleared.

Shared chords across different actions are allowed (context-specific routing
in the TUI decides the winner — e.g. default `alt+enter` is newline while
typing and tool expand only when the composer is empty). `/keys` shows the
effective map; `/keys reset` restores built-in defaults for the current
session only — delete the `keybinds` object from config to persist defaults.

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

### Disable default (builtin) providers

Hide stock catalog providers (`anthropic`, `openai`, `xai`, `gemini`, `kimi`,
`deepseek`, `echo`) so only custom endpoints appear in `/provider`, `/auth`,
and model pickers. Same keys work in **`providers.jsonc`** or config JSON;
later layers win (project overrides global; providers.jsonc overrides the
config file in the same root).

```jsonc
// ~/.strike/providers.jsonc — custom-only setup, keep openai available
{
  "disable-default-providers": true,
  "disable-default-openai": false, // per-provider override re-enables
  "disable-default-anthropic": true, // redundant when all are disabled
  "acme": {
    "options": {
      "baseURL": "https://api.example.com/v1",
      "apiKey": "{env:ACME_API_KEY}"
    },
    "models": ["acme-latest"]
  }
}
```

| Key | Effect |
|---|---|
| `disable-default-providers` | `true` hides **all** builtins unless a per-provider flag says otherwise |
| `disable-default-<name>` | `true` disables that builtin; `false` **re-enables** it when the bulk flag is on |

Customs are never affected. Selecting a disabled builtin (`--provider`,
`/provider`, config default) fails with a clear error. Overlays/endpoints for
a disabled builtin are ignored for selection until it is re-enabled.

### `providers.jsonc` (OpenCode-style)

```jsonc
// ~/.strike/providers.jsonc or ./.strike/providers.jsonc
{
  // Custom / self-hosted endpoint
  "acme": {
    "npm": "@ai-sdk/openai-compatible", // optional; hints wire dialect only (not loaded)
    "name": "Acme",
    "options": {
      "baseURL": "https://api.example.com/v1",
      "apiKey": "{env:ACME_API_KEY}"
    },
    // Legacy flat ids still work:
    // "models": ["acme-latest"]
    // Nested rich objects (display name, limits, variants):
    "models": {
      "acme-latest": {
        "name": "Acme Latest",
        "limit": { "context": 128000, "output": 8192 },
        "options": { "forcedReasoning": true },
        "variants": {
          "high": { "reasoningEffort": "high", "textVerbosity": "low" },
          "low": { "reasoningEffort": "low" }
        }
      }
    }
  },
  // Built-in overlay — does NOT become a separate custom provider.
  // options.baseURL / options.apiKey customize the stock endpoint (proxy).
  // Omit models (or leave empty) to keep the full models.dev catalog.
  // Overlay one id to refine name/limits/variants; other catalog ids remain.
  "anthropic": {
    "name": "Corp Anthropic",
    "options": {
      // OpenCode/AI SDK shape: include /v1 (strike also accepts origin-only).
      "baseURL": "https://proxy.example/anthropic/v1",
      "apiKey": "{env:CORP_ANTHROPIC_KEY}"
    }
  },
  "openai": {
    "models": {
      "gpt-5.5": {
        "name": "GPT-5.5",
        "limit": { "context": 272000, "output": 128000 },
        "variants": {
          "high": { "reasoningEffort": "high" },
          "xhigh": { "reasoningEffort": "xhigh" }
        }
      }
    }
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
| map key | yes | provider id (lowercased slug). Built-ins (`anthropic`/`openai`/`xai`/`gemini`/`kimi`/`deepseek`/`echo`) stay builtins: options → **endpoint overlay**, models → **catalog overlay**. Other keys are custom providers. |
| `options.baseURL` | custom yes | absolute `http`/`https` URL, or `{env:VAR}` / `$VAR` / `${VAR}`. On builtins, optional — overrides the stock endpoint. **OpenCode shape:** include `/v1` (Anthropic → `…/v1` + `/messages`; OpenAI → `…/v1` + `/chat/completions` or `/responses`). Origin-only Anthropic bases still work. |
| `options.apiKey` | no | env ref only (`{env:NAME}`, `$NAME`, `${NAME}`) → checked before auth store. On builtins, pins the env var used for that provider. Missing env fails at select time with a clear error. |
| `npm` | no | **advisory only** — never installed; `anthropic` → Messages; `@ai-sdk/openai` → **Responses** (`/responses`); `@ai-sdk/openai-compatible` (default) → chat completions |
| `api` | no | strike override: `openai` (chat), `responses`, or `anthropic` (wins over npm hint) |
| `models` | no | `[]string` (legacy) **or** object map id → model def; see merge rules below |
| `models.<id>` map key | yes (when nested) | **wire model id** sent on the API `model` field and used by `/model` selection |
| `models.<id>.name` | no | **display label only** in `/model` (never sent on the wire; default: id or models.dev name) |
| `models.<id>.limit.context` / `.output` | no | token ceilings; overlay wins over models.dev when set (>0) |
| `models.<id>.options` | no | opaque bag (unsupported keys ignored; must not change the wire id) |
| `models.<id>.variants` | no | named effort presets; `reasoningEffort`/`effort` map onto `/effort` |
| `options.headers` | no | extra HTTP headers (values may use env refs) |

#### Wire id vs display name

Nested `models` object **keys** are the ids strike selects and sends on the wire
(`{"model":"<key>"}`). The optional `name` field is a UI label only. Example:
`"gpt-5.5": { "name": "GPT-5.5" }` lists as “GPT-5.5” but requests `gpt-5.5`.
Variants and options never rewrite the wire id.

#### Builtin endpoint overlay (anthropic / openai / …)

Defining `"anthropic": { "options": { "baseURL", "apiKey" } }` (with or without
`models`) keeps the builtin provider registered, routes HTTP to the custom
endpoint, resolves the pinned apiKey env, and still lists models.dev when
`models` is omitted. Same for other credential builtins (openai chat-completions
path when baseURL/apiKey is set — not the ChatGPT OAuth backend).

#### baseURL path join (OpenCode parity)

| Wire | `options.baseURL` example | Request path |
|---|---|---|
| anthropic | `https://proxy.example/v1` (OpenCode) | `…/v1/messages` |
| anthropic | `https://proxy.example` (origin-only) | `…/v1/messages` |
| openai (chat) | `https://proxy.example/v1` | `…/v1/chat/completions` |
| responses (`@ai-sdk/openai`) | `https://proxy.example/v1` | `…/v1/responses` |

Do **not** put `/messages` or `/chat/completions` in `baseURL` unless the whole
URL is already the final endpoint (strike leaves a trailing `/messages` or
`/responses` alone).

#### models.dev / catalog merge

| Situation | Behavior |
|---|---|
| Builtin (openai, anthropic, …) with models.dev data | `/model` lists **catalog** models by default |
| Builtin with only `options` (no `models`) | endpoint overlay applied; **full catalog** unchanged |
| Config omits `models` or `models` is empty | full catalog unchanged |
| Config nested/flat models on a **builtin** | **merge/overlay** by id: config wins name/limits/variants; catalog-only ids still appear |
| Config nested/flat models on a **custom** provider | config list is the full `/model` list (no models.dev); map keys are wire ids |
| Config sets limits for a catalog id | config wins for those fields; other catalog metadata kept |

You never need to paste an entire upstream catalog into `providers.jsonc` just to set one variant, context limit, or proxy baseURL.

#### Default model precedence

1. `config.model` / `--model` when set  
2. Custom provider: first configured model id (`models` array order, or sorted nested keys)  
3. Built-in pin via `DefaultModel(provider)` (e.g. openai → `gpt-5.5`)  
4. Otherwise unset (freeform `/model <id>`)

#### Variants → effort

Variant bags may include `reasoningEffort` or `effort` (`off`\|`low`\|`medium`\|`high`\|`xhigh`\|`max`). Selecting a variant (from the `/effort` picker when the active model has variants, or `/effort <variant-id>`) sets the session effort dial; adapters map that onto wire fields (`reasoning_effort`, Anthropic `output_config.effort`, …). Other variant keys are ignored for now.

### Config `providers` array (legacy)

```json
{
  "providers": [
    {
      "name": "acme",
      "baseURL": "https://api.example.com/v1",
      "api": "openai",
      "apiKeyEnv": "ACME_API_KEY",
      "models": ["acme-latest"],
      "headers": { "X-Custom": "optional" }
    }
  ]
}
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | lowercase slug (`[a-z][a-z0-9_-]{0,63}`); not `anthropic`/`openai`/`xai`/`gemini`/`kimi`/`deepseek`/`echo` |
| `baseURL` | yes | absolute URL or env ref template |
| `api` | yes | wire dialect: `openai` (chat), `responses`, or `anthropic` |
| `apiKeyEnv` | no | env var name (or `{env:NAME}` / `$NAME`) checked before the auth store |
| `models` | no | flat `[]string` ids listed in `/model`; first is the default when unset (rich nested models use `providers.jsonc`) |
| `headers` | no | extra HTTP headers on every request (values may use env refs) |

**Migration:** existing `models: ["a","b"]` keeps working everywhere. Prefer
`providers.jsonc` nested objects when you need display names, limits, or
variants. Built-in overlays go under the builtin key in `providers.jsonc`
(not in the `providers` array — builtin names remain reserved there).

**Env interpolation:** `{env:NAME}`, `$NAME`, and `${NAME}` expand from the
process environment (vars exported to the strike process, e.g. via bashrc).

**TUI:** `/settings` CRUD and `/provider` → “Add custom provider…”. Custom
names appear in `/provider` like built-ins. **Logout** (`ctrl+x` or
`/auth logout <name>`) of a custom provider **deletes** its definition from
config/providers.jsonc and clears credentials; `/settings` `d` does the same.
Built-in logout only clears credentials.

## Surface presentation (`vimMode`, `nanoMode`, `mdReadMode`)

Editor and markdown-reader surfaces share a presentation vocabulary:
**embedded** (right-pane chrome) vs **modal** (large centered overlay with
background scrim). Prefer those names for new config; legacy aliases remain.

### Embedded editor (`vimMode`)

`/vim [path|@path[:line]]` opens a file in an editor resolved from `$VISUAL` →
`$EDITOR` → nvim/vim/vi/nano on `PATH`. `vimMode` selects how:

| Value | Aliases | Behavior |
|---|---|---|
| `pane` (default) | `embedded` | embed the editor in the right-pane `editor` window (PTY) |
| `overlay` | `modal` | large modal popout with background scrim |
| `takeover` | — | full-screen handoff via `tea.ExecProcess` |

Unknown values are ignored at load time. GUI `$EDITOR` values always take
over the terminal regardless of `vimMode`. Leave the embedded/modal editor
with `ctrl+g`.

### Nano (`nanoMode`)

`/nano [path|@path[:line]]` opens **nano** specifically (does not use `$VISUAL`/
`$EDITOR`). `nanoMode` uses the same values and aliases as `vimMode`
(default `pane`/`embedded`). Missing `nano` on `PATH` shows a clear error.
Leave the embedded/modal editor with `ctrl+g`.

### Markdown reader (`mdReadMode`)

`/md-read <path|@path>` opens a markdown file. `mdReadMode` selects how:

| Value | Aliases | Behavior |
|---|---|---|
| `embedded` (default) | `pane` | right-pane `markdown` window |
| `modal` | `overlay` | large modal popout with background scrim |

Unknown values are ignored at load time. Dismiss the modal with `esc`, `q`,
or `ctrl+g`. Preference is read from config at session start (global then
project merge).

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
