# Protocol schema

`pkg/protocol` is the source of truth for the Op/Event wire. Everything else
is generated.

| Artifact | Role |
|---|---|
| [`schemas/protocol/v1/schema.json`](../schemas/protocol/v1/schema.json) | JSON Schema 2020-12 for this `protocol.Version` |
| [`schemas/protocol/v1/COMPAT.md`](../schemas/protocol/v1/COMPAT.md) | Generated version, type list, and reader rules |
| [`schemas/protocol/v1/golden-events.jsonl`](../schemas/protocol/v1/golden-events.jsonl) | Fixed event stream, including an unknown field and an unknown type |
| `webui/protocol-schema` | Rust types and `decode_event` / `decode_op` |

The cockpit's HTTP view models stay in `webui/src/model.rs`. They are not a
second copy of this schema. Live SSE envelopes go through `decode_event`
before the view renders them.

## Regenerate and check

```sh
make protocol-schema        # rewrite the four artifacts
make protocol-schema-check  # drift + cargo test of the golden stream
```

`make test` runs the Go drift check. CI also runs the Rust decode tests.
`UPDATE_SCHEMA=1` inside the Go test is the same write path as the make target.

## Versioning

The number in the schema (`x-strike-protocol-version`) is `protocol.Version`.
Rules match the package comment and [protocol.md](protocol.md):

| Change | Bump | Readers of the previous minor |
|---|---|---|
| New event type string | minor | Keep the envelope as an unknown event. Do not fail the stream. |
| New optional field | minor | Ignore the field. |
| New op type string | minor | Reject it. Ops stay fail-closed. |
| Rename, remove, or change the meaning of a field or type string | major | Migration note in CHANGELOG and protocol.md. |
| Docs or helpers that do not change encoded JSON | patch | No schema change. |

`required` in the schema is what writers emit. Readers treat a missing field
as the zero value so an older log still decodes. Named strings
(`Effort`, `PermissionMode`, …) list `x-strike-known-values` but stay open:
a reader must accept a string it does not know.

An envelope with no `v` is `LegacyVersion` (`1.0.0`), same as `Envelope.SchemaVersion`.

## Clients

The JSON Schema is the portable contract. A future TypeScript client can
read `schemas/protocol/v1/schema.json`. The React Native client in #1291 is
not part of this schema and is not started.

Rust consumers should use `webui/protocol-schema` rather than hand-copying
structs into `webui/src/model.rs`:

- `decode_event` maps unknown type strings to `Event::Unknown { type_name, data }`.
- Known events ignore unknown fields. Go nil slices arrive as JSON `null` and
  decode as empty vectors.
- `decode_op` returns an error for an unknown op type.

## What this schema is not

`webui/src/model.rs` still describes daemon HTTP payloads (`TeamSnapshot`,
session lists, files). Those types are not `pkg/protocol` events. Giving
them a schema would be a separate contract.
