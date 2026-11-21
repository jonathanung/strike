// Team message composer (#1573). The cockpit pass drives the real
// team.message path against a live echo child; this scenario pins the
// honest states the echo daemon cannot produce on demand, by shimming
// window.fetch for GET /v1/team and POST /v1/ops in the page:
//   - no live team -> the Message action is absent (not disabled);
//   - a terminal child -> the composer explains it cannot receive mail;
//   - a send carries target, root and idempotency key, and the delivered
//     outcome names the daemon's messageId;
//   - a coded refusal (not_lead on broadcast) renders its meaning;
//   - Escape closes the composer (keyboard path).

import { delay } from "../browser.mjs";

const TEAM = {
  available: true,
  leadId: "lead-root",
  members: {
    "kid-run": { sessionId: "kid-run", name: "scout", state: "working" },
    "kid-done": { sessionId: "kid-done", name: "fixer", state: "completed", terminal: true },
  },
};
const NO_TEAM = { available: false, unavailableReason: "", members: {} };

function shim(team) {
  return `(() => {
    if (!window.__teamMsgOrigFetch) window.__teamMsgOrigFetch = window.fetch.bind(window);
    window.__teamMsgTeam = ${JSON.stringify(team)};
    window.__teamMsgOps = window.__teamMsgOps || [];
    window.fetch = async function (input, init) {
      const url = typeof input === "string" ? input : (input && input.url) || "";
      const method = ((init && init.method) || (typeof input !== "string" && input && input.method) || "GET").toUpperCase();
      const json = (status, body) => new Response(JSON.stringify(body), {
        status,
        headers: { "content-type": "application/json" },
      });
      if (method === "GET" && url.includes("/v1/team?")) return json(200, window.__teamMsgTeam);
      if (method === "POST" && url.includes("/v1/ops")) {
        let body = (init && init.body) || "";
        if (typeof input !== "string" && input && !body) body = await input.clone().text();
        let op = {};
        try { op = JSON.parse(String(body)); } catch {}
        if (op.type === "team.message") {
          window.__teamMsgOps.push({ url, op });
          return json(200, { ok: true, messageId: "msg-uat-1" });
        }
        if (op.type === "team.broadcast") {
          window.__teamMsgOps.push({ url, op });
          return json(403, { error: "not_lead", code: "not_lead" });
        }
      }
      return window.__teamMsgOrigFetch(input, init);
    };
    return true;
  })()`;
}

async function typeBody(page, text) {
  await page.evaluate(`(() => {
    const ta = document.querySelector(".team-message-body");
    if (!ta) return false;
    ta.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, ${JSON.stringify(text)});
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: "x" }));
    return true;
  })()`);
}

export async function scenario(ctx) {
  const { page, waitUntil, check, click, shoot, press, log } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await page.evaluate(shim(NO_TEAM));
  await click(".side-tabs button", "Activity");

  // The panel polls /v1/team on its own tick; give it two polls.
  await delay(5000);
  const absent = await page.evaluate(
    `!document.querySelector("[data-team-message-open]") && !document.querySelector("[data-team-message-all]")`,
  );
  check(Boolean(absent), "Message action rendered without a live team");

  await page.evaluate(shim(TEAM));
  const rows = await waitUntil(
    `document.querySelectorAll(".activity-row [data-team-message-open]").length === 2`,
    10000,
    "Message action on both child rows",
  );
  check(Boolean(rows), "child rows did not get a Message action with teamControl and a live team");
  if (!rows) return;
  check(
    Boolean(await page.evaluate(`Boolean(document.querySelector("[data-team-message-all]"))`)),
    "activity head has no Message all action",
  );

  // Terminal child: the composer explains, no form.
  await page.evaluate(`document.querySelector('[data-team-message-open="kid-done"]').click()`);
  const terminal = await waitUntil(
    `document.querySelector("[data-team-message-terminal]")?.textContent || ""`,
    4000,
    "terminal child note",
  );
  check(/fixer has finished/.test(String(terminal)), `terminal child note missing: ${terminal}`);
  check(
    Boolean(await page.evaluate(`!document.querySelector(".team-message-body")`)),
    "terminal child still offered a message form",
  );

  // Running child: send carries target/root/key; outcome names messageId.
  await page.evaluate(`document.querySelector('[data-team-message-open="kid-run"]').click()`);
  await waitUntil(`Boolean(document.querySelector(".team-message-body"))`, 4000, "composer form");
  await page.evaluate(`document.querySelector('[data-team-message-urgency="high"]').click()`);
  await typeBody(page, "stop editing main.go, use lib.go");
  await shoot("team-message-child", ["desk1024", "desk1440", "mobile"]);
  await page.evaluate(`document.querySelector("[data-team-message-send]").click()`);
  const delivered = await waitUntil(
    `document.querySelector('[data-team-message-outcome="delivered"]')?.textContent || ""`,
    6000,
    "delivered outcome",
  );
  check(/msg-uat-1/.test(String(delivered)), `delivered outcome does not name the messageId: ${delivered}`);
  const sent = await page.evaluate(`JSON.stringify(window.__teamMsgOps[0] || null)`);
  const first = JSON.parse(sent || "null");
  check(first?.op?.type === "team.message", `first op was not team.message: ${sent}`);
  check(first?.op?.data?.to === "kid-run", `team.message target wrong: ${sent}`);
  check(first?.op?.data?.urgency === "high", `team.message urgency wrong: ${sent}`);
  check(Boolean(first?.op?.data?.idempotencyKey), `team.message has no idempotencyKey: ${sent}`);
  check(
    Boolean(first?.op?.data?.rootSessionId) && String(first?.url || "").includes("root="),
    `team.message not scoped to the active root: ${sent}`,
  );

  // Broadcast via the same form: coded refusal renders its meaning.
  await page.evaluate(`document.querySelector('[data-team-message-target="all"]').click()`);
  await typeBody(page, "wrap up in five minutes");
  await page.evaluate(`document.querySelector("[data-team-message-send]").click()`);
  const refused = await waitUntil(
    `(() => {
      const el = document.querySelector('[data-team-message-outcome="refused"]');
      return el && el.getAttribute("data-code") === "not_lead" ? el.textContent : "";
    })()`,
    6000,
    "not_lead refusal",
  );
  check(/not the lead/.test(String(refused)), `not_lead refusal did not render its meaning: ${refused}`);
  await shoot("team-message-refused", ["desk1024"]);

  // Keyboard path: Escape from the textarea closes the composer.
  await page.evaluate(`document.querySelector(".team-message-body")?.focus()`);
  await press("Escape");
  const closed = await waitUntil(`!document.querySelector('[data-team-message="open"]')`, 4000, "composer closed");
  check(Boolean(closed), "Escape did not close the team message composer");
  log("team message scenario done");
}
