# Security and harness telemetry schema

Versioned **export/observability** families for timeline export, the durable
audit sink (#893), and eval tooling. This is **not** the Op/Event wire schema
(`pkg/protocol`); wire compatibility is unchanged.

| | |
|---|---|
| Registry | [`schemas/telemetry/v1/registry.json`](../schemas/telemetry/v1/registry.json) |
| Go types / builders | [`pkg/telemetry`](../pkg/telemetry) |
| Schema version | `1.0.0` (`telemetry.SchemaVersion`) |
| Drift gate | `make telemetry-check` (also under `go test ./pkg/telemetry`) |

## Core families (v1)

| Family | Role |
|---|---|
| `tool` | Tool call spans (previews + stable error codes) |
| `permission` | Permission decisions (layer + redacted patterns) |
| `sandbox` | OS sandbox posture / capability blocks |
| `usage` | Token usage samples |
| `error` | Harness/engine errors with stable codes |
| `egress` | Outbound network/tool egress decisions |
| `admission` | Scheduler admission pool wait/admit/cancel |

No external OpenTelemetry collector is required in v1.

### Optional OTLP export

Set `STRIKE_OTLP_ENDPOINT` (e.g. `http://localhost:4318/v1/traces`) to POST
redacted envelopes as a minimal OTLP/HTTP JSON `resourceSpans` payload.
Optional `STRIKE_OTLP_HEADERS` is a comma-separated `k=v` list. Export is
best-effort and never required for normal operation (`pkg/telemetry.OTLP`).


## Redaction annotations

Each field declares a redaction policy in the registry and on the Go struct
tag `telemetry:"redact=…"`:

| Policy | Behavior |
|---|---|
| `none` | Persist as-is (ids, enums, counts, codes) |
| `scrub` | `pkg/redact.String` (and string slices element-wise) |
| `hash` | SHA-256 hex fingerprint instead of raw value |
| `class` | Coarse label only (already non-secret) |
| `omit` | Drop at the boundary (never persist) |

`telemetry.RedactRecord` and `telemetry.NewEnvelope` apply these policies
before export. Full conversational transcripts stay on session JSONL — these
families are compact trust-boundary records.

## Adding a field

1. Edit **both** copies of the registry (keep them identical):
   - `schemas/telemetry/v1/registry.json` (source of truth for docs/review)
   - `pkg/telemetry/registry.json` (embedded via `go:embed`)
2. Add the field to the matching Go struct in `pkg/telemetry/events.go` with:
   - `json:"camelCase"` matching the registry `name`
   - `telemetry:"redact=<policy>"` matching the registry `redact`
   - Go type matching registry `type` (`string`, `string[]`, `bool`, `int`, `int64`)
3. Extend golden fixtures in `pkg/telemetry/testdata/golden_envelopes.jsonl` if
   the field is part of the canonical example.
4. Run:

```sh
make telemetry-check
go test ./pkg/telemetry/ -count=1
```

Additive optional fields (`omitempty`) do not require a `schemaVersion` bump.
Renames, removals, or meaning changes **do** — bump `schemaVersion` in the
registry and `telemetry.SchemaVersion`, refresh fixtures, and note the change
in `CHANGELOG.md`.

## Adding a family

1. Add a family object to the registry (both JSON copies).
2. Add a Go struct + `goTypes()` entry + `NewEnvelope` switch arm.
3. Append a golden envelope line.
4. Document the family in this file’s table.
5. Run `make telemetry-check`.

Unknown families must be ignored by consumers (forward-compat). Prefer
extending an existing family when the event is a variant of one above.

## Relationship to other surfaces

| Surface | Role |
|---|---|
| `pkg/protocol` | Live Op/Event wire (sessions, TUI, RPC) |
| `pkg/timeline` | Derived run trace from protocol events |
| `pkg/telemetry` | Versioned family catalog + redacted export records |
| `internal/audit` | Durable retention-bounded security audit sink (#893) |
| `pkg/diag` | Prompt/config diagnostic bundle (not turn spans) |

## CI

`make telemetry-check` loads the embedded registry, asserts core families,
checks Go struct drift, and verifies the disk registry file matches the
embedded copy when run from a full checkout.
