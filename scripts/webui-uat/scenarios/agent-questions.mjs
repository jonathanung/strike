// Agent question card (#1338). The echo provider turns "ask <text>" into a
// real question tool call (one choice question, one free-form question), so
// this drives the daemon's question.asked / question.reply path end to end:
//   1. answer both questions from the card and see the tool succeed;
//   2. ask again, dismiss, and see the turn end with no card left and the
//      session row no longer waiting on a question.

import { delay } from "../browser.mjs";

const CARD = ".question-card";

async function activeRoot(baseURL) {
  const roots = await fetch(`${baseURL}/v1/roots`).then((r) => r.json());
  return roots.activeId || roots.active_id || roots.roots?.[0]?.id || "";
}

async function sendAsk(baseURL, root, text) {
  return fetch(`${baseURL}/v1/ops?root=${encodeURIComponent(root)}`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ type: "user.input", data: { text } }),
  });
}

const IDLE = `(async () => {
  const res = await fetch("/v1/bootstrap");
  if (!res.ok) return false;
  const body = await res.json();
  return Boolean(body.status && body.status.busy === false && !body.status.questionPending);
})()`;

export async function scenario(ctx) {
  const { page, baseURL, waitUntil, check, shoot, viewport } = ctx;
  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);

  const root = await activeRoot(baseURL);
  check(Boolean(root), "no live root for the question scenario");
  if (!root) return;

  // Happy path: answer from the card.
  const sent = await sendAsk(baseURL, root, "ask Use Postgres?");
  check(sent.ok, `user.input HTTP ${sent.status}`);
  const shown = await waitUntil(
    `(() => {
      const card = document.querySelector(${JSON.stringify(CARD)});
      return Boolean(card && card.innerText.includes("Use Postgres?") && card.innerText.includes("Yes (Recommended)"));
    })()`,
    20000,
    "question card",
  );
  check(Boolean(shown), "question card did not render the asked question and its options");
  if (!shown) return;
  const waiting = await waitUntil(
    `(() => /Question/.test(document.querySelector(".session-row.on")?.innerText || ""))()`,
    8000,
    "session row waiting on a question",
  );
  check(Boolean(waiting), "session row did not show the question waiting state");
  await shoot("question-card", ["mobile", "desk1024", "desk1440"]);
  await viewport(1440, 900);
  await delay(250);

  // Next without a pick is refused in place.
  await page.evaluate(`document.querySelector('${CARD} [data-question-next]').click()`);
  const refused = await waitUntil(`Boolean(document.querySelector('${CARD} .question-error'))`, 3000, "unanswered refusal");
  check(Boolean(refused), "Next on an unanswered question did not explain why");

  await page.evaluate(`document.querySelector('${CARD} [data-question-option="0"]').click()`);
  await page.evaluate(`document.querySelector('${CARD} [data-question-next]').click()`);
  const second = await waitUntil(
    `(() => {
      const step = document.querySelector('${CARD} [data-question-step="1"]');
      return Boolean(step && !step.hidden);
    })()`,
    3000,
    "second question",
  );
  check(Boolean(second), "Next did not move to the second question");
  await page.evaluate(`(() => {
    const input = document.querySelector('${CARD} [data-question-text="1"]');
    input.value = "keep the old API";
    input.dispatchEvent(new Event("input", { bubbles: true }));
  })()`);
  await page.evaluate(`document.querySelector('${CARD} [data-question-next]').click()`);
  const answered = await waitUntil(
    `(() => !document.querySelector(${JSON.stringify(CARD)}) &&
      (document.querySelector(".transcript")?.innerText || "").includes("The tool call succeeded"))()`,
    20000,
    "answered question",
  );
  check(Boolean(answered), "submitting answers did not resume the tool call");
  check(Boolean(await waitUntil(IDLE, 20000, "idle after answer")), "session did not go idle after answering");

  // Error path: dismiss interrupts the turn instead of hanging.
  const again = await sendAsk(baseURL, root, "ask Drop the table?");
  check(again.ok, `user.input HTTP ${again.status}`);
  const shownAgain = await waitUntil(`Boolean(document.querySelector(${JSON.stringify(CARD)}))`, 20000, "second card");
  check(Boolean(shownAgain), "second question card did not render");
  if (!shownAgain) return;
  await page.evaluate(`document.querySelector('${CARD} [data-question-dismiss]').click()`);
  const gone = await waitUntil(`!document.querySelector(${JSON.stringify(CARD)})`, 8000, "dismissed card");
  check(Boolean(gone), "dismissed card stayed on screen");
  check(Boolean(await waitUntil(IDLE, 20000, "idle after dismiss")), "turn did not end after dismissing the question");
  const cleared = await waitUntil(
    `(() => !/Question/.test(document.querySelector(".session-row.on")?.innerText || ""))()`,
    8000,
    "session row cleared",
  );
  check(Boolean(cleared), "session row kept the question waiting state after dismiss");
  await shoot("question-dismissed", ["desk1440"]);
}
