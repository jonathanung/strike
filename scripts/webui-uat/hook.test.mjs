import assert from "node:assert/strict";
import test from "node:test";
import { mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { PHONE } from "./lib.mjs";
import { loadScenario, loadScenarios } from "./scenarios.mjs";
import { createSession } from "./session.mjs";

test("loadScenarios skips underscore files and requires scenario(ctx)", async () => {
  const dir = await mkdtemp(join(tmpdir(), "uat-scenarios-"));
  await writeFile(
    join(dir, "approvals.mjs"),
    "export const scenario = (ctx) => ctx.phone('approvals-empty');\n",
  );
  await writeFile(join(dir, "_phone.example.mjs"), "export const scenario = (ctx) => ctx.phone('nope');\n");
  await writeFile(join(dir, "notes.txt"), "not a scenario\n");
  const mods = await loadScenarios(dir);
  assert.deepEqual(mods.map((m) => m.name), ["approvals"]);
  const calls = [];
  await mods[0].scenario({ phone: (name) => calls.push(name) });
  assert.deepEqual(calls, ["approvals-empty"]);

  await writeFile(join(dir, "broken.mjs"), "export const nope = 1;\n");
  await assert.rejects(() => loadScenarios(dir), /must export scenario/);
  await assert.rejects(() => loadScenario("missing", dir), /no scenario/);
});

test("the checked-in example is not loaded", async () => {
  const mods = await loadScenarios();
  assert.ok(!mods.some((m) => m.name.startsWith("_")));
  const example = await import("./scenarios/_phone.example.mjs");
  assert.equal(example.scenario.length, 1);
  const calls = [];
  await example.scenario({ phone: (name) => calls.push(name) });
  assert.deepEqual(calls, ["stream-empty"]);
});

test("phone() captures 390x844 and restores the desk", async () => {
  const dir = await mkdtemp(join(tmpdir(), "uat-phone-"));
  const calls = [];
  const page = {
    size: null,
    send: async (method, params) => {
      calls.push([method, params?.width, params?.height, params?.mobile]);
      if (method === "Page.captureScreenshot") return { data: Buffer.from("png").toString("base64") };
      return {};
    },
    evaluate: async (expr) => {
      if (String(expr).includes("getPropertyValue")) return { acid: "#7c3aed", ground: "#14131c" };
      return "Run a command";
    },
  };
  const session = await createSession({ page, outDir: dir, video: false, goldens: false, updateGoldens: false });
  const path = await session.phone("stream-empty");
  assert.match(path, /stream-empty-mobile\.png$/);
  assert.deepEqual(session.shots, ["stream-empty-mobile"]);
  const metrics = calls.filter((c) => c[0] === "Emulation.setDeviceMetricsOverride");
  assert.deepEqual(metrics[0], ["Emulation.setDeviceMetricsOverride", PHONE.width, PHONE.height, true]);
  assert.deepEqual(metrics.at(-1), ["Emulation.setDeviceMetricsOverride", 1440, 900, false]);
  assert.equal(page.size.width, 1440);
});
