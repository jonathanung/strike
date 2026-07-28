# External harnesses

An external harness replaces Strike's default model/tool control loop for an
agent turn. It is an ordinary executable that communicates with Strike over
bidirectional JSON Lines (JSONL) on stdin and stdout.

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
      "args": ["./.strike/harnesses/mcts.js"],
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

## Process model

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

## Messages

### `turn.start`

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

### `provider.call`

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

### `progress.emit`

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

### `tool.execute`

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

### `turn.complete` and `turn.error`

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

### `turn.cancel`

When a turn is interrupted, Strike sends a best-effort cancellation message:

```json
{"version":1,"type":"turn.cancel","turnId":"turn-id","reason":"context canceled"}
```

The harness should cancel its work and exit promptly. Strike closes or
terminates an unresponsive process after a short grace period.
