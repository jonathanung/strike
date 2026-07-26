---
description: run project verification gates and fix failures on this branch
---
Run the project's verification gates, diagnose failures, and fix branch-related issues.

## Context

1. `git status --short`
2. `git branch --show-current`
3. Read `AGENTS.md` / `README` / `Makefile` / CI config for the expected gates
4. Prefer documented commands (strike-cli typically: `make test`, `make vet`, `make build`; stronger: `go test -race ./... -count=1`)

## Safety

- NEVER update git config or skip hooks unless the user explicitly asks
- NEVER force-push
- Do not weaken or delete tests solely to go green
- Do not commit secrets
- If failures look like infra/flakes unrelated to this branch, say so and stop after evidence

## Task

$ARGUMENTS

1. Run the project verify suite (smallest full gate first; add race/cover when the change touches concurrency, tools, permissions, auth, or session I/O).
2. On failure: capture the failing command and error verbatim; classify branch-related vs infra.
3. Fix branch-related failures with the smallest correct change; re-run until green or blocked.
4. Report: commands run, pass/fail, what you fixed (or why blocked). Do not claim green without running the suite.
