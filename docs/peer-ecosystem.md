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
| MCP server mode | Codex/CC as MCP clients | `strike mcp-serve` (`strike_task`) | shipped |
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

## Settings inventory

Strike keeps a **lean** config surface (`~/.strike/config` + layered
`mcp.jsonc` / `providers.jsonc` / `keybinds.jsonc`) rather than cloning every
Claude Code `settings.json` or OpenCode `opencode.json` key. Interactive
defaults live under `/settings` → Defaults (and ctrl+d on pickers).

### Two-dial model (sandbox × permission)

Codex-style separation, documented with both dials in [config.md](config.md):

| Dial | Strike | Peer analogues | Meaning |
|---|---|---|---|
| **sandbox** | `sandbox`, `--sandbox`, `/sandbox` | Codex sandbox; CC sandboxing | What OS isolation makes *possible* for bash |
| **permissionMode** | `permissionMode`, `/mode`, Shift+Tab | CC permission modes; OpenCode `permission` | *When* the agent is asked before a tool runs |

They are independent. `yolo` does not disable the OS sandbox; `sandbox: off`
does not skip asks. `yolo` + `sandbox: off` requires `--i-know`.

### Peer → strike map (high-value)

| Peer surface | Upstream | Strike | Status |
|---|---|---|---|
| Default model / provider | OC `model`, CC model | `provider` / `model`, `/provider` `/model`, ctrl+d | shipped |
| Default agent | OC `default_agent` | `defaultAgent`, `/agent` | shipped |
| Theme | OC `tui.json` theme, CC theme | `theme`, `/theme`, `/settings` | shipped |
| Keybinds | OC/CC keybinds | `keybinds.jsonc`, `/keys` | shipped |
| Permissions rules | CC allow/deny, OC `permission` | `permissions[]` last-match-wins | shipped |
| Permission mode dial | CC modes / auto | `permissionMode` + `/mode` | shipped |
| OS sandbox dial | Codex / CC sandbox | `sandbox` + `/sandbox` | shipped |
| Desktop / attention notify | OC TUI attention, CC notify | `notify` (`on`\|`off`\|`unfocused-only`) | shipped (+ `/settings`) |
| Compaction | OC `compaction`, CC autoCompact | `compaction*` / `prune*` keys | shipped (config; not all in `/settings`) |
| MCP servers | both | `mcp.jsonc` + `/mcp` | shipped |
| Custom providers | OC `provider` | `providers.jsonc` + `/settings` Providers | shipped |
| Disable default providers | OC `disabled_providers` | `disable-default-*` | shipped |
| Hooks | both | `hooks[]` | shipped (lean schema) |
| Subagent depth | OC `subagent_depth` | `maxChildDepth` | shipped (+ `/settings`) |
| Permission auto-approve | CC/OC soft auto | `permissionAutoApproveSeconds` / `Exclude` | shipped (+ `/settings`) |
| Session worktree isolation | OC snapshot-ish / CC worktrees | `session.worktree` | shipped (+ `/settings`) |
| Lean / efficiency guidance | — (strike) | `leanCode` | shipped (+ `/settings`) |
| Deferred tool schemas | OC tools gating-ish | `deferTools` + `toolsearch` | shipped (+ `/settings`) |
| Instructions globs | OC `instructions` | AGENTS.md + discovery roots | shipped (different model) |
| Autoupdate | OC `autoupdate` | `strike upgrade` | partial (manual) |
| Formatters | OC `formatter` | gap (use hooks / editor) | gap |
| LSP servers | OC `lsp` | gap — tracked #555 | gap / out of this epic |
| Network allowlist | OC network / CC | gap — tracked #527 | gap / coordinate |
| Managed / MDM settings | CC/OC enterprise | gap | gap (later) |
| JSON schema `$schema` | both | gap | gap (nice DX) |
| Main config JSONC | OC | partial (`mcp`/`providers`/`keybinds` JSONC; main `config` is JSON) | gap |
| Plugins / Node hosts | OC plugins | **out of scope** | wont |

### `/settings` Defaults coverage

Editable: theme, vimMode, nanoMode, mdReadMode, permissionMode,
**permissionAutoApproveSeconds**, **permissionAutoApproveExclude**, **sandbox**,
**notify**, **leanCode**, **deferTools**, **session.worktree**, **maxChildDepth**,
effort.
Read-only (set via pickers + ctrl+d): provider, model, agent.
Providers CRUD: custom OpenAI-/Anthropic-compatible endpoints.

Remaining config-only dials (edit JSON or future `/settings` pages): compaction
knobs, prune knobs, scheduler, hooks, MCP, permissions rules, harnesses.

## Attribution

Clean-room prompts inspired by common peer UX (Claude Code, OpenCode, Codex,
Crush). No upstream source files are vendored into the binary. External user
trees remain the user's files under discovery roots.
