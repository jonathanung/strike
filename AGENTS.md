# strike-cli

Go 1.26 agentic coding TUI. Engine emits protocol events; TUI consumes them. Sessions are JSONL event logs.

## Verification (required before claiming done)

**Tiers** (skills `test-and-validate` / `issue-handler` follow this table — do not fork softer gates):

| Tier | When | Local gate |
|---|---|---|
| **A** | Docs, skills, comments, markdown-only | `gofmt` if any `.go` touched; full suite not required |
| **B** | Normal Go / web / TUI (default) | `gofmt` → `go generate ./internal/tui` if `internal/tui/_src` changed → `make web-check` if `web/` changed → `make test && make vet && make build` |
| **C** | Trust boundary: tool, permission, auth, session, engine concurrency/turn, protocol wire, sandbox | Tier B + `go test -race ./... -count=1` + focused package tests first |

CI (`.github/workflows/ci.yml`) always runs gofmt, TUI generate, web-check (when web present), build, vet, and `go test -race ./...`. Local race on every PR is redundant for tier A/B.

```sh
make test          # go test ./...
make vet           # go vet ./...
make build         # go build -o strike ./cmd/strike
make cover         # go test ./... -coverprofile=coverage.out (+ total %)
make cover-check   # cover + fail if total statements % < COVER_MIN (default 75)
```

Product smoke (user-visible cmd/engine/tui/session/auth): skill `smoke` / `make run-echo`.
Releases: skill `release`.

Report exact commands and failing output verbatim. Do not claim green without running the tier gate.

## Testing conventions

- Standard library `testing` only — no testify/ginkgo/gomock.
- Tests live next to source as `*_test.go`.
- Prefer table-driven cases; use `t.TempDir()` and `t.Setenv("HOME", ...)` for isolation.
- Mock only external boundaries (HTTP via `httptest`, clocks when needed). Never mock the unit under test.
- Tool tests: allow-all `Ask` helper unless testing permission denial.
- Provider tests: `httptest.Server` for wire format; use `internal/provider/echo` for offline engine loops.
- TUI tests: reuse helpers from `internal/tui/_src/app/app_test.go` (`updateApp`, `runAppCmd`, etc.; package tests after `go generate ./internal/tui`).

## Architecture map

See docs/ARCHITECTURE.md for the dataflow diagram, full package table with import
rules, and recipes (add a provider/tool/slash command/UI component/host
service/theme token).

| Package | Role |
|---|---|
| `cmd/strike` | CLI flags + auth/exec/rpc/acp/serve subcommands (`main.go`), composition root wiring (`wire.go`) |
| `internal/rpc` | Stdio JSON-RPC 2.0 Op/Event bridge (`strike rpc`: NDJSON ops in, event envelopes out) |
| `internal/acp` | Agent Client Protocol adapter (`strike acp`: ACP session/prompt ↔ Op/Event for Zed/Devin) |
| `internal/server` | Experimental read-only HTTP attach (`strike serve`: /health, SSE events, attach page) |
| `pkg/protocol` | Public Ops/Events wire schema; JSONL envelopes (semver `Version`) |
| `pkg/redact` | Shared credential-shaped string scrubbing (exports, inspect, traces) |
| `pkg/timeline` | Structured run timeline builder + redacted JSON/JSONL export |
| `pkg/sdk` | Thin Go client over `pkg/protocol` (channel/JSONL client, RunTurn, session replay) |
| `internal/protocol` | Compatibility re-export of `pkg/protocol` |
| `internal/engine` | Turn loop, tool dispatch, interrupts |
| `internal/harness` | Function harness contract, registry, external process adapter |
| `internal/provider` | LLM adapters (+ `base`, `echo`, anthropic, openai, xai, google, chatgpt) |
| `internal/sandbox` | OS process sandbox (`Wrap` via bwrap / sandbox-exec) for bash |
| `internal/tool` | read/glob/grep/edit/write/apply_patch/bash/task/task_status/task_read/task_message/task_interrupt/wait/agent_roster/agent_message/agent_broadcast/team_task/webfetch/todowrite/todoread/memory_write/memory_read/issue_write/issue_read/plan_write/plan_read/notebook_edit/sleep/skill/question/enter_plan_mode/exit_plan_mode/toolsearch |
| `internal/mcp` | MCP client (stdio + streamable HTTP); bridges external tools onto the registry |
| `internal/lsp` | LSP client (JSON-RPC over stdio); extension registry; diagnostics collection |
| `internal/question` | user-question ask service (suspend tool until QuestionReply) |
| `internal/permission` | last-match-wins allow/ask/deny + ask service |
| `internal/secret` | secret-ref env indirection + protocol event redaction on top of pkg/redact (see docs/secrets.md) |
| `internal/auth` | credentials, OAuth/PKCE/device, env precedence |
| `internal/config` | global/project JSON + agents/skills markdown |
| `internal/session` | JSONL event log append/replay + concurrent Manager |
| `internal/history` | project-scoped prompt history |
| `internal/memory` | project-scoped durable key/value memory |
| `internal/issue` | project-scoped durable issue tracker |
| `internal/goal` | loop harness: goals, guards, critic, hooks, JSONL state |
| `internal/plan` | project-scoped root-owned structured plans (sections, lifecycle, CAS) |
| `internal/host` | frozen stdlib-only contract: what a frontend needs from its host (auth, catalog, settings, history, memory, issues, plans, goals, agents, skills) |
| `internal/host/local` | real `host.Services` impl, wraps auth/config/models/history/memory/issue/plan/goal |
| `internal/tui` | Bubble Tea UI: app model, layout, cells, modals |
| `internal/tui/theme` | design tokens: adaptive colors, `Icons`, precomputed `Styles` |
| `internal/tui/ui` | reusable component library (Panel, Dialog, Badge, List, Bento, …) |

## Scope

- Smallest correct change. Match surrounding style and comment density.
- Lean-code guidance (config `leanCode`: off|lite|full, default lite) biases
  build/general/debugger toward a YAGNI ladder and plan/orchestrator toward
  efficient designs that still scale — never at the cost of tests, security,
  a11y, or trust-boundary errors. See docs/agents-skills.md (ponytail-inspired).
- Must implement as many tests as possible for all new chunks of code.
- No new test frameworks or dependencies without an explicit ask.
- Do not commit secrets or write real credentials into fixtures.
- UI work goes through `internal/tui/ui` components and `internal/tui/theme`
  tokens — no raw lipgloss styles or hardcoded glyphs in views; colors and
  icons come from the theme. Load the `tui-components` skill before TUI
  view/panel/modal/picker work (`.claude/skills/tui-components/`).
- **TUI source trap:** package `internal/tui` sources live under
  `internal/tui/_src/<group>/` and are flattened by `go generate ./internal/tui`
  (make/CI run this first). Flattened `internal/tui/*.go` are gitignored —
  edit `_src/` only; editing flattened files is silently reverted.
- `internal/tui` may import only `internal/protocol`, `internal/host`, and
  `internal/tui/...` among `internal/*` packages — enforced by
  `internal/tui/boundary_test.go` (`TestArchitectureBoundaries`). Prefer
  `pkg/protocol` / `pkg/redact` for public wire/scrub helpers (also allowed;
  not under `internal/`). Charm paths: v1 `github.com/charmbracelet/…` or v2
  `charm.land/…`; never `github.com/charmbracelet/…/v2`
  (`TestCharmImportPaths`).

## Agent process skills (`.claude/skills`)

| Skill | Role |
|---|---|
| `test-and-validate` | Tiered verification report (read-only) |
| `write-go-tests` | Author `*_test.go` |
| `smoke` | Offline product happy-path |
| `release` | Tag + GitHub release |
| `issue-handler` | Issue → worktree → PR → review → merge |
| `issue-orchestrator` | Parallel issue board |
| `issue-create-and-handle` | File approved issue then handle |
| `tui-components` | TUI ui/theme catalog |

Built-in user skills (`/verify`, `/ship`, `/learn`, …) live under `internal/config/skills/`.

