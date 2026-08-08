import { describe, expect, it, vi } from "vitest";
import { createStreamBatcher } from "./streamBatch";

describe("createStreamBatcher", () => {
  it("coalesces many pushes into one sink call per scheduled frame", () => {
    const frames: Array<() => void> = [];
    const sink = vi.fn();
    const batcher = createStreamBatcher<number>(sink, {
      schedule: (cb) => {
        frames.push(cb);
        return frames.length;
      },
      cancel: () => {},
    });

    batcher.push(1);
    batcher.push(2);
    batcher.push(3);
    expect(sink).not.toHaveBeenCalled();
    expect(batcher.pending()).toBe(3);
    expect(frames).toHaveLength(1);

    frames[0]();
    expect(sink).toHaveBeenCalledTimes(1);
    expect(sink).toHaveBeenCalledWith([1, 2, 3]);
    expect(batcher.pending()).toBe(0);
  });

  it("schedules a new frame after a flush under sustained input", () => {
    const frames: Array<() => void> = [];
    const sink = vi.fn();
    const batcher = createStreamBatcher<string>(sink, {
      schedule: (cb) => {
        frames.push(cb);
        return frames.length;
      },
      cancel: () => {},
    });

    batcher.push("a");
    frames[0]();
    expect(sink).toHaveBeenCalledWith(["a"]);

    batcher.push("b");
    batcher.push("c");
    expect(frames).toHaveLength(2);
    frames[1]();
    expect(sink).toHaveBeenLastCalledWith(["b", "c"]);
    expect(sink).toHaveBeenCalledTimes(2);
  });

  it("flush delivers immediately without waiting for the frame", () => {
    const sink = vi.fn();
    const batcher = createStreamBatcher<number>(sink, {
      schedule: () => 1,
      cancel: () => {},
    });
    batcher.push(9);
    batcher.flush();
    expect(sink).toHaveBeenCalledWith([9]);
    expect(batcher.pending()).toBe(0);
  });

  it("clear drops pending work", () => {
    const sink = vi.fn();
    const batcher = createStreamBatcher<number>(sink, {
      schedule: () => 1,
      cancel: () => {},
    });
    batcher.push(1);
    batcher.clear();
    batcher.flush();
    expect(sink).not.toHaveBeenCalled();
  });
});
