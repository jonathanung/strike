// Browser checks for skill load warnings (#1386). Called from webui-smoke
// after it seeds ~/.strike/skills/smoke-undescribed.md, a skill with no
// description. The daemon's loader flags it as missing_description, and it
// still loads. The cockpit must name the file in the session-start notice
// and in Customize → Skills, and remember the fingerprint so the same
// unchanged file does not warn again.
import { setTimeout as delay } from "node:timers/promises";

export const BROKEN_SKILL = "smoke-undescribed";
export const BROKEN_SKILL_BODY = `---\nname: ${BROKEN_SKILL}\n---\nThis seeded skill has no description.\n`;

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
  const details = ev.result?.exceptionDetails;
  if (details) {
    throw new Error(`eval failed: ${JSON.stringify(details).slice(0, 400)}`);
  }
  return ev.result?.result?.value;
}

async function waitFor(send, expression, label, tries = 60) {
  let last = "";
  for (let i = 0; i < tries; i++) {
    last = String((await evalValue(send, expression)) ?? "");
    if (last.startsWith("ok")) return last;
    await delay(250);
  }
  throw new Error(`load warnings: ${label}: ${last.slice(0, 400)}`);
}

// Session-start notice in the chat column, plus the remembered fingerprint.
export async function loadWarningNoticeCheck({ send }) {
  const file = `${BROKEN_SKILL}.md`;
  await waitFor(
    send,
    `(() => {
      const n = document.querySelector('[data-load-warning-notice]');
      const text = n ? n.innerText : "";
      return text.includes(${JSON.stringify(file)}) && text.includes("See Customize")
        ? "ok"
        : "notice " + JSON.stringify(text);
    })()`,
    "session-start notice names the broken skill",
  );
  await waitFor(
    send,
    `(() => {
      let seen = [];
      try { seen = JSON.parse(localStorage.getItem("strike.loadWarningsSeen") || "[]"); } catch (_) {}
      return Array.isArray(seen) && seen.length > 0 ? "ok" : "seen " + JSON.stringify(seen);
    })()`,
    "notice remembers announced fingerprints",
  );
  await evalValue(send, `document.querySelector('[data-load-warning-dismiss]')?.click()`);
  await waitFor(
    send,
    `document.querySelector('[data-load-warning-notice]') ? "still shown" : "ok"`,
    "dismiss hides the notice",
  );
}

// Customize → Skills lists the file with its reason. Customize must be open
// on the Skills section.
export async function loadWarningCustomizeCheck({ send }) {
  const file = `${BROKEN_SKILL}.md`;
  await waitFor(
    send,
    `(() => {
      const box = document.querySelector('[data-cz-load-warnings="skills"]');
      const text = box ? box.innerText : "";
      const row = box && box.querySelector('[data-cz-load-warning="missing_description"]');
      return row && text.includes(${JSON.stringify(file)}) && text.includes("No description")
        ? "ok"
        : "skills warnings " + JSON.stringify(text.slice(0, 300));
    })()`,
    "Customize lists the broken skill with its reason",
  );
}
