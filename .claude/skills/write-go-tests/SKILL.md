---
name: write-go-tests
description: Use when adding or extending Go tests in strike-cli — *_test.go, table-driven cases, fixtures, httptest, tool Ask helpers, TUI harnesses, coverage gaps. Do not use for running tests only (use test-and-validate).
---

# Write Go tests (strike-cli)

Tests only. Never modify production code; if code is untestable or buggy, report it.

## Conventions

- Framework: stdlib `testing` only.
- Placement: `package foo` tests beside source as `foo_test.go` (same package is fine and preferred for unexported helpers).
- Style: table-driven where multiple inputs matter; plain functions for stateful sequences.
- Isolation: `t.TempDir()`, `t.Setenv`, never touch real `~/.strike` or network without `httptest`.
- Assert observable behavior (return values, files written, events emitted, errors), not private call graphs.

## Helpers by domain

### Tools (`internal/tool`)

```go
func allowAll() *Context {
	return &Context{
		WorkDir: t.TempDir(), // set in test
		Ask: func(ctx context.Context, req AskRequest) error { return nil },
	}
}
```

Cover: happy path, invalid JSON args, permission rejection via Ask error, path relative/absolute, truncation/limits where applicable.

### Permissions (`internal/permission`)

- `Evaluate` last-match-wins across layered rulesets.
- `Service.Ask` + `Reply`: once, always (session grant + sibling resolve), reject cascade, ctx cancel.

### Protocol / session

- Round-trip every event type through `protocol.Wrap` → `Envelope.Decode`.
- Session: `Open` → `Append` → `Close` → `Replay`; malformed line errors.

### Providers

- Prefer `httptest.NewServer` against `internal/provider/base` and concrete adapters.
- Engine integration: `internal/provider/echo` (prompts starting with `run ` emit bash tool calls).

### Config / auth

- Point HOME at `t.TempDir()` so GlobalPath/cache paths stay sandboxed.
- Auth store must stay mode `0600`.

### TUI

Reuse `internal/tui/app_test.go` helpers: `updateApp`, `runAppCmd`, `runAllAppCmds`, `receiveAppOp`, `assertNoAppOp`.

## After writing

```sh
go test ./path/to/package/ -count=1 -v
go test ./... -count=1
```

Load skill `test-and-validate` for the full battery.

## Priority gaps (fill these first when expanding coverage)

1. `internal/tool` — all six tools + registry  
2. `internal/permission` — Evaluate + Service  
3. `internal/protocol` + `internal/session`  
4. `internal/config` Load/merge/SetGlobalDefaults  
5. `internal/provider/base` + echo  
6. `internal/models` cache/fetch (httptest)  
7. `internal/auth` beyond store/bearer (PKCE, resolve edge cases)  
8. `internal/engine` error/interrupt/multi-tool paths  
