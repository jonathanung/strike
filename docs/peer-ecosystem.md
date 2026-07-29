# Peer ecosystem imports

Strike maps useful **user-facing** surfaces from peer agent CLIs without
cloning their runtimes. External trees load via discovery roots (see
[agents-skills.md](agents-skills.md)); this page is the inventory of what is
native vs imported vs gap.

Research snapshots (optional, not required at runtime): `.plan/cc`,
`.plan/opencode`, `.plan/codex`, `.plan/grok-build`, `.plan/t3code`,
`.plan/crush`.

Principles: UX-first, adapters over forks, clean-room prompts, no
license-incompatible copy-paste, default product stays lean.

## Inventory matrix

| Peer surface | Upstream (examples) | Strike equivalent | Status |
|---|---|---|---|
| Shipping git chain | CC commit/PR, OpenCode commit | `/commit` `/push` `/pr` `/ship` skills | shipped |
| Project bootstrap | CC `/init` | `/init` builtin → `AGENTS.md` | shipped |
| External agents/skills trees | `.claude`, `.opencode` | discovery merge (#200) | shipped |
| PR/diff review command | CC `/review`, Codex code-review skills | `/review` skill + `reviewer` agent | shipped |
| Session → instructions | OpenCode `learn` | `/learn` skill | shipped |
| AI slop cleanup | OpenCode `rmslop` | `/deslop` skill | shipped |
| Verify / doctor gates | CC doctor-ish, CI babysit | `/verify` skill + `tester` agent | shipped |
| PR babysit | Codex babysit-pr | `pr-babysitter` agent | shipped |
| Plan → act phases | Grok/CC plan mode | `plan-implement` workflow | shipped |
| Review → fix phases | common review loops | `review-fix` workflow | shipped |
| Lifecycle hooks | CC/OpenCode/Crush hooks | config `hooks` (see below) | shipped (schema lean) |
| Compact history | CC/OpenCode compact | `/compact` builtin | shipped |
| Memory / issues | various | `/memory` `/issues` + tools | shipped |
| MCP servers | peers MCP hosts | `mcp.jsonc` / config `mcp` + `/mcp` | shipped |
| Security review pack | CC security-review | gap (use `/review` + focus args) | gap |
| Changelog / translate packs | OpenCode commands | gap (user skills) | gap |
| Node plugin hosts | OpenCode plugins | **out of scope** | wont |
| Full IDE extensions | peers | **out of scope** | wont |

## Built-in skills (this wave)

| Skill | Role |
|---|---|
| `/review` | branch/PR correctness review (no edits) |
| `/learn` | write non-obvious session learnings into AGENTS.md |
| `/deslop` | strip AI style noise from the branch diff |
| `/verify` | run project gates and fix branch-related failures |

Shipping chain remains `/commit` `/push` `/pr` `/ship`. Override any skill via
later discovery roots.

## Built-in workflows

| Name | Phases |
|---|---|
| `plan-implement` | plan (`plan`, user gate) → implement (`build`) |
| `review-fix` | review (`reviewer`, user gate) → fix (`build`, `make test` check) |

`enter_plan_mode` / `exit_plan_mode` drive the default plan workflow. Custom
and extra builtins load from `~/.strike/workflows` and `./.strike/workflows`.

## Hooks alignment

Strike config uses a flat `hooks` array (global then project concatenate).

| Strike | Claude Code-ish | OpenCode / Crush-ish | Notes |
|---|---|---|---|
| `pre_tool_use` | `PreToolUse` | `PreToolUse` / `pre_tool_use` | shell + declarative; **block** only here |
| `post_tool_use` | `PostToolUse` | `PostToolUse` | shell + log/notify |
| `turn_start` | Session/turn start variants | — | declarative only |
| `turn_end` | Stop / turn end variants | — | declarative only |

### Strike schema

```json
{
  "hooks": [
    {
      "event": "pre_tool_use",
      "matcher": "write",
      "action": "block",
      "message": "writes denied in this profile"
    },
    {
      "event": "post_tool_use",
      "matcher": "edit",
      "command": "gofmt -w \"$STRIKE_TOOL_FILE\" 2>/dev/null || true",
      "timeoutMs": 15000
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `event` | `pre_tool_use` \| `post_tool_use` \| `turn_start` \| `turn_end` |
| `matcher` | doublestar over **tool name** (empty/`*` = all); not full regex |
| `action` | declarative: `log` \| `block` \| `notify` (mutually exclusive with `command`) |
| `command` | shell hook: event JSON on stdin; exit allow/block; stdout may inject |
| `timeoutMs` | shell bound (default 30000, max 120000) |

### Mapping notes (not auto-translated)

- Peer **PascalCase** event names → strike **snake_case** (`PreToolUse` → `pre_tool_use`).
- Peer **regex matchers** on tool name → strike **doublestar** (`^bash$` → `bash`, `Edit|Write` → separate entries or `*{edit,write}*` only if that glob fits — prefer explicit rows).
- Crush/CC **env-var rich** payloads differ; strike shell hooks receive JSON on stdin (see engine/tool hook runner). Do not assume `CLAUDE_*` / `CRUSH_*` env names.
- Peer hook **trees** under `.claude/hooks` are not executed as Node hosts — re-express as strike `hooks` JSON or a small shell script.
- Invalid hook rows are **dropped at load** (startup stays up).

## Attribution

Clean-room prompts inspired by common peer UX (Claude Code, OpenCode, Codex,
Crush). No upstream source files are vendored into the binary. External user
trees remain the user's files under discovery roots.
