---
name: issue-orchestrator
description: Drive open GitHub issues on strike-cli to merge via parallel worktrees — parse wave/depends/blocks/conflicts headers, dispatch issue-handler agents, prefer bugs-first, never parallelize conflicting issues. Use when asked to orchestrate issues, farm the backlog, run the issue board, or coordinate multiple agents on GitHub issues.
---

# Issue orchestrator (strike-cli)

You coordinate **many** issues. You do **not** implement feature code in the primary checkout. Each shippable unit is owned by an **issue-handler** agent (or you acting as handler for a single issue) inside its own git worktree.

## When to use

- User asks to orchestrate / farm / drain open issues
- User wants parallel agents on the backlog
- After filing a wave of issues and wanting them driven to `main`

## Source of truth

```sh
gh issue list --state open --limit 100
gh issue view N
gh pr list --state open
git fetch origin main
git worktree list
```

### Issue body headers (machine-readable)

Issues should start with:

```text
wave: N
depends: none | #a #b
blocks: none | #c #d
conflicts: none | #e #f
priority: bugs-first | feature
```

| Field | Meaning |
|---|---|
| `wave` | Lower ships first. Wave 0 = bugs/tests baseline. |
| `depends` | All listed issues must be **CLOSED** before start |
| `conflicts` | Do not run in parallel with these (shared files / same agent) |
| `blocks` | What this unlocks (scheduling hint) |
| `priority` | `bugs-first` before `feature` within the same wave |

Closed `depends` count as satisfied.

**Missing headers:** treat as `wave: 99`, `depends: none`, `conflicts: none`, `priority: feature`. Prefer issues that have proper headers. When farming, **comment once** on headerless issues asking for headers (or load `issue-create-and-handle` to refine) before parallel dispatch — do not guess large parallel graphs.

### Suggested `conflicts` hotspots

Serialize (or mark mutual `conflicts`) when multiple issues touch:

- `internal/tui/_src/app/keymap*.go`, default keybinds, `internal/config/keybinds.go`
- `internal/engine/turn.go` / core turn loop
- `internal/tool/registry.go` + defer/toolsearch wiring
- `internal/protocol` event kind additions shared across frontends

Prefer explicit `conflicts: #N` on the issue over silent collision.

## Sibling skills

| Skill | Role |
|---|---|
| `issue-handler` | One issue → worktree → implement → test → PR → **review-agent loop** → CI → merge → cleanup |
| `test-and-validate` | Verification report format + tiers |
| `write-go-tests` | Tests when handler implements |
| `smoke` | Product happy-path when user-visible |
| `release` | Version tags (orchestrator does not cut releases unless asked) |
| `tui-components` | TUI work inside a handler |

Orchestrator **dispatches handlers**; handlers load domain skills.

## Priority order

1. **Wave 0** (`priority: bugs-first` / test gaps) until none remain open  
2. Lowest open `wave` with `depends` satisfied  
3. Within a wave: `bugs-first`, then issues that unlock the most open `blocks`, then lowest number  

Do **not** start large features while wave-0 bugs that touch the same surface are open, unless the user overrides.

## Conflict rules

- If A lists B under `conflicts` (or vice versa), **at most one** of A/B may be in-progress (open PR or active worktree) at a time.
- Same package hotspots without headers: serialize when both touch keymap/default binds or `engine/turn.go` heavily — prefer explicit `conflicts` on new issues.
- Multi-issue “cluster” (e.g. all keymap bugs): **one worktree / one handler** for the whole cluster when they conflict with each other.
- Parallelism cap defaults to **4**, but the **conflict graph wins** — fewer slots when hotspots overlap.

## Ready set

An issue is **ready** when all are true:

- state `OPEN`
- every `depends` issue is `CLOSED` (or not listed)
- none of `conflicts` are in-progress (open PR titled/body `Fixes #N`, or worktree branch `worktree-*N*` / clearly for that issue)
- `wave` is the current minimum eligible wave (or user pinned this issue)
- no open PR already fixing it (attach/babysit that PR instead of duplicating)

## Dispatch

For each ready issue (up to the parallelism cap / conflict graph):

1. Spawn or instruct an agent to load **`issue-handler`** with the issue number.
2. Handler must: worktree off `origin/main`, implement, tests per `AGENTS.md`, tier gate, PR `Fixes #N`, review loop, CI, merge, cleanup.
3. Orchestrator tracks: issue → branch → PR URL → status (`queued` / `in_progress` / `review` / `merged` / `blocked`).

Do **not** implement production code in the primary checkout as the orchestrator.

## Orchestrator loop

```text
loop:
  1. Refresh issues, PRs, worktrees, origin/main
  2. Build ready set
  3. Fill free slots with highest-priority ready issues
  4. For in-flight PRs: ensure handler/review/CI progress; re-dispatch handler if stalled
  5. On merge: mark done, unlock dependents, pull main awareness
  6. On block: comment on issue, mark blocked, free the slot
  7. Exit when no open issues left, or user stops, or only blocked remain
```

### Stalls

- CI red > reasonable time → handler fixes or orchestrator assigns handler to that PR  
- Review loop hits stall ceiling (5) → stop-and-ask per issue-handler  
- Merge conflict with main → handler merges `origin/main` in worktree  
- Ambiguous product decision → comment on issue + stop that lane  

## Reporting

Lead with board status:

```text
Wave 0: done | in flight | blocked
Ready: #N #M
In flight: #N → PR URL
Blocked: #N (reason)
Next: #N (why)
```

Do not claim “all green” without `gh pr checks` / issue state.

## Hard rules

1. Bugs/tests (`wave: 0`, `bugs-first`) before features when both are open.  
2. Never parallelize `conflicts`.  
3. Never duplicate an existing open PR for the same issue.  
4. Never force-push `main`; never commit secrets.  
5. Handlers own merge gates (review + CI); orchestrator does not merge over failing checks.  
6. `.plan/` is optional research for handlers — orchestrator schedules **GitHub issues**, not the whole roadmap file.  
7. Stop-and-ask when `gh` auth fails, user must choose between conflicting product directions, or only blocked issues remain.

## What this skill is not

- Not a substitute for `issue-handler` on a single issue (use handler directly)
- Not an implementer of unscoped `.plan/features.md` items without issues
- Not a requirement to max out parallel agents if conflicts forbid it
