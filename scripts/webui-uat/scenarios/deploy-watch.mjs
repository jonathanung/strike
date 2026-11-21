// Deploy watch verdict card (#1785). Real daemon, real probes: each watch
// is created over REST on the active session and its HTTP check targets the
// running serve itself (/health is healthy, an unknown path fails, port 1 is
// unreachable). Shots cover watching, healthy (after the 60 s window),
// regression (both handoffs offered) and inconclusive at desk1024, desk1440
// and phone width. Run: node scripts/webui-uat.mjs --suite deploy-watch.

async function api(ctx, method, path, body) {
  const res = await fetch(`${ctx.baseURL}${path}`, {
    method,
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const text = await res.text();
  let json = null;
  try {
    json = JSON.parse(text);
  } catch {
    // keep text for the failure message
  }
  return { ok: res.ok, status: res.status, json, text };
}

async function watchOn(ctx, root, name, url, expectStatus, windowSeconds = 600) {
  const created = await api(ctx, "POST", "/v1/deploy-watches", {
    repo: "acme/app",
    sha: "0123456789abcdef0123456789abcdef01234567",
    pr: 42,
    environment: "uat",
    sessionId: root,
    failureThreshold: 1,
    windowSeconds,
    intervalSeconds: 10,
    checks: [{ kind: "http", name, url, expectStatus }],
  });
  ctx.check(created.ok, `create ${name} watch HTTP ${created.status} ${created.text.slice(0, 300)}`);
  return created.json?.id || "";
}

async function cardSays(ctx, verdict, label) {
  return ctx.waitUntil(
    `(() => {
      const card = document.querySelector(".deploy-watch-card");
      return Boolean(card && card.dataset.verdict === ${JSON.stringify(verdict)});
    })()`,
    25000,
    label,
  );
}

export const scenario = async (ctx) => {
  const roots = await fetch(`${ctx.baseURL}/v1/roots`).then((r) => r.json());
  const root = roots.activeId || roots.active_id || roots.roots?.[0]?.id;
  ctx.check(Boolean(root), "no live root for the watch session");
  if (!root) return;

  // Watching, then healthy: the serve's own /health over a 60 s window.
  const healthyID = await watchOn(ctx, root, "api", `${ctx.baseURL}/health`, 200, 60);
  if (healthyID) {
    await api(ctx, "POST", `/v1/deploy-watches/${healthyID}/poll`);
    const watching = await cardSays(ctx, "watching", "watching card");
    ctx.check(Boolean(watching), "watching card did not render for the watch session");
    if (watching) {
      const text = await ctx.bodyText();
      ctx.check(text.includes("acme/app @ 0123456789ab"), "card is missing the watched target");
      ctx.check(text.includes("consecutive failures"), "watching card does not explain the failure threshold");
      await ctx.shoot("deploy-watch-watching", ["desk1024", "desk1440"]);
    }
    await new Promise((resolve) => setTimeout(resolve, 61000));
    const settled = await api(ctx, "POST", `/v1/deploy-watches/${healthyID}/poll`);
    ctx.check(settled.json?.verdict === "healthy", `healthy poll verdict ${settled.json?.verdict}`);
    const healthy = await cardSays(ctx, "healthy", "healthy card");
    ctx.check(Boolean(healthy), "healthy card did not render");
    if (healthy) {
      const buttons = await ctx.page.evaluate(`document.querySelectorAll(".deploy-watch-card [data-dw-action]").length`);
      ctx.check(Number(buttons) === 0, `healthy card offers ${buttons} handoffs`);
      await ctx.shoot("deploy-watch-healthy", ["desk1024", "desk1440", "mobile"]);
    }
  }

  const inconclusiveID = await watchOn(ctx, root, "api", "http://127.0.0.1:1/health", 200);
  if (inconclusiveID) {
    await api(ctx, "POST", `/v1/deploy-watches/${inconclusiveID}/poll`);
    await api(ctx, "POST", `/v1/deploy-watches/${inconclusiveID}/cancel`);
    const inconclusive = await cardSays(ctx, "inconclusive", "inconclusive card");
    ctx.check(Boolean(inconclusive), "inconclusive card did not render");
    if (inconclusive) {
      const text = await ctx.bodyText();
      ctx.check(/unreachable/i.test(text), "inconclusive card does not show the unreachable evidence");
      const buttons = await ctx.page.evaluate(`document.querySelectorAll(".deploy-watch-card [data-dw-action]").length`);
      ctx.check(Number(buttons) === 0, `inconclusive card offers ${buttons} handoffs`);
      await ctx.shoot("deploy-watch-inconclusive", ["desk1024", "desk1440", "mobile"]);
    }
  }

  const regressionID = await watchOn(ctx, root, "api", `${ctx.baseURL}/v1/deploy-watch-uat-missing`, 200);
  if (regressionID) {
    const polled = await api(ctx, "POST", `/v1/deploy-watches/${regressionID}/poll`);
    ctx.check(polled.json?.verdict === "regression", `regression poll verdict ${polled.json?.verdict}`);
    const regression = await cardSays(ctx, "regression", "regression card");
    ctx.check(Boolean(regression), "regression card did not render");
    if (regression) {
      const actions = await ctx.page.evaluate(`[...document.querySelectorAll(".deploy-watch-card [data-dw-action]")].map((b) => b.dataset.dwAction).join(",")`);
      ctx.check(String(actions) === "fix,revert", `regression handoffs = ${actions}`);
      await ctx.shoot("deploy-watch-regression", ["desk1024", "desk1440", "mobile"]);
      // Unconfirmed handoffs are refused by the daemon.
      const refused = await api(ctx, "POST", `/v1/deploy-watches/${regressionID}/fix-session`, {});
      ctx.check(refused.status === 400, `unconfirmed fix-session HTTP ${refused.status}`);
      // The button asks first; cancelling the dialog starts nothing.
      await ctx.page.evaluate(`document.querySelector(".deploy-watch-card [data-dw-action='fix']")?.click()`);
      const asked = await ctx.waitUntil(
        `document.body && document.body.innerText.includes("Open fix session?")`,
        4000,
        "fix session confirm",
      );
      ctx.check(Boolean(asked), "Open fix session did not ask for confirmation");
      await ctx.press("Escape");
      const after = await api(ctx, "GET", `/v1/deploy-watches/${regressionID}`);
      ctx.check(!after.json?.fixSession, "cancelled confirmation still started a fix session");
    }
  }
  await ctx.viewport(390, 844);
  await ctx.page.evaluate(`document.querySelector(".deploy-watch-card")?.scrollIntoView({block:"center"})`);
  await ctx.phone("deploy-watch-card");
};
