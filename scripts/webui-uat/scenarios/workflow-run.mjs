// Workflow run controls (#1574). Start a builtin workflow from the
// palette, see the composer chip show workflow + phase once the daemon
// reports `phase.changed`, refuse to silently replace it (the replace
// confirm names both workflows), render a daemon-rejected start
// verbatim, then stop through the confirm that names workflow + phase.

import { delay } from "../browser.mjs";

const NOTE = `(document.querySelector('[data-model-note]')?.innerText || "")`;
const CHIP = `(document.querySelector('[data-workflow-status]')?.innerText || "")`;
const DIALOG = `(document.querySelector("[data-confirm='dialog']")?.innerText || "")`;

async function typeInto(ctx, selector, text) {
  await ctx.page.evaluate(`document.querySelector(${JSON.stringify(selector)})?.focus()`);
  await ctx.page.send("Input.insertText", { text });
  await ctx.page.evaluate(`(() => {
    const input = document.querySelector(${JSON.stringify(selector)});
    if (!input || input.value.includes(${JSON.stringify(text)})) return;
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(text)});
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${JSON.stringify(text)} }));
  })()`);
  await delay(120);
}

// Opens the palette on "Start workflow…" and returns once its prompt shows.
async function openStart(ctx) {
  await ctx.page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  const listed = await ctx.waitUntil(
    `Boolean(document.querySelector('[data-command-id="workflow-start"]'))`,
    4000,
    "Start workflow command",
  );
  ctx.check(Boolean(listed), "palette did not list Start workflow…");
  if (!listed) return false;
  await ctx.page.evaluate(`document.querySelector('[data-command-id="workflow-start"]')?.click()`);
  await delay(150);
  return true;
}

async function startViaChoice(ctx, name) {
  if (!(await openStart(ctx))) return false;
  const choice = await ctx.waitUntil(
    `Boolean(document.querySelector('[data-choice-id="${name}"]'))`,
    6000,
    `choice ${name}`,
  );
  ctx.check(Boolean(choice), `Start workflow… did not offer ${name} from the daemon catalog`);
  if (!choice) return false;
  await ctx.page.evaluate(`document.querySelector('[data-choice-id="${name}"]')?.click()`);
  return true;
}

export async function scenario(ctx) {
  const { page, waitUntil, check, shoot } = ctx;
  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  const ready = await waitUntil(
    `Boolean(document.querySelector('[data-autonomy-status]'))`,
    8000,
    "composer chips",
  );
  check(Boolean(ready), "composer chips did not render");
  if (!ready) return;
  // Nothing rendered while no workflow is active.
  check(
    !(await page.evaluate(`Boolean(document.querySelector('[data-workflow-status]'))`)),
    "workflow chip rendered with no active workflow",
  );

  // Start: the chip appears only from daemon-reported phase.changed.
  if (!(await startViaChoice(ctx, "plan-implement"))) return;
  const started = await waitUntil(
    `${CHIP}.includes("plan-implement") && ${CHIP}.includes("plan")`,
    10000,
    "workflow chip after start",
  );
  check(Boolean(started), `chip did not show plan-implement · plan; note: ${await page.evaluate(NOTE)}`);
  if (!started) return;
  await shoot("workflow-run-active", ["desk1024", "desk1440", "mobile"]);

  // Replace-active requires a confirm naming both workflows; cancel keeps it.
  if (await startViaChoice(ctx, "review-fix")) {
    const asked = await waitUntil(`${DIALOG}.includes("review-fix")`, 4000, "replace confirm");
    const text = await page.evaluate(DIALOG);
    check(Boolean(asked), "starting over an active workflow did not ask first");
    check(
      text.includes("plan-implement") && text.includes("review-fix"),
      `replace confirm must name both workflows: ${text}`,
    );
    await shoot("workflow-run-replace", ["desk1440", "mobile"]);
    await page.evaluate(`document.querySelector("[data-confirm='cancel']")?.click()`);
    await delay(400);
    check(
      (await page.evaluate(CHIP)).includes("plan-implement"),
      "cancelling the replace confirm changed the active workflow",
    );
  }

  // A daemon-rejected start is rendered with the daemon's reason.
  if (await openStart(ctx)) {
    await typeInto(ctx, ".palette input", "uat-missing-workflow");
    await ctx.press("Enter");
    // Active workflow present → replace confirm first; accept to send it.
    const asked = await waitUntil(`${DIALOG}.includes("uat-missing-workflow")`, 4000, "replace confirm (missing)");
    if (asked) {
      await page.evaluate(`document.querySelector("[data-confirm='accept']")?.click()`);
    }
    const refused = await waitUntil(
      `${NOTE}.includes("unknown workflow") && ${NOTE}.includes("uat-missing-workflow")`,
      8000,
      "rejected start note",
    );
    check(Boolean(refused), `rejected start not shown verbatim; note: ${await page.evaluate(NOTE)}`);
    check(
      (await page.evaluate(CHIP)).includes("plan-implement"),
      "a rejected start changed the displayed workflow",
    );
  }

  // Stop: confirm names workflow + phase; accept clears the chip.
  await page.evaluate(`document.querySelector('[data-workflow-stop]')?.click()`);
  const stopAsk = await waitUntil(`${DIALOG}.includes("plan-implement")`, 4000, "stop confirm");
  const stopText = await page.evaluate(DIALOG);
  check(Boolean(stopAsk), "Stop did not ask first");
  check(stopText.includes('phase "plan"'), `stop confirm must name the phase: ${stopText}`);
  await shoot("workflow-run-stop", ["desk1440", "mobile"]);
  await page.evaluate(`document.querySelector("[data-confirm='accept']")?.click()`);
  const stopped = await waitUntil(
    `!document.querySelector('[data-workflow-status]')`,
    10000,
    "workflow chip cleared",
  );
  check(Boolean(stopped), `chip still shown after stop; note: ${await page.evaluate(NOTE)}`);
}
