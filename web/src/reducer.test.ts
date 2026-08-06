import { describe, expect, it } from "vitest";
import { emptySlice, initialClientState, initialState, reduceClient, reduceEvent, selectedSlice } from "./reducer";

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

  it("ignores unknown harness extension event types without crashing", () => {
    const base = reduceEvent(initialState(), { type: "turn.started", data: { turnId: "t" } });
    const next = reduceEvent(base, {
      type: "harness.future_gate",
      time: "9",
      data: { name: "lint", passed: true },
    });
    expect(next.status.busy).toBe(true);
    expect(next.items).toEqual(base.items);
    expect(next.seen.size).toBe(base.seen.size + 1);
  });
});

describe("reduceClient workspace isolation", () => {
  it("routes events only to the target workspace id", () => {
    let state = initialClientState();
    state = reduceClient(state, { type: "client.ensure", id: "A" });
    state = reduceClient(state, { type: "client.ensure", id: "B" });
    state = reduceClient(state, {
      type: "client.event", id: "A",
      envelope: { type: "user.message", time: "1", data: { text: "from A", turnId: "ta" } },
    });
    state = reduceClient(state, {
      type: "client.event", id: "B",
      envelope: { type: "user.message", time: "2", data: { text: "from B", turnId: "tb" } },
    });
    state = reduceClient(state, {
      type: "client.event", id: "A",
      envelope: { type: "text.delta", time: "3", data: { turnId: "ta", text: "reply-A" } },
    });

    expect(state.byID.A.items.map((i) => i.text)).toEqual(["from A", "reply-A"]);
    expect(state.byID.B.items.map((i) => i.text)).toEqual(["from B"]);
    expect(state.byID.A.items.some((i) => i.text.includes("B"))).toBe(false);
    expect(state.byID.B.items.some((i) => i.text.includes("A"))).toBe(false);
  });

  it("preserves drafts and queues per workspace across select", () => {
    let state = initialClientState();
    state = reduceClient(state, { type: "client.ensure", id: "A" });
    state = reduceClient(state, {
      type: "client.composer", id: "A",
      patch: { draft: "draft-A", queue: [{ text: "queued-A", images: [] }] },
    });
    state = reduceClient(state, { type: "client.ensure", id: "B" });
    state = reduceClient(state, {
      type: "client.composer", id: "B",
      patch: { draft: "draft-B" },
    });
    expect(state.selectedID).toBe("B");
    expect(selectedSlice(state).draft).toBe("draft-B");

    state = reduceClient(state, { type: "client.select", id: "A" });
    expect(selectedSlice(state).draft).toBe("draft-A");
    expect(selectedSlice(state).queue).toEqual([{ text: "queued-A", images: [] }]);
    expect(state.byID.B.draft).toBe("draft-B");
  });

  it("keeps permission/question state scoped and does not cross-clear", () => {
    let state = initialClientState();
    state = reduceClient(state, { type: "client.ensure", id: "A" });
    state = reduceClient(state, { type: "client.ensure", id: "B" });
    state = reduceClient(state, {
      type: "client.event", id: "A",
      envelope: { type: "permission.asked", data: { requestId: "pa", tool: "bash" } },
    });
    state = reduceClient(state, {
      type: "client.event", id: "B",
      envelope: { type: "question.asked", data: { requestId: "qb", question: "Mode?" } },
    });
    expect(state.byID.A.permission?.requestId).toBe("pa");
    expect(state.byID.B.question?.requestId).toBe("qb");
    expect(state.byID.B.permission).toBeUndefined();
    expect(state.byID.A.question).toBeUndefined();

    state = reduceClient(state, {
      type: "client.event", id: "B",
      envelope: { type: "permission.resolved", data: { requestId: "pa" } },
    });
    expect(state.byID.A.permission?.requestId).toBe("pa");
  });

  it("preserves composer when a workspace transcript is reset", () => {
    let state = reduceClient(initialClientState(), { type: "client.ensure", id: "A" });
    state = reduceClient(state, {
      type: "client.composer", id: "A",
      patch: { draft: "keep-me", queue: [{ text: "q", images: [] }], fast: true },
    });
    state = reduceClient(state, {
      type: "client.event", id: "A",
      envelope: { type: "user.message", data: { text: "hi" } },
    });
    state = reduceClient(state, { type: "client.reset", id: "A" });
    expect(state.byID.A.items).toHaveLength(0);
    expect(state.byID.A.draft).toBe("keep-me");
    expect(state.byID.A.queue).toEqual([{ text: "q", images: [] }]);
    expect(state.byID.A.fast).toBe(true);
  });

  it("drops a workspace without touching peers", () => {
    let state = initialClientState();
    state = reduceClient(state, { type: "client.ensure", id: "A" });
    state = reduceClient(state, { type: "client.ensure", id: "B" });
    state = reduceClient(state, { type: "client.drop", id: "A" });
    expect(state.byID.A).toBeUndefined();
    expect(state.byID.B).toEqual(emptySlice("B"));
    expect(state.selectedID).toBe("B");
  });
});
