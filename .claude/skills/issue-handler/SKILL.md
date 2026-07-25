---
name: issue-handler
description: Own a GitHub issue end-to-end in strike-cli — worktree, implement, test, PR, CI, merge. Use when asked to handle/fix/close an issue, ship a PR from an issue, or babysit issue work through merge to main.
---

# Issue handler (strike-cli)

Own the issue end-to-end with due diligence on research, testing, and validation. Shipping is ownership: commit → push → PR → CI green → merge → cleanup. All code edits happen inside a **git worktree**, never the primary checkout.

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
8. **Babysit** — CI/reviews; fix branch-related; re-push; merge when green.
9. **Cleanup** — remove worktree after merge; leave cwd outside deleted tree.

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

## CI watch / fix / merge
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
| Branch-related | fix in worktree → commit → push → re-watch |
| Flaky/infra | `gh run rerun <id> --failed` ≤2; then stop-and-ask |
| Ambiguous | one diagnosis; then stop-and-ask |
| Actionable review | fix → commit → push |

Merge **only when all true**: not draft; `OPEN`; checks green; `mergeable=MERGEABLE`; no `CHANGES_REQUESTED`:
```sh
gh pr merge --merge
```
`--merge` matches repo history. Do not pass `--delete-branch` while still checked out on the feature branch in the worktree (main is already checked out in the primary tree) — remote/local branch deletion happens in Cleanup after `git worktree remove`. Blocked on review/permissions → stop-and-ask; never force. **Hard forbids:** force-push; careless `reset --hard`; merge with failing checks; close/reopen PR unprompted.

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
1. Own issue end-to-end through merge.
2. Never edit primary checkout for issue implementation.
3. Never claim done without CI-equivalent local gates (incl. race).
4. Never push secrets. Never weaken/delete tests for green.
5. Never force-push main; no destructive git on shared history.
6. Smallest correct change; honor `AGENTS.md` scope. TUI imports: `protocol`, `host`, `tui/…` only.
7. `.plan/` optional research only — never required; never treat unscoped roadmap as the issue.
8. Stop-and-ask on ambiguity rather than guess.

## Stop-and-ask
- `gh` auth/permission failures; unclear/contradictory acceptance criteria
- Foreign/unexpected dirty worktree; ambiguous path/branch collision; CI red outside branch after 2 reruns
- Merge conflicts with main you cannot resolve confidently (prefer `git merge origin/main` over rebase)
- Review requires product decision; would commit secrets or change CI/security unexpectedly

## What this skill is not
- Not unscoped `.plan/features.md` implementer
- Not multi-agent orchestrator requirement
- Not Python CI babysitter
- Not a substitute for sibling domain skills
