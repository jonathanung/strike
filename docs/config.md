# Config

`~/.strike/config` (global) merged with `./.strike/config` (project), both
JSON:

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-5",
  "effort": "high",
  "defaultAgent": "build",
  "theme": "strike",
  "vimMode": "pane",
  "permissionAutoApproveSeconds": 10,
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

**Permission auto-approve (yolo-lite):** when `permissionAutoApproveSeconds`
is a positive integer (clamped to 1–60), the permission modal counts down and
submits **allow once** at zero. Esc, reject, or any explicit choice cancels
the timer. Disabled by default (`0` / omitted). Names in
`permissionAutoApproveExclude` (case-insensitive) never auto-approve.

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

List/permission modal conventions (`lists.*`, `perm.*`) are not remappable.

## Custom providers

Add OpenAI-compatible (chat completions) or Anthropic-compatible (messages)
endpoints via the `providers` array (global and project layers merge; same
`name` in project replaces global). Credentials never live here — use
`apiKeyEnv` and/or `/auth` / the auth store.

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
| `name` | yes | lowercase slug (`[a-z][a-z0-9_-]{0,63}`); not `anthropic`/`openai`/`xai`/`echo` |
| `baseURL` | yes | absolute `http`/`https` URL |
| `api` | yes | wire dialect: `openai` or `anthropic` |
| `apiKeyEnv` | no | env var name checked before the auth store |
| `models` | no | listed in `/model`; first is the default when unset |
| `headers` | no | extra HTTP headers on every request |

In the TUI, `/settings` manages the same list (CRUD persists to
`~/.strike/config`). Custom names appear in `/provider` like built-ins.

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

Agents, skills, and workflows: [agents-skills.md](agents-skills.md).
