// Session /loop in the cockpit (#1588).
// Starts a loop from the composer, checks the queue-pane row (id, interval,
// next fire), checks a daemon rejection (sub-second interval) lands as a
// note without a row, then stops the loop from its row.

import { delay } from "../browser.mjs";

const COMPOSER = `document.querySelector('[data-feature="composer-triggers"] textarea')`;

function submit(text) {
  return `(() => {
    const ta = ${COMPOSER};
    if (!ta) return false;
    ta.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, ${JSON.stringify(text)});
    ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(text)} }));
    const form = ta.closest("form");
    if (!form) return false;
    form.requestSubmit();
    return true;
  })()`;
}

export async function scenario(ctx) {
  const { page, waitUntil, check, shoot } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  const ready = await waitUntil(`Boolean(${COMPOSER})`, 8000, "composer");
  check(Boolean(ready), "composer did not render");
  if (!ready) return;

  // The loops capability arrives with bootstrap; the catalog gains /loop.
  await delay(500);

  check(await page.evaluate(submit("/loop 1h uat loop job")), "could not submit /loop start");
  const row = await waitUntil(
    `Boolean(document.querySelector('.loop-list li[data-loop-id]'))`,
    8000,
    "loop row",
  );
  check(Boolean(row), "loop row did not appear after /loop 1h");
  if (!row) return;
  const rowText = await page.evaluate(
    `(document.querySelector('.loop-list li[data-loop-id]')?.innerText || "")`,
  );
  check(rowText.includes("every 1h"), `loop row missing interval: ${rowText}`);
  check(rowText.includes("uat loop job"), `loop row missing job: ${rowText}`);
  check(/next in|due now/.test(rowText), `loop row missing next fire: ${rowText}`);
  const draftLeft = await page.evaluate(`(${COMPOSER}?.value || "")`);
  check(draftLeft === "", `composer kept the /loop line: ${draftLeft}`);
  await shoot("session-loop-row", ["mobile", "desk1024", "desk1440"]);

  check(await page.evaluate(submit("/loop 500ms too fast")), "could not submit bad /loop");
  const rejected = await waitUntil(
    `(document.querySelector('.loop-note')?.innerText || "").includes("too short")`,
    8000,
    "sub-second rejection",
  );
  check(Boolean(rejected), "sub-second interval was not rejected with the daemon's reason");
  const count = await page.evaluate(`document.querySelectorAll('.loop-list li[data-loop-id]').length`);
  check(count === 1, `rejected loop added a row (rows=${count})`);

  await page.evaluate(`document.querySelector('.loop-list li[data-loop-id] button')?.click()`);
  const gone = await waitUntil(
    `!document.querySelector('.loop-list li[data-loop-id]')`,
    8000,
    "loop stopped",
  );
  check(Boolean(gone), "Stop did not remove the loop row");
  const note = await page.evaluate(`(document.querySelector('.loop-note')?.innerText || "")`);
  check(note.includes("stopped"), `stop note missing: ${note}`);
}
