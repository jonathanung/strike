# Contributing

## Package layout

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
internal/tool/         tool contract + registry (read/glob/grep/edit/write/
                       apply_patch/bash/task/webfetch/todo*/memory_*/issue_*/
                       notebook_edit/sleep/skill/question/plan_mode/phase_done/
                       toolsearch — full list: ARCHITECTURE.md)
internal/permission/   rulesets + suspend/resume ask service
internal/session/      JSONL event-log persistence
internal/config/       layered config + agents/skills/workflows
internal/host/         frontend-facing host-service contract (stdlib-only);
                       local/ wraps auth/config/models/history/memory/issue/files
internal/tui/          BubbleTea app: transcript cells, modals, composer
internal/tui/theme/    design tokens: adaptive colors, icons, precomputed styles
internal/tui/ui/       reusable components: Panel, Dialog, Badge, List, Bento, …
```

Full dataflow, import rules, and recipes (add a provider/tool/slash
command/UI component/host service/theme token):
[ARCHITECTURE.md](ARCHITECTURE.md).

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
the user answers, and rejections carry feedback back to the model.

## Verification

```sh
make test          # go test ./...
make vet           # go vet ./...
make build         # go build -o strike ./cmd/strike
make cover         # statement coverage → coverage.out + total %
make cover-check   # cover + enforce COVER_MIN (default 75)
```

Stronger checks when touching concurrency, tools, permissions, auth, or
session I/O:

```sh
go test -race ./... -count=1
```

### Coverage

`make cover` runs `go test ./... -count=1 -coverprofile=coverage.out` and
prints the total statement percentage. Inspect details with:

```sh
go tool cover -func=coverage.out    # per-function
go tool cover -html=coverage.out    # browser
```

`make cover-check` fails if the total is below `COVER_MIN` (default **75**).
Override locally, e.g. `make cover-check COVER_MIN=77`. This is a **local**
floor only — CI does not hard-fail on coverage yet (soft report step only).
Raise `COVER_MIN` in the Makefile as package coverage improves (auth,
providers, term, wire, etc.).

Offline smoke (no API keys): `make run-echo`.

See `AGENTS.md` at the repo root for testing conventions and scope rules.

## Doc check (when shipping UX)

User-facing docs must match code. When you add or rename a slash command,
keybind, right-pane window, CLI flag, or shipped skill/workflow, update the
matching paths (and keep relative links valid):

| Surface | Source of truth | Docs |
|---|---|---|
| Slash commands | `internal/tui/commands.go` (`builtinCommandSpecs`) | [usage.md](usage.md) |
| Keybinds | `internal/tui/keymap.go` (`defaultKeyMap`, `keybindCatalog`) | [keybinds.md](keybinds.md) |
| CLI flags / `exec` | `cmd/strike` + `strike --help` | [install.md](install.md), [usage.md](usage.md) |
| Config / custom providers / `vimMode` | `internal/config` | [config.md](config.md) |
| Agents, skills, workflows | `internal/config` builtins + loaders | [agents-skills.md](agents-skills.md) |
| Tool inventory | `internal/tool` + `cmd/strike/wire.go` | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Index | — | [README.md](../README.md) |

No heavy doc framework — a PR that changes the surface should touch the table
row above in the same change.
