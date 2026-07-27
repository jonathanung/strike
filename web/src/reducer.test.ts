import { describe, expect, it } from "vitest";
import { initialState, reduceEvent } from "./reducer";

describe("reduceEvent", () => {
  it("merges streamed text and deduplicates replayed envelopes", () => {
    const first = { type: "text.delta", time: "1", data: { turnId: "t", text: "hello " } };
    let state = reduceEvent(initialState(), first);
    state = reduceEvent(state, { type: "text.delta", time: "2", data: { turnId: "t", text: "world" } });
    state = reduceEvent(state, first);
    expect(state.items).toHaveLength(1);
    expect(state.items[0].text).toBe("hello world");
  });

  it("keeps blocking requests associated by request id", () => {
    let state = reduceEvent(initialState(), { type: "permission.asked", data: { requestId: "one" } });
    state = reduceEvent(state, { type: "permission.resolved", data: { requestId: "other" } });
    expect(state.permission?.requestId).toBe("one");
    state = reduceEvent(state, { type: "permission.resolved", data: { requestId: "one" } });
    expect(state.permission).toBeUndefined();
  });

  it("does not present unknown context as zero", () => {
    const state = reduceEvent(initialState(), { type: "status", data: { provider: "echo" } });
    expect(state.status.contextUsed).toBeUndefined();
    expect(state.status.contextLimit).toBeUndefined();
  });
});
