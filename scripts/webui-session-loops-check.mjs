// Browser check for session /loop rows on the live stream (#1944). Called
// from webui-smoke on the same DevTools session and auth'd daemon.
//
// Covers: a loop started from the composer shows a row; with a fetch spy in
// the page, a loop started behind the cockpit's back (daemon route) appears,
// a Stop clicked in the panel removes its row, and a stop-all behind its
// back clears the list. The spy must see zero GETs of the loops route, so
// every update came from loops.changed on GET /v1/live/events, not a poll
// or a refetch. Firings are covered by the Go tests: a 1s loop here would
// inject prompts into the echo session for the rest of the run.
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
  throw new Error(`session loops: ${label}: ${last.slice(0, 400)}`);
}

// Row ids in the loop list, in order, as "ok:<ids>" when they equal want.
const rowsAre = (want) =>
  `(() => { const ids = [...document.querySelectorAll(".loop-list [data-loop-id]")].map((li) => li.getAttribute("data-loop-id")).join(","); return ids === ${JSON.stringify(want)} ? "ok:" + ids : "rows=" + ids; })()`;

export async function sessionLoopsCheck({ send, port, token }) {
  const api = async (method, path, body) => {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      method,
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) throw new Error(`session loops: ${method} ${path} ${res.status} ${text.slice(0, 200)}`);
    return text ? JSON.parse(text) : {};
  };

  await evalValue(send, `document.querySelector('[data-view="chat"]')?.click()`);
  await waitFor(send, `document.querySelector("#composer-input") ? "ok" : "no composer"`, "composer present");

  // 1. Start from the composer, the way a user does.
  const job = "smoke loop from composer";
  await waitFor(
    send,
    `(() => {
      const el = document.querySelector("#composer-input");
      const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
      proto.set.call(el, ${JSON.stringify(`/loop 1h ${job}`)});
      el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true }));
      const b = document.querySelector("#send-primary");
      if (!b) return "no send button";
      if (b.disabled) return "send disabled";
      b.click();
      return "ok";
    })()`,
    "send /loop from the composer",
    40,
  );
  const first = await waitFor(
    send,
    `(() => { const li = [...document.querySelectorAll(".loop-list [data-loop-id]")].find((r) => r.innerText.includes(${JSON.stringify(job)})); return li ? "ok:" + li.getAttribute("data-loop-id") : "no row: " + (document.querySelector(".loop-note")?.textContent || ""); })()`,
    "composer loop shows a row",
  );
  const firstId = first.slice(3);

  // The root the page shows is the one whose loop list holds that job.
  const roots = ((await api("GET", "/v1/roots")).roots || []).map((r) => r.id);
  let root = "";
  for (const id of roots) {
    const got = await api("GET", `/v1/sessions/${encodeURIComponent(id)}/loops`).catch(() => ({ loops: [] }));
    if ((got.loops || []).some((l) => l.id === firstId && l.job === job)) {
      root = id;
      break;
    }
  }
  if (!root) throw new Error(`session loops: no live root holds ${firstId} (${roots.join(",")})`);
  const loopsPath = `/v1/sessions/${encodeURIComponent(root)}/loops`;

  // 2. From here on, count every GET of the loops route the page makes.
  await evalValue(
    send,
    `(() => {
      window.__loopGets = 0;
      window.__loopFetch = window.fetch;
      window.fetch = function (input, init) {
        const url = typeof input === "string" ? input : (input && input.url) || "";
        const method = ((init && init.method) || (input && input.method) || "GET").toUpperCase();
        if (method === "GET" && /\\/v1\\/sessions\\/[^/]+\\/loops/.test(url)) window.__loopGets++;
        return window.__loopFetch.apply(this, arguments);
      };
      return true;
    })()`,
  );

  try {
    // 3. A loop started behind the cockpit's back appears without a fetch.
    const second = (await api("POST", loopsPath, { interval: "2h", job: "smoke loop from the daemon" })).loop?.id;
    if (!second) throw new Error("session loops: daemon start returned no id");
    await waitFor(send, rowsAre(`${firstId},${second}`), "daemon-started loop appears from the stream");

    // 4. Stop in the panel: the row goes, with no refetch.
    await waitFor(
      send,
      `(() => { const b = document.querySelector('.loop-list [data-loop-id=${JSON.stringify(firstId)}] button'); if (!b) return "no stop button"; b.click(); return "ok"; })()`,
      "click Stop on the composer loop",
      40,
    );
    await waitFor(send, rowsAre(second), "stopped row leaves the panel");

    // 5. Stop-all behind the cockpit's back clears the list from the stream.
    await api("DELETE", loopsPath);
    await waitFor(
      send,
      `(() => { const n = document.querySelectorAll(".loop-list [data-loop-id]").length; return n === 0 ? "ok" : "rows=" + n; })()`,
      "stop-all from the daemon empties the panel",
    );

    const gets = await evalValue(send, `window.__loopGets`);
    if (gets !== 0) {
      throw new Error(`session loops: the panel fetched the loops route ${gets} time(s); updates must come from loops.changed`);
    }
  } finally {
    await evalValue(send, `(() => { if (window.__loopFetch) { window.fetch = window.__loopFetch; delete window.__loopFetch; } return true; })()`);
    await api("DELETE", loopsPath).catch(() => {});
  }
}
