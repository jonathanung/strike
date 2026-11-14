#!/usr/bin/env node
// Side-chat drawer pass. Boots `strike serve --provider echo`, opens the
// read-only drawer from the composer, the Activity row, and the keyboard,
// and checks that POST /v1/sidechat/ops refuses a tool approval.
// Artifacts: webui-uat-artifacts/side-chat/ (gitignored).

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
  waitForPage,
  waitHealth,
} from "./webui-uat/browser.mjs";
import { encodeVideo, writeBytes } from "./webui-uat/media.mjs";

const QUESTION = "what is the agent doing?";
const CHILD_NAME = "uat-child";
const CHILD_OBJECTIVE = "Summarize the working directory";

const outDir = resolve("webui-uat-artifacts/side-chat");
const home = await mkdtemp(join(tmpdir(), "strike-sidechat-uat-"));
const port = await freePort();
const baseURL = `http://127.0.0.1:${port}`;
const bin = process.env.STRIKE_BIN || "./strike";
await mkdir(join(home, ".strike"), { recursive: true });
await writeFile(join(home, ".strike", "config.json"), '{"sandboxAllowDegrade":true}\n');

const failures = [];
const shots = [];
let strike = startStrike({ bin, port, home, cwd: process.cwd() });
let chrome;
let page;

try {
  await waitHealth(`${baseURL}/health`);
  chrome = await launchChrome();
  const target = await waitForPage(chrome);
  page = attachCDP(target.webSocketDebuggerUrl);
  await page.opened;
  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Emulation.setFocusEmulationEnabled", { enabled: true });

  const shotDir = join(outDir, "screenshots");
  const frameDir = join(outDir, "frames");
  await mkdir(shotDir, { recursive: true });
  await mkdir(frameDir, { recursive: true });
  let frame = 0;
  let lastFrame = 0;

  const check = (ok, message) => {
    if (!ok) failures.push(message);
  };
  const log = (msg) => console.log(`sidechat-uat: ${msg}`);

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

  const grabFrame = async (copies = 1) => {
    if (page.size?.width !== 1440) return;
    const now = Date.now();
    if (copies === 1 && now - lastFrame < 350) return;
    lastFrame = now;
    const shot = await page.send("Page.captureScreenshot", { format: "jpeg", quality: 60 });
    const bytes = Buffer.from(shot.data, "base64");
    for (let i = 0; i < copies; i++) {
      frame += 1;
      await writeBytes(join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`), bytes);
    }
  };

  const bodyText = () => page.evaluate("document.body ? document.body.innerText : ''");
  const panelText = () =>
    page.evaluate(
      `document.querySelector(".side-chat") ? document.querySelector(".side-chat").innerText : ""`,
    );

  const waitUntil = async (expression, timeoutMs, label) => {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      const last = await page.evaluate(expression);
      if (last) return last;
      await grabFrame();
      await delay(200);
    }
    const text = await bodyText();
    failures.push(`timed out waiting for ${label}; page starts: ${String(text).slice(0, 400)}`);
    return null;
  };

  const click = async (selector, text) => {
    const ok = await page.evaluate(`(() => {
      const want = ${JSON.stringify(text)};
      const nodes = [...document.querySelectorAll(${JSON.stringify(selector)})];
      const el = nodes.find((n) => (n.innerText || "").replace(/\\s+/g, " ").trim().includes(want));
      if (!el) return false;
      el.click();
      return true;
    })()`);
    check(ok, `no clickable ${selector} containing ${JSON.stringify(text)}`);
    await grabFrame(2);
    return ok;
  };

  const saveShot = async (name) => {
    const png = await page.send("Page.captureScreenshot", { format: "png" });
    await writeBytes(join(shotDir, `${name}.png`), Buffer.from(png.data, "base64"));
    shots.push(name);
    await grabFrame(3);
  };

  const sizes = [
    { width: 1024, height: 594, tag: "1024" },
    { width: 1440, height: 900, tag: "1440" },
    { width: 390, height: 844, tag: "mobile" },
  ];

  const shoot = async (name) => {
    for (const size of sizes) {
      await viewport(size.width, size.height);
      await delay(150);
      await saveShot(`${name}-${size.tag}`);
    }
    await viewport(1440, 900);
    await delay(80);
  };

  const dispatchKey = async (key, extra) => {
    const ok = await page.evaluate(`(() => {
      const ev = new KeyboardEvent("keydown", Object.assign({ key: ${JSON.stringify(key)}, bubbles: true, cancelable: true }, ${JSON.stringify(extra || {})}));
      window.dispatchEvent(ev);
      return true;
    })()`);
    check(ok, `keydown ${key} failed`);
    await delay(120);
    await grabFrame(2);
  };

  await viewport(1440, 900);
  await page.send("Page.navigate", { url: baseURL });
  const booted = await waitUntil(
    `(() => {
      const text = document.body ? document.body.innerText : "";
      const acid = getComputedStyle(document.documentElement).getPropertyValue("--acid").trim();
      return text.includes("Run a command") && text.includes("Activity") && acid === "#7c3aed" && document.querySelector(".side-chat-open-composer");
    })()`,
    30000,
    "cockpit boot",
  );
  if (!booted) throw new Error(failures.join("\n"));
  log("booted");
  check(!(await page.evaluate(`!!document.querySelector(".side-chat")`)), "side chat was open before the user opened it");
  await shoot("before-closed");

  // Mirror seeds (#1439): six echo turns produce well over 8 mirror
  // events. The first carries a marker that only "Show all" can reveal;
  // the last is over 180 chars with a tail marker that only expanding
  // the clipped event can reveal.
  const LONG_TAIL = "MIRROR-TAIL-MARKER";
  const longSeed = `mirror long event ${"filler word ".repeat(24)}${LONG_TAIL}`;
  check(longSeed.length > 180, "long mirror seed is not over 180 chars");
  const seedTexts = [
    "mirror-oldest-marker read the makefile",
    "mirror seed two",
    "mirror seed three",
    "mirror seed four",
    "mirror seed five",
    longSeed,
  ];
  for (const text of seedTexts) {
    const seeded = await fetch(`${baseURL}/v1/ops`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ type: "user.input", data: { text } }),
    });
    check(seeded.ok, `seed user.input HTTP ${seeded.status}`);
    await waitUntil(
      `document.querySelector(".transcript") && document.querySelector(".transcript").innerText.includes(${JSON.stringify(text.slice(0, 40))})`,
      20000,
      "mirror seed in transcript",
    );
    await waitUntil(
      `(async () => {
        const res = await fetch("/v1/bootstrap");
        if (!res.ok) return false;
        const body = await res.json();
        return Boolean(body.status && body.status.busy === false);
      })()`,
      20000,
      "session idle after mirror seed",
    );
  }
  await waitUntil(
    `document.querySelector(".transcript") && document.querySelector(".transcript").innerText.includes("You said:")`,
    20000,
    "seeded transcript",
  );

  await click("button.side-chat-open-composer", "Side chat");
  const opened = await waitUntil(
    `(() => {
      const el = document.querySelector(".side-chat");
      if (!el) return false;
      const buttons = [...el.querySelectorAll("button")].map((b) => (b.textContent || "").trim());
      return el.innerText.includes("Read-only") && buttons.includes("Ask") && !buttons.some((b) => b.startsWith("Approval"));
    })()`,
    4000,
    "read-only side chat panel",
  );
  check(Boolean(opened), "composer Side chat did not open a read-only panel");
  log("panel open");

  // Mirror show-all + expand (#1439). Default: last 8 only, the header
  // names the total, the oldest seed and the long seed's tail are both
  // absent. Expand works in the collapsed window; Show all reveals the
  // oldest event and the expansion survives the window toggle.
  const mirrorText = () =>
    page.evaluate(
      `document.querySelector(".side-chat-mirror") ? document.querySelector(".side-chat-mirror").innerText : ""`,
    );
  const mirrorDefault = await waitUntil(
    `(() => {
      const btn = document.querySelector("[data-mirror-more]");
      return Boolean(btn && /^Show all \\(\\d+\\)$/.test(btn.textContent.trim()));
    })()`,
    8000,
    "mirror Show all (N) control",
  );
  check(Boolean(mirrorDefault), "mirror did not offer Show all (N) for >8 events");
  const defaultText = await mirrorText();
  check(!defaultText.includes("mirror-oldest-marker"), "mirror default window leaked the oldest event");
  check(!defaultText.includes(LONG_TAIL), "mirror default rendered the clipped tail");
  check(
    Boolean(await page.evaluate(`!!document.querySelector("[data-mirror-expand]")`)),
    "clipped mirror event has no expand control",
  );
  await shoot("mirror-default");
  const expandedClick = await page.evaluate(`(() => {
    const btn = document.querySelector("[data-mirror-expand]");
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  check(expandedClick, "mirror expand click failed");
  const expandedText = await waitUntil(
    `document.querySelector(".side-chat-mirror") && document.querySelector(".side-chat-mirror").innerText.includes(${JSON.stringify(LONG_TAIL)})`,
    4000,
    "expanded mirror event shows the full body",
  );
  check(Boolean(expandedText), "expanding a clipped mirror event did not show its full body");
  await shoot("mirror-expanded");
  const showAllClick = await page.evaluate(`(() => {
    const btn = document.querySelector("[data-mirror-more]");
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  check(showAllClick, "mirror Show all click failed");
  const allText = await waitUntil(
    `document.querySelector(".side-chat-mirror") && document.querySelector(".side-chat-mirror").innerText.includes("mirror-oldest-marker")`,
    4000,
    "show-all mirror reveals the oldest event",
  );
  check(Boolean(allText), "Show all did not reveal the oldest mirror event");
  const shownAllText = await mirrorText();
  check(shownAllText.includes(LONG_TAIL), "expansion was lost when the mirror window widened");
  const lessLabel = await page.evaluate(
    `document.querySelector("[data-mirror-more]") ? document.querySelector("[data-mirror-more]").textContent.trim() : ""`,
  );
  check(/^Show less \(\d+ total\)$/.test(lessLabel), `mirror toggle label after Show all: ${lessLabel}`);
  log("mirror show-all + expand");
  await shoot("mirror-all");
  // Restore the default window for the rest of the pass.
  await page.evaluate(`document.querySelector("[data-mirror-more]")?.click()`);

  await shoot("after-open");

  const asked = await page.evaluate(`(() => {
    const input = document.getElementById("side-chat-input");
    if (!input) return false;
    input.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(QUESTION)});
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(QUESTION)} }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    return true;
  })()`);
  check(asked, "Enter did not submit the side-chat question");
  const answered = await waitUntil(
    `(() => {
      const el = document.querySelector(".side-chat");
      if (!el) return false;
      const text = el.innerText;
      return text.includes(${JSON.stringify(QUESTION)}) && text.includes("You said:");
    })()`,
    20000,
    "side chat answer",
  );
  check(Boolean(answered), "happy-path answer missing");
  log("answered");
  await shoot("after-answer");

  const errorAsk = await page.evaluate(`(() => {
    const input = document.getElementById("side-chat-input");
    if (!input) return false;
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(input, "run true");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "run true" }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    return true;
  })()`);
  check(errorAsk, "Enter did not submit the tool-call question");
  const refused = await waitUntil(
    `(() => {
      const el = document.querySelector(".side-chat");
      return Boolean(el && /refused a tool call/i.test(el.innerText));
    })()`,
    15000,
    "tool-call refusal",
  );
  check(Boolean(refused), "side chat did not show the tool-call refusal");
  const stillNoApproval = await page.evaluate(`(() => {
    const el = document.querySelector(".side-chat");
    if (!el) return false;
    return ![...el.querySelectorAll("button")].some((b) => (b.textContent || "").trim().startsWith("Approval"));
  })()`);
  check(stillNoApproval, "approval control appeared after the refusal");
  log("refused tool call");
  await shoot("after-error");

  await dispatchKey("Escape");
  const closed = await waitUntil(`!document.querySelector(".side-chat")`, 4000, "Escape closes side chat");
  check(Boolean(closed), "Escape did not close side chat");
  const approvalRemains = await page.evaluate(`document.body && document.body.innerText.includes("Approval")`);
  check(approvalRemains, "main composer lost its Approval chip");
  await grabFrame(8);

  await dispatchKey("s", { altKey: true });
  const altOpened = await waitUntil(`!!document.querySelector(".side-chat")`, 4000, "Alt+S opens side chat");
  check(Boolean(altOpened), "Alt+S did not open side chat");
  log("Alt+S");
  await saveShot("keyboard-alt-s-1440");
  await grabFrame(6);
  await dispatchKey("Escape");
  await waitUntil(`!document.querySelector(".side-chat")`, 4000, "Escape after Alt+S");

  await dispatchKey("k", { ctrlKey: true });
  const palette = await waitUntil(`!!document.querySelector(".palette input")`, 4000, "Ctrl+K palette");
  if (palette) {
    const typed = await page.evaluate(`(() => {
      const input = document.querySelector(".palette input");
      if (!input) return false;
      input.focus();
      const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
      proto.set.call(input, "side chat");
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "side chat" }));
      return input.value === "side chat";
    })()`);
    check(typed, "palette did not accept side chat");
    const filtered = await waitUntil(
      `document.body && document.body.innerText.includes("Ask about this session without steering")`,
      4000,
      "palette filtered to Side chat",
    );
    check(Boolean(filtered), "palette filter did not show Side chat");
    log("palette");
    await saveShot("keyboard-palette-1440");
    await grabFrame(6);
    const entered = await page.evaluate(`(() => {
      const input = document.querySelector(".palette input");
      if (!input) return false;
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      return true;
    })()`);
    check(entered, "palette Enter did not run");
    const fromPalette = await waitUntil(`!!document.querySelector(".side-chat")`, 4000, "palette Side chat");
    check(Boolean(fromPalette), "Ctrl+K Side chat did not open the drawer");
    await grabFrame(6);
    await dispatchKey("Escape");
  }

  await click("button.side-chat-open-activity", "Side chat");
  const fromActivity = await waitUntil(`!!document.querySelector(".side-chat")`, 4000, "Activity header Side chat");
  check(Boolean(fromActivity), "Activity header did not open side chat");
  await dispatchKey("Escape");

  await waitUntil(
    `(async () => {
      const res = await fetch("/v1/bootstrap");
      if (!res.ok) return false;
      const body = await res.json();
      return Boolean(body.status && body.status.busy === false);
    })()`,
    20000,
    "session idle before spawn",
  );
  const roots = await fetch(`${baseURL}/v1/roots`).then((r) => r.json());
  const root = roots.activeId || roots.active_id || roots.roots?.[0]?.id;
  check(Boolean(root), "no live root");
  if (root) {
    const spawned = await fetch(`${baseURL}/v1/ops?root=${encodeURIComponent(root)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        type: "team.spawn",
        data: { objective: CHILD_OBJECTIVE, name: CHILD_NAME, idempotencyKey: "sidechat-uat-1" },
      }),
    });
    const spawnBody = await spawned.text();
    check(spawned.ok, `team.spawn HTTP ${spawned.status} ${spawnBody.slice(0, 300)}`);
    const row = await waitUntil(
      `(() => {
        const el = [...document.querySelectorAll(".activity-row")].find((n) => (n.innerText || "").includes(${JSON.stringify(CHILD_NAME)}));
        return Boolean(el);
      })()`,
      45000,
      "activity row",
    );
    if (row) {
      await click(".activity-row button", "Side chat");
      const about = await waitUntil(
        `(() => {
          const el = document.querySelector(".side-chat");
          return Boolean(el && el.innerText.includes("About") && el.innerText.includes(${JSON.stringify(CHILD_NAME)}));
        })()`,
        4000,
        "side chat focused on the activity row",
      );
      check(Boolean(about), "activity row Side chat did not focus the child");
      log("activity row");
      await shoot("after-activity-row");
    }
  }

  const denied = await fetch(`${baseURL}/v1/sidechat/ops`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ type: "permission.reply", data: { requestId: "r1", decision: "once" } }),
  });
  const deniedBody = await denied.json().catch(() => ({}));
  check(denied.status === 403, `side chat ops status ${denied.status}`);
  check(deniedBody.code === "sidechat_read_only", `side chat ops code ${deniedBody.code}`);
  check(
    String(deniedBody.error || "").includes("cannot approve tools"),
    `side chat ops error ${deniedBody.error}`,
  );
  const mainOps = await fetch(`${baseURL}/v1/ops`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ type: "permission.reply", data: { requestId: "r1", decision: "once" } }),
  });
  const mainBody = await mainOps.json().catch(() => ({}));
  check(mainOps.ok, `main /v1/ops permission.reply HTTP ${mainOps.status} ${JSON.stringify(mainBody).slice(0, 200)}`);
  check(mainBody.code !== "sidechat_read_only", "main ops used the side-chat refusal");
  log("daemon refused side-chat approval");

  let videoPath = "";
  if (frame > 0) {
    videoPath = join(outDir, "side-chat.mp4");
    await encodeVideo(frameDir, videoPath, 4);
  }
  const report = { ok: failures.length === 0, shots, video: videoPath, frames: frame, failures };
  await writeFile(join(outDir, "report.json"), JSON.stringify(report, null, 2));
  if (failures.length) {
    console.error(`webui-sidechat-uat failed (${failures.length})`);
    for (const f of failures) console.error(`- ${f}`);
    if (strike?.log) console.error(strike.log().slice(-1500));
    process.exitCode = 1;
  } else {
    console.log(`webui-sidechat-uat ok shots=${shots.length} video=${videoPath}`);
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
