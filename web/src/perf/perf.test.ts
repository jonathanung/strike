import { describe, expect, it } from "vitest";
import { reduceEvent, initialState } from "../reducer";
import { createStreamBatcher } from "../streamBatch";
import { estimateMountedCount } from "../VirtualList";
import { buildPerfFixture } from "./fixture";
import { runReferenceProfile } from "./profile";
import { PERF_FIXTURE, PERF_THRESHOLDS_CI } from "./thresholds";

describe("WEBUI.20 performance fixture (CI stable subset)", () => {
  it("covers ≥10k events, mixed cells, and a 50-agent roster", () => {
    const fixture = buildPerfFixture();
    expect(fixture.meta.eventCount).toBeGreaterThanOrEqual(PERF_FIXTURE.minEvents);
    expect(fixture.meta.agentCount).toBeGreaterThanOrEqual(PERF_FIXTURE.minAgents);
    expect(fixture.meta.streamDeltas).toBeGreaterThan(100);
    expect(fixture.meta.toolCells).toBeGreaterThan(50);
    expect(fixture.meta.diffCells).toBeGreaterThan(10);
    expect(Object.keys(fixture.teamSnapshot.members).length).toBeGreaterThanOrEqual(PERF_FIXTURE.minAgents);
  });

  it("reduces the full fixture under the committed threshold and keeps data complete", () => {
    const fixture = buildPerfFixture();
    const t0 = performance.now();
    let state = initialState();
    for (const env of fixture.events) {
      state = reduceEvent(state, env);
    }
    const ms = performance.now() - t0;
    expect(ms).toBeLessThanOrEqual(PERF_THRESHOLDS_CI.maxReduceMs);
    expect(state.items.length).toBeGreaterThanOrEqual(PERF_THRESHOLDS_CI.minTranscriptItems);
    expect(Object.keys(state.team.members).length).toBeGreaterThanOrEqual(PERF_THRESHOLDS_CI.minRosterSize);
    // Complete in-memory data retained (not windowed at the store layer).
    expect(state.items.length).toBeGreaterThan(1000);
  });

  it("keeps mounted transcript DOM rows bounded for long sessions", () => {
    const fixture = buildPerfFixture();
    let state = initialState();
    for (const env of fixture.events) state = reduceEvent(state, env);
    const mounted = estimateMountedCount(
      state.items.length,
      800,
      96,
      6,
      PERF_THRESHOLDS_CI.maxMountedCells,
    );
    expect(mounted).toBeLessThanOrEqual(PERF_THRESHOLDS_CI.maxMountedCells);
    expect(mounted).toBeLessThan(state.items.length);
  });

  it("batches stream deltas to at most one sink call per animation frame", () => {
    const frames: Array<() => void> = [];
    const sinkCalls: number[][] = [];
    const batcher = createStreamBatcher<number>((batch) => sinkCalls.push(batch), {
      schedule: (cb) => {
        frames.push(cb);
        return frames.length;
      },
      cancel: () => {},
    });

    // Sustained input across 5 frames, 20 deltas each.
    for (let f = 0; f < 5; f++) {
      for (let i = 0; i < 20; i++) batcher.push(f * 20 + i);
      expect(frames.length).toBe(f + 1);
      frames[f]();
      expect(sinkCalls).toHaveLength(f + 1);
      expect(sinkCalls[f]).toHaveLength(20);
    }
    // One sink invocation per frame.
    expect(sinkCalls.length).toBe(frames.length);
    expect(PERF_THRESHOLDS_CI.maxSinkCallsPerFrame).toBe(1);
  });

  it("reference profile report meets CI gates", () => {
    const report = runReferenceProfile();
    expect(report.ci.reduceOk).toBe(true);
    expect(report.ci.mountOk).toBe(true);
    expect(report.fixture.events).toBeGreaterThanOrEqual(PERF_FIXTURE.minEvents);
    expect(report.rosterSize).toBeGreaterThanOrEqual(PERF_FIXTURE.minAgents);
  });
});
