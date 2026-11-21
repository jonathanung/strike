// Browser check for Debug mode (#1345 / #1820). Called from webui-smoke on
// the same DevTools session and auth'd daemon, at the end of the run so the
// closed-session summary does not shift earlier layout checks.
//
// Covers: debug.control start shows the panel in the hypothesis phase and
// a transcript notice; hypotheses typed in the panel become two cards; an
// unlock without evidence is refused by the daemon and the reason shows in
// the panel; evidence attached from the reproduce form, confirm, unlock,
// mark fixed and end all go through the panel's buttons; the end sweep
// leaves zero tagged lines and the summary and closing notice say so.
// The phase gate denying an untagged edit is covered by the engine tests
// (harness/engine/debug_test.go): the echo provider cannot issue edits.
import { setTimeout as delay } from "node:timers/promises";

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
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
  throw new Error(`debug mode: ${label}: ${last.slice(0, 400)}`);
}

const click = (selector) =>
  `(() => { const b = document.querySelector(${JSON.stringify(selector)}); if (!b) return "missing ${selector.replace(/"/g, "'")}"; if (b.disabled) return "disabled"; b.click(); return "ok"; })()`;

const typeInto = (selector, value) =>
  `(() => { const t = document.querySelector(${JSON.stringify(selector)}); if (!t) return "missing"; t.value = ${JSON.stringify(value)}; t.dispatchEvent(new Event("input", { bubbles: true })); return "ok"; })()`;

const phaseIs = (phase) =>
  `(() => { const p = document.querySelector(".debug-panel"); const got = p ? p.getAttribute("data-debug-phase") : "none"; return got === ${JSON.stringify(phase)} ? "ok" : "phase=" + got; })()`;

const transcriptHas = (text) =>
  `(document.body.innerText.includes(${JSON.stringify(text)}) ? "ok" : "transcript lacks " + ${JSON.stringify(text)})`;

export async function debugModeCheck({ send, port, token }) {
  const op = async (data) => {
    const res = await fetch(`http://127.0.0.1:${port}/v1/ops`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ type: "debug.control", data }),
    });
    if (!res.ok) {
      throw new Error(`debug mode: op ${data.action} ${res.status} ${await res.text()}`);
    }
  };
  // Clicks retry while a previous op is still in flight (buttons disable).
  const press = (selector, label) => waitFor(send, click(selector), label, 40);

  await evalValue(send, `document.querySelector('[data-view="chat"]')?.click()`);
  await op({ action: "start" });
  await waitFor(send, phaseIs("hypothesis"), "panel shows the hypothesis phase");
  await waitFor(send, transcriptHas("Debug session started"), "start notice in the transcript");
  await waitFor(send, `(document.querySelector("[data-debug-chip]")?.textContent || "").includes("Hypothesis") ? "ok" : "chip"`, "status chip names the phase");

  await waitFor(send, typeInto("[data-debug-hypotheses-input]", "off by one | index == len | index < len\nstale cache"), "type hypotheses");
  await press("[data-debug-set-hypotheses]", "set hypotheses");
  await waitFor(
    send,
    `(() => { const n = document.querySelectorAll("[data-debug-hypothesis]").length; return n === 2 ? "ok" : "cards=" + n; })()`,
    "two hypothesis cards",
  );

  // Error path: the daemon refuses an unlock with no confirmed hypothesis.
  await op({ action: "unlock" });
  await waitFor(
    send,
    `(() => { const e = document.querySelector("[data-debug-error]"); return e && e.textContent.includes("no confirmed hypothesis") ? "ok" : "error=" + (e ? e.textContent : "none"); })()`,
    "refused unlock shows the reason",
  );
  await waitFor(send, phaseIs("hypothesis"), "refusal keeps the hypothesis phase");

  await waitFor(send, typeInto("[data-debug-evidence-input]", "IndexError: index 3 out of range for length 3"), "type evidence");
  await press("[data-debug-attach]", "attach evidence");
  await waitFor(
    send,
    `(() => { const c = document.querySelector('[data-debug-hypothesis="h1"]'); const n = c ? c.querySelectorAll(".debug-evidence li").length : -1; return n === 1 ? "ok" : "evidence=" + n; })()`,
    "evidence attached to h1",
  );
  await press('[data-debug-confirm="h1"]', "confirm h1");
  await waitFor(
    send,
    `(document.querySelector('[data-debug-hypothesis="h1"]')?.getAttribute("data-status") === "confirmed" ? "ok" : "not confirmed")`,
    "h1 confirmed",
  );
  await press("[data-debug-unlock]", "unlock fix");
  await waitFor(send, phaseIs("fix"), "fix phase");
  await waitFor(send, transcriptHas("Fix unlocked"), "unlock notice in the transcript");

  await press("[data-debug-fixed]", "mark fixed");
  await press("[data-debug-end]", "end debug session");
  await waitFor(send, phaseIs("closed"), "session closed");
  await waitFor(
    send,
    `(document.querySelector('[data-debug-remaining]')?.getAttribute("data-debug-remaining") === "0" ? "ok" : "remaining=" + document.querySelector('[data-debug-remaining]')?.getAttribute("data-debug-remaining"))`,
    "summary shows zero tagged lines remaining",
  );
  await waitFor(send, transcriptHas("Debug session closed"), "closing summary in the transcript");
}
