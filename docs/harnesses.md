# External harnesses

> [!CAUTION]
> A harness is a **trusted executable with the same OS permissions as
> `strike`**. There is no sandbox or process isolation beyond the operating
> system process boundary. A harness can read files, access credentials, make
> network requests, and execute programs. Install and configure only code you
> trust.

This document describes protocol v1 for external agent harnesses.

The dependency-free TypeScript reference types and runner live in
[`sdk/typescript`](../sdk/typescript/).

## Transport

Strike starts the configured executable and exchanges UTF-8 JSON Lines
(JSONL): one complete JSON object per newline. Strike writes requests to the
harness's stdin; the harness writes events and responses to stdout. Stdout is
protocol-only. Diagnostics belong on stderr.

Every message has `version: 1`, a `type`, and `turnId`. Request/response pairs
also carry an opaque ID such as `callId` or `toolCallId`. IDs must be echoed
unchanged and are unique within a turn; consumers must not infer ordering or
meaning from them.

Protocol v1 messages:

| Message | Direction | Purpose |
|---|---|---|
| `turn.start` | strike -> harness | Begin a turn with conversation, tools, model, and limits |
| `provider.call` | either | Request a normalized provider call |
| `provider.event` | response direction | Stream text, reasoning, tool-call, usage, or completion data for `callId` |
| `progress.emit` | harness -> strike | Publish human-readable harness progress |
| `tool.execute` | harness -> strike | Request execution of a named Strike tool |
| `tool.result` | strike -> harness | Return output or an error for `toolCallId` |
| `turn.complete` | harness -> strike | Finish successfully with an assistant message |
| `turn.error` | either | Finish unsuccessfully with a structured error |
| `turn.cancel` | strike -> harness | Request cooperative cancellation |

`provider.call` is direction-neutral so a future transport can host the
provider on either side. Its paired `provider.event` messages use the same
`callId` and set `done: true` on the final event.

## Provider concepts

`turn.start` carries `agent`, `provider`, `capabilities`, and a normalized
`request`. `provider.call` carries that same request shape under `request`.
Messages use `role` plus optional `text`, `images`, `toolCalls`, `toolResult`,
and `reasoning` fields. Provider event kinds are `text`, `reasoning`,
`tool_call`, `usage`, `completion`, and `error`.

Unknown object fields must be ignored. Unknown message types are protocol
errors in v1. JSON values under `arguments`, `output`, and `metadata` are
intentionally open.

## Concurrency, limits, and cancellation

- Multiple turns and multiple provider/tool calls may be in flight at once.
- Correlate all work by `turnId` and request ID, never by line order.
- Writes must remain whole JSONL records; do not interleave bytes from records.
- `limits` can include `maxSteps`, `maxProviderCalls`, `maxToolCalls`,
  `maxOutputTokens`, and `deadlineMs` (Unix epoch milliseconds). A missing
  limit is not unlimited if the host enforces a stricter policy.
- `turn.cancel` is cooperative. Stop starting work, abort cancellable I/O,
  and emit `turn.cancel` as acknowledgement or `turn.error` with
  `code: "cancelled"`. The host may kill an unresponsive process.
- Backpressure applies to both streams. A harness must await writes where its
  runtime exposes drain signals and bound internal queues.
- On EOF, stop accepting work and cancel in-flight operations. A malformed
  line or duplicate terminal message is a protocol error.

## Configuration

The eventual config key may change when the Go transport is implemented. The
planned shape is:

```json
{
  "harnesses": {
    "planner": {
      "command": "node",
      "args": ["./harness/dist/simple.js"],
      "env": { "HARNESS_MODE": "careful" },
      "protocol": 1
    }
  },
  "harness": "planner"
}
```

Use an argument array rather than a shell command string. Environment entries
augment the host environment; never put committed credentials in config.

## Simple loop

The SDK runner accepts an async function and serializes returned or explicitly
emitted messages. This sketch asks Strike to run a provider call, relays the
final text as the turn result, and respects cancellation:

```ts
import { runHarness, type HarnessMessage } from "@strike-cli/harness-sdk";

const text = new Map<string, string>();

await runHarness(async (message, context) => {
  if (message.type === "turn.start") {
    const callId = `${message.turnId}:provider:1`;
    text.set(callId, "");
    return {
      version: 1,
      type: "provider.call",
      turnId: message.turnId,
      callId,
	  request: message.request,
    };
  }

  if (message.type === "provider.event" && message.kind === "text") {
    text.set(message.callId, (text.get(message.callId) ?? "") + message.text);
  }

  if (message.type === "provider.event" && message.done) {
    const content = text.get(message.callId) ?? "";
    text.delete(message.callId);
    await context.emit({
      version: 1,
      type: "turn.complete",
      turnId: message.turnId,
	  text: content,
    });
  }
});
```

## MCTS-style harness

Tree search is an orchestration policy, not a separate protocol feature. Give
each rollout a unique `callId`, run only as many rollouts as the turn limits
and local concurrency cap allow, score candidates locally, and return the best
candidate. Provider events can arrive interleaved:

```ts
import { runHarness } from "@strike-cli/harness-sdk";

await runHarness(async (message, context) => {
  if (message.type !== "turn.start") return;

	const width = 4;
  await Promise.all(Array.from({ length: width }, async (_, index) => {
    if (context.signal.aborted) return;
    await context.emit({
      version: 1,
      type: "provider.call",
      turnId: message.turnId,
      callId: `${message.turnId}:rollout:${index}`,
	  request: message.request,
    });
  }));

  // Collect provider.event records by callId, expand/score within maxSteps,
  // optionally execute tools, then emit exactly one terminal message.
});
```

Production MCTS harnesses should maintain a per-turn state machine, enforce
all limits before expansion, and discard late events after cancellation or a
terminal message.
