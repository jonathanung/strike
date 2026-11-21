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

// #1533 / #1538: a card offers device-code / OAuth sign-in only when the
// daemon lists it in `methods` (today: xai device+oauth, openai oauth). The
// flows hit the real daemon endpoints; the vendor step cannot finish here,
// so the check is start -> pending (link, code or paste field) -> cancel,
// or an honest daemon error at the card. Never a "Signed in".
async function signinFlow(page, check, waitUntil, grab) {
  const offered = await page.evaluate(`(() => {
    const pick = (name) => {
      const card = document.querySelector('[data-provider="' + name + '"]');
      if (!card) return null;
      return [...card.querySelectorAll("[data-signin]")].map((el) => el.getAttribute("data-signin")).sort().join(",");
    };
    return { xai: pick("xai"), openai: pick("openai"), anthropic: pick("anthropic"), echo: pick("echo") };
  })()`);
  check(offered.xai === "device,oauth", `xai sign-in actions were ${offered.xai}, want device,oauth`);
  check(offered.openai === "oauth", `openai sign-in actions were ${offered.openai}, want oauth`);
  check(offered.anthropic === "", `anthropic offered sign-in it does not advertise: ${offered.anthropic}`);
  check(offered.echo === "", `echo offered sign-in it does not advertise: ${offered.echo}`);

  for (const [provider, method] of [
    ["openai", "oauth"],
    ["xai", "device"],
  ]) {
    const card = `[data-provider="${provider}"]`;
    await page.evaluate(`document.querySelector('${card} [data-signin="${method}"]')?.click()`);
    const outcome = await waitUntil(
      `(() => {
        const flow = document.querySelector('${card} [data-signin-flow="${method}"]');
        const note = document.querySelector('${card} [data-provider-note]');
        return Boolean(flow) || Boolean(note && note.innerText.trim());
      })()`,
      20000,
      `${provider} ${method} sign-in start`,
    );
    check(Boolean(outcome), `${provider} ${method} sign-in showed neither a flow nor an error`);
    const state = await page.evaluate(`(() => {
      const flow = document.querySelector('${card} [data-signin-flow="${method}"]');
      if (!flow) return { phase: "" };
      return {
        phase: flow.getAttribute("data-signin-phase") || "",
        url: flow.querySelector("[data-signin-url]")?.textContent || "",
        code: flow.querySelector("[data-signin-code]")?.textContent || "",
        paste: Boolean(flow.querySelector("[data-signin-paste]")),
        cancel: Boolean(flow.querySelector("[data-signin-cancel]")),
      };
    })()`);
    check(state.phase !== "completed", `${provider} ${method} claimed a completed sign-in in UAT`);
    if (state.phase !== "pending") {
      // Vendor unreachable from this box: the daemon's error is the evidence.
      continue;
    }
    check(/^https?:\/\//.test(state.url), `${provider} ${method} pending flow has no link`);
    check(state.cancel, `${provider} ${method} pending flow has no Cancel`);
    if (method === "oauth") check(state.paste, `${provider} oauth pending flow has no paste field`);
    if (method === "device") check(state.code.trim().length > 0, `${provider} device flow shows no user code`);
    const stored = await page.evaluate(`(() => {
      let store = "";
      try { store = JSON.stringify(localStorage); } catch (err) { store = ""; }
      const url = ${JSON.stringify(state.url)};
      return url !== "" && store.includes(url);
    })()`);
    check(!stored, `${provider} ${method} flow link was written to localStorage`);
    await grab(`providers-signin-${method}-pending-1440`);
    await page.evaluate(`document.querySelector('${card} [data-signin-cancel]')?.click()`);
    const canceled = await waitUntil(
      `document.querySelector('${card} [data-signin-flow="${method}"]')?.getAttribute("data-signin-phase") === "canceled"`,
      8000,
      `${provider} ${method} cancel`,
    );
    check(Boolean(canceled), `${provider} ${method} Cancel did not reach the canceled state`);
    await page.evaluate(`document.querySelector('${card} [data-signin-dismiss]')?.click()`);
  }
}

// #1508: the composer action row must never scroll sideways or clip a
// chip, Send must stay on screen, and the transcript must not render
// underneath the composer — at 1024x594 especially.
async function composerAt(page, check, viewport, grab) {
  const sizes = [
    [1024, 594],
    [1440, 900],
    [390, 844],
    [360, 800],
  ];
  for (const [width, height] of sizes) {
    await viewport(width, height);
    await delay(180);
    const fit = await page.evaluate(`(() => {
      const composer = document.querySelector(".chat > .composer");
      const bar = document.querySelector(".composer-bar");
      const chips = document.querySelector(".composer-chips");
      const send = document.querySelector("#send-primary");
      if (!composer || !bar || !chips || !send) return null;
      const box = composer.getBoundingClientRect();
      if (box.width === 0 || box.height === 0) return { hidden: true };
      const clipped = [...chips.children]
        .filter((el) => el.getBoundingClientRect().width > 0)
        .filter((el) => {
          const r = el.getBoundingClientRect();
          return r.left < box.left - 1 || r.right > box.right + 1;
        })
        .map((el) => (el.innerText || el.getAttribute("aria-label") || "").trim());
      const s = send.getBoundingClientRect();
      const t = document.querySelector(".chat .transcript");
      const tb = t ? t.getBoundingClientRect() : null;
      return {
        hidden: false,
        phone: document.body.getAttribute("data-phone") === "1",
        barScroll: bar.scrollWidth > bar.clientWidth + 1,
        chipScroll: chips.scrollWidth > chips.clientWidth + 1,
        clipped,
        sendOnScreen: s.width > 0 && s.left >= 0 && s.right <= window.innerWidth + 1 && s.bottom <= window.innerHeight + 1,
        overlap: tb && tb.height > 0 ? Math.round(tb.bottom - box.top) : 0,
      };
    })()`);
    check(Boolean(fit), `composer missing at ${width}x${height}`);
    if (!fit || fit.hidden) continue;
    // Phones keep the #1332 chip scroller (wrapping would push the
    // composer out of a keyboard-open viewport); Send must still be on
    // screen there. Desktop layouts must not scroll or clip at all.
    if (!fit.phone) {
      check(!fit.barScroll && !fit.chipScroll, `composer row scrolls sideways at ${width}x${height}`);
      check(fit.clipped.length === 0, `composer chip clipped at ${width}x${height}: ${fit.clipped.join(", ")}`);
    } else {
      check(!fit.barScroll, `composer bar (Send column) scrolls sideways at ${width}x${height}`);
    }
    check(fit.sendOnScreen, `Send is off screen at ${width}x${height}`);
    // Phones use a sticky composer over a scrolling pane (#1332); the
    // overlap rule is for the in-flow desktop column.
    if (width > 720) {
      check(fit.overlap <= 1, `transcript renders ${fit.overlap}px under the composer at ${width}x${height}`);
    }
    await grab(`composer-${width}`);
  }
  await viewport(1440, 900);
}

async function providerFlow(page, check, waitUntil, grab) {
  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  const opened = await waitUntil(
    `Boolean(document.querySelector('[data-command-id="providers"]'))`,
    4000,
    "providers command",
  );
  if (opened) {
    await page.evaluate(`document.querySelector('[data-command-id="providers"]')?.click()`);
  }
  check(Boolean(opened), "palette did not expose Providers");
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
  await signinFlow(page, check, waitUntil, grab);
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
  // #1531: a stored key is not a connection. The badge must say the key is
  // stored-but-untested; "Connected" here was the honesty bug.
  const storedState = await waitUntil(
    `(() => {
      const state = document.querySelector('[data-provider="openai"] [data-provider-state]');
      return Boolean(state && state.textContent.trim() === "Key stored — not tested");
    })()`,
    8000,
    "openai stored-not-tested after save",
  );
  check(Boolean(storedState), "openai did not show 'Key stored — not tested' after the key was stored");
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
  // #1531: Test key runs a real probe against the vendor. The fake key can
  // only be rejected (vendor reachable) or unverifiable (offline UAT box);
  // it must never report a catalog listing or a verified/Connected state.
  const tested = await waitUntil(
    `(() => {
      const note = document.querySelector('[data-provider="openai"] [data-provider-note]');
      const text = note ? note.innerText : "";
      const honest = text.includes("Key rejected") || text.includes("Could not verify the key");
      return honest && !text.includes("Listed") && !text.includes(${JSON.stringify(FAKE_KEY)});
    })()`,
    20000,
    "provider key probe",
  );
  check(Boolean(tested), "Test key did not report an honest probe verdict for the fake key");
  const stillNotConnected = await page.evaluate(
    `(() => {
      const state = document.querySelector('[data-provider="openai"] [data-provider-state]');
      return Boolean(state) && state.textContent.trim() !== "Connected";
    })()`,
  );
  check(Boolean(stillNotConnected), "openai showed Connected after the fake key failed probing");
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
  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector('[data-nav="palette-dialog"] input'))`, 4000, "remove-key palette");
  await page.evaluate(`(() => {
    const input = document.querySelector('[data-nav="palette-dialog"] input');
    const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
    proto.set.call(input, "Remove stored key");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "Remove stored key" }));
    return true;
  })()`);
  await waitUntil(
    `[...document.querySelectorAll("[data-command-id='providers.remove-key']")].some((el) => (el.innerText || "").includes("Remove stored key…"))`,
    4000,
    "remove-key command",
  );
  await page.evaluate(`(() => {
    const row = [...document.querySelectorAll("[data-command-id='providers.remove-key']")].find((el) => (el.innerText || "").includes("Remove stored key…"));
    if (row) row.click();
    return true;
  })()`);
  await waitUntil(
    `Boolean(document.querySelector('[data-choice-id="openai"]'))`,
    4000,
    "remove-key choices",
  );
  await page.evaluate(`document.querySelector('[data-choice-id="openai"]')?.click()`);
  const armed = await waitUntil(
    `Boolean(document.querySelector('[data-confirm="accept"]'))`,
    4000,
    "remove-key confirm",
  );
  check(Boolean(armed), "Remove stored key did not ask for confirmation");
  await page.evaluate(`document.querySelector('[data-confirm="accept"]')?.click()`);
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
    `document.body && document.body.innerText.includes("Model ·")`,
    4000,
    "model status after leaving providers",
  );
  check(Boolean(chip), "model status missing after returning to chat");
  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  const menu = await waitUntil(
    `Boolean(document.querySelector('[data-command-id="providers.add"]'))`,
    4000,
    "palette add provider",
  );
  check(Boolean(menu), "palette is missing Add provider");
  await grab("model-menu-add-provider-1440");
  await page.evaluate(`document.querySelector(".palette-backdrop")?.click()`);
  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  await page.evaluate(`(() => {
    const input = document.querySelector('[data-nav="palette-dialog"] input');
    if (!input) return false;
    const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
    proto.set.call(input, "key");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "key" }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    return true;
  })()`);
  const back = await waitUntil(
    `Boolean(document.querySelector("[data-providers-root]"))`,
    4000,
    "providers from palette key",
  );
  check(Boolean(back), "palette query key did not open Providers");
  await page.evaluate(`document.querySelector('[data-nav="primary"] [data-view="chat"]')?.click()`);
}
const CHILD_OBJECTIVE = "Summarize the working directory";
const CHILD_NAME = "uat-child";

const BOOT_EXPR = `(() => {
  const text = document.body ? document.body.innerText : "";
  const root = document.documentElement;
  if (!root) return false;
  const acid = getComputedStyle(root).getPropertyValue("--acid").trim();
  return text.includes("Run a command") && text.includes("Activity") && acid === "#7c3aed";
})()`;

// The shell fixture requires "Connected". The chip stays "Checking" until
// /health returns, and the shell can paint before that fetch finishes.
// Since #1590 the header prints no version/gateway text (A6/A7): the
// connection state ("Connected · v…" / "Unreachable") is the brand
// mark's title, so read it there instead of the body text.
const BRAND_TITLE_EXPR = `(document.querySelector(".brand-mark")?.getAttribute("title") || "")`;
const CONNECTED_EXPR = `(() => {
  const text = document.body ? document.body.innerText : "";
  const link = ${BRAND_TITLE_EXPR};
  return link.includes("Connected") && !link.includes("Unreachable") && !text.includes("Unreachable") && !text.includes("Could not reach the daemon");
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
    // "Connected" is the brand mark's title since #1590, not body text;
    // append it so that fixture label still has to be genuinely painted.
    const shellText = async () =>
      `${await bodyText()}\n${await page.evaluate(BRAND_TITLE_EXPR)}`;
    const { text, missing } = await waitForLabels(shellText, fixture.mustInclude, {
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
    // Steer and Queue left the composer bar. The primary button is Send
    // until a turn is running; both words stay reachable on one command.
    await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
    const steerQueue = await waitUntil(
      `(() => {
        const el = document.querySelector('[data-command-id="enter.toggle"]');
        const text = el ? (el.innerText || "") : "";
        return text.includes("Steer") && text.includes("Queue");
      })()`,
      4000,
      "steer/queue palette command",
    );
    check(Boolean(steerQueue), "palette is missing Toggle Enter behaviour: Steer / Queue");
    await page.evaluate(`document.querySelector(".palette-backdrop")?.click()`);
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
      `document.body && document.body.innerText.includes("Appearance") && document.body.innerText.includes("Type size")`,
      8000,
      "settings page",
    );
    if (settingsUp) {
      await shoot("settings", ["mobile", "desk768", "desk1024", "desk1440"]);
      // #1584: Settings → Isolation renders the daemon's real posture (host
      // mode is a supported rung), or a visible error — never an empty panel.
      const isolation = await waitUntil(
        `Boolean(document.querySelector('#settings-isolation .container-posture-label, #settings-isolation .banner'))`,
        20000,
        "isolation panel",
      );
      const posture = await page.evaluate(`(() => {
        const label = document.querySelector('#settings-isolation .container-posture-label');
        const rungs = document.querySelectorAll('#settings-isolation .container-ladder .rung.on');
        return { label: label ? label.textContent.trim() : "", active: rungs.length };
      })()`);
      check(Boolean(isolation) && posture.label !== "", "Settings → Isolation did not render a posture");
      check(posture.active === 1, `isolation ladder has ${posture.active} active rungs, want 1`);
      if (isolation) {
        await page.evaluate(`document.querySelector('#settings-isolation')?.scrollIntoView()`);
        await shoot("settings-isolation", ["mobile", "desk1024", "desk1440"]);
      }
    }
    await page.evaluate(`document.querySelector('[data-nav="primary"] button')?.click()`);

    await shoot("shell-empty", ["desk1024", "desk1440", "mobile"]);
    const emptyState = await page.evaluate(`(() => {
      const root = document.querySelector(".sx-empty");
      return {
        cta: Boolean(root && root.querySelector("[data-add-provider]")),
        cards: Boolean(root && root.querySelector(".sx-cards")),
      };
    })()`);
    check(!emptyState.cards, "empty transcript still shows suggestion cards");
    check(emptyState.cta, "empty transcript is missing Add provider");
    await headerAt(page, check, viewport, async (name) => {
      const png = await page.send("Page.captureScreenshot", { format: "png" });
      await writeFile(join(outDir, "screenshots", `${name}.png`), Buffer.from(png.data, "base64"));
      shots.push(name);
    });
    await composerAt(page, check, viewport, async (name) => {
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
      await shoot("shell-palette", ["mobile", "desk768", "desk1024", "desk1440"]);
      await page.evaluate(`document.querySelector(".palette input")?.focus()`);
      await press("Enter");
      const planned = await waitUntil(
        `document.body && document.body.innerText.includes("Approval · Plan")`,
        4000,
        "approval mode Plan",
      );
      check(Boolean(planned), "keyboard palette did not set Approval · Plan");
      await press("k", 2);
      const again = await waitUntil(
        `Boolean(document.querySelector("[data-nav='palette-dialog']"))`,
        4000,
        "palette reopened",
      );
      if (again) {
        await page.evaluate(`(() => {
          const input = document.querySelector("[data-nav='palette-dialog'] input");
          if (!input) return;
          const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
          proto.set.call(input, "zzzz-no-command");
          input.dispatchEvent(new Event("input", { bubbles: true }));
        })()`);
        await delay(150);
        const empty = await page.evaluate(
          `Boolean(document.querySelector(".palette-empty"))`,
        );
        check(empty, "palette empty state did not render");
        await press("Escape");
        await delay(120);
        const closed = await page.evaluate(
          `!document.querySelector("[data-nav='palette-dialog']")`,
        );
        check(closed, "Escape did not close the palette");
      }
      await press("k", 2);
      const rotateOpen = await waitUntil(
        `Boolean(document.querySelector("[data-nav='palette-dialog']"))`,
        4000,
        "palette for confirm",
      );
      if (rotateOpen) {
        await page.evaluate(`(() => {
          const input = document.querySelector("[data-nav='palette-dialog'] input");
          if (!input) return;
          const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
          proto.set.call(input, "rotate");
          input.dispatchEvent(new Event("input", { bubbles: true }));
        })()`);
        await delay(200);
        await page.evaluate(`document.querySelector('[data-command-id="gateway.rotate"]')?.click()`);
        await delay(200);
        const confirm = await page.evaluate(
          `Boolean(document.querySelector("[data-confirm='dialog']"))`,
        );
        check(confirm, "rotate token did not open the confirm dialog");
        if (confirm) {
          await shoot("palette-confirm", ["mobile", "desk768", "desk1024", "desk1440"]);
          await page.evaluate(`document.querySelector("[data-confirm='cancel']")?.click()`);
        }
      }
    }

    await click("[data-nav='palette']", "Run a command");
    await waitUntil(
      `Boolean(document.querySelector('[data-command-id="mode-yolo"]'))`,
      4000,
      "approval commands",
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
    // #1337: global live regions exist at the shell root, and the echo
    // turn ending is announced politely.
    const live = await page.evaluate(`(() => {
      const p = document.querySelector('#a11y-polite[aria-live="polite"]');
      const a = document.querySelector('#a11y-assertive[aria-live="assertive"]');
      return { polite: Boolean(p), assertive: Boolean(a) };
    })()`);
    check(live.polite && live.assertive, "global aria-live regions missing");
    const announced = await waitUntil(
      `(() => {
        const p = document.querySelector("#a11y-polite");
        return Boolean(p) && /Turn (finished|ended)/.test(p.textContent || "");
      })()`,
      8000,
      "turn-end announcement",
    );
    check(Boolean(announced), "turn end was not announced in the polite live region");
    // #1337: with reduced motion emulated, transitions and animations finish instantly.
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
    const motion = await page.evaluate(`(() => {
      const el = document.querySelector("button.send") || document.querySelector("button");
      if (!el) return null;
      const cs = getComputedStyle(el);
      const secs = (v) => Math.max(...v.split(",").map((x) => parseFloat(x) * (x.trim().endsWith("ms") ? 0.001 : 1)));
      return {
        matches: matchMedia("(prefers-reduced-motion: reduce)").matches,
        transition: secs(cs.transitionDuration),
        animation: secs(cs.animationDuration),
      };
    })()`);
    check(Boolean(motion && motion.matches), "reduced-motion emulation did not apply");
    check(Boolean(motion) && motion.transition <= 0.001 && motion.animation <= 0.001, `reduced motion not honoured: ${JSON.stringify(motion)}`);
    await page.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "no-preference" }] });
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

      // #1375: a lead -> child message the daemon accepts is aggregated
      // into /v1/team messages and must appear in the Activity feed.
      const team = await fetch(`${baseURL}/v1/team?root=${encodeURIComponent(root)}`).then((r) => r.json());
      const child = Object.values(team.members || {}).find(
        (m) => m.name === CHILD_NAME || m.objective === CHILD_OBJECTIVE,
      );
      if (child?.sessionId) {
        const sent = await fetch(`${baseURL}/v1/ops?root=${encodeURIComponent(root)}`, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            type: "team.message",
            data: {
              to: child.sessionId,
              body: "uat feed body",
              idempotencyKey: "uat-feed-1",
            },
          }),
        });
        const sentBody = await sent.text();
        log(`team.message for feed: HTTP ${sent.status} ${sentBody.slice(0, 200)}`);
        if (sent.ok) {
          const fed = await waitUntil(
            `[...document.querySelectorAll("[data-agent-feed] .agent-msg")].some((el) => el.innerText.includes("uat feed body"))`,
            15000,
            "agent message feed entry",
          );
          check(Boolean(fed), "accepted team.message never appeared in the agent message feed");
          await shoot("shell-agent-feed", ["desk1024"]);
        }
      } else {
        log("no child member in /v1/team; skipped agent feed check");
      }
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

    // Daemon is palette-only on desktop (#1590 A4): open the palette and
    // run its Daemon command, the same shape providerFlow uses.
    await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
    await waitUntil(
      `Boolean(document.querySelector('[data-command-id="daemon"]'))`,
      4000,
      "daemon palette command",
    );
    await page.evaluate(`document.querySelector('[data-command-id="daemon"]')?.click()`);
    await waitUntil(
      `document.body && document.body.innerText.includes("Daemon")`,
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
