// Quote a message (#1362).
// Sends a message, waits for the echo reply, quotes the reply with its
// Quote action, checks the composer chip, removes it with Backspace on
// an empty composer, quotes again, sends a message with the quote and
// checks the card renders above the sent message (and survives a
// reload) and that clicking it moves the transcript cursor onto the
// quoted reply.

import { delay } from "../browser.mjs";

const FIRST = "uat quote source message";
const REPLY = "uat quote reply text";

const setDraft = (text) => `(() => {
  const ta = document.querySelector("#composer-input");
  if (!ta) return false;
  ta.focus();
  const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
  proto.set.call(ta, ${JSON.stringify(text)});
  ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(text)} }));
  return ta.value === ${JSON.stringify(text)};
})()`;

const TRANSCRIPT = `(document.querySelector(".transcript")?.innerText || "")`;
const CHIP = `(document.querySelector(".quote-chip-text")?.innerText || "")`;

// Quote action on the newest Strike reply.
const QUOTE_REPLY = `(() => {
  const rows = [...document.querySelectorAll('.sx-msg[data-role="strike"]')];
  const row = rows[rows.length - 1];
  const btn = row && row.querySelector(".sx-quote-btn");
  if (!btn) return false;
  btn.click();
  return true;
})()`;

const IDLE = `(async () => {
  const res = await fetch("/v1/bootstrap");
  if (!res.ok) return false;
  const body = await res.json();
  return Boolean(body.status && body.status.busy === false);
})()`;

export async function scenario(ctx) {
  const { page, waitUntil, check, click, shoot } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  const ready = await waitUntil(`Boolean(document.querySelector("#composer-input"))`, 8000, "composer");
  check(Boolean(ready), "composer did not render");
  if (!ready) return;

  check(await page.evaluate(setDraft(FIRST)), "composer did not take the first message");
  await click("button.send", "Send");
  const echoed = await waitUntil(
    `${TRANSCRIPT}.includes("You said:") && Boolean(document.querySelector('.sx-msg[data-role="strike"] .sx-quote-btn'))`,
    20000,
    "quotable echo reply",
  );
  check(Boolean(echoed), "the echo reply never offered Quote");
  if (!echoed) return;
  check(Boolean(await waitUntil(IDLE, 20000, "session idle")), "session never went idle");

  // Quoting alone sends nothing.
  const before = await page.evaluate(`document.querySelectorAll('.sx-msg[data-role="you"]').length`);
  check(await page.evaluate(QUOTE_REPLY), "Quote action missing on the reply");
  const chip = await waitUntil(`${CHIP}.startsWith("Strike: ")`, 4000, "quote chip");
  check(Boolean(chip), "quote chip did not appear in the composer");
  await shoot("quote-chip", ["desk1024", "desk1440", "mobile"]);
  await delay(300);
  const after = await page.evaluate(`document.querySelectorAll('.sx-msg[data-role="you"]').length`);
  check(before === after, "quoting sent a message");

  // Backspace on the empty composer removes the chip.
  await page.evaluate(`(() => {
    const ta = document.querySelector("#composer-input");
    ta.focus();
    ta.dispatchEvent(new KeyboardEvent("keydown", { key: "Backspace", bubbles: true, cancelable: true }));
  })()`);
  const removed = await waitUntil(`!document.querySelector(".quote-chip")`, 4000, "chip removed");
  check(Boolean(removed), "Backspace on an empty composer did not remove the quote");

  // Quote again and send with a message.
  check(await page.evaluate(QUOTE_REPLY), "second Quote failed");
  check(Boolean(await waitUntil(`Boolean(document.querySelector(".quote-chip"))`, 4000, "chip back")), "chip missing");
  check(await page.evaluate(setDraft(REPLY)), "composer did not take the reply text");
  await click("button.send", "Send");
  const carded = await waitUntil(
    `(() => {
      const rows = [...document.querySelectorAll('.sx-msg[data-role="you"]')];
      const row = rows.find((r) => (r.innerText || "").includes(${JSON.stringify(REPLY)}));
      return Boolean(row && row.querySelector("button.sx-quote-card") && !(row.innerText || "").includes("[Quoting"));
    })()`,
    20000,
    "sent message with quote card",
  );
  check(Boolean(carded), "sent message did not render a quote card (or showed the raw block)");
  const chipGone = await waitUntil(`!document.querySelector(".quote-chip")`, 4000, "chip cleared after send");
  check(Boolean(chipGone), "quote chip stayed after a successful send");
  await shoot("quote-card", ["desk1024", "desk1440"]);

  // Jump back moves the transcript cursor onto the quoted reply.
  check(Boolean(await waitUntil(IDLE, 20000, "idle before jump")), "session never went idle");
  const target = await page.evaluate(`(() => {
    const card = [...document.querySelectorAll("button.sx-quote-card")].pop();
    if (!card) return "";
    const id = card.getAttribute("data-quote-target") || "";
    card.click();
    return id;
  })()`);
  check(Boolean(target), "quote card has no jump target");
  const highlighted = await waitUntil(
    `Boolean(document.querySelector('.sx-msg.sx-on[data-msg="${target}"][data-role="strike"]'))`,
    4000,
    "jump-back highlight",
  );
  check(Boolean(highlighted), "jump-back did not highlight the quoted reply");
  await shoot("quote-jump", ["desk1440"]);

  // Reload: the card comes back from the stored session log.
  await page.send("Page.reload", {});
  const reloaded = await waitUntil(
    `(() => {
      const rows = [...document.querySelectorAll('.sx-msg[data-role="you"]')];
      const row = rows.find((r) => (r.innerText || "").includes(${JSON.stringify(REPLY)}));
      return Boolean(row && row.querySelector(".sx-quote-card"));
    })()`,
    30000,
    "quote card after reload",
  );
  check(Boolean(reloaded), "the quote card did not survive a reload");
}
