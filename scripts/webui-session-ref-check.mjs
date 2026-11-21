// Browser check for the @session composer UI (#1550 slice 2, #1906).
// Called from webui-smoke on the same DevTools session after the smoke
// seeded beta-session ("Palette polish") into the session dir.
//
// 1. Typing "@palet" lists "Palette polish" under a Sessions section.
// 2. Clicking it inserts "@session:beta-session " and a chip shows the
//    daemon-reported size (GET /v1/sessions/{id}/reference) before send.
// 3. An unknown id shows the not-found (404) state.
// 4. "@session:" lists this session as a disabled row; referencing it
//    renders the self state ("attaches nothing").
import { setTimeout as delay } from "node:timers/promises";

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  const details = ev.result?.exceptionDetails;
  if (details) {
    throw new Error(`eval failed: ${JSON.stringify(details).slice(0, 400)}`);
  }
  return ev.result?.result?.value;
}

async function waitFor(send, expression, label, tries = 80) {
  let last = "";
  for (let i = 0; i < tries; i++) {
    last = String((await evalValue(send, expression)) ?? "");
    if (last.startsWith("ok")) return last;
    await delay(250);
  }
  throw new Error(`session ref: ${label}: ${last.slice(0, 400)}`);
}

function typeDraft(text) {
  return `(() => {
    const ta = document.querySelector('[data-feature="composer-triggers"] textarea');
    if (!ta) return "missing-textarea";
    ta.focus();
    ta.value = ${JSON.stringify(text)};
    const end = ta.value.length;
    ta.setSelectionRange(end, end);
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(text.slice(-1))} }));
    return "ok";
  })()`;
}

function chipState(id, state, needles) {
  return `(() => {
    const chip = document.querySelector('[data-session-ref=${JSON.stringify(id)}]');
    if (!chip) return "no chip for ${id}: " + (document.querySelector('[data-session-ref-chips]')?.innerText || "none");
    const kind = chip.getAttribute("data-session-ref-state");
    const text = chip.innerText;
    if (kind !== ${JSON.stringify(state)}) return "state " + kind + ": " + text;
    for (const n of ${JSON.stringify(needles)}) {
      if (!text.includes(n)) return "missing " + n + " in " + text;
    }
    return "ok " + text;
  })()`;
}

export async function sessionRefCheck({ send }) {
  await waitFor(send, typeDraft("@palet"), "type @palet");
  await waitFor(
    send,
    `(() => {
      const box = document.querySelector('[data-composer-triggers="suggestions"]');
      if (!box) return "no suggestions";
      if (!box.querySelector('[data-composer-section="section-sessions"]')) return "no Sessions section: " + box.innerText;
      const row = box.querySelector('[data-suggest-id="session-beta-session"]');
      if (!row || !row.innerText.includes("Palette polish")) return "no Palette polish row: " + box.innerText;
      return "ok";
    })()`,
    "Sessions section lists Palette polish",
  );
  await evalValue(send, `document.querySelector('[data-suggest-id="session-beta-session"]').click()`);
  await waitFor(
    send,
    `(() => {
      const ta = document.querySelector('[data-feature="composer-triggers"] textarea');
      return ta && ta.value === "@session:beta-session " ? "ok" : "draft " + (ta && ta.value);
    })()`,
    "insert @session token",
  );
  await waitFor(send, chipState("beta-session", "ready", ["Palette polish", "chars", "tokens"]), "chip size");

  await waitFor(send, typeDraft("@session:no-such-session "), "type missing id");
  await waitFor(send, chipState("no-such-session", "missing", ["not found"]), "missing chip");

  await waitFor(send, typeDraft("@session:"), "type @session:");
  const selfId = await waitFor(
    send,
    `(() => {
      const rows = [...document.querySelectorAll('[data-composer-triggers="suggestions"] [data-suggest-id^="session-"]')];
      const own = rows.find((r) => r.disabled && r.innerText.includes("attaches nothing"));
      if (!own) return "no self row: " + rows.map((r) => r.innerText).join(" | ");
      return "ok " + own.getAttribute("data-suggest-id").slice("session-".length);
    })()`,
    "self row in menu",
  );
  const id = selfId.slice(3);
  await waitFor(send, typeDraft(`@session:${id} `), "type self id");
  await waitFor(send, chipState(id, "self", ["attaches nothing"]), "self chip");

  await waitFor(send, typeDraft(""), "clear draft");
  await waitFor(
    send,
    `document.querySelector('[data-session-ref-chips]') ? "chips still shown" : "ok"`,
    "chips cleared",
  );
}
