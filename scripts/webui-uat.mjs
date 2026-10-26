#!/usr/bin/env node
// Boots `strike serve --provider echo`, drives the cockpit in headless Chrome,
// and writes named screenshots plus an optional video. Exits non-zero when an
// assertion fails. Usage: docs/webui-uat.md.

import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

import {
  attachCDP,
  delay,
  freePort,
  launchChrome,
  removeDir,
  startStrike,
  stopStrike,
  waitDown,
  waitForPage,
  waitHealth,
} from "./webui-uat/browser.mjs";
import { HELP, parseArgs } from "./webui-uat/lib.mjs";
import { sideBySide } from "./webui-uat/media.mjs";
import { runScenario } from "./webui-uat/scenario.mjs";

const opt = parseArgs(process.argv.slice(2));
if (opt.help) {
  console.log(HELP);
  process.exit(0);
}

if (opt.compareOnly) {
  if (!opt.left || !opt.right || !opt.compareOut) {
    console.error("--compare-only needs --left, --right, and --compare-out");
    process.exit(1);
  }
  await sideBySide(opt.left, opt.right, opt.compareOut, opt.height);
  console.log(`side-by-side ${opt.compareOut}`);
  process.exit(0);
}

const outDir = resolve(opt.out);
const home = await mkdtemp(join(tmpdir(), "strike-webui-uat-"));
const port = await freePort();
const baseURL = `http://127.0.0.1:${port}`;
const bin = process.env.STRIKE_BIN || "./strike";
await mkdir(join(home, ".strike"), { recursive: true });
await writeFile(join(home, ".strike", "config.json"), '{"sandboxAllowDegrade":true}\n');

let strike = startStrike({ bin, port, home, cwd: process.cwd() });
let chrome;
let page;
const failures = [];
try {
  await waitHealth(`${baseURL}/health`);
  chrome = await launchChrome();
  const target = await waitForPage(chrome);
  page = attachCDP(target.webSocketDebuggerUrl);
  await page.opened;
  const result = await runScenario({
    page,
    baseURL,
    outDir,
    video: opt.video,
    goldens: opt.goldens,
    updateGoldens: opt.updateGoldens,
    controls: {
      stop: async () => {
        await stopStrike(strike);
        await waitDown(`${baseURL}/health`);
      },
      start: async () => {
        strike = startStrike({ bin, port, home, cwd: process.cwd() });
        await waitHealth(`${baseURL}/health`);
        await delay(300);
      },
    },
  });
  failures.push(...result.failures);
  const report = {
    ok: failures.length === 0,
    suite: opt.suite,
    baseURL,
    shots: result.shots,
    video: result.videoPath || "",
    frames: result.frames || 0,
    failures,
  };
  await mkdir(outDir, { recursive: true });
  await writeFile(join(outDir, "report.json"), JSON.stringify(report, null, 2));
  if (opt.left && opt.right && opt.compareOut) {
    await sideBySide(opt.left, opt.right, opt.compareOut, opt.height);
    console.log(`side-by-side ${opt.compareOut}`);
  }
  if (failures.length) {
    console.error(`webui-uat failed (${failures.length})`);
    for (const f of failures) console.error(`- ${f}`);
    if (strike?.log) console.error(strike.log().slice(-1500));
    process.exitCode = 1;
  } else {
    console.log(`webui-uat ok shots=${result.shots.length} video=${result.videoPath || "off"}`);
  }
} catch (err) {
  console.error(String(err?.stack || err));
  if (strike?.log) console.error(strike.log().slice(-2000));
  if (chrome?.log) console.error(chrome.log().slice(-1500));
  process.exitCode = 1;
} finally {
  if (page) await page.close().catch(() => {});
  if (chrome) chrome.kill("SIGTERM");
  await stopStrike(strike);
  await delay(200);
  await removeDir(home);
  if (chrome?.userDataDir) await removeDir(chrome.userDataDir);
}
