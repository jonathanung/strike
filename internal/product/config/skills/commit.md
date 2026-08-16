---
description: stage and create a git commit
---
Create a single git commit for the current working tree changes.

## Context

Run these via the bash tool (each may ask for permission — approve git writes when appropriate):

1. `git status --short`
2. `git diff HEAD`
3. `git branch --show-current`
4. `git log --oneline -10`

## Safety

- NEVER update git config
- NEVER skip hooks (`--no-verify`, `--no-gpg-sign`) unless the user explicitly asks
- ALWAYS create a NEW commit — never `--amend` unless the user explicitly asks
- Do not commit secrets (`.env`, credentials, private keys). Warn if asked to
- If there is nothing to commit, say so and stop — no empty commits
- Never use interactive git flags (`-i`)

## Task

$ARGUMENTS

1. Draft a concise commit message that matches this repo's recent style (conventional commits when the log uses them). Prefer why over what.
2. Stage only the intended files (`git add` paths — avoid blanket `git add -A` unless the whole tree is clearly intentional).
3. Commit with a HEREDOC message:

```
git commit -m "$(cat <<'EOF'
Commit message here.

EOF
)"
```

4. Show `git status` afterward. Do not push.
