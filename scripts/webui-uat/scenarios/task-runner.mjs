// Task runner pass for the cockpit UAT. Echo cannot satisfy schema:handoff
// unless the section body contains a fenced handoff, so the happy plan puts
// one in the body and the blocked plan does not.

import { delay } from "../browser.mjs";

const HANDOFF = [
  "```json",
  JSON.stringify({
    summary: "section done",
    files_changed: [],
    findings: [],
    blockers: [],
  }),
  "```",
].join("\n");

const KEYS = {
  j: { key: "j", code: "KeyJ", vk: 74 },
  k: { key: "k", code: "KeyK", vk: 75 },
  r: { key: "r", code: "KeyR", vk: 82 },
  a: { key: "a", code: "KeyA", vk: 65 },
  s: { key: "s", code: "KeyS", vk: 83 },
  t: { key: "t", code: "KeyT", vk: 84 },
  "[": { key: "[", code: "BracketLeft", vk: 219 },
  "]": { key: "]", code: "BracketRight", vk: 221 },
};

async function api(baseURL, method, path, body) {
  const res = await fetch(`${baseURL}${path}`, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body ? JSON.stringify(body) : undefined,
  });
  const text = await res.text();
  let json = null;
  try {
    json = text ? JSON.parse(text) : null;
  } catch {
    json = { raw: text };
  }
  if (!res.ok) {
    throw new Error(`${method} ${path} -> ${res.status} ${text.slice(0, 400)}`);
  }
  return json;
}

function planIdOf(plan) {
  return plan.ID || plan.id || "";
}

async function pollRun(baseURL, id, pred, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  let last = null;
  while (Date.now() < deadline) {
    last = await api(baseURL, "GET", `/v1/plans/${id}/run`);
    if (last.run && pred(last.run)) return last.run;
    await delay(400);
  }
  return last && last.run;
}

function statuses(run) {
  return (run?.sections || []).map((s) => `${s.title}:${s.status}`).join(", ");
}

async function press(page, name) {
  const def = KEYS[name];
  await page.evaluate(`document.querySelector("#task-runner")?.focus()`);
  await delay(40);
  const common = {
    key: def.key,
    code: def.code,
    windowsVirtualKeyCode: def.vk,
    nativeVirtualKeyCode: def.vk,
    modifiers: 0,
  };
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
  await delay(120);
}

export async function runTaskRunnerPass({ page, baseURL, check, shoot, click, waitUntil, log }) {
  let owner = "";
  try {
    const roots = await api(baseURL, "GET", "/v1/roots");
    owner = roots.activeId || roots.roots?.[0]?.id || "";
  } catch (err) {
    check(false, `task runner could not list roots: ${err.message}`);
    return;
  }
  check(Boolean(owner), "task runner needs a live root to own the plan");
  if (!owner) return;

  const blockedPlan = await api(baseURL, "POST", "/v1/plans", {
    ownerRoot: owner,
    title: "Blocked chain",
    sections: [
      { title: "Plain step", body: "plain echo body, no handoff" },
      { title: "Later step", body: "depends on the plain step" },
    ],
  });
  const blockedId = planIdOf(blockedPlan);
  check(Boolean(blockedId), "blocked plan was not created");
  if (!blockedId) return;
  const openedBlocked = await api(baseURL, "POST", `/v1/plans/${blockedId}/run`, {
    action: "open",
    ownerRoot: owner,
  });
  await api(baseURL, "POST", `/v1/plans/${blockedId}/run`, {
    action: "start",
    ownerRoot: owner,
    expectedVersion: openedBlocked.run.version,
  });
  const blocked = await pollRun(
    baseURL,
    blockedId,
    (run) => run.sections[0]?.status === "failed" && run.sections[1]?.status === "blocked",
    45000,
  );
  check(
    blocked?.sections?.[0]?.status === "failed" && blocked?.sections?.[1]?.status === "blocked",
    `plain section did not block its dependant (${statuses(blocked)})`,
  );

  const happyPlan = await api(baseURL, "POST", "/v1/plans", {
    ownerRoot: owner,
    title: "Review chain",
    sections: [
      { title: "Handoff step", body: HANDOFF },
      { title: "Next step", body: HANDOFF },
    ],
  });
  const happyId = planIdOf(happyPlan);
  check(Boolean(happyId), "review plan was not created");
  if (!happyId) return;
  await api(baseURL, "POST", `/v1/plans/${happyId}/run`, { action: "open", ownerRoot: owner });

  await page.evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
  await waitUntil(`Boolean(document.querySelector('[data-nav="palette-dialog"] input'))`, 4000, "task palette");
  await page.evaluate(`(() => {
    const input = document.querySelector('[data-nav="palette-dialog"] input');
    const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
    proto.set.call(input, "Task runner");
    input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "Task runner" }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
    return true;
  })()`);
  const board = await waitUntil(
    `document.querySelector("#task-runner") && document.body.innerText.includes("Review chain") && document.body.innerText.includes("Blocked chain")`,
    8000,
    "task runner board",
  );
  if (!board) return;
  log("task runner open");

  await click(".task-plan", "Review chain");
  await waitUntil(
    `document.querySelector(".task-row .name") && document.body.innerText.includes("Handoff step")`,
    6000,
    "review chain checklist",
  );
  await shoot("task-pending", ["desk1024", "desk1440", "mobile"]);

  const beforeOrder = (await api(baseURL, "GET", `/v1/plans/${happyId}/run`)).run.sections.map((s) => s.id);
  const firstTitle = (await api(baseURL, "GET", `/v1/plans/${happyId}/run`)).run.sections[0].title;
  const secondTitle = (await api(baseURL, "GET", `/v1/plans/${happyId}/run`)).run.sections[1].title;
  await press(page, "j");
  await press(page, "[");
  const reordered = await pollRun(
    baseURL,
    happyId,
    (run) => run.sections[0]?.id === beforeOrder[1],
    6000,
  );
  check(reordered?.sections?.[0]?.id === beforeOrder[1], `keyboard [ did not reorder (${statuses(reordered)})`);
  await waitUntil(
    `document.querySelector(".task-list .name")?.textContent?.includes(${JSON.stringify(secondTitle)})`,
    6000,
    "reordered row on the board",
  );
  await press(page, "]");
  const restored = await pollRun(
    baseURL,
    happyId,
    (run) => run.sections[0]?.id === beforeOrder[0],
    6000,
  );
  check(restored?.sections?.[0]?.id === beforeOrder[0], `keyboard ] did not restore order (${statuses(restored)})`);
  await waitUntil(
    `document.querySelector(".task-list .name")?.textContent?.includes(${JSON.stringify(firstTitle)})`,
    6000,
    "restored row on the board",
  );
  await press(page, "k");

  await press(page, "r");
  const review = await pollRun(
    baseURL,
    happyId,
    (run) => run.sections.some((s) => s.status === "checkpoint"),
    45000,
  );
  check(
    review?.sections?.[0]?.status === "checkpoint",
    `handoff section did not reach review (${statuses(review)})`,
  );
  // "Review chain" and the section body both contain those words while the
  // child is still running. Wait until the reading pane badge is Review,
  // which is when the client has the checkpoint version the approve key sends.
  const shown = await waitUntil(
    `document.querySelector(".task-read .badge")?.textContent?.trim() === "Review" && document.body.innerText.includes("Verification passed")`,
    15000,
    "checkpoint on the board",
  );
  if (shown) await shoot("task-review", ["desk1024", "desk1440"]);

  // Approve the checkpoint row. `a` follows the selected section, which the
  // reorder keys may have left on the other row.
  await page.evaluate(`(() => {
    const row = [...document.querySelectorAll(".task-row")].find((el) => (el.innerText || "").includes("Handoff step"));
    row?.click();
    document.querySelector("#task-runner")?.focus();
  })()`);
  await delay(150);
  await press(page, "a");
  const approved = await pollRun(
    baseURL,
    happyId,
    (run) => run.sections.some((s) => s.status === "approved"),
    8000,
  );
  check(approved?.sections?.[0]?.status === "approved", `approve did not stick (${statuses(approved)})`);
  await waitUntil(
    `document.querySelector(".task-read .badge")?.textContent?.trim() === "Approved"`,
    8000,
    "approved badge",
  );
  await press(page, "r");
  const second = await pollRun(
    baseURL,
    happyId,
    (run) => run.sections[1]?.status === "checkpoint" || run.sections[1]?.status === "failed",
    45000,
  );
  check(
    second?.sections?.[1]?.status === "checkpoint",
    `approved section did not release the next one (${statuses(second)})`,
  );

  await click(".task-plan", "Blocked chain");
  const blockedShown = await waitUntil(
    `document.body.innerText.includes("Failed") && document.body.innerText.includes("Blocked") && document.body.innerText.includes("Plain step")`,
    8000,
    "blocked chain on the board",
  );
  if (blockedShown) await shoot("task-blocked", ["desk1024", "desk1440", "mobile"]);
  await press(page, "r");
  await delay(800);
  const still = (await api(baseURL, "GET", `/v1/plans/${blockedId}/run`)).run;
  check(
    still.sections[0]?.status === "failed" && still.sections[1]?.status === "blocked",
    `Run spawned a dependant of a failed section (${statuses(still)})`,
  );

  await press(page, "t");
  const retried = await pollRun(
    baseURL,
    blockedId,
    (run) => run.sections[0]?.status === "pending" && run.sections[1]?.status === "pending",
    8000,
  );
  check(
    retried?.sections?.[0]?.status === "pending" && retried?.sections?.[1]?.status === "pending",
    `retry did not clear the block (${statuses(retried)})`,
  );
  await press(page, "s");
  const skipped = await pollRun(
    baseURL,
    blockedId,
    (run) => run.sections[0]?.status === "skipped",
    8000,
  );
  check(skipped?.sections?.[0]?.status === "skipped", `skip did not apply (${statuses(skipped)})`);
  await click(".segmented button", "Chat");
  log("task runner pass done");
}

// Per-stream scenario entry (scripts/webui-uat/scenarios.mjs loader):
// the cockpit pass runs first; this pass then exercises the task board.
export const scenario = (ctx) => runTaskRunnerPass(ctx);
