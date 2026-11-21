// Child signals (#1375). The echo daemon cannot trip a child budget on
// demand, so this scenario shims GET /v1/team in the page with the shape
// the daemon sends (member `escalation`, bounded `messages`) and asserts:
//   - an escalated child row shows the kind/reason/action sentence;
//   - inter-agent messages render summary-first, newest first, bounded to
//     5 with "Show all", From -> To resolved to member names / Lead;
//   - the body is hidden until expanded;
//   - no messages -> no feed (the Activity tab looks as before).

import { delay } from "../browser.mjs";

function messages(n) {
  return Array.from({ length: n }, (_, i) => ({
    messageId: `m${i}`,
    from: i % 2 ? "kid-a" : "lead-root",
    to: i % 2 ? "lead-root" : "kid-a",
    summary: i === n - 1 ? "blocked on the schema" : `note ${i}`,
    body: i === n - 1 ? "Full body: the schema file is missing field X." : "",
    urgency: i === n - 1 ? "high" : "",
    kind: i === n - 1 ? "request" : "",
  }));
}

const TEAM = {
  available: true,
  leadId: "lead-root",
  members: {
    "kid-a": {
      sessionId: "kid-a",
      name: "scout",
      state: "working",
      escalation: { kind: "stall", reason: "no progress for 300s", action: "finalizing", terminalStatus: "blocked" },
    },
    "kid-b": { sessionId: "kid-b", name: "fixer", state: "working" },
  },
  messages: messages(7),
};

function shim(team) {
  return `(() => {
    if (!window.__childSigOrigFetch) window.__childSigOrigFetch = window.fetch.bind(window);
    window.__childSigTeam = ${JSON.stringify(team)};
    window.fetch = async function (input, init) {
      const url = typeof input === "string" ? input : (input && input.url) || "";
      const method = ((init && init.method) || (typeof input !== "string" && input && input.method) || "GET").toUpperCase();
      if (method === "GET" && url.includes("/v1/team?")) {
        return new Response(JSON.stringify(window.__childSigTeam), {
          status: 200,
          headers: { "content-type": "application/json" },
        });
      }
      return window.__childSigOrigFetch(input, init);
    };
    return true;
  })()`;
}

export async function scenario(ctx) {
  const { page, waitUntil, check, click, shoot, log } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await page.evaluate(shim({ ...TEAM, messages: [] }));
  await click(".side-tabs button", "Activity");

  const rows = await waitUntil(
    `document.querySelectorAll(".activity-row").length >= 2`,
    10000,
    "shimmed child rows",
  );
  check(Boolean(rows), "shimmed team rows did not render");
  if (!rows) return;
  await delay(500);
  check(
    Boolean(await page.evaluate(`!document.querySelector("[data-agent-feed]")`)),
    "agent message feed rendered with no messages",
  );

  const esc = await waitUntil(
    `document.querySelector("[data-child-escalation]")?.textContent || ""`,
    6000,
    "escalation line",
  );
  check(
    String(esc).includes("Stalled: no progress for 300s") && String(esc).includes("finishing with a handoff"),
    `escalation line missing kind/reason/action: ${esc}`,
  );

  await page.evaluate(shim(TEAM));
  const feed = await waitUntil(
    `document.querySelectorAll("[data-agent-feed] .agent-msg").length`,
    10000,
    "agent message feed",
  );
  check(Number(feed) === 5, `feed should show 5 recent messages, got ${feed}`);
  const first = await page.evaluate(`(() => {
    const li = document.querySelector("[data-agent-feed] .agent-msg");
    return li ? {
      id: li.getAttribute("data-agent-msg"),
      route: li.querySelector(".agent-msg-route")?.textContent || "",
      summary: li.querySelector(".agent-msg-summary")?.textContent || "",
      body: Boolean(li.querySelector(".agent-msg-body")),
      text: li.innerText,
    } : null;
  })()`);
  check(first?.id === "m6", `newest message is not first: ${JSON.stringify(first)}`);
  check(first?.route === "scout → Lead", `route not resolved to names: ${JSON.stringify(first)}`);
  check(first?.summary === "blocked on the schema", `summary not shown first: ${JSON.stringify(first)}`);
  check(first?.body === false, "message body visible before expand");
  check(/high/.test(first?.text || "") && /request/.test(first?.text || ""), `urgency/kind tags missing: ${first?.text}`);
  await shoot("child-signals", ["desk1024", "desk1440", "mobile"]);

  await page.evaluate(`document.querySelector("[data-agent-feed] .agent-msg .agent-msg-toggle")?.click()`);
  const body = await waitUntil(
    `document.querySelector("[data-agent-feed] .agent-msg-body")?.textContent || ""`,
    4000,
    "expanded message body",
  );
  check(/field X/.test(String(body)), `expanded body missing: ${body}`);

  await page.evaluate(`document.querySelector("[data-agent-feed-more]")?.click()`);
  const all = await waitUntil(
    `document.querySelectorAll("[data-agent-feed] .agent-msg").length === 7`,
    4000,
    "show all messages",
  );
  check(Boolean(all), "Show all did not list every message");
  log("child signals scenario done");
}
