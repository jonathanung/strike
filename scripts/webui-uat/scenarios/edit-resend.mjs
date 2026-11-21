// Edit last message and resend (#1361).
// Sends a message with a typo, recalls it with Ctrl+Up into an empty
// composer, abandons with Esc (draft cleared, nothing sent), recalls
// again, fixes the text and resends. The confirm must name what is
// discarded, and after the engine's rewind the transcript must read as
// if only the fixed message was sent.

import { delay } from "../browser.mjs";

const TYPO = "uat edit resend teh typo";
const FIXED = "uat edit resend the fix";

const setDraft = (text) => `(() => {
  const ta = document.querySelector("#composer-input");
  if (!ta) return false;
  ta.focus();
  const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
  proto.set.call(ta, ${JSON.stringify(text)});
  ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(text)} }));
  return ta.value === ${JSON.stringify(text)};
})()`;

const key = (name, ctrl) => `(() => {
  const ta = document.querySelector("#composer-input");
  if (!ta) return false;
  ta.focus();
  ta.dispatchEvent(new KeyboardEvent("keydown", { key: ${JSON.stringify(name)}, ctrlKey: ${ctrl}, bubbles: true, cancelable: true }));
  return true;
})()`;

const DRAFT = `(document.querySelector("#composer-input")?.value || "")`;
const BANNER = `(document.querySelector(".edit-resend-note")?.innerText || "")`;
const TRANSCRIPT = `(document.querySelector(".transcript")?.innerText || "")`;

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

  check(await page.evaluate(setDraft(TYPO)), "composer did not take the typo message");
  await click("button.send", "Send");
  const echoed = await waitUntil(
    `${TRANSCRIPT}.includes(${JSON.stringify(TYPO)}) && ${TRANSCRIPT}.includes("You said:")`,
    20000,
    "typo message echoed",
  );
  check(Boolean(echoed), "the typo message was never answered");
  check(Boolean(await waitUntil(IDLE, 20000, "session idle")), "session never went idle");

  // Keyboard recall into an empty composer.
  await page.evaluate(key("ArrowUp", true));
  const recalled = await waitUntil(`${DRAFT} === ${JSON.stringify(TYPO)}`, 4000, "recalled text");
  check(Boolean(recalled), "Ctrl+Up did not load the last message into the composer");
  const banner = await page.evaluate(BANNER);
  check(String(banner).includes("Editing your last message"), `edit banner missing: ${banner}`);
  await shoot("edit-resend-recalled", ["desk1024", "desk1440"]);

  // Esc abandons with no side effects.
  await page.evaluate(key("Escape", false));
  const abandoned = await waitUntil(`${DRAFT} === "" && ${BANNER} === ""`, 4000, "abandoned edit");
  check(Boolean(abandoned), "Esc did not abandon the edit");
  await delay(300);
  const stillTypo = await page.evaluate(`${TRANSCRIPT}.includes(${JSON.stringify(TYPO)})`);
  check(Boolean(stillTypo), "abandoning the edit changed the transcript");

  // Recall, fix, resend through the confirm.
  await page.evaluate(key("ArrowUp", true));
  check(
    Boolean(await waitUntil(`${DRAFT} === ${JSON.stringify(TYPO)}`, 4000, "recalled again")),
    "second recall failed",
  );
  check(await page.evaluate(setDraft(FIXED)), "composer did not take the fixed text");
  await click("button.send", "Send");
  const asked = await waitUntil(`Boolean(document.querySelector('[data-confirm="accept"]'))`, 4000, "discard confirm");
  check(Boolean(asked), "resend did not ask before discarding");
  if (!asked) return;
  const body = await page.evaluate(`document.body.innerText`);
  check(String(body).includes("This discards"), "confirm does not name what is discarded");
  await shoot("edit-resend-confirm", ["desk1024", "desk1440"]);
  await page.evaluate(`document.querySelector('[data-confirm="accept"]')?.click()`);

  const resent = await waitUntil(
    `${TRANSCRIPT}.includes(${JSON.stringify(FIXED)}) && !${TRANSCRIPT}.includes(${JSON.stringify(TYPO)})`,
    20000,
    "transcript after resend",
  );
  check(Boolean(resent), "after resend the transcript still shows the typo or lacks the fix");
  const cleared = await waitUntil(`${DRAFT} === ""`, 4000, "draft cleared after resend");
  check(Boolean(cleared), "the composer kept the draft after a successful resend");
  await shoot("edit-resend-after", ["desk1024", "desk1440"]);
}
