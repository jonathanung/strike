#!/usr/bin/env node
// Conversation-layout pass for stream chat-mode. Boots echo, drives Chrome,
// and writes screenshots plus a 1440x900 video. Not part of CI; webui-smoke
// covers the layout toggle and attachment redaction on every PR.
//
//   node scripts/webui-chat-mode-uat.mjs --out webui-chat-mode-artifacts

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
import { encodeVideo, writeBytes } from "./webui-uat/media.mjs";

const outDir = resolve(process.argv.includes("--out") ? process.argv[process.argv.indexOf("--out") + 1] : "webui-chat-mode-artifacts");
const home = await mkdtemp(join(tmpdir(), "strike-chat-mode-uat-"));
const port = await freePort();
const baseURL = `http://127.0.0.1:${port}`;
const bin = process.env.STRIKE_BIN || "./strike";
await mkdir(join(home, ".strike"), { recursive: true });
await writeFile(join(home, ".strike", "config.json"), '{"sandboxAllowDegrade":true}\n');

const pngPath = join(home, "pixel.png");
const emptyPath = join(home, "empty.txt");
const png = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==",
  "base64",
);
await writeFile(pngPath, png);
await writeFile(emptyPath, "");

const shotDir = join(outDir, "screenshots");
const frameDir = join(outDir, "frames");
await mkdir(shotDir, { recursive: true });
await mkdir(frameDir, { recursive: true });

let strike = startStrike({ bin, port, home, cwd: process.cwd() });
let chrome;
const failures = [];
const shots = [];
let frame = 0;

const check = (ok, message) => {
  if (!ok) failures.push(message);
};

try {
  await waitHealth(`${baseURL}/health`);
  chrome = await launchChrome();
  const target = await waitForPage(chrome);
  const page = attachCDP(target.webSocketDebuggerUrl);
  await page.opened;
  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Emulation.setFocusEmulationEnabled", { enabled: true });

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
  };

  const grab = async (copies = 1) => {
    if (page.size?.width !== 1440) return;
    const shot = await page.send("Page.captureScreenshot", { format: "jpeg", quality: 60 });
    const bytes = Buffer.from(shot.data, "base64");
    for (let i = 0; i < copies; i++) {
      frame += 1;
      await writeBytes(join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`), bytes);
    }
  };

  const saveShot = async (name) => {
    const pngShot = await page.send("Page.captureScreenshot", { format: "png" });
    await writeBytes(join(shotDir, `${name}.png`), Buffer.from(pngShot.data, "base64"));
    shots.push(name);
    await grab(3);
  };

  const text = () => page.evaluate("document.body ? document.body.innerText : ''");

  const waitUntil = async (expression, timeoutMs, label) => {
    const deadline = Date.now() + timeoutMs;
    let last = "";
    while (Date.now() < deadline) {
      last = await page.evaluate(expression);
      if (last) return last;
      await grab();
      await delay(200);
    }
    const body = await text();
    failures.push(`timed out waiting for ${label}; page starts: ${String(body).slice(0, 400)}`);
    return null;
  };

  const press = async (key, code, vk, modifiers = 0) => {
    const common = { key, code, windowsVirtualKeyCode: vk, nativeVirtualKeyCode: vk, modifiers };
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
    await delay(80);
    await grab();
  };

  const setFiles = async (paths) => {
    const doc = await page.send("DOM.getDocument", { depth: 0 });
    const found = await page.send("DOM.querySelector", {
      nodeId: doc.root.nodeId,
      selector: "input[data-chat-file]",
    });
    check(found.nodeId > 0, "file input missing");
    if (!found.nodeId) return;
    await page.send("DOM.setFileInputFiles", { nodeId: found.nodeId, files: paths });
    await delay(250);
  };

  await viewport(1440, 900);
  await page.send("Page.navigate", { url: baseURL });
  const booted = await waitUntil(
    `(() => {
      const body = document.body ? document.body.innerText : "";
      const acid = getComputedStyle(document.documentElement).getPropertyValue("--acid").trim();
      return body.includes("Run a command") && body.includes("Activity") && acid === "#7c3aed";
    })()`,
    30000,
    "cockpit boot",
  );
  if (!booted) throw new Error(failures.join("\n") || "boot failed");

  const def = await page.evaluate(`(() => {
    const shell = document.querySelector(".shell");
    return {
      layout: document.documentElement.getAttribute("data-layout") || "",
      cls: shell ? shell.className : "",
      attach: !!document.querySelector("[data-chat-attach]"),
    };
  })()`);
  check(def.layout !== "conversation" && !String(def.cls).includes("layout-conversation"), `default is not cockpit: ${JSON.stringify(def)}`);
  check(def.attach, "attach control missing");
  await saveShot("cockpit-1440");
  await viewport(1024, 594);
  await delay(150);
  await saveShot("cockpit-1024");
  await viewport(390, 844);
  await delay(150);
  await saveShot("cockpit-390");

  await viewport(1440, 900);
  await delay(100);
  await press("k", "KeyK", 75, 2);
  const palette = await waitUntil(`Boolean(document.querySelector(".palette input"))`, 4000, "palette");
  if (palette) {
    await page.evaluate(`(() => {
      const input = document.querySelector(".palette input");
      input.focus();
      const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
      proto.set.call(input, "conversation");
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "conversation" }));
    })()`);
    await delay(200);
    await grab(2);
    await saveShot("palette-conversation-1440");
    await page.evaluate(`document.querySelector(".palette input")?.focus()`);
    await press("Enter", "Enter", 13);
  }
  const chat = await waitUntil(
    `(() => {
      const shell = document.querySelector(".shell");
      const bar = document.querySelector(".chat-mode-bar");
      return Boolean(shell && shell.className.includes("layout-conversation") && bar && getComputedStyle(bar).display === "flex");
    })()`,
    4000,
    "conversation layout",
  );
  check(Boolean(chat), "conversation layout did not apply");
  await saveShot("conversation-1440");
  await viewport(1024, 594);
  await delay(150);
  await saveShot("conversation-1024");
  await viewport(390, 844);
  await delay(150);
  await saveShot("conversation-390");

  await viewport(1440, 900);
  await delay(100);
  await page.evaluate(`document.querySelector("[data-chat-sessions]")?.click()`);
  const drawer = await waitUntil(
    `Boolean(document.querySelector(".shell.drawer-sessions"))`,
    3000,
    "sessions drawer",
  );
  check(Boolean(drawer), "sessions drawer did not open");
  await saveShot("drawer-sessions-1440");
  await viewport(390, 844);
  await delay(120);
  await saveShot("drawer-sessions-390");
  await viewport(1440, 900);
  await press("Escape", "Escape", 27);
  const closed = await waitUntil(
    `(() => {
      const shell = document.querySelector(".shell");
      return Boolean(shell && !shell.className.includes("drawer-sessions"));
    })()`,
    3000,
    "escape closes drawer",
  );
  check(Boolean(closed), "escape did not close the drawer");

  await page.evaluate(`document.querySelector("[data-chat-panel]")?.click()`);
  const panel = await waitUntil(`Boolean(document.querySelector(".shell.drawer-panel"))`, 3000, "panel drawer");
  check(Boolean(panel), "panel drawer did not open");
  await saveShot("drawer-panel-1440");
  await press("Escape", "Escape", 27);

  const marker = "chat-mode-hello";
  const typed = await page.evaluate(`(() => {
    const ta = document.querySelector("textarea");
    if (!ta) return false;
    ta.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, ${JSON.stringify(marker)});
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(marker)} }));
    return ta.value === ${JSON.stringify(marker)};
  })()`);
  check(typed, "composer did not accept the marker");
  await page.evaluate(`document.querySelector("button.send")?.click()`);
  const echoed = await waitUntil(
    `document.body && document.body.innerText.includes(${JSON.stringify(marker)}) && document.body.innerText.includes("You said")`,
    8000,
    "echo reply",
  );
  check(Boolean(echoed), "echo did not show the marker");
  await saveShot("conversation-reply-1440");
  await viewport(390, 844);
  await delay(150);
  await saveShot("conversation-reply-390");
  await viewport(1440, 900);

  const follow = await page.evaluate(`document.body.innerText.includes("What should I check next?")`);
  check(follow, "fixed follow-up chips missing after a reply");

  await setFiles([emptyPath]);
  const emptyChip = await waitUntil(
    `document.body && document.body.innerText.includes("file is empty")`,
    3000,
    "empty file chip",
  );
  check(Boolean(emptyChip), "empty file did not show an error chip");
  await saveShot("attach-empty-1440");
  await page.evaluate(`(() => {
    const btn = [...document.querySelectorAll(".attach-chip button")].find((b) => (b.innerText || "").includes("Remove"));
    if (btn) btn.click();
  })()`);
  await delay(150);

  await setFiles([pngPath]);
  const chip = await waitUntil(
    `document.body && document.body.innerText.includes("pixel.png")`,
    4000,
    "png chip",
  );
  check(Boolean(chip), "png did not appear as a chip");
  await saveShot("attach-png-1440");
  const withFile = "chat-mode-with-file";
  await page.evaluate(`(() => {
    const ta = document.querySelector("textarea");
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, ${JSON.stringify(withFile)});
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(withFile)} }));
  })()`);
  await page.evaluate(`document.querySelector("button.send")?.click()`);
  const sentFile = await waitUntil(
    `document.body && document.body.innerText.includes(${JSON.stringify(withFile)}) && !document.body.innerText.includes("pixel.png")`,
    8000,
    "send with attachment",
  );
  check(Boolean(sentFile), "attachment send did not clear the chip or echo the text");
  await saveShot("conversation-attached-1440");

  await stopStrike(strike);
  await waitDown(`${baseURL}/health`);
  const down = await waitUntil(
    `(() => {
      const body = document.body ? document.body.innerText : "";
      return body.includes("Unreachable") || body.includes("Reconnecting") || body.includes("Could not reach the daemon");
    })()`,
    8000,
    "daemon down",
  );
  check(Boolean(down), "page did not show a disconnect");
  const kept = await page.evaluate(`document.body && document.body.innerText.includes(${JSON.stringify(marker)})`);
  check(kept, "transcript disappeared while the daemon was down");
  await saveShot("conversation-offline-1440");
  await viewport(390, 844);
  await delay(120);
  await saveShot("conversation-offline-390");
  await viewport(1440, 900);

  strike = startStrike({ bin, port, home, cwd: process.cwd() });
  await waitHealth(`${baseURL}/health`);
  await page.evaluate(`(() => {
    const btn = [...document.querySelectorAll("button")].find((b) => (b.innerText || "").trim() === "Retry");
    if (btn) btn.click();
  })()`);
  const back = await waitUntil(
    `document.body && document.body.innerText.includes("Connected")`,
    8000,
    "reconnect",
  );
  check(Boolean(back), "retry did not show Connected");
  await saveShot("conversation-reconnected-1440");

  const voice = await page.evaluate(`Boolean(document.querySelector("[data-chat-voice]"))`);
  await writeFile(join(outDir, "report.json"), JSON.stringify({
    ok: failures.length === 0,
    shots,
    frames: frame,
    voiceButton: voice,
    failures,
  }, null, 2));

  if (frame > 0) {
    await encodeVideo(frameDir, join(outDir, "chat-mode-uat.mp4"), 6);
  }
  if (failures.length) {
    console.error(`chat-mode uat failed (${failures.length})`);
    for (const f of failures) console.error(`- ${f}`);
    if (strike?.log) console.error(strike.log().slice(-1500));
    process.exitCode = 1;
  } else {
    console.log(`chat-mode uat ok shots=${shots.length} frames=${frame} voice=${voice}`);
  }
  await page.close();
} catch (err) {
  console.error(String(err));
  if (strike?.log) console.error(strike.log().slice(-2000));
  process.exitCode = 1;
} finally {
  if (chrome) chrome.kill("SIGTERM");
  await stopStrike(strike);
  await removeDir(home);
}
