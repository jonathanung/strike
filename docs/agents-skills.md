# Agents & skills

Both `.strike` roots (global and project) can hold `agents/` and `skills/`
folders of markdown files; project files override same-named global ones.

## Agents

**Agents** (`agents/*.md`) are personas — a system prompt with optional
provider/model/effort pins. Built-ins: **build** (default coding agent) and
**plan** (read-only planning). Define your own `build.md` / `plan.md` to
replace them. **Tab cycles agents**; `/agent [name]` lists or selects; the
active agent shows in the status bar.

Each model request composes the system prompt in layers (like opencode):

1. **Shared baseline** — identity, ADHD-shaped response contract, tool/safety rules
2. **Provider overlay** — anthropic / openai (incl. chatgpt) / xai / default, chosen from the active provider and model id
3. **Agent persona** — empty for built-in build/plan (provider overlay used); custom `agents/*.md` body replaces the provider overlay; config `systemPrompt` replaces it for build only
4. **Plan overlay** — always added while the plan agent is active
5. **Environment** — workdir, workspace root, git, platform, date, model id
6. **Instructions** — `AGENTS.md` / `CLAUDE.md` from `~/.strike` and the project (walked up to the git root)

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
