# Config

`~/.strike/config` (global) merged with `./.strike/config` (project), both
JSON.

**Symlinks:** `~/.strike` and `<project>/.strike` may be directory symlinks
(state lives elsewhere). Strike resolves them before opening history/memory/
issues and before writing config. A file symlink at `~/.strike/config` (for
example stow/dotfiles) is preserved on save — the referent is updated, not
replaced by a plain file.

## First-time onboarding state

Global (not per-project) acknowledgement lives at
`~/.strike/onboarding.json`:

```json
{
  "version": 1,
  "acknowledged": true
}
```

Interactive TUI launches auto-open `/ftue` while `acknowledged` is false or
the file is missing on a clean install. Finish or dismiss (esc) sets
`acknowledged: true` atomically. An interrupted session that never finishes
or dismisses leaves the file unacknowledged so the wizard can reopen next
launch. Established installs (existing session logs or real provider
credentials) migrate to acknowledged without showing the modal. Precreated
empty `~/.strike` directories alone do not suppress first launch.
`strike exec`, `auth`, `serve`, `version`, and `upgrade` neither display nor
write this file. Manual `/ftue` remains available after acknowledgement.

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-5",
  "effort": "high",
  "defaultAgent": "build",
  "leanCode": "lite",
  "deferTools": "off",
  "theme": "strike",
  "vimMode": "pane",
  "nanoMode": "pane",
  "mdReadMode": "embedded",
  "notify": "unfocused-only",
  "permissionMode": "default",
  "sandbox": "workspace-write",
  "permissionAutoApproveSeconds": 0,
  "permissionAutoApproveExclude": ["bash"],
  "compactionStrategy": "trim",
  "compactionModel": "",
  "compactionThreshold": 0.70,
  "compactionBuffer": 4096,
  "keepUserTurns": 2,
  "pruneProtectTokens": 40000,
  "pruneMinimumTokens": 20000,
  "pruneKeepUserTurns": 2,
  "pruneProtectTools": [],
  "session": {
    "worktree": "off",
    "worktreeCleanup": "keep"
  },
  "scheduler": {
    "presets": ["cargo", "npm"],
    "limits": {
      "process": 8,
      "build": 2,
      "test": 4,
      "model": 3,
      "container": 1
    },
    "commands": [
      { "pattern": "go test *", "class": "test" },
      { "pattern": "go *", "class": "build" },
      { "pattern": "make *", "class": "build" }
    ]
  },
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" },
    { "permission": "write", "pattern": "**/*.env", "action": "deny" }
  ]
}
```

Rules concatenate across layers; the last matching rule wins, so project
config overrides global, and session "always" grants override both.

**Two-dial model (Codex mental model):**

| Dial | Config / CLI | Controls |
|---|---|---|
| **sandbox** | `sandbox`, `--sandbox` | What OS isolation makes *possible* for bash (`off` \| `read-only` \| `workspace-write`) |
| **permissionMode** | `permissionMode`, `/mode`, Shift+Tab | *When* the agent is asked before a tool runs |

They are independent. Turning off permission prompts (`yolo`) does not disable
the OS sandbox; setting `sandbox: off` does not skip permission asks.

**OS sandbox dial:** `sandbox` is `off` | `read-only` | `workspace-write`
(default **`workspace-write`**). Applies to the bash tool (and composer `!`
shell) via Linux `bwrap` / macOS `sandbox-exec`. When the backend is missing
or blocked, bash degrades to unsandboxed with a one-shot startup warning
(unless `sandbox` is `off`). Override per invocation with `--sandbox <mode>`.
Inspect the effective policy with `/sandbox`; `/sandbox explain` prints the
generated OS profile (bwrap flags or seatbelt SBPL) compiled from permission
rules.

**Permission → sandbox profile:** hard `write`/`edit` deny rules are compiled
into OS filesystem denials inside the bash sandbox (globs become seatbelt
regexes and, when paths exist, bwrap `--ro-bind` remounts). A deny on
`write`/`edit` `*` (including plan mode) suppresses the writable workspace
bind. Network inside the sandbox stays **on** by default (so bash can run
`gh`, `git`, package managers, etc.). It turns **off** only when both
`webfetch` and `mcp` are hard-**deny** on `*` (patterned rules do not flip
full-network posture). Host/CIDR allowlists are tracked in #527. Ask/yolo
posture does not widen the OS profile. Composer `!` uses the config-layer
compile; agent bash uses live layers (agent/phase/session).

**Yolo + sandbox off:** `permissionMode: yolo` (or a resumed session in yolo)
combined with `sandbox: off` **refuses to start** unless you pass `--i-know`.
Mid-session `/mode yolo` is also rejected while sandbox is off without that
startup override. This is the only supported way to run with neither OS
isolation nor permission prompts.

**Not a security boundary alone:** permission rules and modes (including
`yolo` / `--auto` / `--dangerously-skip-permissions`) only control whether the
agent is *asked* before a tool runs. Prefer keeping `sandbox` at
`workspace-write` (or `read-only`) so OS isolation still applies. The bash
tool also applies a separate best-effort static path guard on a small set of
destructive command forms; that guard is incomplete and must not be treated as
isolation. Linux glob denials expand existing paths at compile time (new files
matching a deny glob are covered on the next compile / seatbelt regex on
macOS).

**Permission mode dial:** `permissionMode` sets the default tool-permission
posture for **new** sessions: `default` | `plan` | `soft-approve` |
`accept-edits` | `yolo` (see [usage.md](usage.md)). Session changes via
Shift+Tab or `/mode` persist in the session JSONL, not back into this file.
Distinct from `/autonomy` (workflow exit gates) and from `sandbox` (OS
isolation).

**Lean code:** `leanCode` is `off` | `lite` (default) | `full`. Injects
agent-scoped efficiency guidance into the system prompt (strict ladder for
build/general/debugger; softer scaling-aware lean for plan/orchestrator;
none for explore/reviewer/tester/validator/commit). Inspired by
[ponytail](https://github.com/DietrichGebert/ponytail) (clean-room wording).
Details: [agents-skills.md](agents-skills.md#lean-code-ponytail-lite).

**Deferred tool schemas:** `deferTools` is `on` | `off` (default off). When
`on`, non-core tools are omitted from the provider `tools[]` array until
`toolsearch` discovers them (or the model calls them by name). Core coding
tools stay always available: `read`/`glob`/`grep`/`edit`/`write`/
`apply_patch`/`bash`, the `task*` family, `toolsearch`, `question`, and plan
workflow tools. Deferred surface includes optional built-ins (`webfetch`,
todo/memory/issue, `sleep`, `skill`, `notebook_edit`, …) and all `mcp_*`
tools. Discovery lives on the process registry: matches from `toolsearch`
load full schemas on the **next** model request (including the next
iteration of the same turn’s tool loop). Tools already present as assistant
tool calls in history are re-promoted on each stream (so `--continue` keeps
schemas for tools used earlier). Set `"deferTools": "on"` in global or
project config to enable.

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

## Scheduler (in-process resource limits)

`scheduler` bounds concurrent agent work **inside one Strike OS process**.
Separate `strike` programs do **not** coordinate leases or share capacity —
each process applies its own effective limits independently.

| Field | Meaning |
|---|---|
| `presets` | Ordered list of shipped build-system preset IDs (see below) |
| `limits` | Map of pool name → positive integer capacity |
| `commands` | Ordered list of `{ "pattern", "class" }` classification rules |

**Pools:** `process`, `build`, `test`, `model`, `container`. Omitted keys keep
the lower config layer's value; when no layer sets a pool, that pool is
**unlimited** (same as today's default). An explicit `0` or negative capacity
**fails config load** with the file path — use omission for unlimited, not
zero.

**Layering:** project `limits` override global **per pool**. `presets` and
`commands` concatenate (global then project; duplicate preset IDs keep the
first). Malformed patterns, unknown classes, or unknown/duplicate preset IDs
in one file fail load before the engine starts and name the source file and
index.

**Presets:** versioned bundles for common resource-heavy tools. At compile
time each selected ID expands into ordinary suggested `limits` and `commands`
(no second runtime matcher). Expansion order follows the shipped catalog
order among the selected IDs (not the order written in config). Then:

1. User/project `limits` overlay preset-suggested capacities per pool.
2. User/project `commands` append after expanded preset rules (last-match-wins,
   so a later user rule can reclassify a preset pattern).

Shipped preset IDs: `cmake`, `ninja`, `gradle`, `bazel`, `maven`, `cargo`,
`npm` (covers npm/yarn/pnpm/bun). Each has a stable ID, display name,
rationale, default class, and inspectable generated rules (see
`scheduler.Catalog` / host `SchedulerPresets`). Expanded rule provenance is
`preset:<id>@v<version>` in `Effective.Report()`. The `/ftue` setup wizard can
checkbox-select these presets and write the global `presets` list atomically
(custom `limits`/`commands` are preserved; skip leaves config unchanged).

**Command classification:** each rule's `pattern` is a full-string glob over
the submitted shell command (`*` = any run of runes, `?` = one rune, `\`
escapes the next byte). Matching is case-sensitive. `class` is `general` |
`build` | `test`. Evaluation is **last-match-wins**: every matching rule is
considered in order; the last match's class wins. When nothing matches, the
class is `general`. Multiple matches are therefore resolved by rule order
(project rules append after global, so a later project rule can reclassify).

Admission wiring uses the compiled policy:

- **Model streams** (ordinary turns, child turns, concurrent roots, harness
  calls, and compaction summarize) acquire the `model` pool for the duration of
  each `Provider.Stream` attempt. The lease is released when the stream is
  fully drained, before retry backoff, and reacquired fairly on the next
  attempt. Omitted/`unlimited` model capacity preserves pre-scheduler behavior.
- **Bash** acquires `process` after permission approval and before process
  start (command timeout begins after admission). `build` / `test` classes
  acquire those pools in addition via the scheduler's multi-pool path.
  Omitted limits stay unlimited (no wait — same as pre-scheduler behavior).

**Queue lifecycle (protocol / session JSONL):** when a caller **blocks** on
capacity, the engine emits `scheduler.queued` with correlation, a stable
`requestId`, the constrained `pools`, and a short `label` (`model`, `bash`,
`bash:build`, …). Grant then emits `scheduler.admitted` (with `waitMs`); cancel
or scheduler close emits `scheduler.canceled` (`reason` `canceled`|`closed`).
Immediate grants (unlimited pools or free capacity) emit **no** queue events,
so default sessions stay quiet. After `canceled` for a `requestId`, `admitted`
never follows. Exact queue positions are **not** on the wire — FIFO is internal
and may change; UIs show the pool and label only. Replay reconstructs
queued→admitted or queued→canceled so waiting roots/children are not mistaken
for idle. Task status, team roster, and the activity/agents panes project these
events without importing scheduler internals.

Inspect the compiled policy via `Config.SchedulerEffective()` /
`Effective.Report()`.

Example — cargo preset plus global caps with a project test override:

```json
// ~/.strike/config
{
  "scheduler": {
    "presets": ["cargo"],
    "limits": { "process": 8, "build": 2 },
    "commands": [
      { "pattern": "go *", "class": "build" }
    ]
  }
}

// ./.strike/config
{
  "scheduler": {
    "limits": { "build": 4, "test": 2 },
    "commands": [
      { "pattern": "go test *", "class": "test" },
      { "pattern": "cargo test *", "class": "general" }
    ]
  }
}
```

Effective: `process=8`, `build=4` (project overlays preset/global), `test=2`
(from project; cargo preset also suggests test capacity when not overridden),
other pools unlimited. `cargo build` → `build` (preset); `cargo test --lib` →
`general` (project rule wins over preset); `go test ./...` → `test`;
`go build .` → `build`.

## Session worktrees

When concurrent root sessions would otherwise share one working tree, strike
can bind each session's tool CWD to its own `git worktree` under
`<repo>/.strike/worktrees/<session-id>/` (gitignored via `*/worktrees`).

| `session.worktree` | Behavior |
|---|---|
| `off` (default) | launch cwd; no isolation |
| `auto` | worktree when a second root session starts in-process |
| `always` | every new root session gets a worktree (git repos only) |

| `session.worktreeCleanup` | Behavior |
|---|---|
| `keep` (default) | leave the worktree and branch after session close |
| `delete` | `git worktree remove` + delete the branch on close |

CLI: `strike --worktree` forces a worktree for that invocation (same as always
for one session). Non-git directories soft-fail: the app launches on the launch
cwd and shows a dismissible modal (TUI) or stderr line (exec) explaining that
no git repository was detected. Other `git worktree add` failures still return
a clear error and do not leave a half-bound session. Project-scoped state
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
mdReadMode, permissionMode, and effort (plus a read-only view of
provider/model/agent). Changes write `~/.strike/config` and apply theme/editor
presentation to the current session immediately.

## Theme

`theme` is a color-theme id: bundled JSON themes plus files under
`~/.strike/themes` and `./.strike/themes`. Empty means the stock `strike`
palette. In the TUI, bare `/theme` opens a picker; `/theme <id>` applies one;
`/theme dark|light|auto` only adjusts session appearance (forced or terminal
background from Bubble Tea detection), not the color-theme id.

## Keybinds

Remap app-level chords without recompiling. Ids match the in-app cheatsheet
(`/keys` / `f1`). Prefer a dedicated file (JSONC comments allowed); the
`keybinds` object in config still works:

```jsonc
// ~/.strike/keybinds.jsonc or ./.strike/keybinds.jsonc
// Flat map (preferred). Wrapped {"keybinds": {...}} is also accepted.
{
  "nav.jump-bottom": "ctrl+b",
  "global.palette": "ctrl+k",
  "composer.newline": ["ctrl+j", "alt+enter"],
  "nav.window-next": "ctrl+o",
  "nav.window-prev": "ctrl+p",
  "nav.group-next": "ctrl+shift+o",
  "nav.group-prev": "ctrl+shift+p",
  "nav.tool-expand": "alt+enter"
}
```

Legacy shape in `~/.strike/config` or `./.strike/config`:

```json
{
  "keybinds": {
    "nav.jump-bottom": "ctrl+b",
    "global.palette": "ctrl+k"
  }
}
```

Layers merge last-wins per id:

`defaults → ~/.strike/config → ~/.strike/keybinds.jsonc → ./.strike/config → ./.strike/keybinds.jsonc`

(`.json` is accepted as well as `.jsonc`. In the same root, the dedicated file
overrides the config object.) Unknown binding ids and invalid/empty chords
fail config load with a clear error. Critical `global.quit` and
`global.interrupt` cannot be cleared.

Shared chords across different actions are allowed (context-specific routing
in the TUI decides the winner — e.g. default `alt+enter` is newline while
typing and tool expand only when the composer is empty). `/keys` shows the
effective map; `/keys reset` restores built-in defaults for the current
session only — remove remaps from `keybinds.jsonc` / the config `keybinds`
object to persist defaults.

List/permission modal conventions (`lists.*`, `perm.*`) and agents-pane local
controls (`agents.*`) are not remappable.

## Language servers (LSP)

Configure stdio language servers so file tool mutations (`write` / `edit` /
`apply_patch` / `notebook_edit`) drive `textDocument/didOpen` /
`didChange` / `didClose`, and `publishDiagnostics` notifications are collected
per URI. A dead language server degrades to no diagnostics and never takes
down the session (same crash isolation as MCP).

```json
// ~/.strike/config or ./.strike/config
{
  "lsp": {
    "servers": {
      "go": {
        "command": "gopls",
        "extensions": [".go"]
      },
      "typescript": {
        "command": "typescript-language-server",
        "args": ["--stdio"],
        "extensions": [".ts", ".tsx", ".js", ".jsx"]
      }
    }
  }
}
```

| Field | Required | Notes |
|---|---|---|
| `command` | yes | Executable on `PATH` or absolute path |
| `args` | no | Extra argv after command |
| `env` | no | Env overlay for the subprocess (never logged) |
| `extensions` | yes | File extensions this server owns (with or without leading `.`). First server claiming an extension wins. Servers with no extensions are skipped. |

**Layering:** when a config layer sets `lsp.servers` (including `{}`), it
**replaces** the previous layer's server map entirely (same as MCP). Omitted
`lsp` leaves the lower layer unchanged.

Diagnostics injection into tool results and the `/lsp` UI are separate
follow-ups (epic E2.2 / E2.3). This client only collects diagnostics in-process.

## MCP servers (stdio + HTTP)

Connect external [Model Context Protocol](https://modelcontextprotocol.io)
servers so their tools appear in the model registry as `mcp_<server>_<tool>`.
Supported transports: **stdio** (local subprocess) and **streamable HTTP**
(remote endpoint; JSON or SSE responses).

Prefer **`mcp.jsonc`** (or `mcp.json`) for server definitions. The legacy
`mcp` object in config still works. Layers merge last-wins by file:

`defaults → ~/.strike/config → ~/.strike/mcp.jsonc → ./.strike/config → ./.strike/mcp.jsonc`

(`.json` is accepted as well as `.jsonc`.) When a layer sets servers
(including `{}`), it **replaces** the previous layer's server map. Omitted
`mcp` / missing mcp file leaves the lower layer unchanged.

### `mcp.jsonc` (preferred)

Bare server map or wrapped `servers` object; JSONC comments allowed:

```jsonc
// ~/.strike/mcp.jsonc or ./.strike/mcp.jsonc
{
  "github": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-github"],
    "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "…" }
  },
  "remote": {
    "type": "http",
    "url": "https://mcp.example.com/mcp",
    "headers": { "Authorization": "Bearer …" }
  }
}
```

Equivalent wrapped form:

```jsonc
{
  "servers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"]
    }
  }
}
```

### Legacy: `mcp` in config

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

| Field | Required | Notes |
|---|---|---|
| `servers.<name>` | yes | short letter-led slug (`[A-Za-z][A-Za-z0-9_-]*`) |
| `type` | no | `stdio` (default) or `http` (`sse` is accepted as an alias for `http`) |
| `command` | stdio | executable on `PATH` or absolute path |
| `args` | no | argv after the command |
| `env` | no | stdio env overlay; **never logged** |
| `url` | http | MCP endpoint URL (if set without `type`, transport is `http`) |
| `headers` | no | HTTP request headers (e.g. `Authorization`); **never logged or shown in `/mcp`** |

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
HTTP may send secrets via `headers`. Prefer global `~/.strike/mcp.jsonc` for
shared servers; review `command`/`args`/`env`/`url`/`headers` before trusting
a project's `.strike/mcp.jsonc`.

## Custom providers

Add OpenAI-compatible (chat completions) or Anthropic-compatible (messages)
endpoints via **`providers.jsonc`** (preferred) or the `providers` array in
config. Layers merge last-wins by name:

`defaults → ~/.strike/config → ~/.strike/providers.jsonc → ./.strike/config → ./.strike/providers.jsonc`

(`.json` is accepted as well as `.jsonc`.) Credentials never live in these
files — use env refs and/or `/auth` / the auth store.

### Disable default (builtin) providers

Hide stock catalog providers (`anthropic`, `openai`, `xai`, `google`, `kimi`,
`deepseek`, `echo`) so only custom endpoints appear in `/provider`, `/auth`,
and model pickers. The shipped alias `gemini` is accepted on
`disable-default-gemini` and routes to `google`. Same keys work in
**`providers.jsonc`** or config JSON; later layers win (project overrides
global; providers.jsonc overrides the config file in the same root).

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
| map key | yes | provider id (lowercased slug). Built-ins (`anthropic`/`openai`/`xai`/`google`/`kimi`/`deepseek`/`echo`) stay builtins: options → **endpoint overlay**, models → **catalog overlay**. The shipped alias `gemini` is accepted and canonicalized to `google`. Other keys are custom providers. |
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
| `name` | yes | lowercase slug (`[a-z][a-z0-9_-]{0,63}`); not `anthropic`/`openai`/`xai`/`google`/`gemini`/`kimi`/`deepseek`/`echo` (`gemini` is reserved as an alias of `google`) |
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
history while keeping a recent tail. Continuous tool-result prune
(`internal/engine/prune.go`) blanks older tool bodies under that ceiling;
threshold compaction is the coarser whole-history rewrite.

| Field | Values | Default |
|---|---|---|
| `compactionStrategy` | `trim` (drop older turns) or `summarize` (model-authored summary of dropped turns) | `trim` |
| `compactionModel` | optional model id for the summarize call (same provider as the session) | session model |
| `compactionThreshold` | occupancy fraction of the known context window that triggers auto-compact before a Stream; `>=1` disables threshold compaction; omit/`0` uses the engine default | `0.70` |
| `compactionBuffer` | extra token headroom reserved with `MaxTokens` so threshold compaction fires before hard exhaustion; omit/`0` uses the engine default | `4096` |
| `keepUserTurns` | trailing real user turns preserved when compacting (compact markers do not count); omit/`0` uses the engine default | `2` |
| `pruneProtectTokens` | recent tool-output tokens kept intact while walking history backward during continuous prune; omit/`0` uses the engine default; negatives clamp to `0` | `40000` |
| `pruneMinimumTokens` | minimum estimated tokens that must be freed before prune mutates history (avoids thrash); omit/`0` uses the engine default; negatives clamp to `0` | `20000` |
| `pruneKeepUserTurns` | real user turns whose tool results stay complete during prune (compact markers do not count); omit/`0` uses the engine default; negatives clamp to `0` | `2` |
| `pruneProtectTools` | extra tool names whose results are never blanked (merged with built-in `skill`); names lowercased/deduped; omit/empty adds none | `[]` (+ built-in `skill`) |

Recommended ranges: threshold `0.60`–`0.85` (lower = earlier pressure response;
too low thrash-compacts short sessions), buffer `1024`–`8192`, keep turns
`1`–`4`. For prune, lower `pruneProtectTokens` / `pruneMinimumTokens` on
MCP-heavy sessions (tighter reclaim); raise minimum on short interactive
sessions to avoid thrash. Overflow recovery still compacts on context-length
provider errors regardless of threshold.

On summarize failure the engine falls back to trim and emits a notice. The
summary path never re-runs tools.

## Reasoning effort

`/effort` sets how much internal reasoning the model spends before answering.
The active level shows on the top status bar once set. Persist a default with
`ctrl+d` in the effort picker, `/settings` → Defaults → Effort, config
`"effort"`, or `--effort`. The ladder is normalized across vendors and each
adapter maps it to its own wire fields — Anthropic to adaptive thinking plus
`output_config.effort`, the OpenAI family to a `reasoning_effort` string. With
no level set, strike sends no reasoning fields at all and each provider's own
default applies. The `task` tool accepts optional `effort` so a parent can pin
a child dial independently of the UI default.

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
