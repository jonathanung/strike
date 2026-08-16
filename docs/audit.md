# Security audit sink

Compact, retention-bounded log of **trust-boundary decisions** under
`~/.strike/audit/`. Complements session JSONL (full transcript) and timeline
export (run spans) — it does **not** store conversational payloads by default.

| | |
|---|---|
| Package | `internal/trust/audit` |
| Schema | `1.0.0` |
| CLI | `strike audit export`, `strike audit prune` |
| Config | `session.auditRetentionMaxEvents`, `session.auditRetentionMaxAgeDays` |

## Event families

Every family below has a production emitter and an Observe→Record path (#1032).

| Family | Production source |
|---|---|
| `permission` | `permission.decided` |
| `toolchain_match` | `permission.decided` with `chainRule` set (tool-chain correlation #891) |
| `sandbox` | `tool.end` with `errorCode=sandbox_denied` |
| `egress` | `tool.end` with `errorCode=network_denied` |
| `content_guard` | `tool.end` with `errorCode=content_guard_denied`, or `permission.decided` for permission `content_guard` |
| `admission` | `scheduler.queued` / `admitted` / `canceled`, and `admission.decided` |
| `hook` | `hook.matched` (shell_* and declarative), and `tool.end` with `errorCode=blocked` |
| `secret_ref_use` | `Sink.RecordSecretRefUse` at bash secret inject (class + name hash only — never values) |
| `serve_op` | `strike serve` `POST /v1/ops` and WebSocket op frames (op type + source IP + channel + outcome; never op bodies) |

Payloads are redacted via `pkg/telemetry` / `pkg/redact` before append.
Raw credentials, sensitive tool bodies, and secret values never enter the log.

## Storage and privacy

- Segmented append-only JSONL (`*.jsonl`), mode `0600`, directory `0700`
- Correlation: `sessionId`, `turnId`, `toolCallId`, optional `chainId`
- Default retention: **10 000** events or **90** days (whichever prunes more),
  applied on sink close and `strike audit prune`
- Export: `strike audit export -o bundle.json` → versioned JSON with
  `"redacted": true` and an explicit note that it is not a transcript

## Config

```jsonc
{
  "session": {
    "auditRetentionMaxEvents": 10000,
    "auditRetentionMaxAgeDays": 90
  }
}
```

Zero on both axes keeps the package defaults above. Setting either axis
overrides that axis only.

## Related

- Timeline: `pkg/timeline` / `/timeline`
- Telemetry families: `docs/telemetry.md`
- Secrets scrubbing: `docs/secrets.md`
