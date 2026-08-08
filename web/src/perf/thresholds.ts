/**
 * Committed performance regression thresholds for WEBUI.20 (#1087).
 *
 * CI runs the stable subset in perf.test.ts against these numbers.
 * Full browser profiling (TTI, longest task, memory) is local via
 * `npm run profile:perf` — see docs/web.md § Long-session performance.
 */

export const PERF_FIXTURE = {
  /** Minimum protocol events in the deterministic fixture. */
  minEvents: 10_000,
  /** Minimum agents in the large-team roster. */
  minAgents: 50,
  /** Mixed cell kinds required in the fixture. */
  requiredKinds: ["user", "assistant", "tool", "diff", "reasoning", "system"] as const,
} as const;

/** Stable CI gates (jsdom / node). Times are wall-clock on a modest CI runner. */
export const PERF_THRESHOLDS_CI = {
  /** reduceEvent over the full fixture must finish under this many ms. */
  maxReduceMs: 8_000,
  /** Virtual list must mount at most this many DOM rows for the fixture viewport. */
  maxMountedCells: 120,
  /** Stream batcher under sustained push must average ≤ 1 sink call per frame. */
  maxSinkCallsPerFrame: 1,
  /** After reduce, in-memory transcript items must remain complete (not truncated). */
  minTranscriptItems: 2_000,
  /** Team roster size after fixture apply. */
  minRosterSize: 50,
} as const;

/**
 * Local reference profile budgets (desktop + throttled mobile guidance).
 * Recorded by `npm run profile:perf`; not enforced in CI (environment variance).
 */
export const PERF_THRESHOLDS_LOCAL = {
  desktop: {
    /** Time-to-interactive after fixture hydrate (ms). */
    maxTTI: 3_000,
    /** Longest main-thread task while streaming (ms). */
    maxLongTask: 100,
    /** Approximate DOM node count inside the transcript scroller. */
    maxDomNodes: 4_000,
    /** Heap used growth over a 30s stream sample (bytes); advisory. */
    maxHeapDelta: 80 * 1024 * 1024,
    /** Composer keystroke → paint responsiveness target (ms). */
    maxInputLatency: 50,
    /** Scroll event handler budget (ms). */
    maxScrollHandler: 16,
  },
  mobileThrottled: {
    maxTTI: 6_000,
    maxLongTask: 200,
    maxDomNodes: 4_000,
    maxHeapDelta: 100 * 1024 * 1024,
    maxInputLatency: 100,
    maxScrollHandler: 32,
  },
} as const;

export type PerfProfileSample = {
  profile: "desktop" | "mobileThrottled";
  ttiMs?: number;
  longestTaskMs?: number;
  domNodes?: number;
  heapDeltaBytes?: number;
  inputLatencyMs?: number;
  scrollHandlerMs?: number;
  mountedCells?: number;
  totalItems?: number;
  reduceMs?: number;
  notes?: string;
};

export function evaluateLocalProfile(sample: PerfProfileSample): { ok: boolean; failures: string[] } {
  const budget = PERF_THRESHOLDS_LOCAL[sample.profile];
  const failures: string[] = [];
  const check = (label: string, value: number | undefined, max: number) => {
    if (value === undefined) return;
    if (value > max) failures.push(`${label}=${value} exceeds ${max}`);
  };
  check("ttiMs", sample.ttiMs, budget.maxTTI);
  check("longestTaskMs", sample.longestTaskMs, budget.maxLongTask);
  check("domNodes", sample.domNodes, budget.maxDomNodes);
  check("heapDeltaBytes", sample.heapDeltaBytes, budget.maxHeapDelta);
  check("inputLatencyMs", sample.inputLatencyMs, budget.maxInputLatency);
  check("scrollHandlerMs", sample.scrollHandlerMs, budget.maxScrollHandler);
  check("mountedCells", sample.mountedCells, PERF_THRESHOLDS_CI.maxMountedCells);
  return { ok: failures.length === 0, failures };
}
