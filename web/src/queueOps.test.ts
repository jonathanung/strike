import { describe, expect, it } from "vitest";
import { clearQueue, editQueuedText, moveQueuedAt, removeQueuedAt, type QueuedPrompt } from "./queueOps";

const sample = (): QueuedPrompt[] => [
  { text: "one", images: [] },
  { text: "two", images: [{ name: "a.png", mime: "image/png", data: "x" }] },
  { text: "three", images: [] },
];

describe("queueOps", () => {
  it("removes by index", () => {
    expect(removeQueuedAt(sample(), 1).map((q) => q.text)).toEqual(["one", "three"]);
    expect(removeQueuedAt(sample(), 99)).toEqual(sample());
  });

  it("reorders with moveQueuedAt", () => {
    expect(moveQueuedAt(sample(), 0, 1).map((q) => q.text)).toEqual(["two", "one", "three"]);
    expect(moveQueuedAt(sample(), 2, -1).map((q) => q.text)).toEqual(["one", "three", "two"]);
    expect(moveQueuedAt(sample(), 0, -1)).toEqual(sample());
    expect(moveQueuedAt(sample(), 2, 1)).toEqual(sample());
  });

  it("edits text while keeping images", () => {
    const next = editQueuedText(sample(), 1, "two-b");
    expect(next[1]?.text).toBe("two-b");
    expect(next[1]?.images).toHaveLength(1);
    expect(editQueuedText(sample(), -1, "x")).toEqual(sample());
  });

  it("clears the queue", () => {
    expect(clearQueue()).toEqual([]);
  });
});
