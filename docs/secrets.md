# Secrets: detection, redaction, and refs

Strike keeps credentials out of session logs, exports, model-facing tool
results, and diagnostic bundles. The shared implementation lives in
`internal/secret` and is used by:

| Path | What happens |
|---|---|
| **Session JSONL** (`internal/session`) | Every `Append` runs `secret.RedactEvent` before encode |
| **Engine tool results** | `ScrubToolOutput` on settle + streaming tool/process tails |
| **TUI `/export`** | Markdown transcript runs `secret.Redact` on text/args |
| **Context doctor previews** | Engine layer previews use `secret.Redact` |
| **MCP status errors** | `secret.RedactError` collapses token-bearing messages |
| **Timeline / trace export (#790)** | Import `internal/secret` (`Redact`, `RedactEvent`, `ScrubToolOutput`) |

Auth material itself stays in `~/.strike/auth.json` (0600) and process env;
see [auth.md](auth.md). This document covers **egress** scrubbing and
**secret refs**, not the credential store.

## What is redacted

Best-effort patterns (prefer false negatives over mangling ordinary prose):

- Provider API key shapes: `sk-…`, `sk-ant-…`, `xai-…`
- GitHub tokens: `ghp_` / `gho_` / `ghu_` / `ghs_` / `ghr_` / `github_pat_…`
- Slack-style `xox[baprs]-…`, AWS access key ids `AKIA…`
- PEM private key blocks
- `Bearer <token>` headers
- Assignments: `api_key=`, `password=`, `OPENAI_API_KEY=`, …
- JSON credential fields: `apiKey`, `access`, `refresh`, `idToken`, …
- **Tool results only:** long high-entropy tokens (mixed letters+digits, ≥40
  chars) → `[REDACTED_HIGH_ENTROPY]`

Placeholders look like `[REDACTED]`, `[REDACTED_API_KEY]`,
`[REDACTED_GITHUB_TOKEN]`, etc.

## What is preserved (for debugging)

- Structural event fields: call IDs, session/turn IDs, tool names, stop
  reasons, exit codes, permission decisions
- File paths and ordinary prose (unless they embed a matching token shape)
- Short hex/ids (commit SHAs, UUIDs under the high-entropy threshold)
- Secret **refs** themselves (`secret://env/NAME`) — the wire form is not a
  secret; only the resolved value is

Redaction is not a substitute for filesystem permissions on
`~/.strike/auth.json` or for avoiding pasting live keys into chat.

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

// Inject into a subprocess without showing values to the model:
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
host process and are not written into session events. Secret refs are the
explicit “never embed resolved value on egress” form for tools/harness.

### Non-goals (v1)

- Enterprise secret managers / MDM (#764)
- Encrypting session JSONL at rest
- Guaranteeing zero false negatives on novel token formats

## Bypass resistance

Tier C tests cover nested tool output (tool result wrapping a bearer token,
JSON args with `apiKey` fields, session append of fake credentials). New
egress paths should call `secret.Redact` / `RedactEvent` / `ScrubToolOutput`
rather than copying regexes.

## Related

- #796 — this feature
- #790 — structured timeline / trace export (consumes the same package)
- [auth.md](auth.md) — credential store and login
- [usage.md](usage.md) — `/export` redacts common API-key shapes
