# Contributing

## Package layout

```
cmd/strike/            main.go: flags/usage/auth/exec/rpc/acp/serve subcommands;
                       wire.go: composition root (engine + host + tui wiring)
pkg/protocol/          public Op/Event wire schema (semver Version; docs/protocol.md)
pkg/sdk/               public Go client over pkg/protocol (see docs/sdk.md)
internal/protocol/     compatibility re-export of pkg/protocol
internal/rpc/          stdio JSON-RPC 2.0 transport (strike rpc; ops in, events out)
internal/acp/          Agent Client Protocol adapter (strike acp; ACP ↔ Op/Event)
internal/engine/       turn loop & tool dispatch
internal/auth/         credential store + OAuth (PKCE, device) flows
internal/provider/     provider interface; base/ (embeddable client: HTTP,
                       auth, JSON/SSE, error shaping) embedded by anthropic,
                       openaicompat (openai platform + xai), chatgpt
                       (subscription backend), google; echo dev adapter
internal/tool/         tool contract + registry (read/glob/grep/edit/write/
                       apply_patch/bash/task/webfetch/todo*/memory_*/issue_*/
                       notebook_edit/sleep/skill/question/plan_mode/phase_done/
                       toolsearch — full list: ARCHITECTURE.md)
internal/permission/   rulesets + suspend/resume ask service
internal/session/      JSONL event-log persistence
internal/server/       experimental read-only HTTP attach (strike serve)
internal/config/       layered config + agents/skills/workflows
internal/host/         frontend-facing host-service contract (stdlib-only);
                       local/ wraps auth/config/models/history/memory/issue/files
internal/tui/          BubbleTea app (flattened package; edit _src/ only)
internal/tui/_src/     source of truth by concern — go generate flattens here
internal/tui/theme/    design tokens: adaptive colors, icons, precomputed styles
internal/tui/ui/       reusable components: Panel, Dialog, Badge, List, Bento, …
```

Full dataflow, import rules, and recipes (add a provider/tool/slash
command/UI component/host service/theme token):
[ARCHITECTURE.md](ARCHITECTURE.md).

**TUI edit rule:** change files under `internal/tui/_src/<group>/` (or the
real packages `theme`/`ui`/`term`/`common`), then `go generate ./internal/tui`.
Flattened `internal/tui/*.go` are gitignored and wiped on generate — never
edit them.

## Architecture in one paragraph

The TUI and the agent engine are separate halves connected only by
`pkg/protocol` (in-tree call sites may still import the `internal/protocol`
re-export) — frontends submit **Ops** (`UserInput`, `PermissionReply`,
`Interrupt`), the engine emits **Events** (`TextDelta`, `ToolCallBegin/End`,
`PermissionAsked`, `TurnCompleted`, …) — codex's SQ/EQ pattern, in-process
over Go channels for now. The event stream *is* the transcript: every
session is persisted as a JSONL event log (`~/.strike/sessions/`). Everything
else the TUI needs from its host process — credentials, the model catalog,
saved defaults, prompt history, agent/skill listings — arrives through a
second, narrower seam, `internal/host` (implemented by `internal/host/local`);
the TUI never imports `internal/auth`, `config`, `models`, or `history`
directly, and a boundary test enforces it, so the backend can add a host
service without touching the UI and the UI can be developed against fakes.
Tools return `{Title, Output, Metadata}` separating model-facing text from UI
rendering data (opencode's contract). Permissions are ordered allow/ask/deny
rulesets with last-match-wins evaluation; an "ask" suspends the tool
goroutine until the user answers, and rejections carry feedback back to the
model.

## Verification

Risk-tiered gates live in root `AGENTS.md` (*Verification tiers*). Summary:
tier **A** docs/skills; **B** normal code (`make test && make vet && make build`
after gofmt / TUI generate / web-check as needed); **C** trust boundary adds
`go test -race ./... -count=1`. CI always races.

```sh
make test          # go test ./...
make vet           # go vet ./...
make build         # go build -o strike ./cmd/strike
make cover         # statement coverage → coverage.out + total %
make cover-check   # cover + enforce COVER_MIN (default 75)
make prompt-reg    # E3.2 prompt metrics report (soft deltas by default)
make harness-eval  # #807 harness regression pack + report (offline)
```

### Harness evaluation suite (#807)

Offline regression pack under `internal/replay` (`TestHarnessEvalSuite`) covering
**correctness** (tool contracts, precondition fail-closed, golden echo replay),
**safety** (secret redaction, permission deny, sandbox capability report),
**recovery** (cancel error codes, no mutative double-retry), and
**latency/cost** (timeline duration/token fields, budget wire + echo metrics).
Also consumes #791 recordings and #782 run snapshots. Composes with epic #459
E3 runners; does **not** replace SWE-bench (#561) or failure injection (#808).

```sh
make harness-eval
HARNESS_EVAL_REPORT=/tmp/harness-eval.json make harness-eval
```

Scenario failures are hard errors (also under `make test` / CI Test). The CI
"Harness eval report (soft)" step is **non-blocking** (`continue-on-error`) so
the verbose artifact can land without gating merges. **Path to blocking:**
remove `continue-on-error` on that step once the pack is stable on `main`.


Stronger checks when touching concurrency, tools, permissions, auth, or
session I/O (tier C):

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
| Config / custom providers / `vimMode`/`nanoMode` | `internal/config` | [config.md](config.md) |
| Agents, skills, workflows | `internal/config` builtins + loaders | [agents-skills.md](agents-skills.md) |
| Tool inventory | `internal/tool` + `cmd/strike/wire.go` | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Index | — | [README.md](../README.md) |

No heavy doc framework — a PR that changes the surface should touch the table
row above in the same change.

## Changelog and releases

`CHANGELOG.md` is the canonical source for release notes. It follows these
rules:

- Add every notable user-facing change to `[Unreleased]` in the same pull
  request as the change.
- Use only `Added`, `Changed`, `Deprecated`, `Removed`, `Fixed`, and `Security`;
  omit empty categories.
- Describe user impact, not implementation activity. Group related changes
  and link their pull requests where practical.
- Use absolute URLs because version entries are copied to GitHub Releases.
- Put breaking behavior, changed defaults, and migration steps in a bold
  **Upgrade note** under `Changed`.
- Omit tests, refactors, chores, and documentation-only changes unless they
  materially affect the shipped product.

To prepare a release, rename `[Unreleased]` to `[vX.Y.Z] - YYYY-MM-DD`, add a
fresh `[Unreleased]` section above it, and update the comparison links at the
bottom of the file. Use the annotated tag date for `YYYY-MM-DD` and Semantic
Versioning for `vX.Y.Z`.

GitHub release titles use `strike vX.Y.Z`. Their bodies contain the standard
install block followed by the matching changelog entry verbatim. The release
workflow enforces this by failing when the tagged version has no non-empty
entry in `CHANGELOG.md`.
