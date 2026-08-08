import { describe, expect, it } from "vitest";
import {
  applyTeamSnapshot,
  emptyTeam,
  mergeMember,
  reduceTeamEvent,
} from "./team";
import { initialState, reduceClient, reduceEvent } from "./reducer";

describe("mergeMember", () => {
  it("does not revive terminal agents", () => {
    const terminal = mergeMember(undefined, {
      sessionId: "c1",
      state: "completed",
      name: "scout",
    });
    expect(terminal.terminal).toBe(true);
    const revived = mergeMember(terminal, {
      sessionId: "c1",
      state: "running",
      name: "scout",
    });
    expect(revived.state).toBe("completed");
    expect(revived.terminal).toBe(true);
  });
});

describe("reduceTeamEvent", () => {
  it("projects team.roster into a stable member model", () => {
    let team = emptyTeam();
    team = reduceTeamEvent(team, "team.roster", {
      leadId: "lead-1",
      members: [
        { sessionId: "lead-1", name: "lead", state: "running", role: "lead" },
        { sessionId: "c1", name: "scout", agent: "explore", state: "running", role: "member" },
      ],
    });
    expect(team.leadId).toBe("lead-1");
    expect(team.members["c1"]?.agent).toBe("explore");
    expect(team.members["c1"]?.state).toBe("running");
  });

  it("projects delegation, messages, artifacts, ledger, path overlap", () => {
    let team = emptyTeam();
    team = reduceTeamEvent(team, "delegation.changed", {
      id: "d1",
      state: "working",
      sessionId: "c1",
      version: 2,
    });
    team = reduceTeamEvent(team, "agent.message", {
      from: "lead-1",
      to: "c1",
      body: "hello",
      messageId: "m1",
      urgency: "high",
    }, "t1");
    team = reduceTeamEvent(team, "artifact.updated", {
      id: "a1",
      type: "findings",
      version: 3,
      op: "create",
      title: "bugs",
    });
    team = reduceTeamEvent(team, "ledger.updated", {
      id: "L1",
      kind: "decision",
      status: "active",
      op: "append",
      statement: "use postgres",
    });
    team = reduceTeamEvent(team, "path.overlap", {
      path: "src/a.go",
      sessions: ["c1", "c2"],
    }, "t2");
    expect(team.delegations.d1?.state).toBe("working");
    expect(team.messages[0]?.body).toBe("hello");
    expect(team.artifacts.a1?.version).toBe(3);
    expect(team.ledger.L1?.statement).toBe("use postgres");
    expect(team.pathOverlaps[0]?.path).toBe("src/a.go");
  });

  it("ignores unknown event types safely", () => {
    const team = reduceTeamEvent(emptyTeam(), "future.unknown.event", { foo: 1 });
    expect(team).toEqual(emptyTeam());
  });

  it("marks unavailable after team.unavailable", () => {
    const team = reduceTeamEvent(emptyTeam(), "team.unavailable", { reason: "dissolved" });
    expect(team.available).toBe(false);
    expect(team.unavailableReason).toMatch(/dissolved/);
  });
});

describe("applyTeamSnapshot", () => {
  it("loads late-join snapshot without claiming missing fields", () => {
    const snap = applyTeamSnapshot(emptyTeam(), {
      available: true,
      leadId: "L",
      members: {
        L: { sessionId: "L", state: "running", role: "lead" },
        c1: { sessionId: "c1", state: "completed", name: "done" },
      },
      delegations: { d1: { id: "d1", state: "done" } },
    });
    expect(snap.leadId).toBe("L");
    expect(snap.members.c1?.terminal).toBe(true);
    expect(snap.delegations.d1?.state).toBe("done");
  });

  it("handles null snapshot as unavailable", () => {
    const snap = applyTeamSnapshot(emptyTeam(), null);
    expect(snap.available).toBe(false);
  });
});

describe("reduceEvent team integration", () => {
  it("keeps children and team in sync on child lifecycle", () => {
    let state = initialState();
    state = reduceEvent(state, {
      type: "child.started",
      time: "t1",
      data: { sessionId: "c1", agent: "explore", name: "scout", prompt: "find bugs" },
    });
    expect(state.children.c1?.status).toBe("running");
    expect(state.team.members.c1?.state).toBe("running");
    state = reduceEvent(state, {
      type: "child.completed",
      time: "t2",
      data: {
        sessionId: "c1",
        status: "completed",
        summary: "done",
        handoff: { quality: "complete", summary: "done" },
        verification: { passed: true, claimed: true, verified: true, summary: "ok" },
      },
    });
    expect(state.children.c1?.quality).toBe("complete");
    expect(state.team.members.c1?.terminal).toBe(true);
    expect(state.team.verifications[0]?.passed).toBe(true);
  });

  it("isolates team state per root in client cache", () => {
    let client = reduceClient(
      { selectedID: "", byID: {} },
      { type: "client.ensure", id: "root-a" },
    );
    client = reduceClient(client, { type: "client.ensure", id: "root-b" });
    client = reduceClient(client, {
      type: "client.event",
      id: "root-a",
      envelope: {
        type: "team.roster",
        time: "t1",
        data: {
          leadId: "root-a",
          members: [{ sessionId: "root-a", state: "running", role: "lead" }],
        },
      },
    });
    client = reduceClient(client, {
      type: "client.event",
      id: "root-b",
      envelope: {
        type: "team.roster",
        time: "t2",
        data: {
          leadId: "root-b",
          members: [{ sessionId: "c-b", state: "running", name: "other" }],
        },
      },
    });
    expect(client.byID["root-a"].team.leadId).toBe("root-a");
    expect(client.byID["root-a"].team.members["c-b"]).toBeUndefined();
    expect(client.byID["root-b"].team.members["c-b"]?.name).toBe("other");
    expect(client.byID["root-b"].team.members["root-a"]).toBeUndefined();
  });

  it("dedupes duplicate envelopes via seen set", () => {
    let state = initialState();
    const env = {
      type: "delegation.changed",
      time: "same",
      data: { id: "d1", state: "working" },
    };
    state = reduceEvent(state, env);
    state = reduceEvent(state, env);
    expect(Object.keys(state.team.delegations)).toHaveLength(1);
  });
});
