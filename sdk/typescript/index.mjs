import { createInterface } from "node:readline";

// runHarness adapts Strike's private JSONL transport to one ordinary function:
// async (input, provider, emit) => Response.
export function runHarness(harness) {
  const lines = createInterface({ input: process.stdin });
  const waiters = new Map();
  let sequence = 0;
  let started = false;
  let controller;

  const send = (message) => {
    process.stdout.write(`${JSON.stringify(message)}\n`);
  };

  const requestResponse = (id, message) => new Promise((resolve, reject) => {
    waiters.set(id, { resolve, reject });
    send(message);
  });

  lines.on("line", async (line) => {
    let message;
    try {
      message = JSON.parse(line);
    } catch {
      return;
    }

    if (message.type === "provider.result") {
      const waiter = waiters.get(message.callId);
      if (!waiter) return;
      waiters.delete(message.callId);
      if (message.error) waiter.reject(new Error(message.error));
      else waiter.resolve(message);
      return;
    }
    if (message.type === "tool.result") {
      const waiter = waiters.get(message.callId);
      if (!waiter) return;
      waiters.delete(message.callId);
      // Transport-level failure only when error is set without a tool outcome.
      if (message.error && message.output == null && !message.isError) {
        waiter.reject(new Error(message.error));
        return;
      }
      waiter.resolve({
        callId: message.callId,
        output: message.output ?? message.error ?? "",
        isError: Boolean(message.isError || message.error),
        errorCode: message.errorCode,
        retryable: message.retryable,
      });
      return;
    }
    if (message.type === "harness.cancel") {
      const error = new Error(message.reason || "harness canceled");
      controller?.abort(error);
      for (const waiter of waiters.values()) waiter.reject(error);
      waiters.clear();
      return;
    }
    if (message.type !== "harness.start" || started) return;
    started = true;

    controller = new AbortController();
    const base = { version: 1, invocationId: message.invocationId };
    const provider = {
      call(request) {
        const callId = `provider-${++sequence}`;
        return requestResponse(callId, { ...base, type: "provider.call", callId, request });
      },
    };
    const tools = {
      execute(call = {}) {
        const callId = `tool-${++sequence}`;
        const toolCallId = call.id || callId;
        return requestResponse(callId, {
          ...base,
          type: "tool.execute",
          callId,
          toolCallId,
          name: call.name,
          arguments: call.arguments ?? {},
        });
      },
    };
    const input = {
      request: message.request,
      signal: controller.signal,
      tools,
    };
    const emit = (payload) => send({ ...base, type: "progress.emit", payload });

    try {
      const response = await harness(input, provider, emit);
      if (response == null || typeof response !== "object") {
        throw new Error("harness must return an object");
      }
      send({
        ...base,
        type: "harness.complete",
        text: response.text,
        reasoning: response.reasoning,
        toolCalls: response.toolCalls,
        stopReason: response.stopReason,
      });
    } catch (error) {
      send({ ...base, type: "harness.error", error: String(error?.message || error) });
    } finally {
      lines.close();
    }
  });
}
