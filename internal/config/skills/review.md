---
description: review a branch diff or pull request for correctness
---
Review code changes for correctness. Prefer the read-only `reviewer` agent posture: no source edits, no commits, no push.

## Context

Run via tools (bash for git/gh; read/grep for surrounding code):

1. `git status --short`
2. `git branch --show-current`
3. `git rev-parse --abbrev-ref origin/HEAD 2>/dev/null || true`
4. If `$ARGUMENTS` looks like a PR number or URL: `gh pr view …` and `gh pr diff …`
5. Else prefer branch diff vs default base (`git diff main...HEAD` / `master...HEAD` / `origin/HEAD...HEAD`), else `git diff HEAD` / staged

## Safety

- NEVER edit, write, commit, push, or merge
- NEVER update git config or skip hooks
- Do not dump secrets from the tree into the review

## Task

$ARGUMENTS

1. Summarize what the change does (2–4 sentences).
2. Rank findings. Each serious finding needs a concrete failure scenario and `path:line` on the new side when possible.
3. Severity:
   - **blocking** — wrong behavior, data loss, security, broken gates
   - **should-fix** — likely bug or missing test for new behavior
   - **nit** — optional clarity (skip pure style with no bug risk)
4. Output:
   1. **Verdict** — approve | request-changes
   2. **Findings** — ranked list
   3. **Questions** — only product/design blockers
