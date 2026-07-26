---
description: remove AI-generated style slop from the current branch
---
Clean AI-generated "slop" from changes on this branch so the tree matches human project style.

## Context

1. `git status --short`
2. `git branch --show-current`
3. Diff vs default base when known (`git diff main...HEAD` / `master...HEAD` / `origin/HEAD...HEAD`), else `git diff HEAD` and staged

## What to remove

- Comments a human would not add, or that fight the file's existing comment density
- Extra defensive checks / try-catch abnormal for the call site (especially trusted paths)
- Type escapes used only to silence the checker (`any`, blanket ignores) without fixing the type
- Style inconsistent with the surrounding file
- Unnecessary emoji in code or comments
- Restated narration in docs that adds no contract

## What to keep

- Real bug fixes, tests, and security/permission checks
- Comments that document non-obvious invariants
- Project-required headers or generated markers

## Safety

- Do not change behavior except by deleting pure noise
- Do not rewrite unrelated files
- NEVER commit or push unless the user explicitly asks
- Do not touch secrets

## Task

$ARGUMENTS

1. Inspect the branch diff and open only files that show slop.
2. Edit to match neighboring style; prefer deletion over rewrite.
3. End with a 1–3 sentence summary of what changed (no long report).
