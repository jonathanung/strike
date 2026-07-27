# Agents & skills

Agents and skills are markdown (frontmatter + body). Strike discovers them
from several trees; **later roots override earlier ones by name**.

### Discovery order (merge)

| Order | Agents | Skills |
|------:|--------|--------|
| 0 | built-in embed | built-in embed |
| 1 | `~/.strike/agents` | `~/.strike/skills` |
| 2 | `~/.claude/agents` | `~/.claude/skills` |
| 3 | `~/.config/opencode/agents` (`$XDG_CONFIG_HOME/opencode/…`) | same `…/skills` |
| 4 | `~/.opencode/agents` | `~/.opencode/skills` |
| 5 | `<project>/.strike/agents` | `<project>/.strike/skills` |
| 6 | `<project>/.claude/agents` | `<project>/.claude/skills` |
| 7 | `<project>/.opencode/agent` then `…/agents` | `<project>/.claude/commands` then `…/.opencode/skills` |

- **Strike-native** roots (`.strike`) fail load on invalid names/effort/permissions.
- **External** roots (`.claude`, `.opencode`, XDG opencode) **warn on stderr and skip** bad files; load continues.
- Skills accept flat `name.md` **or** Claude/OpenCode `name/SKILL.md` directories.
- Markdown only — no OpenCode plugin JS/TS execution.
- OpenCode-style `model: provider/id` splits into provider + model when `provider` is unset.
- Nested Claude/OpenCode `permission:` maps map to strike permission rules (best-effort).

## Agents

**Agents** (`agents/*.md`) are personas — a system prompt with optional
provider/model/effort pins. Shipping built-ins (override with same-named
files under any later discovery root):

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
| **validator** | goal-backward requirements check; PASS/FAIL/UNVERIFIED only |
| **orchestrator** | plan → delegate via `task` to specialists → synthesize (not solo bulk impl) |
| **pr-babysitter** | own an open PR through CI/review (watch, fix branch failures, push); overlap with issue-handler skill (skill = full issue→merge; agent = in-session PR watch) |

**Tab cycles agents**; bare `/agent` opens a picker; `/agent [name]` selects
directly; the active agent shows in the status bar. The `task` tool’s optional
`agent` field must match one of these names (or a user-defined agent) —
unknown names fail with `unknown agent "…" (available: …)`.

Each model request composes the system prompt in layers (like opencode):

1. **Shared baseline** — identity, ADHD-shaped response contract, doing-tasks
2. **Tools** — effective registry guidance (name + short purpose, recommended use). Reflects agent/permission/depth/MCP; hard-denied tools omitted. Own `/context` provenance layer (`tools` / `registry:effective`).
3. **Provider overlay** — anthropic / openai (incl. chatgpt) / xai / default, chosen from the active provider and model id
4. **Agent persona** — empty for built-in build/plan (provider overlay used); custom `agents/*.md` body replaces the provider overlay; config `systemPrompt` replaces it for build only
5. **Plan overlay** — always added while the plan agent is active
6. **Lean code** — agent-scoped efficiency guidance (see below); off via config `leanCode`
7. **Environment** — workdir, workspace root, git, platform, date, model id
8. **Instructions** — `AGENTS.md` / `CLAUDE.md` from `~/.strike` and the project (walked up to the git root). Create or refresh the project file with `/init` (confirms before replacing an existing `AGENTS.md`; light local scan only — no secrets).
9. **Project memory** — entries tagged `instruction`, `preference`, or `project-convention` (capped; untrusted). Untagged notes and issues stay on-demand via tools.

### Lean code (ponytail-lite)

Strike injects a short **lean-code** bias toward efficient, low-LOC solutions
(YAGNI ladder: skip → reuse → stdlib → native → installed dep → one line →
minimum). Clean-room wording inspired by
[ponytail](https://github.com/DietrichGebert/ponytail) — not a full copy of that
skill. Never sacrifices validation, tests for new behavior, security,
accessibility, or trust-boundary error handling.

| Agents | Strength |
|--------|----------|
| **build**, **general**, **debugger** | **Strict** — implementer ladder; smallest correct change |
| **plan**, **orchestrator** | **Strategic** — efficient designs that still scale; thin implement path |
| **explore**, **reviewer**, **tester**, **validator**, **commit**, others | **None** — specialized prompts stay undiluted |

Config: `"leanCode": "off" | "lite" | "full"` (global or project JSON). Default
`lite`. `full` strengthens the implementer ladder only. See [config.md](config.md).

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

Evaluation order: defaults → config → optional --auto / --dangerously-skip-permissions allow-all → active agent profile → session always grants (last-match-wins). Switching agents replaces the profile and clears session always-grants. Agent denies still apply under --auto / --dangerously-skip-permissions.

Layered JSON config: [config.md](config.md).

## Skills

**Skills** (`skills/*.md` or `skills/<name>/SKILL.md`) are prompt templates
invoked as slash commands: `/commit fix the auth bug` runs the `commit`
skill with `$ARGUMENTS` replaced by "fix the auth bug" (arguments are
appended if the placeholder is absent). Strike ships built-in skills —
overridden by same-named files in any later discovery root (including
`.claude` / `.opencode`). Project `.claude/commands/*.md` are loaded as
skills when the markdown is compatible. Successful `gh pr …` output that
prints a GitHub PR URL is recorded on the session (JSONL `session.meta` +
sidecar `.meta.json`).

| Skill | Role |
|-------|------|
| `/commit` `/push` `/pr` `/ship` | git shipping chain |
| `/review` | branch/PR correctness review (no edits) |
| `/learn` | extract non-obvious learnings into AGENTS.md |
| `/deslop` | remove AI style slop from the branch diff |
| `/verify` | run project gates; fix branch-related failures |

Peer import inventory and hooks mapping: [peer-ecosystem.md](peer-ecosystem.md).

```markdown
---
description: stage and commit with a good message
---
Look at the uncommitted changes and commit them: $ARGUMENTS
```

## Workflows

**Workflows** are ordered phase sequences loaded from
`~/.strike/workflows/*.json` and `./.strike/workflows/*.json` (project
overrides global by name). Strike ships built-in workflows that may be
overridden by the same name.

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

Built-in `review-fix`:

1. **review** — `reviewer` agent, hard-deny `write`/`edit`, user exit gate
2. **fix** — `build` agent, check gate (`make test`)

Tools `enter_plan_mode` / `exit_plan_mode` start and advance the default plan
workflow. After plan completes, `exit_plan_mode` switches to **build** (simple)
or **orchestrator** (complex): pass `agent`, or omit and supply `steps` /
`areas` / `multi_agent` (heuristic: steps ≥ 4, areas ≥ 3, or multi_agent →
orchestrator). The active phase shows as a badge in the TUI header. Example custom
file:

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
