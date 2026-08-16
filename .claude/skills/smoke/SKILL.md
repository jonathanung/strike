---
name: smoke
description: Offline product happy-path checks for strike-cli — boot, echo turn, interrupt, session resume. Use when verifying user-visible CLI/TUI/session/auth changes, before release, or when issue-handler tier B/C touches cmd/, engine, tui input/keymap, session, or auth.
---

# Smoke (strike-cli)

Product-level checks beyond unit tests. Prefer automated commands; fall back to a short manual checklist when automation is missing.

No API keys required for the default path (`echo` provider).

## When to run

- Changes under `cmd/strike`, `harness/engine`, `internal/tui` (input/keymap/app), `internal/session`, `internal/auth`, `internal/host`
- Before cutting a release (with skill `release`)
- After CI green when the PR is user-visible

Skip for pure docs/skills (tier A) unless the docs claim a runtime behavior change.

## Automated baseline

```sh
make build
make run-echo   # must start; quit cleanly (q / ctrl+c per current binds)
```

If `strike` binary exists from `make build`:

```sh
./strike version
./strike exec --provider echo "say hi" 2>/dev/null || ./strike exec "say hi" || true
```

Record the exact flags that work on this revision (exec/provider flags drift — read `./strike -h` / `cmd/strike`).

## Checklist (mark each PASS/FAIL/SKIP)

| # | Path | How |
|---|---|---|
| 1 | Boot | `make run-echo` or `./strike --provider echo` starts without panic |
| 2 | Version | `./strike version` prints stamped or `dev` version |
| 3 | Echo turn | Send a short prompt; assistant text appears (manual TUI or `exec`) |
| 4 | Tool path | If tools touched: echo `run …` or unit-covered tool still OK — note SKIP if not exercised |
| 5 | Interrupt | If engine/TUI interrupt touched: esc (or documented bind) cancels in-flight turn |
| 6 | Resume | If session touched: `--continue` or `/session` resume loads without corrupt transcript |
| 7 | Auth surface | If auth touched: `strike auth` help/list does not crash (no live login required) |

## Report format

1. **Verdict** — PASS only if no FAIL; list SKIP with reason.
2. **Commands** — exact lines.
3. **Failures** — verbatim.
4. **Not run** — what a human should still click-test.

## Rules

- Do not claim product readiness from `make test` alone when this skill applies.
- Do not require live provider keys for baseline smoke.
- Do not skip FAIL by weakening the app; open a bugs-first issue instead.
