// Scripted cockpit pass: keyboard palette, approval, composer, delegated
// child, session switch, reconnect. Per-stream files in ./scenarios run
// after this pass when --suite is full (the default). Assertions accumulate
// so one run reports every miss instead of stopping at the first.

import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { delay } from "./browser.mjs";
import { DARK_TOKENS, LIGHT_TOKENS, tokenMismatch, waitForLabels } from "./lib.mjs";
import { encodeVideo } from "./media.mjs";
import { loadScenario, loadScenarios } from "./scenarios.mjs";
import { createSession } from "./session.mjs";
import { capturePhoneFrame, runPhoneScreens } from "./phone.mjs";

const MARKER = "uat-marker-alpha";
const FAKE_KEY = "sk-uat-fake-key-not-real";

async function headerAt(page, check, viewport, grab) {
  const widths = [
    [390, 844],
    [768, 1024],
    [1024, 768],
    [1440, 900],
  ];
  for (const [width, height] of widths) {
    await viewport(width, height);
    await delay(180);
    const box = await page.evaluate(`(() => {
      const h = document.querySelector('[data-nav="header"]');
      if (!h) return null;
      const wrapped = h.scrollHeight > h.clientHeight + 2;
      const labels = [...h.querySelectorAll('[data-nav="primary"] button, .nav-menu-btn')].map((b) => ({
        text: (b.innerText || "").trim(),
        clipped: b.scrollWidth > b.clientWidth + 1,
      }));
      return {
        height: Math.round(h.getBoundingClientRect().height),
        overflow: h.scrollWidth > h.clientWidth + 1,
        wrapped,
        labels,
      };
    })()`);
    check(Boolean(box), `header missing at ${width}`);
    if (!box) continue;
    check(!box.wrapped, `header wrapped at ${width} (height ${box.height})`);
    check(!box.overflow, `header overflowed at ${width}`);
    check(box.height > 0 && box.height <= 56, `header height ${box.height} at ${width} is outside 1..56`);
    const clipped = box.labels.filter((row) => row.clipped).map((row) => row.text);
    check(clipped.length === 0, `header label clipped at ${width}: ${clipped.join(", ")}`);
    await grab(`header-${width}`);
  }
  await viewport(1440, 900);
}

async function providerFlow(page, check, waitUntil, grab) {
  const opened = await page.evaluate(`(() => {
    const more = document.querySelector('[data-nav="more"]');
    if (!more) return false;
    more.click();
    const item = document.querySelector('[data-nav="providers"]');
    if (!item) return false;
    item.click();
    return true;
  })()`);
  check(opened, "Menu did not expose Providers");
  const listed = await waitUntil(
    `(() => {
      const root = document.querySelector("[data-providers-root]");
      const names = [...document.querySelectorAll("[data-provider]")].map((el) => el.getAttribute("data-provider"));
      return Boolean(root && names.includes("echo") && names.length >= 2);
    })()`,
    8000,
    "provider list",
  );
  check(Boolean(listed), "provider list did not include echo and another daemon provider");
  const echoState = await page.evaluate(
    `document.querySelector('[data-provider="echo"]')?.innerText || ""`,
  );
  check(String(echoState).includes("Connected"), `echo was not Connected: ${echoState}`);
  for (const [width, height] of [
    [390, 844],
    [768, 1024],
    [1024, 768],
    [1440, 900],
  ]) {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: width < 500,
      screenWidth: width,
      screenHeight: height,
    });
    await delay(160);
    await grab(`providers-list-${width}`);
  }

  const typed = await page.evaluate(`(() => {
    const card = document.querySelector('[data-provider="openai"]');
    const input = card && card.querySelector("[data-provider-key]");
    if (!input) return false;
    const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(FAKE_KEY)});
    input.dispatchEvent(new Event("input", { bubbles: true }));
    return input.value === ${JSON.stringify(FAKE_KEY)};
  })()`);
  check(typed, "openai key field did not take the paste");
  await page.evaluate(
    `document.querySelector('[data-provider="openai"] [data-save-key]')?.click()`,
  );
  const stored = await waitUntil(
    `(() => {
      const note = document.querySelector('[data-provider="openai"] [data-provider-note]');
      return Boolean(note && note.innerText.includes("not shown again"));
    })()`,
    8000,
    "key stored",
  );
  check(Boolean(stored), "saving the openai key did not confirm");
  const connected = await waitUntil(
    `(() => {
      const state = document.querySelector('[data-provider="openai"] [data-provider-state]');
      return Boolean(state && state.textContent.trim() === "Connected");
    })()`,
    8000,
    "openai connected after save",
  );
  check(Boolean(connected), "openai did not show Connected after the key was stored");
  const leaked = await page.evaluate(`(() => {
    const body = document.body ? document.body.innerText : "";
    let store = "";
    try { store = JSON.stringify(localStorage); } catch (err) { store = ""; }
    const key = ${JSON.stringify(FAKE_KEY)};
    return body.includes(key) || store.includes(key);
  })()`);
  check(!leaked, "the pasted key is in the page text or localStorage");
  await page.evaluate(
    `document.querySelector('[data-provider="openai"] [data-test-provider]')?.click()`,
  );
  const tested = await waitUntil(
    `(() => {
      const note = document.querySelector('[data-provider="openai"] [data-provider-note]');
      const text = note ? note.innerText : "";
      return text.includes("Listed") && !text.includes(${JSON.stringify(FAKE_KEY)});
    })()`,
    15000,
    "provider test",
  );
  check(Boolean(tested), "Test did not report a result");
  for (const [width, height] of [
    [390, 844],
    [768, 1024],
    [1024, 768],
    [1440, 900],
  ]) {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: width < 500,
      screenWidth: width,
      screenHeight: height,
    });
    await delay(120);
    await grab(`providers-key-${width}`);
  }
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1440,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
    screenWidth: 1440,
    screenHeight: 900,
  });
  await page.evaluate(
    `document.querySelector('[data-provider="openai"] [data-remove-key]')?.click()`,
  );
  const removed = await waitUntil(
    `(() => {
      const card = document.querySelector('[data-provider="openai"]');
      const note = card && card.querySelector("[data-provider-note]");
      const text = note ? note.innerText : "";
      const state = card ? card.querySelector("[data-provider-state]")?.textContent || "" : "";
      return text.includes("Stored credential removed") || state.trim() === "Not connected";
    })()`,
    8000,
    "key removed",
  );
  check(Boolean(removed), "Remove stored key did not confirm");

  await page.evaluate(`document.querySelector('[data-nav="primary"] [data-view="chat"]')?.click()`);
  const chip = await waitUntil(
    `Boolean(document.querySelector("[data-model-chip]"))`,
    4000,
    "model chip after leaving providers",
  );
  check(Boolean(chip), "model chip missing after returning to chat");
  await page.evaluate(`document.querySelector("[data-model-chip]")?.click()`);
  const menu = await waitUntil(
    `Boolean(document.querySelector("[data-model-menu] [data-add-provider]"))`,
    4000,
    "model menu add provider",
  );
  check(Boolean(menu), "model menu is missing Add provider");
  await grab("model-menu-add-provider-1440");
  await page.evaluate(`document.querySelector("[data-model-menu] [data-add-provider]")?.click()`);
  const back = await waitUntil(
    `Boolean(document.querySelector("[data-providers-root]"))`,
    4000,
    "add provider from the model menu",
  );
  check(Boolean(back), "Add provider in the model menu did not open Providers");
  await page.evaluate(`document.querySelector('[data-nav="primary"] [data-view="chat"]')?.click()`);
}
const CHILD_OBJECTIVE = "Summarize the working directory";
const CHILD_NAME = "uat-child";

const BOOT_EXPR = `(() => {
  const text = document.body ? document.body.innerText : "";
  const root = document.documentElement;
  if (!root) return false;
  const acid = getComputedStyle(root).getPropertyValue("--acid").trim();
  return text.includes("Menu") && text.includes("Activity") && acid === "#7c3aed";
})()`;

// The shell fixture requires "Connected". The chip stays "Checking" until
// /health returns, and the shell can paint before that fetch finishes.
const CONNECTED_EXPR = `(() => {
  const text = document.body ? document.body.innerText : "";
  return text.includes("Connected") && !text.includes("Unreachable") && !text.includes("Could not reach the daemon");
})()`;

export async function runScenario({ page, baseURL, outDir, video, goldens, updateGoldens, controls, suite = "full" }) {
  const session = await createSession({ page, outDir, video, goldens, updateGoldens });
  const { failures, shots, check, waitUntil, click, press, shoot, viewport, bodyText, tokens, log } = session;
  let phone;
  const cockpit = suite === "full" || suite === "smoke";

  await page.send("Page.enable");
  await page.send("Runtime.enable");
  await page.send("Emulation.setFocusEmulationEnabled", { enabled: true });
  await viewport(1440, 900);
  await page.send("Page.navigate", { url: baseURL });

  const booted = await waitUntil(BOOT_EXPR, 30000, "cockpit boot");
  if (!booted) return { failures, shots, videoPath: "" };
  log("booted");

  if (cockpit) {
    const online = await waitUntil(CONNECTED_EXPR, 15000, "gateway connected");
    check(Boolean(online), "gateway did not reach Connected");
    const fixture = JSON.parse(
      await readFile(new URL("./fixtures/shell-dark.json", import.meta.url), "utf8"),
    );
    // Wait for the shell labels on condition instead of snapshotting the
    // body text once. BOOT_EXPR above is satisfied by the pre-spawn stub
    // ("Run a command" + "Activity" + theme token), before the live
    // session has spawned and the shell has rendered states like
    // "Connected" — the one-shot read raced that render and failed
    // healthy builds ~1/3 of runs with `shell labels missing: Connected`.
    // The waiter (lib.mjs waitForLabels, the polling pattern PR #1378
    // established in scripts/webui-smoke.mjs) polls until every fixture
    // label is present or the deadline passes; it does not weaken the
    // check — labels that genuinely never appear still fail below, now
    // with a shell snapshot so a real failure is distinguishable from
    // the race. Deadline is env-tunable like webui-smoke's waits.
    const shellWaitRaw = Number(process.env.STRIKE_SMOKE_WAIT_MS || 30000);
    const shellWaitMs = Number.isFinite(shellWaitRaw) && shellWaitRaw > 0 ? shellWaitRaw : 30000;
    const { text, missing } = await waitForLabels(bodyText, fixture.mustInclude, {
      timeoutMs: shellWaitMs,
      intervalMs: 200,
    });
    let shellDiag = "";
    if (missing.length > 0) {
      let snapshot = "";
      try {
        snapshot = JSON.stringify(
          await page.evaluate(`(() => ({
            url: location.href,
            readyState: document.readyState,
            shell: document.querySelector(".shell")?.className || "",
            status: document.querySelector("[data-status]")?.getAttribute("data-status") || "",
            views: [...document.querySelectorAll("[data-view]")].map((e) => e.getAttribute("data-view")),
          }))()`),
        );
      } catch (err) {
        snapshot = `snapshot failed: ${err?.message || err}`;
      }
      shellDiag = ` (after polling ${shellWaitMs}ms; shell snapshot: ${snapshot}; page text: ${String(text).slice(0, 600)})`;
    }
    check(missing.length === 0, `shell labels missing: ${missing.join(", ")}${shellDiag}`);
    const tok = await tokens();
    const tokenMiss = tokenMismatch(tok, fixture.tokens);
    check(tokenMiss.length === 0, `dark tokens ${tokenMiss.join("; ")}`);
    for (const [sel, min] of Object.entries(fixture.selectors)) {
      const count = await page.evaluate(`document.querySelectorAll(${JSON.stringify(sel)}).length`);
      check(count >= min, `${sel} count ${count} < ${min}`);
    }

    const cardReady = await waitUntil(
      `(() => {
        const b = document.querySelector('[data-action="onboarding-continue"]');
        return !b || !b.disabled;
      })()`,
      8000,
      "onboarding ready",
    );
    if (cardReady && await page.evaluate(`Boolean(document.querySelector('[data-action="onboarding-continue"]'))`)) {
      await shoot("onboarding", ["mobile", "desk768", "desk1024", "desk1440"]);
      await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
      await waitUntil(
        `document.querySelector('[data-action="telemetry-note"]') == null`,
        4000,
        "onboarding dismissed",
      );
    }
    await page.evaluate(`document.querySelector('[data-action="open-settings"]')?.click()`);
    const settingsUp = await waitUntil(
      `document.body && document.body.innerText.includes("Keyboard")`,
      8000,
      "settings page",
    );
    if (settingsUp) {
      await shoot("settings", ["mobile", "desk768", "desk1024", "desk1440"]);
    }
    await page.evaluate(`document.querySelector('[data-nav="primary"] button')?.click()`);

    await shoot("shell-empty", ["desk1024", "desk1440", "mobile"]);
    const emptyAdd = await page.evaluate(
      `Boolean(document.querySelector(".sx-empty [data-add-provider]"))`,
    );
    check(emptyAdd, "empty transcript is missing Add provider");
    await headerAt(page, check, viewport, async (name) => {
      const png = await page.send("Page.captureScreenshot", { format: "png" });
      await writeFile(join(outDir, "screenshots", `${name}.png`), Buffer.from(png.data, "base64"));
      shots.push(name);
    });

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
      `document.body && document.body.innerText.includes("Menu")`,
    );
    if (!stillHere) {
      failures.push("Alt+Left left the cockpit (headless Chrome treats it as history back)");
      await page.send("Page.navigate", { url: baseURL });
      await waitUntil(
        `document.body && document.body.innerText.includes("Menu")`,
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
      `document.body && document.body.innerText.includes("This gateway")`,
      4000,
      "daemon view",
    );
    await shoot("shell-daemon", ["desk1440", "desk1024"]);
    await click(".segmented button", "Chat");
    await providerFlow(page, check, waitUntil, async (name) => {
      const png = await page.send("Page.captureScreenshot", { format: "png" });
      await writeFile(join(outDir, "screenshots", `${name}.png`), Buffer.from(png.data, "base64"));
      shots.push(name);
    });

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

    try {
      phone = await runPhoneScreens({ page, outDir, video, check, shots });
    } catch (err) {
      failures.push(`phone pass: ${err?.message || err}`);
    }

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
      await capturePhoneFrame(page, phone, outDir, shots, "phone-reconnect-error-390");
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
        const backOnline = await waitUntil(CONNECTED_EXPR, 10000, "daemon reconnected");
        check(Boolean(backOnline), "Retry did not restore Connected");
      }
      await shoot("shell-reconnect-ok", ["desk1024", "desk1440"]);
      await capturePhoneFrame(page, phone, outDir, shots, "phone-reconnect-ok-390");
    }
  }

  if (suite !== "smoke") {
    let mods = [];
    try {
      mods = suite === "full" ? await loadScenarios() : [await loadScenario(suite)];
    } catch (err) {
      failures.push(`${err?.message || err}`);
    }
    const ctx = {
      page,
      baseURL,
      controls,
      check,
      waitUntil,
      click,
      press,
      shoot,
      phone: session.phone,
      viewport,
      bodyText,
      tokens,
      log,
    };
    for (const mod of mods) {
      if (mod.error) {
        failures.push(`scenario ${mod.name}: ${mod.error?.message || mod.error}`);
        continue;
      }
      if (cockpit) {
        await page.send("Page.navigate", { url: baseURL });
        const again = await waitUntil(BOOT_EXPR, 30000, `reload before ${mod.name}`);
        if (!again) continue;
      }
      // Device metrics survive Page.navigate. A scenario that left the
      // phone width must not leak into the next file.
      await viewport(1440, 900);
      log(`scenario ${mod.name}`);
      try {
        await mod.scenario(ctx);
      } catch (err) {
        failures.push(`scenario ${mod.name}: ${err?.message || err}`);
      }
    }
  }

  let videoPath = "";
  const frame = session.frames();
  if (video && frame > 0) {
    videoPath = join(outDir, "uat.mp4");
    await encodeVideo(session.frameDir, videoPath, 4);
  }

  let phoneVideo = "";
  if (phone) {
    try {
      phoneVideo = await phone.finish(video);
    } catch (err) {
      failures.push(`phone video: ${err?.message || err}`);
    }
  }

  return { failures, shots, videoPath, phoneVideo, frames: frame };
}
