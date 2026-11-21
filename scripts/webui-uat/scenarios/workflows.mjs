// Workflows view (#1501). The catalog renders from GET /v1/workflows, a
// document opens with its phases, Review of a broken draft reports the
// failure and blocks Save, Review of a draft with an allow grant shows
// the permission widening, and Save draft persists it inactive (the
// catalog lists it after reload; the session's status never runs it).

import { delay } from "../browser.mjs";

const INVALID = JSON.stringify(
  {
    schemaVersion: 1,
    name: "uat-broken",
    description: "check gate without a command",
    phases: [{ name: "verify", exit: { type: "check" } }],
  },
  null,
  2,
);

const WIDEN = JSON.stringify(
  {
    schemaVersion: 1,
    name: "uat-widen",
    description: "UAT draft that widens bash for one pattern",
    phases: [
      {
        name: "build",
        permissions: [{ permission: "bash", pattern: "uat-widen-*", action: "allow" }],
        exit: { type: "agent" },
      },
    ],
  },
  null,
  2,
);

async function setDraft(page, text) {
  await page.evaluate(`(() => {
    const el = document.querySelector("#wf-draft");
    if (!el) return;
    const proto = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, "value");
    proto.set.call(el, ${JSON.stringify(text)});
    el.dispatchEvent(new InputEvent("input", { bubbles: true }));
  })()`);
  await delay(120);
}

async function openView(ctx) {
  await ctx.page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  const listed = await ctx.waitUntil(
    `Boolean(document.querySelector('[data-command-id="workflows"]'))`,
    4000,
    "Workflows command",
  );
  ctx.check(Boolean(listed), "palette did not list Workflows");
  if (!listed) return false;
  await ctx.page.evaluate(`document.querySelector('[data-command-id="workflows"]')?.click()`);
  return Boolean(
    await ctx.waitUntil(`Boolean(document.querySelector("[data-workflows-root]"))`, 6000, "workflows view"),
  );
}

export async function scenario(ctx) {
  const { page, waitUntil, check, shoot, baseURL } = ctx;
  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  if (!(await openView(ctx))) {
    check(false, "Workflows view did not open");
    return;
  }

  const catalog = await waitUntil(
    `Boolean(document.querySelector('[data-wf-row="plan-implement"]'))`,
    8000,
    "catalog rows",
  );
  check(Boolean(catalog), "catalog did not list the builtin plan-implement");
  if (!catalog) return;
  await shoot("workflows-catalog", ["desk1024", "desk1440", "mobile"]);

  await page.evaluate(`document.querySelector('[data-wf-row="plan-implement"]')?.click()`);
  const opened = await waitUntil(
    `(() => {
      const d = document.querySelector("[data-wf-doc]");
      return Boolean(d && d.querySelector('[data-wf-phase="plan"]') && d.innerText.includes("deny write"));
    })()`,
    8000,
    "document phases",
  );
  check(Boolean(opened), "document view did not render phases with their permissions");
  await shoot("workflows-document", ["desk1440", "mobile"]);

  await page.evaluate(`document.querySelector('[data-action="wf-edit-draft"]')?.click()`);
  const drafted = await waitUntil(
    `(document.querySelector("#wf-draft")?.value || "").includes("plan-implement")`,
    8000,
    "edit as draft",
  );
  check(Boolean(drafted), "Edit as draft did not load the formatted document");

  // Failing check: invalid draft → Review invalid, Save disabled.
  await setDraft(page, INVALID);
  await page.evaluate(`document.querySelector('[data-action="wf-review"]')?.click()`);
  const invalid = await waitUntil(
    `Boolean(document.querySelector('[data-wf-review="invalid"]'))`,
    8000,
    "invalid review",
  );
  check(Boolean(invalid), "Review of a broken draft did not report invalid");
  check(
    await page.evaluate(`Boolean(document.querySelector('[data-action="wf-save"]')?.disabled)`),
    "Save stayed enabled after a failing Review",
  );
  await page.evaluate(`document.querySelector('[data-action="wf-validate"]')?.click()`);
  const validated = await waitUntil(
    `(document.querySelector("[data-wf-validate]")?.innerText || "").startsWith("Validate")`,
    8000,
    "validate result",
  );
  check(Boolean(validated), "Validate did not report a daemon result");

  // Widening: Review names the widened grant.
  await setDraft(page, WIDEN);
  await page.evaluate(`document.querySelector('[data-action="wf-review"]')?.click()`);
  const widened = await waitUntil(
    `(document.querySelector("[data-wf-widening]")?.innerText || "").includes("bash")`,
    8000,
    "widening review",
  );
  check(
    Boolean(widened),
    `Review did not show the bash widening: ${await page.evaluate(`document.querySelector(".wf-review")?.innerText || ""`)}`,
  );
  await shoot("workflows-review-widening", ["desk1024", "desk1440", "mobile"]);

  // Save inactive behind a confirm that names the widening.
  await page.evaluate(`document.querySelector('[data-action="wf-save"]')?.click()`);
  const asked = await waitUntil(
    `(document.querySelector("[data-confirm='dialog']")?.innerText || "").includes("inactive")`,
    4000,
    "save confirm",
  );
  const askText = await page.evaluate(`document.querySelector("[data-confirm='dialog']")?.innerText || ""`);
  check(Boolean(asked), "Save draft did not ask first");
  check(askText.includes("bash"), `save confirm must name the widening: ${askText}`);
  await shoot("workflows-save-confirm", ["desk1440", "mobile"]);
  await page.evaluate(`document.querySelector("[data-confirm='accept']")?.click()`);
  const saved = await waitUntil(
    `(document.querySelector("[data-wf-saved]")?.innerText || "").includes("Saved inactive")`,
    8000,
    "save result",
  );
  check(
    Boolean(saved),
    `save did not report inactive: ${await page.evaluate(`document.querySelector("[data-wf-saved]")?.innerText || ""`)}`,
  );

  // Persisted: the catalog lists it after a reload; it is not running.
  const list = await fetch(`${baseURL}/v1/workflows`).then((r) => r.json()).catch(() => ({}));
  check(
    (list.workflows || []).some((w) => w.name === "uat-widen"),
    "saved draft is not in the daemon catalog",
  );
  await page.send("Page.navigate", { url: baseURL });
  await delay(1500);
  if (await openView(ctx)) {
    const persisted = await waitUntil(
      `Boolean(document.querySelector('[data-wf-row="uat-widen"]'))`,
      8000,
      "persisted row",
    );
    check(Boolean(persisted), "saved draft did not persist across reload");
  }
  const status = await fetch(`${baseURL}/v1/status`).then((r) => r.json()).catch(() => ({}));
  check(status.workflow !== "uat-widen", "saving a draft activated it");
}
