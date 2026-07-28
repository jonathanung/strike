import type { HarnessMessage } from "./types.js";

export interface HarnessContext {
  signal: AbortSignal;
  emit(message: HarnessMessage): Promise<void>;
}

export type HarnessResult = HarnessMessage | readonly HarnessMessage[] | void;
export type HarnessHandler = (
  message: HarnessMessage,
  context: HarnessContext,
) => HarnessResult | Promise<HarnessResult>;

function writeLine(stream: StrikeWritableStream, value: unknown): Promise<void> {
  const line = `${JSON.stringify(value)}\n`;
  if (stream.write(line)) return Promise.resolve();
  return new Promise((resolve) => stream.once("drain", resolve));
}

function isHarnessMessage(value: unknown): value is HarnessMessage {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as { version?: unknown; type?: unknown; turnId?: unknown };
  return candidate.version === 1 && typeof candidate.type === "string" &&
    typeof candidate.turnId === "string";
}

/** Runs a function harness over process stdin/stdout until stdin closes. */
export async function runHarness(handler: HarnessHandler): Promise<void> {
  process.stdin.setEncoding("utf8");
  const turns = new Map<string, AbortController>();
  const pending = new Set<Promise<void>>();
  let writes = Promise.resolve();
  let buffer = "";

  const emit = (message: HarnessMessage): Promise<void> => {
    writes = writes.then(() => writeLine(process.stdout, message));
    return writes;
  };

  const dispatch = (message: HarnessMessage): void => {
    let controller = turns.get(message.turnId);
    if (message.type === "turn.start") {
      controller?.abort("turn restarted");
      controller = new AbortController();
      turns.set(message.turnId, controller);
    } else if (!controller) {
      controller = new AbortController();
      turns.set(message.turnId, controller);
    }

    if (message.type === "turn.cancel") controller.abort(message.reason);
    const context: HarnessContext = {
      signal: controller.signal,
      emit,
    };

    const task = Promise.resolve(handler(message, context))
      .then(async (result) => {
        if (result === undefined) return;
        const messages: readonly HarnessMessage[] = Array.isArray(result)
          ? result
          : [result as HarnessMessage];
        for (const outbound of messages) {
          await context.emit(outbound);
        }
      })
      .catch(async (error: unknown) => {
        const detail = error instanceof Error ? error.message : String(error);
        await writeLine(process.stderr, `harness handler error (${message.turnId}): ${detail}`);
        await context.emit({
          version: 1,
          type: "turn.error",
          turnId: message.turnId,
          code: "harness_error",
          message: detail,
        });
      })
      .finally(() => {
        pending.delete(task);
        if (message.type === "turn.complete" || message.type === "turn.error") {
          turns.delete(message.turnId);
        }
      });
    pending.add(task);
  };

  for await (const chunk of process.stdin) {
    buffer += typeof chunk === "string" ? chunk : new TextDecoder().decode(chunk);
    let newline = buffer.indexOf("\n");
    while (newline >= 0) {
      const line = buffer.slice(0, newline).trim();
      buffer = buffer.slice(newline + 1);
      if (line !== "") {
        const value: unknown = JSON.parse(line);
        if (!isHarnessMessage(value)) throw new Error("invalid harness message envelope");
        dispatch(value);
      }
      newline = buffer.indexOf("\n");
    }
  }

  if (buffer.trim() !== "") throw new Error("stdin ended with an incomplete JSONL record");
  for (const controller of turns.values()) controller.abort("stdin closed");
  await Promise.allSettled(pending);
  await writes;
}
