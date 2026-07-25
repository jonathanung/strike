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
0. **Orient** — `gh issue view N` (+ comments). Read `AGENTS.md` + `docs/ARCHITECTURE.md` for boundaries.
1. **Research** — map issue→packages; read code + nearest tests. Optional local `.plan/features.md` / `FEATURES_STATUS.md` **if present** (never block if missing). State acceptance criteria bullets. Stop-and-ask if ambiguous.
2. **Worktree** — create/enter; confirm `pwd` + branch before edits.
3. **Plan** — smallest correct change; no drive-by refactors.
4. **Implement** — worktree only; load sibling skills when domain matches.
5. **Test** — load `write-go-tests` when behavior changes.
6. **Validate** — load `test-and-validate`; CI-equivalent gates; that skill’s report format.
7. **Ship** — commit, push `origin/<branch>`, PR with `Fixes #N`.
8. **Review loop** — spawn review agent(s); post findings as PR review comments; address them; re-review until merge-ready (see below).
9. **Babysit CI** — fix branch-related failures; re-push; re-enter review loop after material code changes.
10. **Merge** — only when merge gates pass (below).
11. **Cleanup** — remove worktree after merge; leave cwd outside deleted tree.

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

## Local verification
CI-equivalent — **always** before push (not softer than CI):
```sh
test -z "$(gofmt -l .)"
make test && make vet && make build
go test -race ./... -count=1
```
Optional: `make run-echo` if CLI/TUI startup touched. CI (`.github/workflows/ci.yml`): gofmt, `go build ./...`, `go vet ./...`, `go test -race ./...`. Load `test-and-validate` for report format; issue-handler’s always-race gate overrides that skill’s “race when warranted” for the ship gate. Never weaken/delete tests for green.

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

## Verification
- gofmt clean; make test && make vet && make build; go test -race ./... -count=1
EOF
)"
```
Focused PR; no AI wall of text. If PR exists: `gh pr view`.

## Review-agent loop (required)

After the PR exists, **do not merge until this loop completes**. You (the issue handler) own dispatching reviewers, posting their findings on the PR, fixing code, and repeating.

### When to run
- Once after first push + PR open (may run in parallel with CI).
- Again after **any** push that changes production or test code in response to review or CI.
- Skip re-review only for pure doc/typo commits that cannot affect behavior — when unsure, re-review.

### Spawn reviewers
Dispatch a **read-only** `reviewer` subagent (Task tool `subagent_type: reviewer`) against the PR diff. Optionally spawn a second pass focused on tests/security if the first pass is large or the change touches auth, permissions, tools, session, or concurrency.

Reviewer prompt must include:
- PR number/URL and base `main`
- Issue number and acceptance criteria
- Instruction: correctness bugs first (concrete failure scenario), then needless complexity / missed reuse; no impact-free style nits
- Instruction: return ranked findings with `file_path:line` and severity `blocking` | `should-fix` | `nit`
- Instruction: do **not** edit files; findings only

Also pull human/GitHub review state:
```sh
gh pr view --json reviews,comments,reviewDecision
gh api repos/{owner}/{repo}/pulls/PR/comments
gh api repos/{owner}/{repo}/issues/PR/comments
```

### Post findings as PR review comments
Convert reviewer output into a real GitHub review on the PR (not only a chat summary).

**Preferred — single review with inline comments** (when line mappings are known):
```sh
# Build a review payload from reviewer findings, then:
gh api repos/{owner}/{repo}/pulls/PR/reviews \
  --method POST \
  --input - <<'EOF'
{
  "commit_id": "HEAD_SHA",
  "event": "REQUEST_CHANGES",
  "body": "Automated review pass N — blocking/should-fix items must be addressed before merge.",
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
Use `"event": "COMMENT"` when there are only nits or no findings. Use `"event": "APPROVE"` only when the review agent reports zero blocking and zero should-fix items **and** you agree after a sanity check.

**Fallback — top-level PR comment** when inline line mapping fails:
```sh
gh pr comment PR --body "$(cat <<'EOF'
## Review pass N
### Blocking
- `file:line` — …

### Should-fix
- …

### Nits (optional)
- …
EOF
)"
```

Rules for posted comments:
- One finding per comment when inline; include severity tag and failure scenario for blocking/should-fix.
- Quote path:line from the diff on this PR’s head SHA.
- Do not invent line numbers; if unsure, put the item in the top-level summary with the best path anchor.
- Do not spam duplicate comments for the same finding across passes — reply on the existing thread or mark resolved in the next summary.

### Address comments
1. List open review threads / new comments (human + automated).
2. For each **blocking** and **should-fix**: fix in the worktree, add/adjust tests when behavior changes, run local verification.
3. **Nits:** fix if cheap and clearly better; otherwise reply on the thread why deferred (one line).
4. Commit and push (new commit; no amend/force on shared PR branch).
5. Reply on each addressed thread (or in a single PR comment) with what changed (`commit` shortsha + brief note).
6. Re-run local gates; watch CI; **spawn another review pass**.

### Loop limits
- Continue until a review pass reports **no blocking and no should-fix**, CI is green, and merge gates pass.
- Cap at **5** review passes. If still blocked after 5, stop-and-ask with a summary of remaining disagreements.
- Product/design ambiguity in a comment → stop-and-ask; do not guess.

### Merge-ready checklist (all required)
- [ ] Latest review pass: 0 blocking, 0 should-fix (nits ok if deferred with reply)
- [ ] All actionable human review comments addressed or explicitly deferred with reason
- [ ] `reviewDecision` is not `CHANGES_REQUESTED`
- [ ] CI checks green on head
- [ ] `mergeable=MERGEABLE`, not draft, state `OPEN`
- [ ] Local CI-equivalent gates passed on the same commit

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
| Branch-related | fix in worktree → commit → push → re-watch → **re-enter review loop** |
| Flaky/infra | `gh run rerun <id> --failed` ≤2; then stop-and-ask |
| Ambiguous | one diagnosis; then stop-and-ask |
| Actionable review | fix → commit → push → **re-enter review loop** |

## Merge
Merge **only when** the merge-ready checklist above is fully satisfied:
```sh
gh pr merge --merge
```
`--merge` matches repo history. Do not pass `--delete-branch` while still checked out on the feature branch in the worktree (main is already checked out in the primary tree) — remote/local branch deletion happens in Cleanup after `git worktree remove`. Blocked on review/permissions → stop-and-ask; never force. **Hard forbids:** force-push; careless `reset --hard`; merge with failing checks; merge without a clean review pass; close/reopen PR unprompted.

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
| Any `internal/tui` view/panel/modal/theme | `tui-components` first |

## Hard rules
1. Own issue end-to-end through merge — including review-agent loop.
2. Never edit primary checkout for issue implementation.
3. Never claim done without CI-equivalent local gates (incl. race).
4. Never push secrets. Never weaken/delete tests for green.
5. Never force-push main; no destructive git on shared history.
6. Smallest correct change; honor `AGENTS.md` scope. TUI imports: `protocol`, `host`, `tui/…` only.
7. `.plan/` optional research only — never required; never treat unscoped roadmap as the issue.
8. Stop-and-ask on ambiguity rather than guess.
9. Never merge with open blocking/should-fix review findings from the latest pass.
10. Always post review findings on the PR via `gh` (inline review or comment) — chat-only review does not count.

## Stop-and-ask
- `gh` auth/permission failures; unclear/contradictory acceptance criteria
- Foreign/unexpected dirty worktree; ambiguous path/branch collision; CI red outside branch after 2 reruns
- Merge conflicts with main you cannot resolve confidently (prefer `git merge origin/main` over rebase)
- Review requires product decision; would commit secrets or change CI/security unexpectedly
- Review loop still has blocking findings after 5 passes

## What this skill is not
- Not unscoped `.plan/features.md` implementer
- Not multi-agent orchestrator requirement (beyond required PR review subagent)
- Not Python CI babysitter
- Not a substitute for sibling domain skills
