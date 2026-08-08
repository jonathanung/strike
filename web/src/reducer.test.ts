import { describe, expect, it } from "vitest";
import { emptySlice, initialClientState, initialState, reduceClient, reduceEvent, selectedSlice, applyUsageReported, tokenCount, setAdd, setRemove } from "./reducer";

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

describe("usage.reported accumulation", () => {

  it("records autonomy.selected on status for set.autonomy confirmations", () => {
    const state = reduceEvent(initialState(), { type: "autonomy.selected", data: { mode: "skip-all" } });
    expect(state.status.autonomy).toBe("skip-all");
  });

  it("accumulates known usage.reported parts and updates context used", () => {
    let state = reduceEvent(initialState(), {
      type: "usage.reported",
      time: "1",
      data: {
        input: { n: 100, known: true },
        output: { n: 40, known: true },
        cacheRead: { known: false },
        used: { n: 140, known: true },
        source: "actual",
      },
    });
    state = reduceEvent(state, {
      type: "usage.reported",
      time: "2",
      data: {
        input: { n: 50, known: true },
        output: { known: false },
        used: { n: 200, known: true },
        source: "estimated",
      },
    });
    expect(state.status.usageReports).toBe(2);
    expect(state.status.inputTokens).toBe(150);
    expect(state.status.outputTokens).toBe(40);
    expect(state.status.contextUsed).toBe(200);
    expect(state.status.usageSource).toBe("mixed (actual + estimated)");
  });

  it("merges chrome status without wiping client usage totals", () => {
    let state = reduceEvent(initialState(), {
      type: "usage.reported",
      data: { input: { n: 10, known: true }, output: { n: 2, known: true }, source: "actual" },
    });
    state = reduceEvent(state, { type: "status", data: { provider: "echo", busy: false } });
    expect(state.status.provider).toBe("echo");
    expect(state.status.inputTokens).toBe(10);
    expect(state.status.outputTokens).toBe(2);
  });

  it("stores prompt.effective layers, attribution, and control sets", () => {
    const state = reduceEvent(initialState(), {
      type: "prompt.effective",
      time: "2",
      data: {
        fromLastStream: true,
        systemChars: 400,
        messageCount: 3,
        layers: [
          { kind: "shared", source: "builtin:shared", mode: "append", chars: 100, estTokens: 25, pinned: true },
          { kind: "persona", source: "agent:build", mode: "replace", chars: 300, estTokens: 75 },
        ],
        attribution: {
          system: { n: 100, known: true },
          tools: { n: 50, known: true },
          messages: { n: 200, known: true },
          toolResults: { n: 10, known: true },
          total: { n: 360, known: true },
          source: "estimated",
        },
        pinnedKinds: ["shared"],
        excludedKinds: ["project_memory"],
        shedKinds: ["lean_code"],
      },
    });
    expect(state.promptScope).toBe("last");
    expect(state.systemChars).toBe(400);
    expect(state.messageCount).toBe(3);
    expect(state.layers).toHaveLength(2);
    expect(state.layers[0].kind).toBe("shared");
    expect(state.layers[0].pinned).toBe(true);
    expect(state.attribution?.total?.n).toBe(360);
    expect(state.attribution?.source).toBe("estimated");
    expect(state.pinnedKinds).toEqual(["shared"]);
    expect(state.excludedKinds).toEqual(["project_memory"]);
    expect(state.shedKinds).toEqual(["lean_code"]);
    expect(state.status.contextUsed).toBe(360);
  });

  it("applies context.controls to pin/exclude sets and layer pin flags", () => {
    let state = reduceEvent(initialState(), {
      type: "prompt.effective",
      time: "1",
      data: {
        layers: [{ kind: "persona", source: "agent:build", mode: "replace", chars: 10 }],
        pinnedKinds: [],
        excludedKinds: [],
      },
    });
    state = reduceEvent(state, {
      type: "context.controls",
      time: "2",
      data: { pinnedKinds: ["persona"], excludedKinds: ["project_memory"] },
    });
    expect(state.pinnedKinds).toEqual(["persona"]);
    expect(state.excludedKinds).toEqual(["project_memory"]);
    expect(state.layers[0].pinned).toBe(true);
  });

  it("surfaces context.fit_warning and updates limit", () => {
    const state = reduceEvent(initialState(), {
      type: "context.fit_warning",
      time: "3",
      data: {
        level: "critical",
        message: "projected prompt ~180k tok is ≥80% of the 200k context window",
        estimatedTokens: 180_000,
        contextLimit: 200_000,
        source: "estimated",
      },
    });
    expect(state.fitWarning?.level).toBe("critical");
    expect(state.fitWarning?.message).toContain("180k");
    expect(state.status.contextLimit).toBe(200_000);
    expect(state.status.contextUsed).toBe(180_000);
  });

  it("clears fit warning when a new turn starts", () => {
    let state = reduceEvent(initialState(), {
      type: "context.fit_warning",
      time: "1",
      data: { level: "warn", message: "hot", contextLimit: 100 },
    });
    expect(state.fitWarning).toBeDefined();
    state = reduceEvent(state, { type: "turn.started", time: "2", data: { turnId: "t2" } });
    expect(state.fitWarning).toBeUndefined();
    expect(state.status.busy).toBe(true);
    expect(state.status.contextLimit).toBe(100);
  });

  it("clears context doctor state on workspace.reset", () => {
    let state = reduceEvent(initialState(), {
      type: "context.fit_warning",
      time: "1",
      data: { level: "warn", message: "hot", contextLimit: 100 },
    });
    state = reduceEvent(state, {
      type: "prompt.effective",
      time: "2",
      data: { layers: [{ kind: "shared", chars: 1 }], pinnedKinds: ["shared"] },
    });
    state = reduceEvent(state, { type: "workspace.reset", data: { sessionId: "next" } });
    expect(state.fitWarning).toBeUndefined();
    expect(state.layers).toEqual([]);
    expect(state.pinnedKinds).toEqual([]);
    expect(state.status.sessionId).toBe("next");
  });

});

describe("set helpers", () => {
  it("adds and removes without duplicates", () => {
    expect(setAdd(["a"], "a")).toEqual(["a"]);
    expect(setAdd(["a"], "b")).toEqual(["a", "b"]);
    expect(setRemove(["a", "b"], "a")).toEqual(["b"]);
    expect(setRemove(["a"], "z")).toEqual(["a"]);
  });
});

describe("tokenCount / applyUsageReported", () => {
  it("treats unknown token parts as absent", () => {
    expect(tokenCount(undefined)).toEqual({ n: 0, known: false });
    expect(tokenCount({ known: false })).toEqual({ n: 0, known: false });
    expect(tokenCount({ n: 0, known: true })).toEqual({ n: 0, known: true });
    const status = applyUsageReported({}, { input: { known: false }, output: { n: 0, known: true } });
    expect(status.inputTokens).toBeUndefined();
    expect(status.outputTokens).toBe(0);
    expect(status.usageReports).toBe(1);
  });
});

describe("client.events batching", () => {
  it("applies many envelopes in one reduce without dropping order", () => {
    let state = reduceClient(initialClientState(), { type: "client.ensure", id: "A" });
    state = reduceClient(state, {
      type: "client.events",
      id: "A",
      envelopes: [
        { type: "user.message", time: "1", data: { text: "hi", turnId: "t1" } },
        { type: "text.delta", time: "2", data: { text: "Hel", turnId: "t1" } },
        { type: "text.delta", time: "3", data: { text: "lo", turnId: "t1" } },
        { type: "team.roster", time: "4", data: { members: [{ sessionId: "c1", name: "worker", state: "running" }] } },
      ],
    });
    const slice = selectedSlice(state);
    expect(slice.items.some((i) => i.kind === "user" && i.text === "hi")).toBe(true);
    const assistant = slice.items.find((i) => i.kind === "assistant");
    expect(assistant?.text).toBe("Hello");
    expect(slice.team.members["c1"]?.name).toBe("worker");
  });

  it("team-only batch keeps items reference stable when no transcript events", () => {
    let state = reduceClient(initialClientState(), { type: "client.ensure", id: "A" });
    state = reduceClient(state, {
      type: "client.event",
      id: "A",
      envelope: { type: "user.message", time: "1", data: { text: "x", turnId: "t" } },
    });
    const before = selectedSlice(state).items;
    state = reduceClient(state, {
      type: "client.events",
      id: "A",
      envelopes: [
        { type: "team.roster", time: "2", data: { members: [{ sessionId: "c2", name: "w2", state: "running" }] } },
      ],
    });
    const after = selectedSlice(state).items;
    expect(after).toBe(before);
    expect(selectedSlice(state).team.members["c2"]?.name).toBe("w2");
  });
});
