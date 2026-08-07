# Go SDK (`pkg/sdk`)

Thin client over the public Op/Event wire schema
([`pkg/protocol`](../pkg/protocol)) for Go programs that drive or inspect
Strike sessions.

```go
import (
	"github.com/jonathanung/strike-cli/pkg/protocol"
	"github.com/jonathanung/strike-cli/pkg/sdk"
)
```

This is **not** the subprocess harness SDK (`sdk/go/harness`). Harnesses
implement custom task-subagent loops; `pkg/sdk` speaks the frontend↔engine
protocol.

## Boundaries

| Surface | Importable by external modules? |
|---|---|
| `pkg/protocol` | yes — wire types + JSONL envelopes |
| `pkg/sdk` | yes — client helpers over protocol |
| `internal/*` (engine, tools, auth, …) | **no** — Go `internal/` visibility |

The stock engine stays internal. External embedders either:

1. **Custom binary** — fork/extend `cmd/strike` composition root, construct
   `internal/engine`, pass `eng.Ops()` / `eng.Events()` into `sdk.New`.
2. **Transport** — exchange the same Op/Event JSON envelopes over a pipe or
   socket (`strike serve` WebSocket/`POST /v1/ops` today; `strike rpc` when
   shipped) via `sdk.ConnectJSONL` or manual `WriteOp` / event decode.
3. **Offline** — read durable session logs with `sdk.ReadSession`.

## Channel client (in-process)

```go
// eng is *engine.Engine from a custom composition root.
client := sdk.New(eng.Ops(), eng.Events())

result, err := client.RunTurn(ctx, sdk.Turn{
	Text: "summarize this repository",
	OnPermission: func(a protocol.PermissionAsked) protocol.PermissionReply {
		return protocol.PermissionReply{
			RequestID: a.RequestID,
			Decision:  protocol.DecisionOnce,
		}
	},
})
if err != nil {
	return err
}
fmt.Print(result.Text)
```

`RunTurn` submits `user.input`, concatenates `text.delta`, answers
permission/question asks (defaults: reject permissions, empty question
answers), and returns on `turn.completed`. Use `client.Send` for other ops
(`interrupt`, `select.model`, `compact`, …).

## JSONL transport

Ops and events on the wire use the same envelopes as session persistence and
`strike serve`:

```go
// opsOut ← client writes OpEnvelope lines
// eventsIn → client reads Event Envelope lines
client := sdk.ConnectJSONL(opsOut, eventsIn)
defer client.Close()

_ = client.Send(ctx, protocol.UserInput{Text: "hello"})
for ev := range client.Events() {
	fmt.Println(sdk.FormatEvent(ev))
}
```

Helpers:

| API | Role |
|---|---|
| `WriteOp` / `WriteEvent` | one JSONL envelope line |
| `DecodeOpLine` / `DecodeEventLine` | parse one line |
| `NewOpDecoder` / `NewEventDecoder` | streaming scanners (32 MiB line cap) |

Tear-down: close the underlying `eventsIn` reader (or connection) so the
pump's Decode unblocks and `Events` closes; `client.Close()` only signals the
pump to stop filling a full buffer and never blocks on a stuck read. Call
`Close` again after the pump exits if you need a decode error return.

## Session files

Strike persists each session as JSONL under `~/.strike/sessions/<id>.jsonl`.
New logs may begin with a `session.header` line (`schemaVersion`); event lines
are protocol envelopes (`type` / `time` / `v` / `data`). The stock CLI
(`internal/session`) fsyncs each append, skips trailing crash residue on
replay, and offers redacted portable packages (`strike.session`) plus fork/
retention helpers — see [config.md](config.md) (session durability) and
[ARCHITECTURE.md](ARCHITECTURE.md). Markdown `/export` (#221) and durable checkpoint stacks
(`~/.strike/checkpoints/`, #573) are separate surfaces.

```go
events, err := sdk.ReadSession(path)
// …
err = sdk.WriteSession(path, events) // fixtures / offline tooling
```

## Stability

- Wire schema version: `protocol.Version` (see `pkg/protocol` package doc and
  [protocol.md](protocol.md)).
- Compatibility: additive optional fields OK; renames need a major bump.
  Unknown **event** type strings decode as `protocol.UnknownEvent` (skip in
  type switches; `protocol.IsUnknown`). Unknown **ops** still fail decode.
- `pkg/sdk` APIs follow the strike-cli Go module version; prefer additive
  changes. Do not fork envelope type strings here — change `pkg/protocol`.

## Related

- [Protocol wire schema](protocol.md) — envelopes, harness events, extensions
- [Architecture](ARCHITECTURE.md) — dataflow and package table
- [Web cockpit](web.md) — live ops over HTTP/WebSocket
- [Harnesses](harnesses.md) — task function harnesses (`sdk/go/harness`, …)
