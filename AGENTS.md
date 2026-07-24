# strike-cli

Go 1.26 agentic coding TUI. Engine emits protocol events; TUI consumes them. Sessions are JSONL event logs.

## Verification (required before claiming done)

```sh
make test          # go test ./...
make vet           # go vet ./...
make build         # go build -o strike ./cmd/strike
```

Stronger checks when touching concurrency, tools, permissions, auth, or session I/O:

```sh
go test -race ./... -count=1
go test ./... -count=1 -cover
```

Offline smoke (no API keys):

```sh
make run-echo
```

Report exact commands and failing output verbatim. Do not claim green without running the suite.

## Testing conventions

- Standard library `testing` only — no testify/ginkgo/gomock.
- Tests live next to source as `*_test.go`.
- Prefer table-driven cases; use `t.TempDir()` and `t.Setenv("HOME", ...)` for isolation.
- Mock only external boundaries (HTTP via `httptest`, clocks when needed). Never mock the unit under test.
- Tool tests: allow-all `Ask` helper unless testing permission denial.
- Provider tests: `httptest.Server` for wire format; use `internal/provider/echo` for offline engine loops.
- TUI tests: reuse helpers in `internal/tui/app_test.go` (`updateApp`, `runAppCmd`, etc.).

## Architecture map

| Package | Role |
|---|---|
| `cmd/strike` | CLI flags, auth subcommands, wiring |
| `internal/protocol` | Ops/Events seam; JSONL envelopes |
| `internal/engine` | Turn loop, tool dispatch, interrupts |
| `internal/provider` | LLM adapters (+ `base`, `echo`, anthropic, openai, xai, chatgpt) |
| `internal/tool` | read/glob/grep/edit/write/bash |
| `internal/permission` | last-match-wins allow/ask/deny + ask service |
| `internal/auth` | credentials, OAuth/PKCE/device, env precedence |
| `internal/config` | global/project JSON + agents/skills markdown |
| `internal/session` | JSONL event log append/replay |
| `internal/history` | project-scoped prompt history |
| `internal/tui` | Bubble Tea UI |

## Scope

- Smallest correct change. Match surrounding style and comment density.
- No new test frameworks or dependencies without an explicit ask.
- Do not commit secrets or write real credentials into fixtures.
