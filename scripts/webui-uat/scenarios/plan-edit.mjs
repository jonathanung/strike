// Task runner plan editing (#1480). Create a plan, then from the board:
// retitle it, add a section, edit a section body, set status, see a
// daemon refusal (editing a closed plan) keep the user's text, reopen,
// reload and verify every edit persisted in the daemon's plan store.

import { delay } from "../browser.mjs";

async function api(baseURL, method, path, body) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  if (!res.ok) throw new Error(`${method} ${path} -> ${res.status} ${text.slice(0, 300)}`);
  return text ? JSON.parse(text) : null;
}

async function setValue(page, selector, text) {
  await page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return;
    const proto = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(el), "value");
    proto.set.call(el, ${JSON.stringify(text)});
    el.dispatchEvent(new InputEvent("input", { bubbles: true }));
  })()`);
  await delay(80);
}

async function submit(page, form) {
  await page.evaluate(
    `document.querySelector('[data-plan-edit="${form}"] button[type="submit"]')?.click()`,
  );
}

async function setStatus(page, value) {
  await page.evaluate(`(() => {
    const el = document.querySelector('[data-action="plan-status"]');
    if (!el) return;
    el.value = ${JSON.stringify(value)};
    el.dispatchEvent(new Event("change", { bubbles: true }));
  })()`);
}

async function openBoard(ctx, title) {
  const { page, waitUntil } = ctx;
  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector('[data-command-id="tasks"]'))`, 4000, "tasks command");
  await page.evaluate(`document.querySelector('[data-command-id="tasks"]')?.click()`);
  await waitUntil(
    `Boolean(document.querySelector("#task-runner")) && document.body.innerText.includes(${JSON.stringify(title)})`,
    8000,
    "task runner with plan",
  );
  await page.evaluate(`(() => {
    const b = [...document.querySelectorAll(".task-plan")].find((el) => el.innerText.includes(${JSON.stringify(title)}));
    b?.click();
  })()`);
  return waitUntil(
    `Boolean(document.querySelector('[data-action="plan-rename"]')) && document.querySelector(".task-board .panel-title")?.innerText.includes(${JSON.stringify(title)})`,
    8000,
    "plan open on board",
  );
}

export async function scenario(ctx) {
  const { page, waitUntil, check, shoot, baseURL } = ctx;
  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  const roots = await api(baseURL, "GET", "/v1/roots").catch(() => ({}));
  const owner = roots.activeId || roots.roots?.[0]?.id || "";
  check(Boolean(owner), "plan edit needs a live root to own the plan");
  if (!owner) return;
  const created = await api(baseURL, "POST", "/v1/plans", {
    ownerRoot: owner,
    title: "Edit me",
    sections: [{ title: "First step", body: "wrong instruction" }],
  });
  const id = created.ID || created.id;
  check(Boolean(id), "plan was not created");
  if (!id) return;

  check(Boolean(await openBoard(ctx, "Edit me")), "plan did not open on the board");
  await shoot("plan-edit-before", ["desk1024", "desk1440", "mobile"]);

  // Retitle.
  await page.evaluate(`document.querySelector('[data-action="plan-rename"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector("#plan-edit-title"))`, 3000, "rename form");
  await setValue(page, "#plan-edit-title", "Edited plan");
  await submit(page, "title");
  const renamed = await waitUntil(
    `document.querySelector(".task-board .panel-title")?.innerText.includes("Edited plan") && !document.querySelector("#plan-edit-title")`,
    8000,
    "retitled",
  );
  check(Boolean(renamed), "retitle did not land on the board");

  // Add a section.
  await page.evaluate(`document.querySelector('[data-action="plan-add-section"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector("#plan-add-title"))`, 3000, "add form");
  await setValue(page, "#plan-add-title", "Second step");
  await setValue(page, "#plan-add-body", "added from the cockpit");
  await submit(page, "add");
  const added = await waitUntil(
    `[...document.querySelectorAll(".task-row .name")].some((el) => el.innerText.includes("Second step"))`,
    8000,
    "added section",
  );
  check(Boolean(added), "added section did not appear on the board");

  // Edit the first section's body.
  await page.evaluate(`(() => {
    const r = [...document.querySelectorAll(".task-row")].find((el) => el.innerText.includes("First step"));
    r?.click();
  })()`);
  await delay(200);
  await page.evaluate(`document.querySelector('[data-action="plan-edit-section"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector("#plan-section-body"))`, 3000, "section form");
  await shoot("plan-edit-section", ["desk1440", "mobile"]);
  await setValue(page, "#plan-section-body", "corrected instruction");
  await submit(page, "section");
  const edited = await waitUntil(
    `!document.querySelector("#plan-section-body") && (document.querySelector(".task-read .task-check")?.innerText || "").includes("corrected instruction")`,
    8000,
    "edited section",
  );
  check(Boolean(edited), "section edit did not land in the read view");

  // Set status.
  await setStatus(page, "approved");
  const approved = await waitUntil(
    `Boolean(document.querySelector('[data-plan-status="approved"]'))`,
    8000,
    "status approved",
  );
  check(Boolean(approved), "set status did not reach approved");

  // Daemon refusal keeps the user's text: close, then try to retitle.
  await setStatus(page, "closed");
  await waitUntil(`Boolean(document.querySelector('[data-plan-status="closed"]'))`, 8000, "status closed");
  await page.evaluate(`document.querySelector('[data-action="plan-rename"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector("#plan-edit-title"))`, 3000, "rename form (closed)");
  await setValue(page, "#plan-edit-title", "Should be refused");
  await submit(page, "title");
  const refused = await waitUntil(
    `(document.querySelector("[data-plan-edit-error]")?.innerText || "").length > 0`,
    8000,
    "refusal shown",
  );
  check(Boolean(refused), "editing a closed plan did not show the daemon's refusal");
  check(
    (await page.evaluate(`document.querySelector("#plan-edit-title")?.value || ""`)) === "Should be refused",
    "the refused title was not preserved in the form",
  );
  await shoot("plan-edit-refused", ["desk1440", "mobile"]);
  await page.evaluate(`document.querySelector('[data-plan-edit="title"] button[type="button"]')?.click()`);
  await page.evaluate(`document.querySelector('[data-action="plan-reopen"]')?.click()`);
  const reopened = await waitUntil(
    `Boolean(document.querySelector('[data-plan-status="draft"]'))`,
    8000,
    "reopened",
  );
  check(Boolean(reopened), "reopen did not return the plan to draft");

  // Persistence: the daemon store and a reload agree.
  const stored = await api(baseURL, "GET", `/v1/plans/${id}`);
  const sections = stored.sections || [];
  check(stored.title === "Edited plan", `stored title is ${stored.title}`);
  check(sections.length === 2 && sections[1].title === "Second step", "stored sections missing the added one");
  check((sections[0]?.body || "").includes("corrected instruction"), "stored first section body not edited");
  await page.send("Page.navigate", { url: baseURL });
  await delay(1500);
  check(Boolean(await openBoard(ctx, "Edited plan")), "edited plan did not persist across reload");
  await shoot("plan-edit-after-reload", ["desk1024", "desk1440", "mobile"]);
}
