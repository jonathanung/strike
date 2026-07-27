---
name: issue-create-and-handle
description: Turn a reported problem or requested change into a refined GitHub issue, obtain explicit approval, create and assign the issue to the authenticated GitHub user, then immediately hand it to issue-handler. Use when asked to file an issue and start work on it, create-and-handle an issue, or turn a bug report into an assigned implementation workflow.
---

# Create, approve, assign, and handle an issue

Convert the user's request into one scoped GitHub issue. The issue body must be approved before it is created. Once created, assign it to the authenticated GitHub user and immediately invoke `issue-handler` for that issue.

## Preconditions

- `gh auth status` succeeds.
- The current directory is a Git repository with a GitHub remote.
- The user has described a problem, change, or outcome.

Stop and ask if authentication, repository access, or the required GitHub permissions are unavailable.

## Workflow

1. **Inspect**
   - Read `AGENTS.md` and relevant code, tests, and configuration.
   - Search existing open issues and PRs for duplicates:
     ```sh
     gh issue list --state open --limit 100 --search "<key terms> in:title,body"
     gh pr list --state open --search "<key terms>"
     ```
   - If an existing issue or PR already covers the request, report it and stop. Do not create a duplicate.

2. **Draft**
   - State the observed or requested behavior, likely affected area, acceptance criteria, and test expectations.
   - **Headers are required** on every new issue (orchestrator depends on them):
     ```text
     wave: 0
     depends: none
     blocks: none
     conflicts: none
     priority: bugs-first
     ```
   - Use a concise draft with this body structure:
     ```text
     wave: 0
     depends: none
     blocks: none
     conflicts: none
     priority: bugs-first

     ## Problem
     ...

     ## Scope
     ...

     ## Acceptance criteria
     - ...

     ## Verification
     - Tier: A | B | C
     - ...
     ```
   - For a feature rather than a bug, use an appropriate `wave` and `priority: feature`.
   - Set `depends` / `conflicts` / `blocks` from repository evidence:
     - `conflicts` when the change likely touches the same hotspots as another open issue (keymap/defaults, `engine/turn.go`, tool registry/defer, shared protocol events) — list those issue numbers.
     - `depends` when another issue must land first.
   - Do not create feature issues with missing headers; refuse and re-draft.

3. **Refine and approve**
   - Present the proposed title and complete issue body to the user.
   - Ask for corrections or explicit approval.
   - Incorporate requested refinements and present the revised draft again when material details change.
   - Do **not** create the issue until the user explicitly approves the current draft.

4. **Create and assign**
   - Resolve the authenticated GitHub login:
     ```sh
     ASSIGNEE=$(gh api user --jq .login)
     ```
   - Create the approved issue and assign it in the same operation:
     ```sh
     ISSUE_URL=$(gh issue create --title "<approved title>" --body-file <approved-body-file> --assignee "$ASSIGNEE")
     ISSUE_NUMBER=${ISSUE_URL##*/}
     ```
   - Confirm the issue number, URL, and assignee with `gh issue view "$ISSUE_NUMBER" --json number,url,assignees`.
   - If assignment fails after creation, report the created issue and stop. Do not claim it is assigned.

5. **Dispatch immediately**
   - Load and follow the `issue-handler` skill with the newly created issue number.
   - The handler owns the implementation through worktree, tests, PR, review loop, CI, merge, and cleanup.
   - Do not implement the issue in the primary checkout before dispatching the handler.

## Hard rules

1. Never create an issue without explicit approval of its current title and body.
2. Never create duplicates of an open issue or PR.
3. Always assign the created issue to `gh api user --jq .login`; never guess a username.
4. Preserve approved scope. Newly discovered ambiguity after approval requires another refinement and approval before changing the issue body.
5. Create exactly one issue per approved request unless the user explicitly approves splitting it.
6. After successful creation and assignment, invoke `issue-handler` immediately. Do not wait for another user prompt.
7. If issue creation or assignment fails, do not start implementation under an untracked or unassigned issue.
8. Never create issues without `wave` / `depends` / `blocks` / `conflicts` / `priority` headers.
