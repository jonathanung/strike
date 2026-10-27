// Scripted cockpit pass: keyboard palette, approval, composer, delegated
// child, session switch, reconnect. Per-stream files in ./scenarios run
// after this pass when --suite is full (the default). Assertions accumulate
// so one run reports every miss instead of stopping at the first.

import { readFile } from "node:fs/promises";
import { join } from "node:path";

import { delay } from "./browser.mjs";
import { DARK_TOKENS, LIGHT_TOKENS, missingLabels, tokenMismatch } from "./lib.mjs";
import { encodeVideo } from "./media.mjs";
import { loadScenario, loadScenarios } from "./scenarios.mjs";
import { createSession } from "./session.mjs";

const MARKER = "uat-marker-alpha";
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
const CONNECTED_EXPR = `(() => {
  const text = document.body ? document.body.innerText : "";
  return text.includes("Connected") && !text.includes("Unreachable") && !text.includes("Could not reach the daemon");
})()`;

export async function runScenario({ page, baseURL, outDir, video, goldens, updateGoldens, controls, suite = "full" }) {
  const session = await createSession({ page, outDir, video, goldens, updateGoldens });
  const { failures, shots, check, waitUntil, click, press, shoot, viewport, bodyText, tokens, log } = session;
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
      `document.body && document.body.innerText.includes("This gateway")`,
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
        const backOnline = await waitUntil(CONNECTED_EXPR, 10000, "daemon reconnected");
        check(Boolean(backOnline), "Retry did not restore Connected");
      }
      await shoot("shell-reconnect-ok", ["desk1024", "desk1440"]);
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

  return { failures, shots, videoPath, frames: frame };
}
