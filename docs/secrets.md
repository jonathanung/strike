# Secrets: detection, redaction, and refs

Strike keeps credentials out of session logs, exports, model-facing tool
results, and diagnostic bundles.

| Package | Role |
|---|---|
| **`pkg/redact`** | Shared string scrubbing (`String`, `ScrubToolOutput`, `JSON`, `Error`, `Bytes`) — used by timeline export (#790), diagnostic bundles (#792), TUI `/export`, engine inspect previews |
| **`internal/trust/secret`** | Secret-ref env indirection + `RedactEvent` for session JSONL; thin wrappers over `pkg/redact` |

Auth material itself stays in `~/.strike/auth.json` (0600) and process env;
see [auth.md](auth.md).

## Egress paths

| Path | What happens |
|---|---|
| **Session JSONL** | `Append` runs `secret.RedactEvent` before encode |
| **Engine tool results** | `secret.ScrubToolOutput` on settle + streaming tool/process tails |
| **Timeline export** (`/timeline export`) | `pkg/redact.String` on previews (#790) |
| **Diagnostic bundle** (`/diag`) | `pkg/diag` + `pkg/redact` on layer previews, paths, and dial strings (#792); digests only — never full secret-bearing config files |
| **TUI `/export`** | `pkg/redact.String` on markdown bodies |
| **Context doctor previews** | Engine layer previews use `pkg/redact` |
| **MCP status errors** | `secret.RedactError` → `pkg/redact.Error` |

## What is redacted

Best-effort patterns (prefer false negatives over mangling ordinary prose):

- Provider API key shapes: `sk-…`, `sk-ant-…`, `xai-…`
- GitHub tokens: `ghp_` / `gho_` / … / `github_pat_…`
- Slack-style `xox[baprs]-…`, AWS access key ids `AKIA…`
- PEM private key blocks
- `Bearer <token>` headers
- Assignments: `api_key=`, `password=`, `OPENAI_API_KEY=`, `TOKEN=`, …
- JSON credential fields: `apiKey`, `access`, `refresh`, `idToken`, …
- **Tool results only (`ScrubToolOutput`):** long high-entropy tokens (mixed
  letters+digits outside pure hex, ≥40 chars) → `[REDACTED_HIGH_ENTROPY]`

## What is preserved (for debugging)

- Structural event fields: call IDs, session/turn IDs, tool names, stop
  reasons, exit codes, permission decisions
- File paths and ordinary prose (unless they embed a matching token shape)
- Pure hex digests (git SHAs) and short ids
- Secret **refs** themselves (`secret://env/NAME`) — the wire form is not a
  secret; only the resolved value is

## Secret refs (v1: env-key indirection)

Tools and harness code may hold a **reference** instead of a literal secret.
Resolve only at process exec or provider HTTP construction — never expand
into model-visible tool output, `apply_patch` hunks, session JSONL, or test
fixtures.

Wire forms (equivalent):

```text
secret://env/VAR_NAME
{secret:env:VAR_NAME}
```

Go API (`internal/trust/secret`):

```go
ref, ok := secret.ParseRef("secret://env/OPENAI_API_KEY")
val, err := secret.Resolve(ref) // os.LookupEnv; fail closed if unset/empty

env, err := secret.MergeEnv(nil /* os.Environ */, map[string]secret.Ref{
    "OPENAI_API_KEY": ref,
})
// pass env to os/exec.Cmd.Env — do not log env
```

**Bash without model-visible values:** the bash tool receives a **minimal**
environment (not the full Strike process env — #1030). Put the secret in the
strike process environment (shell profile, CI secret, `direnv`, etc.) and
declare a config `bashSecrets` entry so it is injected by ref:

```jsonc
{
  "bashSecrets": {
    "GITHUB_TOKEN": "secret://env/GITHUB_TOKEN"
  }
}
```

Prefer refs + `MergeEnv` / `bashSecrets` when a tool must set a *named*
variable for a subprocess without embedding the value in tool args or results.

Config provider options still use `{env:NAME}` / `$NAME` expansion (see
[config.md](config.md)); those expand at provider select time inside the
host process and are not written into session events.

### Non-goals (v1)

- Enterprise secret managers (vault/KMS integration) — managed **config**
  policy is separate (#764 / [config.md](config.md#managed--mdm-config-enterprise))
- Encrypting session JSONL at rest
- Guaranteeing zero false negatives on novel token formats

## Write-time content guards (#890)

Egress redaction does **not** stop agents from writing credential-shaped
material to the workspace. Write guards scan **proposed file content** on
`write` / `edit` / `apply_patch` / `notebook_edit` **before** disk commit.

| | Egress redaction (`pkg/redact`) | Write guard (`contentGuard`) |
|---|---|---|
| When | After content exists (logs, timeline, tool results, exports) | Before mutators write |
| Action | Replace with `[REDACTED…]` placeholders | `allow` / `ask` / `deny` |
| Error code | n/a | `content_guard_denied` (stable; visible on tool results / timeline) |
| Patterns | Shared credential library (`redact.Findings`) | Same credentials + high-confidence dangerous sinks (language-limited) |

**Default posture:** credential shapes (PEM private keys, `AKIA…`, provider
API key forms, GitHub/Slack tokens, labeled secrets) → **deny**. High-confidence
dangerous sinks (`eval`/`exec`/`os.system`/`subprocess(…shell=True)` /
JS `eval`/`new Function`/`child_process.exec`) → **ask**.

Config (`contentGuard` — see [config.md](config.md)):

```jsonc
{
  "contentGuard": {
    "mode": "default",              // off | default | ask | deny
    "pathAllow": ["**/testdata/**"]  // skip globs (false-positive escape)
  }
}
```

- **ask** mode upgrades all findings to interactive `content_guard` permission
  asks (allow-once or session path always-grant; audited via
  `permission.decided`).
- **deny** mode hard-blocks all findings.
- **off** disables the scanner unless managed locks deny.
- Managed/MDM `contentGuard.mode: deny` sets a **ForcedDeny** ceiling: project
  config (including `pathAllow`), yolo, and session grants cannot widen it.
- `permissionMode: yolo` may upgrade remaining **ask** findings only; it never
  bypasses deny-severity hits or the managed ceiling.

Optional skill `/write-guards` documents the same rules for the model.

**Non-goals:** full SAST product; blocking all security bugs; high-entropy
token heuristics on write (those stay egress-only via `ScrubToolOutput`).

## Related

- #796 — secret refs + session/engine scrub wiring
- #890 — write-time content guards on edit/write/apply_patch
- #790 — structured timeline / trace export (`pkg/redact` + `pkg/timeline`)
- #792 — prompt/config diagnostic bundle (`pkg/diag` + `/diag`)
- [auth.md](auth.md) — credential store and login
