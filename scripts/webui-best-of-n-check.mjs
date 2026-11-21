// Browser check for best-of-N (#1881). Called last from webui-smoke on the
// same DevTools session and auth'd daemon, so the card it leaves behind
// cannot disturb other checks.
//
// Covers: the palette's "Run with N models…" opens the picker; one model is
// refused before anything runs; two models show the 2× cost and per-run
// budget; Run posts /v1/bestofn and a comparison card appears with one row
// per model; every row reaches a stopped state (the echo provider may run
// or refuse a made-up model, both are honest outcomes); Keep none goes
// through the confirm dialog and the card reports it.
import { setTimeout as delay } from "node:timers/promises";

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
  const details = ev.result?.exceptionDetails;
  if (details) {
    throw new Error(`eval failed: ${JSON.stringify(details).slice(0, 400)}`);
  }
  return ev.result?.result?.value;
}

async function waitFor(send, expression, label, tries = 120) {
  let last = "";
  for (let i = 0; i < tries; i++) {
    last = String((await evalValue(send, expression)) ?? "");
    if (last.startsWith("ok")) return last;
    await delay(250);
  }
  throw new Error(`best-of-n: ${label}: ${last.slice(0, 400)}`);
}

const PROMPT = "best-of-n smoke: reply with one word";

// Set a field's value the way typing does, so Leptos on:input fires.
const setField = (field, value) => `(() => {
  const el = document.querySelector('[data-bon-field="${field}"]');
  if (!el) return "no field ${field}";
  const proto = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value");
  proto.set.call(el, ${JSON.stringify(value)});
  el.dispatchEvent(new InputEvent("input", { bubbles: true }));
  return "ok";
})()`;

const costIncludes = (text) => `(() => {
  const c = document.querySelector("[data-bon-cost]");
  if (!c) return "no cost line";
  return c.textContent.includes(${JSON.stringify(text)}) ? "ok" : "cost: " + c.textContent;
})()`;

export async function bestOfNCheck({ send }) {
  await evalValue(
    send,
    `(() => { document.querySelector('[data-view="chat"]')?.click(); const b = [...document.querySelectorAll("button")].find((el) => el.textContent.trim() === "Back to chat"); if (b) b.click(); return "ok"; })()`,
  );
  await waitFor(
    send,
    `(() => { if (document.querySelector("textarea")) return "ok"; document.querySelector('[data-view="chat"]')?.click(); return "no composer, view=" + (document.querySelector("[data-view].active, [data-view][aria-current]")?.getAttribute("data-view") || "?"); })()`,
    "composer",
  );

  // Palette → "Run with N models…".
  await evalValue(send, `(() => { if (!document.querySelector('[data-nav="palette-dialog"]')) document.querySelector('[data-nav="palette"]')?.click(); return "ok"; })()`);
  await waitFor(send, `(() => document.querySelector('[data-nav="palette-dialog"] input') ? "ok" : "no palette")()`, "palette open");
  await evalValue(
    send,
    `(() => {
      const input = document.querySelector('[data-nav="palette-dialog"] input');
      const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
      proto.set.call(input, "Run with N models");
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "Run with N models" }));
      return "ok";
    })()`,
  );
  await waitFor(
    send,
    `(() => ((document.querySelector('[data-nav="palette-dialog"]') || {}).innerText || "").includes("Run with N models") ? "ok" : "no row")()`,
    "palette row",
  );
  await evalValue(
    send,
    `(() => { document.querySelector('[data-nav="palette-dialog"] input').dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true })); return "ok"; })()`,
  );
  await waitFor(send, `(() => document.querySelector(".bon-picker") ? "ok" : "no picker")()`, "picker opens");

  // One model is refused up front; two show the cost and budget.
  await waitFor(send, setField("models", "echo"), "type one model");
  await waitFor(send, costIncludes("Pick 2–4 models"), "one model refused");
  await waitFor(send, setField("models", "echo, echo@high"), "type two models");
  await waitFor(send, setField("turns", "5"), "type turns");
  await waitFor(send, costIncludes("about 2× the cost of one run"), "2x cost shown");
  await waitFor(send, costIncludes("up to 5 turns"), "budget shown");

  // Answers only: no worktrees in the smoke repo.
  await waitFor(
    send,
    `(() => { const c = document.querySelector('[data-bon-field="edits"]'); if (!c) return "no edits box"; if (c.checked) c.click(); return c.checked ? "still checked" : "ok"; })()`,
    "answers only",
  );
  await waitFor(send, setField("prompt", PROMPT), "type prompt");
  await waitFor(
    send,
    `(() => { const b = document.querySelector('[data-bon-action="start"]'); if (!b) return "no run button"; b.click(); return "ok"; })()`,
    "click Run",
  );

  // The card appears with one row per model and every row stops.
  await waitFor(
    send,
    `(() => {
      const card = document.querySelector("[data-bon-run]");
      if (!card) { const e = document.querySelector(".bon-error"); return "no card " + (e ? e.textContent : ""); }
      const rows = card.querySelectorAll(".bon-cands li");
      return rows.length === 2 && card.textContent.includes(${JSON.stringify(PROMPT)}) ? "ok" : "rows " + rows.length;
    })()`,
    "comparison card",
  );
  await waitFor(
    send,
    `(() => {
      const card = document.querySelector("[data-bon-run]");
      const states = [...card.querySelectorAll(".bon-cands li")].map((li) => li.getAttribute("data-status"));
      if (states.includes("running")) {
        const cancel = card.querySelector('[data-bon-action="cancel"]');
        return "running " + states.join(",") + (cancel ? "" : " (no cancel button)");
      }
      return card.getAttribute("data-state") === "complete" ? "ok" : "state " + card.getAttribute("data-state");
    })()`,
    "every run stops",
    480,
  );

  // Keep none is a confirmed step.
  await waitFor(
    send,
    `(() => { const b = document.querySelector('[data-bon-run] [data-bon-action="keep-none"]'); if (!b) return "no keep none"; b.click(); return "ok"; })()`,
    "click Keep none",
  );
  await waitFor(
    send,
    `(() => { const b = document.querySelector("[data-confirm='accept']"); if (!b) return "no confirm"; b.click(); return "ok"; })()`,
    "confirm keep none",
  );
  await waitFor(
    send,
    `(() => {
      const card = document.querySelector("[data-bon-run]");
      return card && card.getAttribute("data-state") === "decided" && card.textContent.includes("Kept none") ? "ok" : "state " + (card && card.getAttribute("data-state"));
    })()`,
    "kept none",
  );
}
