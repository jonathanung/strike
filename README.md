# strike-cli

An agentic coding TUI in Go/BubbleTea. Architecture informed by deep-dives
into [opencode](https://github.com/sst/opencode) and
[codex](https://github.com/openai/codex) — see [PLAN.md](PLAN.md) for the
full design rationale and roadmap.

## Build & run

Requires Go 1.26+ (`brew install go`).

```sh
make setup          # one-time: creates ~/.strike (config + example
                    # plan agent and commit skill); never overwrites
make build          # builds ./strike        (or: go build -o strike ./cmd/strike)
make run-echo       # offline dev loop — no API key needed. Type
                    # `run <command>` to exercise tool dispatch and the
                    # permission prompt.
make run            # real agent with your configured provider
make test           # go test ./...
make vet            # go vet ./...
```

strike launches without any provider configured. Pick one inside the TUI:

```
/provider                      # centered picker: providers + auth status;
                               # selecting an unauthenticated one starts its
                               # login and switches once it succeeds
/provider anthropic            # direct switch (or openai, xai, echo)
/provider openai gpt-5.5       # optional explicit model
/model                         # centered model picker for the current
                               # provider (live models.dev catalog, cached
                               # 24h; type to filter)
/model grok-4.5                # direct switch on the current provider
/effort                        # centered picker for reasoning effort
/effort xhigh                  # off | low | medium | high | xhigh | max
/fast                          # toggle OpenAI priority tier (~2×, lower
                               # latency). Sticky session preference; no-op
                               # on Anthropic, xAI, ChatGPT subscription, or
                               # models without a fast mode. /fast on|off
/auth                          # same picker as /provider
/auth openai                   # OAuth login in the browser (async — the TUI
                               # keeps working; result shows in the notice line)
/auth xai device               # RFC 8628 device flow for headless machines
/auth anthropic                # masked API-key input (also: /auth <p> key)
/auth status                   # anthropic: none · openai: oauth+key · …
/auth logout <provider>
/help                          # list commands
```

Submitting a prompt before selecting shows "No model selected" in the
notice line above the composer (your prompt stays in the input). Talking to
a real provider needs credentials (see Auth below):

```sh
export ANTHROPIC_API_KEY=sk-ant-…   # or: strike auth login anthropic
./strike                            # tries the config default silently;
                                    # otherwise select with /provider
./strike --provider <provider>       # anthropic, openai, xai, or echo;
                                    # fails loudly if no credentials
./strike --model <model>             # pre-select a model
./strike --effort <level>            # off, low, medium, high, xhigh, or max
```

`--provider <provider>`, `--model <model>`, and `--effort <level>` may be
combined. To bypass permission checks for one invocation, use
`--dangerously-skip-permissions`.
**Warning:** this allows all tool calls without asks or denies. It applies
only to that process invocation, does not persist config or permission rules,
and is visibly marked as dangerous mode in the TUI. Run `strike --help` for
the authoritative CLI usage and option list.

Defaults when a provider is chosen without a model: `claude-sonnet-5`,
`gpt-5.5`, `grok-4.5`.

If you use the `strike` shell alias (points at this repo's built binary),
re-run `make build` after pulling changes to refresh it.

### UI

The screen has a full-width header, footer hints, and danger banner when
needed. Its left pane is one aggregate stack: `session` transcript, reserved
notice line, slash-command completion, and `prompt ❯` composer. The right slot
hosts one active session pane (`context` setup or `activity` tools/tips).
Vim-style pane keys: `ctrl+h` / `ctrl+l` focus the left or right pane; `ctrl+j`
/ `ctrl+k` cycle the active right-pane window next/previous. `ctrl+p` opens the
command palette; `f1` (or `/keys`) opens a filterable keybind cheatsheet. Enter
sends; Shift+Enter (or Alt+Enter) inserts a newline. Pickers, the command
palette, and permission prompts render as centered dialogs in the same panel
style.

The default split appears at 93 columns and above, with a minimum 60-column
left pane, one-column gutter, and 32-column right pane. At 92 columns and
below, only the active pane fills the full width. For a custom gutter of width
`g`, the split threshold is `60 + g + 32`. Below 60 columns or 20 rows panels
drop their borders ("compact mode") instead of clipping or garbling. This is
only pane infrastructure: it has no file, editor, or markdown content, and no
window close state or plugins.

A fresh session with an empty transcript shows a dashboard of fixed-height
cards in place of a blank viewport; the header owns the Strike brand. The
dashboard always shows keybindings. It shows get-started provider rows only
when no provider is selected or the selected provider needs authentication,
with provider rows bounded to fit; agents and skills only when valid configured
entries exist; and recent prompts only when prompt history exists. It repacks
to fit the terminal on resize and collapses to a single column when narrow.

## Auth

Credentials live in `~/.strike/auth.json` (0600). Environment
variables (`ANTHROPIC_API_KEY` / `OPENAI_API_KEY` / `XAI_API_KEY`) always
take precedence over stored credentials.

**OpenAI billing routing**: a ChatGPT OAuth login routes requests to the
ChatGPT backend (`chatgpt.com/backend-api/codex`, Responses API, streamed) —
billed to your ChatGPT Plus/Pro subscription, using the access token +
`ChatGPT-Account-Id` header. An explicit API key (`OPENAI_API_KEY` or
`/auth openai key`) routes to `api.openai.com` instead — billed to your
platform API account. Subscription mode supports the Codex model set
(`gpt-5.5`, `gpt-5.4`, …), not `-pro` models.

Log in either inside the TUI (`/auth <provider>`, see above) or from the
shell:

```sh
strike auth login openai            # OAuth "Sign in with ChatGPT" (browser);
                                    # also exchanges the id_token for a real
                                    # API key usable on api.openai.com
strike auth login xai               # xAI Grok OAuth (browser, PKCE)
strike auth login xai --device      # RFC 8628 device flow for headless/SSH
strike auth login <provider> --api-key   # paste a key instead (any provider)
strike auth status
strike auth logout <provider>
```

Both OAuth integrations reuse public CLI client registrations (Codex CLI's
for OpenAI, Grok-CLI's for xAI — the same approach opencode ships), so the
loopback callback ports are fixed: `localhost:1455` for OpenAI,
`127.0.0.1:56121` for xAI. OAuth access tokens auto-refresh ~2 minutes
before expiry, and rotated refresh tokens are persisted.

Provider selection happens in-app with `/provider`; `--provider` on the
command line just pre-selects (and validates credentials eagerly).

Keys: `enter` send · `shift+enter` newline (alt+enter fallback) ·
`esc` interrupt turn / reject permission · `1/2/3` or `←/→ + enter`
answer permission prompts · `pgup/pgdn` scroll · `ctrl+c` quit.

## Architecture in one paragraph

The TUI and the agent engine are separate halves connected only by
`internal/protocol` — frontends submit **Ops** (`UserInput`,
`PermissionReply`, `Interrupt`), the engine emits **Events** (`TextDelta`,
`ToolCallBegin/End`, `PermissionAsked`, `TurnCompleted`, …) — codex's SQ/EQ
pattern, in-process over Go channels for now. The event stream *is* the
transcript: every session is persisted as a JSONL event log
(`~/.strike/sessions/`). Everything else the TUI needs from its host
process — credentials, the model catalog, saved defaults, prompt history,
agent/skill listings — arrives through a second, narrower seam,
`internal/host` (implemented by `internal/host/local`); the TUI never
imports `internal/auth`, `config`, `models`, or `history` directly, and a
boundary test enforces it, so the backend can add a host service without
touching the UI and the UI can be developed against fakes. Tools return
`{Title, Output, Metadata}` separating model-facing text from UI rendering
data (opencode's contract). Permissions are ordered allow/ask/deny rulesets
with last-match-wins evaluation; an "ask" suspends the tool goroutine until
the user answers, and rejections carry feedback back to the model. See
[ARCHITECTURE.md](ARCHITECTURE.md) for the full package map and dependency
rules.

## Config

`~/.strike/config` (global) merged with `./.strike/config` (project), both
JSON:

```json
{
  "provider": "anthropic",
  "model": "claude-sonnet-5",
  "effort": "high",
  "defaultAgent": "build",
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" },
    { "permission": "write", "pattern": "**/*.env", "action": "deny" }
  ]
}
```

Rules concatenate across layers; the last matching rule wins, so project
config overrides global, and session "always" grants override both.

**ctrl+d saves defaults**: on the main screen it persists the current
provider/model/agent/effort to `~/.strike/config`; in the provider picker it
saves the highlighted provider; in the model picker it saves provider + model;
in the effort picker it saves the highlighted level.

### Reasoning effort

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

## Agents & skills

Both `.strike` roots (global and project) can hold `agents/` and `skills/`
folders of markdown files; project files override same-named global ones.

**Agents** (`agents/*.md`) are personas — a system prompt with optional
provider/model/effort pins. Built-ins: **build** (default coding agent) and
**plan** (read-only planning). Define your own `build.md` / `plan.md` to
replace them. **Tab cycles agents**; `/agent [name]` lists or selects; the
active agent shows in the status bar.

Each model request composes the system prompt in layers (like opencode):

1. **Shared baseline** — identity, ADHD-shaped response contract, tool/safety rules
2. **Provider overlay** — anthropic / openai (incl. chatgpt) / xai / default, chosen from the active provider and model id
3. **Agent persona** — empty for built-in build/plan (provider overlay used); custom `agents/*.md` body replaces the provider overlay; config `systemPrompt` replaces it for build only
4. **Plan overlay** — always added while the plan agent is active
5. **Environment** — workdir, workspace root, git, platform, date, model id
6. **Instructions** — `AGENTS.md` / `CLAUDE.md` from `~/.strike` and the project (walked up to the git root)

```markdown
---
description: reviews diffs for correctness
provider: openai
model: gpt-5.5
effort: xhigh
---
You are a meticulous code reviewer. Focus on correctness…
```

Agents may declare permission rules in frontmatter. Compact form denies (or allows/asks) whole tool categories:

```markdown
---
permission.write: deny
permission.edit: deny
permission.bash: deny
---
```

Or a single-line JSON array (same shape as config `permissions`), appended after compact rules:

```markdown
permissions: [{"permission":"bash","pattern":"git *","action":"allow"}]
```

Evaluation order: defaults → config → optional --dangerously-skip-permissions allow-all → active agent profile → session always grants (last-match-wins). Switching agents replaces the profile and clears session always-grants. Agent denies still apply under --dangerously-skip-permissions.

**Skills** (`skills/*.md`) are prompt templates invoked as slash commands:
`/commit fix the auth bug` runs the `commit.md` template with `$ARGUMENTS`
replaced by "fix the auth bug" (arguments are appended if the placeholder
is absent).

```markdown
---
description: stage and commit with a good message
---
Look at the uncommitted changes and commit them: $ARGUMENTS
```

## Layout

```
cmd/strike/            main.go: flags/usage/auth subcommand;
                       wire.go: composition root (engine + host + tui wiring)
internal/protocol/     Op/Event types — the seam between engine and frontends
internal/engine/       turn loop & tool dispatch
internal/auth/         credential store + OAuth (PKCE, device) flows
internal/provider/     provider interface; base/ (embeddable client: HTTP,
                       auth, JSON/SSE, error shaping) embedded by anthropic,
                       openaicompat (openai platform + xai), chatgpt
                       (subscription backend); echo dev adapter
internal/tool/         tool contract, registry, read/glob/grep/edit/write/bash
internal/permission/   rulesets + suspend/resume ask service
internal/session/      JSONL event-log persistence
internal/config/       layered config
internal/host/         frontend-facing host-service contract (stdlib-only);
                       local/ wraps auth/config/models/history for the TUI
internal/tui/          BubbleTea app: transcript cells, modals, composer
internal/tui/theme/    design tokens: adaptive colors, icons, precomputed styles
internal/tui/ui/       reusable components: Panel, Dialog, Badge, List, Bento, …
```
