/**
 * Local profiling helpers for WEBUI.20. Safe in node/jsdom; browser fields
 * fill in when Performance APIs exist.
 */
import { reduceEvent, initialState } from "../reducer";
import { estimateMountedCount } from "../VirtualList";
import { buildPerfFixture } from "./fixture";
import {
  evaluateLocalProfile,
  PERF_THRESHOLDS_CI,
  PERF_THRESHOLDS_LOCAL,
  type PerfProfileSample,
} from "./thresholds";

export type ProfileReport = {
  generatedAt: string;
  fixture: {
    events: number;
    agents: number;
    streamDeltas: number;
    toolCells: number;
    diffCells: number;
  };
  reduceMs: number;
  transcriptItems: number;
  rosterSize: number;
  mountedCellsDesktop: number;
  mountedCellsMobile: number;
  samples: PerfProfileSample[];
  ci: {
    maxReduceMs: number;
    maxMountedCells: number;
    reduceOk: boolean;
    mountOk: boolean;
  };
  localBudgets: typeof PERF_THRESHOLDS_LOCAL;
};

function nowMs(): number {
  if (typeof performance !== "undefined" && typeof performance.now === "function") {
    return performance.now();
  }
  return Date.now();
}

/** Run the deterministic fixture through the reducer and estimate DOM cost. */
export function runReferenceProfile(): ProfileReport {
  const fixture = buildPerfFixture();
  const t0 = nowMs();
  let state = initialState();
  for (const env of fixture.events) {
    state = reduceEvent(state, env);
  }
  const reduceMs = nowMs() - t0;
  const rosterSize = Object.keys(state.team?.members || {}).length;
  const mountedDesktop = estimateMountedCount(
    state.items.length,
    800,
    96,
    6,
    PERF_THRESHOLDS_CI.maxMountedCells,
  );
  const mountedMobile = estimateMountedCount(
    state.items.length,
    560,
    96,
    6,
    PERF_THRESHOLDS_CI.maxMountedCells,
  );

  const desktopSample: PerfProfileSample = {
    profile: "desktop",
    reduceMs,
    totalItems: state.items.length,
    mountedCells: mountedDesktop,
    domNodes: mountedDesktop * 12, // rough nodes-per-cell estimate
    notes: "jsdom/node reference — browser TTI/long-task filled by manual DevTools run",
  };
  const mobileSample: PerfProfileSample = {
    profile: "mobileThrottled",
    reduceMs,
    totalItems: state.items.length,
    mountedCells: mountedMobile,
    domNodes: mountedMobile * 12,
    notes: "throttled-mobile guidance; measure with CPU 4× + mid-tier network in DevTools",
  };

  return {
    generatedAt: new Date().toISOString(),
    fixture: {
      events: fixture.meta.eventCount,
      agents: fixture.meta.agentCount,
      streamDeltas: fixture.meta.streamDeltas,
      toolCells: fixture.meta.toolCells,
      diffCells: fixture.meta.diffCells,
    },
    reduceMs,
    transcriptItems: state.items.length,
    rosterSize,
    mountedCellsDesktop: mountedDesktop,
    mountedCellsMobile: mountedMobile,
    samples: [desktopSample, mobileSample],
    ci: {
      maxReduceMs: PERF_THRESHOLDS_CI.maxReduceMs,
      maxMountedCells: PERF_THRESHOLDS_CI.maxMountedCells,
      reduceOk: reduceMs <= PERF_THRESHOLDS_CI.maxReduceMs,
      mountOk:
        mountedDesktop <= PERF_THRESHOLDS_CI.maxMountedCells &&
        mountedMobile <= PERF_THRESHOLDS_CI.maxMountedCells,
    },
    localBudgets: PERF_THRESHOLDS_LOCAL,
  };
}

export function formatProfileReport(report: ProfileReport): string {
  const lines = [
    `# WEBUI.20 reference profile`,
    `generated: ${report.generatedAt}`,
    ``,
    `## Fixture`,
    `- events: ${report.fixture.events}`,
    `- agents: ${report.fixture.agents}`,
    `- stream deltas: ${report.fixture.streamDeltas}`,
    `- tool cells: ${report.fixture.toolCells}`,
    `- diff cells: ${report.fixture.diffCells}`,
    ``,
    `## Measured (local/CI-stable)`,
    `- reduceMs: ${report.reduceMs.toFixed(1)} (budget ${report.ci.maxReduceMs}) ${report.ci.reduceOk ? "OK" : "FAIL"}`,
    `- transcriptItems: ${report.transcriptItems}`,
    `- rosterSize: ${report.rosterSize}`,
    `- mountedCells desktop/mobile: ${report.mountedCellsDesktop}/${report.mountedCellsMobile} (budget ${report.ci.maxMountedCells}) ${report.ci.mountOk ? "OK" : "FAIL"}`,
    ``,
    `## Local browser budgets (advisory)`,
    `- desktop TTI≤${PERF_THRESHOLDS_LOCAL.desktop.maxTTI}ms longTask≤${PERF_THRESHOLDS_LOCAL.desktop.maxLongTask}ms dom≤${PERF_THRESHOLDS_LOCAL.desktop.maxDomNodes} input≤${PERF_THRESHOLDS_LOCAL.desktop.maxInputLatency}ms`,
    `- mobile  TTI≤${PERF_THRESHOLDS_LOCAL.mobileThrottled.maxTTI}ms longTask≤${PERF_THRESHOLDS_LOCAL.mobileThrottled.maxLongTask}ms dom≤${PERF_THRESHOLDS_LOCAL.mobileThrottled.maxDomNodes} input≤${PERF_THRESHOLDS_LOCAL.mobileThrottled.maxInputLatency}ms`,
    ``,
    `## Sample evaluation`,
  ];
  for (const sample of report.samples) {
    const ev = evaluateLocalProfile(sample);
    lines.push(`- ${sample.profile}: ${ev.ok ? "OK" : "FAIL"} ${ev.failures.join("; ")}`);
  }
  lines.push(``);
  lines.push(`See docs/web.md § Long-session performance.`);
  return lines.join("\n");
}
