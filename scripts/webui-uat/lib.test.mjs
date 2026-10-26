import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";

import {
  DARK_TOKENS,
  GOLDEN_MEAN_ABS_MAX,
  LIGHT_TOKENS,
  missingLabels,
  meanAbsDiff,
  parseArgs,
  parseViewport,
  sideBySideFilter,
  tokenMismatch,
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

test("parseArgs defaults and compare-only", () => {
  assert.equal(parseArgs([]).video, true);
  assert.equal(parseArgs(["--suite", "smoke", "--no-video"]).video, false);
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
