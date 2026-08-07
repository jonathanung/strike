# Config

`~/.strike/config` (global) merged with `./.strike/config` (project), then
optional **managed/MDM** system config (highest). User and project files
accept **JSON or JSONC** (`//` line comments and `/* block comments */`, same
stripper as `mcp.jsonc` / `providers.jsonc` / `keybinds.jsonc`). An optional
top-level `"$schema"` key is **ignored** at load (editor autocomplete only;
Strike never fetches a schema URL at runtime).

**Published JSON Schema (editor DX):** point `$schema` at the versioned file in
this repo (stable `main` raw URL):

```text
https://raw.githubusercontent.com/jonathanung/strike/main/schemas/strike-config.schema.json
```

Local path (clone/checkout): `schemas/strike-config.schema.json`. The schema
documents high-traffic main-config keys (dials, permissions, hooks, sandbox,
compaction/prune, session, scheduler, MCP/LSP shapes, …). Root and nested
objects use **`additionalProperties: true`** so unknown/future keys and
editor-only fields stay valid — matching runtime `encoding/json` (unknown keys
ignored). Alignment with Go structs is best-effort via
`TestStrikeConfigSchemaAlign` (not full codegen). Sidecar files
(`mcp.jsonc` / `providers.jsonc` / `keybinds.jsonc`) are **not** fully schema'd
here yet.

**Load order (later wins for scalars; permission rules concatenate):**

1. Built-in defaults
2. Global `~/.strike/config` (+ `mcp.jsonc` / `providers.jsonc` / `keybinds.jsonc`)
3. Project `./.strike/config` (+ same sidecar files)
4. **Managed/MDM** system `managed-config` (+ `managed-config.d/`) — see
   [Managed / MDM config](#managed--mdm-config-enterprise) below

**Round-trip / save policy:** hand-edited comments and `$schema` are kept on
disk until a **programmatic write** runs (`SetGlobalDefaults`, theme /
presentation / dials / scheduler presets, custom provider upsert/remove,
`AppendProjectPermission`, etc.). Those paths read JSONC, then rewrite
**pretty-printed pure JSON** via `encoding/json` — comments and `$schema` are
dropped. Prefer keeping durable commentary in a sibling note, or avoid
`/settings`-style writers if you need comments to survive. For raw
comment-preserving edits, use **`/config`** (picker opens `~/.strike` /
`./.strike` files in the embedded editor without a JSON round-trip on close).
Programmatic saves only touch user/project paths — they never write managed
files.

**Symlinks:** `~/.strike` and `<project>/.strike` may be directory symlinks
(state lives elsewhere). Strike resolves them before opening history/memory/
issues and before writing config. A file symlink at `~/.strike/config` (for
example stow/dotfiles) is preserved on save — the referent is updated, not
replaced by a plain file.

## Managed / MDM config (enterprise)

For organizations that need centralized policy users cannot override, Strike
loads a **managed** config layer from a system directory (file-based MDM,
same idea as Claude Code `managed-settings.json`). This is **out of scope for
casual users** — leave the directory empty and Strike behaves as before.

### Paths

| Platform | Directory |
|---|---|
| Linux / other Unix | `/etc/strike/` |
| macOS | `/Library/Application Support/Strike/` |
| Windows | `%ProgramFiles%\Strike\` (usually `C:\Program Files\Strike\`) |

Files under that root:

| File | Role |
|---|---|
| `managed-config` / `managed-config.json` / `managed-config.jsonc` | Primary policy (first existing extension wins) |
| `managed-config.d/*.json` and `*.jsonc` | Drop-in fragments, sorted by filename, merged after the primary file |

Hidden drop-ins (names starting with `.`) are ignored. Use numeric prefixes to
control order (`10-sandbox.json`, `20-permissions.json`).

**Test / custom deploy root:** set `STRIKE_MANAGED_ROOT` to an absolute
directory; system defaults are skipped when the env var is set.

Managed files use the **same JSON/JSONC schema** as user config. Typical
enterprise keys:

```jsonc
// /etc/strike/managed-config.jsonc
{
  "sandbox": "read-only",
  "permissionMode": "default",
  "permissionPreset": "dev",
  "permissions": [
    { "permission": "bash", "pattern": "rm -rf *", "action": "deny" },
    { "permission": "write", "pattern": "**/.env", "action": "deny" },
    { "permission": "webfetch", "pattern": "*", "action": "deny" }
  ]
}
```

### What is enforceable

| Control | Behavior when set in managed |
|---|---|
| `sandbox` | Wins over global/project. **CLI `--sandbox` is ignored** so operators cannot loosen OS isolation from the command line. |
| `permissionMode` | Wins over global/project and **session resume**. Mid-session `/mode` / Shift+Tab is **rejected** while locked. |
| `permissionPreset` | Wins over user/project preset selection. |
| `contentGuard.mode` | Wins over global/project. When set to **`deny`**, write-time content guards force deny (cannot be widened by project `off`/`ask`, yolo, or session grants). |
| `permissions[]` | Concatenated after user/project rules (last-match in the config layer). **Deny** rules are also installed as a late evaluation **ceiling** so session always-grants, scoped grants, `--auto` / `--dangerously-skip-permissions`, and workflow phase widens cannot re-allow a managed deny. |

Other managed keys (theme, model, MCP, …) merge with normal last-wins
semantics; only the security dials above are hard-locked against CLI/session
override. Invalid managed files **fail Load** (fail closed) so a broken MDM
push is visible at startup rather than silently dropped.

Strike never writes managed paths. Deploy with your OS package manager, MDM
profile, or configuration management (Ansible, Puppet, …).

## First-time onboarding state

Global (not per-project) acknowledgement lives at
`~/.strike/onboarding.json`:

```json
{
  "version": 1,
  "acknowledged": true
}
```

Interactive TUI launches auto-open `/ftue` while `acknowledged` is false or
the file is missing on a clean install. Finish or dismiss (esc) sets
`acknowledged: true` atomically. An interrupted session that never finishes
or dismisses leaves the file unacknowledged so the wizard can reopen next
launch. Established installs (existing session logs or real provider
credentials) migrate to acknowledged without showing the modal. Precreated
empty `~/.strike` directories alone do not suppress first launch.
`strike exec`, `auth`, `serve`, `version`, and `upgrade` neither display nor
write this file. Manual `/ftue` remains available after acknowledgement.

```jsonc
// ~/.strike/config or ./.strike/config — JSONC comments allowed
{
  // Optional editor hint; ignored by Strike at load (no network fetch)
  "$schema": "https://raw.githubusercontent.com/jonathanung/strike/main/schemas/strike-config.schema.json",
  "provider": "anthropic",
  "model": "claude-sonnet-5",
  "effort": "high",
  "defaultAgent": "build",
  "leanCode": "lite",
  "deferTools": "on",
  "theme": "strike",
  "vimMode": "pane",
  "nanoMode": "pane",
  "mdReadMode": "embedded",
  "notify": "unfocused-only",
  "autoupdate": "notify",
  "permissionMode": "default",
  "sandbox": "workspace-write",
  "network": {
    "allow": ["api.github.com", "*.npmjs.org", "10.0.0.0/8"]
  },
  "container": {
    "execution": "local",
    "baseImage": "ubuntu:24.04",
    "packages": [],
    "shell": "/bin/bash",
    "resources": { "memory": "", "cpus": "", "pidsLimit": 512, "gpus": "" },
    "workspace": { "mountPath": "/workspace", "ports": [], "persistHome": true },
    "auth": {
      "forwardEnv": ["ANTHROPIC_API_KEY", "OPENAI_API_KEY", "STRIKE_*"],
      "forwardSSHAgent": false
    },
    "network": { "mode": "default", "allow": [] },
    "engine": ""
  },
  "permissionAutoApproveSeconds": 0,
  "permissionAutoApproveExclude": ["bash"],
  "maxChildDepth": 0,
  "toolRetry": {
    "maxAttempts": 3,
    "baseDelayMs": 200,
    "maxDelayMs": 2000,
    "loopThreshold": 3
  },
  "compactionStrategy": "trim",
  "compactionModel": "",
  "compactionThreshold": 0.70,
  "compactionBuffer": 4096,
  "keepUserTurns": 2,
  "pruneProtectTokens": 40000,
  "pruneMinimumTokens": 20000,
  "pruneKeepUserTurns": 2,
  "pruneProtectTools": [],
  "session": {
    "worktree": "off",
    "worktreeCleanup": "keep",
    "overlapPolicy": "warn",
    // Optional durability retention hooks (#803). Zero / omitted = unlimited.
    // Applied via session.ApplyRetention (not automatic on every launch).
    "retentionMaxSessions": 0,
    "retentionMaxAgeDays": 0,
    "retentionMaxBytes": 0,
    // Timeline / trace storage bounds (#810). Coordinates with retention* above.
    "timelineMaxEntries": 0,
    "timelineArgsPreviewMax": 0,
    "timelineOutputPreviewMax": 0,
    "timelineBlobSpill": false,
    "traceRetentionMaxFiles": 0,
    "traceRetentionMaxAgeDays": 0,
    "traceRetentionMaxBytes": 0,
    "delegationPolicy": {
      "mode": "enforce",
      "tinyPromptRunes": 280,
      "maxPathsLocal": 1,
      "maxLiveChildren": 0
    },
    "agentBudget": {
      "maxWallClockS": 0,
      "maxTokens": 0,
      "maxCostUsd": 0,
      "maxToolCalls": 0,
      "maxDangerousTools": 0,
      "stallAfterS": 0,
      "loopDetectN": 0
    }
  },
  "scheduler": {
    "presets": ["cargo", "npm"],
    "limits": {
      "process": 8,
      "build": 2,
      "test": 4,
      "model": 3,
      "container": 1
    },
    "commands": [
      { "pattern": "go test *", "class": "test" },
      { "pattern": "go *", "class": "build" },
      { "pattern": "make *", "class": "build" }
    ]
  },
  "permissionPreset": "dev",
  "permissions": [
    { "permission": "bash", "pattern": "go *", "action": "allow" },
    { "permission": "write", "pattern": "**/*.env", "action": "deny" }
  ]
}
```

Rules concatenate across layers; the last matching rule wins, so project
config overrides global, managed overrides both for rules present there, and
session "always" grants override user/project — but **not** managed denies
(see evaluation order).

**Evaluation order (last-match-wins):** defaults → optional
`permissionPreset` → `permissions[]` (global then project then managed) →
optional `--dangerously-skip-permissions` allow-all → project runtime grants
→ active agent profile → session always grants → scoped TTL grants →
permission-mode late denies (plan) → workflow phase profile → **managed deny
ceiling** → mode ask-upgrade (yolo / accept-edits only upgrade remaining
Ask→Allow; never widen Deny).

**Permission presets (`permissionPreset`):** shipped named rulesets inserted
after defaults and before `permissions[]`. Empty means no preset layer.
Inspect with `/permission presets`.

| ID | Behavior |
|---|---|
| `read-only` | Allow read/search/LSP; **deny** write, edit, bash, webfetch, websearch, mcp, hooks |
| `dev` | Allow common local-dev bash (`go *`, `git status/diff/log/show`, `make test*`); deny force-push and `.env` writes; other mutations stay ask |
| `yolo-with-sandbox` | Rule-level allow-all (`* *` allow). Does **not** turn off OS sandbox — keep `sandbox` at `workspace-write` or `read-only`. Later deny rules still win. Distinct from `permissionMode: yolo` |

**Admission (`admission`):** register/load-time scans for MCP servers, skills,
and plugins **before** tools bind / skills enter the catalog. Distinct from
`permissionPreset` (per-call rules) and `sandbox` (OS isolation). Full matrix,
scanners, and fail-open/closed behavior: [admission.md](admission.md).

```jsonc
{
  "admission": {
    "preset": "default",              // permissive | default | strict
    "allowPaths": ["~/trusted-skills"] // home-anchored only; bare ".strike/…" rejected
  }
}
```

| Preset | High-risk MCP tool shapes | Scanner errors |
|---|---|---|
| `permissive` | warn (critical → quarantine) | warn (fail-open) |
| `default` | quarantine (critical → block) | warn (fail-open) |
| `strict` | **block** before bind | **block** (fail-closed) |

**Explain:** `/permission explain <tool> [pattern]` (or the
`permission.Explain` / `Service.Explain` API) returns the effective action,
matched rule, layer name, and match trail for a sample tool call. For bash
(and selected tools), explain also reports whether the decisive match used
**action facts** or the raw **pattern** path (`eval=facts` / `eval=pattern`)
plus a short fact summary (#888). See [isolation.md](isolation.md#action-facts-semantic-permission-projection-888).

**Action facts + last-match-wins:** when a bash command parses completely,
rules may match semantic keys (e.g. inner `rm *` inside `bash -c '…'`, path
`**/.env`, `host:example.com`) in addition to the raw command string. Each
rule uses **either** facts or pattern — not both — so deny cannot double-fire.
Incomplete parses (expansions, `eval`, opaque scripts) never drive deny via
facts; legacy pattern matching alone applies.

**Dry-run preset:** `/permission explain --preset <id> <tool> [pattern]`
evaluates under an alternate shipped preset without mutating the session
(`ExplainPreset`). Useful for “what would `read-only` do on this call?”

**Diff:** `/permission diff <presetA> <presetB>` lists added/removed/changed
rules with layer labels (`permission.DiffPresets`). HTTP:
`GET /v1/permissions/diff?left=&right=`, and explain accepts optional
`preset=` for dry-run.

**Managed ceiling:** explain output notes when the managed/MDM deny layer
blocks a widen (stricter than the stack without managed). Sandbox dial and
`network.allow` appear on the same explain surface when the host provides them.

**Scoped approvals:** runtime grants may be bounded by scope and optional
wall-clock TTL (`session`, `path-prefix`, `tool`, `command-class`). A scoped
grant that would override a parent **Deny** is rejected (does not silently
widen). Session always / project decisions remain the TUI reply path;
programmatic `Service.Grant` is the scoped+TTL API.

**Audit trail:** hard **deny**, **ask** suspend, and user **reply** outcomes
emit `permission.decided` (plus `permission.asked` / `permission.resolved`
when the user is prompted). Synchronous allows are not audited (avoids
flooding session JSONL on high-frequency read/search tools). `/timeline`
folds audit events into `kind: permission` entries with redacted patterns
(see [secrets.md](secrets.md) / `pkg/redact`).

**Two-dial model (Codex mental model):**

| Dial | Config / CLI | Controls |
|---|---|---|
| **sandbox** | `sandbox`, `--sandbox` | What OS isolation makes *possible* for bash (`off` \| `read-only` \| `workspace-write`) |
| **permissionMode** | `permissionMode`, `/mode`, Shift+Tab | *When* the agent is asked before a tool runs |

They are independent. Turning off permission prompts (`yolo`) does not disable
the OS sandbox; setting `sandbox: off` does not skip permission asks.

**Isolation matrix:** OS sandbox vs session worktrees vs planned containers and
optional process mem/CPU caps — [isolation.md](isolation.md).

**OS sandbox dial:** `sandbox` is `off` | `read-only` | `workspace-write`
(default **`workspace-write`**). Applies to the bash tool (and composer `!`
shell) via Linux `bwrap` / macOS `sandbox-exec`. When the backend is missing
or blocked, bash **fails closed** with `sandbox_denied` unless
`sandboxAllowDegrade: true` (explicit unsandboxed fallback) or `sandbox` is
`off`. Override per invocation with `--sandbox <mode>`. Inspect the effective
policy with `/sandbox`; `/sandbox explain` prints degrade policy, egress
enforcement, and the generated OS profile (bwrap flags or seatbelt SBPL)
compiled from permission rules.

**Shared writable roots (default usability):** under any non-`off` mode the
sandbox keeps common scratch dirs writable (`/tmp`, `/var/tmp`, `$TMPDIR`,
and on Linux `/dev/shm`). In **`workspace-write`** it also binds user/tool
caches when present (`$XDG_CACHE_HOME` or `~/.cache`, `$GOCACHE` /
`$GOMODCACHE`, `~/.npm`, `~/.cargo`, `~/.rustup`, …) so `go build`, package
managers, and `2>/dev/null` redirections work without disabling isolation.
The bash static path guard allows the same roots (and safe devices like
`/dev/null`) outside the workspace; critical targets (`/`, `/tmp` as a whole,
`$HOME`, …) stay blocked. The rest of the host filesystem remains read-only
aside from the session workdir.

**Permission → sandbox profile:** hard `write`/`edit` deny rules are compiled
into OS filesystem denials inside the bash sandbox (globs become seatbelt
regexes and, when paths exist, bwrap `--ro-bind` remounts). A deny on
`write`/`edit` `*` (including plan mode) suppresses the writable workspace
bind. Network inside the sandbox stays **on** by default (so bash can run
`gh`, `git`, package managers, etc.). It turns **off** only when both
`webfetch`, `websearch`, and `mcp` are all hard-**deny** on `*` (patterned
rules do not flip full-network posture). Ask/yolo posture does not widen the
OS profile. Composer `!` uses the config-layer compile; agent bash uses live
layers (agent/phase/session).

**Network allowlist (`network.allow`):** optional host/CIDR whitelist for
**application-layer** egress. Entries may be hostnames (`api.github.com`), a
single leading wildcard label (`*.example.com`), IP literals, or CIDRs
(`10.0.0.0/8`). Empty or omitted means unrestricted **public** hosts (existing
SSRF blocks for private/loopback/link-local/CGNAT still apply on webfetch and
cannot be opened by the allowlist). When non-empty, destinations must match an
entry: hostname/wildcard on the name, or IP/CIDR on the literal host or (when
the list includes IP/CIDR entries) a resolved address. Webfetch redirects and
dial-time resolution are re-checked. Global and project layers: when a layer
sets `network.allow` (including `[]`), it **replaces** the previous list so a
project can tighten or clear a global whitelist.

This is the same policy **shape** for webfetch, bash preflight, and future
container filters. It is **not** a third independent system:

| Surface | What applies today |
|---|---|
| `webfetch` | `network.allow` host/CIDR allowlist + SSRF private blocks |
| `websearch` | `network.allow` on the search API host + SSRF private blocks; result domain filters are separate tool args |
| bash preflight | Fail-closed when allow is non-empty and OS host net is on: known clients (`curl`/`wget`/`ssh`/…) must match allow; interpreters, `/dev/tcp`, package network subcommands, and unknown binaries → `network_denied`. Skipped when OS `NoNetwork`. **Not** a full shell/network proxy. |
| bash OS profile | host networking on by default (`Policy.NoNetwork` zero value / `NetworkEnabled()`); off only when `webfetch`, `websearch`, and `mcp` are all hard-deny on `*` (all-or-nothing; **no** per-host filter inside bwrap/seatbelt; no Windows host filter) |
| permission rules | `webfetch` / `websearch` / `bash` ask/allow/deny patterns (prompt posture), independent of the hard allowlist |
| container net | `container.network.mode` (`default`\|`none`); `container.network.allow` reserved (same host/CIDR shape as `network.allow`) |

**Sandbox degrade (`sandboxAllowDegrade`):** default `false`. When `true`, a
missing OS backend may run bash unsandboxed (with a one-shot warning). When
`false`, non-off sandbox modes return `sandbox_denied` instead of silent
degrade (#1030).

**Bash secrets (`bashSecrets`):** map of env var name → secret ref injected
into the bash tool process only:

```jsonc
{
  "sandboxAllowDegrade": false,
  "bashSecrets": {
    "GITHUB_TOKEN": "secret://env/GITHUB_TOKEN"
  }
}
```

Bash does **not** inherit the full Strike environment (minimal documented
keys only — PATH, HOME, locale, common toolchain roots). Secret values never
appear in events or logs. See [secrets.md](secrets.md).

`/sandbox explain` prints the effective allowlist, degrade policy, an
`egress enforcement:` line, and bash `network: on/off`. Prefer `webfetch` for
ordinary page fetches; use allowlisted curl when agents still shell out.

**Web search (`webSearch`):** configures the `websearch` tool backend. Search
discovers titles/URLs/snippets; use `webfetch` to retrieve a selected page.
Provider-neutral shape:

```jsonc
{
  "webSearch": {
    "provider": "brave",           // currently "brave"; empty = auto
    "apiKeyEnv": "BRAVE_API_KEY",  // env var holding the API key
    "baseURL": ""                  // optional API base override (tests/proxies)
  }
}
```

| Field | Meaning |
|---|---|
| `provider` | Backend id. `brave` is supported. Empty auto-selects brave when its key env is set. |
| `apiKeyEnv` | Environment variable for the API key (default `BRAVE_API_KEY` for brave). |
| `baseURL` | Optional API origin override (default `https://api.search.brave.com`). |

When no backend is configured (no provider/key), `websearch` returns a
structured `precondition_failed` error with setup steps — it does not scrape
the open web as a silent fallback. Project layers replace the whole
`webSearch` object when any field is set. Permission default is **ask** (same
family as `webfetch`). Queries and snippets pass through the usual tool-args /
tool-output redaction and session audit paths.

**Write-time content guards (`contentGuard`):** scan proposed file content on
`write` / `edit` / `apply_patch` / `notebook_edit` **before** disk commit.
Distinct from egress redaction (`pkg/redact` on logs/timeline/tool results —
see [secrets.md](secrets.md)). Shared credential patterns via
`redact.Findings`; plus high-confidence dangerous sinks (language-limited v1).

```jsonc
{
  "contentGuard": {
    "mode": "default",                 // off | default | ask | deny
    "pathAllow": ["**/testdata/**"]     // doublestar globs; skip guard
  }
}
```

| Mode | Behavior |
|---|---|
| `default` (empty) | Credential shapes **deny**; dangerous sinks **ask** |
| `ask` | All findings raise permission `content_guard` (allow-once / path always-grant; audited) |
| `deny` | All findings hard-deny with error code `content_guard_denied` |
| `off` | Scanner disabled (unless managed forces deny) |

`pathAllow` replaces per layer when set (including `[]`). Managed
`contentGuard.mode: deny` installs a ForcedDeny ceiling that project `off`/`ask`,
`pathAllow`, yolo, and session grants cannot widen. Yolo only upgrades remaining
**ask** findings — never deny-severity hits. Resulting file content is evaluated
(not only patch hunks). Optional skill `/write-guards` documents the rules for
the model.

**Yolo + sandbox off:** `permissionMode: yolo` (or a resumed session in yolo)
combined with `sandbox: off` **refuses to start** unless you pass `--i-know`.
Mid-session `/mode yolo` is also rejected while sandbox is off without that
startup override. This is the only supported way to run with neither OS
isolation nor permission prompts.

**Not a security boundary alone:** permission rules and modes (including
`yolo` / `--auto` / `--dangerously-skip-permissions`) only control whether the
agent is *asked* before a tool runs. Prefer keeping `sandbox` at
`workspace-write` (or `read-only`) so OS isolation still applies. The bash
tool also applies a separate best-effort static path guard on a small set of
destructive command forms; that guard is incomplete and must not be treated as
isolation. Linux glob denials expand existing paths at compile time (new files
matching a deny glob are covered on the next compile / seatbelt regex on
macOS).

**Permission mode dial:** `permissionMode` sets the default tool-permission
posture for **new** sessions: `default` | `plan` | `soft-approve` |
`accept-edits` | `yolo` (see [usage.md](usage.md)). Session changes via
Shift+Tab or `/mode` persist in the session JSONL, not back into this file.
Distinct from `/autonomy` (workflow exit gates) and from `sandbox` (OS
isolation).

**User system prompt:** `systemPrompt` is optional user text that **replaces**
(not appends) a composition slot. Whitespace-only values are ignored (they do
not blank the overlay).

| `systemPromptMode` | Behavior |
|---|---|
| `overlay` (default) | Replace the provider overlay only; shared baseline stays |
| `defaults` | Replace **shared + provider** with `systemPrompt`; tools, environment, instructions, memory, and ledger still append |

**Precedence** for the overlay/defaults slot: custom agent persona body
(`agents/*.md`) **wins over** config `systemPrompt`, which **wins over** the
built-in provider overlay. When a persona wins, shared baseline still applies
even if `systemPromptMode` is `defaults` (defaults mode only applies when the
config prompt is the active slot content). Visible in `/context` and `/diag`
as kind `config` with source `config:systemPrompt+mode:overlay|defaults`.
Details: [agents-skills.md](agents-skills.md#system-prompt-layers).

**Lean code:** `leanCode` is `off` | `lite` (default) | `full`. Injects
agent-scoped efficiency guidance into the system prompt (strict ladder for
build/general/debugger; softer scaling-aware lean for plan/orchestrator;
none for explore/reviewer/tester/validator/commit). Inspired by
[ponytail](https://github.com/DietrichGebert/ponytail) (clean-room wording).
Details: [agents-skills.md](agents-skills.md#lean-code-ponytail-lite).

**Deferred tool schemas:** `deferTools` is `on` (default) | `off`. When
`on` (or unset), non-core tools are omitted from the provider `tools[]` array
until `toolsearch` discovers them, the model calls them by name, or
deterministic workflow activation promotes them. Core coding tools stay always
available: `read`/`glob`/`grep`/`edit`/`write`/`apply_patch`/`move`/`delete`/
`bash`, progressive `task`, `toolsearch`, and `question`. Deferred surface
includes compatibility delegation shims (`delegate`, `task_status`,
`task_read`, `task_message`, `task_interrupt`, `wait`), team coordination
(`agent_roster`, `agent_message`, `agent_broadcast`, `agent_thread`,
`agent_ownership`, `team_task`, `patch_collab`), plan tools (`plan_write`,
`plan_read`, `plan_delegate`, `enter_plan_mode`, `exit_plan_mode`,
`phase_done`), optional built-ins (`webfetch`, `websearch`, todo/memory/issue,
`sleep`, `skill`, `notebook_edit`, …), and all `mcp_*` tools. Discovery lives
on the process registry: matches from `toolsearch` load full schemas on the
**next** model request (including the next iteration of the same turn’s tool
loop). Tools already present as assistant tool calls in history are
re-promoted on each stream (so `--continue` keeps schemas for tools used
earlier). Set `"deferTools": "off"` in global or project config to expose the
full permitted registry.

**Progressive `task` schema:** the unified `task` tool starts with a compact
basic schema (prompt-only create plus `status` / `wait` / `cancel`). The full
advanced contract (routing, budget, verify, `context_bundle`, lifecycle
`get`/`list`/`read`/`message`/`transition`, …) loads after `toolsearch`
matches `task`, a call uses advanced fields/actions, or workflow activation
promotes it. Providers always see a single tool named `task`; the executor
accepts the full argument surface regardless of the schema level currently
advertised. Session resume restores advanced when history used advanced args.

**Workflow tool activation:** when `deferTools` is on, deterministic engine
state promotes deferred families without `toolsearch` or a classifier:
plan mode / plan agent / active workflow / active plan handoff → plan tools
(`plan_write`/`plan_read`/`plan_delegate`, `enter_plan_mode`/`exit_plan_mode`/
`phase_done`); any live or historical child → roster/messaging/ownership plus
advanced `task`; two or more live children → team tools (`agent_broadcast`,
`team_task`, `patch_collab`). Hard-denied tools stay omitted. Guidance source
tags include `+activate:<families>`.

**Rollback / permanent default:** progressive disclosure (`deferTools` on) is
the shipped default. Offline comparison lives in
`go test ./internal/eval/progressive` and `evals/progressive/README.md`. Roll
back to `"deferTools": "off"` if progressive completion drops by more than 5
absolute points or median wall time rises by more than +25% vs full exposure
on that fixture pack (schema reduction below 30% on solo first-turn is a soft
warning only).

**Permission soft-approve / auto-approve:** session mode `soft-approve`
(`permissionMode`, `/mode`, Shift+Tab) arms a **visible** 15s countdown on
permission asks and submits **allow once** at zero if the user does nothing.
Esc, reject, or any explicit once/session/project choice cancels the timer.
Hard deny rules always win. Queued/hidden asks (behind another modal) do not
count down or auto-approve. Disabled by default (mode `default`, seconds `0`).

`permissionAutoApproveSeconds` (1–60) optionally sets/overrides the countdown
duration without selecting soft-approve mode; when soft-approve is active and
seconds is unset/`0`, the default is **15**. Names in
`permissionAutoApproveExclude` (case-insensitive) never auto-approve.
Both are editable under `/settings` → Defaults (auto-approve applies to the
current session immediately).

**Max child depth:** `maxChildDepth` bounds nested `task` tool spawns (root
depth 0). Zero/unset means the engine default (**1**: children cannot spawn
further tasks). Values above **8** clamp to 8. Editable under `/settings` →
Defaults; takes effect for **new** sessions (already-running engines keep their
bound).

**Tool retry / error recovery:** `toolRetry` controls harness auto-retry and
loop detection for tool dispatch (issue #795). Policy is **error code ×
idempotency** (see `internal/tool/retry.go`):

| | `safe-retry` | `conditional` | `unsafe` |
|---|---|---|---|
| `transient` / `timeout` | auto-retry + backoff | fail (no blind mutation retry) | fail |
| `precondition_failed` | recover hint | recover hint | fail |
| other codes | fail | fail | fail |

Mutative tools (`edit` / `write` / `apply_patch` / `bash` / …) never
auto-retry on generic or transient failure — that prevents double-apply.
Provider stream retries remain separate (`MaxStreamAttempts` in the engine).

| Key | Meaning | Default |
|---|---|---|
| `toolRetry.maxAttempts` | attempts per tool call including the first; `1` disables auto-retry | `3` |
| `toolRetry.baseDelayMs` | first backoff step (full jitter applied) | `200` |
| `toolRetry.maxDelayMs` | backoff cap | `2000` |
| `toolRetry.loopThreshold` | identical consecutive failing tool+args before the turn stops with `loop_detected` | `3` |

When the loop detector trips the engine emits `tool.loop_detected`, settles the
tool as blocked, and ends the turn (`stopReason: loop_detected`). Auto-retries
emit `tool.retrying` (timeline) before each backoff sleep.


## Desktop notifications (`notify`)

When the terminal is unfocused, strike can ring the bell and emit OSC 9
desktop notifications for **needs attention** (permission / question) and
**long turn complete** (≥30s). Notification text is fixed labels only — never
paths, prompts, or secrets.

| Value | Behavior |
|---|---|
| `unfocused-only` (default) | notify when unfocused; if the terminal never reports focus, use the same path for attention + long turns |
| `on` | always notify (attention + long turns), even when focused |
| `off` | never notify |

Unknown values are ignored at load time.

## Autoupdate (`autoupdate`)

Startup (and at most once per 24h) GitHub Releases check that reuses the same
release metadata path as `strike upgrade` / `/upgrade`. The probe is async and
time-bounded so TUI startup is not blocked on the network. Offline, rate-limit,
and API failures stay silent.

| Value | Behavior |
|---|---|
| `notify` (default) | when a newer release exists, show status chrome + optional desktop notify; path is `/upgrade` or `strike upgrade` |
| `off` | no startup release check |
| `auto` | opt-in: when the binary is writable, download+replace in place (no re-exec); otherwise same as `notify` with a Nix/package-manager hint |

**Default never replaces the binary** — only `auto` may. Nix store installs and
other non-writable binaries never attempt replace; the notice tells you to
update the flake/lock input or re-run the install script. Windows self-update
remains unsupported (same as manual upgrade).

Probe state is cached under `~/.strike/cache/update-check.json`. Editable under
`/settings` → Defaults.

## Container (native containerization, E12)

Layered JSON for `internal/container` (epic
[#547](https://github.com/jonathanung/strike/issues/547)). Merge order matches
the rest of config: **defaults → global → project → managed**.

| Source | Path |
|---|---|
| Inline | `"container": { … }` in `~/.strike/config` or `./.strike/config` |
| Dedicated file | `container.jsonc` / `container.json` under the same `.strike` roots (like `mcp.jsonc`) |

Dedicated files overlay the inline block at the same layer (global file after
global config; project file after project config).

| Field | Meaning |
|---|---|
| `execution` | `local` (default) or `container` — where the agent runs (CLI flag in E12.4) |
| `baseImage` | Dockerfile `FROM` (default `ubuntu:24.04`) |
| `packages` | Extra apt packages at build |
| `shell` | Login shell (default `/bin/bash`) |
| `resources` | `memory`, `cpus`, `pidsLimit`, `gpus` → create flags |
| `workspace` | `mountPath`, `hostPath`, `ports` (`host:container`), `persistHome`, `extraBinds` |
| `auth` | `forwardEnv` globs, `envFile`, `requiredEnv`, `forwardSSHAgent` (credentials never baked into images) |
| `network.mode` | `default` (bridge) or `none` |
| `network.allow` | Reserved container egress allowlist (same shape as top-level `network.allow`) |
| `dockerfile` | Optional hand-written Dockerfile path |
| `engine` | Override CLI binary (`docker` / `podman` / absolute path) |
| `needsNode` / `nodeVersion` | Install Node via NodeSource (`nodeVersion` major, default 22) |
| `needsPython` / `pythonVersion` | Install Python apt packages (default version `3`) |
| `needsGo` / `goVersion` | Install `golang-go` (+ build deps); `goVersion` is informational |
| `needsRust` | Install Rust via rustup |

Scaffold with `/devcontainer` or `strike container detect` (E12.5).

Runtime mapping: `Config.Container.ToRuntime(version)` → `container.Config` for
`Manager`. See [container.md](container.md) and [isolation.md](isolation.md).

## Scheduler (in-process resource limits)

`scheduler` bounds concurrent agent work **inside one Strike OS process**.
Separate `strike` programs do **not** coordinate leases or share capacity —
each process applies its own effective limits independently.

| Field | Meaning |
|---|---|
| `presets` | Ordered list of shipped build-system preset IDs (see below) |
| `limits` | Map of pool name → positive integer capacity |
| `commands` | Ordered list of `{ "pattern", "class" }` classification rules |

**Pools:** `process`, `build`, `test`, `model`, `container`. Omitted keys keep
the lower config layer's value; when no layer sets a pool, that pool is
**unlimited** (same as today's default). An explicit `0` or negative capacity
**fails config load** with the file path — use omission for unlimited, not
zero.

**Layering:** project `limits` override global **per pool**. `presets` and
`commands` concatenate (global then project; duplicate preset IDs keep the
first). Malformed patterns, unknown classes, or unknown/duplicate preset IDs
in one file fail load before the engine starts and name the source file and
index.

**Presets:** versioned bundles for common resource-heavy tools. At compile
time each selected ID expands into ordinary suggested `limits` and `commands`
(no second runtime matcher). Expansion order follows the shipped catalog
order among the selected IDs (not the order written in config). Then:

1. User/project `limits` overlay preset-suggested capacities per pool.
2. User/project `commands` append after expanded preset rules (last-match-wins,
   so a later user rule can reclassify a preset pattern).

Shipped preset IDs: `cmake`, `ninja`, `gradle`, `bazel`, `maven`, `cargo`,
`npm` (covers npm/yarn/pnpm/bun). Each has a stable ID, display name,
rationale, default class, and inspectable generated rules (see
`scheduler.Catalog` / host `SchedulerPresets`). Expanded rule provenance is
`preset:<id>@v<version>` in `Effective.Report()`. The `/ftue` setup wizard can
checkbox-select these presets and write the global `presets` list atomically
(custom `limits`/`commands` are preserved; skip leaves config unchanged).

**Command classification:** each rule's `pattern` is a full-string glob over
the submitted shell command (`*` = any run of runes, `?` = one rune, `\`
escapes the next byte). Matching is case-sensitive. `class` is `general` |
`build` | `test`. Evaluation is **last-match-wins**: every matching rule is
considered in order; the last match's class wins. When nothing matches, the
class is `general`. Multiple matches are therefore resolved by rule order
(project rules append after global, so a later project rule can reclassify).

Admission wiring uses the compiled policy:

- **Model streams** (ordinary turns, child turns, concurrent roots, harness
  calls, and compaction summarize) acquire the `model` pool for the duration of
  each `Provider.Stream` attempt. The lease is released when the stream is
  fully drained, before retry backoff, and reacquired fairly on the next
  attempt. Omitted/`unlimited` model capacity preserves pre-scheduler behavior.
- **Bash** acquires `process` after permission approval and before process
  start (command timeout begins after admission). `build` / `test` classes
  acquire those pools in addition via the scheduler's multi-pool path.
  Omitted limits stay unlimited (no wait — same as pre-scheduler behavior).

**Queue lifecycle (protocol / session JSONL):** when a caller **blocks** on
capacity, the engine emits `scheduler.queued` with correlation, a stable
`requestId`, the constrained `pools`, and a short `label` (`model`, `bash`,
`bash:build`, …). Grant then emits `scheduler.admitted` (with `waitMs`); cancel
or scheduler close emits `scheduler.canceled` (`reason` `canceled`|`closed`).
Immediate grants (unlimited pools or free capacity) emit **no** queue events,
so default sessions stay quiet. After `canceled` for a `requestId`, `admitted`
never follows. Exact queue positions are **not** on the wire — FIFO is internal
and may change; UIs show the pool and label only. Replay reconstructs
queued→admitted or queued→canceled so waiting roots/children are not mistaken
for idle. Task status, team roster, and the activity/agents panes project these
events without importing scheduler internals.

**Interaction with turn cancel and `task_interrupt`:** parent `interrupt`
cancels the active parent turn (and any in-flight model/bash acquire on that
turn's ctx) but does **not** cancel non-blocking children. `task_interrupt`
targets one child session's turn ctx only. A canceled waiter never holds a
lease; an admitted bash/model call releases its lease on exit (including
timeout and cancel). See [ARCHITECTURE.md](ARCHITECTURE.md#cancellation-deadlines-and-backpressure)
for process-group kill, partial output, and engine queue backpressure.

Inspect the compiled policy via `Config.SchedulerEffective()` /
`Effective.Report()`.

Example — cargo preset plus global caps with a project test override:

```json
// ~/.strike/config
{
  "scheduler": {
    "presets": ["cargo"],
    "limits": { "process": 8, "build": 2 },
    "commands": [
      { "pattern": "go *", "class": "build" }
    ]
  }
}

// ./.strike/config
{
  "scheduler": {
    "limits": { "build": 4, "test": 2 },
    "commands": [
      { "pattern": "go test *", "class": "test" },
      { "pattern": "cargo test *", "class": "general" }
    ]
  }
}
```

Effective: `process=8`, `build=4` (project overlays preset/global), `test=2`
(from project; cargo preset also suggests test capacity when not overridden),
other pools unlimited. `cargo build` → `build` (preset); `cargo test --lib` →
`general` (project rule wins over preset); `go test ./...` → `test`;
`go build .` → `build`.

## Session worktrees

When concurrent root sessions would otherwise share one working tree, strike
can bind each session's tool CWD to its own `git worktree` under
`<repo>/.strike/worktrees/<session-id>/` (gitignored via `*/worktrees`).

| `session.worktree` | Behavior |
|---|---|
| `off` (default) | launch cwd; no isolation |
| `auto` | worktree when a second root session starts in-process |
| `always` | every new root session gets a worktree (git repos only) |

| `session.worktreeCleanup` | Behavior |
|---|---|
| `keep` (default) | leave the worktree and branch after session close |
| `delete` | `git worktree remove` + delete the branch on close |

CLI: `strike --worktree` forces a worktree for that invocation (same as always
for one session). Non-git directories soft-fail: the app launches on the launch
cwd and shows a dismissible modal (TUI) or stderr line (exec) explaining that
no git repository was detected. Other `git worktree add` failures still return
a clear error and do not leave a half-bound session. Project-scoped state
(history, memory, issues) stays keyed to the main repo, not the worktree path.
Tools (`bash`, `read`, `write`, …) resolve paths inside the session worktree.
Each `bash` invocation is a fresh process whose cwd is that session workdir
(workspace root, or the bound git worktree root). A `cd` inside one command
does not affect later bash calls or other tools; chain with `&&` or
`(cd subdir && …)` when a single command needs a subdirectory.

### Root-turn deadline (`session.turnTimeoutS`)

Each root user turn gets an independent wall-clock deadline so a stuck provider
stream or long-running tool cannot retain the active turn indefinitely.

| `session.turnTimeoutS` | Behavior |
|---|---|
| omitted / `0` | **default 1800** (30 minutes) — bounds unattended runs without breaking ordinary builds |
| positive integer | that many seconds per root turn |
| negative (e.g. `-1`) | disabled — cancel only via Interrupt / parent context |

CLI: `strike --turn-timeout 30m` (or `1h`, `1800s`, plain seconds). Use
`--turn-timeout off` (or `0` / `none`) to disable for that invocation. CLI
overrides config.

**Interaction:** expiry cancels the turn context (provider stream drain, in-flight
tools, bash process groups, scheduler waiters) the same way as other turn
deadlines — see [ARCHITECTURE.md](ARCHITECTURE.md#cancellation-deadlines-and-backpressure).
Terminal events use `stopReason=timeout` and `EngineError` code `timeout`,
distinct from user `interrupted` and provider `error`. Partial tool output stays
structurally valid in history. **Resume / `--continue`:** each new turn applies
the configured posture again; an expired deadline from a prior process is not
restored. Child agents use `session.agentBudget` wall-clock limits, not this dial.
Inspect the effective value via the diagnostic bundle (`config.turnTimeoutS`;
negative means off).

### Parallel children and path overlap

Within one session team, `task` children share the lead's tool CWD. Write tools
(`edit`, `write`, `apply_patch`, `notebook_edit`) register path touches on a
shared ownership map. When two **active** agents claim the same path:

| `session.overlapPolicy` | Behavior |
|---|---|
| `warn` (default) | write proceeds; tool output gets a warning; engine emits `path.overlap` |
| `block` | conflicting write is refused |
| `off` | track only (no warning/event) |

### Session log durability and retention

Session transcripts are JSONL under `~/.strike/sessions/<id>.jsonl` with a
sidecar `<id>.meta.json`. New logs start with a `session.header` line carrying
`schemaVersion` (currently `1`). Each event append writes a full JSON line and
`fsync`s so a crash cannot leave an unreadable half-record; resume skips a
trailing torn line and fails with an actionable error on interior corruption or
an unsupported newer schema (upgrade strike). Secrets are scrubbed on append
via `secret.RedactEvent` (see [secrets.md](secrets.md)).

Portable **session packages** (`format: strike.session`) export/import the
redacted event sequence + meta for support bundles — distinct from the
human-readable markdown transcript (`/export`, #221) and from durable checkpoint stacks
under `~/.strike/checkpoints/` (#573). Live `/fork` / `/rewind` copy into a new
id with `meta.forkedFrom` lineage.

| `session.retentionMaxSessions` | Cap closed sessions retained (0 = unlimited) |
| `session.retentionMaxAgeDays` | Drop closed sessions older than N days (0 = off) |
| `session.retentionMaxBytes` | Cap total closed log+meta bytes (0 = off) |
| `session.timelineMaxEntries` | Cap in-memory run timeline entries (0 = library default 10000) |
| `session.timelineArgsPreviewMax` | Inline tool-args preview rune cap (0 = 512) |
| `session.timelineOutputPreviewMax` | Inline tool-output preview rune cap (0 = 2048) |
| `session.timelineBlobSpill` | Spill oversized redacted payloads to `~/.strike/traces/<id>/blobs/` with `blob:sha256:` refs |
| `session.traceRetentionMaxFiles` | Cap top-level trees under traces + runs (0 = unlimited) |
| `session.traceRetentionMaxAgeDays` | Drop trace/run session trees older than N days (0 = off) |
| `session.traceRetentionMaxBytes` | Cap total bytes under traces + runs trees (0 = off) |

Build a policy with `session.RetentionFromConfig` and run
`Manager.ApplyRetention` from tooling or a maintenance path. Open sessions are
never deleted. Project config overrides global per field when non-zero.

**Trace storage (#810):** the structured run timeline (`pkg/timeline`, `/timeline`)
keeps bounded inline previews. With `timelineBlobSpill`, full redacted payloads
that exceed the preview caps are written under
`~/.strike/traces/<sessionId>/blobs/` (content-addressed; **no fsync** — session
JSONL remains the durability boundary so the turn/UI loop is not blocked on
observability I/O). Entries carry `argsRef` / `outputRef` (`blob:sha256:<hex>`)
and `truncated: true`. In-memory builders prune oldest **terminal** entries when
over `timelineMaxEntries`. `Builder.Metrics()` exposes Observe latency and
spill/truncate/prune counters.

Sidecar retention uses the same count/age/size axes as session retention:

- `session.ApplyTraceRetention(tracesDir, runsDir, policy)` — caps
  `~/.strike/traces` and `~/.strike/runs` (recordings / run snapshots)
- `Manager.ApplyRetentionWithSidecars` — session JSONL retention then deletes
  matching trace/run trees for each removed session id

Build the sidecar policy with `session.TraceRetentionFromConfig` from
`traceRetentionMax*`. Not automatic on launch.

Lead and children can query the map with `agent_ownership` (`list`), and claim
path prefixes with `lease` / `release` (exclusive or shared). Finished children
are deactivated so they no longer cause overlap. Structured handoff
`files_changed` (when available) can be merged via the same tracker.

### Delegation-worthiness policy (`session.delegationPolicy`)

Before every `task` / `delegate` create, the engine runs a deterministic
worthiness gate (#876) so tiny or tightly coupled work stays local and fan-out
respects concurrency/budget ceilings. Capability routing (#778) runs only after
the policy chooses to delegate.

| Field | Meaning |
|---|---|
| `mode` | `off` (always spawn), `advise` (record preferred action but spawn), `enforce` (soft-local returns status `local`; hard ceilings deny). When the block is omitted, the CLI defaults to `enforce`. Zero-value engine Options (tests/embedders) stay `off`. |
| `tinyPromptRunes` | Bare prompts at or below this rune count prefer local (default 280) |
| `maxPathsLocal` | Bare tasks with ≤N `context_bundle` paths prefer local (default 1; negative disables) |
| `maxLiveChildren` | Hard-deny when live children reach this count (0 = unlimited) |

**Soft prefer local** (overridable with `force_delegate=true` on the tool call):

- Bare tiny prompt (no agent/specialty/criteria/deps/verify) with few scoped paths
- Requested paths overlap active ownership claims of other live agents

**Soft prefer delegate**: intentional signals (agent pin, specialty/capabilities,
criteria, deps, verify gates) or multi-path independent work.

**Hard deny** (never overridable): depth ceiling (`maxChildDepth`), optional
`maxLiveChildren`, delegation object ceiling, and session budget exhausted when
a `SessionBudgetExhausted` hook is wired (session cost envelope #577).

Decisions expose a structured `policyReason` on tool metadata and
`child.started`. Engine counters (`delegate` / `local` / `deny` / `override`)
support comparing elapsed time and cost with policy on vs off.

Orchestrator guidance has a single pre-spawn decision table matching this gate.

### Per-agent budgets (`session.agentBudget`)

Optional defaults for every `task` / `delegate` child in a session. Spawn-time
`budget` fields on the tool call overlay any non-zero dimension. Zero means
unlimited for that dimension.

| Field | Meaning |
|---|---|
| `maxWallClockS` | Wall-clock seconds before fail + interrupt |
| `maxTokens` | Accumulated stream tokens before fail |
| `maxCostUsd` | USD before fail (enforced when cost pricing lands; see below) |
| `maxToolCalls` | Tool invocations before fail |
| `maxDangerousTools` | bash/write/edit/apply_patch/notebook_edit calls before fail |
| `stallAfterS` | Hard block after this many seconds without progress |
| `loopDetectN` | Hard block when the same tool name repeats N times |

On hard exceed the engine emits `child.escalated` and stops the child. Soft
resource budgets (`wall_clock`, `tokens`, `cost_usd`, `tool_calls`,
`dangerous_tools`, and hard `stall`/`loop`) first attempt **one reserved
finalization turn** (tools disabled, ~45s wall ceiling) so the child can return
a structured partial handoff before termination (#879). `child.escalated`
`action` is `finalizing` then the child ends with `ChildCompleted.budgetKind` +
`finalization` (`succeeded`|`failed`) and handoff `quality`
(`complete`|`partial`|`unavailable`). Hard cancel, parent shutdown, and trust
boundary failures skip finalization (`finalization=skipped_hard`,
`action=interrupted`). Already-written typed artifacts and engine-tracked
`files_changed` are always merged into the terminal handoff.

Delegation is marked `failed` or `blocked`, and a structured lead/owner mailbox
notice is delivered. Soft stall (default 300s idle) and loop (default 6
identical tools) flags always appear on `task_status` / `agent_roster` without
killing when no hard threshold is set.

**Stale children (#517):** folded into stall — not a second detector.

| Mode | Trigger | Parent-visible | Kills child? |
|---|---|---|---|
| **Soft stall** | Default **300s** without progress (or `stallAfterS` when set, for the soft flag) | `budget.stall=true`, `idle_s`, `last_progress_at`, `stall_after_s` on `task_status` / `agent_roster`; live state `needs_attention` + `block_reason`; rising-edge `child.escalated` with `action=signaled` + lead mailbox; `wait` on `task.stale` or `task.blocked` | **No** |
| **Hard stall** | `stallAfterS` / spawn `stall_after_s` configured and idle ≥ threshold | Same pulse fields + `child.escalated` `interrupted`/`finalizing`, terminal `blocked`, mailbox | **Yes** (after optional finalization) |

Progress clears soft stall flags and allows a later rising-edge signal. Prefer
`wait` / `task` action=wait over busy-polling status.

**Session cost envelope (#577 / #542):**

| Field | Meaning |
|---|---|
| `session.maxSessionCostUSD` | Outer USD ceiling for the whole session (0 = unlimited). CLI `--max-cost` overrides. |
| `session.maxTurnTokens` | Per-turn accumulated stream token ceiling (0 = unlimited). |

When `maxSessionCostUSD` is set it is the **outer** cost cap. Per-agent
`maxCostUsd` nests inside that envelope and never raises the session ceiling.
Cost is estimated from models.dev / catalog rates on each `usage.reported`.
At 50% / 80% / 100% the engine emits `session.budget_warning`; at 100% it hard-
stops with `EngineError` code `budget_exhausted` and `TurnCompleted`
`stopReason=budget_exhausted` (not a silent halt). The TUI status bar shows a
budget chip at those thresholds. Delegation fan-out is denied while exhausted.

**Isolated worktree path (explicit apply):** for true filesystem isolation,
prefer separate root sessions with `session.worktree=always` (or
`strike --worktree`), or dogfood sibling checkouts (e.g. under
`strike-cli-worktrees/`). Apply back to the primary tree with an explicit
`git merge` / `cherry-pick` / patch apply from the child branch or worktree —
never silent clobber of the shared primary checkout. Per-child worktrees inside
one `task` fan-out remain a follow-up; overlap detection is the in-session
safety rail today.

**ctrl+d saves defaults**: on the main screen it persists the current
provider/model/agent/effort/theme to `~/.strike/config`; in the provider
picker it saves the highlighted provider; in the model picker it saves
provider + model; in the effort picker it saves the highlighted level; in
the theme picker it saves the highlighted theme id.

**/settings Defaults**: interactive editor for theme, vimMode, nanoMode,
mdReadMode, **permissionMode**, **permissionAutoApproveSeconds**,
**permissionAutoApproveExclude**, **sandbox**, **notify**, **autoupdate**,
**leanCode**, **deferTools**, **session.worktree**, **maxChildDepth**, and
effort (plus a read-only view of provider/model/agent). Changes write
`~/.strike/config`. Theme, editor/reader presentation, notify, and auto-approve
countdown/exclude apply to the current session immediately; permissionMode,
sandbox, leanCode, deferTools, session.worktree, autoupdate, and maxChildDepth
affect **new** sessions (use `/mode` / Shift+Tab for the live permission dial,
and `/sandbox` to inspect the OS dial already bound for this process).
Autoupdate probes run at process start from the config loaded at launch.
Programmatic `/settings` saves drop JSONC comments; use **`/config`** (or
Settings → **Open config files…**) for hand-edited files and sidecars
(`mcp.jsonc`, `providers.jsonc`, `keybinds.jsonc`, agents/skills/themes/
workflows).

**/settings Compaction**: editor for history compaction and continuous prune
dials (`compactionStrategy`, `compactionModel`, `compactionThreshold`,
`compactionBuffer`, `keepUserTurns`, `pruneProtectTokens`,
`pruneMinimumTokens`, `pruneKeepUserTurns`, `pruneProtectTools`). Writes
`~/.strike/config`; values apply to **new** sessions (the running engine keeps
the dials it was started with). Pick lists cover common ranges; summarize
model and prune-protect tools use free-text input (empty clears).

Peer settings inventory (Claude Code / OpenCode → strike): see
[peer-ecosystem.md](peer-ecosystem.md#settings-inventory).

## Theme

`theme` is a color-theme id: bundled JSON themes plus files under
`~/.strike/themes` and `./.strike/themes`. Empty means the stock `strike`
palette. In the TUI, bare `/theme` opens a picker; `/theme <id>` applies one;
`/theme dark|light|auto` only adjusts session appearance (forced or terminal
background from Bubble Tea detection), not the color-theme id.

## Keybinds

Remap app-level chords without recompiling. Ids match the in-app cheatsheet
(`/keys` / `f1`). Prefer a dedicated file (JSONC comments allowed); the
`keybinds` object in config still works:

```jsonc
// ~/.strike/keybinds.jsonc or ./.strike/keybinds.jsonc
// Flat map (preferred). Wrapped {"keybinds": {...}} is also accepted.
{
  "nav.jump-bottom": "ctrl+b",
  "global.palette": "ctrl+k",
  "composer.newline": ["ctrl+j", "alt+enter"],
  "nav.window-next": "ctrl+p",
  "nav.window-prev": "ctrl+o",
  "nav.group-next": "ctrl+shift+o",
  "nav.group-prev": "ctrl+shift+p",
  "nav.tool-expand": "alt+enter"
}
```

Legacy shape in `~/.strike/config` or `./.strike/config`:

```json
{
  "keybinds": {
    "nav.jump-bottom": "ctrl+b",
    "global.palette": "ctrl+k"
  }
}
```

Layers merge last-wins per id:

`defaults → ~/.strike/config → ~/.strike/keybinds.jsonc → ./.strike/config → ./.strike/keybinds.jsonc`

(`.json` is accepted as well as `.jsonc`. In the same root, the dedicated file
overrides the config object.) Unknown binding ids and invalid/empty chords
fail config load with a clear error. Critical `global.quit` and
`global.interrupt` cannot be cleared.

Shared chords across different actions are allowed (context-specific routing
in the TUI decides the winner — e.g. default `alt+enter` is newline while
typing and tool expand only when the composer is empty). `/keys` shows the
effective map; `/keys reset` restores built-in defaults for the current
session only — remove remaps from `keybinds.jsonc` / the config `keybinds`
object to persist defaults.

List/permission modal conventions (`lists.*`, `perm.*`) and agents-pane local
controls (`agents.*`) are not remappable.

## Language servers (LSP)

Configure stdio language servers so file tool mutations (`write` / `edit` /
`apply_patch` / `notebook_edit`) drive `textDocument/didOpen` /
`didChange` / `didClose`, and `publishDiagnostics` notifications are collected
per URI. A dead language server degrades to no diagnostics and never takes
down the session (same crash isolation as MCP).

By default strike ships a server map for **Go** (`gopls`), **TypeScript**
(`typescript-language-server --stdio`), **Python** (`pylsp`), and **Rust**
(`rust-analyzer`). Missing binaries degrade to per-server `error` status and
never take down the session. Clear the map with `"servers": {}`, or replace it
entirely by setting `lsp.servers` in config.

```json
// ~/.strike/config or ./.strike/config
{
  "lsp": {
    "servers": {
      "go": {
        "command": "gopls",
        "extensions": [".go"]
      },
      "typescript": {
        "command": "typescript-language-server",
        "args": ["--stdio"],
        "extensions": [".ts", ".tsx", ".js", ".jsx"]
      },
      "python": {
        "command": "pylsp",
        "extensions": [".py"]
      },
      "rust": {
        "command": "rust-analyzer",
        "extensions": [".rs"]
      }
    }
  }
}
```

| Field | Required | Notes |
|---|---|---|
| `command` | yes | Executable on `PATH` or absolute path |
| `args` | no | Extra argv after command |
| `env` | no | Env overlay for the subprocess (never logged) |
| `extensions` | yes | File extensions this server owns (with or without leading `.`). First server claiming an extension wins. Servers with no extensions are skipped. |

**Layering:** when a config layer sets `lsp.servers` (including `{}`), it
**replaces** the previous layer's server map entirely (same as MCP). Omitted
`servers` leaves the lower layer's map. Scalar diagnostics knobs
(`diagnosticsSeverity`, `diagnosticsMaxChars`, `diagnosticsWaitMs`) overlay
last-wins when set. Omitted `lsp` leaves the lower layer unchanged.

### Diagnostics in tool results

After a successful `write` / `edit` / `apply_patch` / `notebook_edit`, strike
waits briefly for `publishDiagnostics` on the touched paths and appends a
single `--- diagnostics ---` block to the tool `Result` the model sees.
Multi-file `apply_patch` shares one wait window and one block (not one block
per file). A dead language server degrades to no injection.

| Field | Default | Notes |
|---|---|---|
| `diagnosticsSeverity` | `error` | Minimum severity to inject: `error`, `warning`, `info`, or `hint`. Errors-only by default; set `warning` to opt in to warnings. |
| `diagnosticsMaxChars` | `4000` | Cap on injected text (runes). Excess lines become `… (N more diagnostic(s) truncated)`. |
| `diagnosticsWaitMs` | `400` | Max wait for `publishDiagnostics` after a mutation. Negative skips the wait (snapshot immediately). |

```json
{
  "lsp": {
    "diagnosticsSeverity": "warning",
    "diagnosticsMaxChars": 6000,
    "diagnosticsWaitMs": 600,
    "servers": {
      "go": { "command": "gopls", "extensions": [".go"] }
    }
  }
}
```

### `/lsp` and the diagnostics pane

- `/lsp` — status (`up` / `down` / `error` / `disabled`); command, extensions, open docs
- `/lsp retry [name]` — reconnect one server, or every non-up server
- `/lsp disable <name>` — stop a server for the session
- `/diagnostics` — focus the right-pane diagnostics browser (findings from live servers; Enter opens the file)

### Navigation and diagnostics tools (optional)

Read-only tools call the language server for code navigation and diagnostics
queries. They are **not** core tools: when `deferTools` is `on`, their schemas
stay out of the hot provider Tools array until `toolsearch` discovers them (or
the model calls them by name).

| Tool | LSP method / source | Args |
|---|---|---|
| `definition` | `textDocument/definition` | `filePath`, `line` (1-based), optional `character` (0-based) |
| `references` | `textDocument/references` | same position args; includes declaration |
| `symbols` | `textDocument/documentSymbol` or `workspace/symbol` | `filePath` and/or `query` |
| `diagnostics` | cached `publishDiagnostics` from live servers | optional `path` (file or directory; omit = workspace), optional `severity` (`error` default, `warning`, `info`, `hint`), optional `maxResults` (default 100, max 500) |

`diagnostics` returns a stable JSON payload: `file`, `range` (1-based
line/character start+end), `severity`, `source`, `code`, `message`, plus
server status, counts, and a `truncated` flag. Results are sorted
deterministically. Paths stay workspace-scoped.

A missing or dead language server returns structured status / a soft message in
the tool result (never hangs or takes down the session). Default permission is
Allow (read-only).

## External harnesses (`harnesses`)

Named subprocess harnesses used by agent frontmatter `harness: <name>`. See
[harnesses.md](harnesses.md) for the full protocol and SDK notes.

```json
{
  "harnesses": {
    "choose-best-js": {
      "command": "node",
      "args": ["./examples/harnesses/choose-best.mjs"]
    },
    "heavy-runtime": {
      "command": "./bin/my-harness-worker",
      "mode": "persistent",
      "maxConcurrent": 2,
      "idleTimeoutMs": 120000,
      "maxRestarts": 3
    }
  }
}
```

| Field | Default | Notes |
|---|---|---|
| `command` | required | Executable |
| `args` | `[]` | Argv tail |
| `env` | `{}` | Env overlay (never logged) |
| `mode` | `oneshot` | `oneshot` or `persistent` |
| `maxConcurrent` | `1` | Persistent only; in-flight invocation cap |
| `idleTimeoutMs` | `60000` | Persistent idle shutdown; negative disables |
| `maxRestarts` | `3` | Persistent crash recovery budget; negative unlimited |

Project definitions replace global entries with the same name. Unknown `mode`
fails config load. Persistent workers are still trusted native commands (not
OS-sandboxed); brokered tools keep normal permission/sandbox policy.

## MCP servers (stdio + HTTP)

Connect external [Model Context Protocol](https://modelcontextprotocol.io)
servers so their tools appear in the model registry as `mcp_<server>_<tool>`.
Supported transports: **stdio** (local subprocess) and **streamable HTTP**
(remote endpoint; JSON or SSE responses).

Prefer **`mcp.jsonc`** (or `mcp.json`) for server definitions. The legacy
`mcp` object in config still works. Layers merge last-wins by file:

`defaults → ~/.strike/config → ~/.strike/mcp.jsonc → ./.strike/config → ./.strike/mcp.jsonc`

(`.json` is accepted as well as `.jsonc`.) When a layer sets servers
(including `{}`), it **replaces** the previous layer's server map. Omitted
`mcp` / missing mcp file leaves the lower layer unchanged.

### `mcp.jsonc` (preferred)

Bare server map or wrapped `servers` object; JSONC comments allowed:

```jsonc
// ~/.strike/mcp.jsonc or ./.strike/mcp.jsonc
{
  "github": {
    "command": "npx",
    "args": ["-y", "@modelcontextprotocol/server-github"],
    "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "…" }
  },
  "remote": {
    "type": "http",
    "url": "https://mcp.example.com/mcp",
    "headers": { "Authorization": "Bearer …" }
  }
}
```

Equivalent wrapped form:

```jsonc
{
  "servers": {
    "github": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-github"]
    }
  }
}
```

### Legacy: `mcp` in config

```json
{
  "mcp": {
    "servers": {
      "github": {
        "command": "npx",
        "args": ["-y", "@modelcontextprotocol/server-github"],
        "env": { "GITHUB_PERSONAL_ACCESS_TOKEN": "…" }
      }
    }
  }
}
```

| Field | Required | Notes |
|---|---|---|
| `servers.<name>` | yes | short letter-led slug (`[A-Za-z][A-Za-z0-9_-]*`) |
| `type` | no | `stdio` (default) or `http` (`sse` is accepted as an alias for `http`) |
| `command` | stdio | executable on `PATH` or absolute path |
| `args` | no | argv after the command |
| `env` | no | stdio env overlay; **never logged** |
| `url` | http | MCP endpoint URL (if set without `type`, transport is `http`) |
| `headers` | no | HTTP request headers (e.g. `Authorization`); **never logged or shown in `/mcp`** |

Lifecycle: servers start with the session (after the tool worktree is bound),
list tools once, and shut down on exit. A crashed or unreachable server does
not take down strike — its tools error cleanly; `/mcp` shows `up` / `down` /
`error` / `disabled`.

Control from the TUI:

- `/mcp` — status (transport, endpoint label, tools, errors)
- `/mcp retry [name]` — reconnect one server, or every non-up server
- `/mcp disable <name>` — stop a server and unregister its tools

Permissions: every MCP tool call asks with permission name `mcp` and pattern
`<server>/<tool>` (default action **ask**). Allow a server or tool in config:

```json
{
  "permissions": [
    { "permission": "mcp", "pattern": "github/*", "action": "allow" },
    { "permission": "mcp", "pattern": "github/delete_*", "action": "deny" }
  ]
}
```

Treat project-local MCP config like shell hooks: stdio runs local commands;
HTTP may send secrets via `headers`. Prefer global `~/.strike/mcp.jsonc` for
shared servers; review `command`/`args`/`env`/`url`/`headers` before trusting
a project's `.strike/mcp.jsonc`.

## Custom providers

Add OpenAI-compatible (chat completions) or Anthropic-compatible (messages)
endpoints via **`providers.jsonc`** (preferred) or the `providers` array in
config. Layers merge last-wins by name:

`defaults → ~/.strike/config → ~/.strike/providers.jsonc → ./.strike/config → ./.strike/providers.jsonc`

(`.json` is accepted as well as `.jsonc`.) Credentials never live in these
files — use env refs and/or `/auth` / the auth store.

### Disable default (builtin) providers

Hide stock catalog providers (`anthropic`, `openai`, `xai`, `google`, `kimi`,
`deepseek`, `echo`) so only custom endpoints appear in `/provider`, `/auth`,
and model pickers. The shipped alias `gemini` is accepted on
`disable-default-gemini` and routes to `google`. Same keys work in
**`providers.jsonc`** or config JSON; later layers win (project overrides
global; providers.jsonc overrides the config file in the same root).

```jsonc
// ~/.strike/providers.jsonc — custom-only setup, keep openai available
{
  "disable-default-providers": true,
  "disable-default-openai": false, // per-provider override re-enables
  "disable-default-anthropic": true, // redundant when all are disabled
  "acme": {
    "options": {
      "baseURL": "https://api.example.com/v1",
      "apiKey": "{env:ACME_API_KEY}"
    },
    "models": ["acme-latest"]
  }
}
```

| Key | Effect |
|---|---|
| `disable-default-providers` | `true` hides **all** builtins unless a per-provider flag says otherwise |
| `disable-default-<name>` | `true` disables that builtin; `false` **re-enables** it when the bulk flag is on |

Customs are never affected. Selecting a disabled builtin (`--provider`,
`/provider`, config default) fails with a clear error. Overlays/endpoints for
a disabled builtin are ignored for selection until it is re-enabled.

### `providers.jsonc` (OpenCode-style)

```jsonc
// ~/.strike/providers.jsonc or ./.strike/providers.jsonc
{
  // Custom / self-hosted endpoint
  "acme": {
    "npm": "@ai-sdk/openai-compatible", // optional; hints wire dialect only (not loaded)
    "name": "Acme",
    "options": {
      "baseURL": "https://api.example.com/v1",
      "apiKey": "{env:ACME_API_KEY}"
    },
    // Legacy flat ids still work:
    // "models": ["acme-latest"]
    // Nested rich objects (display name, limits, variants):
    "models": {
      "acme-latest": {
        "name": "Acme Latest",
        "limit": { "context": 128000, "output": 8192 },
        "options": { "forcedReasoning": true },
        "variants": {
          "high": { "reasoningEffort": "high", "textVerbosity": "low" },
          "low": { "reasoningEffort": "low" }
        }
      }
    }
  },
  // Built-in overlay — does NOT become a separate custom provider.
  // options.baseURL / options.apiKey customize the stock endpoint (proxy).
  // Omit models (or leave empty) to keep the full models.dev catalog.
  // Overlay one id to refine name/limits/variants; other catalog ids remain.
  "anthropic": {
    "name": "Corp Anthropic",
    "options": {
      // OpenCode/AI SDK shape: include /v1 (strike also accepts origin-only).
      "baseURL": "https://proxy.example/anthropic/v1",
      "apiKey": "{env:CORP_ANTHROPIC_KEY}"
    }
  },
  "openai": {
    "models": {
      "gpt-5.5": {
        "name": "GPT-5.5",
        "limit": { "context": 272000, "output": 128000 },
        "variants": {
          "high": { "reasoningEffort": "high" },
          "xhigh": { "reasoningEffort": "xhigh" }
        }
      }
    }
  },
  "claude-proxy": {
    "npm": "@ai-sdk/anthropic",
    "options": {
      "baseURL": "$ANTHROPIC_BASE_URL",
      "apiKey": "${ANTHROPIC_AUTH_TOKEN}"
    }
  }
}
```

| Field | Required | Notes |
|---|---|---|
| map key | yes | provider id (lowercased slug). Built-ins (`anthropic`/`openai`/`xai`/`google`/`kimi`/`deepseek`/`echo`) stay builtins: options → **endpoint overlay**, models → **catalog overlay**. The shipped alias `gemini` is accepted and canonicalized to `google`. Other keys are custom providers. |
| `options.baseURL` | custom yes | absolute `http`/`https` URL, or `{env:VAR}` / `$VAR` / `${VAR}`. On builtins, optional — overrides the stock endpoint. **OpenCode shape:** include `/v1` (Anthropic → `…/v1` + `/messages`; OpenAI → `…/v1` + `/chat/completions` or `/responses`). Origin-only Anthropic bases still work. |
| `options.apiKey` | no | env ref only (`{env:NAME}`, `$NAME`, `${NAME}`) → checked before auth store. On builtins, pins the env var used for that provider. Missing env fails at select time with a clear error. |
| `npm` | no | **advisory only** — never installed; `anthropic` → Messages; `@ai-sdk/openai` → **Responses** (`/responses`); `@ai-sdk/openai-compatible` (default) → chat completions |
| `api` | no | strike override: `openai` (chat), `responses`, or `anthropic` (wins over npm hint) |
| `models` | no | `[]string` (legacy) **or** object map id → model def; see merge rules below |
| `models.<id>` map key | yes (when nested) | **wire model id** sent on the API `model` field and used by `/model` selection |
| `models.<id>.name` | no | **display label only** in `/model` (never sent on the wire; default: id or models.dev name) |
| `models.<id>.limit.context` / `.output` | no | token ceilings; overlay wins over models.dev when set (>0) |
| `models.<id>.options` | no | opaque bag (unsupported keys ignored; must not change the wire id) |
| `models.<id>.variants` | no | named effort presets; `reasoningEffort`/`effort` map onto `/effort` |
| `options.headers` | no | extra HTTP headers (values may use env refs) |

#### Wire id vs display name

Nested `models` object **keys** are the ids strike selects and sends on the wire
(`{"model":"<key>"}`). The optional `name` field is a UI label only. Example:
`"gpt-5.5": { "name": "GPT-5.5" }` lists as “GPT-5.5” but requests `gpt-5.5`.
Variants and options never rewrite the wire id.

#### Builtin endpoint overlay (anthropic / openai / …)

Defining `"anthropic": { "options": { "baseURL", "apiKey" } }` (with or without
`models`) keeps the builtin provider registered, routes HTTP to the custom
endpoint, resolves the pinned apiKey env, and still lists models.dev when
`models` is omitted. Same for other credential builtins (openai chat-completions
path when baseURL/apiKey is set — not the ChatGPT OAuth backend).

#### baseURL path join (OpenCode parity)

| Wire | `options.baseURL` example | Request path |
|---|---|---|
| anthropic | `https://proxy.example/v1` (OpenCode) | `…/v1/messages` |
| anthropic | `https://proxy.example` (origin-only) | `…/v1/messages` |
| openai (chat) | `https://proxy.example/v1` | `…/v1/chat/completions` |
| responses (`@ai-sdk/openai`) | `https://proxy.example/v1` | `…/v1/responses` |

Do **not** put `/messages` or `/chat/completions` in `baseURL` unless the whole
URL is already the final endpoint (strike leaves a trailing `/messages` or
`/responses` alone).

#### models.dev / catalog merge

| Situation | Behavior |
|---|---|
| Builtin (openai, anthropic, …) with models.dev data | `/model` lists **catalog** models by default |
| Builtin with only `options` (no `models`) | endpoint overlay applied; **full catalog** unchanged |
| Config omits `models` or `models` is empty | full catalog unchanged |
| Config nested/flat models on a **builtin** | **merge/overlay** by id: config wins name/limits/variants; catalog-only ids still appear |
| Config nested/flat models on a **custom** provider | config list is the full `/model` list (no models.dev); map keys are wire ids |
| Config sets limits for a catalog id | config wins for those fields; other catalog metadata kept |

You never need to paste an entire upstream catalog into `providers.jsonc` just to set one variant, context limit, or proxy baseURL.

#### Default model precedence

1. `config.model` / `--model` when set  
2. Custom provider: first configured model id (`models` array order, or sorted nested keys)  
3. Built-in pin via `DefaultModel(provider)` (e.g. openai → `gpt-5.5`)  
4. Otherwise unset (freeform `/model <id>`)

#### Variants → effort

Variant bags may include `reasoningEffort` or `effort` (`off`\|`low`\|`medium`\|`high`\|`xhigh`\|`max`). Selecting a variant (from the `/effort` picker when the active model has variants, or `/effort <variant-id>`) sets the session effort dial; adapters map that onto wire fields (`reasoning_effort`, Anthropic `output_config.effort`, …). Other variant keys are ignored for now.

### Config `providers` array (legacy)

```json
{
  "providers": [
    {
      "name": "acme",
      "baseURL": "https://api.example.com/v1",
      "api": "openai",
      "apiKeyEnv": "ACME_API_KEY",
      "models": ["acme-latest"],
      "headers": { "X-Custom": "optional" }
    }
  ]
}
```

| Field | Required | Notes |
|---|---|---|
| `name` | yes | lowercase slug (`[a-z][a-z0-9_-]{0,63}`); not `anthropic`/`openai`/`xai`/`google`/`gemini`/`kimi`/`deepseek`/`echo` (`gemini` is reserved as an alias of `google`) |
| `baseURL` | yes | absolute URL or env ref template |
| `api` | yes | wire dialect: `openai` (chat), `responses`, or `anthropic` |
| `apiKeyEnv` | no | env var name (or `{env:NAME}` / `$NAME`) checked before the auth store |
| `models` | no | flat `[]string` ids listed in `/model`; first is the default when unset (rich nested models use `providers.jsonc`) |
| `headers` | no | extra HTTP headers on every request (values may use env refs) |

**Migration:** existing `models: ["a","b"]` keeps working everywhere. Prefer
`providers.jsonc` nested objects when you need display names, limits, or
variants. Built-in overlays go under the builtin key in `providers.jsonc`
(not in the `providers` array — builtin names remain reserved there).

**Env interpolation:** `{env:NAME}`, `$NAME`, and `${NAME}` expand from the
process environment (vars exported to the strike process, e.g. via bashrc).

**TUI:** `/settings` CRUD and `/provider` → “Add custom provider…”. Custom
names appear in `/provider` like built-ins. **Logout** (`ctrl+x` or
`/auth logout <name>`) of a custom provider **deletes** its definition from
config/providers.jsonc and clears credentials; `/settings` `d` does the same.
Built-in logout only clears credentials.

## Surface presentation (`vimMode`, `nanoMode`, `mdReadMode`)

Editor and markdown-reader surfaces share a presentation vocabulary:
**embedded** (right-pane chrome) vs **modal** (large centered overlay with
background scrim). Prefer those names for new config; legacy aliases remain.

### Embedded editor (`vimMode`)

`/vim [path|@path[:line]]` opens a file in an editor resolved from `$VISUAL` →
`$EDITOR` → nvim/vim/vi/nano on `PATH`. `vimMode` selects how:

| Value | Aliases | Behavior |
|---|---|---|
| `pane` (default) | `embedded` | embed the editor in the right-pane `editor` window (PTY) |
| `overlay` | `modal` | large modal popout with background scrim |
| `takeover` | — | full-screen handoff via `tea.ExecProcess` |

Unknown values are ignored at load time. GUI `$EDITOR` values always take
over the terminal regardless of `vimMode`. Leave the embedded/modal editor
with `ctrl+g`.

### Nano (`nanoMode`)

`/nano [path|@path[:line]]` opens **nano** specifically (does not use `$VISUAL`/
`$EDITOR`). `nanoMode` uses the same values and aliases as `vimMode`
(default `pane`/`embedded`). Missing `nano` on `PATH` shows a clear error.
Leave the embedded/modal editor with `ctrl+g`.

### Markdown reader (`mdReadMode`)

`/md-read <path|@path>` opens a markdown file. `mdReadMode` selects how:

| Value | Aliases | Behavior |
|---|---|---|
| `embedded` (default) | `pane` | right-pane `markdown` window |
| `modal` | `overlay` | large modal popout with background scrim |

Unknown values are ignored at load time. Dismiss the modal with `esc`, `q`,
or `ctrl+g`. Preference is read from config at session start (global then
project merge).

## Hooks

Lifecycle hooks live in the same JSON config under `hooks` (global then
project **concatenate**). Each entry is either a **declarative rule**
(`action`) or a **shell command** (`command`) — not both.

```json
{
  "hooks": [
    {
      "event": "pre_tool_use",
      "matcher": "bash",
      "action": "log"
    },
    {
      "event": "pre_tool_use",
      "matcher": "write",
      "action": "block",
      "message": "writes blocked by policy"
    },
    {
      "event": "post_tool_use",
      "matcher": "edit",
      "command": "echo ok",
      "timeoutMs": 10000
    }
  ]
}
```

| Field | Notes |
|---|---|
| `event` | `pre_tool_use`, `post_tool_use`, `turn_start`, `turn_end` |
| `matcher` | doublestar on tool name; empty/`*` = all (turn events: empty/`*` only) |
| `action` | `log`, `block`, or `notify` (block only on `pre_tool_use`) |
| `message` | optional block/notify text |
| `command` | `bash -c` with event JSON on stdin (shell hooks: tool events) |
| `timeoutMs` | shell bound; default 30000, max 120000 |

Invalid rows are dropped at load. Peer event-name mapping (CC/OpenCode/Crush):
[peer-ecosystem.md](peer-ecosystem.md#hooks-alignment).

### Shell hook stdin payload

Shell hooks (`command`) receive one JSON object on stdin (not env vars):

| Field | When | Notes |
|---|---|---|
| `event` | always | `pre_tool_use` or `post_tool_use` |
| `session_id` | always | session id |
| `cwd` | always | engine workdir |
| `tool_name` | tool events | e.g. `edit`, `write` |
| `tool_call_id` | tool events | call id |
| `tool_input` | tool events | raw tool args object (`filePath` for `edit`/`write`) |
| `tool_output` | `post_tool_use` | tool result text |
| `is_error` | `post_tool_use` | `true` when the tool failed |

Exit **0** allows; non-zero **blocks** (pre: deny tool; post: mark the completed call blocked and replace feedback). Timeouts and start failures **fail-open**. Prefer always-exit-0 recipes for non-blocking side effects (formatters, notify).

### Post-edit formatters (recipe)

Strike has **no** first-class `formatters` map (OpenCode-style plugin host is out of scope). Run formatters with `post_tool_use` shell hooks after successful `edit` / `write`. Requires `jq` on `PATH` for the snippets below.

**Non-blocking Go format** (recommended default — formatter failure does not fail the tool):

```json
{
  "hooks": [
    {
      "event": "post_tool_use",
      "matcher": "{edit,write}",
      "timeoutMs": 15000,
      "command": "payload=$(cat); echo \"$payload\" | jq -e '.is_error == true' >/dev/null 2>&1 && exit 0; f=$(echo \"$payload\" | jq -r '.tool_input.filePath // empty'); [ -n \"$f\" ] || exit 0; case \"$f\" in *.go) gofmt -w \"$f\" 2>/dev/null || true ;; esac; exit 0"
    }
  ]
}
```

**Multi-language sketch** (still non-blocking; adjust tools to taste):

```json
{
  "hooks": [
    {
      "event": "post_tool_use",
      "matcher": "{edit,write}",
      "timeoutMs": 30000,
      "command": "payload=$(cat); echo \"$payload\" | jq -e '.is_error == true' >/dev/null 2>&1 && exit 0; f=$(echo \"$payload\" | jq -r '.tool_input.filePath // empty'); [ -n \"$f\" ] || exit 0; case \"$f\" in *.go) gofmt -w \"$f\" 2>/dev/null || true ;; *.ts|*.tsx|*.js|*.jsx|*.json|*.css|*.md) command -v prettier >/dev/null && prettier --write \"$f\" 2>/dev/null || true ;; *.py) command -v ruff >/dev/null && ruff format \"$f\" 2>/dev/null || true ;; esac; exit 0"
    }
  ]
}
```

**Blocking format** (rare): omit the trailing `|| true` / `exit 0` so a non-zero formatter exit marks the tool result blocked. Prefer non-blocking unless you intentionally gate the agent on format success.

Notes:

- Matcher is doublestar on **tool name** (`{edit,write}` is fine); it does not filter by file extension — do that in the shell `case`.
- `apply_patch` and other mutators are not covered by this matcher; add separate rows if you format those paths.
- Project hooks concatenate after global hooks; put team formatters in `./.strike/config` (or your project config path).
- Editor/`$EDITOR` format-on-save remains a valid alternative outside the agent loop.
- Peer inventory: [peer-ecosystem.md](peer-ecosystem.md#settings-inventory) (Formatters → hooks recipe).

## History compaction

`/compact` and automatic threshold/overflow compaction shrink model-facing
history while keeping a recent tail. Continuous tool-result prune
(`internal/engine/prune.go`) blanks older tool bodies under that ceiling;
threshold compaction is the coarser whole-history rewrite. Edit these dials in
JSON or interactively via **`/settings` → Compaction**.

| Field | Values | Default |
|---|---|---|
| `compactionStrategy` | `trim` (drop older turns) or `summarize` (model-authored summary of dropped turns) | `trim` |
| `compactionModel` | optional model id for the summarize call (same provider as the session) | session model |
| `compactionThreshold` | occupancy fraction of the known context window that triggers auto-compact before a Stream; `>=1` disables threshold compaction; omit/`0` uses the engine default | `0.70` |
| `compactionBuffer` | extra token headroom reserved with `MaxTokens` so threshold compaction fires before hard exhaustion; omit/`0` uses the engine default | `4096` |
| `keepUserTurns` | trailing real user turns preserved when compacting (compact markers do not count); omit/`0` uses the engine default | `2` |
| `pruneProtectTokens` | recent tool-output tokens kept intact while walking history backward during continuous prune; omit/`0` uses the engine default; negatives clamp to `0` | `40000` |
| `pruneMinimumTokens` | minimum estimated tokens that must be freed before prune mutates history (avoids thrash); omit/`0` uses the engine default; negatives clamp to `0` | `20000` |
| `pruneKeepUserTurns` | real user turns whose tool results stay complete during prune (compact markers do not count); omit/`0` uses the engine default; negatives clamp to `0` | `2` |
| `pruneProtectTools` | extra tool names whose results are never blanked (merged with built-in `skill`); names lowercased/deduped; omit/empty adds none | `[]` (+ built-in `skill`) |

Recommended ranges: threshold `0.60`–`0.85` (lower = earlier pressure response;
too low thrash-compacts short sessions), buffer `1024`–`8192`, keep turns
`1`–`4`. For prune, lower `pruneProtectTokens` / `pruneMinimumTokens` on
MCP-heavy sessions (tighter reclaim); raise minimum on short interactive
sessions to avoid thrash. Overflow recovery still compacts on context-length
provider errors regardless of threshold.

On summarize failure the engine falls back to trim and emits a notice. The
summary path never re-runs tools.

## Reasoning effort

`/effort` sets how much internal reasoning the model spends before answering.
The active level shows on the top status bar once set. Persist a default with
`ctrl+d` in the effort picker, `/settings` → Defaults → Effort, config
`"effort"`, or `--effort`. The ladder is normalized across vendors and each
adapter maps it to its own wire fields — Anthropic to adaptive thinking plus
`output_config.effort`, the OpenAI family to a `reasoning_effort` string. With
no level set, strike sends no reasoning fields at all and each provider's own
default applies. The `task` tool accepts optional `effort` so a parent can pin
a child dial independently of the UI default.

The two ends of the ladder are requests, not guarantees, because the vendor
ladders differ in length: `off` disables thinking outright on Anthropic but
floors at `minimal` on the OpenAI family (which has no zero setting), and
`xhigh`/`max` clamp down to `high` there for the same reason.

| Level | Meaning |
|---|---|
| `off` | least reasoning the provider allows — fastest and cheapest |
| `low` | minimal reasoning for short, scoped tasks |
| `medium` | balanced reasoning for routine work |
| `high` | thorough reasoning — the provider default |
| `xhigh` | deeper reasoning, best for coding and agentic work |
| `max` | maximum reasoning when correctness beats cost |

Agents, skills, and workflows (including `.claude` / `.opencode` discovery
roots and merge order): [agents-skills.md](agents-skills.md). Peer import
inventory: [peer-ecosystem.md](peer-ecosystem.md).
