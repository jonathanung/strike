# Usage and audit (web)

`GET /v1/usage` sums `usage.reported` events from session JSONL. Cache read
and cache write tokens are their own columns. A dollar amount is shown only
when the models.dev catalog has a cost block for that provider and model.
If the catalog is missing, or the model is not in it, the page shows the
token counts and says the cost is not priced.

Cache rates are included only when the catalog publishes a non-zero
`cache_read` or `cache_write`. A zero cache field is indistinguishable from
an omitted one, so those tokens are left out of the dollar figure and the
row is marked partial. They are not billed at the input rate.

`PUT /v1/usage/budget` with `{"dailyUsd": number}` stores a ceiling.
`0` clears it. The page raises a 50/80/100 alert only when today's priced
total is complete. An incomplete price does not count as "within budget".

## Hash chain

`~/.strike/usage-audit/chain.jsonl` is append-only. Each line's `hash` is
the SHA-256 of the redacted payload plus the previous line's hash.
`tip.json` repeats the last hash. `GET /v1/audit/verify` fails when a line
was edited without rebuilding the chain, or when a tail line was removed
and the tip was left behind. `GET /v1/audit/export` downloads the redacted
bundle (`"redacted": true`).

Strings pass through `pkg/redact` before they are hashed. The chain commits
to the scrubbed text.

This is separate from the retention-bounded sink in `internal/trust/audit`.
Pruning that sink would break a hash chain, so this log is not pruned.

### What is recorded

| Action | Source |
|---|---|
| `approval` | `permission.decided` when the user replied, including a delegated child |
| `permission` | `permission.decided` without a reply, `permission.mode_selected`, workspace permission mode |
| `shell` | `tool.begin` for bash/shell, including a delegated child, and `process.started` that is not already a tool call |
| `terminal` | pane input (field count only, not keystrokes). A PTY terminal (#1279) should `POST /v1/audit` |
| `config` | settings, sandbox, workspace rules, custom providers, daily budget |
| `token` | provider API key stored or cleared (never the key), `strike daemon logout`, and a daemon token file change observed by `strike serve` |

### POST /v1/audit

Other web streams append actions that are not already protocol events:

```json
{"action":"terminal","sessionId":"…","summary":"…","detail":{}}
```

`action` is one of `approval`, `permission`, `shell`, `terminal`, `config`,
`token`. Do not POST events the table above already records, or the chain
will contain two lines for one action. Summary and detail are redacted.

## Browser check

`node scripts/usage-audit-uat.mjs` boots `strike serve --provider echo` with
a temporary `HOME`. It writes `usage.reported` envelopes for
`anthropic/claude-haiku-4-5-20251001` (cache rates published), an echo child
(models.dev prices echo input/output and publishes no cache rate, so the row
is partial), and `local/offline` (not in the catalog, tokens only). The
models.dev cache is `STRIKE_MODELS_CACHE` or `/tmp/models.json`. Screenshots
and `usage-audit.mp4` go to `usage-audit-uat/`.
