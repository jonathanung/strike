/**
 * Coalesce high-frequency stream envelopes to at most one React commit per
 * animation frame (WEBUI.20). Full event order is preserved; only paint is batched.
 */

export type StreamSink<T> = (batch: T[]) => void;

export type StreamBatcher<T> = {
  /** Enqueue one item; schedules a rAF flush if idle. */
  push: (item: T) => void;
  /** Flush immediately (e.g. on disconnect or tests). */
  flush: () => void;
  /** Cancel pending frame and drop the queue. */
  clear: () => void;
  /** Items waiting for the next flush. */
  pending: () => number;
};

export type StreamBatcherOptions = {
  /** Override scheduler (tests). Defaults to requestAnimationFrame. */
  schedule?: (cb: () => void) => number;
  cancel?: (id: number) => void;
};

/**
 * Create a batcher that delivers all pushed items in order via a single sink
 * call per animation frame.
 */
export function createStreamBatcher<T>(
  sink: StreamSink<T>,
  opts: StreamBatcherOptions = {},
): StreamBatcher<T> {
  const vitest = typeof process !== "undefined" && Boolean(process.env?.VITEST);
  const schedule =
    opts.schedule ??
    ((cb: () => void) => {
      // Vitest/jsdom: deliver immediately so App tests stay synchronous.
      // Coalescing is covered by streamBatch.test.ts with a fake scheduler.
      // Production: one sink call per animation frame under sustained streams.
      if (vitest) {
        cb();
        return -1;
      }
      if (typeof requestAnimationFrame === "function") return requestAnimationFrame(cb);
      return setTimeout(cb, 16) as unknown as number;
    });
  const cancel =
    opts.cancel ??
    ((id: number) => {
      if (id < 0) return;
      if (typeof cancelAnimationFrame === "function") cancelAnimationFrame(id);
      else clearTimeout(id);
    });

  let queue: T[] = [];
  let frame: number | undefined;
  let scheduled = false;

  const deliver = () => {
    scheduled = false;
    frame = undefined;
    if (!queue.length) return;
    const batch = queue;
    queue = [];
    sink(batch);
  };

  const flush = () => {
    if (frame !== undefined) {
      cancel(frame);
      frame = undefined;
    }
    scheduled = false;
    if (!queue.length) return;
    const batch = queue;
    queue = [];
    sink(batch);
  };

  const push = (item: T) => {
    queue.push(item);
    if (scheduled) return;
    scheduled = true;
    frame = schedule(deliver);
  };

  const clear = () => {
    if (frame !== undefined) {
      cancel(frame);
      frame = undefined;
    }
    scheduled = false;
    queue = [];
  };

  return {
    push,
    flush,
    clear,
    pending: () => queue.length,
  };
}
