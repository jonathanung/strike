---
description: commit, push, and open a pull request
---
Ship the current work: commit → push → pull request. Alias of the full `/pr` chain when you also need a fresh commit.

## Context

Run via the bash tool:

1. `git status --short`
2. `git diff HEAD`
3. `git branch --show-current`
4. `git log --oneline -10`
5. `git remote show origin 2>/dev/null | sed -n '/HEAD branch/s/.*: //p' || true`
6. `gh pr view --json url,number,title 2>/dev/null || true`

## Safety

- NEVER update git config
- NEVER force-push to main/master or skip hooks unless the user explicitly asks
- ALWAYS create NEW commits — never `--amend` unless asked
- Do not commit secrets
- Never use interactive git flags (`-i`)
- If there is nothing to ship, say so and stop

## Task

$ARGUMENTS

1. **Commit** — stage intended files and create one commit with a HEREDOC message matching repo style (skip if the tree is already clean and HEAD has unpushed commits to ship).
2. **Push** — `git push -u origin HEAD` when no upstream, else `git push`.
3. **PR** — create with `gh pr create` or update with `gh pr edit` if one exists. Short title; body with Summary + Test plan.
4. Print the PR URL when finished (`gh pr create` does this; or `gh pr view --json url,number,state`). Strike stores URL/number/state on the session for picker badges.
