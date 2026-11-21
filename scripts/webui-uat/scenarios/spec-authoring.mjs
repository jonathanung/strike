// Spec authoring (#1348): requirements (EARS) → design → tasks with phase
// gates, handed off to the Task runner. Error paths from the issue: approving
// design before requirements is refused with the reason on screen, and
// handing off with an uncovered requirement is refused with the reason.

import { delay } from "../browser.mjs";

const NOTICE = `(document.querySelector("[data-spec-notice]")?.innerText || "")`;

async function setNth(page, selector, index, text) {
  await page.evaluate(`(() => {
    const el = document.querySelectorAll(${JSON.stringify(selector)})[${index}];
    if (!el) return;
    const proto = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value");
    proto.set.call(el, ${JSON.stringify(text)});
    el.dispatchEvent(new InputEvent("input", { bubbles: true }));
  })()`);
  await delay(60);
}

async function clickAction(page, action) {
  await page.evaluate(`document.querySelector('[data-action="${action}"]')?.click()`);
}

async function tab(page, name) {
  await page.evaluate(`document.querySelector('[data-spec-tab="${name}"]')?.click()`);
  await delay(120);
}

async function statusIs(ctx, phase, label) {
  return ctx.waitUntil(
    `(document.querySelector('[data-spec-status="${phase}"]')?.innerText || "") === ${JSON.stringify(label)}`,
    8000,
    `${phase} ${label}`,
  );
}

export async function scenario(ctx) {
  const { page, waitUntil, check, shoot, baseURL } = ctx;
  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  const listed = await waitUntil(`Boolean(document.querySelector('[data-command-id="specs"]'))`, 4000, "Specs command");
  check(Boolean(listed), "palette did not list Specs");
  if (!listed) return;
  await page.evaluate(`document.querySelector('[data-command-id="specs"]')?.click()`);
  const opened = await waitUntil(`Boolean(document.querySelector("[data-spec-root]"))`, 6000, "specs page");
  check(Boolean(opened), "Specs page did not open");
  if (!opened) return;

  await setNth(page, "#spec-new-name", 0, "uat-spec");
  await setNth(page, "#spec-new-title", 0, "UAT export");
  await clickAction(page, "spec-create");
  const created = await waitUntil(`Boolean(document.querySelector('[data-spec-tab="requirements"]'))`, 8000, "spec created");
  check(Boolean(created), `spec was not created: ${await page.evaluate(NOTICE)}`);
  if (!created) return;

  // Requirements: one EARS criterion, one that is not.
  await clickAction(page, "spec-add-req");
  await clickAction(page, "spec-add-req");
  await delay(150);
  await setNth(page, ".spec-story", 0, "As a user I export a csv");
  await setNth(page, ".spec-criteria", 0, "WHEN I press export THE SYSTEM SHALL download a csv file");
  await setNth(page, ".spec-story", 1, "As a user I export json");
  await setNth(page, ".spec-criteria", 1, "json export works");
  await clickAction(page, "spec-save");
  const linted = await waitUntil(`Boolean(document.querySelector('[data-spec-req="R2"] [data-spec-lint]'))`, 8000, "EARS warning");
  check(Boolean(linted), `EARS warning not shown for R2: ${await page.evaluate(NOTICE)}`);
  check(
    !(await page.evaluate(`Boolean(document.querySelector('[data-spec-req="R1"] [data-spec-lint]'))`)),
    "valid EARS criterion was flagged",
  );
  await shoot("spec-requirements-ears", ["desk1024", "desk1440", "mobile"]);

  // Error path 1: design before requirements.
  await tab(page, "design");
  await clickAction(page, "spec-approve");
  const gate = await waitUntil(`${NOTICE}.includes("approve requirements first")`, 8000, "design gate refusal");
  check(Boolean(gate), `approving design first was not refused visibly: ${await page.evaluate(NOTICE)}`);
  await shoot("spec-design-gate-refused", ["desk1440", "mobile"]);

  await tab(page, "requirements");
  await clickAction(page, "spec-approve");
  check(Boolean(await statusIs(ctx, "requirements", "Approved")), "requirements did not approve");
  await tab(page, "design");
  await setNth(page, "#spec-design", 0, "## Design\nA CsvWriter and a JsonWriter behind one Exporter interface.");
  await clickAction(page, "spec-save");
  await delay(400);
  await clickAction(page, "spec-approve");
  check(Boolean(await statusIs(ctx, "design", "Approved")), `design did not approve: ${await page.evaluate(NOTICE)}`);

  // Tasks: cover only R1, then try to hand off.
  await tab(page, "tasks");
  await clickAction(page, "spec-add-task");
  await delay(120);
  await setNth(page, ".spec-task-title", 0, "Write the CSV exporter");
  await setNth(page, ".spec-task-refs", 0, "R1");
  await clickAction(page, "spec-save");
  const partial = await waitUntil(
    `(document.querySelector("[data-spec-coverage]")?.innerText || "").startsWith("1/2") && Boolean(document.querySelector("[data-spec-uncovered]"))`,
    8000,
    "coverage 1/2",
  );
  check(Boolean(partial), "coverage line did not show 1/2 with the uncovered requirement");
  await clickAction(page, "spec-approve");
  await statusIs(ctx, "tasks", "Approved");
  await shoot("spec-coverage-uncovered", ["desk1024", "desk1440", "mobile"]);
  await clickAction(page, "spec-handoff");
  const refused = await waitUntil(`${NOTICE}.includes("R2")`, 8000, "uncovered hand-off refusal");
  check(Boolean(refused), `hand-off with an uncovered requirement was not refused visibly: ${await page.evaluate(NOTICE)}`);

  // Cover R2, re-approve tasks, hand off.
  await clickAction(page, "spec-add-task");
  await delay(120);
  await setNth(page, ".spec-task-title", 1, "Write the JSON exporter");
  await setNth(page, ".spec-task-refs", 1, "R2");
  await clickAction(page, "spec-save");
  await waitUntil(`(document.querySelector("[data-spec-coverage]")?.innerText || "").startsWith("2/2")`, 8000, "coverage 2/2");
  await clickAction(page, "spec-approve");
  check(Boolean(await statusIs(ctx, "tasks", "Approved")), "tasks did not re-approve after covering R2");
  await clickAction(page, "spec-handoff");
  const handed = await waitUntil(`Boolean(document.querySelector("[data-spec-readonly]"))`, 10000, "handed off");
  check(Boolean(handed), `hand-off did not complete: ${await page.evaluate(NOTICE)}`);
  if (!handed) return;

  const plans = await fetch(`${baseURL}/v1/plans`).then((r) => r.json()).catch(() => ({}));
  check(
    (plans.plans || []).some((p) => (p.Title || p.title) === "UAT export"),
    "hand-off did not create the plan in /v1/plans",
  );
  await clickAction(page, "spec-open-tasks");
  const board = await waitUntil(
    `Boolean(document.querySelector("#task-runner")) && document.body.innerText.includes("UAT export")`,
    8000,
    "plan in task runner",
  );
  check(Boolean(board), "the handed-off plan is not in the Task runner");
  await shoot("spec-handoff-task-runner", ["desk1024", "desk1440", "mobile"]);
}
