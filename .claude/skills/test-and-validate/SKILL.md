---
name: test-and-validate
description: Use when running tests, validation, CI checks, coverage, race detection, vet, build, or verifying strike-cli changes. Trigger on make test, go test, lint, verify, validate, regression, or before claiming work done.
---

# Test and validate (strike-cli)

Read-only verification skill. Observe and report — do not fix failures here
(use built-in `/verify` or implement fixes under `issue-handler` when owning a branch).

**Single source of truth for gates:** root `AGENTS.md` → *Verification tiers*.
This skill runs those tiers; do not invent softer or harder local suites.

## CI mirror (order matters)

Match `.github/workflows/ci.yml`:

1. `gofmt -l .` must be empty
2. `go generate ./internal/tui/app` (TUI flatten; required before build/test if `_src` changed or generate is stale)
3. `make web-check` when `web/` is touched or `web/package.json` exists and UI may be affected
4. `go build ./...` or `make build`
5. `make vet`
6. `go test ./...` — CI uses `go test -race ./...` on every PR

Local convenience: `make test && make vet && make build` after gofmt (+ generate/web when needed).

## Risk tiers (pick one per change)

| Tier | When | Local gate |
|---|---|---|
| **A** | Docs, skills, comments, markdown-only, no Go/web | `test -z "$(gofmt -l .)"` (skip if no `.go` touched); no full suite required |
| **B** | Normal Go/web/TUI code (default) | gofmt → generate if TUI `_src` → `make web-check` if `web/` → `make test && make vet && make build` |
| **C** | Trust boundary: `harness/tool`, `permission`, `auth`, `session`, `engine` concurrency/turn loop, `protocol` wire, sandbox/workspace | Tier B + `go test -race ./... -count=1` + focused package tests first |

CI still runs race on every PR. **Do not** pay full local race on Tier A/B unless reproducing a CI failure.

Optional: `make cover` / `make cover-check` (soft in CI). Offline product smoke: load skill `smoke` when user-visible startup/input/session/auth paths change.

## Commands

| Check | Command |
|---|---|
| Format | `test -z "$(gofmt -l .)"` |
| TUI generate | `go generate ./internal/tui/app` |
| Web | `make web-check` |
| Unit suite | `make test` or `go test ./...` |
| Fresh run | `go test ./... -count=1` |
| Race | `go test -race ./... -count=1` |
| Coverage | `make cover` / `make cover-check` |
| Package focus | `go test ./harness/tool/ -count=1 -v` |
| Single test | `go test ./harness/permission/ -run TestEvaluate -count=1 -v` |
| Vet / build | `make vet` / `make build` |
| Offline boot | `make run-echo` |

## Required workflow

1. Discover what changed (`git diff --stat`, package paths) → assign **tier A/B/C**.
2. Run focused package tests for touched packages first (Tier B/C).
3. Run the tier gate above.
4. If Tier C or CI-red reproduction: full race suite.
5. If user-visible CLI/TUI/session/auth: load `smoke` (or note gap if smoke not run).

## Report format

1. **Verdict** — pass/fail with tier.
2. **Commands run** — exact shell lines.
3. **Failures** — verbatim output, never paraphrased.
4. **Gaps** — packages or flows not exercised and why.
5. **Flakes** — see below; never silent skip.

## Flake policy

A **flake** fails intermittently or only on one OS/env while CI (or 3 local reruns) disagrees.

1. Capture: platform, command, pass/fail of **3** reruns, snippet.
2. Open or link a `wave: 0` / `priority: bugs-first` issue with repro.
3. Do **not** block unrelated feature merges on a known env-only flake.
4. Quarantine only with `t.Skip("…")` + issue link — never delete coverage silently.
5. Prefer fixing root cause (e.g. `EvalSymlinks` path compare, mtime granularity) over skip.

## Package risk map

| Area | Higher risk signals |
|---|---|
| `harness/tool` | filesystem mutation, shell, sandbox, filestate freshness, workspace roots |
| `harness/permission` | last-match-wins, always grants, reject cascade |
| `internal/auth` | credentials mode 0600, env precedence, OAuth |
| `internal/persist/session` + `protocol` | transcript integrity, replay |
| `providers` | HTTP/SSE, cancellation |
| `harness/engine` | turn state machine, tool loop, prune/compaction, interrupt |
| `internal/tui` | Bubble Tea update loops (existing harnesses) |
| `internal/persist/history` | concurrency + path security |

## Rules

- Never edit source or tests to make verification green.
- Never skip a failing package without marking it FAIL with output.
- Prefer project Make targets over inventing new scripts.
- Prefer tiered gates over always-max ceremony.
