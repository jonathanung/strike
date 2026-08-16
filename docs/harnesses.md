# Function harnesses

A harness is one ordinary function used to implement a task subagent. When the
parent delegates to that agent, Strike calls the function once with an input,
a provider capability, and a progress callback. The input contains the subtask
request, abort signal, and a brokered tools capability; the provider only
performs additional model calls. Tool execution goes through Strike's registry,
permissions, hooks, sandbox, scheduler, redaction, and protocol events. The
function owns the complete subagent run and returns its final response.

## Integration modes

Every implementation becomes a `fn.Func`, but there are two distinct ways
to construct one:

| Mode | Loading | Configuration | Stock binary |
|---|---|---|---|
| Embedded Go | Imported, compiled, and registered by a Go composition root | Not configurable | No custom Go harnesses registered |
| External process | Command started at runtime and adapted to `fn.Func` over JSONL | `harnesses` entries in Strike config | Supported for any executable |

Go, JavaScript, and Lean can all use the external-process mode through
`sdk/go/harness`, `sdk/typescript`, and `sdk/lean`. These SDKs only help build a
compatible program; they do not add language-specific linkage to Strike. The
stock CLI reads configured commands in `cmd/strike`, starts a process per
invocation by default (oneshot), and communicates through the language-neutral
protocol. Config may opt a harness into **persistent-worker** mode so one
process serves multiple invocations.

An embedded Go harness is available only in binaries whose source explicitly
imports and registers that function. Go harnesses are not discovered from the
filesystem or loaded from JSON. The Go example in this repository is linked
into the integration test binary, not the stock `strike` executable.

The repository keeps the complete choose-best integration fixture in
[`examples/harnesses`](../examples/harnesses):

- [`examples/harnesses/choose_best.go`](../examples/harnesses/choose_best.go)
  implements `fn.Func` directly in Go.
- [`examples/harnesses/go-subprocess`](../examples/harnesses/go-subprocess)
  uses the public Go subprocess SDK.
- [`examples/harnesses/choose-best.mjs`](../examples/harnesses/choose-best.mjs)
  uses the typed JavaScript runtime from
  [`sdk/typescript`](../sdk/typescript).
- [`examples/harnesses/ChooseBest.lean`](../examples/harnesses/ChooseBest.lean)
  imports the reusable [`sdk/lean`](../sdk/lean) Lake package.
- [`examples/harnesses/config.example.json`](../examples/harnesses/config.example.json)
  configures all external implementations.

All implementations make three model calls, report progress, select the longest
candidate, and return it as the only committed response.

The harness may loop, branch, run MCTS, invoke external programs, maintain its
own state, or return immediately. It does not declare phases or a workflow
graph.

The primary boundary is application-oriented and language-neutral. A chess
engine, theorem prover, neural-symbolic runtime, or other native application
can implement the process ABI directly; JavaScript and `runHarness` are only a
convenience adapter.

## Embedded Go applications

Go harnesses are compile-time extensions. They cannot be named or loaded from
JSON because a Go function must already be linked into the Strike binary.

First, define the function in the Strike source tree. See the complete
[`choose_best.go`](../examples/harnesses/choose_best.go) example. Its compile-time
assertion confirms that it implements the same function contract used by the
engine:

```go
var _ fn.Func = ChooseBest
```

A minimal custom function looks like this:

```go
package main

import (
	"encoding/json"
	"fmt"

	"github.com/jonathanung/strike-cli/internal/fn"
)

func chessHarness(input fn.Input, provider fn.Provider, emit fn.Emit) (fn.Result, error) {
	position := decodePosition(input.Request)
	for depth := 1; ; depth++ {
		if err := input.Context.Err(); err != nil {
			return fn.Result{}, err
		}
		move := search(position, depth)
		emit(json.RawMessage(fmt.Sprintf(
			`{"kind":"depth","current":%d,"move":%q}`,
			depth, move,
		)))
		if solved(move) {
			return fn.Result{Text: move, StopReason: "complete"}, nil
		}
	}
}
```

Second, register it after `fn.NewRegistry()` in
`cmd/strike/assemble_tools.go`:

```go
harnessRegistry := fn.NewRegistry()
harnessRegistry.Register("chess", chessHarness)
```

The composition root already passes that registry to `engine.Options`. Finally,
select the registered name in the task agent's frontmatter:

```yaml
---
description: searches chess positions
harness: chess
---
```

Rebuild Strike after adding or changing an embedded Go harness. The application
owns its search, state, branching, and termination; Strike supplies only the
input, provider capability, and progress callback.

## Engine integration

The integration path is deliberately small:

1. `cmd/strike` converts configured commands into `fn.Func` values and
   registers them by name.
2. Agent frontmatter stores the selected name in `engine.Agent.Harness`.
3. When `task` starts that agent, the child engine resolves the name. With no
   custom name it runs the built-in child model/tool loop; otherwise the
   complete subagent run is one function call. Root turns never invoke harnesses.
4. `internal/engine/harness.go` constructs the input (including tools broker),
   provider capability, and progress callback. Only the function's returned
   `fn.Result` is committed as the assistant response. Mid-run tool
   results are returned to the harness only and are not written into history.

`sdk/typescript` and `sdk/lean` hide the subprocess JSONL protocol. Harness code
does not manage invocation IDs, call IDs, protocol messages, or process
lifecycle. The TypeScript package exports declarations alongside its Node
runtime; the Lean package exports `StrikeHarness.runHarness`.

> [!WARNING]
> Harnesses are trusted native executables. Loading one is equivalent to
> running its configured command directly. Strike does not sandbox harnesses,
> filter their environment, or restrict their direct access to the system.

## External process configuration

JSON configuration is for subprocess harnesses. Define their commands in
`~/.strike/config` or `./.strike/config`:

```json
{
  "harnesses": {
    "choose-best-js": {
      "command": "node",
      "args": ["./examples/harnesses/choose-best.mjs"]
    },
    "choose-best-lean": {
      "command": "lake",
      "args": ["--dir", "./examples/harnesses", "exe", "choose_best_lean"]
    },
    "heavy-runtime": {
      "command": "./bin/my-harness-worker",
      "mode": "persistent",
      "maxConcurrent": 2,
      "idleTimeoutMs": 120000,
      "maxRestarts": 3
    }
  }
}
```

| Field | Default | Meaning |
|---|---|---|
| `command` / `args` / `env` | required command | Subprocess argv and env overlay |
| `mode` | `oneshot` | `oneshot` = new process per invocation; `persistent` = reuse one worker |
| `maxConcurrent` | `1` | Max in-flight invocations on one persistent worker |
| `idleTimeoutMs` | `60000` | Shut down idle persistent workers; negative disables eviction |
| `maxRestarts` | `3` | Crash/start recoveries before the worker is disabled for the process; negative = unlimited |

Global and project definitions merge by name. A project definition replaces a
global definition with the same name.

Persistent workers still run the configured command with the same trust model
as oneshot harnesses (trusted native executables; no OS sandbox). Tool
execution continues through Strike's registry, permissions, hooks, sandbox,
scheduler, and redaction — the worker only brokers `tool.execute` / `provider.call`.

Select the external harness in frontmatter for an agent intended to run through
`task`:

```yaml
---
description: searches for the strongest solution
harness: choose-best-js
---
```

For both embedded and external harnesses, unknown names fail during startup.
Omitting `harness`, or setting it to `default`, keeps Strike's built-in child
loop.

## Function API

```ts
type Harness = (input: {
	request: ProviderRequest;
	signal: AbortSignal;
	tools: {
		execute: (call: ToolCall) => Promise<ToolResult>;
	};
}, provider: {
	call: (request: ProviderRequest) => Promise<ModelResponse>;
}, emit: (progress: ProgressEvent) => void) => Promise<Response>;
```

- `input.request` is the initial normalized request, including messages and
  tool schemas the harness may execute.
- `provider.call` performs a complete Strike-managed model call. Calls may run
	concurrently and do not enter conversation history. Provider streaming and
	retries remain internal to Strike.
- `input.tools.execute` runs one tool through the same safety and audit path as
  the built-in engine loop (permissions, hooks, sandbox, scheduler, redaction,
  begin/end events). Concurrent execute requests are serialized by Strike.
  Denied, unknown, and malformed calls return structured `{ isError, errorCode,
  output }` results rather than crashing the harness process.
- `emit` publishes structured progress to the UI and session log.
- `input.signal` is aborted when the user interrupts the agent run; in-flight
  tool execution is canceled with the turn.
- The returned `Response` is the only assistant response committed to history.
  Do not return unresolved tool calls from the final response — execute them
  via `tools.execute` during the run.

## Private transport

The remaining protocol is an implementation detail for SDK/runner authors.
Harness functions should use `runHarness` rather than producing these messages.

### Process model

**Oneshot (default):** Strike starts one harness process per invocation and
tears it down after `harness.complete`, `harness.error`, or cancel.

**Persistent (`mode: "persistent"`):** Strike keeps one worker process per
configured harness name. Each invocation still gets a unique `invocationId`.
The host multiplexes messages by `invocationId` and advertises `persistent` in
`harness.start` capabilities. Workers must loop on `harness.start` (Go:
`RunWorker`; TypeScript: `runHarness(fn, { persistent: true })`) instead of
exiting after one completion.

Messages are single-line JSON objects with `version: 1`, a `type`, and (for
invocation-scoped messages) the active `invocationId`. Harness stdout is
reserved for protocol messages; diagnostics should use stderr.

Strike serializes writes to the process. A harness may issue multiple
`provider.call` and `tool.execute` requests concurrently by assigning each a
unique `callId` (shared ID space per invocation). Provider calls are speculative: their output
does not enter Strike's conversation history. Tool results are returned only to
the harness; begin/end events still appear on the session timeline. Only the
response supplied by `harness.complete` becomes the final assistant message.

**Cancellation (persistent):** `harness.cancel` targets one `invocationId`.
Other in-flight invocations keep running. If the worker does not finish that
invocation within the cancel grace period, Strike kills the whole worker and
fails remaining invocations (then may restart per policy).

**Crash recovery:** A dead or protocol-violating worker fails in-flight
invocations. The next invoke may start a fresh process until `maxRestarts` is
exceeded, after which the harness is disabled for the Strike process lifetime.

**Idle eviction:** When no invocations are active, Strike sends
`harness.shutdown` after `idleTimeoutMs` and closes the process. The next
invoke starts a new worker. Session teardown also shuts workers down.

**Readiness / health:** Process start success is the readiness gate. Workers
may optionally emit `harness.ready` after boot. Health is observed via I/O and
process exit; there is no separate ping RPC in v1.

Strike rejects malformed messages, unsupported versions, unknown
`invocationId`s (persistent), duplicate request IDs, lines over 1 MiB, and
aggregate output over 16 MiB. These are reliability limits, not security
boundaries.

The external ABI is versioned additively under `version: 1`. Older oneshot
harnesses that only use `provider.call` remain compatible; hosts advertise
`tool.execute` (and `persistent` when applicable) in `harness.start`
capabilities.

### Messages

#### `harness.start`

Strike sends the initial request and active selection:

```json
{
  "version": 1,
  "type": "harness.start",
  "invocationId": "invocation-id",
	"request": {
    "model": "gpt-5",
    "system": "...",
    "messages": [{"role": "user", "text": "..."}],
    "tools": [],
    "maxOutputTokens": 8192,
    "effort": "high"
  },
  "capabilities": [
	  "provider.call",
	  "tool.execute",
	  "progress.emit",
	  "harness.cancel",
	  "persistent"
  ]
}
```

The `persistent` capability is present only when the host is running this
harness in persistent-worker mode. The request contains normalized provider
messages and the currently available Strike tool schemas. Strike retains
provider selection and authentication.

#### `harness.ready` (optional, worker → host)

A persistent worker may emit readiness after process start:

```json
{"version":1,"type":"harness.ready"}
```

#### `harness.shutdown` (host → worker)

Strike asks a persistent worker to exit cleanly (idle eviction or session end):

```json
{"version":1,"type":"harness.shutdown"}
```

The worker should cancel in-flight work and exit. Strike kills an unresponsive
process after a short grace period.

#### `provider.call`

The harness requests a completed model response:

```json
{
  "version": 1,
  "type": "provider.call",
  "invocationId": "invocation-id",
  "callId": "candidate-1",
  "request": {
    "model": "ignored",
    "messages": [{"role": "user", "text": "Generate a candidate"}],
    "tools": []
  }
}
```

Strike forces the active model, consumes its stream internally, and replies
once with the same `callId`:

```json
{"version":1,"type":"provider.result","invocationId":"invocation-id","callId":"candidate-1","text":"candidate","stopReason":"end_turn"}
```

#### `tool.execute`

The harness requests brokered execution of one tool exposed in
`harness.start.request.tools`:

```json
{
  "version": 1,
  "type": "tool.execute",
  "invocationId": "invocation-id",
  "callId": "tool-1",
  "toolCallId": "optional-event-id",
  "name": "read",
  "arguments": {"filePath": "README.md"}
}
```

`callId` correlates the request with `tool.result` (unique across provider and
tool requests). `toolCallId` is the id stamped on protocol `tool.call.begin` /
`tool.call.end` events; when omitted it defaults to `callId`.

Strike replies once:

```json
{
  "version": 1,
  "type": "tool.result",
  "invocationId": "invocation-id",
  "callId": "tool-1",
  "output": "file contents",
  "isError": false
}
```

Denied, unknown, canceled, and malformed calls set `isError` and a stable
`errorCode` (`permission_denied`, `invalid_args`, `canceled`, `internal`, …)
with a model-facing `output` string. Transport failures may set `error` instead.

#### `progress.emit`

The harness may publish structured progress:

```json
{
  "version": 1,
  "type": "progress.emit",
  "invocationId": "invocation-id",
  "payload": {
    "kind": "iteration",
    "message": "Expanded node 12",
    "current": 12,
    "total": 100
  }
}
```

Strike records this as `harness.progress` in the session event log. Wire
compatibility and other harness-related protocol events (verification,
permission audit, hooks): [protocol.md](protocol.md).

#### `harness.complete` and `harness.error`

Return the final assistant response with `harness.complete`:

```json
{
  "version": 1,
  "type": "harness.complete",
  "invocationId": "invocation-id",
  "text": "Final response",
  "stopReason": "end_turn"
}
```

A terminal harness failure uses `harness.error`:

```json
{
  "version": 1,
  "type": "harness.error",
  "invocationId": "invocation-id",
  "code": "search_failed",
  "error": "No viable candidate"
}
```

Exactly one terminal message is required. Exiting without one fails the agent
run.

#### `harness.cancel`

When the agent run is interrupted, Strike sends a best-effort cancellation
message:

```json
{"version":1,"type":"harness.cancel","invocationId":"invocation-id","reason":"context canceled"}
```

The harness should cancel its work and exit promptly. Strike closes or
terminates an unresponsive process after a short grace period. Go and
JavaScript expose cancellation to harness code through their input context or
signal. Lean observes cancellation while waiting for a provider call; otherwise
Strike terminates the process after the grace period.
