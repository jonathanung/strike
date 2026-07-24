---
name: test-and-validate
description: Use when running tests, validation, CI checks, coverage, race detection, vet, build, or verifying strike-cli changes. Trigger on make test, go test, lint, verify, validate, regression, or before claiming work done.
---

# Test and validate (strike-cli)

Read-only verification skill. Observe and report — do not fix failures here.

## Commands (prefer Makefile)

| Check | Command |
|---|---|
| Unit/integration suite | `make test` or `go test ./...` |
| Fresh non-cached run | `go test ./... -count=1` |
| Race detector | `go test -race ./... -count=1` |
| Coverage | `go test ./... -count=1 -cover` |
| Package focus | `go test ./internal/tool/ -count=1 -v` |
| Single test | `go test ./internal/permission/ -run TestEvaluate -count=1 -v` |
| Static analysis | `make vet` |
| Build binary | `make build` |
| Offline TUI smoke | `make run-echo` |

CI (`.github/workflows/ci.yml`) runs: `go build`, `go vet`, `go test ./...`.

## Required workflow

1. Discover what changed (`git diff`, package paths).
2. Run focused package tests for touched packages first.
3. Run full battery: `make test && make vet && make build`.
4. If the change touches concurrency, permissions, tools, auth, session, or history: also `go test -race ./... -count=1`.
5. If the change is user-visible CLI/TUI startup: `make run-echo` briefly, or exercise the relevant CLI flag path under test.

## Report format

1. **Verdict** — pass/fail with counts.
2. **Commands run** — exact shell lines.
3. **Failures** — verbatim output, never paraphrased.
4. **Gaps** — packages or flows not exercised and why.

## Package risk map

| Area | Higher risk signals |
|---|---|
| `internal/tool` | filesystem mutation, shell exec, path edge cases |
| `internal/permission` | last-match-wins, always grants, reject cascade |
| `internal/auth` | credentials on disk (mode 0600), env precedence, OAuth |
| `internal/session` + `protocol` | transcript integrity, replay |
| `internal/provider` | HTTP/SSE, cancellation |
| `internal/engine` | turn state machine, tool loop |
| `internal/tui` | Bubble Tea update loops (use existing harnesses) |
| `internal/history` | concurrency + path security |

## Rules

- Never edit source or tests to make verification green.
- Never skip a failing package without marking it FAIL with output.
- Prefer project Make targets over inventing new scripts.
