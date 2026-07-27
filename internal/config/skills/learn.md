---
description: extract non-obvious session learnings into AGENTS.md and process skills
---
Capture durable, non-obvious learnings from this session into AGENTS.md files (and skills when the lesson is process) so future agents start smarter.

## Scope

AGENTS.md can live at any directory level. Place each learning as close to the relevant code as practical:

- Project-wide → root `AGENTS.md`
- Package/module → e.g. `internal/foo/AGENTS.md`
- Feature area → e.g. `cmd/strike/AGENTS.md`

**Process / agent-behavior lessons** (gates agents forget, review policy, worktree recipes, stale commands) → patch the relevant skill under `.claude/skills/*/SKILL.md` or `internal/config/skills/*.md` with **1–3 bullets**, rather than only dumping into AGENTS.md.

## What counts

Include only non-obvious discoveries:

- Hidden relationships between modules
- Execution paths that differ from how code appears
- Non-obvious config, env vars, or flags
- Debugging breakthroughs when errors misled
- API/tool quirks and workarounds
- Build/test commands not obvious from README
- Architectural constraints and files that must change together
- Process failures (wrong tier, skipped smoke, stale skill paths)

## What to skip

- Facts already documented in an AGENTS.md, skill, or README
- Standard language/framework behavior
- Session-only chatter, verbose essays, secrets

## Safety

- NEVER write credentials, tokens, or `.env` contents into AGENTS.md or skills
- Prefer editing existing sections over duplicating
- Keep each insight to 1–3 lines
- Do not rewrite entire skills for one tip — surgical bullets only

## Task

$ARGUMENTS

1. Review this session for discoveries (failed attempts, surprising wiring, missing docs, process misses).
2. Read existing AGENTS.md files and any skill that should have prevented the miss.
3. Create or update the right file(s) with concise bullets (AGENTS.md and/or SKILL.md).
4. Summarize which files changed and how many learnings each gained.
