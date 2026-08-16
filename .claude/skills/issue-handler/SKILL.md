---
name: issue-handler
description: Own a GitHub issue end-to-end in strike-cli — worktree, implement, test, PR, spawn review agents that comment on the PR, address feedback in a loop, CI, merge. Use when asked to handle/fix/close an issue, ship a PR from an issue, or babysit issue work through merge to main.
---

# Issue handler (strike-cli)

Own the issue end-to-end with due diligence on research, testing, and validation. Shipping is ownership: commit → push → PR → **review-agent loop** → CI green → merge → cleanup. All code edits happen inside a **git worktree**, never the primary checkout.

For agents developing strike. Authoring skills/docs may use this pipeline; apply when the user frames work as an issue (or asks you to own issue→main).

## Preconditions
- `gh auth status` ok; git repo; network; issue number/URL known.
- **Stop-and-ask:** no write access; `gh` missing; issue already has an open PR you did not create (attach or ask — no duplicate). Dirty primary is fine for `git worktree add` from `origin/main` — do not clean/stash primary WIP.

## Workflow
0. **Orient** — `gh issue view N` (+ comments). Read `AGENTS.md` + `docs/ARCHITECTURE.md` for boundaries. Note issue headers (`wave`/`depends`/`conflicts`/`priority`) if present.
1. **Research** — map issue→packages; read code + nearest tests. Optional local `.plan/features.md` / `FEATURES_STATUS.md` **if present** (never block if missing). State acceptance criteria bullets. Stop-and-ask if ambiguous.
2. **Worktree** — create/enter; confirm `pwd` + branch before edits.
3. **Plan** — smallest correct change; no drive-by refactors. Assign **risk tier A/B/C** (see Verification tiers).
4. **Implement** — worktree only; load sibling skills when domain matches.
5. **Test** — load `write-go-tests` when behavior changes.
6. **Validate** — load `test-and-validate` for the **tier gate**; smoke when applicable.
7. **Sync main** — if the PR will be open >~1h or `origin/main` moved, `git fetch origin main && git merge origin/main` then re-run tier gate before/at push.
8. **Ship** — commit, push `origin/<branch>`, PR with `Fixes #N`.
9. **Review loop** — tier-scaled (below); post findings; address; re-review until merge-ready.
10. **Babysit CI** — fix branch-related failures; re-push; re-enter review loop after material code changes.
11. **Merge** — only when merge gates pass (below).
12. **Cleanup** — remove worktree after merge; leave cwd outside deleted tree.

## Worktree setup
```sh
MAIN_ROOT=$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")
WT_PARENT="$(dirname "$MAIN_ROOT")/strike-cli-worktrees"
BRANCH="worktree-<slug>"   # e.g. worktree-24-model-prefix; 2–4 kebab words; include issue # if helpful
mkdir -p "$WT_PARENT"
git fetch origin main
# Resume if yours; else create. Never delete others' worktrees.
if [ -d "$WT_PARENT/$BRANCH" ] && git worktree list | grep -q "$WT_PARENT/$BRANCH"; then
  # On $BRANCH and yours (clean or only your WIP): cd and continue.
  # Stop-and-ask: foreign worktree, unexpected dirty state you don't own, or ambiguous collision.
  cd "$WT_PARENT/$BRANCH"
else
  git worktree add -b "$BRANCH" "$WT_PARENT/$BRANCH" origin/main
  cd "$WT_PARENT/$BRANCH"
fi
```
Before every edit session:
```sh
pwd   # must be $WT_PARENT/$BRANCH
git rev-parse --abbrev-ref HEAD   # must be $BRANCH
```
Refuse Write/Edit under primary checkout for issue implementation.

## Verification tiers (local ship gate)

Source of truth: root `AGENTS.md` *Verification tiers*. Summary:

| Tier | When | Local gate |
|---|---|---|
| **A** | Docs/skills/markdown only | gofmt if any `.go`; no full test suite |
| **B** | Normal code (default) | gofmt → TUI generate if `_src` → `web-check` if `web/` → `make test && make vet && make build` |
| **C** | tool / permission / auth / session / engine concurrency / protocol wire / sandbox | Tier B + `go test -race ./... -count=1` + focused package tests |

CI runs `go test -race ./...` on every PR — do **not** always re-run full local race on A/B.

**Smoke:** load skill `smoke` when the diff touches `cmd/`, `harness/engine`, TUI input/keymap/app, `session`, or `auth`.

**Contract freeze:** default keybind or config-schema changes must update `docs/keybinds.md` / `docs/config.md` and call out migration in the PR body.

**Flakes:** see `test-and-validate` flake policy — do not block unrelated merges on known env-only flakes; file `wave: 0` bugs.

Load `test-and-validate` for report format. Never weaken/delete tests for green.

## Commit and push
```sh
git status && git diff && git log --oneline -10
git add <intended paths only>
git commit -m "$(cat <<'EOF'
type(scope): concise summary

Fixes #<N>
EOF
)"
git push -u origin HEAD
```
Conventional commits: `feat`/`fix`/`docs`/`refactor`/`test`/`chore`. No secrets. No force-push main. No hook skips. Prefer new commits over amend/force on shared PR branch.

## Open PR
```sh
gh pr create --base main --head "$BRANCH" --title "type(scope): summary" --body "$(cat <<'EOF'
## Summary
- …

## Issue
Fixes #<N>

## Tier
A | B | C

## Verification
- <exact commands from tier gate>
- smoke: ran | skipped (reason)
EOF
)"
```
Focused PR; no AI wall of text. If PR exists: `gh pr view`.

## Review-agent loop (required for B/C; optional for A)

After the PR exists, **do not merge until the tier’s review bar is met**. You own dispatching reviewers, posting findings on the PR, fixing code, and repeating.

### Pass budget (risk-scaled)

| Tier | Review passes | Merge bar |
|---|---|---|
| **A** | 0–1 optional | Owner judgment; no blocking findings if a pass ran |
| **B** | **1** clean pass on current HEAD | 0 **blocking**; should-fix fixed or explicitly deferred with reason on the PR |
| **C** | **1–2** passes on current HEAD | 0 **blocking** and 0 **should-fix** |

Stall ceiling: **5** passes max — if still dirty, stop-and-ask (do not merge). Five is a cap, not a target.

### When to run
- Tier B/C: once after first push + PR open (may run in parallel with CI).
- Again after **any** push that changes production, test, skill, agent, workflow, or other merge-bound files — including review/CI fixes.
- Skip re-review only for pure changelog typos that cannot affect behavior or process — when unsure, re-review.
- A clean review on an **older** SHA does **not** satisfy merge gates after a new push.

### Resolve PR identity
```sh
PR=$(gh pr view --json number -q .number)
HEAD_SHA=$(gh pr view --json headRefOid -q .headRefOid)
REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner)
```

### Spawn reviewers
**Preferred:** dispatch a read-only reviewer subagent:
- Task `subagent_type: reviewer` when the host provides it, **or**
- the in-repo agent at `.claude/agents/strike-reviewer.md`

**Fallback** if Task/reviewer is unavailable: you run the same rubric in-process — still load the diff, still post findings via `gh`. Never skip the loop on Tier B/C.

Optionally spawn a second pass focused on tests/security if Tier C or the first pass is large.

Reviewer prompt **must** include:
- PR number/URL, base branch (`main`), and current `headRefOid`
- Issue number and acceptance criteria
- Hard requirement: run `gh pr diff $PR` (or receive the full patch) and read surrounding callers/tests before any verdict — **never** approve from title/URL alone
- Instruction: correctness bugs first (concrete failure scenario), then needless complexity / missed reuse; no impact-free style nits
- Instruction: return ranked findings with `path:line`, severity `blocking` | `should-fix` | `nit`, failure scenario, and the **head SHA actually reviewed**
- Instruction: do **not** edit files; findings only; output must be paste-ready verbatim

Also pull human/GitHub review state:
```sh
gh pr view "$PR" --json reviews,comments,reviewDecision,headRefOid
gh api "repos/$REPO/pulls/$PR/comments"
gh api "repos/$REPO/issues/$PR/comments"
```

### Post findings as PR review comments
Post reviewer output on the PR (not only chat). **Post the reviewer’s ranked findings verbatim** — do not omit, soften, or downgrade severity. If you disagree with a blocking/should-fix item, still post it and **stop-and-ask** (or reply on the thread with the dispute); silent drop is forbidden.

**Event policy (handler is usually the PR author):**
- Automated/handler-authored reviews **always** use `"event": "COMMENT"` (never `REQUEST_CHANGES` or `APPROVE` — GitHub rejects self-approve and self-request-changes leaves merge gates stuck).
- Encode severity in the review body and inline comment text (`**blocking:**`, `**should-fix:**`, `**nit:**`).
- Reserve GitHub `APPROVE` / `REQUEST_CHANGES` for **distinct human** reviewers only.

**Preferred — single COMMENT review with inline notes** (only for lines in the PR diff hunk on `$HEAD_SHA`):
```sh
HEAD_SHA=$(gh pr view "$PR" --json headRefOid -q .headRefOid)
gh api "repos/$REPO/pulls/$PR/reviews" --method POST --input - <<EOF
{
  "commit_id": "$HEAD_SHA",
  "event": "COMMENT",
  "body": "Review pass N on $HEAD_SHA\n\n(verbatim summary of blocking / should-fix / nits)",
  "comments": [
    {
      "path": "path/to/file.go",
      "line": 42,
      "side": "RIGHT",
      "body": "**blocking:** …\n\nFailure scenario: …"
    }
  ]
}
EOF
```
If an inline `line` is not part of the diff hunk, the API 422s — put that finding in the top-level review body instead (do not invent lines).

**Fallback — top-level PR comment** when inline mapping is impractical:
```sh
gh pr comment "$PR" --body "$(cat <<EOF
## Review pass N (head \`$HEAD_SHA\`)
### Blocking
- \`file:line\` — … (failure scenario)

### Should-fix
- …

### Nits (optional)
- …
EOF
)"
```

**Clean pass (meets tier bar) — still required on the PR for B/C:**
A chat-only “LGTM” does **not** count. Always record the clean pass against `$HEAD_SHA`:
```sh
HEAD_SHA=$(gh pr view "$PR" --json headRefOid -q .headRefOid)
gh pr comment "$PR" --body "$(cat <<EOF
## Review pass N (head \`$HEAD_SHA\`) — clean
0 blocking, 0 should-fix (or should-fix deferred per tier B). Merge checklist may proceed for this SHA only.
EOF
)"
```

When building `gh api` JSON for inline reviews, encode bodies with `jq -n --arg body "$text"` so quotes/newlines do not break the payload.

Rules for posted comments:
- Include severity tag and failure scenario for blocking/should-fix.
- Anchor path:line to the diff on this PR’s current head SHA.
- Do not spam duplicates across passes — reply on the existing thread or mark resolved in the next summary with the new SHA.

### Address comments
1. List open review threads / new comments (human + automated).
2. For each **blocking** (and **should-fix** on Tier C, or B when not deferring): fix in the worktree, add/adjust tests when behavior changes, run tier gate.
3. **Nits:** fix if cheap and clearly better; otherwise reply on the thread why deferred (one line).
4. Commit and push (new commit; no amend/force on shared PR branch).
5. Reply on each addressed thread (or in a single PR comment) with what changed (`commit` shortsha + brief note).
6. Re-run tier gate; watch CI; **spawn another review pass on the new `headRefOid`** when the tier requires it.

### Merge-ready checklist (all required for the tier)
- [ ] Review bar met for tier A/B/C on **current** `headRefOid` (posted review/comment cites that SHA)
- [ ] That SHA matches `gh pr view --json headRefOid` at merge time
- [ ] Latest pass findings posted **verbatim** on the PR (when a pass ran)
- [ ] All actionable human review comments addressed or explicitly deferred with reason
- [ ] `reviewDecision` is not `CHANGES_REQUESTED` (human reviewers); do not use self-REQUEST_CHANGES
- [ ] CI checks green on the same head SHA
- [ ] `mergeable=MERGEABLE`, not draft, state `OPEN`
- [ ] Local tier gate passed on the same commit

## CI watch / fix
gh-only (no Python watchers):
```sh
gh pr checks --watch
# or poll:
gh pr view --json state,mergeable,mergeStateStatus,statusCheckRollup,reviewDecision,isDraft
gh pr checks
gh run list --branch "$BRANCH" --limit 5
gh run view <run-id> --log-failed
```
| Class | Action |
|---|---|
| Branch-related | fix in worktree → commit → push → re-watch → **re-enter review loop** if tier requires |
| Flaky/infra | `gh run rerun <id> --failed` ≤2; then stop-and-ask |
| Ambiguous | one diagnosis; then stop-and-ask |
| Actionable review | fix → commit → push → **re-enter review loop** |

## Merge
Merge **only when** the merge-ready checklist above is fully satisfied:
```sh
gh pr merge --merge
```
`--merge` matches repo history. Do not pass `--delete-branch` while still checked out on the feature branch in the worktree — remote/local branch deletion happens in Cleanup after `git worktree remove`. Blocked on review/permissions → stop-and-ask; never force. **Hard forbids:** force-push; careless `reset --hard`; merge with failing checks; merge without meeting the tier review bar; close/reopen PR unprompted.

## Cleanup (after merge)
From `MAIN_ROOT`:
```sh
cd "$MAIN_ROOT" && git fetch origin main
git worktree remove "$WT_PARENT/$BRANCH"
git branch -d "$BRANCH" 2>/dev/null || true
git worktree prune
```
Do not leave cwd inside deleted worktree. Only delete branch you created.

## Sibling skills
| When | Load |
|---|---|
| Writing/extending `*_test.go` | `write-go-tests` |
| Before claiming done / after impl or CI-fix | `test-and-validate` |
| User-visible cmd/engine/tui/session/auth | `smoke` |
| Cutting a version tag | `release` |
| Any `internal/tui` view/panel/modal/theme | `tui-components` first |

## Hard rules
1. Own issue end-to-end through merge — including review-agent loop on B/C.
2. Never edit primary checkout for issue implementation.
3. Never claim done without the **tier** local gate (not always full race).
4. Never push secrets. Never weaken/delete tests for green.
5. Never force-push main; no destructive git on shared history.
6. Smallest correct change; honor `AGENTS.md` scope. TUI imports: `protocol`, `host`, `tui/…` only.
7. `.plan/` optional research only — never required; never treat unscoped roadmap as the issue.
8. Stop-and-ask on ambiguity rather than guess.
9. Never merge with open blocking findings (or tier-C should-fix) from the latest pass on the **current** head SHA.
10. Always post review findings on the PR via `gh` (COMMENT review or comment) when a pass runs — chat-only review does not count.
11. Post reviewer findings verbatim; never omit or downgrade severity. Disputes → stop-and-ask.
12. Handler-authored automated reviews use `event: COMMENT` only — never self-APPROVE or self-REQUEST_CHANGES.

## Stop-and-ask
- `gh` auth/permission failures; unclear/contradictory acceptance criteria
- Foreign/unexpected dirty worktree; ambiguous path/branch collision; CI red outside branch after 2 reruns
- Merge conflicts with main you cannot resolve confidently (prefer `git merge origin/main` over rebase)
- Review requires product decision; would commit secrets or change CI/security unexpectedly
- Review loop still fails the tier bar after 5 passes
- Desire to drop or downgrade a reviewer’s blocking/should-fix finding

## What this skill is not
- Not unscoped `.plan/features.md` implementer
- Not multi-agent orchestrator requirement (beyond required PR review subagent / strike-reviewer)
- Not Python CI babysitter
- Not a substitute for sibling domain skills
