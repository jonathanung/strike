// Scripted cockpit pass: keyboard palette, approval, composer, delegated
// child, session switch, reconnect. Assertions accumulate so one run reports
// every miss instead of stopping at the first.

import { mkdir, readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { delay } from "./browser.mjs";
import {
  DARK_TOKENS,
  GOLDEN_MEAN_ABS_MAX,
  LIGHT_TOKENS,
  missingLabels,
  tokenMismatch,
} from "./lib.mjs";
import { compareGolden, encodeVideo, writeBytes } from "./media.mjs";
import { runTaskRunnerPass } from "./task-runner.mjs";

const MARKER = "uat-marker-alpha";
const CHILD_OBJECTIVE = "Summarize the working directory";
const CHILD_NAME = "uat-child";

const KEYS = {
  k: { key: "k", code: "KeyK", vk: 75 },
  Enter: { key: "Enter", code: "Enter", vk: 13 },
  Escape: { key: "Escape", code: "Escape", vk: 27 },
  ArrowLeft: { key: "ArrowLeft", code: "ArrowLeft", vk: 37 },
};

export async function runScenario({ page, baseURL, outDir, video, goldens, updateGoldens, controls }) {
  const failures = [];
  const shots = [];
  const log = (msg) => console.log(`uat: ${msg}`);
  const shotDir = join(outDir, "screenshots");
  const domDir = join(outDir, "dom");
  const frameDir = join(outDir, "frames");
  await mkdir(shotDir, { recursive: true });
  await mkdir(domDir, { recursive: true });
  if (video) await mkdir(frameDir, { recursive: true });
  let frame = 0;
  let lastFrame = 0;
  const check = (ok, message) => {
    if (!ok) failures.push(message);
  };

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

  const grabFrame = async (force = false, copies = 1) => {
    if (!video || page.size?.width !== 1440) return;
    const now = Date.now();
    if (!force && now - lastFrame < 400) return;
    lastFrame = now;
    const shot = await page.send("Page.captureScreenshot", { format: "jpeg", quality: 55 });
    const bytes = Buffer.from(shot.data, "base64");
    for (let i = 0; i < copies; i++) {
      frame += 1;
      await writeBytes(join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`), bytes);
    }
  };

  const bodyText = () => page.evaluate("document.body ? document.body.innerText : ''");
  const tokens = () =>
    page.evaluate(`(() => {
      const cs = getComputedStyle(document.documentElement);
      return { acid: cs.getPropertyValue("--acid").trim(), ground: cs.getPropertyValue("--ground").trim() };
    })()`);

  const waitUntil = async (expression, timeoutMs, label) => {
    const deadline = Date.now() + timeoutMs;
    let last = "";
    while (Date.now() < deadline) {
      last = await page.evaluate(expression);
      if (last) return last;
      await grabFrame();
      await delay(200);
    }
    const text = await bodyText();
    failures.push(`timed out waiting for ${label}; page starts: ${String(text).slice(0, 360)}`);
    return null;
  };

  const click = async (selector, text) => {
    const ok = await page.evaluate(`(() => {
      const want = ${JSON.stringify(text)};
      const nodes = [...document.querySelectorAll(${JSON.stringify(selector)})];
      const el = nodes.find((n) => (n.innerText || "").replace(/\\s+/g, " ").includes(want));
      if (!el) return false;
      el.click();
      return true;
    })()`);
    check(ok, `no clickable ${selector} containing ${JSON.stringify(text)}`);
    await grabFrame(true);
    return ok;
  };

  const press = async (name, modifiers = 0) => {
    const def = KEYS[name];
    const common = {
      key: def.key,
      code: def.code,
      windowsVirtualKeyCode: def.vk,
      nativeVirtualKeyCode: def.vk,
      modifiers,
    };
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
    await delay(80);
    await grabFrame();
  };

  const saveShot = async (name) => {
    const png = await page.send("Page.captureScreenshot", { format: "png" });
    const pngPath = join(shotDir, `${name}.png`);
    await writeBytes(pngPath, Buffer.from(png.data, "base64"));
    const text = await bodyText();
    const tok = await tokens();
    const dom = {
      name,
      viewport: page.size,
      tokens: tok,
      text: String(text).slice(0, 4000),
    };
    await writeFile(join(domDir, `${name}.json`), JSON.stringify(dom, null, 2));
    shots.push(name);
    if (goldens) {
      const repoGolden = new URL(`./goldens/${name}.rgb`, import.meta.url);
      const result = await compareGolden(pngPath, repoGolden, updateGoldens);
      if (result.missing) failures.push(`golden missing for ${name} (run --update-goldens)`);
      else if (!result.updated && result.diff > GOLDEN_MEAN_ABS_MAX) {
        failures.push(`golden ${name} mean abs diff ${result.diff.toFixed(2)} > ${GOLDEN_MEAN_ABS_MAX}`);
      }
    }
    await grabFrame(true, 4);
    return pngPath;
  };

  const sizes = {
    desk1024: { width: 1024, height: 594, tag: "1024" },
    desk1440: { width: 1440, height: 900, tag: "1440" },
    mobile: { width: 390, height: 844, tag: "mobile" },
  };

  const shoot = async (name, which) => {
    for (const key of which) {
      const size = sizes[key];
      await viewport(size.width, size.height);
      await delay(120);
      await saveShot(`${name}-${size.tag}`);
    }
    await viewport(1440, 900);
  };

  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Emulation.setFocusEmulationEnabled", { enabled: true });
  await viewport(1440, 900);
  await page.send("Page.navigate", { url: baseURL });

  const booted = await waitUntil(
    `(() => {
      const text = document.body ? document.body.innerText : "";
      const acid = getComputedStyle(document.documentElement).getPropertyValue("--acid").trim();
      return text.includes("Run a command") && text.includes("Activity") && acid === "#7c3aed";
    })()`,
    30000,
    "cockpit boot",
  );
  if (!booted) return { failures, shots, videoPath: "" };
  log("booted");

  const text = await bodyText();
  const tok = await tokens();
  const fixture = JSON.parse(
    await readFile(new URL("./fixtures/shell-dark.json", import.meta.url), "utf8"),
  );
  const missing = missingLabels(text, fixture.mustInclude);
  check(missing.length === 0, `shell labels missing: ${missing.join(", ")}`);
  const tokenMiss = tokenMismatch(tok, fixture.tokens);
  check(tokenMiss.length === 0, `dark tokens ${tokenMiss.join("; ")}`);
  for (const [sel, min] of Object.entries(fixture.selectors)) {
    const count = await page.evaluate(`document.querySelectorAll(${JSON.stringify(sel)}).length`);
    check(count >= min, `${sel} count ${count} < ${min}`);
  }

  await shoot("shell-empty", ["desk1024", "desk1440", "mobile"]);

  // Keyboard path: Ctrl+K opens the palette, "plan" filters to Approval: Plan, Enter applies it.
  await press("k", 2);
  const palette = await waitUntil(
    `Boolean(document.querySelector(".palette"))`,
    4000,
    "command palette",
  );
  if (palette) {
    await page.evaluate(`document.querySelector(".palette input")?.focus()`);
    await page.send("Input.insertText", { text: "plan" });
    await delay(200);
    let filtered = await page.evaluate(`(() => {
      const input = document.querySelector(".palette input");
      return Boolean(input && input.value.toLowerCase().includes("plan"));
    })()`);
    if (!filtered) {
      filtered = await page.evaluate(`(() => {
        const input = document.querySelector(".palette input");
        if (!input) return false;
        const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
        proto.set.call(input, "plan");
        input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "plan" }));
        return input.value === "plan";
      })()`);
    }
    check(filtered, "palette query did not accept plan");
    await shoot("shell-palette", ["desk1024", "desk1440"]);
    await page.evaluate(`document.querySelector(".palette input")?.focus()`);
    await press("Enter");
    const planned = await waitUntil(
      `document.body && document.body.innerText.includes("Approval · Plan")`,
      4000,
      "approval mode Plan",
    );
    check(Boolean(planned), "keyboard palette did not set Approval · Plan");
  }

  await click("button.chip", "Approval");
  await waitUntil(
    `Boolean(document.querySelector(".menu") && document.querySelector(".menu").innerText.includes("Yolo"))`,
    4000,
    "approval menu",
  );
  await shoot("shell-approval", ["desk1024", "desk1440"]);
  await press("Escape");

  await click(".side-tabs button", "Files");
  await waitUntil(
    `document.body && (document.body.innerText.includes("Working directory") || document.body.innerText.includes("Click or drag"))`,
    4000,
    "files tab",
  );
  await shoot("shell-files", ["desk1024"]);

  await click(".side-tabs button", "Changes");
  await waitUntil(
    `Boolean(document.querySelector(".changes"))`,
    4000,
    "changes tab",
  );
  await shoot("shell-changes", ["desk1024"]);

  await click(".side-tabs button", "Activity");
  await delay(200);

  log("composer");
  const typed = await page.evaluate(`(() => {
    const ta = document.querySelector("textarea");
    if (!ta) return false;
    ta.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, ${JSON.stringify(MARKER)});
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(MARKER)} }));
    return ta.value === ${JSON.stringify(MARKER)};
  })()`);
  check(typed, "composer did not accept the marker");
  await shoot("shell-composer", ["desk1024", "desk1440", "mobile"]);
  await click("button.send", "Send");
  const echoed = await waitUntil(
    `(() => {
      const t = document.querySelector(".transcript");
      return t && t.innerText.includes(${JSON.stringify(MARKER)}) && t.innerText.includes("You said:");
    })()`,
    20000,
    "echoed composer message",
  );
  check(Boolean(echoed), "transcript never showed the echo");
  await shoot("shell-composer-sent", ["desk1024", "desk1440"]);

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
  check(Boolean(root), "no live root to spawn a child");
  if (root) {
    const spawned = await fetch(`${baseURL}/v1/ops?root=${encodeURIComponent(root)}`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        type: "team.spawn",
        data: {
          objective: CHILD_OBJECTIVE,
          name: CHILD_NAME,
          idempotencyKey: "uat-spawn-1",
        },
      }),
    });
    const spawnBody = await spawned.text();
    check(spawned.ok, `team.spawn HTTP ${spawned.status} ${spawnBody.slice(0, 400)}`);
    const blocked = await waitUntil(
      `(() => {
        const row = [...document.querySelectorAll(".activity-row")].find((el) =>
          /blocked/i.test(el.innerText) && (${JSON.stringify(CHILD_NAME)} === "" || el.innerText.includes(${JSON.stringify(CHILD_NAME)}) || el.innerText.includes(${JSON.stringify(CHILD_OBJECTIVE)}))
        );
        return Boolean(row);
      })()`,
      45000,
      "blocked delegated child",
    );
    check(Boolean(blocked), "activity panel did not show the blocked child");
    await click(".activity-row button.activity-main", CHILD_OBJECTIVE);
    await delay(250);
    await shoot("shell-activity", ["desk1024", "desk1440"]);
  }

  const before = await page.evaluate(
    `document.querySelector(".session-row.on .name")?.textContent || ""`,
  );
  const countBefore = await page.evaluate(`document.querySelectorAll(".session-row").length`);
  await click(".rail button.secondary", "New");
  const switched = await waitUntil(
    `(() => {
      const n = document.querySelectorAll(".session-row").length;
      const name = document.querySelector(".session-row.on .name")?.textContent || "";
      return n > ${countBefore} && name && name !== ${JSON.stringify(before)};
    })()`,
    8000,
    "new session",
  );
  check(Boolean(switched), "New did not open another live session");
  await shoot("shell-sessions", ["desk1024", "desk1440"]);
  await page.evaluate(`(() => {
    const other = [...document.querySelectorAll(".session-row")].find((el) => !el.classList.contains("on"));
    other?.querySelector("button.session-main")?.click();
  })()`);
  const back = await waitUntil(
    `(() => {
      const name = document.querySelector(".session-row.on .name")?.textContent || "";
      return name && name === ${JSON.stringify(before)};
    })()`,
    6000,
    "switch back to the first session",
  );
  check(Boolean(back), "clicking the other session did not switch");
  await press("ArrowLeft", 1);
  await delay(500);
  const stillHere = await page.evaluate(
    `document.body && document.body.innerText.includes("Run a command")`,
  );
  if (!stillHere) {
    failures.push("Alt+Left left the cockpit (headless Chrome treats it as history back)");
    await page.send("Page.navigate", { url: baseURL });
    await waitUntil(
      `document.body && document.body.innerText.includes("Run a command")`,
      15000,
      "return to cockpit after Alt+Left",
    );
  } else {
    const afterAlt = await page.evaluate(
      `document.querySelector(".session-row.on .name")?.textContent || ""`,
    );
    check(Boolean(afterAlt) && afterAlt !== before, "Alt+Left did not switch sessions");
  }

  await click(".segmented button", "Daemon");
  await waitUntil(
    `document.body && document.body.innerText.includes("Saved daemons")`,
    4000,
    "daemon view",
  );
  await shoot("shell-daemon", ["desk1440", "desk1024"]);
  await click(".segmented button", "Chat");

  await press("k", 2);
  const schemePalette = await waitUntil(
    `document.body && document.body.innerText.includes("Scheme: Light")`,
    4000,
    "palette scheme rows",
  );
  if (schemePalette) {
    await click(".palette-row", "Scheme: Light");
    const light = await waitUntil(
      `getComputedStyle(document.documentElement).getPropertyValue("--ground").trim() === "#ffffff"`,
      4000,
      "light scheme",
    );
    if (light) {
      const lightTok = await tokens();
      const lightMiss = tokenMismatch(lightTok, LIGHT_TOKENS);
      check(lightMiss.length === 0, `light tokens ${lightMiss.join("; ")}`);
      await shoot("shell-light", ["desk1024", "desk1440"]);
    }
    await press("k", 2);
    await waitUntil(
      `document.body && document.body.innerText.includes("Scheme: Dark")`,
      4000,
      "palette to restore dark",
    );
    await click(".palette-row", "Scheme: Dark");
    await waitUntil(
      `getComputedStyle(document.documentElement).getPropertyValue("--ground").trim() === ${JSON.stringify(DARK_TOKENS.ground)}`,
      4000,
      "restore dark scheme",
    );
  }
  await press("Escape");

  await runTaskRunnerPass({ page, baseURL, check, shoot, click, waitUntil, log });

  if (controls) {
    await controls.stop();
    const down = await waitUntil(
      `(() => {
        const retry = [...document.querySelectorAll(".notice button")].some((b) => (b.innerText || "").includes("Retry"));
        const text = document.body ? document.body.innerText : "";
        const dead = text.includes("Unreachable") || text.includes("Could not reach the daemon");
        return retry && dead;
      })()`,
      15000,
      "unreachable notice with Retry",
    );
    check(Boolean(down), "stopping the daemon did not show Unreachable and Retry");
    await shoot("shell-reconnect-error", ["desk1024", "desk1440"]);
    // The cockpit polls refresh_meta every 2s. Once the daemon is back that
    // poll clears the notice, so Retry can disappear before the click.
    // Freeze intervals, then click Retry ourselves.
    await page.evaluate(`(() => {
      const top = window.setInterval(() => {}, 1e9);
      for (let i = 1; i <= top; i++) window.clearInterval(i);
    })()`);
    await controls.start();
    const retried = await page.evaluate(`(() => {
      const btn = [...document.querySelectorAll(".notice button")].find((b) => (b.innerText || "").includes("Retry"));
      if (!btn) return false;
      btn.click();
      return true;
    })()`);
    check(retried, "Retry was not clicked");
    if (retried) {
      const backOnline = await waitUntil(
        `(() => {
          const text = document.body ? document.body.innerText : "";
          return text.includes("Connected") && !text.includes("Unreachable") && !text.includes("Could not reach the daemon");
        })()`,
        10000,
        "daemon reconnected",
      );
      check(Boolean(backOnline), "Retry did not restore Connected");
    }
    await shoot("shell-reconnect-ok", ["desk1024", "desk1440"]);
  }

  let videoPath = "";
  if (video && frame > 0) {
    videoPath = join(outDir, "uat.mp4");
    await encodeVideo(frameDir, videoPath, 4);
  }

  return { failures, shots, videoPath, frames: frame };
}
