# Function harnesses

A harness is one ordinary function. Strike calls it once with the user's
request, model access, a progress callback, optional Strike tool execution, and
an abort signal. The function owns all control flow and returns the final
response:

```js
import { runHarness } from "../../sdk/harness.mjs";

const chooseBest = (candidates) =>
  candidates.reduce((best, candidate) =>
    candidate.length > best.length ? candidate : best, "");

async function harness({ request, provider, emit, execute, signal }) {
  emit({ kind: "started", message: "Searching candidates" });

  const candidates = [];
  for (let i = 0; i < 4; i++) {
    signal.throwIfAborted();
    let text = "";
    for await (const event of provider({
      ...request,
      messages: [
        ...request.messages,
        { role: "user", text: `Generate candidate ${i + 1}` },
      ],
    })) {
      if (event.kind === "text") text += event.text;
      if (event.kind === "error") throw new Error(event.error);
    }
    candidates.push(text);
    emit({ kind: "iteration", current: i + 1, total: 4 });
  }

  return { text: chooseBest(candidates), stopReason: "end_turn" };
}

runHarness(harness);
```

The harness may loop, branch, run MCTS, invoke external programs, maintain its
own state, call Strike tools through `execute`, or return immediately. It does
not declare phases or a workflow graph.

`sdk/harness.mjs` hides the subprocess JSONL protocol. Harness code does not
manage turn IDs, call IDs, protocol messages, or process lifecycle.

> [!WARNING]
> Harnesses are trusted native executables. Loading one is equivalent to
> running its configured command directly. Strike does not sandbox harnesses,
> filter their environment, or restrict their direct access to the system.

## Configuration

Define named harnesses in `~/.strike/config` or `./.strike/config`:

```json
{
  "harnesses": {
    "mcts": {
      "command": "node",
      "args": ["./.strike/harnesses/mcts.mjs"],
      "env": {
        "SEARCH_BUDGET": "100"
      }
    }
  }
}
```

Global and project definitions merge by name. A project definition replaces a
global definition with the same name.

Select the harness in agent frontmatter:

```yaml
---
description: searches for the strongest solution
harness: mcts
---
```

Unknown harness references fail during startup. Omitting `harness`, or setting
it to `default`, keeps Strike's built-in loop.

## Function API

```ts
type Harness = (context: {
  request: ProviderRequest;
  provider: (request: ProviderRequest) => AsyncIterable<ProviderEvent>;
  emit: (progress: ProgressEvent) => void;
  execute: (call: ToolCall) => Promise<ToolResult>;
  signal: AbortSignal;
}) => Promise<Response>;
```

- `request` is the initial normalized request, including messages and tools.
- `provider` performs a Strike-managed model call. Calls may run concurrently
  and do not enter conversation history.
- `emit` publishes structured progress to the UI and session log.
- `execute` optionally runs a Strike tool through normal permissions and hooks.
- `signal` is aborted when the user interrupts the request.
- The returned `Response` is the only assistant response committed to history.

## Private transport

The remaining protocol is an implementation detail for SDK/runner authors.
Harness functions should use `runHarness` rather than producing these messages.

### Process model

Strike starts one harness process per turn. Messages are single-line JSON
objects with `version: 1`, a `type`, and the active `turnId`. Harness stdout is
reserved for protocol messages; diagnostics should use stderr.

Strike serializes writes to the process. A harness may issue multiple
`provider.call` requests concurrently by assigning each a unique `callId`.
Provider calls are speculative: their output does not enter Strike's
conversation history. Only the response supplied by `turn.complete` becomes
the final assistant message.

Strike rejects malformed messages, unsupported versions, duplicate request
IDs, lines over 1 MiB, and aggregate output over 16 MiB. These are reliability
limits, not security boundaries.

### Messages

#### `turn.start`

Strike sends the initial request and active selection:

```json
{
  "version": 1,
  "type": "turn.start",
  "turnId": "turn-id",
  "agent": "search",
  "provider": "openai",
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
    "progress.emit",
    "tool.execute",
    "turn.cancel"
  ]
}
```

The request contains normalized provider messages and the currently available
Strike tool schemas. Strike retains provider selection and authentication.

#### `provider.call`

The harness requests a normalized model stream:

```json
{
  "version": 1,
  "type": "provider.call",
  "turnId": "turn-id",
  "callId": "candidate-1",
  "request": {
    "model": "ignored",
    "messages": [{"role": "user", "text": "Generate a candidate"}],
    "tools": []
  }
}
```

Strike forces the active model and forwards normalized `provider.event`
messages carrying the same `callId`. Event `kind` values are `text`,
`reasoning`, `tool_call`, `usage`, `completion`, and `error`.

```json
{"version":1,"type":"provider.event","turnId":"turn-id","callId":"candidate-1","kind":"text","text":"candidate"}
{"version":1,"type":"provider.event","turnId":"turn-id","callId":"candidate-1","kind":"completion","done":true,"stopReason":"end_turn"}
```

#### `progress.emit`

The harness may publish structured progress:

```json
{
  "version": 1,
  "type": "progress.emit",
  "turnId": "turn-id",
  "payload": {
    "kind": "iteration",
    "message": "Expanded node 12",
    "current": 12,
    "total": 100
  }
}
```

Strike records this as `harness.progress` in the session event log.

#### `tool.execute`

The harness can ask Strike to execute a built-in tool:

```json
{
  "version": 1,
  "type": "tool.execute",
  "turnId": "turn-id",
  "toolCallId": "tool-1",
  "name": "read",
  "arguments": {"filePath": "README.md"}
}
```

Strike routes the call through its normal tool implementation, permissions,
hooks, questions, and event emission, then replies with `tool.result`:

```json
{"version":1,"type":"tool.result","turnId":"turn-id","toolCallId":"tool-1","output":"..."}
```

`tool.execute` is a convenience API, not a sandbox boundary. The harness may
also execute external logic directly.

#### `turn.complete` and `turn.error`

Return the final assistant response with `turn.complete`:

```json
{
  "version": 1,
  "type": "turn.complete",
  "turnId": "turn-id",
  "text": "Final response",
  "stopReason": "end_turn"
}
```

A terminal harness failure uses `turn.error`:

```json
{
  "version": 1,
  "type": "turn.error",
  "turnId": "turn-id",
  "code": "search_failed",
  "error": "No viable candidate"
}
```

Exactly one terminal message is required. Exiting without one fails the turn.

#### `turn.cancel`

When a turn is interrupted, Strike sends a best-effort cancellation message:

```json
{"version":1,"type":"turn.cancel","turnId":"turn-id","reason":"context canceled"}
```

The harness should cancel its work and exit promptly. Strike closes or
terminates an unresponsive process after a short grace period.
