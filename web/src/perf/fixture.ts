/**
 * Deterministic long-session / large-team performance fixture (WEBUI.20).
 * Generates ≥10_000 protocol envelopes with mixed streaming/tool/diff cells
 * and a 50-agent roster with task/attention updates.
 */
import type { Envelope } from "../types";
import type { DelegationTask, TeamMember, TeamObservation } from "../team";
import { emptyTeam } from "../team";
import { PERF_FIXTURE } from "./thresholds";

export type PerfFixture = {
  events: Envelope[];
  teamSnapshot: TeamObservation;
  meta: {
    eventCount: number;
    agentCount: number;
    streamDeltas: number;
    toolCells: number;
    diffCells: number;
  };
};

export type FixtureOptions = {
  events?: number;
  agents?: number;
  /** Seed for deterministic ids (default 1087). */
  seed?: number;
};

function mulberry32(seed: number) {
  let a = seed >>> 0;
  return () => {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const DIFF_SAMPLE = `--- a/src/example.ts
+++ b/src/example.ts
@@ -1,5 +1,7 @@
 export function hello() {
-  return "world";
+  // updated
+  return "strike";
 }
`;

const TOOL_OUTPUT = JSON.stringify({ path: "src/example.ts", lines: 120, ok: true }, null, 2);

/**
 * Build a deterministic fixture. Defaults satisfy PERF_FIXTURE minimums.
 */
export function buildPerfFixture(opts: FixtureOptions = {}): PerfFixture {
  const eventTarget = Math.max(opts.events ?? PERF_FIXTURE.minEvents, PERF_FIXTURE.minEvents);
  const agentCount = Math.max(opts.agents ?? PERF_FIXTURE.minAgents, PERF_FIXTURE.minAgents);
  const rand = mulberry32(opts.seed ?? 1087);

  const events: Envelope[] = [];
  let streamDeltas = 0;
  let toolCells = 0;
  let diffCells = 0;
  let t = 0;
  const tick = () => {
    t += 1;
    return `2026-01-01T00:00:${String(Math.floor(t / 1000)).padStart(2, "0")}.${String(t % 1000).padStart(3, "0")}Z`;
  };

  events.push({
    type: "status",
    time: tick(),
    data: { sessionId: "perf-root", provider: "echo", model: "echo", agent: "build", busy: true },
  });

  const members: Record<string, TeamMember> = {};
  const delegations: Record<string, DelegationTask> = {};
  for (let i = 0; i < agentCount; i++) {
    const sid = `agent-${String(i).padStart(3, "0")}`;
    const state = i % 7 === 0 ? "needs_attention" : i % 5 === 0 ? "blocked" : i % 3 === 0 ? "completed" : "running";
    members[sid] = {
      sessionId: sid,
      name: `Worker ${i}`,
      agent: i % 2 === 0 ? "build" : "explore",
      state,
      terminal: state === "completed",
      blockReason: state === "blocked" ? "waiting on review" : undefined,
      parentSessionId: i === 0 ? undefined : "agent-000",
      filesTouched: i % 4 === 0 ? [`src/f${i}.ts`] : undefined,
    };
    if (i % 2 === 0) {
      const tid = `task-${i}`;
      delegations[tid] = {
        id: tid,
        name: `Task ${i}`,
        state: state === "completed" ? "completed" : state === "blocked" ? "blocked" : "working",
        sessionId: sid,
        ownerSessionId: "agent-000",
      };
    }
  }
  const teamSnapshot: TeamObservation = {
    ...emptyTeam(),
    leadId: "agent-000",
    members,
    delegations,
    available: true,
  };

  events.push({
    type: "team.snapshot",
    time: tick(),
    data: teamSnapshot as unknown as Record<string, unknown>,
  });

  // Attention/task churn as roster + delegation updates (members[] wire shape).
  events.push({
    type: "team.roster",
    time: tick(),
    data: {
      leadId: "agent-000",
      members: Object.values(members).map((m) => ({ ...m })),
    },
  });
  for (let i = 0; i < agentCount; i++) {
    const sid = `agent-${String(i).padStart(3, "0")}`;
    if (i % 7 === 0) {
      events.push({
        type: "team.roster",
        time: tick(),
        data: {
          leadId: "agent-000",
          members: [{
            sessionId: sid,
            name: `Worker ${i}`,
            agent: i % 2 === 0 ? "build" : "explore",
            state: "needs_attention",
          }],
        },
      });
    }
    if (i % 5 === 0) {
      events.push({
        type: "delegation.changed",
        time: tick(),
        data: {
          id: `task-${i}`,
          name: `Task ${i}`,
          state: "working",
          sessionId: sid,
        },
      });
    }
  }

  let call = 0;
  while (events.length < eventTarget) {
    const roll = rand();
    const turn = `turn-${Math.floor(events.length / 20)}`;

    if (roll < 0.12) {
      events.push({
        type: "user.message",
        time: tick(),
        data: { text: `User prompt ${events.length}`, turnId: turn },
      });
    } else if (roll < 0.45) {
      const burst = 3 + Math.floor(rand() * 8);
      for (let b = 0; b < burst && events.length < eventTarget; b++) {
        events.push({
          type: "text.delta",
          time: tick(),
          data: { text: `tok${b} `, turnId: turn },
        });
        streamDeltas += 1;
      }
    } else if (roll < 0.55) {
      events.push({
        type: "reasoning.delta",
        time: tick(),
        data: { text: "thinking… ", turnId: turn },
      });
    } else if (roll < 0.75) {
      call += 1;
      const callId = `call-${call}`;
      const isDiff = rand() < 0.35;
      events.push({
        type: "tool.begin",
        time: tick(),
        data: {
          callId,
          name: isDiff ? "edit" : "read",
          args: { path: `src/f${call % 50}.ts` },
          turnId: turn,
        },
      });
      events.push({
        type: "tool.output",
        time: tick(),
        data: { callId, data: isDiff ? DIFF_SAMPLE : TOOL_OUTPUT, turnId: turn },
      });
      events.push({
        type: "tool.end",
        time: tick(),
        data: {
          callId,
          title: isDiff ? "edit" : "read",
          output: isDiff ? DIFF_SAMPLE : TOOL_OUTPUT,
          status: "done",
          durationMs: 12 + (call % 40),
          turnId: turn,
        },
      });
      toolCells += 1;
      if (isDiff) diffCells += 1;
    } else if (roll < 0.85) {
      events.push({
        type: "path.overlap",
        time: tick(),
        data: { path: `src/shared/${call % 10}.ts`, sessions: ["agent-001", "agent-002"] },
      });
    } else if (roll < 0.92) {
      const sid = `agent-${String(Math.floor(rand() * agentCount)).padStart(3, "0")}`;
      events.push({
        type: "agent.message",
        time: tick(),
        data: { from: sid, to: "agent-000", body: `status update ${events.length}` },
      });
    } else {
      events.push({
        type: "usage.reported",
        time: tick(),
        data: {
          input: { n: 100, known: true },
          output: { n: 50, known: true },
          source: "estimated",
        },
      });
    }
  }

  if (events.length > eventTarget) events.length = eventTarget;

  return {
    events,
    teamSnapshot,
    meta: {
      eventCount: events.length,
      agentCount,
      streamDeltas,
      toolCells,
      diffCells,
    },
  };
}
