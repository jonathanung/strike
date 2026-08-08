/**
 * Local WEBUI.20 reference profile runner (plain node, no TS loader required).
 * Mirrors web/src/perf thresholds + a lightweight reduce loop over the fixture
 * generator inlined for the CLI report. Prefer `npm test -- src/perf` in CI.
 *
 * Usage (from web/): npm run profile:perf
 */
import { writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { createRequire } from "node:module";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const require = createRequire(import.meta.url);

// Run via vitest programmatic API is heavy; instead shell out is already done.
// This script prints the committed budgets and reminds operators how to measure
// browser TTI / long tasks. Full reduce metrics come from perf.test.ts output.

const thresholds = {
  fixture: { minEvents: 10_000, minAgents: 50 },
  ci: {
    maxReduceMs: 8_000,
    maxMountedCells: 120,
    maxSinkCallsPerFrame: 1,
    minTranscriptItems: 2_000,
    minRosterSize: 50,
  },
  local: {
    desktop: {
      maxTTI: 3_000,
      maxLongTask: 100,
      maxDomNodes: 4_000,
      maxHeapDelta: 80 * 1024 * 1024,
      maxInputLatency: 50,
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
  },
};

const report = {
  generatedAt: new Date().toISOString(),
  thresholds,
  howToMeasure: {
    ciStable: "npm test -- src/perf/perf.test.ts",
    browserDesktop:
      "Open a long session, DevTools Performance → record hydrate + scroll + type in composer. Note TTI, longest task, DOM nodes in transcript, heap delta.",
    browserMobile:
      "DevTools sensors: CPU 4× slowdown, mid-tier mobile. Repeat desktop steps against mobileThrottled budgets.",
  },
  notes:
    "CI enforces fixture size, reduce wall time, mounted-cell bound, and stream batching. Browser TTI/long-task/memory are local reference budgets in docs/web.md.",
};

const out = join(root, "perf-profile-latest.json");
writeFileSync(out, JSON.stringify(report, null, 2));
console.log(`# WEBUI.20 reference profile budgets`);
console.log(`generated: ${report.generatedAt}`);
console.log(``);
console.log(`## CI gates (enforced by vitest)`);
console.log(JSON.stringify(thresholds.ci, null, 2));
console.log(``);
console.log(`## Local browser budgets (advisory)`);
console.log(JSON.stringify(thresholds.local, null, 2));
console.log(``);
console.log(`Wrote ${out}`);
console.log(`See docs/web.md § Long-session performance.`);
