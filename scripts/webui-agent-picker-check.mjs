// Browser check for the cockpit agent picker (#1563). Called from
// webui-smoke on the same DevTools session and auth'd daemon.
//
// The daemon's built-in agents give bootstrap at least two selectable
// agents. The check opens the composer's Agent chip, expects the palette to
// list them, picks a different agent, and waits for the chip to follow the
// daemon's agent.selected confirmation (the chip never renames on click).
// The posted op must be select.agent with exactly the documented
// { "name": ... } payload. It then switches back so the rest of the smoke
// run keeps the default agent.
import { setTimeout as delay } from "node:timers/promises";

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
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
  throw new Error(`agent picker: ${label}: ${last.slice(0, 400)}`);
}

// Record every POST /v1/ops body the page sends so the check can assert
// the exact select.agent payload the docs promise.
const RECORD_OPS = `(() => {
  if (window.__agentPickerOps) return "ok";
  window.__agentPickerOps = [];
  const orig = window.fetch.bind(window);
  window.fetch = (input, init) => {
    // gloo-net hands fetch a Request object; plain callers pass init.body.
    try {
      const url = typeof input === "string" ? input : (input && input.url) || "";
      if (url.includes("/v1/ops")) {
        if (init && typeof init.body === "string") {
          window.__agentPickerOps.push(init.body);
        } else if (input instanceof Request) {
          input.clone().text().then((body) => window.__agentPickerOps.push(body), () => {});
        }
      }
    } catch (_) {}
    return orig(input, init);
  };
  return "ok";
})()`;

async function sentSelectAgent(send, name) {
  return waitFor(
    send,
    `(() => {
      const ops = (window.__agentPickerOps || []).map((b) => { try { return JSON.parse(b); } catch (_) { return null; } });
      const hit = ops.some((op) => op && op.type === "select.agent" && op.data && op.data.name === ${JSON.stringify(name)} && Object.keys(op.data).length === 1);
      return hit ? "ok" : "ops " + JSON.stringify(window.__agentPickerOps || []).slice(0, 300);
    })()`,
    `select.agent posted for ${name}`,
  );
}

async function switchTo(send, name) {
  await evalValue(send, `document.querySelector('[data-agent-status]')?.click()`);
  await waitFor(
    send,
    `(() => {
      const rows = [...document.querySelectorAll('[data-nav="palette-dialog"] [data-choice-id]')];
      const row = rows.find((el) => el.getAttribute("data-choice-id") === ${JSON.stringify(name)});
      if (!row) return "rows " + rows.map((el) => el.getAttribute("data-choice-id")).join(",");
      row.click();
      return "ok";
    })()`,
    `pick ${name}`,
  );
  await waitFor(
    send,
    `(() => {
      const chip = document.querySelector('[data-agent-status]');
      const cur = chip ? chip.getAttribute("data-agent-current") : "";
      return cur === ${JSON.stringify(name)} && chip.innerText.includes(${JSON.stringify(name)})
        ? "ok"
        : "chip " + cur + " / " + (chip ? chip.innerText : "missing");
    })()`,
    `chip follows agent.selected for ${name}`,
  );
  await sentSelectAgent(send, name);
}

export async function agentPickerCheck({ send }) {
  await evalValue(send, RECORD_OPS);
  await evalValue(send, `document.querySelector('[data-view="chat"]')?.click()`);
  const chip = await waitFor(
    send,
    `(() => {
      const chip = document.querySelector('[data-agent-status]');
      if (!chip) return "no agent chip";
      const cur = chip.getAttribute("data-agent-current") || "";
      return cur ? "ok " + cur : "agent not reported yet";
    })()`,
    "agent chip shows the running agent",
  );
  const original = chip.slice(3);

  await evalValue(send, `document.querySelector('[data-agent-status]')?.click()`);
  const listed = await waitFor(
    send,
    `(() => {
      const rows = [...document.querySelectorAll('[data-nav="palette-dialog"] [data-choice-id]')];
      const agents = rows
        .filter((el) => (el.innerText || "").includes("Switch agent: "))
        .map((el) => el.getAttribute("data-choice-id"));
      return agents.length >= 2 ? "ok " + JSON.stringify(agents) : "listed " + JSON.stringify(agents);
    })()`,
    "palette lists at least two agents",
  );
  const agents = JSON.parse(listed.slice(3));
  if (!agents.includes(original)) {
    throw new Error(`agent picker: current agent ${original} not listed in ${listed}`);
  }
  await evalValue(send, `document.querySelector(".palette-backdrop")?.click()`);
  await waitFor(
    send,
    `document.querySelector('[data-nav="palette-dialog"]') ? "palette still open" : "ok"`,
    "palette closes",
  );

  const other = agents.find((name) => name !== original);
  await switchTo(send, other);
  await switchTo(send, original);
}
