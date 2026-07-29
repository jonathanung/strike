import { createInterface } from "node:readline";

// runHarness adapts Strike's private JSONL transport to one ordinary function:
// async ({ request, provider, emit, execute, signal }) => Response.
export function runHarness(harness) {
  const lines = createInterface({ input: process.stdin });
  const waiters = new Map();
  let sequence = 0;
  let started = false;
  let controller;

  const send = (message) => {
    process.stdout.write(`${JSON.stringify(message)}\n`);
  };

  const stream = (id) => {
    const events = [];
    const readers = [];
    let done = false;

      push(event) {
        if (readers.length) readers.shift()({ value: event, done: false });
        else events.push(event);
        if (event.done || event.kind === "error") {
          done = true;
          while (readers.length) readers.shift()({ done: true });
          waiters.delete(id);
        }
      },
    });

    return {
      [Symbol.asyncIterator]() {
        return {
          next() {
            if (events.length) return Promise.resolve({ value: events.shift(), done: false });
            if (done) return Promise.resolve({ done: true });
            return new Promise((resolve) => readers.push(resolve));
          },
        };
      },
    };
  };

  lines.on("line", async (line) => {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      return;
    }

    if (message.type === "provider.event") {
      waiters.get(message.callId)?.push(message);
      return;
    }
    if (message.type === "tool.result") {
      waiters.get(message.toolCallId)?.push({ ...message, done: true });
      return;
    }
    if (message.type === "harness.cancel") {
      controller?.abort(new Error(message.reason || "harness canceled"));
      return;
    }
    if (message.type !== "harness.start" || started) return;
    started = true;

    controller = new AbortController();
    const base = { version: 1, invocationId: message.invocationId };
    const provider = (request) => {
      const callId = `provider-${++sequence}`;
      const result = stream(callId);
      send({ ...base, type: "provider.call", callId, request });
      return result;
    };
    const emit = (payload) => send({ ...base, type: "progress.emit", payload });
    const execute = async (call) => {
      const toolCallId = call.id || `tool-${++sequence}`;
      const result = stream(toolCallId)[Symbol.asyncIterator]();
      send({
        ...base,
        type: "tool.execute",
        toolCallId,
        name: call.name,
        arguments: call.arguments ?? {},
      });
      return (await result.next()).value;
    };

    try {
      const response = await harness({
        request: message.request,
        provider,
        emit,
        execute,
        signal: controller.signal,
      });
      send({ ...base, type: "harness.complete", ...response });
    } catch (error) {
      send({ ...base, type: "harness.error", error: String(error?.message || error) });
    } finally {
      lines.close();
    }
  });
}
