# Plugin bundle contract

Normative contract for **versioned plugin packages**. Strike is a client of
[Agent Plugins](https://agent-plugins.org/) **1.0.0**
([specification](https://agent-plugins.org/specification)): that format is
the **native on-disk package**. Portable components are **skills** and **MCP
servers**. Strike-only surfaces (agents, workflows, themes, provider profiles,
harnesses, hooks, panes, and extra Strike-flat skills) live in the
`com.strike.cli` client-extension namespace.

This document supersedes the Strike-native `schemaVersion` + `contributions`
authoring contract from [#725](https://github.com/jonathanung/strike/issues/725)
as the format authors MUST write. Loaders still honor that legacy shape as a
**deprecated** load path (see §3.10). Lifecycle (install roots, lockfile,
digest, trust, catalog, TUI `/plugin`) remains Strike-owned, as the spec
allows.

A plugin is **not** a new runtime, a Node/Go host ABI, or a way to inject
arbitrary provider code.

Implementations must not weaken these rules. Loader/CLI code lands in
[#1143](https://github.com/jonathanung/strike/issues/1143)–[#1147](https://github.com/jonathanung/strike/issues/1147);
this document is the contract they implement ([#1142](https://github.com/jonathanung/strike/issues/1142),
epic [#1141](https://github.com/jonathanung/strike/issues/1141)).

| Status | Meaning |
|---|---|
| **Contract (this doc)** | Normative. Loaders and CLI must conform. Native format: Agent Plugins 1.0.0. |
| **Shipped loaders** | Strike-native packages (#726–#730). Agent Plugins loaders: #1143+. |
| **Passive load (#726)** | Discovery + load of agents, skills, workflows, themes, and provider profiles from enabled local plugin trees. |
| **Lifecycle CLI (#727)** | `strike plugin` install/list/inspect/enable/disable/remove/doctor for local + Git sources. |
| **Catalog / updates (#729)** | Remote `catalog.json`, search, catalog install, outdated, update with review; digest verify + zip-slip guards. |
| **TUI manager (#730)** | `/plugin` modal over `host.Plugins` (browse, install, trust, update, remove). |
| **Theme contributions (#511)** | Plugin themes load via `theme.Catalog`; `/theme` preview/apply/revert + provenance. |
| **Pane hosts** | TUI (#731) / web (#732). ABI: [plugin-panes.md](plugin-panes.md). Packaging: §7.9. |
| **Out of scope forever (v1 model)** | In-process Go `plugin` packages, OpenCode-style Node plugin hosts, arbitrary provider/auth/streaming adapters, silent executable startup from an untrusted bundle. |

Related: [Agent Plugins spec](https://agent-plugins.org/specification),
[agents-skills.md](agents-skills.md), [config.md](config.md),
[harnesses.md](harnesses.md), [theme.md](theme.md), [secrets.md](secrets.md),
[plugin-panes.md](plugin-panes.md) (pane ABI; this doc covers packaging only),
[protocol.md](protocol.md#extension-points-plugins--hooks--harnesses),
[peer-ecosystem.md](peer-ecosystem.md).

---

## 1. Definitions

| Term | Definition |
|---|---|
| **Plugin** | A directory tree with a root `plugin.json` plus optional components. Native packages follow Agent Plugins 1.0.0. |
| **Plugin name** | Agent Plugins `name` (constraints in §3.3). This is the plugin identity used in lockfiles, install directories `~/.strike/plugins/<name>/`, diagnostics, and catalog slugs. Replaces Strike-native `id`. |
| **Display name** | Optional human label `extensions.com.strike.cli.displayName`. Not an identity key. |
| **Bundle version** | Manifest `version` (Semantic Versioning RECOMMENDED). Independent of Strike's release version. |
| **APS version** | Agent Plugins specification version declared by `plugin.json` `$schema` (this contract: **1.0.0**). |
| **Source identity** | How the bundle was obtained: `local`, `git`, or `catalog` (see §6). Strike-owned. |
| **Content digest** | Canonical SHA-256 over the package payload (see §5.2). Strike-owned. |
| **Portable component** | Agent Plugins v1 types only: skills under `skills/<name>/SKILL.md`, MCP servers in root `mcp.json`. Not declared inline in `plugin.json`. |
| **Strike-only contribution** | Agents, workflows, themes, providers, harnesses, hooks, panes, Strike-flat skills, and Strike `bin/` — discovered from `com.strike.cli/` (and optional `extensions.com.strike.cli` metadata). |
| **Passive** | Declarative assets loaded without starting a subprocess (agents, skills, workflows, themes, provider profiles, static panes). |
| **Executable** | Contributions that start a process or run shell (MCP stdio, harness commands, shell hooks, process panes). Networked MCP (`streamable-http` / `sse`) is executable-class for trust. Require explicit trust (§5). |
| **Enablement** | User/project flag: disabled plugins contribute **nothing** on the next launch. |
| **Trust record** | Durable binding of `(plugin name, source identity, content digest, capability set)` after explicit user review. |
| **PLUGIN_ROOT** | Absolute filesystem-resolved plugin root. Set on plugin subprocesses (Agent Plugins §9). |
| **PLUGIN_DATA** | Client-managed writable data directory for that installed plugin instance (Agent Plugins §9). Strike paths: §2.2. |
| **Legacy package** | Strike-native `plugin.json` / `plugin.jsonc` with `schemaVersion` + `id` + `contributions`. Deprecated load path (§3.10). |

---

## 2. Package layout

Native (Agent Plugins 1.0.0) layout:

```text
<plugin-root>/
  plugin.json                 # required APS manifest (JSON only; not JSONC)
  skills/                     # optional portable Agent Skills
    <skill-name>/
      SKILL.md                # required per skill; immediate child dirs only
      scripts/                # optional (Agent Skills spec)
      references/             # optional
      assets/                 # optional
  mcp.json                    # optional portable MCP servers
  com.strike.cli/             # optional Strike-only extension directory
    agents/                   # optional *.md agents
    skills/                   # optional Strike-flat extras (name.md only)
    workflows/                # optional *.json workflows (schemaVersion 1)
    themes/                   # optional theme JSON files
    providers/                # optional provider profile fragments
    harnesses/                # optional harness JSON definitions
    hooks/                    # optional hook JSON definitions
    panes/                    # optional pane definitions (ABI: plugin-panes.md)
    bin/                      # optional Strike-only executables
  bin/                        # optional portable/shared executables (e.g. MCP command ./bin/…)
  LICENSE                     # optional
  README.md                   # optional human docs (not loaded as a contribution)
  assets/                     # optional non-code assets (icons, fixtures)
```

Rules:

1. **Root confinement.** All package paths Strike reads MUST resolve inside
   `<plugin-root>` after cleaning (`..`, symlinks that escape, absolute paths
   outside the root are rejected). Loaders fail closed with a diagnostic.
   Agent Plugins §4.1 containment applies to portable paths (`command` / `cwd`
   in `mcp.json`, discovered `SKILL.md`). Strike applies the same confinement
   to `com.strike.cli/` paths. `${PLUGIN_DATA}`-rooted MCP `cwd` MUST stay
   inside the filesystem-resolved plugin data directory (spec §7.2.1).
2. **No hidden roots.** Strike does not scan arbitrary repo paths for plugins.
   Install/discovery roots are only the configured global and project plugin
   directories (and lockfile-recorded installs) — see §2.1.
3. **Fixed portable locations.** `plugin.json` MUST NOT contain inline
   portable skills or MCP configuration and MUST NOT override discovery paths.
   Skills come from `skills/`; MCP from root `mcp.json` (Agent Plugins §6).
4. **Manifest is JSON.** Native packages use `plugin.json` only. `plugin.jsonc`
   is legacy (§3.10). Strike MUST NOT treat a sidecar as replacing APS
   `plugin.json`.
5. **Undeclared files.** Files outside the discovery locations in this section
   are ignored for loading (they may still affect the content digest — §5.2).
   Other clients' extension directories (`com.other.client/`, …) are ignored.
6. **No credentials in the tree.** Auth material, API keys, tokens, and
   private key material MUST NOT ship inside a plugin bundle. Use secret refs
   and the auth store ([secrets.md](secrets.md), [auth.md](auth.md)). Agent
   Plugins treats MCP `env` / `headers` as visible package data, not a portable
   secret mechanism.

### 2.1 Install roots (passive load #726 / lifecycle #727)

| Scope | Directory | Precedence |
|---|---|---|
| Global | `~/.strike/plugins/<name>/` | lower |
| Project | `./.strike/plugins/<name>/` | higher |

`<name>` is the Agent Plugins `name` (legacy packages: Strike `id`).

Lockfile: `~/.strike/plugins.lock.json` and/or `./.strike/plugins.lock.json`.
Passive load (#726) honors per-plugin `enabled` (default **true** when absent).
Lifecycle commands (#727) record source identity, pinned version, content digest,
and enablement — **never** credentials.

```json
{
  "schemaVersion": 1,
  "plugins": {
    "acme.review-pack": {
      "enabled": true,
      "version": "1.2.0",
      "digest": "sha256:…",
      "installedAt": "2026-08-06T12:00:00Z",
      "source": {
        "type": "git",
        "url": "https://github.com/acme/review-pack.git",
        "ref": "main",
        "commit": "0123456789abcdef0123456789abcdef01234567"
      }
    }
  }
}
```

Local source identity uses `"type":"local"` and `"path"` (absolute path supplied
at install time). Git installs **must** pin `commit` (full SHA); mutable branches
are never followed silently on later launches. Catalog installs use
`"type":"catalog"` with `registry`, `package`, `version`, artifact `url`, and
artifact `digest` (see §6.3). Optional lockfile `trust` is cleared when an update
changes digest, source identity, or executable contributions (#728/#729).

### 2.2 `PLUGIN_DATA`

Strike-managed persistent data for an installed plugin instance (Agent Plugins
§9). Created before launching a plugin subprocess; writable to that subprocess;
preserved across plugin updates; MAY be deleted on uninstall.

| Scope | Directory |
|---|---|
| Global install | `~/.strike/plugin-data/<name>/` |
| Project install | `./.strike/plugin-data/<name>/` |

Subprocess environment: `PLUGIN_ROOT` = filesystem-resolved plugin root;
`PLUGIN_DATA` = the matching row above. After overlaying configured `env`,
Strike MUST set these two names itself (spec §9.1). An MCP server `env` object
MUST NOT contain `PLUGIN_ROOT` or `PLUGIN_DATA` keys (invalid server entry).

Use `PLUGIN_DATA` for caches, generated files, and installed dependencies that
must survive package replacement. Use `PLUGIN_ROOT` for bundled scripts,
binaries, and config that ship in the package.

### 2.3 Lifecycle CLI (`strike plugin`, #727 / #729)

| Command | Behavior |
|---|---|
| `install <path\|git-url\|catalog:pkg[@ver]>` | Validate, copy/clone/download into scope root, write lockfile. Atomic: failed validation leaves no partially enabled plugin. |
| `search <query> --registry <url>` | Search a remote catalog index. |
| `outdated [--registry]` | List catalog-sourced installs with a newer published version. |
| `update <name> --yes` | Show contribution/capability review, then install newer catalog version (rollback-safe). |
| `migrate <name\|path>` | Convert a **legacy** Strike-native bundle to Agent Plugins 1.0.0. Stage, validate with the APS loader, then replace. Failed migrate leaves the prior tree enabled. Installed plugins require `--yes`; `--dry-run` prints the plan. After an installed migrate: recompute digest, **clear trust**, print a review summary (do not auto-trust). Refuse already-APS packages. |
| `list` / `inspect <name>` | Show installed plugins (including disabled) with scope, digest, source, trust state. |
| `enable` / `disable <name>` | Toggle lockfile `enabled`. Disable **preserves** source files; disabled plugins contribute nothing (passive or executable) on next launch. |
| `trust <name>` | Record executable trust for the current content digest + source identity + capability set. Required before MCP/harness/shell-hook/process-pane activation. |
| `untrust <name>` | Remove the trust grant; executables stay inactive on next launch. Passive load is unaffected. |
| `remove <name> --yes` | Delete install directory and lockfile entry (confirmation required). |
| `doctor [name]` | Paths, provenance, contribution summary, collisions, trust state (`none` / `trusted` / `stale` / `n/a-passive-only`), format (`aps` / `legacy`). Never prints secrets or MCP/harness env values (keys only). |

Flags: `--scope global|project` (install defaults to global), git `--ref` /
`--commit` / `--subdir`, catalog `--registry` / `--version`, install `--force`
to replace. Project scope uses the process working directory's `./.strike`.
Install destinations cannot escape the configured plugins roots. Lockfile
updates use an exclusive advisory lock plus atomic rename so concurrent
lifecycle ops are safe. Updates are never unattended (`--yes` required after
review). `migrate` of an installed plugin is never unattended (`--yes` after
the plan).

Drop a validated bundle under a plugins root manually if needed (directory name
need not match `name`; the manifest `name` is authoritative). Restart Strike to
pick up changes (hot reload is a non-goal).

---

## 3. Normative Agent Plugins manifest (native)

Native packages target Agent Plugins **1.0.0**. The specification text is
authoritative for portable fields when it conflicts with this document; this
document is authoritative for Strike lifecycle, trust, and `com.strike.cli`.

### 3.1 Format detection

| Root file / fields | Format | Load |
|---|---|---|
| `plugin.json` with `$schema` `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json` | **APS** (native) | Load per this section. JSON only. |
| `plugin.json` or `plugin.jsonc` with Strike `schemaVersion` and Strike fields (`id`, `contributions`, …) | **Legacy** | Load per §3.10 with a deprecation diagnostic. |
| Neither, or both required identity sets missing / contradictory | **Invalid** | Reject plugin; do not discover components. |

APS `$schema` selects locally supported validation. Strike MUST NOT fetch a
schema while loading. An unrecognized APS `$schema` version → **skip plugin**
with diagnostic (spec §5.2). Strike currently recognizes only 1.0.0.

### 3.2 Top-level fields (closed set)

The only permitted APS top-level fields are `$schema`, `name`, `version`,
`description`, `author`, `homepage`, `repository`, `license`, `keywords`, and
`extensions` (spec §5.2).

| Field | Required | Type | Rules |
|---|---|---|---|
| `$schema` | yes | string | MUST be `https://agent-plugins.org/schemas/1.0.0/plugin.schema.json` for this contract. |
| `name` | yes | string | Plugin identity. Constraints §3.3. |
| `version` | no | string | Bundle version. Semver RECOMMENDED; Strike MUST NOT reject solely because it is not Semver (spec §5.4). Lifecycle still records whatever string is present. |
| `description` | no | string | Short summary. |
| `author` | no | object | Optional `name`, `email`, `url` strings only. |
| `homepage` | no | string | Documentation URL. |
| `repository` | no | string | Source repository URL. |
| `license` | no | string | SPDX identifier RECOMMENDED. |
| `keywords` | no | string[] | Search tags. |
| `extensions` | no | object | Client namespaces → objects. Strike reads `com.strike.cli` only (§3.8). |

Missing/invalid `$schema` or `name`, wrong types for permitted fields (other
than the non-fatal cases below), or invalid `author` members: **reject the
plugin**; do not discover components.

`plugin.json` MUST NOT contain inline `contributions`, top-level `id`,
top-level `strike`, or other Strike-native keys. Those belong in legacy
packages or under `extensions.com.strike.cli`.

### 3.3 Plugin name constraints

Agent Plugins §5.5 (native identity):

| Constraint | Requirement |
|---|---|
| Length | 1–64 characters inclusive |
| Character set | `a-z`, `0-9`, `-`, `.` only |
| Start and end | Alphanumeric |
| Repetition | No `--` or `..` |

Valid: `my-plugin`, `acme.tools`, `lint3r`, `a`.
Invalid: `My-Plugin`, `-start`, `has--double`, `too.many..dots`, empty.

### 3.4 Unknown fields (spec §5.2)

| Case | Behavior |
|---|---|
| Unknown **top-level** APS field | **Report and ignore.** Continue loading. MUST NOT assign semantics. MUST NOT strict-reject the plugin. |
| `extensions` present but not an object | Report and ignore `extensions`; continue loading portable components. |
| Unknown `extensions.<other-namespace>` | Ignore without validating contents (spec §8.1). |
| Unknown keys inside `extensions.com.strike.cli` | **Report and ignore** (forward compatible). Do not reject the plugin. |
| Invalid type for a **known** `com.strike.cli` key | Skip Strike-only contributions from that plugin with diagnostic; portable skills/MCP still load. |
| Any other `plugin.json` schema violation | **Reject plugin** (fatal). |

This is an intentional change from Strike-native strict unknown-field reject.

### 3.5 APS manifest example

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "acme.review-pack",
  "version": "1.2.0",
  "description": "Review skill, MCP linter, and Strike-only review agent.",
  "author": {
    "name": "Acme",
    "url": "https://example.com"
  },
  "homepage": "https://docs.example.com/review-pack",
  "repository": "https://github.com/acme/review-pack",
  "license": "MIT",
  "keywords": ["review", "lint"],
  "extensions": {
    "com.strike.cli": {
      "displayName": "Acme Review Pack",
      "strike": {
        "min": "0.2.0"
      },
      "capabilities": [
        "agents",
        "skills",
        "mcp.stdio",
        "panes"
      ]
    }
  }
}
```

A valid APS plugin MAY omit `extensions` and `com.strike.cli/` entirely
(portable skills and/or `mcp.json` only).

### 3.6 `mcp.json` example

Root `mcp.json` only. JSON object with required `$schema` and `mcpServers`;
no other top-level fields. `$schema` version MUST match `plugin.json`
(mismatch → disable MCP for that plugin; other components still load).

Strike maps `stdio` and `streamable-http` onto existing MCP. `sse` is
**optional**; unsupported transports are skipped per server with a diagnostic
(plugin continues). Trust is still required before executable start (§5).

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
  "mcpServers": {
    "acme-lint": {
      "type": "stdio",
      "command": "./bin/acme-lint-mcp",
      "args": ["--serve", "--data", "${PLUGIN_DATA}/lint"],
      "env": {
        "CONFIG": "${PLUGIN_ROOT}/config.json"
      },
      "cwd": "${PLUGIN_ROOT}"
    },
    "acme-cloud": {
      "type": "streamable-http",
      "url": "https://mcp.example.com/acme",
      "headers": {
        "X-Tenant": "public-tenant"
      }
    }
  }
}
```

`command` is a **single token**: `./` relative to plugin root, or a bare
executable name. Clients MUST NOT expand placeholders in `command`. Default
`cwd` is the plugin root. Expand `${PLUGIN_ROOT}` / `${PLUGIN_DATA}` only in
`args`, `env` values, and `cwd` (spec §9.2). Missing `mcp.json` is not an
error. Invalid `mcp.json` disables MCP for that plugin only.

### 3.7 Skills (portable)

Discover **immediate** child directories of `skills/` that contain a regular
file named exactly `SKILL.md` (Agent Skills spec). MUST NOT recurse. Invalid
skill → skip that skill, continue. Missing `skills/` is not an error. If
`skills` exists but is not a directory → treat skills as invalid for this
plugin; continue other component types.

Strike-flat `name.md` skills are **not** portable; they belong under
`com.strike.cli/skills/` (§7.2).

### 3.8 Strike extension `com.strike.cli`

Strike MAY use the manifest object, the directory, or both (spec §8).

#### Manifest object `extensions.com.strike.cli`

| Field | Required | Type | Rules |
|---|---|---|---|
| `displayName` | no | string | Human label (1–80 chars). Identity remains APS `name`. |
| `strike` | no | object | Compatibility range for the Strike binary. |
| `strike.min` | no | string | Inclusive minimum Strike semver. If **omitted**, do not skip the plugin solely for a missing Strike range. If present and unsatisfied → skip plugin with diagnostic. |
| `strike.max` | no | string | Optional exclusive upper bound. |
| `capabilities` | no | string[] | Declared capability tags for update review (§6.4). Inferred executable kinds still apply. |
| `digest` | no | string | Author-published expected content digest (`sha256:<hex>`). Installers verify when present; loaders recompute. |

No inline portable MCP/skills here. Optional explicit path lists for
panes/harnesses MAY be added later; **authoring default is directory
discovery** under `com.strike.cli/`.

#### Directory `com.strike.cli/`

| Path | Contents | Trust class |
|---|---|---|
| `agents/*.md` | Strike agent markdown | Passive |
| `skills/*.md` | Strike-flat extra skills (`name.md` only; not `name/SKILL.md`) | Passive |
| `workflows/*.json` | Workflow schema v1 | Passive |
| `themes/*.json` | Theme JSON | Passive |
| `providers/*` | Provider profile fragments | Passive (config only) |
| `harnesses/*.json` | Harness entries (`name`, `command`, `args`?, …) | Executable |
| `hooks/*.json` | Hook entries (same fields as config `hooks[]`); file MAY be one object or an array | Command/shell: executable; declarative: passive |
| `panes/*.json` | Pane definitions (`schemaVersion` 1, `id`, `mode`, …) — [plugin-panes.md](plugin-panes.md) | Static + none permissions: passive; `mode: process`: executable |
| `bin/` | Executables referenced by relative paths from Strike-only entries | Covered by the referencing contribution's trust class |

Directory discovery: regular files matching the patterns above, sorted by
relative path ascending. Invalid file → skip that entry with diagnostic;
plugin continues. Missing `com.strike.cli/` is not an error. The directory
MUST resolve inside the plugin root. Unknown keys inside
`extensions.com.strike.cli` are **reported and ignored** (forward compatible;
§3.4) — they do not reject the plugin or skip the directory. Invalid JSON
types for **known** keys skip Strike-only contributions only; portable skills
and `mcp.json` still load.

### 3.9 `com.strike.cli` example

```text
acme.review-pack/
  plugin.json
  skills/
    ship-review/
      SKILL.md
  mcp.json
  com.strike.cli/
    agents/
      reviewer-strict.md
    workflows/
      review-gate.json
    themes/
      acme-dark.json
    providers/
      acme-proxy.jsonc
    harnesses/
      choose-best.json
    hooks/
      pre-bash.json
    panes/
      status.json
    bin/
      choose-best
      hook-pre-bash.sh
```

`com.strike.cli/harnesses/choose-best.json`:

```json
{
  "name": "acme-choose",
  "command": "com.strike.cli/bin/choose-best",
  "args": []
}
```

`com.strike.cli/hooks/pre-bash.json`:

```json
{
  "event": "pre_tool_use",
  "matcher": "bash",
  "type": "command",
  "command": "com.strike.cli/bin/hook-pre-bash.sh"
}
```

Relative `command` paths resolve inside the plugin root (confinement §9).

### 3.10 Legacy Strike manifest (deprecated)

**Status:** deprecated **authoring** format. Still a **supported load path**
until a future major removal (announced in CHANGELOG **Upgrade note**; APS.6
[#1147](https://github.com/jonathanung/strike/issues/1147) stops *new* legacy
installs). Existing installs MUST keep loading.

Detection: root `plugin.json` or `plugin.jsonc` with integer `schemaVersion`
and Strike fields (`id`, `contributions`, typically `strike`). Loaders emit a
diagnostic `format=legacy` (doctor, inspect, stderr as appropriate). Do not
fail the plugin solely for being legacy.

Legacy identity is `id` (reverse-DNS or single-segment slug; max 128 chars) —
used as the lockfile / install-directory key for that package. `name` in the
legacy manifest is a display string, not APS identity.

Unknown top-level fields on **legacy** manifests remain **strict-reject**
(historical #725 posture). Do not apply APS report-and-ignore to legacy
documents.

Skills and MCP in legacy packages come from `contributions.skills` and
`contributions.mcp`, not from APS fixed locations (unless a future migrate
command rewrites the tree — [#1145](https://github.com/jonathanung/strike/issues/1145)).

#### Legacy example (load-only; do not author)

```jsonc
// plugin.jsonc — DEPRECATED. Loaders still accept this shape.
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
    "mcp.stdio",
    "panes"
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
        "abi": "pane/1"
      }
    ]
  }
}
```

Legacy contribution field rules remain those of #725 (path entries, MCP/harness
objects, pane `{id,path,abi}`). Trust, path confinement, and secrets in §5,
§9, and §10 still apply.

---

## 4. Discovery, precedence, namespacing, collisions, enablement

### 4.1 Discovery order

Deterministic merge order for **enabled** plugins (later wins where noted):

1. Built-in Strike surfaces (not plugins).
2. Global non-plugin roots (`~/.strike/agents`, skills, workflows, themes, …).
3. **Global plugins** — enabled entries under `~/.strike/plugins/`, ordered by
   plugin name ascending for stability, then contribution path ascending.
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
| Agents, skills, workflows, themes | Contribution keeps its **own** declared name (frontmatter / JSON `id` / filename). Plugin name is attached as **provenance** in diagnostics, not forced into the user-visible name. |
| MCP servers | APS `mcpServers` member name (legacy: `contributions.mcp[].name`) is the server slug (`mcp_<name>_…` tools). Must match existing MCP slug rules: `^[A-Za-z][A-Za-z0-9_-]*$`. |
| Harnesses | Entry `name` is the harness registry name referenced from agent frontmatter. |
| Provider profiles | `profileName` (or object key / filename stem) is the provider name in `/provider`. |
| Panes | Definition `id` (legacy: `contributions.panes[].id`) — unique across enabled plugins; ABI in [plugin-panes.md](plugin-panes.md). |

Diagnostics and `/context`-style provenance SHOULD show
`plugin=<name>@<version> source=<…> path=<rel>`.

### 4.4 Collisions

| Case | Behavior |
|---|---|
| Same plugin name in global and project | **Project wins** entirely (project install shadows global for that name). |
| Two different plugin names contribute the same agent/skill/workflow/theme name | **Higher precedence source wins** (§4.1). Loser is skipped with an explicit diagnostic (not silent). |
| Two plugins contribute the same MCP server name or harness name | **Fail closed** for the colliding executable contribution; emit diagnostic. Do not merge commands. |
| Plugin contribution vs non-plugin project file same name | Non-plugin project root wins over global plugins; project plugins win over project non-plugin only when the plugin layer is higher in §4.1 (project plugins are highest user layer). |
| Duplicate paths / duplicate skill directory names inside one plugin | Skip duplicates with diagnostic (APS skills) or reject that plugin's malformed Strike-only index if the duplicate is inside `com.strike.cli` and cannot be disambiguated. |
| Same skill name from portable `skills/<n>/SKILL.md` and `com.strike.cli/skills/<n>.md` in one plugin | Portable skill wins; Strike-flat extra skipped with diagnostic. |
| Malformed plugin | Skip that plugin; **must not** shadow another source silently ([#726](https://github.com/jonathanung/strike/issues/726) AC). |

---

## 5. Trust model

Unchanged in posture from #725: passive load when enabled; executables blocked
until trust matches. Identity key is plugin **name** (legacy: `id`).

### 5.1 Trust classes

| Class | Applies to | Startup / invoke |
|---|---|---|
| **Passive** | agents, skills, workflows, themes, provider profiles, **static panes** (`mode: static`) | Load when plugin enabled + manifest valid + Strike version OK (when `strike.min` present). No subprocess. |
| **Executable** | MCP stdio command, harnesses, shell hooks (`type: command`), **process panes** (`mode: process`) | **Blocked** until a trust record matches current source identity + content digest + capability set. |
| **Networked config** | MCP `type: streamable-http` or `sse` (legacy: `transport: http`) | Treated as executable-class for trust (may send headers/secrets to a URL) even without a local binary. |

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
- Manifest / extension field `digest`, when present, must equal the computed
  digest or install/load fails.
- Lockfile stores the digest that was reviewed.

`PLUGIN_DATA` is **outside** the package payload and MUST NOT be included in
the content digest.

### 5.3 Trust record binding

A trust grant is valid only when **all** match:

1. Plugin `name` (legacy: `id`)
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
  hook command, MCP URL/headers keys, process-pane command/args/env)
- APS `$schema` / legacy `schemaVersion` increase that the running Strike does
  not implement
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

Catalog and lockfile remain Strike-owned. Agent Plugins does not define
distribution.

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
| `registry` | Catalog base identity (URL) |
| `package` | Package slug in the catalog (matches plugin `name`, or legacy `id`) |
| `version` | Immutable published version |
| `url` | Artifact URL (`.tar.gz` or `.zip`) |
| `digest` | Expected **artifact** digest (`sha256:<hex>`) from catalog metadata |

Lockfile `digest` (entry-level) remains the **content-tree** digest after extract.
Both are recorded so an install is reproducible.

#### Remote catalog format (`schemaVersion: 1`)

Index URL: `<registry>/catalog.json` when `registry` is a base directory, or a
direct `.json` URL. Strict JSON; unknown fields rejected. Metadata **cannot**
enable plugins or grant trust. Catalog format is Strike-owned (not Agent
Plugins).

```json
{
  "schemaVersion": 1,
  "registry": "https://example.com/strike-plugins",
  "packages": [
    {
      "id": "acme.review-pack",
      "name": "Acme Review Pack",
      "description": "…",
      "versions": [
        {
          "version": "1.2.0",
          "url": "https://cdn.example.com/acme.review-pack-1.2.0.tar.gz",
          "digest": "sha256:…",
          "contentDigest": "sha256:…",
          "capabilities": ["agents", "skills", "mcp.stdio"],
          "strike": { "min": "0.2.0" },
          "manifestSchema": 1,
          "size": 12345
        }
      ]
    }
  ]
}
```

`packages[].id` is the install identity (APS `name` or legacy `id`).
`packages[].name` is a display string. `manifestSchema` is the **catalog
record** schema (still integer `1`); it is not the Agent Plugins `$schema`.
Clients detect APS vs legacy from the extracted `plugin.json`.

| Field | Rules |
|---|---|
| `digest` | **Required.** SHA-256 of artifact bytes; install fails on mismatch. |
| `contentDigest` | Optional. Must match `ComputeDigest` of extracted tree when set. |
| `signature` | Reserved; v1 verifies digests only. Presence does not grant trust. |
| `size` | Optional upper bound on artifact bytes (also globally capped). |

Transport: HTTP(S) only, bounded body sizes, limited redirects. Extraction
rejects absolute paths, `..`, symlinks, and oversized payloads (zip-slip / tar
traversal fail closed).

CLI: `strike plugin search`, `install catalog:pkg[@ver] --registry`, `outdated`,
`update --yes` (`internal/plugin` catalog client).

### 6.4 Update capability review

Before accepting an update (git pull to new commit, catalog newer version, or
local replace), the lifecycle UX MUST show:

- Old → new `version` and content digest
- Added/removed/changed contribution types
- Executable command/args/env/URL diffs (env/header **keys** only — never values)
- Capability tag diffs (`capabilities` and inferred executable kinds)

User confirmation (`--yes`) applies the update. Prior trust is **invalidated**
when digest, source identity, or executable contributions change (lockfile
`trust` cleared). The previous working version remains until the update commits
atomically; failed download, verification, validation, or activation rolls back
([#727](https://github.com/jonathanung/strike/issues/727),
[#729](https://github.com/jonathanung/strike/issues/729)).
No automatic unattended updates.

---

## 7. Contribution matrix

Portable types: fixed APS locations. Strike-only types: `com.strike.cli/`.
Legacy types: `contributions.*` (§3.10).

For each type: validation, naming, precedence, lifecycle, trust.

### 7.1 Agents (`com.strike.cli/agents`)

| Axis | Rule |
|---|---|
| **Discovery** | `com.strike.cli/agents/*.md`. Legacy: `contributions.agents[].path`. |
| **Validation** | Existing agent markdown loader rules for Strike-native trees (fail closed on invalid names/effort/permissions). Path must be `.md` under the plugin root. |
| **Naming** | Agent name from frontmatter/`filename`; uniqueness per §4.4. |
| **Precedence** | §4.1; same-name override with diagnostic on the loser. |
| **Lifecycle** | Loaded at session start when plugin enabled; unavailable when disabled. No process. |
| **Trust** | Passive. |

### 7.2 Skills (`skills/` portable + `com.strike.cli/skills` extras)

| Axis | Rule |
|---|---|
| **Discovery (native)** | Immediate `skills/<dir>/SKILL.md` only (Agent Skills spec). No recursive search. No flat `skills/name.md` in the portable tree. |
| **Discovery (Strike extra)** | `com.strike.cli/skills/*.md` (flat). |
| **Discovery (legacy)** | `contributions.skills[].path` — file `name.md` or `name/SKILL.md`. |
| **Validation** | Existing skill loader; markdown only — no JS/TS execution. Invalid portable skill skipped; plugin continues. |
| **Naming** | Skill invoke name from frontmatter or directory / file stem. |
| **Precedence** | §4.1; within one APS plugin, portable wins over Strike-flat extra (§4.4). |
| **Lifecycle** | Catalog at start; `/skill` and skill tool see enabled plugins only. |
| **Trust** | Passive. |

### 7.3 Workflows (`com.strike.cli/workflows`)

| Axis | Rule |
|---|---|
| **Discovery** | `com.strike.cli/workflows/*.json`. Legacy: `contributions.workflows[].path`. |
| **Validation** | Workflow schema v1 strict decode + structural validate ([agents-skills.md](agents-skills.md#workflows)). |
| **Naming** | Workflow `name` field; source provenance `plugin`. |
| **Precedence** | §4.1; project non-plugin and project plugin layers as specified. |
| **Lifecycle** | Listed in catalog; activation remains a separate user/tool step (scaffold never activates). |
| **Trust** | Passive. Phase permission widening still requires existing approval UX. |

### 7.4 Themes (`com.strike.cli/themes`)

| Axis | Rule |
|---|---|
| **Discovery** | `com.strike.cli/themes/*.json`. Legacy: `contributions.themes[].path`. |
| **Validation** | Existing theme JSON schema ([theme.md](theme.md)). |
| **Naming** | Theme `id` inside file. |
| **Precedence** | §4.1 (after bundled, before or with user theme dirs as loaders specify — plugin project > plugin global). |
| **Lifecycle** | Available to `/theme` when enabled. |
| **Trust** | Passive. Packaging tracked further in [#511](https://github.com/jonathanung/strike/issues/511). |

### 7.5 Provider profiles (`com.strike.cli/providers`)

| Axis | Rule |
|---|---|
| **Discovery** | `com.strike.cli/providers/*`. Legacy: `contributions.providers[]` `{path, profileName?}`. |
| **Validation** | Same shape as `providers.jsonc` custom/overlay entries ([config.md](config.md#custom-providers)). |
| **Naming** | `profileName` / object key / filename stem. |
| **Precedence** | §4.1; cannot override managed policy. |
| **Lifecycle** | Merged into provider catalog when enabled. |
| **Trust** | Passive **configuration only**. |
| **Hard limit** | Profiles may target **shipped wire adapters only** (openai-compatible, anthropic-compatible, and built-in ids). Fields like `npm` are hints only and are **never** loaded as code. No bundle may register new Go provider implementations, OAuth handlers, or streaming parsers. Credentials stay in env refs / auth store — never in plugin files. |

### 7.6 MCP (`mcp.json`)

| Axis | Rule |
|---|---|
| **Discovery (native)** | Root `mcp.json` only (Agent Plugins §7.2). Map `type: stdio` → stdio; `type: streamable-http` → Strike HTTP MCP; `type: sse` optional. |
| **Discovery (legacy)** | `contributions.mcp[]` `{name, transport, command?, args?, env?, url?, headers?}`. |
| **Validation** | APS closed server variants (unknown field / unknown type → skip that server). Path confinement on `command` / `cwd`. Env/header **values** must not be raw secrets — never print values in doctor/UI. Strike MAY resolve `secret://` / `{env:NAME}` at process start ([secrets.md](secrets.md)). |
| **Naming** | Server object key / `name` slug; tools `mcp_<name>_<tool>`. |
| **Precedence** | Name collision fail closed (§4.4). User `mcp.jsonc` entries are a separate source; document winner in loader diagnostics (recommended: explicit user config overrides plugin on same name). |
| **Lifecycle** | Registered only when plugin enabled **and** trusted (#728). Start with session like config MCP; disablement stops process and unregisters tools. Invalid MCP config disables MCP only. |
| **Trust** | **Executable** (stdio and HTTP/SSE). |
| **Env** | Set `PLUGIN_ROOT` and `PLUGIN_DATA` after overlaying configured `env`. Expand placeholders per §3.6. |

### 7.7 Harnesses (`com.strike.cli/harnesses`)

| Axis | Rule |
|---|---|
| **Discovery** | `com.strike.cli/harnesses/*.json` — `{name, command, args?, …}` aligned with [harnesses.md](harnesses.md). Legacy: `contributions.harnesses[]`. |
| **Validation** | Command path confinement; no embedded Go harness registration from plugins. |
| **Naming** | Harness registry name; agent frontmatter `harness: <name>`. |
| **Precedence** | Name collision fail closed. |
| **Lifecycle** | Available to `task` children when trusted; root turns never invoke harnesses (existing engine rule). |
| **Trust** | **Executable**. Still unsandboxed process isolation model as today — trust review is mandatory because of that. |

### 7.8 Hooks (`com.strike.cli/hooks`)

| Axis | Rule |
|---|---|
| **Discovery** | `com.strike.cli/hooks/*.json` — same fields as config `hooks[]` ([config.md](config.md), [peer-ecosystem.md](peer-ecosystem.md#hooks-alignment)). File is one object or an array. Legacy: `contributions.hooks[]`. |
| **Validation** | Existing hook schema; `command` path confinement when `type` is command/shell. |
| **Naming** | No global name; identity is plugin name + index/path for diagnostics. |
| **Precedence** | Concatenate like global-then-project hooks: after user global, plugin global, user project, plugin project (exact order fixed in #728 to match §4.1). |
| **Lifecycle** | Matched at runtime only if plugin enabled + trusted for command hooks. Declarative-only hooks follow passive load rules but still require plugin enablement. |
| **Trust** | Command/shell hooks: **executable**. Pure declarative hooks: passive. |

### 7.9 Panes (`com.strike.cli/panes`)

Normative pane **ABI**: **[plugin-panes.md](plugin-panes.md)** (PLUGIN.8 / #522).
This section is **packaging only** — do not contradict that document's
definition schema, render tree, or process protocol.

| Axis | Rule |
|---|---|
| **Discovery (native)** | `com.strike.cli/panes/*.json` (JSON/JSONC). Pane `id` and `abi` come from the definition (`schemaVersion` 1 ⇒ `pane/1`). |
| **Discovery (legacy)** | `contributions.panes[]` `{id, path, abi: "pane/1"}`. |
| **Validation** | Definition `schemaVersion` 1; `mode` is `static` or `process`; path confinement; permissions default-deny ([plugin-panes.md](plugin-panes.md#8-permissions)). Unknown definition fields rejected. |
| **Naming** | Pane `id` unique across enabled plugins; fail closed on collision. Built-in window ids are reserved and must not be claimed. |
| **Precedence** | §4.1 for discovery; pane id collision fail closed (like MCP/harness names). Layout group placement is host UX ([#731](https://github.com/jonathanung/strike/issues/731)). |
| **Lifecycle** | Descriptors register when the plugin is enabled (and trusted for process mode). Mount/focus starts static binding or process supervision; disable/remove tears down. Hosts: [#731](https://github.com/jonathanung/strike/issues/731) TUI, [#732](https://github.com/jonathanung/strike/issues/732) web. |
| **Trust** | `mode: static` with `fs/network/command: none` → **passive**. `mode: process` → **executable** (`panes.process` capability). Never exposes the private Go `window` interface or in-process TUI/Go plugins; render trees are bounded primitives only. |

---

## 8. Version compatibility

### 8.1 Agent Plugins `$schema`

| Running Strike \ Package | Behavior |
|---|---|
| Recognizes `plugin.json` `$schema` 1.0.0 | Load as APS. |
| `plugin.json` `$schema` for an APS version Strike does not implement (and has not explicitly mapped as compatible) | **Skip plugin** with diagnostic: unsupported Agent Plugins version. Do not partial-load. |
| `mcp.json` `$schema` version ≠ `plugin.json` `$schema` version | Disable MCP for that plugin; continue other components (spec §7.2.2 / §10.1). |
| `mcp.json` `$schema` unrecognized | Disable MCP for that plugin; continue other components. |
| Unknown APS top-level fields | Report and ignore (§3.4). |

Strike MUST NOT retrieve schemas over the network while loading. Forward
compatibility inside `extensions.com.strike.cli`: unknown keys reported and
ignored (§3.4).

### 8.2 Legacy `schemaVersion`

| Running Strike \ Manifest | Behavior |
|---|---|
| Implements legacy `schemaVersion` **N**, manifest `N` | Load as legacy + deprecation diagnostic. |
| Implements max **N**, manifest `M` where `M < N` | Load if Strike still supports `M` (v1 support remains until a future major deprecation). |
| Manifest `M` where `M >` Strike max | **Skip plugin** with explicit diagnostic: upgrade Strike. Do not partial-load. |
| Missing `schemaVersion` on a legacy-shaped file | Reject manifest. |
| Unknown fields at legacy version N | Reject (strict). |

**Forward compatibility for catalogs:** registries MAY list multiple
artifacts per plugin version for different APS / legacy schemas; clients pick
the highest schema they implement.

**Backward compatibility promise:** legacy schemaVersion 1 remains readable by
all Strike releases that advertise plugin support until a future major release
announces removal in CHANGELOG **Upgrade note**. APS 1.0.0 remains the native
authoring format from this contract forward.

---

## 9. Path confinement (normative)

1. Reject contribution paths that are absolute or contain `..` segments after
   cleaning. APS plugin-relative paths MUST begin with `./` (spec §4.1).
   Strike-only relative paths MAY omit `./` but MUST still resolve inside the
   plugin root.
2. After `filepath.EvalSymlinks` (or equivalent), the resolved path MUST have
   the plugin root as prefix (except MCP `cwd` under `${PLUGIN_DATA}/`, which
   MUST have the plugin data directory as prefix).
3. `command` fields: if relative, resolve under plugin root; if absolute, allow
   only when the trust review UI showed the absolute path and the path is not
   rewritten by the plugin after trust (digest covers scripts; absolute system
   binaries are part of the reviewed entry text). APS MCP `command` MUST be a
   single `./` path or bare name (no absolute APS `command`).
4. Archive extraction (catalog installs) MUST guard against zip-slip / tar
   traversal ([#729](https://github.com/jonathanung/strike/issues/729)).
5. Plugin roots themselves MUST live under `~/.strike/plugins` or
   `./.strike/plugins` (or test overrides via env used only in tests).
6. `PLUGIN_DATA` directories MUST live under `~/.strike/plugin-data` or
   `./.strike/plugin-data` (or test overrides). They are not package payload.

---

## 10. Secret handling (normative)

1. **No secrets in bundles** — keys, tokens, PEMs, `.env` files with
   credentials. APS MCP `env` / `headers` are visible package data (spec §7.2.1
   / §9.2); plugins MUST NOT embed credentials there.
2. MCP `env` / `headers` and Strike-only executable env SHOULD use
   `secret://env/NAME` or `{env:NAME}` forms ([secrets.md](secrets.md)) when
   Strike-specific secret indirection is needed; resolve only at process start,
   never into model-visible output or doctor text.
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
| Strike-native contract | #725 | Historical authoring; now §3.10 legacy load |
| APS contract | #1142 (this doc) | Native APS + `com.strike.cli` + legacy deprecation |
| Passive load | #726 (`internal/plugin` + config/theme wiring) | §3–4, §7.1–7.5, §8–10 |
| Local/Git CLI | #727 | §2, §6.1–6.2, enablement, doctor |
| Executable activation | #728 (`trust`/`untrust`, `CompileExecutables`, assemble wiring) | §5, §7.6–7.8 |
| Catalog / updates | #729 (`internal/plugin` catalog/archive + CLI) | §6.3–6.4, digest verify |
| TUI manager | #730 (`/plugin` modal + `host.Plugins`) | UX over enablement + trust |
| Themes packaging | #511 (`theme.Catalog` + `/theme` preview) | §7.4 |
| Pane ABI | #522 | §7.9 + [plugin-panes.md](plugin-panes.md) |
| Pane host / web | #731 #732 | implement [plugin-panes.md](plugin-panes.md); no TUI type leakage to web |
| APS portable load | #1143 | skills/`SKILL.md` + `mcp.json` |
| APS Strike-only load | #1144 (`com.strike.cli/`) | §3.4, §3.8, §7.1–7.5, §7.7–7.9 |
| Legacy → APS migrate | #1145 (`strike plugin migrate`) | §2.3, §3.10 |

---

## 12. Non-goals (restated)

- Automatic unattended updates (catalog update requires explicit `--yes`).
- Paid marketplace infrastructure. Agent Plugins distribution/marketplace
  (Strike catalog stays).
- A generic arbitrary-code plugin ABI or in-process extension mechanism.
- Hot reload of plugin trees.
- Replacing stock `mcp.jsonc` / config hooks / harnesses (plugins are additive
  packages with stronger trust for executables).
- Inventing a second portable component type beyond skills and MCP.
- Changing the pane ABI (definition schema, render tree, process protocol)
  beyond how panes are packaged.
- Loader/CLI implementation or refusing new legacy
  installs (APS.2–APS.3, APS.6). `strike plugin migrate` is #1145.

## 13. Acceptance mapping (#1142)

| AC | Section |
|---|---|
| Agent Plugins 1.0.0 is the native package format; spec linked | Intro, §3 |
| `com.strike.cli` namespace (manifest + directory) for every current contribution type | §3.8, §7 |
| Portable vs Strike-only discovery; no inline portable MCP/skills in `plugin.json` | §2, §3.6–3.8, §7.2, §7.6 |
| Legacy Strike manifest deprecated with load/diagnostic/removal-later rules | §3.10, §8.2 |
| Trust, path confinement, secrets, non-goals from #725 preserved | §5, §9, §10, §12, §5.5 |
| Forward/backward APS `$schema` and unknown Strike extension fields | §3.4, §8 |

## 14. Acceptance mapping (#725, still applicable)

| AC | Section |
|---|---|
| Trust binds to source + content digest; invalidation on relevant changes | §5 |
| Path confinement + secret-handling explicit | §9, §10 |
| Every contribution type: validation, naming, precedence, lifecycle, trust | §7 |

## 15. Acceptance mapping (#726)

| AC | Implementation |
|---|---|
| Valid fixtures appear on agent/skill/workflow/theme/provider surfaces | `internal/plugin.Discover` + `config` loaders + `theme.Catalog` |
| Unsupported versions, duplicates, traversal, malformed, collisions → diagnostics | `Diagnostic` codes: `schema_version`, `strike_version`, `duplicate_id`, `path`, `malformed`, `collision`, `digest` (APS loaders add `aps_schema` / `legacy_deprecated`) |
| Malformed plugin cannot silently shadow | Skip plugin; other sources unchanged |
| Disabled plugins contribute nothing | `plugins.lock.json` `enabled: false` |
| No arbitrary provider/auth/streaming code | Provider profiles via `ParseProvidersFile` / shipped `WireAPI` only; secret literals rejected |
| Tests: precedence, namespacing, disablement, path confinement | `internal/plugin/*_test.go`, `internal/config/plugins_test.go`, theme catalog tests |

## 16. Acceptance mapping (#727)

| AC | Implementation |
|---|---|
| Install atomic; failed validation leaves nothing enabled | `plugin.Install` stages under plugins root, validates, then rename + lockfile under flock; rollback on lock write failure |
| Git installs pinned (no silent mutable branch follow) | Lockfile `source.commit` full SHA; `ref` stored only as resolve hint |
| Disable preserves files; remove confirms + updates lockfile | `Disable` / `Remove` (`--yes`) |
| Doctor exact paths; no secrets or env values | `plugin.Doctor` + `FormatDoctorText`; env/header **keys** only; `pkg/redact` on URLs |
| Project/global scopes explicit; no root escape | `--scope`; `Roots.ConfinePath` |
| Safe under concurrent lockfile writes | `WithLockfileLock` (flock) + atomic rename |
| CLI | `strike plugin list\|inspect\|install\|enable\|disable\|remove\|doctor` |

## 17. Acceptance mapping (#729)

| AC | Implementation |
|---|---|
| Remote installs pin immutable version + verified digest | `SourceIdentity` catalog fields + artifact `sha256` check before extract; lockfile pins version/URL/digests |
| Catalog metadata cannot silently enable or execute | No trust from catalog; install sets `enabled` for passive only; `trust` never set from metadata; unknown catalog fields rejected |
| Updates changing executable content invalidate prior trust | `BuildUpdateReview` + lockfile `trust` cleared on digest/source/executable change |
| Failed download/verify/validate/activation preserves prior | Stage under plugins root; validate before rename; backup restore on failure |
| Lockfile provenance enough to reproduce | `registry`, `package`, `version`, artifact `url`/`digest`, content `digest` |
| Network + archive paths bounded; traversal tested | `downloadBytes` caps; `sanitizeArchivePath` / zip-slip tests in `catalog_test.go` |
| Search / install / outdated / update CLI | `strike plugin search\|install catalog:…\|outdated\|update` |

## 18. Acceptance mapping (#1144)

| AC | Implementation |
|---|---|
| APS `com.strike.cli/` agents/workflows/themes/providers/extra skills load with plugin provenance | `Discover` + `config` loaders + `theme.Catalog` (`com.strike.cli/themes`) |
| Extension harness/hook/process-pane stay inactive without trust | `CompileExecutables` + `HasProcessPanes` + `host/local` panes |
| APS plugin with no `com.strike.cli/` dir remains valid | missing dir is not an error |
| Unknown `extensions.com.strike.cli` keys reported and ignored | `parseStrikeCLIExtension`; directory still loads |
| Invalid type for a known `com.strike.cli` key skips Strike-only files only | `StrikeCLIExtension.SkipContributions` |
| Other reverse-domain dirs and other `extensions.*` namespaces ignored | directory discovery is `com.strike.cli/` only |
| Path confinement: extension dir cannot escape plugin root | `resolveStrikeCLIDir` / `ResolveUnderRoot` |
| Legacy packages keep `contributions` | APS branch only; legacy shim unchanged |

