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
- **Bug fixes:** add at least one regression test that fails before the fix when feasible.

## Helpers by domain

### Tools (`harness/tool`)

```go
func allowAll(dir string) *Context {
	return &Context{
		WorkDir: dir, // usually t.TempDir()
		Ask:     func(ctx context.Context, req AskRequest) error { return nil },
	}
}
```

Cover where relevant: happy path, invalid JSON args, permission rejection via Ask error,
path relative/absolute, workspace sandbox escape attempts, filestate stale read after
external change, truncation/output caps, deferred/`toolsearch` registration.

Tool surface is large (read/glob/grep/edit/write/apply_patch/bash + sandbox, task*,
webfetch, todo*, memory_*, issue_*, notebook_edit, sleep, skill, question, plan_mode,
phase_done, toolsearch, …). Prefer tests next to the module you touch; do not assume
a fixed “six tools” list.

### Permissions (`harness/permission`)

- `Evaluate` last-match-wins across layered rulesets.
- `Service.Ask` + `Reply`: once, always (session grant + sibling resolve), reject cascade, ctx cancel.

### Protocol / session

- Round-trip event types through `protocol.Wrap` → `Envelope.Decode` when adding kinds.
- Session: `Open` → `Append` → `Close` → `Replay`; malformed line errors.

### Providers

- Prefer `httptest.NewServer` against `providers/base` and concrete adapters.
- Engine integration: `harness/provider/echo` (prompts starting with `run ` emit bash tool calls).

### Config / auth

- Point HOME at `t.TempDir()` so GlobalPath/cache paths stay sandboxed.
- Auth store must stay mode `0600`.

### TUI

Source lives under `internal/frontend/tui/app/_src/` and is flattened into `internal/frontend/tui/app` via
`go generate ./internal/frontend/tui/app`. Tests may live beside `_src` or as generated/package
tests under `internal/frontend/tui/app`.

Reuse helpers from `internal/frontend/tui/app/_src/app/app_test.go` (same package after generate),
including: `updateApp`, `runAppCmd`, `runAllAppCmds`, `receiveAppOp`, `assertNoAppOp`.
Shared support also appears under `internal/frontend/tui/app/_src/test/` (e.g. `testsupport_test.go`).
Load skill `tui-components` before asserting chrome/theme behavior.

## After writing

```sh
go test ./path/to/package/ -count=1 -v
# then tier gate — load test-and-validate
```

## Priority gaps (fill these first when expanding coverage)

1. `harness/tool` — sandbox/workspace, filestate freshness, caps, defer/toolsearch  
2. `harness/engine` — prune/compaction, interrupt, deferred tool re-promote, multi-tool  
3. `harness/permission` — Evaluate + Service edge cases  
4. `internal/protocol` + `internal/persist/session` — new event kinds, replay  
5. `internal/question` — multi-question ask/reply  
6. `internal/product/auth` — OAuth/PKCE/device, resolve edge cases  
7. `providers/*` — SSE cancel, cache headers where applicable  
8. `internal/frontend/tui` — keymap/default binds, modals, interrupt/esc paths  

## Platform notes

- Compare canonical paths with `filepath.EvalSymlinks` when asserting absolute paths
  (macOS `/var` → `/private/var`).
- Filesystem mtime granularity: sleep or content-change strategies when testing staleness.
