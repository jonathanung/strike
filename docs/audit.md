# Security audit sink

Compact, retention-bounded log of **trust-boundary decisions** under
`~/.strike/audit/`. Complements session JSONL (full transcript) and timeline
export (run spans) — it does **not** store conversational payloads by default.

| | |
|---|---|
| Package | `internal/audit` |
| Schema | `1.0.0` |
| CLI | `strike audit export`, `strike audit prune` |
| Config | `session.auditRetentionMaxEvents`, `session.auditRetentionMaxAgeDays` |

## Event families

| Family | Source (v1) |
|---|---|
| `permission` | `permission.decided` |
| `sandbox` | `tool.end` with `errorCode=sandbox_denied` |
| `admission` | `scheduler.queued` / `admitted` / `canceled` |
| `egress` | direct `Record` (tooling hooks) |
| `secret_ref_use` | direct `Record` (class/hash only) |
| `content_guard` | direct `Record` |
| `toolchain_match` | direct `Record` |

Payloads are redacted via `pkg/telemetry` / `pkg/redact` before append.

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
