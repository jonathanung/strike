---
description: write clean files without credential shapes or high-risk eval/exec sinks
---
# Write guards (content scanner)

$ARGUMENTS

Strike scans **proposed file content** on `write`, `edit`, `apply_patch`, and
`notebook_edit` **before** it reaches disk. This is separate from egress
redaction (`pkg/redact` on logs/timeline/tool results).

## Default posture

| Kind | Examples | Default action |
|---|---|---|
| **Credential** | PEM private keys, `AKIA…` AWS keys, `sk-…` / `sk-ant-…` / `xai-…`, `ghp_…`, Slack `xox…`, labeled `api_key=` / `password=` | **deny** → error code `content_guard_denied` |
| **Dangerous sink** (v1, high-confidence) | Python `eval(`/`exec(`/`os.system(`/`subprocess.*(shell=True`; JS `eval(`/`new Function(`/`child_process.exec` | **ask** (yolo may auto-allow; explicit deny rules and managed deny ceiling still hold) |

## How to write cleanly the first time

1. **Never embed live secrets** in source, fixtures under normal paths, `.env`
   bodies, or docs. Use env vars, secret refs (`secret://env/NAME`), or CI
   secret stores. Prefer placeholders like `YOUR_API_KEY` / `sk-ant-…REDACTED`.
2. **Tests that need key-shaped strings:** put them under a path covered by
   `contentGuard.pathAllow` (e.g. `**/testdata/**`), or use clearly fake
   non-matching shapes when possible.
3. **Avoid introducing raw `eval` / `exec` / `os.system` / `shell=True`** unless
   the user explicitly requires it; prefer safe APIs. If required, expect an
   ask (or configure mode).
4. On `content_guard_denied`, **do not retry the same content**. Remove the
   matching material (rule id is in the error), use a secret ref, or ask the
   human to allow a path.

## Config (`contentGuard`)

```jsonc
{
  "contentGuard": {
    "mode": "default",           // off | default | ask | deny
    "pathAllow": ["**/testdata/**"]
  }
}
```

- **default** — credentials deny; sinks ask
- **ask** — all findings ask (allow-once / session path always-grant via
  permission `content_guard`)
- **deny** — all findings hard-deny
- **off** — scanner disabled (managed `mode: deny` still forces deny ceiling)

Managed/MDM `contentGuard.mode: deny` cannot be widened by project config,
session always-grants, or yolo.

## Related

- docs/secrets.md — egress redaction vs write guards
- docs/config.md — `contentGuard` field
- Issue #890
