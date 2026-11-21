// Pin a transcript message (#1528).
// Sends a message, pins the echo reply, checks the rail appears, reloads
// (the pin comes back from the daemon), jumps from the rail and checks
// the reply gets the cursor highlight, then repeats the jump at phone
// width. Finally unpins and checks the rail disappears entirely.

import { delay } from "../browser.mjs";

const FIRST = "uat pin source message";

const setDraft = (text) => `(() => {
  const ta = document.querySelector("#composer-input");
  if (!ta) return false;
  ta.focus();
  const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
  proto.set.call(ta, ${JSON.stringify(text)});
  ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(text)} }));
  return ta.value === ${JSON.stringify(text)};
})()`;

const PIN_REPLY = `(() => {
  const rows = [...document.querySelectorAll('.sx-msg[data-role="strike"]')];
  const row = rows[rows.length - 1];
  const btn = row && row.querySelector(".sx-pin");
  if (!btn) return "";
  btn.click();
  return row.getAttribute("data-msg") || "";
})()`;

const RAIL_TARGET = `(document.querySelector(".pin-rail button.pin-rail-item")?.getAttribute("data-pin-target") || "")`;

const JUMP = `(() => {
  const btn = document.querySelector(".pin-rail button.pin-rail-item");
  if (!btn) return "";
  btn.click();
  return btn.getAttribute("data-pin-target") || "";
})()`;

const HIGHLIGHT = (id) =>
  `Boolean(document.querySelector('.sx-msg.sx-on[data-msg="${id}"][data-role="strike"]'))`;

const IDLE = `(async () => {
  const res = await fetch("/v1/bootstrap");
  if (!res.ok) return false;
  const body = await res.json();
  return Boolean(body.status && body.status.busy === false);
})()`;

export async function scenario(ctx) {
  const { page, waitUntil, check, click, shoot, viewport } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  const ready = await waitUntil(`Boolean(document.querySelector("#composer-input"))`, 8000, "composer");
  check(Boolean(ready), "composer did not render");
  if (!ready) return;

  check(await page.evaluate(setDraft(FIRST)), "composer did not take the message");
  await click("button.send", "Send");
  const settled = await waitUntil(
    `Boolean(document.querySelector('.sx-msg[data-role="strike"] .sx-pin'))`,
    20000,
    "pinnable echo reply",
  );
  check(Boolean(settled), "the settled echo reply never offered Pin");
  if (!settled) return;
  check(Boolean(await waitUntil(IDLE, 20000, "session idle")), "session never went idle");

  check(!(await page.evaluate(`Boolean(document.querySelector(".pin-rail"))`)), "rail rendered before any pin");
  const pinnedId = await page.evaluate(PIN_REPLY);
  check(Boolean(pinnedId), "Pin action missing on the reply");
  const railed = await waitUntil(`${RAIL_TARGET} !== ""`, 6000, "pin rail");
  check(Boolean(railed), "pin rail did not appear after pinning");
  await shoot("pins-rail", ["desk1024", "desk1440"]);

  // Reload: the pin comes back from the daemon and still resolves.
  await page.send("Page.reload", {});
  const back = await waitUntil(`${RAIL_TARGET} !== ""`, 30000, "pin rail after reload");
  check(Boolean(back), "the pin did not survive a reload");
  if (!back) return;
  const target = await page.evaluate(JUMP);
  check(Boolean(target), "rail item has no jump target");
  check(Boolean(await waitUntil(HIGHLIGHT(target), 4000, "jump highlight")), "jumping from the rail did not highlight the pinned reply");
  await shoot("pins-jump", ["desk1440"]);

  // Phone width: the rail is there and the jump still works.
  await viewport(390, 844);
  await delay(300);
  const phoneTarget = await page.evaluate(JUMP);
  check(Boolean(phoneTarget), "rail missing at 390x844");
  check(Boolean(await waitUntil(HIGHLIGHT(phoneTarget), 4000, "phone jump highlight")), "phone jump did not highlight the pinned reply");
  await shoot("pins-phone", ["mobile"]);
  await viewport(1440, 900);
  await delay(200);

  // Unpin: the rail goes away entirely.
  await page.evaluate(`(() => {
    const btn = document.querySelector('.sx-msg[data-role="strike"] .sx-pin[aria-pressed="true"]');
    if (btn) btn.click();
  })()`);
  const cleared = await waitUntil(`!document.querySelector(".pin-rail")`, 6000, "rail removed");
  check(Boolean(cleared), "unpinning the last message left the rail on screen");
}
