import assert from "node:assert/strict";
import test from "node:test";

import { SCAN_EXPR, focusReport, scanMarkdown, summarize } from "./a11y.mjs";

test("focusReport tolerates a missing dialog probe", () => {
  assert.deepEqual(focusReport(null), { inside: false, first: false, last: false, count: 0 });
  assert.deepEqual(focusReport({ inside: true, first: true, last: false, count: 3 }), {
    inside: true,
    first: true,
    last: false,
    count: 3,
  });
});

test("summarize counts findings by rule across surfaces", () => {
  const cases = [
    { name: "empty", surfaces: [], want: { total: 0, byRule: {} } },
    { name: "no findings", surfaces: [{ name: "chat", findings: [] }], want: { total: 0, byRule: {} } },
    {
      name: "mixed",
      surfaces: [
        { name: "chat", findings: [{ rule: "control-name" }, { rule: "duplicate-id" }] },
        { name: "palette", findings: [{ rule: "control-name" }] },
      ],
      want: { total: 3, byRule: { "control-name": 2, "duplicate-id": 1 } },
    },
  ];
  for (const c of cases) assert.deepEqual(summarize(c.surfaces), c.want, c.name);
});

test("scanMarkdown states warn mode and lists each surface", () => {
  const md = scanMarkdown([
    { name: "chat", findings: [{ rule: "field-label", impact: "serious", target: "input.x", help: "No label." }] },
    { name: "command palette", findings: [] },
  ]);
  assert.match(md, /warn mode/i);
  assert.match(md, /\| field-label \| 1 \|/);
  assert.match(md, /## chat \(1\)/);
  assert.match(md, /## command palette \(0\)/);
  assert.match(md, /`input\.x`/);
});

test("the in-page scan is a valid expression covering the documented rules", () => {
  // Parses as a function body without running it in a browser.
  assert.doesNotThrow(() => new Function(`return ${SCAN_EXPR};`));
  for (const rule of ["control-name", "field-label", "image-alt", "duplicate-id", "hidden-focusable", "positive-tabindex", "html-lang"]) {
    assert.ok(SCAN_EXPR.includes(`"${rule}"`), `scan is missing rule ${rule}`);
  }
});
