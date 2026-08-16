---
description: push the current branch to origin
---
Push the current branch to its remote.

## Context

Run via the bash tool:

1. `git status --short`
2. `git branch --show-current`
3. `git rev-parse --abbrev-ref @{upstream} 2>/dev/null || true`
4. `git log --oneline @{upstream}..HEAD 2>/dev/null || git log --oneline -5`

## Safety

- NEVER update git config
- NEVER force-push to `main` or `master`
- NEVER `push --force` unless the user explicitly asks
- NEVER skip hooks unless the user explicitly asks
- If there is nothing to push, say so and stop

## Task

$ARGUMENTS

1. If the branch has no upstream, push with `-u`:
   `git push -u origin HEAD`
2. Otherwise:
   `git push`
3. Report the remote branch and any errors clearly (auth, rejected non-fast-forward). Do not open a PR.
