# Admission scan (MCP, skills, plugins)

Register/load-time **admission** scans capability surfaces before they bind
into the tool registry or skill catalog. This is a trust-boundary gate
([#889](https://github.com/jonathanung/strike/issues/889)), distinct from:

| Dial | Controls |
|---|---|
| **admission** (this doc) | Whether MCP tools / skills / plugin contributions may bind at all |
| **permissionPreset** / `permissions[]` | Per-call allow/ask/deny once a tool is bound |
| **sandbox** | OS isolation for bash |
| **plugin trust** | Explicit grant for executable plugin contributions |

Admission does **not** replace OS sandbox or permission rules. It does **not**
depend on any external gateway.

## Config

```jsonc
{
  "admission": {
    "preset": "default",           // permissive | default | strict
    "allowPaths": ["~/trusted"], // home-anchored only
    // "failClosed": true         // optional override (strict defaults true)
  }
}
```

| Field | Meaning |
|---|---|
| `preset` | Severity → action matrix (below). Empty = `default`. |
| `allowPaths` | Path prefixes treated as first-party for spoof checks. **Must** be `~/…` or absolute under `$HOME`. Bare relative markers (e.g. `.strike/skills`) are **rejected at load** — they match spoofable nested paths like `evil/.strike/skills`. |
| `failClosed` | When true, scanner/internal errors become **block**. Defaults **true** for `strict`, **false** otherwise. |

Managed/MDM config may set `admission` like other scalars (last-wins merge;
`allowPaths` replaces when the layer sets the array, including `[]`).

## Severity → action matrix

| Severity | `permissive` | `default` | `strict` |
|---|---|---|---|
| info | allow | allow | allow |
| low | allow | allow | warn |
| medium | allow | warn | quarantine |
| high | warn | quarantine | **block** |
| critical | quarantine | **block** | **block** |

| Action | Effect |
|---|---|
| **allow** | Bind normally |
| **warn** | Bind + operator message (`stderr`) + `admission.decided` audit |
| **quarantine** | Do **not** bind tools/skills; MCP server may stay connected for diagnostics (`/mcp` shows `quarantined`) |
| **block** | Do not bind; MCP client closed; skill omitted from catalog |

## Scanners (v1, metadata + static)

### MCP (after `tools/list`, before registry bind)

| Rule id | Severity | Signal |
|---|---|---|
| `mcp.shell_tool` | critical | Name/description resembles shell/exec |
| `mcp.network_tool` | high | Name/description resembles network/egress |
| `mcp.broad_fs_tool` | high (medium if path required) | Over-broad filesystem tool shape |
| `mcp.credential_default` | critical | Schema default/const looks like a credential |
| `mcp.remote_http` | medium | Non-localhost HTTP transport |

Under **`strict`**, a server presenting high-risk tool shapes is **blocked**
before tools bind.

### Skills (at load)

| Rule id | Severity | Signal |
|---|---|---|
| `skill.path_spoof` | high | Path nests a first-party marker (`.strike/skills`, …) outside real first-party roots / allow-list |
| `skill.credential_content` | critical | Template contains credential-shaped material |
| `skill.suspicious_instruction` | high | High-risk instruction patterns |

Built-in shipping skills always **allow** (not re-scanned for content).

### Plugins (at discover)

| Rule id | Severity | Signal |
|---|---|---|
| `plugin.path_spoof` | high | Root nests a first-party marker outside install roots |
| `plugin.mcp_capability` / `plugin.exec_capability` | low | Declared executable capability tags |

Executable **trust** (`strike plugin trust`) remains a separate fail-closed
gate for starting MCP/harness/shell hooks from plugins.

## Operator visibility

- **stderr** one-liners: `admission mcp evil → block (…)`
- **`/mcp`**: state `quarantined` / error text; `admission=` when not allow
- **Session JSONL / timeline**: `admission.decided` events (`kind: admission` on export) with action, reason, preset, rule ids

## Fail-open vs fail-closed

| Preset | Scanner error |
|---|---|
| `strict` | **block** (fail-closed) |
| `default` / `permissive` | **warn** (fail-open) unless `failClosed: true` |

## Shared finding types

`internal/security.Finding` + `Severity` are shared with write-time content
guards ([#890](https://github.com/jonathanung/strike/issues/890)). Admission
actions (`allow|warn|block|quarantine`) stay in `internal/admission`; content
guards use `allow|ask|deny`.

## Related

- [config.md](config.md) — full config reference
- [plugins.md](plugins.md) — plugin trust and executable contributions
- [isolation.md](isolation.md) — sandbox / permission two-dial model
- [secrets.md](secrets.md) — credential redaction (egress), not admission
