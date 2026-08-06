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

  it("appends local.system notices without touching seen", () => {
    const base = initialState();
    const next = reduceEvent(base, {
      type: "local.system",
      time: "1",
      data: { title: "Help", text: "/export downloads markdown" },
    });
    expect(next.items).toHaveLength(1);
    expect(next.items[0]).toMatchObject({ kind: "system", title: "Help", text: "/export downloads markdown" });
    expect(next.seen.size).toBe(0);
  });

  it("stacks undo preview from turn.completed and pops on session.rewound", () => {
    let state = reduceEvent(initialState(), {
      type: "turn.completed",
      time: "1",
      data: {
        files: [{ path: "a.go", kind: "update" }],
        checkpointSkipped: 1,
        uncovered: ["bash"],
      },
    });
    expect(state.status.busy).toBe(false);
    expect(state.undoStack).toHaveLength(1);
    expect(state.undoStack[0]).toEqual({
      files: [{ path: "a.go", kind: "update" }],
      skipped: 1,
      uncovered: ["bash"],
    });

    state = reduceEvent(state, {
      type: "turn.completed",
      time: "2",
      data: { files: [{ path: "b.go", kind: "create" }] },
    });
    expect(state.undoStack).toHaveLength(2);
    expect(state.undoStack.at(-1)?.files[0]?.path).toBe("b.go");

    state = reduceEvent(state, { type: "session.rewound", time: "3", data: { removed: 2 } });
    expect(state.undoStack).toHaveLength(1);
    expect(state.undoStack[0].files[0]?.path).toBe("a.go");

    state = reduceEvent(state, { type: "session.rewound", time: "4", data: {} });
    expect(state.undoStack).toHaveLength(0);
    // Extra pop is a no-op.
    state = reduceEvent(state, { type: "session.rewound", time: "5", data: {} });
    expect(state.undoStack).toHaveLength(0);
  });

  it("ignores child-lineage turn.completed for undo stack", () => {
    let state = reduceEvent(initialState(), {
      type: "turn.completed",
      time: "1",
      data: { parentSessionId: "parent", files: [{ path: "child.go", kind: "update" }] },
    });
    expect(state.undoStack).toHaveLength(0);
    state = reduceEvent(state, {
      type: "turn.completed",
      time: "2",
      data: { depth: 1, files: [{ path: "nested.go", kind: "update" }] },
    });
    expect(state.undoStack).toHaveLength(0);
  });

  it("clears undo stack on workspace reset", () => {
    let state = reduceEvent(initialState(), {
      type: "turn.completed",
      time: "1",
      data: { files: [{ path: "a.go", kind: "update" }] },
    });
    state = reduceEvent(state, { type: "workspace.reset", data: { sessionId: "other" } });
    expect(state.undoStack).toEqual([]);
    expect(state.status.sessionId).toBe("other");
  });

  it("tracks child start, handoff quality, budget, and escalation fields", () => {
    let state = reduceEvent(initialState(), {
      type: "child.started", time: "1",
      data: { sessionId: "c1", agent: "explore", name: "scout", prompt: "find the bug" },
    });
    expect(state.children.c1).toMatchObject({ agent: "explore", name: "scout", status: "running", prompt: "find the bug" });
    state = reduceEvent(state, {
      type: "child.escalated", time: "2",
      data: { sessionId: "c1", kind: "tokens", reason: "token budget exhausted", action: "finalizing" },
    });
    expect(state.children.c1).toMatchObject({ status: "finalizing", escalateKind: "tokens", budgetKind: "tokens" });
    state = reduceEvent(state, {
      type: "child.completed", time: "3",
      data: { sessionId: "c1", status: "completed", summary: "top-level summary", budgetKind: "tokens", finalization: "succeeded", handoff: { summary: "handoff body", quality: "partial" } },
    });
    expect(state.children.c1).toMatchObject({ status: "completed", summary: "top-level summary", quality: "partial", finalization: "succeeded" });
  });

  it("seeds children from the sessions children API without clobbering events", () => {
    let state = reduceEvent(initialState(), {
      type: "child.started", time: "1", data: { sessionId: "c1", agent: "explore", prompt: "keep me" },
    });
    state = reduceEvent(state, {
      type: "children.seed", time: "seed",
      data: { sessions: [{ ID: "c1", Title: "should-not-overwrite", Open: false }, { id: "c2", title: "from-api", open: true }] },
    });
    expect(state.children.c1).toMatchObject({ agent: "explore", status: "running", prompt: "keep me" });
    expect(state.children.c2).toMatchObject({ agent: "from-api", status: "running" });
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
