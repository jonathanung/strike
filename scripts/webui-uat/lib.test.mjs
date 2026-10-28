import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import {
  DARK_TOKENS,
  GOLDEN_MEAN_ABS_MAX,
  LIGHT_TOKENS,
  PHONE,
  missingLabels,
  meanAbsDiff,
  parseArgs,
  parseViewport,
  phoneShotName,
  sideBySideFilter,
  tokenMismatch,
  waitForLabels,
} from "./lib.mjs";

test("parseViewport accepts the review sizes", () => {
  assert.deepEqual(parseViewport("1024x594"), { width: 1024, height: 594 });
  assert.deepEqual(parseViewport("1440x900"), { width: 1440, height: 900 });
  assert.throws(() => parseViewport("wide"), /WxH/);
});

test("missingLabels and tokenMismatch", () => {
  assert.deepEqual(missingLabels("Activity Files", ["Activity", "Send"]), ["Send"]);
  assert.deepEqual(tokenMismatch({ acid: "#7C3AED", ground: "#14131c" }, DARK_TOKENS), []);
  assert.deepEqual(tokenMismatch({ acid: "#fff", ground: "#14131c" }, DARK_TOKENS), [
    "acid=#fff want #7c3aed",
  ]);
  assert.equal(LIGHT_TOKENS.ground, "#ffffff");
  assert.ok(GOLDEN_MEAN_ABS_MAX > 0);
});

test("waitForLabels polls until the labels render", async () => {
  // The pre-spawn stub satisfies early labels first; "Connected" only
  // appears once the live session has spawned. A one-shot snapshot taken
  // at the first state is the flake this waiter replaces.
  const states = ["Run a command Activity", "Run a command Activity Connected"];
  let calls = 0;
  const { text, missing } = await waitForLabels(
    async () => states[Math.min(calls++, states.length - 1)],
    ["Run a command", "Connected"],
    { timeoutMs: 1000, intervalMs: 1, sleep: async () => {} },
  );
  assert.deepEqual(missing, []);
  assert.ok(text.includes("Connected"));
  assert.equal(calls, 2);
});

test("waitForLabels still fails when a label genuinely never appears", async () => {
  let t = 0;
  const { text, missing } = await waitForLabels(
    async () => "Run a command Activity",
    ["Run a command", "Connected"],
    {
      timeoutMs: 100,
      intervalMs: 40,
      now: () => t,
      sleep: async (ms) => {
        t += ms;
      },
    },
  );
  assert.deepEqual(missing, ["Connected"]);
  assert.equal(text, "Run a command Activity");
  assert.ok(t >= 100);
});

test("meanAbsDiff", () => {
  assert.equal(meanAbsDiff(Buffer.from([0, 10]), Buffer.from([0, 20])), 5);
  assert.throws(() => meanAbsDiff(Buffer.from([1]), Buffer.from([1, 2])));
});

test("sideBySideFilter scales both halves to one height", () => {
  const f = sideBySideFilter(594);
  assert.match(f, /scale=-1:594:flags=lanczos/);
  assert.match(f, /hstack=inputs=2/);
  assert.throws(() => sideBySideFilter(0));
});

test("phone shot name is the 390-wide slug", () => {
  assert.deepEqual(PHONE, { width: 390, height: 844, tag: "mobile" });
  assert.equal(phoneShotName("approvals-empty"), "approvals-empty-mobile");
  assert.throws(() => phoneShotName("Approvals"), /slug/);
  assert.throws(() => phoneShotName("../x"), /slug/);
});

test("parseArgs defaults and compare-only", () => {
  assert.equal(parseArgs([]).video, true);
  assert.equal(parseArgs([]).suite, "full");
  assert.equal(parseArgs(["--suite", "smoke", "--no-video"]).video, false);
  assert.equal(parseArgs(["--suite", "approvals"]).suite, "approvals");
  assert.throws(() => parseArgs(["--suite", "../etc"]), /suite/);
  const cmp = parseArgs([
    "--compare-only",
    "--left",
    "a.png",
    "--right",
    "b.png",
    "--compare-out",
    "c.png",
  ]);
  assert.equal(cmp.compareOnly, true);
  assert.equal(cmp.left, "a.png");
  assert.equal(cmp.compareOut, "c.png");
  assert.throws(() => parseArgs(["--nope"]));
});

test("committed shell fixture matches the Strike dark tokens", async () => {
  const raw = await readFile(new URL("./fixtures/shell-dark.json", import.meta.url), "utf8");
  const fx = JSON.parse(raw);
  assert.equal(fx.tokens.acid, DARK_TOKENS.acid);
  assert.equal(fx.tokens.ground, DARK_TOKENS.ground);
  assert.ok(fx.mustInclude.includes("Run a command"));
  assert.ok(fx.mustInclude.includes("Activity"));
  assert.equal(fx.selectors["textarea"], 1);
});
