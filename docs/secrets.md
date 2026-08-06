# Secrets: detection, redaction, and refs

Strike keeps credentials out of session logs, exports, model-facing tool
results, and diagnostic bundles.

| Package | Role |
|---|---|
| **`pkg/redact`** | Shared string scrubbing (`String`, `ScrubToolOutput`, `JSON`, `Error`, `Bytes`) — used by timeline export (#790), diagnostic bundles (#792), TUI `/export`, engine inspect previews |
| **`internal/secret`** | Secret-ref env indirection + `RedactEvent` for session JSONL; thin wrappers over `pkg/redact` |

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

Go API (`internal/secret`):

```go
ref, ok := secret.ParseRef("secret://env/OPENAI_API_KEY")
val, err := secret.Resolve(ref) // os.LookupEnv; fail closed if unset/empty

env, err := secret.MergeEnv(nil /* os.Environ */, map[string]secret.Ref{
    "OPENAI_API_KEY": ref,
})
// pass env to os/exec.Cmd.Env — do not log env
```

**Bash without model-visible values:** put the secret in the strike process
environment (shell profile, CI secret, `direnv`, etc.). Child `bash` tool
invocations inherit that environment. Prefer refs + `MergeEnv` when a tool
must set a *named* variable for a subprocess without embedding the value in
tool args or results.

Config provider options still use `{env:NAME}` / `$NAME` expansion (see
[config.md](config.md)); those expand at provider select time inside the
host process and are not written into session events.

### Non-goals (v1)

- Enterprise secret managers / MDM (#764)
- Encrypting session JSONL at rest
- Guaranteeing zero false negatives on novel token formats

## Related

- #796 — secret refs + session/engine scrub wiring
- #790 — structured timeline / trace export (`pkg/redact` + `pkg/timeline`)
- #792 — prompt/config diagnostic bundle (`pkg/diag` + `/diag`)
- [auth.md](auth.md) — credential store and login
