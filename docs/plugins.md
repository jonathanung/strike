# Plugin bundle contract

Normative contract for **versioned plugin bundles** (epic
[#451](https://github.com/jonathanung/strike/issues/451), PLUGIN.1
[#725](https://github.com/jonathanung/strike/issues/725)).

A plugin is a **versioned contribution package** that reuses Strike's existing
extension surfaces (agents, skills, workflows, provider profiles, MCP,
harnesses, hooks, themes, and future panes). It is **not** a new runtime, a
Node/Go host ABI, or a way to inject arbitrary provider code.

This document freezes identity, manifest shape, contribution matrix, trust,
path confinement, secrets, and schema-version rules **before** loaders land
([#726](https://github.com/jonathanung/strike/issues/726)+). Implementations
must not weaken these rules.

| Status | Meaning |
|---|---|
| **Contract (this doc)** | Normative. Loaders and CLI must conform. |
| **Not implemented yet** | Discovery, install, activation, catalog, TUI manager — later issues. |
| **Out of scope forever (v1 model)** | In-process Go `plugin` packages, OpenCode-style Node plugin hosts, arbitrary provider/auth/streaming adapters, silent executable startup from an untrusted bundle. |

Related: [agents-skills.md](agents-skills.md), [config.md](config.md),
[harnesses.md](harnesses.md), [theme.md](theme.md), [secrets.md](secrets.md),
[protocol.md](protocol.md#extension-points-plugins--hooks--harnesses),
[peer-ecosystem.md](peer-ecosystem.md).

---

## 1. Definitions

| Term | Definition |
|---|---|
| **Plugin** | A directory tree with a root `plugin.json` (or `plugin.jsonc`) manifest plus contribution assets. |
| **Plugin ID** | Stable reverse-DNS or scoped slug identifying the package across installs (`acme.review-pack`). |
| **Bundle version** | Semver of the package contents (`1.2.0`), independent of Strike's release version. |
| **Schema version** | Integer `schemaVersion` of the manifest format (this contract starts at **1**). |
| **Source identity** | How the bundle was obtained: `local`, `git`, or `catalog` (see §6). |
| **Content digest** | Canonical SHA-256 over the package payload (see §5). |
| **Contribution** | One declared entry under `contributions` that maps onto an existing Strike loader. |
| **Passive** | Declarative assets loaded without starting a subprocess (agents, skills, workflows, themes, provider profiles). |
| **Executable** | Contributions that start a process or run shell (MCP stdio, harness commands, shell hooks). Require explicit trust (§5). |
| **Enablement** | User/project flag: disabled plugins contribute **nothing** on the next launch. |
| **Trust record** | Durable binding of `(plugin ID, source identity, content digest, capability set)` after explicit user review. |

---

## 2. Package layout

```text
<plugin-root>/
  plugin.json          # required manifest (JSON or JSONC)
  README.md            # optional human docs (not loaded as a contribution)
  agents/              # optional *.md agents
  skills/              # optional skills (name.md or name/SKILL.md)
  workflows/           # optional *.json workflows (schemaVersion 1)
  themes/              # optional theme JSON files
  providers/           # optional provider profile fragments
  mcp/                 # optional MCP server definitions (data only until trusted)
  harnesses/           # optional harness command definitions + binaries/scripts
  hooks/               # optional hook definition fragments
  panes/               # reserved for pane ABI (#522); not activated by this contract alone
  bin/                 # optional executables referenced by relative paths
  assets/              # optional non-code assets (icons, fixtures)
```

Rules:

1. **Root confinement.** All contribution paths in the manifest MUST resolve
   inside `<plugin-root>` after cleaning (`..`, symlinks that escape, absolute
   paths outside the root are rejected). Loaders fail closed with a diagnostic.
2. **No hidden roots.** Strike does not scan arbitrary repo paths for plugins.
   Install/discovery roots are only the configured global and project plugin
   directories (and lockfile-recorded installs) — see §4.
3. **Manifest is authoritative.** Undeclared files under the tree are ignored
   for loading (they may still affect the content digest if included in the
   digest set — see §5.2).
4. **No credentials in the tree.** Auth material, API keys, tokens, and
   private key material MUST NOT ship inside a plugin bundle. Use secret refs
   and the auth store ([secrets.md](secrets.md), [auth.md](auth.md)).

### Planned install roots (loaders #726 / lifecycle #727)

| Scope | Directory | Precedence |
|---|---|---|
| Global | `~/.strike/plugins/<plugin-id>/` | lower |
| Project | `./.strike/plugins/<plugin-id>/` | higher |

Lockfile (lifecycle): `~/.strike/plugins.lock.json` and/or
`./.strike/plugins.lock.json` record source identity, pinned version, digest,
and enablement — **never** credentials. Exact lockfile schema is owned by
[#727](https://github.com/jonathanung/strike/issues/727); this contract only
requires that provenance fields above exist.

---

## 3. Normative manifest (`schemaVersion: 1`)

### 3.1 Top-level fields

| Field | Required | Type | Rules |
|---|---|---|---|
| `schemaVersion` | yes | integer | Must be `1` for this contract. See §8. |
| `id` | yes | string | Plugin ID: `^[a-z][a-z0-9-]*(\.[a-z][a-z0-9-]*)+$` **or** single-segment `^[a-z][a-z0-9-]{1,63}$`. Max 128 chars. Immutable for the life of the package identity. |
| `version` | yes | string | Semver 2.0 (`1.0.0`, optional pre-release). Bundle version. |
| `name` | yes | string | Human display name (1–80 chars). |
| `description` | no | string | Short summary (≤500 chars). |
| `strike` | yes | object | Compatibility range for the Strike binary. |
| `strike.min` | yes | string | Minimum Strike semver (inclusive), e.g. `"0.2.0"`. |
| `strike.max` | no | string | Optional exclusive upper bound. |
| `source` | no | object | Author-declared default source hint; **install lockfile overrides** at runtime. |
| `contributions` | yes | object | Map of contribution-type → entries. At least one non-empty list/map required. |
| `capabilities` | no | string[] | Declared capability tags for update review (§6.4). |
| `digest` | no | string | Author-published expected content digest (`sha256:<hex>`). Installers verify when present; loaders recompute. |

Unknown top-level fields: **rejected** on strict decode (same posture as
workflows). Optional `$schema` key is ignored (editor only), matching config.

### 3.2 Complete normative example (mixed passive + executable)

```jsonc
// plugin.jsonc — normative shape for schemaVersion 1
{
  "schemaVersion": 1,
  "id": "acme.review-pack",
  "version": "1.2.0",
  "name": "Acme Review Pack",
  "description": "Review agent, /ship-review skill, workflow, theme, and optional MCP.",
  "strike": {
    "min": "0.2.0"
  },
  "capabilities": [
    "agents",
    "skills",
    "workflows",
    "themes",
    "mcp.stdio"
  ],
  "contributions": {
    "agents": [
      { "path": "agents/reviewer-strict.md" }
    ],
    "skills": [
      { "path": "skills/ship-review/SKILL.md" }
    ],
    "workflows": [
      { "path": "workflows/review-gate.json" }
    ],
    "themes": [
      { "path": "themes/acme-dark.json" }
    ],
    "providers": [
      {
        "path": "providers/acme-proxy.jsonc",
        "profileName": "acme-proxy"
      }
    ],
    "mcp": [
      {
        "name": "acme-lint",
        "transport": "stdio",
        "command": "bin/acme-lint-mcp",
        "args": ["--serve"]
      }
    ],
    "harnesses": [
      {
        "name": "acme-choose",
        "command": "bin/choose-best",
        "args": []
      }
    ],
    "hooks": [
      {
        "event": "pre_tool_use",
        "matcher": "bash",
        "type": "command",
        "command": "bin/hook-pre-bash.sh"
      }
    ],
    "panes": [
      {
        "id": "acme.status",
        "path": "panes/status.json",
        "abi": "reserved"
      }
    ]
  }
}
```

### 3.3 Passive-only example

```json
{
  "schemaVersion": 1,
  "id": "community.nord-extras",
  "version": "0.3.1",
  "name": "Nord extras",
  "description": "Additional Nord-family theme and a read-only explore agent.",
  "strike": { "min": "0.2.0" },
  "capabilities": ["themes", "agents"],
  "contributions": {
    "themes": [{ "path": "themes/nord-soft.json" }],
    "agents": [{ "path": "agents/explore-nord.md" }]
  }
}
```

### 3.4 Executable-focused example

```json
{
  "schemaVersion": 1,
  "id": "acme.tooling",
  "version": "2.0.0",
  "name": "Acme tooling",
  "strike": { "min": "0.2.0" },
  "capabilities": ["mcp.stdio", "harnesses", "hooks.command"],
  "contributions": {
    "mcp": [
      {
        "name": "acme-db",
        "transport": "stdio",
        "command": "bin/acme-db-mcp",
        "args": ["serve"],
        "env": {
          "ACME_TOKEN": "secret://env/ACME_TOKEN"
        }
      }
    ],
    "harnesses": [
      {
        "name": "acme-search",
        "command": "bin/acme-search",
        "args": ["--jsonl"]
      }
    ],
    "hooks": [
      {
        "event": "post_tool_use",
        "matcher": "edit",
        "type": "command",
        "command": "bin/notify-edit.sh"
      }
    ]
  }
}
```

Executable entries **must not** start until trust is recorded (§5). Passive
entries from an enabled plugin may load without that trust record
([#726](https://github.com/jonathanung/strike/issues/726)); executable
activation is [#728](https://github.com/jonathanung/strike/issues/728).

---

## 4. Discovery, precedence, namespacing, collisions, enablement

### 4.1 Discovery order (planned)

Deterministic merge order for **enabled** plugins (later wins where noted):

1. Built-in Strike surfaces (not plugins).
2. Global non-plugin roots (`~/.strike/agents`, skills, workflows, themes, …).
3. **Global plugins** — enabled entries under `~/.strike/plugins/`, ordered by
   plugin ID ascending for stability, then contribution path ascending.
4. Project non-plugin roots (`./.strike/agents`, …).
5. **Project plugins** — enabled entries under `./.strike/plugins/`, same
   sort.
6. Managed/MDM policy still applies as a ceiling on security dials
   ([config.md](config.md#managed--mdm-config-enterprise)); plugins cannot
   weaken managed denies.

Peer import trees (`.claude`, `.opencode`) stay **outside** the plugin system;
they continue to use existing discovery ([agents-skills.md](agents-skills.md)).

### 4.2 Enablement

- Default for a newly installed plugin: **enabled for passive** contributions
  only after successful manifest validation; **executable contributions stay
  inactive** until explicit trust (§5).
- Disablement is sticky in the lockfile/config: a disabled plugin contributes
  **nothing** (passive or executable) on the next launch.
- Hot reload is a non-goal for wave 1–2; changes apply on process restart
  (or an explicit reload command if added later without changing this contract).

### 4.3 Namespacing

| Surface | Public name rule |
|---|---|
| Agents, skills, workflows, themes | Contribution keeps its **own** declared name (frontmatter / JSON `id` / filename). Plugin ID is attached as **provenance** in diagnostics, not forced into the user-visible name. |
| MCP servers | `contributions.mcp[].name` is the server slug (`mcp_<name>_…` tools). Must match existing MCP slug rules: `^[A-Za-z][A-Za-z0-9_-]*$`. |
| Harnesses | `contributions.harnesses[].name` is the harness registry name referenced from agent frontmatter. |
| Provider profiles | `profileName` (or object key) is the provider name in `/provider`. |
| Panes | `contributions.panes[].id` — reserved; ABI in [#522](https://github.com/jonathanung/strike/issues/522). |

Diagnostics and `/context`-style provenance SHOULD show
`plugin=<id>@<version> source=<…> path=<rel>`.

### 4.4 Collisions

| Case | Behavior |
|---|---|
| Same plugin ID in global and project | **Project wins** entirely (project install shadows global for that ID). |
| Two different plugin IDs contribute the same agent/skill/workflow/theme name | **Higher precedence source wins** (§4.1). Loser is skipped with an explicit diagnostic (not silent). |
| Two plugins contribute the same MCP server name or harness name | **Fail closed** for the colliding executable contribution; emit diagnostic. Do not merge commands. |
| Plugin contribution vs non-plugin project file same name | Non-plugin project root wins over global plugins; project plugins win over project non-plugin only when the plugin layer is higher in §4.1 (project plugins are highest user layer). |
| Duplicate paths inside one manifest | Reject manifest. |
| Malformed plugin | Skip that plugin; **must not** shadow another source silently ([#726](https://github.com/jonathanung/strike/issues/726) AC). |

---

## 5. Trust model

### 5.1 Trust classes

| Class | Applies to | Startup / invoke |
|---|---|---|
| **Passive** | agents, skills, workflows, themes, provider profiles | Load when plugin enabled + manifest valid + Strike version OK. No subprocess. |
| **Executable** | MCP (stdio command), harnesses, shell hooks (`type: command`) | **Blocked** until a trust record matches current source identity + content digest + capability set. |
| **Networked config** | MCP `transport: http` | Treated as executable-class for trust (may send headers/secrets to a URL) even without a local binary. |
| **Reserved** | panes (`abi: reserved`) | Not activated by this contract; pane host issues define runtime trust. |

Stock config paths (`~/.strike/mcp.jsonc`, config `hooks`, config `harnesses`)
remain as today: project-local commands are trusted like other local scripts
the user edited. **Plugin-sourced** executables are stricter: no silent
startup from a git clone or catalog install.

### 5.2 Content digest

- Algorithm: **SHA-256**, encoded `sha256:` + lowercase hex.
- **Canonical payload:** sorted list of all regular files under `<plugin-root>`
  except:
  - VCS metadata (`.git/**`)
  - editor junk (`.DS_Store`, `*.swp`)
  - optional local override files explicitly listed in lockfile as
    non-payload (none in v1 by default)
- For each file: length + path relative to root (slash-separated, no leading
  `./`) + raw bytes. Symlinks: digest the referent **only if** it stays inside
  the root; otherwise reject the package.
- Manifest field `digest`, when present, must equal the computed digest or
  install/load fails.
- Lockfile stores the digest that was reviewed.

### 5.3 Trust record binding

A trust grant is valid only when **all** match:

1. Plugin `id`
2. Source identity (§6) — e.g. git URL + resolved commit, or catalog slug +
   version, or absolute local path identity
3. Content digest
4. Capability set actually granted (subset of declared `capabilities` /
   inferred executable kinds)

### 5.4 Invalidation

Trust is **invalidated** (executable contributions stop; user must re-review)
when any of the following change:

- Content digest (any payload file change)
- Source identity (different remote, commit, catalog version, or local path
  replacement)
- New or changed executable contribution entries (command, args, env keys,
  hook command, MCP URL/headers keys)
- `schemaVersion` increase that the running Strike does not implement
- Plugin `version` change that alters executable capability tags

Passive-only file edits that change the digest still invalidate executable
trust (fail closed): reviewers re-confirm the whole package. Implementation
MAY present a diff of capability/executable changes to speed review
([#729](https://github.com/jonathanung/strike/issues/729)).

### 5.5 Explicit exclusions (normative)

Implementations MUST NOT:

1. Load **in-process Go plugins** (`plugin.Open`) or dlopen-style native
   modules from bundles.
2. Host **Node/TypeScript plugin APIs** (OpenCode plugin runtime) — see
   [peer-ecosystem.md](peer-ecosystem.md).
3. Allow provider contributions to register **arbitrary wire/auth/streaming
   code** — only profiles for **shipped** adapters (§7.5).
4. **Auto-start** MCP/harness/hook executables from a plugin without a matching
   trust record.
5. Treat catalog metadata alone as trust (metadata cannot enable execution).

---

## 6. Source identities

### 6.1 `local`

| Field | Meaning |
|---|---|
| `type` | `local` |
| `path` | Absolute path to plugin root after install copy, or user-supplied path |

Install copies or links into the scope root; runtime identity is the installed
path + digest. Path must stay under the configured plugins root (no escape).

### 6.2 `git`

| Field | Meaning |
|---|---|
| `type` | `git` |
| `url` | HTTPS or SSH git URL |
| `ref` | Optional branch/tag name used at resolve time |
| `commit` | **Required** pinned full commit SHA after install |
| `subdir` | Optional subdirectory inside the repo |

Mutable branches MUST NOT be followed silently on update — pin `commit`
([#727](https://github.com/jonathanung/strike/issues/727)). Digest is of the
checked-out plugin subtree.

### 6.3 `catalog`

| Field | Meaning |
|---|---|
| `type` | `catalog` |
| `registry` | Catalog base identity (URL or named registry id) |
| `package` | Package slug in the catalog |
| `version` | Immutable published version |
| `url` | Artifact URL |
| `digest` | Expected artifact/package digest from catalog metadata |

Catalog format and transport are owned by
[#729](https://github.com/jonathanung/strike/issues/729). Contract requirement:
remote install pins version + verified digest; catalog rows cannot execute
content by themselves.

### 6.4 Update capability review

Before accepting an update (git pull to new commit, catalog newer version, or
local replace), the lifecycle UX MUST show:

- Old → new `version` and content digest
- Added/removed/changed contribution types
- Executable command/args/env/URL diffs
- Capability tag diffs (`capabilities` and inferred executable kinds)

User confirmation creates a **new** trust record; the previous working version
remains until the update commits atomically
([#727](https://github.com/jonathanung/strike/issues/727),
[#729](https://github.com/jonathanung/strike/issues/729)).

---

## 7. Contribution matrix

For each type: validation, naming, precedence, lifecycle, trust.

### 7.1 Agents (`contributions.agents`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "path": "<rel .md>" }` |
| **Validation** | Existing agent markdown loader rules for Strike-native trees (fail closed on invalid names/effort/permissions). Path must be `.md` under the plugin root. |
| **Naming** | Agent name from frontmatter/`filename`; uniqueness per §4.4. |
| **Precedence** | §4.1; same-name override with diagnostic on the loser. |
| **Lifecycle** | Loaded at session start when plugin enabled; unavailable when disabled. No process. |
| **Trust** | Passive. |

### 7.2 Skills (`contributions.skills`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "path": "<rel>" }` — file `name.md` or `name/SKILL.md`. |
| **Validation** | Existing skill loader; markdown only — no JS/TS execution. |
| **Naming** | Skill invoke name from frontmatter or directory name. |
| **Precedence** | §4.1. |
| **Lifecycle** | Catalog at start; `/skill` and skill tool see enabled plugins only. |
| **Trust** | Passive. |

### 7.3 Workflows (`contributions.workflows`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "path": "<rel .json>" }` |
| **Validation** | Workflow schema v1 strict decode + structural validate ([agents-skills.md](agents-skills.md#workflows)). |
| **Naming** | Workflow `name` field; source provenance `plugin`. |
| **Precedence** | §4.1; project non-plugin and project plugin layers as specified. |
| **Lifecycle** | Listed in catalog; activation remains a separate user/tool step (scaffold never activates). |
| **Trust** | Passive. Phase permission widening still requires existing approval UX. |

### 7.4 Themes (`contributions.themes`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "path": "<rel .json>" }` |
| **Validation** | Existing theme JSON schema ([theme.md](theme.md)). |
| **Naming** | Theme `id` inside file. |
| **Precedence** | §4.1 (after bundled, before or with user theme dirs as loaders specify — plugin project > plugin global). |
| **Lifecycle** | Available to `/theme` when enabled. |
| **Trust** | Passive. Packaging tracked further in [#511](https://github.com/jonathanung/strike/issues/511). |

### 7.5 Provider profiles (`contributions.providers`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "path": "<rel>", "profileName": "<name>" }` or a single-profile JSON object file. |
| **Validation** | Same shape as `providers.jsonc` custom/overlay entries ([config.md](config.md#custom-providers)). |
| **Naming** | `profileName` / object key. |
| **Precedence** | §4.1; cannot override managed policy. |
| **Lifecycle** | Merged into provider catalog when enabled. |
| **Trust** | Passive **configuration only**. |
| **Hard limit** | Profiles may target **shipped wire adapters only** (openai-compatible, anthropic-compatible, and built-in ids). Fields like `npm` are hints only and are **never** loaded as code. No bundle may register new Go provider implementations, OAuth handlers, or streaming parsers. Credentials stay in env refs / auth store — never in plugin files. |

### 7.6 MCP (`contributions.mcp`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "name", "transport", "command"?, "args"?, "env"?, "url"?, "headers"? }` aligned with [config.md](config.md#mcp-servers-stdio--http). Relative `command` resolves inside plugin root (`bin/…`). |
| **Validation** | Existing MCP server field rules; path confinement on command; env/header **values** must be secret refs or non-secret literals — never print values in doctor/UI. |
| **Naming** | Server `name` slug; tools `mcp_<name>_<tool>`. |
| **Precedence** | Name collision fail closed (§4.4). User `mcp.jsonc` entries are a separate source; document winner in loader diagnostics (recommended: explicit user config overrides plugin on same name). |
| **Lifecycle** | Registered only when plugin enabled **and** trusted (#728). Start with session like config MCP; disablement stops process and unregisters tools. |
| **Trust** | **Executable** (stdio and http). |

### 7.7 Harnesses (`contributions.harnesses`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "name", "command", "args"? }` — external process only ([harnesses.md](harnesses.md)). |
| **Validation** | Command path confinement; no embedded Go harness registration from plugins. |
| **Naming** | Harness registry name; agent frontmatter `harness: <name>`. |
| **Precedence** | Name collision fail closed. |
| **Lifecycle** | Available to `task` children when trusted; root turns never invoke harnesses (existing engine rule). |
| **Trust** | **Executable**. Still unsandboxed process isolation model as today — trust review is mandatory because of that. |

### 7.8 Hooks (`contributions.hooks`)

| Axis | Rule |
|---|---|
| **Entry** | Same fields as config `hooks[]` ([config.md](config.md), [peer-ecosystem.md](peer-ecosystem.md#hooks-alignment)): `event`, `matcher`, `type`, `command` / declarative fields. |
| **Validation** | Existing hook schema; `command` path confinement when `type` is command/shell. |
| **Naming** | No global name; identity is plugin id + index/path for diagnostics. |
| **Precedence** | Concatenate like global-then-project hooks: after user global, plugin global, user project, plugin project (exact order fixed in #728 to match §4.1). |
| **Lifecycle** | Matched at runtime only if plugin enabled + trusted for command hooks. Declarative-only hooks follow passive load rules but still require plugin enablement. |
| **Trust** | Command/shell hooks: **executable**. Pure declarative hooks: passive. |

### 7.9 Panes (`contributions.panes`)

| Axis | Rule |
|---|---|
| **Entry** | `{ "id", "path", "abi": "reserved" }` for schemaVersion 1. |
| **Validation** | Manifest accepts the entry for forward-compat; loaders **must not** execute pane code under this contract alone. |
| **Naming** | Pane `id` unique across enabled plugins. |
| **Precedence** | Defined by [#522](https://github.com/jonathanung/strike/issues/522) / [#731](https://github.com/jonathanung/strike/issues/731). |
| **Lifecycle** | Reserved. |
| **Trust** | Reserved; will not expose private Go `window` interfaces or in-process TUI plugins. |

---

## 8. Schema version compatibility

| Running Strike \ Manifest | Behavior |
|---|---|
| Implements `schemaVersion` **N**, manifest `N` | Load normally. |
| Implements max **N**, manifest `M` where `M < N` | Load if Strike still supports `M` (v1 support remains until a future major deprecation). |
| Manifest `M` where `M >` Strike max | **Skip plugin** with explicit diagnostic: upgrade Strike. Do not partial-load. |
| Missing `schemaVersion` | Reject manifest. |
| Unknown fields at version N | Reject (strict). Future N+1 may add fields only with a version bump. |
| Removing/renaming fields | Only in a new `schemaVersion` with a documented migration. |

**Forward compatibility for catalogs:** registries MAY list multiple
artifacts per plugin version for different schema versions; clients pick the
highest schema they implement.

**Backward compatibility promise:** schemaVersion 1 remains readable by all
Strike releases that advertise plugin support until a future major release
announces removal in CHANGELOG **Upgrade note**.

---

## 9. Path confinement (normative)

1. Reject contribution paths that are absolute or contain `..` segments after
   cleaning.
2. After `filepath.EvalSymlinks` (or equivalent), the resolved path MUST have
   the plugin root as prefix.
3. `command` fields: if relative, resolve under plugin root; if absolute, allow
   only when the trust review UI showed the absolute path and the path is not
   rewritten by the plugin after trust (digest covers scripts; absolute system
   binaries are part of the reviewed entry text).
4. Archive extraction (catalog installs) MUST guard against zip-slip / tar
   traversal ([#729](https://github.com/jonathanung/strike/issues/729)).
5. Plugin roots themselves MUST live under `~/.strike/plugins` or
   `./.strike/plugins` (or test overrides via env used only in tests).

---

## 10. Secret handling (normative)

1. **No secrets in bundles** — keys, tokens, PEMs, `.env` files with
   credentials.
2. MCP `env` / `headers` and similar fields SHOULD use
   `secret://env/NAME` or `{env:NAME}` forms ([secrets.md](secrets.md));
   resolve only at process start, never into model-visible output or doctor
   text.
3. Doctor / plugin inspect / logs print **ref names and redacted placeholders**,
   never resolved values ([#727](https://github.com/jonathanung/strike/issues/727)).
4. Session JSONL and exports continue to run through `secret.RedactEvent` /
   `pkg/redact`; plugin provenance metadata is non-secret.
5. Trust records and lockfiles store digests and source identity, not env
   contents.

---

## 11. Lifecycle summary (implementation map)

| Stage | Issue | Contract touchpoints |
|---|---|---|
| Contract | #725 (this doc) | Manifest, matrix, trust, schema |
| Passive load | #726 | §3–4, §7.1–7.5, §8–10 |
| Local/Git CLI | #727 | §2, §6.1–6.2, enablement, doctor |
| Executable activation | #728 | §5, §7.6–7.8 |
| Catalog / updates | #729 | §6.3–6.4, digest verify |
| TUI manager | #730 | UX over enablement + trust |
| Themes packaging | #511 | §7.4 |
| Pane ABI | #522 | §7.9 |
| Pane host / web | #731 #732 | outside this freeze except reserved entries |

---

## 12. Non-goals (restated)

- Implementing loaders, CLI, or TUI in this change.
- A generic arbitrary-code plugin ABI or in-process extension mechanism.
- Automatic unattended updates.
- Replacing stock `mcp.jsonc` / config hooks / harnesses (plugins are additive
  packages with stronger trust for executables).

---

## 13. Acceptance mapping (#725)

| AC | Section |
|---|---|
| Normative manifest + passive and executable examples | §3 |
| Every contribution type: validation, naming, precedence, lifecycle, trust | §7 |
| Trust binds to source + content digest; invalidation on relevant changes | §5 |
| Path confinement + secret-handling explicit | §9, §10 |
| Forward/backward schema-version behavior | §8 |
