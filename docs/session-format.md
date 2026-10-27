# Session log format

Normative on-disk contract for Strike session logs. External ingesters
(including [Peasant](https://github.com/peasant-labs/peasant)) should use this
document instead of guessing from examples. The Op/Event version policy for
the same payloads on the engine wire is [protocol.md](protocol.md).

A golden fixture
(`internal/persist/session/testdata/session_format_golden.json`) and
`TestSessionFormatGolden` fail when a JSON field or event type in this
contract is renamed, removed, or added without an update. Fixtures are
synthetic: no real paths, credentials, or transcripts.

## Files

| Path | Role |
|---|---|
| `~/.strike/sessions/<id>.jsonl` | Append-only event log |
| `~/.strike/sessions/<id>.meta.json` | Sidecar metadata |

`session.DefaultDir` resolves symlinks on `~/.strike` and, when the home
directory cannot be determined, falls back to `os.TempDir()/strike/sessions`.

`<id>` is a single path segment. `session.NewID` mints a UTC timestamp prefix
plus a random suffix so lexical order matches creation order. The filename
stem is the session id. The sidecar `sessionId` repeats it when Strike has
written the file.

## JSONL log

UTF-8. One JSON object per line, each line ending in `\n`. `Store.Append`
writes a full line and fsyncs it. Replay skips a trailing torn line (crash
residue) and fails closed on interior corruption.

### Envelope

Event lines use `protocol.Envelope`:

```json
{"type":"turn.completed","time":"2026-01-01T00:00:05Z","v":"1.18.0","data":{"stopReason":"end_turn"}}
```

| Field | Role |
|---|---|
| `type` | Event type string from `pkg/protocol/codec.go` |
| `time` | UTC timestamp |
| `v` | Wire schema version written by `protocol.Wrap` (`protocol.Version`). Omitted on legacy lines; readers treat empty `v` as `1.0.0` (`LegacyVersion`) |
| `data` | JSON object for that event |

`v` is part of Strike's contract. Some external readers ignore it. Do not drop
it when copying logs.

Unknown `type` strings are not corrupt. `Envelope.Decode` returns
`protocol.UnknownEvent` and keeps the raw `data` object so replay can skip
them (`docs/protocol.md`). An empty `type`, or `data` that is not valid JSON,
is an error. A second `session.header` after line 1 is corrupt.

### `session.header`

Current writers put a schema marker on line 1 of a new log. It is not a
protocol event and is not produced by `protocol.Wrap`. Replay and
`pkg/sdk` skip it. Older logs may start with an event line and have no header;
readers treat those as schema version 1.

```json
{"type":"session.header","schemaVersion":1,"time":"2026-01-01T00:00:00Z"}
```

| Field | Role |
|---|---|
| `type` | Always `session.header` |
| `schemaVersion` | `session.LogSchemaVersion` (currently 1) |
| `time` | UTC time the log was created |

`Open` and `Replay` reject a `schemaVersion` newer than the reader's
`LogSchemaVersion` with an upgrade error. Bump `LogSchemaVersion` only for a breaking log
layout change, and update this document and the golden fixture in the same
change.

### Redaction at write time

`Store.Append` runs `secret.RedactEvent` before `protocol.Wrap`. That scrubs
credential-shaped spans in string payloads (and JSON-scrubs unknown event
`data`) via `pkg/redact`. Structural fields such as ids, tool names, and stop
reasons are preserved. See [secrets.md](secrets.md).

This is Strike's write-time scrub. It is not a publication policy, and it is
not another tool's redaction category list. Ingesters that publish transcripts
apply their own rules to the bytes they copy. Do not copy those rules into
Strike.

## Sidecar `Meta`

`<id>.meta.json` is indented JSON written by `session.WriteMeta`. A missing
file is a zero `Meta` and not an error. A malformed file is an error.
`encoding/json` ignores unknown keys, and missing keys stay zero, so a sidecar
written by a newer or older Strike still loads.

`projectKey` is the first key when it is set. New keys below are optional.

| Field | JSON | Written when |
|---|---|---|
| Project key | `projectKey` | Session create. Launch workspace identity (the same key as history and memory). Often a filesystem path on a real machine. Empty on legacy sidecars |
| Session id | `sessionId` | Every `WriteMeta`. Set to the filename stem, replacing a stale value. Empty until the next write on older files. Readers should still prefer the filename if the two disagree |
| Last model | `model` | `Manager.Append` of `model.selected` when `model` is non-empty and the event's `sessionId` is empty or equals this session. Memory updates only after the sidecar write succeeds. `Fork` / `ForkAt` copy the last non-empty `model.selected` in the kept prefix, not the source sidecar. `Import` copies that selection from the package log, or the package sidecar `model` when the log has none. A later selection replaces it. An empty model does not clear it. Provider is not stored here |
| Title | `title` | Create, rename, or `session.titled` |
| Parent | `parentSessionId` | Child session create. Empty on roots and on forks |
| Team lead | `leadSessionId` | Child create. Roots are their own lead and leave this empty. Nested children store the root lead, not the immediate parent |
| Fork source | `forkedFrom` | `Manager.Fork` / `ForkAt`. Forks stay roots (`parentSessionId` empty) |
| Worktree | `worktreePath`, `worktreeBranch` | `Manager.SetWorktree`. Empty when the session uses the launch working directory. `worktreePath` is absolute on real machines |
| Created | `createdAt` | Create, RFC3339 UTC |
| Pull request | `prUrl`, `prNumber`, `prState`, `prUpdatedAt` | Shipping side-effects. `prState` is `open`, `merged`, or `closed` |

The event log remains the history. `model` is a convenience copy of the last
selection so a reader can see it without scanning the JSONL. `session.meta`
events mirror PR fields into the log; the sidecar is updated from those events
by the host.

Fields Strike does not write, even if an ingester accepts them as aliases:
`cwd`, `remote`, `branch`, `version`, `parentId`, `projectName`, and nested
`project` or `git` objects. `worktreePath` / `worktreeBranch` / `projectKey`
are the stored forms. Wire schema version stays on each envelope as `v`.

## Compatibility

| Change | What to do |
|---|---|
| New optional sidecar or event field | Allowed. Use `omitempty`. Old readers ignore it. Update this document and the golden fixture |
| New event `type` | Minor protocol bump (see [protocol.md](protocol.md)). Old decoders keep it as `UnknownEvent`. Add it to `eventType` and `Decode`, this document, and the golden fixture |
| Rename or remove a JSON field or event type | Breaking for ingesters. Protocol fields also need a major `protocol.Version` bump and a changelog upgrade note. The golden test fails until the fixture and this document move together |
| Sidecar missing `sessionId` or `model` | Still valid. Read the zero value |

## Ingester notes

Checked against Peasant `internal/ingest/strike.go` on `develop` (adapter
present since the first public release). Peasant discovers `*.jsonl` under
`~/.strike/sessions`, uses the filename as the session id, and reads the
sibling `.meta.json` without failing the session when the sidecar is missing
or bad.

Peasant's envelope struct reads `type`, `time`, and `data` (not `v`). Its
closed kind list handles `session.titled`, `user.message`, `turn.started`,
`turn.completed`, `text.delta`, `reasoning.delta`, `tool.begin`, `tool.output`,
`tool.end`, `process.started`, `process.output`, `process.exited`,
`usage.reported`, and `model.selected`. It also lists names Strike does not
emit: `session.started`, `assistant.text`, `assistant.text.delta`,
`assistant.message.delta`, `assistant.reasoning`, `assistant.reasoning.delta`,
and `assistant.thinking.delta`.

Other Strike events, including `session.header`, stay outside that list.
Peasant keeps unknown kinds as opaque retained evidence; classifying them is
Peasant work, not a Strike wire change.

Peasant already accepts sidecar `sessionId` and `model`. Those keys do not
need a new Strike field name. It does not read `leadSessionId`, `forkedFrom`,
or the `pr*` keys. Cache counts on `usage.reported` (`cacheRead`,
`cacheCreation`, `used`, `source`) are already on the wire; mapping them into
Peasant's canonical model is a Peasant change. If that canonical model has no
slot for them, the schema repository has to grow the slot before Peasant reads
it. This document does not add those slots.

<!-- session-format:catalog:begin -->
## Normative field list

This section is generated from `pkg/protocol/codec.go` and the session writer. `TestSessionFormatGolden` fails when a JSON field or event type is renamed, removed, or added until this section and `internal/persist/session/testdata/session_format_golden.json` are updated together.

`time` is RFC3339 UTC. `raw` is a JSON value. `number` is a JSON number (any integer width). `omitempty` marks a key that is absent at the zero value. Embedded `Correlation` is flattened onto each event. A shadowed name means an outer field uses that key, so `encoding/json` does not also emit the embedded field.

### Envelope

`protocol.Envelope`. Empty `v` means legacy schema `1.0.0`.

| Field | Type | omitempty |
|---|---|---|
| `type` | string |  |
| `time` | time |  |
| `v` | string | yes |
| `data` | raw |  |

### `session.header`

Not an event. First line of logs created by current writers. `schemaVersion` is `1`.

| Field | Type | omitempty |
|---|---|---|
| `type` | string |  |
| `schemaVersion` | number |  |
| `time` | time |  |

### Sidecar `Meta`

`<id>.meta.json`. Missing keys stay zero. `projectKey` is the first key when it is set.

| Field | Type | omitempty |
|---|---|---|
| `projectKey` | string | yes |
| `sessionId` | string | yes |
| `model` | string | yes |
| `title` | string | yes |
| `parentSessionId` | string | yes |
| `leadSessionId` | string | yes |
| `forkedFrom` | string | yes |
| `workspaceId` | string | yes |
| `worktreePath` | string | yes |
| `worktreeBranch` | string | yes |
| `createdAt` | string | yes |
| `prUrl` | string | yes |
| `prNumber` | number | yes |
| `prState` | string | yes |
| `prUpdatedAt` | string | yes |
| `memoryMode` | string | yes |

### Events

Each table is the object inside envelope `data`.

#### `user.message`

Go `UserMessage`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `text` | string |  |
| `images` | array of object | yes |
| `images[].mime` | string |  |
| `images[].data` | string | yes |
| `images[].ref` | string | yes |
| `images[].sha256` | string | yes |
| `images[].bytes` | number | yes |
| `images[].kind` | string | yes |
| `images[].name` | string | yes |

#### `session.titled`

Go `SessionTitled`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `title` | string |  |

#### `turn.started`

Go `TurnStarted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |

#### `text.delta`

Go `TextDelta`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `text` | string |  |

#### `reasoning.delta`

Go `ReasoningDelta`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `text` | string |  |

#### `tool.begin`

Go `ToolCallBegin`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `callId` | string |  |
| `name` | string |  |
| `args` | raw |  |

#### `tool.end`

Go `ToolCallEnd`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `callId` | string |  |
| `title` | string |  |
| `output` | string |  |
| `isError` | bool | yes |
| `errorCode` | string | yes |
| `metadata` | raw | yes |

#### `tool.output`

Go `ToolCallOutput`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `callId` | string |  |
| `data` | string |  |

#### `process.started`

Go `ProcessStarted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `processId` | string |  |
| `callId` | string | yes |
| `argv` | array of string |  |
| `cwd` | string | yes |

#### `process.output`

Go `ProcessOutput`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `processId` | string |  |
| `stream` | string |  |
| `data` | string |  |

#### `process.exited`

Go `ProcessExited`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `processId` | string |  |
| `exitCode` | number |  |
| `status` | string |  |

#### `permission.asked`

Go `PermissionAsked`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |
| `permission` | string |  |
| `patterns` | array of string |  |
| `always` | array of string | yes |
| `metadata` | raw | yes |

#### `permission.resolved`

Go `PermissionResolved`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |
| `decision` | string |  |

#### `permission.decided`

Go `PermissionDecided`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string | yes |
| `permission` | string |  |
| `patterns` | array of string | yes |
| `action` | string |  |
| `decision` | string | yes |
| `layer` | string | yes |
| `rulePermission` | string | yes |
| `rulePattern` | string | yes |
| `ruleAction` | string | yes |
| `evalPath` | string | yes |
| `factSummary` | string | yes |
| `chainId` | string | yes |
| `chainRule` | string | yes |
| `chainSummary` | string | yes |

#### `admission.decided`

Go `AdmissionDecided`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `surface` | string |  |
| `target` | string |  |
| `action` | string |  |
| `reason` | string | yes |
| `preset` | string | yes |
| `findings` | array of string | yes |

#### `question.asked`

Go `QuestionAsked`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |
| `questions` | array of object |  |
| `questions[].id` | string |  |
| `questions[].header` | string | yes |
| `questions[].question` | string |  |
| `questions[].options` | array of object | yes |
| `questions[].options[].label` | string |  |
| `questions[].options[].description` | string | yes |

#### `question.resolved`

Go `QuestionResolved`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |

#### `turn.steered`

Go `TurnSteered`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `text` | string | yes |
| `mode` | string |  |
| `targetTurnId` | string | yes |

#### `turn.completed`

Go `TurnCompleted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `stopReason` | string | yes |
| `files` | array of object | yes |
| `files[].path` | string |  |
| `files[].kind` | string |  |
| `checkpointSkipped` | number | yes |
| `uncovered` | array of string | yes |
| `verification` | object | yes |
| `verification.passed` | bool |  |
| `verification.claimed` | bool |  |
| `verification.verified` | bool |  |
| `verification.checks` | array of object |  |
| `verification.checks[].name` | string | yes |
| `verification.checks[].kind` | string |  |
| `verification.checks[].value` | string | yes |
| `verification.checks[].passed` | bool |  |
| `verification.checks[].exitCode` | number | yes |
| `verification.checks[].output` | string | yes |
| `verification.checks[].error` | string | yes |
| `verification.checks[].durationMs` | number | yes |
| `verification.env` | object |  |
| `verification.env.workDir` | string | yes |
| `verification.env.sessionId` | string | yes |
| `verification.env.worktreeId` | string | yes |
| `verification.env.modelId` | string | yes |
| `verification.env.startedAt` | string | yes |
| `verification.env.finishedAt` | string | yes |
| `verification.summary` | string | yes |
| `verification.durationMs` | number | yes |

#### `verification.started`

Go `VerificationStarted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `scope` | string | yes |
| `gateCount` | number | yes |

#### `verification.completed`

Go `VerificationCompleted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `scope` | string | yes |
| `report` | object |  |
| `report.passed` | bool |  |
| `report.claimed` | bool |  |
| `report.verified` | bool |  |
| `report.checks` | array of object |  |
| `report.checks[].name` | string | yes |
| `report.checks[].kind` | string |  |
| `report.checks[].value` | string | yes |
| `report.checks[].passed` | bool |  |
| `report.checks[].exitCode` | number | yes |
| `report.checks[].output` | string | yes |
| `report.checks[].error` | string | yes |
| `report.checks[].durationMs` | number | yes |
| `report.env` | object |  |
| `report.env.workDir` | string | yes |
| `report.env.sessionId` | string | yes |
| `report.env.worktreeId` | string | yes |
| `report.env.modelId` | string | yes |
| `report.env.startedAt` | string | yes |
| `report.env.finishedAt` | string | yes |
| `report.summary` | string | yes |
| `report.durationMs` | number | yes |

#### `harness.progress`

Go `HarnessProgress`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `name` | string |  |
| `payload` | raw |  |

#### `model.selected`

Go `ModelSelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `provider` | string |  |
| `model` | string |  |

#### `agent.selected`

Go `AgentSelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `name` | string |  |

#### `phase.changed`

Go `PhaseChanged`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `workflow` | string | yes |
| `phase` | string | yes |
| `index` | number | yes |
| `gate` | string | yes |
| `source` | string | yes |
| `fingerprint` | string | yes |
| `status` | string | yes |

#### `plan.handoff`

Go `PlanHandoff`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `planId` | string | yes |
| `planVersion` | number | yes |
| `approvalSource` | string |  |
| `title` | string | yes |
| `agent` | string | yes |
| `legacyText` | string | yes |

#### `artifact.updated`

Go `ArtifactUpdated`.

Shadowed (outer field wins): `sessionId`.

| Field | Type | omitempty |
|---|---|---|
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `id` | string |  |
| `type` | string |  |
| `version` | number |  |
| `scope` | string | yes |
| `title` | string | yes |
| `op` | string |  |
| `sessionId` | string | yes |

#### `ledger.updated`

Go `LedgerUpdated`.

Shadowed (outer field wins): `sessionId`.

| Field | Type | omitempty |
|---|---|---|
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `id` | string |  |
| `kind` | string |  |
| `status` | string |  |
| `op` | string |  |
| `statement` | string | yes |
| `reason` | string | yes |
| `supersedes` | string | yes |
| `supersededBy` | string | yes |
| `authorSession` | string | yes |
| `sessionId` | string | yes |

#### `phase.grant_approved`

Go `PhaseGrantApproved`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `workflow` | string |  |
| `phase` | string |  |
| `index` | number |  |
| `fingerprint` | string |  |
| `grants` | array of object |  |
| `grants[].permission` | string |  |
| `grants[].pattern` | string |  |
| `grants[].action` | string |  |
| `auto` | bool | yes |

#### `effort.selected`

Go `EffortSelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `level` | string |  |

#### `autonomy.selected`

Go `AutonomySelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `mode` | string |  |

#### `permission.mode`

Go `PermissionModeSelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `mode` | string |  |

#### `fast.selected`

Go `FastSelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `enabled` | bool |  |

#### `files.invalidated`

Go `FilesInvalidated`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `paths` | array of string |  |
| `reason` | string | yes |

#### `path.overlap`

Go `PathOverlap`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `path` | string |  |
| `policy` | string |  |
| `blocked` | bool | yes |
| `holders` | array of object | yes |
| `holders[].sessionId` | string |  |
| `holders[].name` | string | yes |
| `holders[].source` | string | yes |
| `holders[].mode` | string | yes |
| `warning` | string | yes |

#### `engine.error`

Go `EngineError`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `message` | string |  |
| `code` | string | yes |

#### `child.started`

Go `ChildStarted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `agent` | string | yes |
| `prompt` | string | yes |
| `name` | string | yes |
| `routeReason` | string | yes |
| `policyReason` | string | yes |
| `contextBundle` | object | yes |
| `contextBundle.goal` | string | yes |
| `contextBundle.acceptance` | array of string | yes |
| `contextBundle.allowedPaths` | array of string | yes |
| `contextBundle.requiredPaths` | array of string | yes |
| `contextBundle.artifacts` | array of object | yes |
| `contextBundle.artifacts[].id` | string |  |
| `contextBundle.artifacts[].version` | number | yes |
| `contextBundle.artifacts[].type` | string | yes |
| `contextBundle.constraints` | array of string | yes |
| `contextBundle.items` | array of object | yes |
| `contextBundle.items[].id` | string |  |
| `contextBundle.items[].kind` | string | yes |
| `contextBundle.items[].title` | string | yes |
| `contextBundle.items[].text` | string | yes |
| `contextBundle.items[].path` | string | yes |
| `contextBundle.items[].artifact` | object | yes |
| `contextBundle.items[].artifact.id` | string |  |
| `contextBundle.items[].artifact.version` | number | yes |
| `contextBundle.items[].artifact.type` | string | yes |
| `contextBundle.items[].hash` | string | yes |
| `contextBundle.filePins` | array of object | yes |
| `contextBundle.filePins[].path` | string |  |
| `contextBundle.filePins[].hash` | string | yes |
| `contextBundle.filePins[].text` | string | yes |
| `provider` | string | yes |
| `model` | string | yes |
| `isolation` | string | yes |
| `worktreePath` | string | yes |
| `baseRevision` | string | yes |

#### `child.completed`

Go `ChildCompleted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `status` | string |  |
| `summary` | string | yes |
| `name` | string | yes |
| `handoff` | object |  |
| `handoff.summary` | string |  |
| `handoff.filesChanged` | array of string |  |
| `handoff.verification` | string | yes |
| `handoff.findings` | array of string | yes |
| `handoff.blockers` | array of string | yes |
| `handoff.recommendedNextAction` | string | yes |
| `handoff.artifactRefs` | array of object | yes |
| `handoff.artifactRefs[].id` | string |  |
| `handoff.artifactRefs[].version` | number | yes |
| `handoff.artifactRefs[].type` | string | yes |
| `handoff.isolation` | string | yes |
| `handoff.patch` | string | yes |
| `handoff.baseRevision` | string | yes |
| `handoff.worktreePath` | string | yes |
| `handoff.missingContext` | array of object | yes |
| `handoff.missingContext[].kind` | string |  |
| `handoff.missingContext[].path` | string | yes |
| `handoff.missingContext[].question` | string | yes |
| `handoff.missingContext[].artifactId` | string | yes |
| `handoff.missingContext[].itemId` | string | yes |
| `handoff.missingContext[].detail` | string | yes |
| `handoff.provenance` | array of string | yes |
| `handoff.incomplete` | bool | yes |
| `handoff.quality` | string | yes |
| `handoff.sectionTitle` | string | yes |
| `handoff.sectionBody` | string | yes |
| `handoff.sectionBodySet` | bool | yes |
| `delegationId` | string | yes |
| `verification` | object | yes |
| `verification.passed` | bool |  |
| `verification.claimed` | bool |  |
| `verification.verified` | bool |  |
| `verification.checks` | array of object |  |
| `verification.checks[].name` | string | yes |
| `verification.checks[].kind` | string |  |
| `verification.checks[].value` | string | yes |
| `verification.checks[].passed` | bool |  |
| `verification.checks[].exitCode` | number | yes |
| `verification.checks[].output` | string | yes |
| `verification.checks[].error` | string | yes |
| `verification.checks[].durationMs` | number | yes |
| `verification.env` | object |  |
| `verification.env.workDir` | string | yes |
| `verification.env.sessionId` | string | yes |
| `verification.env.worktreeId` | string | yes |
| `verification.env.modelId` | string | yes |
| `verification.env.startedAt` | string | yes |
| `verification.env.finishedAt` | string | yes |
| `verification.summary` | string | yes |
| `verification.durationMs` | number | yes |
| `budgetKind` | string | yes |
| `finalization` | string | yes |

#### `child.escalated`

Go `ChildEscalated`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `name` | string | yes |
| `kind` | string |  |
| `reason` | string |  |
| `action` | string |  |
| `terminalStatus` | string | yes |
| `budget` | object | yes |
| `budget.maxWallClockS` | number | yes |
| `budget.maxTokens` | number | yes |
| `budget.maxCostUsd` | float | yes |
| `budget.maxToolCalls` | number | yes |
| `budget.maxDangerousTools` | number | yes |
| `budget.stallAfterS` | number | yes |
| `budget.loopDetectN` | number | yes |
| `budget.elapsedS` | number | yes |
| `budget.tokensUsed` | number | yes |
| `budget.costUsdUsed` | float | yes |
| `budget.toolCalls` | number | yes |
| `budget.dangerousTools` | number | yes |
| `budget.wallClockRemainingS` | number | yes |
| `budget.tokensRemaining` | number | yes |
| `budget.toolCallsRemaining` | number | yes |
| `budget.dangerousRemaining` | number | yes |
| `budget.costUsdRemaining` | float | yes |
| `budget.stall` | bool | yes |
| `budget.idleS` | number | yes |
| `budget.lastProgressAt` | string | yes |
| `budget.stallAfterSEffective` | number | yes |
| `budget.loop` | bool | yes |
| `budget.escalated` | bool | yes |
| `budget.escalateKind` | string | yes |
| `budget.escalateReason` | string | yes |

#### `delegation.changed`

Go `DelegationChanged`.

Shadowed (outer field wins): `sessionId`.

| Field | Type | omitempty |
|---|---|---|
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `id` | string |  |
| `state` | string |  |
| `prev` | string | yes |
| `version` | number |  |
| `sessionId` | string | yes |
| `name` | string | yes |
| `reason` | string | yes |
| `ownerSessionId` | string | yes |

#### `wait.started`

Go `WaitStarted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `waitId` | string |  |
| `events` | array of string |  |
| `targetSessionId` | string | yes |
| `timeoutMs` | number | yes |

#### `wait.resolved`

Go `WaitResolved`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `waitId` | string |  |
| `outcome` | string |  |
| `event` | string | yes |
| `targetSessionId` | string | yes |
| `targetName` | string | yes |
| `status` | string | yes |
| `summary` | string | yes |
| `handoff` | object | yes |
| `handoff.summary` | string |  |
| `handoff.filesChanged` | array of string |  |
| `handoff.verification` | string | yes |
| `handoff.findings` | array of string | yes |
| `handoff.blockers` | array of string | yes |
| `handoff.recommendedNextAction` | string | yes |
| `handoff.artifactRefs` | array of object | yes |
| `handoff.artifactRefs[].id` | string |  |
| `handoff.artifactRefs[].version` | number | yes |
| `handoff.artifactRefs[].type` | string | yes |
| `handoff.isolation` | string | yes |
| `handoff.patch` | string | yes |
| `handoff.baseRevision` | string | yes |
| `handoff.worktreePath` | string | yes |
| `handoff.missingContext` | array of object | yes |
| `handoff.missingContext[].kind` | string |  |
| `handoff.missingContext[].path` | string | yes |
| `handoff.missingContext[].question` | string | yes |
| `handoff.missingContext[].artifactId` | string | yes |
| `handoff.missingContext[].itemId` | string | yes |
| `handoff.missingContext[].detail` | string | yes |
| `handoff.provenance` | array of string | yes |
| `handoff.incomplete` | bool | yes |
| `handoff.quality` | string | yes |
| `handoff.sectionTitle` | string | yes |
| `handoff.sectionBody` | string | yes |
| `handoff.sectionBodySet` | bool | yes |
| `hasHandoff` | bool | yes |

#### `agent.message`

Go `AgentMessage`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `from` | string |  |
| `to` | string |  |
| `body` | string |  |
| `summary` | string | yes |
| `teamId` | string | yes |
| `messageId` | string | yes |
| `taskId` | string | yes |
| `urgency` | string | yes |
| `kind` | string | yes |
| `requireAck` | bool | yes |
| `inReplyTo` | string | yes |
| `escalateTo` | string | yes |
| `ackStatus` | string | yes |

#### `agent.contract.timeout`

Go `AgentContractTimeout`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `messageId` | string |  |
| `from` | string |  |
| `to` | string |  |
| `taskId` | string | yes |
| `teamId` | string | yes |
| `urgency` | string | yes |
| `escalateTo` | string | yes |
| `detail` | string | yes |

#### `team.roster`

Go `TeamRoster`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `leadId` | string |  |
| `members` | array of object |  |
| `members[].sessionId` | string |  |
| `members[].name` | string | yes |
| `members[].agent` | string | yes |
| `members[].state` | string |  |
| `members[].parentSessionId` | string | yes |
| `members[].depth` | number | yes |
| `members[].startedAt` | string | yes |
| `members[].terminalSummary` | string | yes |
| `members[].role` | string | yes |
| `members[].queuePools` | array of string | yes |
| `members[].queueLabel` | string | yes |
| `members[].objective` | string | yes |
| `members[].lastAction` | string | yes |
| `members[].blockReason` | string | yes |
| `members[].filesTouched` | array of string | yes |
| `members[].budget` | object | yes |
| `members[].budget.maxWallClockS` | number | yes |
| `members[].budget.maxTokens` | number | yes |
| `members[].budget.maxCostUsd` | float | yes |
| `members[].budget.maxToolCalls` | number | yes |
| `members[].budget.maxDangerousTools` | number | yes |
| `members[].budget.stallAfterS` | number | yes |
| `members[].budget.loopDetectN` | number | yes |
| `members[].budget.elapsedS` | number | yes |
| `members[].budget.tokensUsed` | number | yes |
| `members[].budget.costUsdUsed` | float | yes |
| `members[].budget.toolCalls` | number | yes |
| `members[].budget.dangerousTools` | number | yes |
| `members[].budget.wallClockRemainingS` | number | yes |
| `members[].budget.tokensRemaining` | number | yes |
| `members[].budget.toolCallsRemaining` | number | yes |
| `members[].budget.dangerousRemaining` | number | yes |
| `members[].budget.costUsdRemaining` | float | yes |
| `members[].budget.stall` | bool | yes |
| `members[].budget.idleS` | number | yes |
| `members[].budget.lastProgressAt` | string | yes |
| `members[].budget.stallAfterSEffective` | number | yes |
| `members[].budget.loop` | bool | yes |
| `members[].budget.escalated` | bool | yes |
| `members[].budget.escalateKind` | string | yes |
| `members[].budget.escalateReason` | string | yes |

#### `usage.reported`

Go `UsageReported`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `input` | object |  |
| `input.n` | number | yes |
| `input.known` | bool |  |
| `output` | object |  |
| `output.n` | number | yes |
| `output.known` | bool |  |
| `cacheRead` | object |  |
| `cacheRead.n` | number | yes |
| `cacheRead.known` | bool |  |
| `cacheCreation` | object |  |
| `cacheCreation.n` | number | yes |
| `cacheCreation.known` | bool |  |
| `used` | object |  |
| `used.n` | number | yes |
| `used.known` | bool |  |
| `source` | string | yes |

#### `provider.retrying`

Go `ProviderRetrying`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `nextAttempt` | number |  |
| `delayMs` | number | yes |
| `fromProvider` | bool | yes |
| `message` | string | yes |

#### `tool.retrying`

Go `ToolRetrying`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `callId` | string |  |
| `name` | string |  |
| `nextAttempt` | number |  |
| `delayMs` | number | yes |
| `errorCode` | string | yes |
| `message` | string | yes |

#### `tool.loop_detected`

Go `ToolLoopDetected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `reason` | string |  |
| `toolName` | string | yes |
| `count` | number | yes |
| `message` | string | yes |

#### `scheduler.queued`

Go `SchedulerQueued`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |
| `pools` | array of string |  |
| `label` | string | yes |

#### `scheduler.admitted`

Go `SchedulerAdmitted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |
| `pools` | array of string |  |
| `label` | string | yes |
| `waitMs` | number | yes |

#### `scheduler.canceled`

Go `SchedulerCanceled`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `requestId` | string |  |
| `pools` | array of string |  |
| `label` | string | yes |
| `waitMs` | number | yes |
| `reason` | string | yes |

#### `compaction.started`

Go `CompactionStarted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `reason` | string |  |
| `strategy` | string | yes |

#### `compaction.completed`

Go `CompactionCompleted`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `reason` | string |  |
| `strategy` | string | yes |
| `removed` | number |  |
| `kept` | number |  |
| `summary` | string | yes |
| `residue` | object | yes |
| `residue.schemaVersion` | string |  |
| `residue.strategy` | string | yes |
| `residue.reason` | string | yes |
| `residue.removed` | number | yes |
| `residue.facts` | array of object | yes |
| `residue.facts[].id` | string |  |
| `residue.facts[].kind` | string |  |
| `residue.facts[].text` | string |  |
| `residue.facts[].confidence` | string | yes |
| `residue.facts[].freshness` | string | yes |
| `residue.facts[].sourceIds` | array of string | yes |
| `residue.facts[].fileRefs` | array of string | yes |
| `residue.facts[].ledgerId` | string | yes |
| `residue.decisions` | array of object | yes |
| `residue.decisions[].id` | string |  |
| `residue.decisions[].kind` | string |  |
| `residue.decisions[].text` | string |  |
| `residue.decisions[].confidence` | string | yes |
| `residue.decisions[].freshness` | string | yes |
| `residue.decisions[].sourceIds` | array of string | yes |
| `residue.decisions[].fileRefs` | array of string | yes |
| `residue.decisions[].ledgerId` | string | yes |
| `residue.openQuestions` | array of object | yes |
| `residue.openQuestions[].id` | string |  |
| `residue.openQuestions[].kind` | string |  |
| `residue.openQuestions[].text` | string |  |
| `residue.openQuestions[].confidence` | string | yes |
| `residue.openQuestions[].freshness` | string | yes |
| `residue.openQuestions[].sourceIds` | array of string | yes |
| `residue.openQuestions[].fileRefs` | array of string | yes |
| `residue.openQuestions[].ledgerId` | string | yes |
| `residue.pinnedKinds` | array of string | yes |
| `residue.summary` | string | yes |

#### `session.meta`

Go `SessionMeta`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `prUrl` | string | yes |
| `prNumber` | number | yes |
| `prState` | string | yes |
| `isolation` | string | yes |

#### `session.rewound`

Go `SessionRewound`.

Shadowed (outer field wins): `turnId`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `removed` | number | yes |
| `turnId` | string | yes |
| `restoreFiles` | bool | yes |
| `filesRestored` | number | yes |
| `filesSkipped` | number | yes |
| `files` | array of string | yes |
| `uncovered` | array of string | yes |

#### `hook.matched`

Go `HookMatched`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `event` | string |  |
| `action` | string |  |
| `matcher` | string | yes |
| `tool` | string | yes |
| `message` | string | yes |
| `callId` | string | yes |

#### `prompt.effective`

Go `EffectivePrompt`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `layers` | array of object |  |
| `layers[].kind` | string |  |
| `layers[].source` | string |  |
| `layers[].mode` | string |  |
| `layers[].chars` | number |  |
| `layers[].estTokens` | number | yes |
| `layers[].pinned` | bool | yes |
| `layers[].preview` | string | yes |
| `systemChars` | number |  |
| `messageCount` | number |  |
| `fromLastStream` | bool | yes |
| `attribution` | object |  |
| `attribution.system` | object |  |
| `attribution.system.n` | number | yes |
| `attribution.system.known` | bool |  |
| `attribution.tools` | object |  |
| `attribution.tools.n` | number | yes |
| `attribution.tools.known` | bool |  |
| `attribution.messages` | object |  |
| `attribution.messages.n` | number | yes |
| `attribution.messages.known` | bool |  |
| `attribution.toolResults` | object |  |
| `attribution.toolResults.n` | number | yes |
| `attribution.toolResults.known` | bool |  |
| `attribution.total` | object |  |
| `attribution.total.n` | number | yes |
| `attribution.total.known` | bool |  |
| `attribution.source` | string | yes |
| `excludedKinds` | array of string | yes |
| `pinnedKinds` | array of string | yes |
| `shedKinds` | array of string | yes |

#### `diagnostic.bundle`

Go `DiagnosticBundle`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `schemaVersion` | string |  |
| `protocolVersion` | string | yes |
| `strikeVersion` | string | yes |
| `exportedAt` | time |  |
| `redacted` | bool |  |
| `note` | string | yes |
| `session` | object |  |
| `session.sessionId` | string | yes |
| `session.parentSessionId` | string | yes |
| `session.rootSessionId` | string | yes |
| `session.depth` | number |  |
| `session.isChild` | bool | yes |
| `prompt` | object |  |
| `prompt.precedence` | array of string |  |
| `prompt.layers` | array of object |  |
| `prompt.layers[].kind` | string |  |
| `prompt.layers[].source` | string |  |
| `prompt.layers[].mode` | string |  |
| `prompt.layers[].chars` | number |  |
| `prompt.layers[].estTokens` | number | yes |
| `prompt.layers[].pinned` | bool | yes |
| `prompt.layers[].preview` | string | yes |
| `prompt.layerCount` | number |  |
| `prompt.systemChars` | number |  |
| `prompt.messageCount` | number |  |
| `prompt.fromLastStream` | bool | yes |
| `prompt.attribution` | object |  |
| `prompt.attribution.system` | object |  |
| `prompt.attribution.system.n` | number | yes |
| `prompt.attribution.system.known` | bool |  |
| `prompt.attribution.tools` | object |  |
| `prompt.attribution.tools.n` | number | yes |
| `prompt.attribution.tools.known` | bool |  |
| `prompt.attribution.messages` | object |  |
| `prompt.attribution.messages.n` | number | yes |
| `prompt.attribution.messages.known` | bool |  |
| `prompt.attribution.toolResults` | object |  |
| `prompt.attribution.toolResults.n` | number | yes |
| `prompt.attribution.toolResults.known` | bool |  |
| `prompt.attribution.total` | object |  |
| `prompt.attribution.total.n` | number | yes |
| `prompt.attribution.total.known` | bool |  |
| `prompt.attribution.source` | string | yes |
| `config` | object |  |
| `config.provider` | string | yes |
| `config.model` | string | yes |
| `config.agent` | string | yes |
| `config.effort` | string | yes |
| `config.autonomy` | string | yes |
| `config.permissionMode` | string | yes |
| `config.sandbox` | string | yes |
| `config.leanCode` | string | yes |
| `config.fast` | bool | yes |
| `config.maxTokens` | number | yes |
| `config.maxChildDepth` | number | yes |
| `config.contextWindow` | number | yes |
| `config.turnTimeoutS` | number | yes |
| `config.workDir` | string | yes |
| `config.projectRoot` | string | yes |
| `config.compaction` | object |  |
| `config.compaction.strategy` | string | yes |
| `config.compaction.model` | string | yes |
| `config.compaction.threshold` | float | yes |
| `config.compaction.buffer` | number | yes |
| `config.compaction.keepUserTurns` | number | yes |
| `config.compaction.pruneProtectTokens` | number | yes |
| `config.compaction.pruneMinimumTokens` | number | yes |
| `config.compaction.pruneKeepUserTurns` | number | yes |
| `config.compaction.pruneProtectTools` | array of string | yes |
| `config.scheduler` | object |  |
| `config.scheduler.limits` | map of number | yes |
| `config.digests` | map of string | yes |
| `warnings` | array of string | yes |

#### `context.fit_warning`

Go `ContextFitWarning`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `estimatedTokens` | number |  |
| `contextLimit` | number |  |
| `level` | string |  |
| `message` | string |  |
| `source` | string | yes |

#### `session.budget_warning`

Go `SessionBudgetWarning`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `level` | string |  |
| `kind` | string |  |
| `exhausted` | bool | yes |
| `costUsd` | float | yes |
| `maxCostUsd` | float | yes |
| `tokensUsed` | number | yes |
| `maxTokens` | number | yes |
| `ratio` | float | yes |
| `message` | string |  |

#### `context.controls`

Go `ContextControlsSelected`.

| Field | Type | omitempty |
|---|---|---|
| `sessionId` | string | yes |
| `turnId` | string | yes |
| `providerRequestId` | string | yes |
| `attempt` | number | yes |
| `parentSessionId` | string | yes |
| `depth` | number | yes |
| `excludedKinds` | array of string | yes |
| `pinnedKinds` | array of string | yes |

<!-- session-format:catalog:end -->
