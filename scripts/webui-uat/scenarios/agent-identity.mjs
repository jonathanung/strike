// Per-agent identity (#1379). Shims GET /v1/team with three running
// children whose aliases (the identity key) hash to distinct tones, and asserts:
//   - each child row has a decorative (aria-hidden) initial disc;
//   - the three discs differ in tone and initial;
//   - every row still has its status dot, all reading "running";
//   - the lead is not rendered as a row (keeps the plain presentation).

const TEAM = {
  available: true,
  leadId: "lead-root",
  members: {
    "lead-root": { sessionId: "lead-root", name: "lead", state: "working", role: "lead" },
    "child-a": { sessionId: "child-a", name: "alpha", state: "working" },
    "child-b": { sessionId: "child-b", name: "bravo", state: "working" },
    "child-c": { sessionId: "child-c", name: "charlie", state: "working" },
  },
};

const SHIM = `(() => {
  if (!window.__identityOrigFetch) window.__identityOrigFetch = window.fetch.bind(window);
  window.fetch = async function (input, init) {
    const url = typeof input === "string" ? input : (input && input.url) || "";
    const method = ((init && init.method) || (typeof input !== "string" && input && input.method) || "GET").toUpperCase();
    if (method === "GET" && url.includes("/v1/team?")) {
      return new Response(${JSON.stringify(JSON.stringify(TEAM))}, {
        status: 200,
        headers: { "content-type": "application/json" },
      });
    }
    return window.__identityOrigFetch(input, init);
  };
  return true;
})()`;

export async function scenario(ctx) {
  const { page, waitUntil, check, click, shoot, log } = ctx;

  await page.evaluate(`document.querySelector('[data-action="onboarding-continue"]')?.click()`);
  await page.evaluate(SHIM);
  await click(".side-tabs button", "Activity");
  const ready = await waitUntil(
    `["alpha", "bravo", "charlie"].every((n) => [...document.querySelectorAll(".activity-row")].some((r) => r.innerText.includes(n)))`,
    10000,
    "three shimmed child rows",
  );
  check(Boolean(ready), "shimmed child rows did not render");
  if (!ready) return;

  const discs = await page.evaluate(`["alpha", "bravo", "charlie"].map((n) => {
    const row = [...document.querySelectorAll(".activity-row")].find((r) => r.innerText.includes(n));
    const d = row && row.querySelector(".task-line .identity-disc");
    const dot = row && row.querySelector(".task-line [data-slot='dot']");
    return {
      name: n,
      text: d ? d.textContent : "",
      tone: d ? d.getAttribute("data-tone") : "",
      hidden: d ? d.getAttribute("aria-hidden") : "",
      status: dot ? dot.getAttribute("data-status") : "",
    };
  })`);
  const tones = new Set(discs.map((d) => d.tone));
  const letters = new Set(discs.map((d) => d.text));
  check(discs.every((d) => d.text && d.hidden === "true"), `disc missing or not decorative: ${JSON.stringify(discs)}`);
  check(tones.size === 3, `three children should get three tones: ${JSON.stringify(discs)}`);
  check(letters.size === 3, `three children should get three initials: ${JSON.stringify(discs)}`);
  check(discs.every((d) => d.status === "running"), `status dots changed: ${JSON.stringify(discs)}`);
  check(
    Boolean(await page.evaluate(`![...document.querySelectorAll(".activity-row")].some((r) => /\\blead\\b/.test(r.querySelector(".task-line .name")?.textContent || ""))`)),
    "lead rendered as an activity row",
  );
  await shoot("agent-identity", ["desk1024", "desk1440", "mobile"]);
  log("agent identity scenario done");
}
