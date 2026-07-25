---
description: open or update a pull request for the current branch
---
Open (or update) a pull request for the current branch. Prefer `gh`.

## Context

Run via the bash tool:

1. `git status --short`
2. `git branch --show-current`
3. `git remote show origin 2>/dev/null | sed -n '/HEAD branch/s/.*: //p' || true`
4. `git log --oneline -15`
5. `gh pr view --json url,number,title 2>/dev/null || true`
6. Diff against the default branch when known, e.g. `git diff main...HEAD` or `git diff master...HEAD`

## Safety

- NEVER update git config
- NEVER force-push to main/master
- NEVER skip hooks unless the user explicitly asks
- Do not commit secrets
- Never use interactive git flags (`-i`)

## Task

$ARGUMENTS

1. If the working tree is dirty with changes that belong in this PR, commit them first (same rules as `/commit`).
2. Push the branch if needed (`git push -u origin HEAD` when no upstream).
3. If a PR already exists for this branch, update title/body with `gh pr edit` when the summary changed; otherwise create one:

```
gh pr create --title "type(scope): short title" --body "$(cat <<'EOF'
## Summary
- …

## Test plan
- [ ] …

EOF
)"
```

Keep the title under ~70 characters. Put detail in the body.
4. When done, ensure the PR URL is printed (gh does this on create; otherwise `gh pr view --json url -q .url`). Strike records GitHub PR URLs from bash output onto the session automatically.
