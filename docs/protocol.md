# Protocol wire schema (`pkg/protocol`)

Public Op/Event wire schema between the engine and frontends (TUI, web cockpit,
`strike serve` SSE, `strike rpc`, `pkg/sdk`). Session transcripts are JSONL
logs of **event envelopes**.

```go
import "github.com/jonathanung/strike-cli/pkg/protocol"
```

Wire schema version: `protocol.Version` (semver on every envelope as `"v"`).
Legacy lines without `"v"` are treated as `LegacyVersion` (`1.0.0`).

In-tree code may still import `internal/protocol` (type aliases). Prefer
`pkg/protocol` for new code. Consumer helpers: [sdk.md](sdk.md).

## Envelope shape

```json
{"type":"turn.started","time":"2026-08-06T12:00:00Z","v":"1.10.0","data":{…}}
```

| Field | Role |
|---|---|
| `type` | Stable type string (e.g. `verification.started`) |
| `time` | UTC timestamp |
| `v` | Schema version written by `Wrap` |
| `data` | Event payload JSON object |

Ops use the same envelope idea via `OpEnvelope` (`WrapOp` / `Decode`).

## Compatibility policy

| Change | Version bump | Consumer impact |
|---|---|---|
| New optional JSON field on an existing event/op | **minor** | Old readers ignore extras (`encoding/json`); writers use `omitempty` |
| New event **type string** | **minor** | Older Go builds decode as `UnknownEvent` (not an error). Web/SSE pass the envelope through and ignore unknown `type`s |
| New op type string | **minor** | Unknown ops still **fail** `OpEnvelope.Decode` (fail closed on client input) |
| Rename/remove field, change meaning, or change a type string | **major** | Requires CHANGELOG **Upgrade note** and migration guidance |
| Docs/helpers only | **patch** | No encoded JSON change |

**Rules of thumb**

- Additive fields are always OK.
- Renames require a major bump — do not silently repurpose a JSON key.
- Do not break existing ops without a migration note in CHANGELOG.
- Full third-party stable API freeze is a **non-goal** for v1; this document
  is the consumer contract for in-tree and thin external clients.

### Forward-compat decode (`UnknownEvent`)

`Envelope.Decode` maps unrecognized event `type` strings to:

```go
protocol.UnknownEvent{Type: "…", Data: json.RawMessage(`{…}`)}
```

- Session replay, TUI resume, timeline export, and `pkg/sdk` must not treat
  unknown types as corrupt logs.
- Type switches should ignore `UnknownEvent` (default branch). Helper:
  `protocol.IsUnknown(ev)`.
- `Wrap(UnknownEvent{…})` re-emits the original type string and raw data so
  fork/export round-trips preserve extension events.
- Malformed JSON and **empty** type strings still error.
- Ops remain strict: unknown op types return an error.

Web cockpit (`web/src/reducer.ts`) already keys on `env.type` and no-ops
unknown types — no Go decode step on the SSE path.

## Harness-related events

These events are the harness / verification / trust surface. Introduced on the
1.x line; current writers stamp `protocol.Version`. Consumers should tolerate
additive fields on all of them.

| Type string | Go type | Role |
|---|---|---|
| `harness.progress` | `HarnessProgress` | Custom function-harness intermediate progress (`name` + JSON `payload`). Subprocess ABI: [harnesses.md](harnesses.md) |
| `verification.started` | `VerificationStarted` | Independent completion gates beginning (`scope`: `turn` \| `child`, `gateCount`) |
| `verification.completed` | `VerificationCompleted` | Gate outcome (`Report`: claimed vs verified, checks, env) |
| `turn.completed` | `TurnCompleted` | Optional `verification` report when solo/harness gates ran |
| `child.completed` | `ChildCompleted` | Optional `verification` when child spawn configured gates |
| `permission.decided` | `PermissionDecided` | Permission audit (effective action + matched rule layer) |
| `permission.asked` / `permission.resolved` | `PermissionAsked` / `PermissionResolved` | Interactive ask lifecycle |
| `hook.matched` | `HookMatched` | Lifecycle hook fired (event/action/matcher/tool) |
| `diagnostic.bundle` | `DiagnosticBundle` | Prompt/config diagnostic export payload |
| `context.fit_warning` | `ContextFitWarning` | Context budget warn/critical |
| `context.controls` | `ContextControlsSelected` | Exclude/pin context kinds |
| `scheduler.queued` / `admitted` / `canceled` | `Scheduler*` | Admission pool wait/admit/cancel |
| `tool.loop_detected` / `tool.retrying` | `ToolLoopDetected` / `ToolRetrying` | Tool reliability signals |
| `provider.retrying` | `ProviderRetrying` | Provider backoff |

Derived views (not separate session event types):

- **Timeline export** — `pkg/timeline` folds protocol events into a versioned
  trace document (`schemaVersion` independent of wire `Version`).
- **Telemetry families** — `pkg/telemetry` + `schemas/telemetry/v1` define
  versioned security/harness export records (redaction annotations; not wire).
- **Run recordings / snapshots** — `internal/replay` (eval / multi-agent).

Golden forward-compat coverage lives in `pkg/protocol` tests
(`TestGoldenAdditiveFieldsHarnessEvents`,
`TestGoldenUnknownHarnessExtensionTypes`,
`TestDecodeUnknownEventForwardCompat`).

## Extension points (plugins / hooks / harnesses)

Strike does **not** yet ship a generic third-party plugin process ABI. Extension
paths that can contribute observability or gates:

| Contribution | How it reaches the wire today | Trust |
|---|---|---|
| **External function harness** | Config `harnesses` → subprocess JSONL → engine emits `harness.progress` (and normal turn/tool events). See [harnesses.md](harnesses.md) | Process isolation; project config is trusted like other local commands |
| **Lifecycle hooks** | Config `hooks[]` → `hook.matched` (+ allow/block side effects). See [config.md](config.md), [peer-ecosystem.md](peer-ecosystem.md) | Shell hooks are executable; treat project hooks like local scripts |
| **MCP tools** | Config MCP servers → tools on the registry; normal `tool.*` events | Stdio MCP runs local commands |
| **Verification gates** | Engine/harness options → `verification.*` + optional `TurnCompleted.verification` | Harness-owned; model self-report is never evidence |
| **Plugin bundles** | Versioned manifest + contribution matrix; passive load vs trusted executable activation | Normative contract: [plugins.md](plugins.md) ([#725](https://github.com/jonathanung/strike/issues/725)); passive load of agents/skills/workflows/themes/providers ([#726](https://github.com/jonathanung/strike/issues/726)); trusted executable activation [#728](https://github.com/jonathanung/strike/issues/728) |
| **Plugin panes** | Declarative view trees (`static`) or supervised JSONL subprocess (`process`); bounded primitives; no private Go `window` ABI | Normative: [plugin-panes.md](plugin-panes.md) ([#522](https://github.com/jonathanung/strike/issues/522)); TUI host [#731](https://github.com/jonathanung/strike/issues/731); web [#732](https://github.com/jonathanung/strike/issues/732) |

**Trusted executable contributions (#728):** plugin MCP startup, harness
commands, and shell hooks run only when a lockfile trust record matches the
current source identity + content digest + capability set (`strike plugin trust`;
see [plugins.md](plugins.md#5-trust-model)). Trust invalidates on content or
source change. Passive contributions load when enabled
([#726](https://github.com/jonathanung/strike/issues/726)). Disablement stops
new activation on the next launch and tears down managed MCP via process
shutdown.

**Contributing new spans or gates**

1. Prefer **additive optional fields** on an existing event when the meaning
   fits (e.g. extra keys inside `HarnessProgress.Payload`).
2. New cross-cutting lifecycle needs a **new type string** + minor Version bump
   + entry in this table + `eventType`/`Decode` cases + `TestEventTypeCoverage`.
3. Do not require old consumers to understand the new type — they will see
   `UnknownEvent` / ignore in web reducers.
4. Redact secrets on the persist path (`internal/secret.RedactEvent` /
   `pkg/redact`); unknown payloads are JSON-scrubbed.

## Related docs

- [ARCHITECTURE.md](ARCHITECTURE.md) — package map and dataflow
- [sdk.md](sdk.md) — Go client over this schema
- [harnesses.md](harnesses.md) — function harness subprocess ABI
- [web.md](web.md) — cockpit SSE/WebSocket envelopes
- [config.md](config.md) — hooks, harnesses, MCP, session durability
- [plugins.md](plugins.md) — versioned plugin bundle contract (manifest, trust)
- [plugin-panes.md](plugin-panes.md) — user pane contribution ABI (static + process)
- [peer-ecosystem.md](peer-ecosystem.md) — hooks alignment with peers
