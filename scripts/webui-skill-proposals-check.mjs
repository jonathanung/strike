// Browser check for skill proposals (#1611). Called from webui-smoke at the
// end of the Customize pass. It seeds three recorded root sessions in this
// workspace that open with the same request and run the same tools. Then it
// turns the opt-in on in Customize → Skills, clicks "Look now", expects one
// proposal card, accepts it, and expects the skill in the catalog. #1913:
// each evidence session row is a link that opens the session, and the
// draft is mirrored into the Ideas inbox and settled when accepted. #1958:
// a second seeded workflow is dismissed and stays gone after a rescan, the
// Skills tab carries a pending-count badge, and the accepted skill is
// invoked in a fresh session of a fresh daemon. Near-miss handling and
// model drafting are covered by Go tests.
import { spawn } from "node:child_process";
import { readFile, rm, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

export const PROPOSED_SKILL = "regenerate-smoke-fixtures";
const OPENING = "Regenerate the smoke fixtures, rerun the linter and summarize warnings";
// #1958: a second repeated workflow, dismissed in the browser.
const DISMISSED_SKILL = "rotate-staging-certificates";
const DISMISSED_OPENING = "Rotate the staging certificates then restart the ingress controller";
const INVOKE_MARKER = "smoke-invoke-1958";

const WORKFLOWS = [
  { tag: "PROPOSALSEED", opening: OPENING, tools: ["bash", "read"], title: "Fixture refresh" },
  { tag: "PROPOSALDISMISS", opening: DISMISSED_OPENING, tools: ["grep", "edit"], title: "Certificate rotation" },
];

export async function seedRepeatedWorkflow({ home, port, token }) {
  // The workspace key the scan reads for, exactly as Customize asks for it
  // (no ?root), so seeded sessions land in the scanned workspace.
  const res = await fetch(`http://127.0.0.1:${port}/v1/skill-proposals`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const projectKey = (await res.json()).workspace || "";
  if (!projectKey) throw new Error("skill proposals: GET /v1/skill-proposals returned no workspace");
  const sessions = join(home, ".strike", "sessions");
  const now = Date.now();
  for (const [w, wf] of WORKFLOWS.entries()) {
    for (let i = 0; i < 3; i++) {
      const id = `20260102T00000${i}.00000000${w}Z-${wf.tag}${i}`;
      const at = (s) => new Date(now - (3 - i) * 3600_000 - w * 60_000 + s * 1000).toISOString();
      const corr = { sessionId: id, turnId: "t1" };
      const lines = [
        { type: "session.header", schemaVersion: 1, time: at(0) },
        { type: "user.message", time: at(0), data: { ...corr, text: wf.opening } },
        ...wf.tools.map((name, j) => ({ type: "tool.begin", time: at(1 + j), data: { ...corr, callId: `c${j}`, name, args: {} } })),
        { type: "turn.completed", time: at(1 + wf.tools.length), data: corr },
      ];
      await writeFile(join(sessions, `${id}.jsonl`), lines.map((l) => JSON.stringify(l)).join("\n") + "\n");
      await writeFile(join(sessions, `${id}.meta.json`), JSON.stringify({ projectKey, title: `${wf.title} ${i + 1}` }));
    }
  }
}

async function evalValue(send, expression) {
  const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
  const details = ev.result?.exceptionDetails;
  if (details) {
    throw new Error(`eval failed: ${JSON.stringify(details).slice(0, 400)}`);
  }
  return ev.result?.result?.value;
}

async function waitFor(send, expression, label, tries = 80) {
  let last = "";
  for (let i = 0; i < tries; i++) {
    last = String((await evalValue(send, expression)) ?? "");
    if (last.startsWith("ok")) return last;
    await delay(250);
  }
  throw new Error(`skill proposals: ${label}: ${last.slice(0, 400)}`);
}

// Customize must be mounted; this opens the Skills section itself.
async function proposalIdea({ port, token }) {
  const res = await fetch(`http://127.0.0.1:${port}/v1/suggestions`, { headers: { Authorization: `Bearer ${token}` } });
  const body = await res.json();
  return (body.suggestions || []).find((s) => s.source === "skill-proposals" && String(s.title || "").includes(PROPOSED_SKILL));
}

// cardSel finds the proposal card whose text names skill.
const cardSel = (skill) =>
  `[...document.querySelectorAll('[data-cz-proposal]')].find((c) => c.innerText.includes(${JSON.stringify(skill)}))`;
const badgeExpr = `(document.querySelector('[data-cz-proposals-badge]')?.getAttribute('data-cz-proposals-badge') || "")`;

// sessionLog reads a session log: the joined assistant text, the raw
// lines, and a short summary for failure messages.
async function sessionLog(file) {
  let raw = "";
  try {
    raw = await readFile(file, "utf8");
  } catch (err) {
    return { reply: "", lines: [], summary: `no log (${err.code || err})` };
  }
  let reply = "";
  const types = {};
  const errors = [];
  const lines = raw.split("\n").filter((l) => l.trim());
  for (const line of lines) {
    try {
      const env = JSON.parse(line);
      types[env.type] = (types[env.type] || 0) + 1;
      if (env.type === "text.delta" && typeof env.data?.text === "string") reply += env.data.text;
      if (String(env.type).includes("error") || String(env.type).includes("notice")) errors.push(JSON.stringify(env.data || {}).slice(0, 200));
    } catch {
      // partial line while the daemon writes
    }
  }
  return { reply, lines, summary: `types ${JSON.stringify(types)} errors ${JSON.stringify(errors.slice(-3))}` };
}

// invokeInFreshSession (#1958) runs the accepted skill as a slash command in
// a new session of a freshly started daemon. The smoke workspace starts
// untrusted, and an untrusted workspace never loads repo skills, so trust is
// granted for this step and revoked after. The echo provider replies with
// the text it was sent, so the reply holds the rendered skill.
async function invokeInFreshSession({ home, port, token }) {
  const auth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const api = (p, base, init = {}) => fetch(`http://127.0.0.1:${base}${p}`, { headers: auth, ...init });
  const trust = await (await api("/v1/workspace/trust", port)).json();
  let granted = false;
  if (!trust.trusted) {
    const grant = await api("/v1/workspace/trust", port, { method: "POST", body: JSON.stringify({ action: "grant" }) });
    if (!grant.ok) throw new Error(`skill proposals: trust grant ${grant.status} ${await grant.text()}`);
    granted = true;
  }
  const freshPort = port + 2000;
  let log = "";
  const fresh = spawn(
    process.env.STRIKE_BIN || "./strike",
    ["serve", "--addr", `127.0.0.1:${freshPort}`, "--provider", "echo", "--auth", "--token", token],
    { env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] },
  );
  fresh.stdout.on("data", (b) => (log += b.toString()));
  fresh.stderr.on("data", (b) => (log += b.toString()));
  let root = "";
  try {
    let healthy = false;
    for (let i = 0; i < 100 && !healthy; i++) {
      try {
        healthy = (await fetch(`http://127.0.0.1:${freshPort}/health`)).ok;
      } catch {
        // still binding
      }
      if (!healthy) await delay(100);
    }
    if (!healthy) throw new Error(`skill proposals: fresh daemon did not start: ${log.slice(-800)}`);
    const created = await api("/v1/roots", freshPort, { method: "POST", body: "{}" });
    if (!created.ok) throw new Error(`skill proposals: fresh session ${created.status} ${await created.text()}`);
    root = (await created.json()).id || "";
    if (!root) throw new Error("skill proposals: fresh session returned no id");
    const sent = await api(`/v1/ops?root=${encodeURIComponent(root)}`, freshPort, {
      method: "POST",
      body: JSON.stringify({ type: "user.input", data: { text: `/${PROPOSED_SKILL} ${INVOKE_MARKER}` } }),
    });
    if (!sent.ok) throw new Error(`skill proposals: invoke ${sent.status} ${await sent.text()}`);
    const file = join(home, ".strike", "sessions", `${root}.jsonl`);
    // The rendered skill reaches the model as the turn text; echo replies
    // with it. Either copy proves the slash command expanded the skill.
    const rendered = (t) => t.includes(INVOKE_MARKER) && t.includes("Use the `bash` tool.");
    let got = { reply: "", lines: [], summary: "" };
    for (let i = 0; i < 120; i++) {
      got = await sessionLog(file);
      if (rendered(got.reply) || got.lines.some((l) => l.includes('"user.message"') && rendered(l))) return;
      await delay(250);
    }
    const boot = await (await api("/v1/bootstrap", freshPort)).json().catch(() => ({}));
    const freshTrust = await (await api("/v1/workspace/trust", freshPort)).json().catch(() => ({}));
    throw new Error(
      `skill proposals: the accepted skill did not run in a fresh session: reply ${JSON.stringify(got.reply.slice(0, 300))} ${got.summary}` +
        ` trusted ${freshTrust.trusted} skills ${JSON.stringify((boot.skills || []).map((k) => k.name)).slice(0, 300)} log ${log.slice(0, 1200)}`,
    );
  } finally {
    if (root) await api(`/v1/roots/${encodeURIComponent(root)}`, freshPort, { method: "DELETE" }).catch(() => {});
    const exited = new Promise((resolve) => fresh.once("exit", resolve));
    fresh.kill("SIGTERM");
    await Promise.race([exited, delay(4000)]);
    if (fresh.exitCode == null && !fresh.signalCode) fresh.kill("SIGKILL");
    if (granted) await api("/v1/workspace/trust", port, { method: "POST", body: JSON.stringify({ action: "revoke" }) });
  }
}

export async function skillProposalsCheck({ send, repoDir, port, token, home }) {
  await evalValue(send, `document.querySelector('[data-cz-section="skills"]')?.click()`);
  await waitFor(
    send,
    `document.querySelector('[data-cz-proposals-toggle="false"]') ? "ok" : "toggle " + (document.querySelector('[data-cz-proposals-toggle]')?.getAttribute("data-cz-proposals-toggle") || "missing")`,
    "opt-in starts off",
  );
  await waitFor(
    send,
    `(() => {
      const m = document.querySelector('[data-cz-proposals-model]');
      return m && m.getAttribute('data-cz-proposals-model') === "false" ? "ok" : "model toggle " + (m ? m.outerHTML.slice(0, 200) : "missing");
    })()`,
    "model drafting starts off",
  );
  await evalValue(send, `document.querySelector('[data-cz-proposals-toggle]').click()`);
  await waitFor(
    send,
    `(() => {
      const scan = document.querySelector('[data-cz-proposals-scan]');
      return document.querySelector('[data-cz-proposals-toggle="true"]') && scan && !scan.disabled ? "ok" : "not enabled";
    })()`,
    "opt-in turns on",
  );
  await evalValue(send, `document.querySelector('[data-cz-proposals-scan]').click()`);
  await waitFor(
    send,
    `(() => {
      const cards = [...document.querySelectorAll('[data-cz-proposal]')];
      const text = cards.map((c) => c.innerText).join(" | ");
      return cards.length === 2 && text.includes(${JSON.stringify(PROPOSED_SKILL)}) && text.includes(${JSON.stringify(DISMISSED_SKILL)}) && text.includes("From 3 sessions")
        ? "ok"
        : "cards " + JSON.stringify(text.slice(0, 300)) + " note " + (document.querySelector('[data-cz-proposals-note]')?.innerText || "");
    })()`,
    "scan proposes both seeded workflows",
  );
  await waitFor(send, `${badgeExpr} === "2" ? "ok" : "badge " + ${badgeExpr}`, "Skills tab badge counts 2 proposals");
  await waitFor(
    send,
    `(() => {
      const links = [...(${cardSel(PROPOSED_SKILL)})?.querySelectorAll('[data-cz-proposal-session]') || []];
      return links.length === 3 && links.every((b) => b.tagName === "BUTTON" && b.getAttribute("data-cz-proposal-session"))
        ? "ok"
        : "evidence links " + links.length;
    })()`,
    "evidence sessions are links",
  );
  if (port && token) {
    const mirrored = await proposalIdea({ port, token });
    if (!mirrored || mirrored.state !== "open" || (mirrored.evidence || []).length !== 3) {
      throw new Error(`skill proposals: Ideas inbox mirror = ${JSON.stringify(mirrored || null).slice(0, 400)}`);
    }
  }

  // #1958 dismiss path: the dismissed card goes, and a rescan does not
  // bring it back.
  await waitFor(
    send,
    `(() => { const b = (${cardSel(DISMISSED_SKILL)})?.querySelector('[data-cz-proposal-dismiss]'); return b && !b.disabled ? "ok" : "dismiss " + (b ? "disabled" : "missing"); })()`,
    "Dismiss is ready",
  );
  await evalValue(send, `(${cardSel(DISMISSED_SKILL)}).querySelector('[data-cz-proposal-dismiss]').click()`);
  await waitFor(
    send,
    `(() => {
      const gone = !(${cardSel(DISMISSED_SKILL)});
      const kept = Boolean(${cardSel(PROPOSED_SKILL)});
      return gone && kept && ${badgeExpr} === "1" ? "ok" : "gone " + gone + " kept " + kept + " badge " + ${badgeExpr};
    })()`,
    "dismissed card leaves and the badge drops to 1",
  );
  await waitFor(
    send,
    `(() => { const b = document.querySelector('[data-cz-proposals-scan]'); return b && !b.disabled ? "ok" : "scan busy"; })()`,
    "Look now is ready after dismiss",
  );
  await evalValue(send, `document.querySelector('[data-cz-proposals-scan]').click()`);
  await waitFor(
    send,
    `(() => {
      // The click disables the buttons until the scan reply lands; dismiss
      // already left "1 proposal", so wait for the scan itself to finish.
      const scan = document.querySelector('[data-cz-proposals-scan]');
      const status = document.querySelector('[data-cz-proposals-status]')?.innerText || "";
      const cards = [...document.querySelectorAll('[data-cz-proposal]')];
      const text = cards.map((c) => c.innerText).join(" | ");
      return scan && !scan.disabled && status.includes("1 proposal") && cards.length === 1 && text.includes(${JSON.stringify(PROPOSED_SKILL)}) && !text.includes(${JSON.stringify(DISMISSED_SKILL)})
        ? "ok"
        : "status " + JSON.stringify(status) + " cards " + JSON.stringify(text.slice(0, 300));
    })()`,
    "rescan does not bring the dismissed proposal back",
  );

  await waitFor(
    send,
    `(() => { const b = (${cardSel(PROPOSED_SKILL)})?.querySelector('[data-cz-proposal-accept]'); return b && !b.disabled ? "ok" : "accept " + (b ? "disabled" : "missing"); })()`,
    "Accept is ready",
  );
  await evalValue(send, `(${cardSel(PROPOSED_SKILL)}).querySelector('[data-cz-proposal-accept]').click()`);
  try {
    await waitFor(
      send,
      `(() => {
        const listed = document.querySelector('[data-cz-item=${JSON.stringify(PROPOSED_SKILL)}]');
        const left = document.querySelectorAll('[data-cz-proposal]').length;
        const badge = ${badgeExpr};
        return listed && left === 0 && badge === "" ? "ok" : "listed " + Boolean(listed) + " cards " + left + " badge " + badge + " note " + (document.querySelector('[data-cz-proposals-note]')?.innerText || "");
      })()`,
      "accepted skill is listed, the card and the badge are gone",
    );
    if (port && token) {
      const settled = await proposalIdea({ port, token });
      if (!settled || settled.state !== "accepted") {
        throw new Error(`skill proposals: Ideas row after accept = ${JSON.stringify(settled || null).slice(0, 400)}`);
      }
      if (home) await invokeInFreshSession({ home, port, token });
    }
  } finally {
    await rm(join(repoDir, ".strike", "skills", `${PROPOSED_SKILL}.md`), { force: true });
    await rm(join(repoDir, ".strike", "skills", PROPOSED_SKILL), { recursive: true, force: true });
  }
}
