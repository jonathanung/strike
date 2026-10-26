#!/usr/bin/env node
// Local happy-path for an authenticated gateway. Not part of CI.
// The daemon token stays in a 0600 file. The page reads it from sessionStorage
// (strike.gateway.tokens) for this tab. It is not put on argv, in a URL, or
// in the screenshot DOM dump. Artifacts are gitignored.

import { mkdir, mkdtemp, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { spawn } from "node:child_process";

import {
  attachCDP,
  delay,
  freePort,
  launchChrome,
  removeDir,
  stopStrike,
  waitForPage,
  waitHealth,
} from "./webui-uat/browser.mjs";
import { encodeVideo, writeBytes } from "./webui-uat/media.mjs";

const outDir = resolve(process.env.GATEWAY_QR_OUT || "webui-uat-artifacts/gateway-qr-auth");
const bin = process.env.STRIKE_BIN || "./strike";
const home = await mkdtemp(join(tmpdir(), "strike-gateway-qr-"));
const port = await freePort();
const baseURL = `http://127.0.0.1:${port}`;
const token = `uat-${Math.random().toString(36).slice(2)}-${Date.now().toString(36)}`;
const tokenPath = join(home, ".strike", "daemon.token");
await mkdir(join(home, ".strike"), { recursive: true });
await writeFile(join(home, ".strike", "config.json"), '{"sandboxAllowDegrade":true}\n');
await writeFile(tokenPath, `${token}\n`, { mode: 0o600 });

const strike = spawn(
  bin,
  [
    "serve",
    "--addr",
    `127.0.0.1:${port}`,
    "--provider",
    "echo",
    "--auth",
    "--token-file",
    tokenPath,
  ],
  { cwd: process.cwd(), env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] },
);
let log = "";
const keep = (buf) => {
  log += buf.toString();
  if (log.length > 16000) log = log.slice(-16000);
};
strike.stdout.on("data", keep);
strike.stderr.on("data", keep);

const failures = [];
let chrome;
let page;
try {
  await waitHealth(`${baseURL}/health`);
  if (log.includes("?token=") || log.includes(`/?token`)) {
    failures.push("serve banner put a token query in the log");
  }
  chrome = await launchChrome();
  const target = await waitForPage(chrome);
  page = attachCDP(target.webSocketDebuggerUrl);
  await page.opened;
  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1440,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await page.send("Page.navigate", { url: baseURL });
  const first = await waitFor(
    page,
    `document.body && document.body.innerText.includes("Run a command")`,
    20000,
  );
  if (!first) failures.push("cockpit did not boot");
  await page.evaluate(`(() => {
    const raw = JSON.stringify({ this: ${JSON.stringify(token)} });
    sessionStorage.setItem("strike.gateway.tokens", raw);
    localStorage.removeItem("strike.token");
    window.__strikeBoot = 1;
  })()`);
  await page.send("Page.reload", { ignoreCache: true });
  const booted = await waitFor(
    page,
    `window.__strikeBoot !== 1 && document.body && document.body.innerText.includes("Run a command")`,
    20000,
  );
  if (!booted) failures.push("cockpit did not boot again");
  await page.send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "k",
    code: "KeyK",
    windowsVirtualKeyCode: 75,
    modifiers: 2,
  });
  await page.send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "k",
    code: "KeyK",
    windowsVirtualKeyCode: 75,
    modifiers: 2,
  });
  const palette = await waitFor(page, `Boolean(document.querySelector(".palette input"))`, 4000);
  if (!palette) failures.push("palette did not open");
  await page.evaluate(`document.querySelector(".palette input")?.focus()`);
  await page.evaluate(`(() => {
    const input = document.querySelector(".palette input");
    if (!input) return false;
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, "phone");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "phone" }));
    return true;
  })()`);
  await delay(200);
  await page.evaluate(`document.querySelector(".palette input")?.focus()`);
  await page.send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "Enter",
    code: "Enter",
    windowsVirtualKeyCode: 13,
  });
  await page.send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "Enter",
    code: "Enter",
    windowsVirtualKeyCode: 13,
  });
  const opened = await waitFor(
    page,
    `document.querySelector(".gq-dialog") && (document.querySelector("#gq-url")?.value || "").includes("#pair=")`,
    8000,
  );
  if (!opened) failures.push("authenticated dialog did not mint a fragment URL");
  const leak = await page.evaluate(`(() => {
    const value = document.querySelector("#gq-url")?.value || "";
    const shown = document.querySelector(".gq-url")?.innerText || "";
    const body = document.body ? document.body.innerText : "";
    const inputs = [...document.querySelectorAll("input")].map((el) => el.value || "").join("\\n");
    const html = document.documentElement ? document.documentElement.innerHTML : "";
    const secret = ${JSON.stringify(token)};
    const blob = value + "\\n" + shown + "\\n" + body + "\\n" + inputs + "\\n" + html;
    return {
      query: value.includes("?token=") || shown.includes("?token=") || body.includes("?token="),
      secret: blob.includes(secret),
      pair: value.includes("#pair="),
      svg: Boolean(document.querySelector(".gq-qr svg")),
    };
  })()`);
  if (!leak?.pair || !leak?.svg) failures.push("pairing QR or fragment URL missing");
  if (leak?.query) failures.push("pairing URL used a token query");
  if (leak?.secret) failures.push("daemon token appeared in the DOM");
  const shotDir = join(outDir, "screenshots");
  const frameDir = join(outDir, "frames");
  await mkdir(shotDir, { recursive: true });
  await mkdir(frameDir, { recursive: true });
  const sizes = [
    [1440, 900, "1440"],
    [1024, 594, "1024"],
    [390, 844, "mobile"],
  ];
  let frame = 0;
  for (const [width, height, tag] of sizes) {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: width < 500,
    });
    await delay(150);
    const png = await page.send("Page.captureScreenshot", { format: "png" });
    await writeBytes(join(shotDir, `shell-phone-paired-${tag}.png`), Buffer.from(png.data, "base64"));
    const jpeg = await page.send("Page.captureScreenshot", { format: "jpeg", quality: 55 });
    frame += 1;
    await writeBytes(
      join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`),
      Buffer.from(jpeg.data, "base64"),
    );
  }
  await encodeVideo(frameDir, join(outDir, "paired.mp4"), 1);
  const report = { failures, outDir };
  await writeFile(join(outDir, "report.json"), JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report));
  if (failures.length) process.exitCode = 1;
} catch (err) {
  console.error(err);
  if (log && !log.includes(token)) console.error(log.slice(-1500));
  process.exitCode = 1;
} finally {
  if (page) {
    try {
      await page.close();
    } catch {
      // already gone
    }
  }
  if (chrome) {
    chrome.kill("SIGTERM");
    await removeDir(chrome.userDataDir);
  }
  await stopStrike(strike);
  await removeDir(home);
}

async function waitFor(page, expression, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await page.evaluate(expression)) return true;
    await delay(200);
  }
  return false;
}
