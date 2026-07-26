# Agents & skills

Both `.strike` roots (global and project) can hold `agents/` and `skills/`
folders of markdown files; project files override same-named global ones.

## Agents

**Agents** (`agents/*.md`) are personas — a system prompt with optional
provider/model/effort pins. Shipping built-ins (override with same-named
files under `~/.strike/agents` or `./.strike/agents`):

| Name | Role |
|------|------|
| **build** | default coding agent (empty persona → provider overlay) |
| **plan** | read-only planning (+ plan overlay) |
| **explore** | fast read-only codebase search (good `task` target) |
| **general** | multi-step research/execution subagent |
| **commit** | git commits only (no source edits, no push) |
| **reviewer** | read-only diff/PR review |
| **tester** | run `make test` / vet / build; report only |
| **debugger** | root-cause investigation |

**Tab cycles agents**; bare `/agent` opens a picker; `/agent [name]` selects
directly; the active agent shows in the status bar. The `task` tool’s optional
`agent` field must match one of these names (or a user-defined agent) —
unknown names fail with `unknown agent "…" (available: …)`.

Each model request composes the system prompt in layers (like opencode):

1. **Shared baseline** — identity, ADHD-shaped response contract, tool/safety rules
2. **Provider overlay** — anthropic / openai (incl. chatgpt) / xai / default, chosen from the active provider and model id
3. **Agent persona** — empty for built-in build/plan (provider overlay used); custom `agents/*.md` body replaces the provider overlay; config `systemPrompt` replaces it for build only
4. **Plan overlay** — always added while the plan agent is active
5. **Environment** — workdir, workspace root, git, platform, date, model id
6. **Instructions** — `AGENTS.md` / `CLAUDE.md` from `~/.strike` and the project (walked up to the git root). Create or refresh the project file with `/init` (confirms before replacing an existing `AGENTS.md`; light local scan only — no secrets).
7. **Project memory** — entries tagged `instruction`, `preference`, or `project-convention` (capped; untrusted). Untagged notes and issues stay on-demand via tools.

```markdown
---
description: reviews diffs for correctness
provider: openai
model: gpt-5.5
effort: xhigh
---
You are a meticulous code reviewer. Focus on correctness…
```

Agents may declare permission rules in frontmatter. Compact form denies (or allows/asks) whole tool categories:

```markdown
---
permission.write: deny
permission.edit: deny
permission.bash: deny
---
```

Or a single-line JSON array (same shape as config `permissions`), appended after compact rules:

```markdown
permissions: [{"permission":"bash","pattern":"git *","action":"allow"}]
```

Evaluation order: defaults → config → optional --dangerously-skip-permissions allow-all → active agent profile → session always grants (last-match-wins). Switching agents replaces the profile and clears session always-grants. Agent denies still apply under --dangerously-skip-permissions.

Layered JSON config: [config.md](config.md).

## Skills

**Skills** (`skills/*.md`) are prompt templates invoked as slash commands:
`/commit fix the auth bug` runs the `commit` skill with `$ARGUMENTS`
replaced by "fix the auth bug" (arguments are appended if the placeholder
is absent). Strike ships built-in shipping skills — `/commit`, `/push`,
`/pr`, `/ship` — overridden by same-named files under `~/.strike/skills` or
`./.strike/skills`. Successful `gh pr …` output that prints a GitHub PR URL
is recorded on the session (JSONL `session.meta` + sidecar `.meta.json`).

```markdown
---
description: stage and commit with a good message
---
Look at the uncommitted changes and commit them: $ARGUMENTS
```

## Workflows

**Workflows** are ordered phase sequences loaded from
`~/.strike/workflows/*.json` and `./.strike/workflows/*.json` (project
overrides global by name). Strike always ships a built-in
`plan-implement` workflow that may be overridden by the same name.

Each phase may pin an agent, extra prompt context, a permission ruleset, and
an exit gate:

| Gate `type` | Clears when |
|---|---|
| `agent` (default) | the model calls `phase_done` |
| `user` | the user approves (e.g. leave plan mode) |
| `check` | `command` exits 0 |

Built-in `plan-implement`:

1. **plan** — `plan` agent, hard-deny `write`/`edit`, user exit gate
2. **implement** — `build` agent, agent exit gate

Tools `enter_plan_mode` / `exit_plan_mode` start and advance that workflow.
The active phase shows as a badge in the TUI header. Example custom file:

```json
{
  "name": "review-fix",
  "description": "Review then fix",
  "phases": [
    {
      "name": "review",
      "agent": "build",
      "exit": { "type": "user" }
    },
    {
      "name": "fix",
      "agent": "build",
      "exit": { "type": "check", "command": "make test" }
    }
  ]
}
```
