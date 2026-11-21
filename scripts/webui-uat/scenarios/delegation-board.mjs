// Delegation board (#1381). Shims GET /v1/team in the page with the
// daemon's `delegations` shape and asserts:
//   - no delegations -> the section is absent;
//   - a queued delegation renders before any child session exists, with
//     "No session yet";
//   - rows order review/blocked, working, queued, terminal;
//   - a linked session shows the member's name.

import { delay } from "../browser.mjs";

const BASE = { available: true, leadId: "lead-root", members: {} };

function shim(team) {
  return `(() => {
    if (!window.__boardOrigFetch) window.__boardOrigFetch = window.fetch.bind(window);
    window.__boardTeam = ${JSON.stringify(team)};
    window.fetch = async function (input, init) {
      const url = typeof input === "string" ? input : (input && input.url) || "";
      const method = ((init && init.method) || (typeof input !== "string" && input && input.method) || "GET").toUpperCase();
      if (method === "GET" && url.includes("/v1/team?")) {
        return new Response(JSON.stringify(window.__boardTeam), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return window.__boardOrigFetch(input, init);
    };
    return true;
  })()`;
}

const ORDER = `[...document.querySelectorAll("[data-delegation-board] .delegation-row")].map((el) => el.getAttribute("data-delegation"))`;

export async function scenario(ctx) {
  const { page, waitUntil, check, click, shoot, log } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await page.evaluate(shim({ ...BASE, delegations: null }));
  await click(".side-tabs button", "Activity");
  await delay(5000);
  check(
    Boolean(await page.evaluate(`!document.querySelector("[data-delegation-board]")`)),
    "delegation board rendered with no delegations",
  );

  await page.evaluate(
    shim({ ...BASE, delegations: { q1: { id: "q1", state: "queued", name: "write the parser tests" } } }),
  );
  const queued = await waitUntil(
    `document.querySelector('[data-delegation="q1"]')?.innerText || ""`,
    10000,
    "queued delegation row",
  );
  check(/write the parser tests/.test(String(queued)), `queued row missing name: ${queued}`);
  check(/Queued/.test(String(queued)), `queued row missing state badge: ${queued}`);
  check(/No session yet/.test(String(queued)), `queued row should say no session: ${queued}`);

  await page.evaluate(
    shim({
      ...BASE,
      members: { c1: { sessionId: "c1", name: "scout", state: "working" } },
      delegations: {
        q1: { id: "q1", state: "queued", name: "write the parser tests" },
        w1: { id: "w1", state: "working", name: "survey the repo", sessionId: "c1" },
        r1: { id: "r1", state: "review", name: "fix the lexer", sessionId: "c9", reason: "awaiting lead review" },
        d1: { id: "d1", state: "done", name: "read docs" },
        b1: { id: "b1", state: "blocked", name: "migrate config" },
      },
    }),
  );
  const ordered = await waitUntil(
    `(() => { const o = ${ORDER}; return o.length === 5 ? o.join(",") : ""; })()`,
    10000,
    "five delegation rows",
  );
  check(ordered === "r1,b1,w1,q1,d1", `delegations not in urgency order: ${ordered}`);
  const working = await page.evaluate(`document.querySelector('[data-delegation="w1"]')?.innerText || ""`);
  check(/scout/.test(working), `working row should name its session: ${working}`);
  const review = await page.evaluate(`document.querySelector('[data-delegation="r1"]')?.innerText || ""`);
  check(/awaiting lead review/.test(review), `review row should show its reason: ${review}`);
  await shoot("delegation-board", ["desk1024", "desk1440", "mobile"]);
  log("delegation board scenario done");
}
