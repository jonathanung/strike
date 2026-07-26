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
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" },
    { "permission": "write", "pattern": "**/*.env", "action": "deny" }
  ]
}
```

Rules concatenate across layers; the last matching rule wins, so project
config overrides global, and session "always" grants override both.

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
