// Empty-folder cleanup (#1566): the session panel offers one bulk
// action while at least one folder holds no session. It counts only
// truly empty folders, asks once through the shared confirm, removes
// only the empty folders, and leaves non-empty folders and every
// session untouched. With nothing empty, no action is shown.

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

function emptyFolderIds(snap) {
  const filed = new Set(
    Object.values(snap.sessions || {})
      .map((org) => org.folderId || "")
      .filter((id) => id !== ""),
  );
  return (snap.folders || [])
    .map((f) => f.id)
    .filter((id) => id && !filed.has(id));
}

export const scenario = async (ctx) => {
  const { baseURL } = ctx;

  // Seed: one folder holding a real session, two folders holding none.
  const kept = await api(baseURL, "POST", "/v1/session-list/folders", {
    name: "Cleanup kept",
  });
  const emptyA = await api(baseURL, "POST", "/v1/session-list/folders", {
    name: "Cleanup empty A",
  });
  const emptyB = await api(baseURL, "POST", "/v1/session-list/folders", {
    name: "Cleanup empty B",
  });
  ctx.check(Boolean(kept?.id && emptyA?.id && emptyB?.id), "folder seeding failed");
  if (!kept?.id) return;

  const rowReady = await ctx.waitUntil(
    `document.querySelector(".ss-panel .ss-row[data-session-id]") != null`,
    10000,
    "session rows before filing",
  );
  ctx.check(Boolean(rowReady), "no session row to file into the kept folder");
  if (!rowReady) return;
  const sid = await ctx.page.evaluate(
    `document.querySelector(".ss-panel .ss-row[data-session-id]").getAttribute("data-session-id")`,
  );
  await api(baseURL, "POST", "/v1/session-list/bulk", {
    ids: [sid],
    op: "move",
    folderId: kept.id,
  });

  // The button counts every folder the snapshot shows as empty (the
  // two seeded here plus any earlier pass left behind), never the
  // kept one.
  const before = await api(baseURL, "GET", "/v1/session-list");
  const expected = emptyFolderIds(before).length;
  ctx.check(expected >= 2, `seeded empties missing from snapshot (${expected})`);
  const shown = await ctx.waitUntil(
    `(() => {
      const btn = document.querySelector("[data-action='cleanup-empty-folders']");
      return Boolean(btn) && btn.innerText.includes("(${expected})");
    })()`,
    10000,
    "cleanup action with the empty count",
  );
  ctx.check(Boolean(shown), `cleanup action did not show count ${expected}`);
  if (!shown) return;
  await ctx.shoot("empty-folders-before", ["desk1024", "desk1440", "mobile"]);

  // Keyboard path: Escape cancels the confirm and removes nothing.
  await ctx.page.evaluate(
    `document.querySelector("[data-action='cleanup-empty-folders']").click()`,
  );
  const asked = await ctx.waitUntil(
    `(() => {
      const title = document.querySelector("#confirm-title");
      const body = document.querySelector("#confirm-body");
      return Boolean(title) && title.textContent.includes("empty folders")
        && Boolean(body) && body.textContent.includes("Sessions are not affected");
    })()`,
    4000,
    "cleanup confirm",
  );
  ctx.check(Boolean(asked), "cleanup did not ask through the shared confirm");
  if (!asked) return;
  await ctx.shoot("empty-folders-confirm", ["desk1024", "desk1440", "mobile"]);
  await ctx.press("Escape");
  const cancelled = await ctx.waitUntil(
    `!document.querySelector("[data-confirm='dialog']")`,
    4000,
    "cleanup confirm cancelled",
  );
  ctx.check(Boolean(cancelled), "Escape did not cancel the cleanup confirm");
  const afterCancel = await api(baseURL, "GET", "/v1/session-list");
  ctx.check(
    (afterCancel.folders || []).some((f) => f.id === emptyA.id),
    "Escape removed a folder anyway",
  );

  // Accept: only the empty folders go; the kept folder and its
  // session stay, and the action disappears once nothing is empty.
  await ctx.page.evaluate(
    `document.querySelector("[data-action='cleanup-empty-folders']").click()`,
  );
  await ctx.waitUntil(
    `document.querySelector("[data-confirm='dialog']") != null`,
    4000,
    "cleanup confirm again",
  );
  await ctx.page.evaluate(`document.querySelector("[data-confirm='accept']").click()`);
  const swept = await ctx.waitUntil(
    `document.querySelector("[data-action='cleanup-empty-folders']") == null`,
    10000,
    "cleanup action gone after sweep",
  );
  ctx.check(Boolean(swept), "cleanup action still shown after every empty folder was removed");
  const after = await api(baseURL, "GET", "/v1/session-list");
  const afterIds = new Set((after.folders || []).map((f) => f.id));
  ctx.check(!afterIds.has(emptyA.id) && !afterIds.has(emptyB.id), "empty folders survived cleanup");
  ctx.check(afterIds.has(kept.id), "cleanup removed the non-empty folder");
  ctx.check(
    (after.sessions || {})[sid]?.folderId === kept.id,
    "cleanup unfiled or lost the kept folder's session",
  );
  ctx.check(emptyFolderIds(after).length === 0, "snapshot still reports empty folders");
  const panelKept = await ctx.waitUntil(
    `document.body && document.body.innerText.includes("Cleanup kept") && !document.body.innerText.includes("Cleanup empty")`,
    10000,
    "panel after cleanup",
  );
  ctx.check(Boolean(panelKept), "panel did not settle to the kept folder only");
  await ctx.shoot("empty-folders-after", ["desk1024", "desk1440", "mobile"]);
};
