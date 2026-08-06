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

## Project process skills (`.claude/skills`)

Repo-local Claude skills used by agents developing strike (not the same as
user-invoked built-ins like `/commit`). Load via the host `skill` tool:

| Skill | Role |
|---|---|
| `test-and-validate` | Tiered verification (mirrors CI; see root `AGENTS.md`) |
| `write-go-tests` | Author `*_test.go` |
| `smoke` | Offline product happy-path |
| `release` | Annotated tag + GitHub release |
| `issue-handler` / `issue-orchestrator` / `issue-create-and-handle` | Issue → merge farm |
| `tui-components` | `internal/tui/ui` + theme catalog |

Built-in **user** skills shipped in the binary are listed under Skills below
(`internal/config/skills/*.md`).

## Agents

**Agents** (`agents/*.md`) are personas — a system prompt with optional
provider/model/effort pins. Model pins apply on explicit selection (Tab,
`/agent`) and on `task` spawn; **workflow phase transitions** that switch
agent type keep the session model (they do not thrash provider/model).
Effort pins still apply on phase switches unless locked. Shipping built-ins
(override with same-named files under any later discovery root):

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
Optional `model` pins the child’s model (bare id on the current provider, or
`provider/model`) against the same catalog as `/model`; omit to inherit.
Optional `effort` pins the child’s reasoning dial (`off`\|`low`\|`medium`\|
`high`\|`xhigh`\|`max`); omit to inherit the parent (agent effort pins still
apply). When set, task effort wins over agent profile effort.
Optional `name` sets a stable teammate alias on the session team (roster +
messaging).

### Agent teams

**Team = the same session tree by default.** Spawning `task` children joins an
**implicit session team**: the lead (root or coordinating parent) plus its
live/terminal children in that tree. No `TeamCreate` step and no opt-in flag —
parent + children already are the team. Concurrent root sessions stay separate
teams. Depth and fan-out stay bounded (`MaxChildDepth`; orchestrator prefers a
few sequential or small parallel slices).

Parent-only workflows are unchanged: if you never call `agent_*` tools,
`task` / `task_message` / `task_status` / `task_read` / `task_interrupt` behave
as before.

| Tool / event | Role |
|--------------|------|
| `agent_message` / `agent_broadcast` | Mid-flight peer coordination (any teammate: child↔child, child↔lead) |
| `agent_thread` | Read task/delegation-bound message thread |
| `team_task` | Shared claim/assign board (create/list/update/claim/complete; CAS) |
| `[child.completed]` | Finished work product — structured handoff JSON when a child ends |
| `task_message` | Parent→owned-child steer only (not team chat) |
| `task_status` / `task_read` | Rare one-off pulse / transcript slice — **not** busy-poll (`task_status` includes `handoff` when terminal) |
| `agent_roster` | Who is on the team and live state |
| `task_interrupt` | Cancel an owned child |
| `todowrite` / `todoread` | Solo session todo list (full-replace) — **not** multi-agent claim |

**Semantics:** prefer **coordination contracts** on `agent_message` over chatty
status loops: bind `task_id` (team_task or delegation id) and read the thread
with `agent_thread`; set `urgency` (`normal` \| `high` \| `blocker`); use
`kind=request` / `require_ack` with `ack_timeout_seconds` so un-acked peers
emit `agent.contract.timeout` and escalate to the lead (or `escalate_to`);
ack with `kind=ack` + `in_reply_to`. Prefer **completion handoff JSON** for
finished deliverables. Lead should not busy-poll `task_status` — use `wait`,
completion events, inbox, and contracts. Children should message the lead early
when blocked. Mid-flight bodies stay plain text plus optional contract fields.
**Completion** is structured: every terminal child emits a handoff with
`summary`, `files_changed`, `verification`, `findings`, `blockers`, and
`recommended_next_action` (empty arrays/strings allowed). The engine merges
tool-tracked file mutations into `files_changed` and sets `incomplete` when the
child did not supply parseable structured fields. Messages inject at tool-round /
idle turn boundaries (never mid-tool-call). Defaults **allow** team messaging;
out-of-team targets fail closed; config/agent deny rules still hard-block.

#### Example: parallel explore + implement with peer handoff

```
User ↔ Lead (build / orchestrator)
         │  task(name=explorer, agent=explore, prompt="find the package for X")
         │  task(name=implementer, agent=general, prompt="wait for handoff, then implement")
         │
         ├─ explorer  ──agent_message(to=implementer)──►  implementer
         │     "change X in path Y; tests in Z"                 │
         │                                                     ▼
         └─ [child.completed] + inbox  ←── lead synthesizes for the user
```

1. Lead spawns **explore** and **general** in parallel with stable `name`
   aliases (`explorer`, `implementer`).
2. Explorer finds the right package and calls `agent_message` with
   `to: "implementer"` (or the implementer’s `session_id` from `agent_roster` /
   the task result) and a short handoff body.
3. Implementer receives the message at the next safe boundary, implements, and
   finishes; lead sees `[child.completed]` (and any inbox traffic) then answers
   the user.

Use `agent_broadcast` sparingly for team-wide notices; prefer a single
`agent_message` when the recipient is known. See also [usage.md](usage.md#agent-teams).

**Todos vs team board:** use `todowrite`/`todoread` for solo lead multi-step
tracking. Use `team_task` when two or more teammates must see the same board and
claim items (exclusive owner + optional `expected_version` CAS). The board is
keyed by the lead session id and cleared when the lead session ends.

Each model request composes the system prompt in layers (like opencode):

1. **Shared baseline** — identity, ADHD-shaped response contract, doing-tasks
2. **Tools** — effective registry guidance (name + short purpose, recommended use). Reflects agent/permission/depth/MCP; hard-denied tools omitted. Own `/context` provenance layer (`tools` / `registry:effective`). On every stream (including turn 1) the same effective set is bound as provider tool schemas so the model has tools without discovery lag. With config `deferTools: on`, non-core/MCP schemas stay out of `tools[]` until `toolsearch` discovers them (core coding tools remain always-on; see [config.md](config.md)).
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

Task subagents (depth > 0) apply a filtered profile: agent **deny** always
restricts further; agent **allow** upgrades parent Ask→Allow but cannot
override a parent **deny** (so `general` / `tester` `permission.bash: allow`
works when spawned from build, while a read-only parent still blocks write).

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

**Workflows** are ordered (linear) phase sequences. Schema version
`schemaVersion: 1` is the current contract. Documents load from:

| Precedence | Source | Path |
|---|---|---|
| 1 (lowest) | builtin | shipped (`plan-implement`, `review-fix`) |
| 2 | global | `~/.strike/workflows/*.json` |
| 3 (highest) | project | `./.strike/workflows/*.json` |

Project overrides global/builtin by **workflow name**. Same-layer duplicate
names fail closed. Each loaded definition keeps runtime diagnostics: `Source`,
absolute `Path` (disk-backed), and a canonical SHA-256 `Fingerprint` of the
formatted document (for resume/diagnostics — not written to disk).

Decoding is **strict**: unknown JSON fields are rejected. Validation checks
identifiers, phase uniqueness, permission rules, exit gates, and (when agents
are loaded) agent references — reporting every actionable error in one pass.

Scaffolding / formatting / validating never activates a workflow. Activation
is a separate catalog or tool step (`enter_plan_mode`, future `/workflow`).

### CLI

```text
strike workflow scaffold --global|--project <name> [--force]
strike workflow format [--write] <path>...
strike workflow validate <path|dir>...
strike workflow validate --global|--project|--all
```

- **scaffold** requires explicit `--global` or `--project`. Refuses overwrite
  unless `--force`. Writes `<scope>/workflows/<name>.json` only.
- **format** emits deterministic pretty JSON (always includes
  `schemaVersion`). `--write` rewrites in place.
- **validate** strict-decodes, runs structural checks, resolves agent pins
  against loaded agents, and prints short fingerprints on success.

### Phase fields

Each phase may pin an agent, extra prompt context, a permission ruleset, and
an exit gate (authored default / check command). Phase agent pins change
persona/permissions only — the session provider/model stays put (see Agents
above).

**Runtime exit policy** is the session `/autonomy` dial (authoritative for
`phase_done` and `exit_plan_mode`), not the phase's authored `exit.type`:

| Autonomy | Clears when |
|---|---|
| `supervised` (default) | the user approves |
| `agent` | the model calls `phase_done` / `exit_plan_mode` |
| `checks` | phase `exit.command` exits 0 (permission `phase_check`) |
| `skip-all` | immediately (workflow/plan approval only; tool perms unchanged) |

**Permission widening review:** phase profiles are evaluated last, so a phase
`allow` can open an earlier config or agent `deny`. Before activation and every
phase transition, strike computes the effective grant delta and requires
explicit approval (question prompt). Rejection leaves the current phase,
permissions, and context unchanged. `--auto` / `--dangerously-skip-permissions`
auto-accepts widening without a prompt but does not bypass hard sandbox or path
protections. Approved decisions are session-persisted (`phase.grant_approved`)
and restored on resume when the workflow fingerprint and grants are unchanged;
edited workflow content invalidates prior approval. Child engines inherit the
parent’s approved phase ceiling and cannot introduce additional widening.

Authored gate types remain useful documentation and supply `command` for
checks mode:

| Gate `type` (authored) | Meaning |
|---|---|
| `agent` (default) | intended model self-affirmation |
| `user` | intended human approval |
| `check` | intended command gate (`command` required) |

Built-in `plan-implement`:

1. **plan** — `plan` agent, hard-deny `write`/`edit`, user exit gate
2. **implement** — `build` agent, agent exit gate

Built-in `review-fix`:

1. **review** — `reviewer` agent, hard-deny `write`/`edit`, user exit gate
2. **fix** — `build` agent, check gate (`make test`)

### Lifecycle, identity, and resume

Any **validated loaded** workflow can be started (`workflow.start` op / engine
`startWorkflow`). Exactly **one** workflow is active per root session; starting
another replaces the prior after the target is validated. Transitions validate
the target phase **before** mutating context, permissions, agent, or protocol
state. Completion and explicit stop (`workflow.stop`) clear phase context and
phase permissions.

`PhaseChanged` events carry workflow **source**, canonical **fingerprint**,
phase name/index, and the effective **gate** (from `/autonomy`). On session
resume the engine rebinds by name + fingerprint:

| Outcome | Behavior |
|---|---|
| Fingerprint matches (or legacy empty fingerprint) | Restore phase permissions and agent pin |
| Definition missing | Fail-closed `status: missing` — no phase perms until stop/restart |
| Fingerprint or phase identity changed | Fail-closed `status: mismatch` — same |

Plan convenience adapters (`enter_plan_mode` / `exit_plan_mode`, plan-agent
tab sync) still drive the default plan workflow; core lifecycle APIs are
workflow-name generic.

Tools `enter_plan_mode` / `exit_plan_mode` start and advance the default plan
workflow. Leaving plan mode uses a **unified approval + handoff**: pass
`plan_id` + `expected_version` from `plan_write`/`plan_read` (required for new
sessions unless autonomy is `skip-all`, or a bounded `legacy_text` for
pre-feature recovery). The gate runs once under `/autonomy`, the plan is marked
approved, and a `plan.handoff` event records identity + approval source
(`user`|`agent`|`checks`|`skip-all`). `phase_done` and manual agent/permission
dials cannot bypass this path. After handoff, `exit_plan_mode` switches to
**build** (simple) or **orchestrator** (complex): pass `agent`, or omit and
supply `steps` / `areas` / `multi_agent` (heuristic: steps ≥ 4, areas ≥ 3, or
multi_agent → orchestrator). The implementer sees the approved plan on the next
request. The active phase shows as a badge in the TUI header (workflow name,
phase, effective gate, and recovery status when resume fails closed).

Use `/workflow list`, `/workflow inspect <name>`, `/workflow start <name>`, and
`/workflow stop` (also in the command palette) to discover and activate any
loaded definition. Sources are labeled `builtin`, `global`, `project`, or
`plugin`. Start always shows phase-0 permission grants before the engine
mutates state; invalid definitions are listed but cannot be activated. Stop
clears phase context and phase permissions without interrupting unrelated
session history.

Example custom file:

```json
{
  "schemaVersion": 1,
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
