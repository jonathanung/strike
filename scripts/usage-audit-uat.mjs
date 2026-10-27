// Browser pass for the usage dashboard and hash-chained audit log.
//
// Token counts are usage.reported envelopes written into the session log
// before the daemon starts — the same records the engine appends. Dollars
// are not typed into the page: the daemon prices them from the models.dev
// cache (fetched here, or STRIKE_MODELS_CACHE). Echo has no catalog price,
// so that session stays tokens-only.
//
// Artifacts land in usage-audit-uat/ (gitignored) or --out.

import { copyFile, mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  attachCDP,
  delay,
  freePort,
  launchChrome,
  removeDir,
  startStrike,
  stopStrike,
  waitForPage,
  waitHealth,
} from "./webui-uat/browser.mjs";
import { encodeVideo, writeBytes } from "./webui-uat/media.mjs";

const MODEL = "claude-haiku-4-5-20251001";
const PROVIDER = "anthropic";
const ROOT_ID = "haiku-root";
const CHILD_ID = "haiku-child";
const OFFLINE_ID = "offline-note";

function arg(name, fallback) {
  const i = process.argv.indexOf(name);
  if (i === -1) return fallback;
  return process.argv[i + 1] || fallback;
}

const outDir = arg("--out", "usage-audit-uat");
const noVideo = process.argv.includes("--no-video");
const bin = process.env.STRIKE_BIN || "./strike";

function envelope(type, time, data) {
  return JSON.stringify({ type, time, v: "1.18.0", data }) + "\n";
}

function tokens(n) {
  return { n, known: true };
}

function dayStamp(offsetDays) {
  const d = new Date(Date.now() + offsetDays * 86400000);
  return d.toISOString().slice(0, 10);
}

async function seed(home) {
  const sessions = join(home, ".strike", "sessions");
  const cache = join(home, ".strike", "cache");
  await mkdir(sessions, { recursive: true });
  await mkdir(cache, { recursive: true });
  const catalog = process.env.STRIKE_MODELS_CACHE || "/tmp/models.json";
  await copyFile(catalog, join(cache, "models.json"));

  const yesterday = dayStamp(-1);
  const today = dayStamp(0);
  const root = [
    envelope("model.selected", `${yesterday}T15:00:00Z`, {
      sessionId: ROOT_ID,
      provider: PROVIDER,
      model: MODEL,
    }),
    envelope("usage.reported", `${yesterday}T15:00:01Z`, {
      sessionId: ROOT_ID,
      input: tokens(500_000),
      output: tokens(0),
      cacheRead: { known: true },
      cacheCreation: { known: true },
      source: "actual",
    }),
    envelope("usage.reported", `${today}T12:00:00Z`, {
      sessionId: ROOT_ID,
      input: tokens(1_000_000),
      output: tokens(200_000),
      cacheRead: tokens(1_000_000),
      cacheCreation: tokens(80_000),
      source: "actual",
    }),
  ].join("");
  await writeFile(join(sessions, `${ROOT_ID}.jsonl`), root);
  await writeFile(
    join(sessions, `${ROOT_ID}.meta.json`),
    JSON.stringify({ title: "Haiku review" }) + "\n",
  );

  const child = [
    envelope("model.selected", `${yesterday}T16:00:00Z`, {
      sessionId: CHILD_ID,
      provider: "echo",
      model: "echo",
    }),
    envelope("usage.reported", `${yesterday}T16:00:01Z`, {
      sessionId: CHILD_ID,
      input: tokens(48),
      output: tokens(12),
      cacheRead: tokens(16),
      cacheCreation: { known: true },
      source: "estimated",
    }),
  ].join("");
  await writeFile(join(sessions, `${CHILD_ID}.jsonl`), child);
  await writeFile(
    join(sessions, `${CHILD_ID}.meta.json`),
    JSON.stringify({ title: "Echo lookup", parentSessionId: ROOT_ID }) + "\n",
  );

  // local/offline is not a models.dev provider. Tokens only, no dollars.
  const offline = [
    envelope("model.selected", `${yesterday}T17:00:00Z`, {
      sessionId: OFFLINE_ID,
      provider: "local",
      model: "offline",
    }),
    envelope("usage.reported", `${yesterday}T17:00:01Z`, {
      sessionId: OFFLINE_ID,
      input: tokens(1200),
      output: tokens(300),
      cacheRead: tokens(40),
      cacheCreation: { known: true },
      source: "actual",
    }),
  ].join("");
  await writeFile(join(sessions, `${OFFLINE_ID}.jsonl`), offline);
  await writeFile(
    join(sessions, `${OFFLINE_ID}.meta.json`),
    JSON.stringify({ title: "Offline note" }) + "\n",
  );
  return { yesterday, today };
}

const failures = [];
function check(ok, message) {
  if (!ok) {
    failures.push(message);
    console.error(`uat fail: ${message}`);
  }
}

const home = await mkdtemp(join(tmpdir(), "strike-usage-audit-"));
const port = await freePort();
let strike;
let chrome;
let page;
try {
  const days = await seed(home);
  console.log(`uat: seeded ${days.yesterday} and ${days.today} under ${home}`);
  strike = startStrike({ bin, port, home, cwd: process.cwd() });
  const base = `http://127.0.0.1:${port}`;
  await waitHealth(`${base}/health`);
  const usage = await fetch(`${base}/v1/usage`).then((r) => r.json());
  const root = (usage.sessions || []).find((s) => s.id === ROOT_ID);
  const child = (usage.sessions || []).find((s) => s.id === CHILD_ID);
  const offline = (usage.sessions || []).find((s) => s.id === OFFLINE_ID);
  check(root && root.costKnown && !root.costPartial, `root session not fully priced: ${JSON.stringify(root)}`);
  // models.dev prices echo/echo (input 10, output 50) and publishes no cache rate.
  check(child && child.costKnown && child.costPartial, `echo child should be partial: ${JSON.stringify(child)}`);
  check(offline && !offline.costKnown, `offline model should be unpriced: ${JSON.stringify(offline)}`);
  check(root && root.cacheRead === 1_000_000, `cache read missing: ${root && root.cacheRead}`);
  check(child && child.parentId === ROOT_ID, "child parent id missing");
  const priced = root ? Number(root.costUsd) : 0;
  console.log(`uat: catalog priced haiku session at $${priced} (cache included)`);

  chrome = await launchChrome();
  const target = await waitForPage(chrome);
  page = attachCDP(target.webSocketDebuggerUrl);
  await page.opened;
  await page.send("Page.enable");
  await page.send("Runtime.enable");

  const shotDir = join(outDir, "screenshots");
  const frameDir = join(outDir, "frames");
  await mkdir(shotDir, { recursive: true });
  if (!noVideo) await mkdir(frameDir, { recursive: true });
  let frame = 0;
  page.size = { width: 0, height: 0 };

  const viewport = async (width, height) => {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: width < 500,
      screenWidth: width,
      screenHeight: height,
    });
    page.size = { width, height };
    await delay(150);
  };

  const grabFrame = async (copies = 1) => {
    if (noVideo || page.size.width !== 1440 || page.size.height !== 900) return;
    const shot = await page.send("Page.captureScreenshot", {
      format: "jpeg",
      quality: 60,
      clip: { x: 0, y: 0, width: 1440, height: 900, scale: 1 },
    });
    const bytes = Buffer.from(shot.data, "base64");
    for (let i = 0; i < copies; i++) {
      frame += 1;
      await writeBytes(join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`), bytes);
    }
  };

  const saveShot = async (name) => {
    const png = await page.send("Page.captureScreenshot", { format: "png" });
    const pngPath = join(shotDir, `${name}.png`);
    await writeBytes(pngPath, Buffer.from(png.data, "base64"));
    await grabFrame(3);
    console.log(`uat: shot ${name} ${page.size.width}x${page.size.height}`);
    return pngPath;
  };

  const bodyText = () => page.evaluate("document.body ? document.body.innerText : ''");
  const waitText = async (needle, timeoutMs, label) => {
    const deadline = Date.now() + timeoutMs;
    let last = "";
    while (Date.now() < deadline) {
      last = await bodyText();
      if (String(last).includes(needle)) return true;
      await grabFrame();
      await delay(200);
    }
    check(false, `timed out waiting for ${label}; page starts: ${String(last).slice(0, 400)}`);
    return false;
  };

  const clickText = async (text) => {
    const ok = await page.evaluate(`(() => {
      const want = ${JSON.stringify(text)};
      const el = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === want);
      if (!el) return false;
      el.click();
      return true;
    })()`);
    check(ok, `no button ${text}`);
    await delay(200);
    await grabFrame(2);
    return ok;
  };

  const press = async (key, code, vk, modifiers = 0) => {
    const common = { key, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers };
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
    await delay(80);
  };

  await page.send("Page.navigate", { url: `${base}/` });
  await waitText("Run a command", 20000, "cockpit shell");
  await delay(400);

  const sizes = [
    ["1024", 1024, 594],
    ["1440", 1440, 900],
    ["mobile", 390, 844],
  ];
  for (const [tag, w, h] of sizes) {
    await viewport(w, h);
    await saveShot(`chat-before-${tag}`);
  }
  await viewport(1440, 900);

  await clickText("Usage");
  await waitText("Input tokens", 8000, "usage page");
  await waitText("Haiku review", 8000, "priced session title");
  await waitText("not priced", 8000, "unpriced offline session");
  await waitText("Echo lookup", 8000, "child session title");
  await waitText("Offline note", 8000, "offline session title");
  await waitText("<$0.01", 8000, "sub-cent echo price");
  const usageText = await bodyText();
  check(usageText.includes("child of"), "child role label missing");
  check(usageText.includes(String(priced.toFixed(2))) || usageText.includes("$2."), `page did not show the catalog cost near ${priced}: ${usageText.slice(0, 500)}`);

  const setBudget = async (value) => {
    const ok = await page.evaluate(`(() => {
      const input = document.querySelector("#daily-budget");
      if (!input) return false;
      input.focus();
      const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
      proto.set.call(input, ${JSON.stringify(value)});
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(value)} }));
      return input.value === ${JSON.stringify(value)};
    })()`);
    check(ok, `budget input did not accept ${value}`);
    await clickText("Save budget");
  };

  await setBudget("nope");
  await waitText("Enter a daily budget of 0 or more", 4000, "invalid budget error");
  await saveShot("usage-budget-error-1440");

  await setBudget("2");
  await waitText("priced usage today", 8000, "daily budget alert");
  const alertText = await bodyText();
  check(alertText.includes("100%"), `expected a 100% budget alert, got: ${alertText.slice(0, 700)}`);
  for (const [tag, w, h] of sizes) {
    await viewport(w, h);
    await saveShot(`usage-after-${tag}`);
  }
  await viewport(1440, 900);

  // Keyboard path: Ctrl+K, filter to Audit, Enter.
  await press("k", "KeyK", 75, 2);
  await waitText("Hash-chained log of sensitive actions", 4000, "command palette");
  await page.evaluate(`document.querySelector(".palette input")?.focus()`);
  await page.send("Input.insertText", { text: "audit" });
  await delay(200);
  await grabFrame(2);
  await saveShot("audit-palette-1440");
  await page.evaluate(`document.querySelector(".palette input")?.focus()`);
  await press("Enter", "Enter", 13, 0);
  await waitText("Verify chain", 8000, "audit page via palette");
  await clickText("Verify chain");
  await waitText("Chain intact", 8000, "chain verification");
  for (const [tag, w, h] of sizes) {
    await viewport(w, h);
    await saveShot(`audit-after-${tag}`);
  }
  await viewport(1440, 900);

  // Live echo turn. Its usage.reported is estimated and has no catalog price.
  await clickText("Chat");
  await waitText("Run a command", 8000, "back to chat");
  const marker = "usage-audit-live";
  const typed = await page.evaluate(`(() => {
    const ta = document.querySelector("textarea");
    if (!ta) return false;
    ta.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, ${JSON.stringify(marker)});
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(marker)} }));
    return ta.value === ${JSON.stringify(marker)};
  })()`);
  check(typed, "composer did not accept the live marker");
  await clickText("Send");
  await waitText("You said:", 20000, "echo reply");
  const live = await (async () => {
    const deadline = Date.now() + 15000;
    while (Date.now() < deadline) {
      const body = await fetch(`${base}/v1/usage`).then((r) => r.json());
      const echo = (body.sessions || []).find((s) => s.provider === "echo" && s.id !== CHILD_ID);
      if (echo && echo.reports > 0) return echo;
      await delay(300);
    }
    return null;
  })();
  check(Boolean(live && live.reports > 0), `live echo usage was not recorded: ${JSON.stringify(live)}`);
  if (live && live.costKnown) {
    console.log(`uat: live echo priced by catalog at $${live.costUsd} (estimated tokens)`);
  }

  await clickText("Usage");
  await waitText("usage-audit-live", 8000, "live session on the usage page");
  await saveShot("usage-after-live-echo-1440");

  if (!noVideo && frame > 0) {
    await encodeVideo(frameDir, join(outDir, "usage-audit.mp4"), 6);
    console.log(`uat: video ${join(outDir, "usage-audit.mp4")} (${frame} frames)`);
  }
} catch (err) {
  failures.push(String(err));
  console.error(err);
  if (strike) console.error(strike.log().slice(-2000));
} finally {
  if (page) await page.close().catch(() => {});
  if (chrome) {
    chrome.kill("SIGTERM");
    await removeDir(chrome.userDataDir).catch(() => {});
  }
  await stopStrike(strike);
  await removeDir(home).catch(() => {});
}

const report = { ok: failures.length === 0, failures };
await mkdir(outDir, { recursive: true });
await writeFile(join(outDir, "report.json"), JSON.stringify(report, null, 2));
if (failures.length) {
  console.error(`uat: ${failures.length} failure(s)`);
  process.exit(1);
}
console.log("uat: usage-audit pass ok");
