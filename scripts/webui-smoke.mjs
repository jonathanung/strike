// Headless check that the embedded Rust cockpit boots and that --auth rejects
// a missing or wrong bearer. Chrome is required (google-chrome or chromium).
//
// Waits in this file are condition-based, never blind sleeps: waitFor()
// polls a probe until the page reaches the wanted state or a CI-realistic
// deadline passes. A timeout is a failure, and it dumps diagnostics (page
// snapshot, console errors, failed requests) so the CI log explains itself.
// The goals wait below is deliberately strict: it requires the real
// [data-view="goals"] nav button in the DOM. When #1296 shipped a stale
// committed bundle whose wasm had no Goals pane at all, this check caught
// it (CI run 40403843230) — a text scrape or a longer sleep would have
// papered over a real bug, and the old 400-char text slice could not say
// what the page actually looked like.
import { execFileSync, spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { agentPickerCheck } from "./webui-agent-picker-check.mjs";
import { createServer } from "node:http";
import { editorBrowserCheck } from "./webui-editor-check.mjs";
import { seedRepeatedWorkflow, skillProposalsCheck } from "./webui-skill-proposals-check.mjs";
import {
  BROKEN_SKILL,
  BROKEN_SKILL_BODY,
  loadWarningCustomizeCheck,
  loadWarningNoticeCheck,
} from "./webui-load-warnings-check.mjs";
import { mdReviewBrowserCheck } from "./webui-md-review-check.mjs";
import { seedSkillUsage, skillUsageCheck } from "./webui-skill-usage-check.mjs";
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const token = "webui-smoke-token";
const port = 18087 + (process.pid % 1000);
const home = await mkdtemp(join(tmpdir(), "strike-webui-smoke-"));
const chromeData = await mkdtemp(join(tmpdir(), "strike-webui-chrome-"));
const chromePort = 9222 + (process.pid % 200);

// Bash is Ask by default. This VM and CI have no bwrap, so a `run sleep`
// ends immediately unless degrade is on before the daemon reads config.
await mkdir(join(home, ".strike"), { recursive: true });
await writeFile(join(home, ".strike", "config"), '{"sandboxAllowDegrade":true}\n');

// Plugin lifecycle (#1570): one installed, passive-only global plugin.
// The cockpit lists it from GET /v1/plugins and remove needs a confirm step.
const smokePluginId = "smoke.lifecycle";
await mkdir(join(home, ".strike", "plugins", smokePluginId, "themes"), { recursive: true });
await writeFile(
  join(home, ".strike", "plugins", smokePluginId, "plugin.json"),
  JSON.stringify({
    schemaVersion: 1,
    id: smokePluginId,
    version: "1.0.0",
    name: "Smoke Lifecycle",
    strike: { min: "0.1.0" },
    contributions: { themes: [{ path: "themes/smoke.json" }] },
  }),
);
await writeFile(
  join(home, ".strike", "plugins", smokePluginId, "themes", "smoke.json"),
  JSON.stringify({ name: "Smoke Plugin Theme", id: "smoke-plugin-theme", colors: { accent: "#123456" } }),
);

// Theme catalog (#1529): a user theme the daemon's GET /v1/themes lists.
// The cockpit picker must offer it, paint it, and keep it across reload.
const smokeTheme = {
  id: "smoke-sunset",
  name: "Smoke Sunset",
  accent: { dark: "#fb923c", light: "#c2410c" },
  background: { dark: "#1b1020", light: "#fff8f0" },
  text: { dark: "#ffe8d0", light: "#2a1a10" },
};
await mkdir(join(home, ".strike", "themes"), { recursive: true });
await writeFile(
  join(home, ".strike", "themes", `${smokeTheme.id}.json`),
  JSON.stringify({
    name: smokeTheme.name,
    id: smokeTheme.id,
    colors: { accent: smokeTheme.accent, background: smokeTheme.background, text: smokeTheme.text },
  }),
);

// Decision ledger seed (#1485). There is no ledger write route, and the
// store loads its file once at open, so the file must exist before the
// daemon starts. Path: ~/.strike/ledger/<sha256(project key)>.json, where
// the key is the canonical git root of the daemon's cwd (project.Resolve).
let ledgerProjectKey = await realpath(process.cwd());
try {
  const top = execFileSync("git", ["-C", ledgerProjectKey, "rev-parse", "--show-toplevel"], {
    encoding: "utf8",
  }).replace(/\r?\n$/, "");
  ledgerProjectKey = await realpath(top);
} catch {
  // Not a git work tree: the key stays the canonical cwd.
}
{
  const now = "2026-11-01T10:00:00Z";
  const later = "2026-11-02T10:00:00Z";
  const entry = (e) => ({
    confidence: "medium",
    author_session: "smoke-session",
    author_agent: "build",
    author_root: "smoke-session",
    created_at: now,
    updated_at: now,
    ...e,
  });
  const doc = {
    version: 1,
    entries: [
      entry({
        id: "led_smoke_old",
        kind: "decision",
        statement: "Smoke config is plain JSON",
        status: "superseded",
        superseded_by: "led_smoke_new",
        updated_at: later,
      }),
      entry({
        id: "led_smoke_new",
        kind: "decision",
        statement: "Smoke config is JSONC",
        confidence: "high",
        status: "active",
        supersedes: "led_smoke_old",
        evidence_refs: ["docs/config.md"],
        scope_paths: ["internal/product/config"],
        created_at: later,
        updated_at: later,
      }),
      entry({
        id: "led_smoke_net",
        kind: "assumption",
        statement: "Smoke runs without network",
        status: "invalidated",
        invalidate_reason: "CI runners have network",
        invalidated_at: later,
        updated_at: later,
      }),
      entry({
        id: "led_smoke_fixture",
        kind: "constraint",
        statement: "Smoke fixtures never hold real credentials",
        status: "active",
      }),
    ],
  };
  const ledgerDir = join(home, ".strike", "ledger");
  await mkdir(ledgerDir, { recursive: true, mode: 0o700 });
  const ledgerFile = createHash("sha256").update(ledgerProjectKey).digest("hex") + ".json";
  await writeFile(join(ledgerDir, ledgerFile), JSON.stringify(doc), { mode: 0o600 });
}

// Git diff expand seed (#1428): an untracked file in the daemon's repo whose
// --no-index diff runs past the Git panel's 400-line render cap. The git
// section below opens it, asserts the capped render + "Show all N lines"
// footer, expands it, and asserts the tail is reachable. Removed in finally.
const gitDiffSeedPath = join(process.cwd(), "smoke-git-diff-1428.txt");
await writeFile(
  gitDiffSeedPath,
  Array.from({ length: 500 }, (_, i) => `smoke-diff-line-${i + 1}`).join("\n") + "\n",
);

// Render/boot waits get a deadline sized for shared CI runners; shorter
// interactive steps get a smaller one. Both are overridable for local runs.
// WAIT_MS is also the budget for anti-hang arrival waits whose predicate
// depends on a daemon round trip (op POST, engine state change, live
// event, re-render) rather than a local DOM reaction: those assert that
// the state arrives, never how fast, so a STEP_MS budget only measures
// host load. See the "turn stopped" wait below (#1713) and #1650, which
// adopted this same 30s convention for the engine's anti-hang waits.
const WAIT_MS = Number(process.env.STRIKE_SMOKE_WAIT_MS || 30000);
const STEP_MS = Number(process.env.STRIKE_SMOKE_STEP_MS || 15000);

// Set once the DevTools socket is up; the top-level catch uses it to append
// page diagnostics to failures that did not already carry them.
let collectDiagnostics = null;

function chromeBin() {
  const fromEnv = process.env.CHROME_BIN;
  if (fromEnv) return fromEnv;
  return "google-chrome";
}

async function waitHealth(url) {
  for (let i = 0; i < 50; i++) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      // daemon still binding
    }
    await delay(100);
  }
  throw new Error(`daemon did not answer ${url}`);
}

// SIGTERM a spawned child and wait for it to actually exit, escalating
// to SIGKILL after a grace period (same pattern as the daemon restart
// below). Cleanup must not rm a child's files while it is still running:
// Chrome keeps writing its profile for well over a fixed sleep, and the
// rm then races it and fails ENOTEMPTY (#1477).
async function stopChild(child) {
  if (!child || child.exitCode != null || child.signalCode) return;
  const exited = new Promise((resolve) => child.once("exit", resolve));
  child.kill("SIGTERM");
  await Promise.race([exited, delay(5000)]);
  if (child.exitCode == null && !child.signalCode) {
    child.kill("SIGKILL");
    await Promise.race([exited, delay(2000)]);
  }
}

async function status(path, headers) {
  const res = await fetch(`http://127.0.0.1:${port}${path}`, { headers });
  return res.status;
}

let strike = spawn(
  process.env.STRIKE_BIN || "./strike",
  [
    "serve",
    "--addr",
    `127.0.0.1:${port}`,
    "--provider",
    "echo",
    "--auth",
    "--token",
    token,
  ],
  { env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] },
);
let strikeLog = "";
strike.stdout.on("data", (b) => {
  strikeLog += b.toString();
});
strike.stderr.on("data", (b) => {
  strikeLog += b.toString();
});

let chrome;
try {
  await waitHealth(`http://127.0.0.1:${port}/health`);
  const missing = await status("/v1/workspaces");
  const wrong = await status("/v1/workspaces", { Authorization: "Bearer wrong" });
  const right = await status("/v1/workspaces", { Authorization: `Bearer ${token}` });
  const schedules = await status("/v1/schedules", { Authorization: `Bearer ${token}` });
  if (missing !== 401 || wrong !== 401 || right !== 200 || schedules !== 200) {
    throw new Error(`auth statuses missing=${missing} wrong=${wrong} right=${right} schedules=${schedules}`);
  }
  const termMissing = await status("/v1/terminals");
  const termWrong = await status("/v1/terminals", { Authorization: "Bearer wrong" });
  const termRight = await status("/v1/terminals", { Authorization: `Bearer ${token}` });
  const termQuery = await status(`/v1/terminals?token=${token}`);
  if (termMissing !== 401 || termWrong !== 401 || termRight !== 200 || termQuery !== 401) {
    throw new Error(
      `terminal auth statuses missing=${termMissing} wrong=${termWrong} right=${termRight} query=${termQuery}`,
    );
  }
  // #1392: notification read state is daemon-side. Two clients (browser A
  // posting a read, browser B polling) must agree, and the route needs auth.
  {
    const seenUrl = `http://127.0.0.1:${port}/v1/notifications/seen`;
    const seenNoAuth = await fetch(seenUrl, { method: "POST", body: '{"upTo":1}' });
    if (seenNoAuth.status !== 401) {
      throw new Error(`notifications/seen without token = ${seenNoAuth.status}`);
    }
    const before = await (
      await fetch(`http://127.0.0.1:${port}/v1/notifications`, {
        headers: { Authorization: `Bearer ${token}` },
      })
    ).json();
    const latest = Number(before.latest || 0);
    const marked = await fetch(seenUrl, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ upTo: Math.max(latest, 1) }),
    });
    if (marked.status !== 200) {
      throw new Error(`notifications/seen mark-all = ${marked.status} ${await marked.text()}`);
    }
    const other = await (
      await fetch(`http://127.0.0.1:${port}/v1/notifications`, {
        headers: { Authorization: `Bearer ${token}` },
      })
    ).json();
    if (!other.seen || other.seen.upTo !== latest || !Array.isArray(other.seen.ids)) {
      throw new Error(`second client read state = ${JSON.stringify(other.seen)} latest=${latest}`);
    }
  }
  // #1488: the away-briefing watermark is daemon-side (shared by every
  // device), authed, forward-only, and clamped to the daemon clock. It is
  // left at daemon now so the cockpit below opens with no briefing.
  {
    const briefUrl = `http://127.0.0.1:${port}/v1/briefing/seen`;
    const auth = { Authorization: `Bearer ${token}` };
    const json = { ...auth, "Content-Type": "application/json" };
    const briefNoAuth = await fetch(briefUrl);
    if (briefNoAuth.status !== 401) {
      throw new Error(`briefing/seen without token = ${briefNoAuth.status}`);
    }
    const fresh = await (await fetch(briefUrl, { headers: auth })).json();
    if (fresh.seenAt !== 0 || !(fresh.now > 0)) {
      throw new Error(`fresh briefing watermark = ${JSON.stringify(fresh)}`);
    }
    const mark = fresh.now - 60 * 60 * 1000;
    const post = async (seenAt) => {
      const res = await fetch(briefUrl, { method: "POST", headers: json, body: JSON.stringify({ seenAt }) });
      if (res.status !== 200) {
        throw new Error(`briefing/seen POST ${seenAt} = ${res.status} ${await res.text()}`);
      }
      return res.json();
    };
    const first = await post(mark);
    const back = await post(mark - 1000);
    const peer = await (await fetch(briefUrl, { headers: auth })).json();
    if (first.seenAt !== mark || back.seenAt !== mark || peer.seenAt !== mark) {
      throw new Error(`briefing watermark first=${first.seenAt} back=${back.seenAt} peer=${peer.seenAt} want=${mark}`);
    }
    const ahead = await post(Date.now() + 24 * 60 * 60 * 1000);
    if (!(ahead.seenAt > mark) || ahead.seenAt > ahead.now) {
      throw new Error(`briefing watermark not clamped to daemon now: ${JSON.stringify(ahead)}`);
    }
    const bad = await fetch(briefUrl, { method: "POST", headers: json, body: '{"seenAt":0}' });
    if (bad.status !== 400) {
      throw new Error(`briefing/seen zero = ${bad.status}`);
    }
  }
  // #1564: Web Push registry. No real push service is contacted: the https
  // endpoint below is never reachable and is removed straight away.
  {
    const authed = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
    const base = `http://127.0.0.1:${port}/v1/notifications`;
    const keyRes = await fetch(`${base}/push-key`, { headers: authed });
    const key = keyRes.status === 200 ? (await keyRes.json()).publicKey : "";
    if (!/^[A-Za-z0-9_-]{86,88}$/.test(key || "")) throw new Error(`push-key ${keyRes.status} ${key}`);
    const noAuth = await fetch(`${base}/push-key`);
    if (noAuth.status !== 401) throw new Error(`push-key without token = ${noAuth.status}`);
    // A real browser key pair is not needed for registry checks; reuse the
    // daemon's own public key as a valid P-256 point.
    const sub = (endpoint) => JSON.stringify({ endpoint, expirationTime: null, keys: { p256dh: key, auth: "BTBZMqHH6r4Tts7J_aSIgg" } });
    const plain = await fetch(`${base}/push-subscriptions`, { method: "POST", headers: authed, body: sub("http://push.invalid/x") });
    if (plain.status !== 400) throw new Error(`plain-http push endpoint accepted: ${plain.status}`);
    const added = await fetch(`${base}/push-subscriptions`, { method: "POST", headers: authed, body: sub("https://push.invalid/smoke") });
    if (added.status !== 201) throw new Error(`push subscribe ${added.status} ${await added.text()}`);
    const status = await (await fetch(base, { headers: authed })).json();
    if (!status.push || status.push.subscriptions !== 1 || JSON.stringify(status).includes("push.invalid")) {
      throw new Error(`push status ${JSON.stringify(status.push)}`);
    }
    const removed = await fetch(`${base}/push-subscriptions`, { method: "DELETE", headers: authed, body: JSON.stringify({ endpoint: "https://push.invalid/smoke" }) });
    if (removed.status !== 204) throw new Error(`push unsubscribe ${removed.status}`);
  }
  const healthBody = await (await fetch(`http://127.0.0.1:${port}/health`)).json();
  if (!healthBody.ok || !(healthBody.now > 0)) {
    throw new Error(`health missing now: ${JSON.stringify(healthBody)}`);
  }
  const boardRes = await fetch(`http://127.0.0.1:${port}/v1/status/sessions`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (boardRes.status !== 200) {
    throw new Error(`status board HTTP ${boardRes.status}`);
  }
  const board = await boardRes.json();
  if (!Array.isArray(board.sessions) || board.heartbeatStaleMs !== 6000 || board.heartbeatIntervalMs !== 2000) {
    throw new Error(`status board shape ${JSON.stringify(board).slice(0, 400)}`);
  }
  const seenRes = await fetch(`http://127.0.0.1:${port}/v1/sessions/not-a-session/seen`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: "{}",
  });
  if (seenRes.status !== 200) {
    throw new Error(`seen HTTP ${seenRes.status}`);
  }

  const auth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const secret = "sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789";
  const putPref = await fetch(`http://127.0.0.1:${port}/v1/memory/smoke-pref`, {
    method: "PUT",
    headers: auth,
    body: JSON.stringify({
      value: "smoke likes plain files",
      tags: ["preference"],
      author: "operator",
      scope: "project",
    }),
  });
  if (!putPref.ok) {
    throw new Error(`memory put failed ${putPref.status} ${await putPref.text()}`);
  }
  const putSecret = await fetch(`http://127.0.0.1:${port}/v1/memory/smoke-secret`, {
    method: "PUT",
    headers: auth,
    body: JSON.stringify({ value: secret, tags: ["note"], author: "operator" }),
  });
  if (!putSecret.ok) {
    throw new Error(`memory secret put failed ${putSecret.status} ${await putSecret.text()}`);
  }
  const listed = await fetch(`http://127.0.0.1:${port}/v1/memory?scope=project`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const listedText = await listed.text();
  const secretAt = listedText.indexOf(secret);
  if (!listed.ok || !listedText.includes("smoke-pref") || secretAt >= 0 || !listedText.includes("REDACTED")) {
    throw new Error(
      `memory list redact check failed status=${listed.status} ok=${listed.ok} secretAt=${secretAt} b64=${Buffer.from(listedText).toString("base64")}`,
    );
  }
  if (!listedText.includes("markdownFile") || !listedText.includes(".md")) {
    throw new Error(`memory list missing plain-file path body=${listedText.slice(0, 500)}`);
  }
  // #1339: instance backup. The archive verifies, never carries the
  // seeded credential, names its exclusions, and a dry-run restore onto
  // the same gateway skips the identical memory entry. A tampered
  // archive is refused before anything is planned.
  const backupRes = await fetch(`http://127.0.0.1:${port}/v1/backup/export`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const backupText = await backupRes.text();
  if (
    !backupRes.ok ||
    !backupText.includes('"format":"strike.instance-backup"') ||
    backupText.includes(secret) ||
    !backupText.includes("provider credentials") ||
    !(backupRes.headers.get("content-disposition") || "").includes("strike-backup.json")
  ) {
    throw new Error(`backup export check failed status=${backupRes.status} body=${backupText.slice(0, 400)}`);
  }
  const backupPlan = await fetch(`http://127.0.0.1:${port}/v1/backup/restore?dryRun=1`, {
    method: "POST",
    headers: auth,
    body: backupText,
  });
  const backupPlanBody = await backupPlan.json().catch(() => ({}));
  if (!backupPlan.ok || backupPlanBody.dryRun !== true || !(backupPlanBody.counts?.memory?.skip >= 1)) {
    throw new Error(`backup dry-run check failed status=${backupPlan.status} body=${JSON.stringify(backupPlanBody).slice(0, 400)}`);
  }
  const backupTampered = await fetch(`http://127.0.0.1:${port}/v1/backup/restore?dryRun=1`, {
    method: "POST",
    headers: auth,
    body: backupText.replace("smoke likes plain files", "smoke likes tampering"),
  });
  const backupTamperedText = await backupTampered.text();
  if (backupTampered.status !== 400 || !backupTamperedText.includes("integrity check")) {
    throw new Error(`backup tamper check failed status=${backupTampered.status} body=${backupTamperedText.slice(0, 300)}`);
  }
  // Seed one knowledge pin for the Knowledge search check (#1469).
  const putPin = await fetch(`http://127.0.0.1:${port}/v1/knowledge`, {
    method: "PUT",
    headers: auth,
    body: JSON.stringify({
      path: "docs/smoke-pin.md",
      note: "smoke pin note",
      author: "operator",
      scope: "project",
    }),
  });
  if (!putPin.ok) {
    throw new Error(`knowledge pin put failed ${putPin.status} ${await putPin.text()}`);
  }

  const authz = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const call = async (method, path, body) => {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      method,
      headers: authz,
      body: body == null ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    return { status: res.status, text };
  };
  const emptyFolder = await call("POST", "/v1/session-list/folders", { name: "   " });
  if (emptyFolder.status !== 400) {
    throw new Error(`empty folder ${emptyFolder.status} ${emptyFolder.text}`);
  }
  const created = await call("POST", "/v1/session-list/folders", { name: "Reviews" });
  if (created.status !== 201 || !created.text.includes("Reviews")) {
    throw new Error(`create folder ${created.status} ${created.text}`);
  }
  const dup = await call("POST", "/v1/session-list/folders", { name: "reviews" });
  if (dup.status !== 400 || !dup.text.includes("already exists")) {
    throw new Error(`dup folder ${dup.status} ${dup.text}`);
  }
  const listedSessions = await call("GET", "/v1/session-list");
  if (listedSessions.status !== 200 || !listedSessions.text.includes("Reviews")) {
    throw new Error(`list ${listedSessions.status} ${listedSessions.text.slice(0, 300)}`);
  }
  const folder = JSON.parse(created.text);
  const removed = await call("DELETE", `/v1/session-list/folders/${folder.id}`);
  if (removed.status !== 200) {
    throw new Error(`delete folder ${removed.status} ${removed.text}`);
  }
  const missingQ = await call("GET", "/v1/session-list/search");
  if (missingQ.status !== 400) {
    throw new Error(`search missing q ${missingQ.status}`);
  }

  const sessionsDir = join(home, ".strike", "sessions");
  await mkdir(sessionsDir, { recursive: true });
  const line = (text) =>
    JSON.stringify({
      type: "user.message",
      time: "2026-10-26T06:00:00Z",
      data: { text },
    }) + "\n";
  await writeFile(join(sessionsDir, "alpha-session.jsonl"), line("Alpha session notes"));
  await writeFile(
    join(sessionsDir, "alpha-session.meta.json"),
    JSON.stringify({ title: "Alpha session" }) + "\n",
  );
  await writeFile(join(sessionsDir, "beta-session.jsonl"), line("remember the coral bookmark"));
  await writeFile(
    join(sessionsDir, "beta-session.meta.json"),
    JSON.stringify({ title: "Palette polish" }) + "\n",
  );
  // Turn elapsed (#1364) fixture: a replayed session with three turns —
  // a fully timed 4m12s turn, a 20s turn, and a 5-minute turn containing
  // one untimed (old-log) line. Only the first may show an elapsed label.
  const envLine = (type, time, data) =>
    JSON.stringify(time ? { type, time, data } : { type, data }) + "\n";
  const elapsedSession =
    JSON.stringify({ type: "session.header", schemaVersion: 1, time: "2026-10-26T06:00:00Z" }) +
    "\n" +
    envLine("user.message", "2026-10-26T06:00:00Z", { text: "start the slow replay" }) +
    envLine("turn.started", "2026-10-26T06:00:01Z", {}) +
    envLine("tool.begin", "2026-10-26T06:00:30Z", {
      callId: "c1",
      name: "bash",
      args: { command: "sleep 180" },
    }) +
    envLine("tool.end", "2026-10-26T06:03:30Z", { callId: "c1", name: "bash", output: "done" }) +
    envLine("text.delta", "2026-10-26T06:04:00Z", { text: "slow answer" }) +
    envLine("turn.completed", "2026-10-26T06:04:12Z", { stopReason: "end_turn" }) +
    envLine("user.message", "2026-10-26T06:10:00Z", { text: "quick follow up" }) +
    // Reasoning visibility (#1372): one reasoning delta before the answer;
    // hidden until the per-session toggle is switched on.
    envLine("reasoning.delta", "2026-10-26T06:10:05Z", { text: "weighing the quick reply" }) +
    envLine("text.delta", "2026-10-26T06:10:10Z", { text: "fast answer" }) +
    envLine("turn.completed", "2026-10-26T06:10:20Z", { stopReason: "end_turn" }) +
    // Compaction visibility (#1359): markers between turns (outside any
    // turn, so the elapsed check is unchanged), plus one schema-rejected
    // compaction.completed that must degrade to a title-only marker.
    envLine("context.fit_warning", "2026-10-26T06:15:00Z", {
      estimatedTokens: 180000,
      contextLimit: 200000,
      level: "warn",
      message: "context is getting full",
    }) +
    envLine("compaction.started", "2026-10-26T06:15:01Z", { reason: "threshold", strategy: "summarize" }) +
    envLine("compaction.completed", "2026-10-26T06:15:02Z", {
      reason: "threshold",
      strategy: "summarize",
      removed: 6,
      kept: 2,
      summary: "Earlier the slow replay ran sleep 180.",
      residue: { schemaVersion: "1", facts: [{ id: "f1", kind: "fact", text: "bash slept for 180s" }] },
    }) +
    envLine("session.budget_warning", "2026-10-26T06:15:03Z", {
      level: "80",
      kind: "cost_usd",
      costUsd: 4,
      maxCostUsd: 5,
      ratio: 0.8,
      message: "80% of session budget",
    }) +
    // `removed: null` is valid for the daemon's Go decoder (resume must
    // still work) but rejected by the cockpit's generated schema, so it
    // exercises the minimal-marker degradation path.
    envLine("compaction.completed", "2026-10-26T06:15:04Z", { reason: "manual", removed: null }) +
    envLine("user.message", "2026-10-26T06:20:00Z", { text: "old log turn" }) +
    envLine("tool.begin", null, { callId: "c9", name: "bash", args: { command: "sleep 240" } }) +
    envLine("tool.end", "2026-10-26T06:24:00Z", { callId: "c9", name: "bash", output: "done" }) +
    envLine("text.delta", "2026-10-26T06:24:30Z", { text: "old answer" }) +
    envLine("turn.completed", "2026-10-26T06:25:00Z", { stopReason: "end_turn" }) +
    // Turn failure (#1371): an engine-error turn and a loop-guard turn,
    // both under a minute so the elapsed check above is unchanged. Only
    // these two may end with a [data-turn-stop] marker; the clean
    // end_turn turns above must not.
    envLine("user.message", "2026-10-26T06:30:00Z", { text: "failing replay turn" }) +
    envLine("turn.started", "2026-10-26T06:30:01Z", { turnId: "tf1" }) +
    envLine("text.delta", "2026-10-26T06:30:05Z", { text: "half answer" }) +
    envLine("engine.error", "2026-10-26T06:30:06Z", { turnId: "tf1", message: "provider exploded" }) +
    envLine("turn.completed", "2026-10-26T06:30:07Z", { turnId: "tf1", stopReason: "error" }) +
    envLine("user.message", "2026-10-26T06:40:00Z", { text: "looping replay turn" }) +
    envLine("turn.started", "2026-10-26T06:40:01Z", { turnId: "tf2" }) +
    envLine("tool.loop_detected", "2026-10-26T06:40:05Z", {
      turnId: "tf2",
      reason: "identical_calls",
      toolName: "bash",
      count: 3,
      message: "repeated identical failing tool calls",
    }) +
    envLine("turn.completed", "2026-10-26T06:40:06Z", { turnId: "tf2", stopReason: "loop_detected" });
  await writeFile(join(sessionsDir, "elapsed-session.jsonl"), elapsedSession);
  await writeFile(
    join(sessionsDir, "elapsed-session.meta.json"),
    JSON.stringify({ title: "Elapsed replay" }) + "\n",
  );
  // files.invalidated (#1380) fixture: a replayed turn with two
  // back-to-back invalidations (five distinct paths). The cockpit must
  // render ONE muted notice (merged, capped at three names) and refetch
  // the open Files panel once.
  const invalidatedSession =
    JSON.stringify({ type: "session.header", schemaVersion: 1, time: "2026-10-26T07:00:00Z" }) +
    "\n" +
    envLine("user.message", "2026-10-26T07:00:00Z", { text: "touch the tree" }) +
    envLine("turn.started", "2026-10-26T07:00:01Z", {}) +
    envLine("files.invalidated", "2026-10-26T07:00:02Z", {
      paths: ["src/a.go", "src/b.go"],
      reason: "external",
    }) +
    envLine("files.invalidated", "2026-10-26T07:00:03Z", {
      paths: ["src/b.go", "src/c.go", "src/d.go", "src/e.go"],
      reason: "external",
    }) +
    envLine("text.delta", "2026-10-26T07:00:04Z", { text: "tree touched" }) +
    envLine("turn.completed", "2026-10-26T07:00:05Z", { stopReason: "end_turn" });
  await writeFile(join(sessionsDir, "invalidated-session.jsonl"), invalidatedSession);
  await writeFile(
    join(sessionsDir, "invalidated-session.meta.json"),
    JSON.stringify({ title: "Invalidated replay" }) + "\n",
  );
  // Diagnostics refresh on files.invalidated (#1894): a second replay
  // with a burst of two invalidations, resumed while the diagnostics pane
  // is shown; the cockpit must refetch /v1/diagnostics exactly once.
  const invalidatedDiagSession =
    JSON.stringify({ type: "session.header", schemaVersion: 1, time: "2026-10-26T07:10:00Z" }) +
    "\n" +
    envLine("user.message", "2026-10-26T07:10:00Z", { text: "touch the tree for lsp" }) +
    envLine("turn.started", "2026-10-26T07:10:01Z", {}) +
    envLine("files.invalidated", "2026-10-26T07:10:02Z", { paths: ["src/a.go"], reason: "external" }) +
    envLine("files.invalidated", "2026-10-26T07:10:03Z", { paths: ["src/b.go"], reason: "external" }) +
    envLine("text.delta", "2026-10-26T07:10:04Z", { text: "lsp tree touched" }) +
    envLine("turn.completed", "2026-10-26T07:10:05Z", { stopReason: "end_turn" });
  await writeFile(join(sessionsDir, "invalidated-diag-session.jsonl"), invalidatedDiagSession);
  await writeFile(
    join(sessionsDir, "invalidated-diag-session.meta.json"),
    JSON.stringify({ title: "Invalidated diagnostics replay" }) + "\n",
  );
  // Live tool tail (#1374) fixture: turn 1's call streams a chunk and
  // then ends (the final output replaces the tail); turn 2's chatty call
  // streams 250 lines and never ends, so it keeps a tail bounded to the
  // last 200 lines with the truncation hint.
  let tailSession =
    JSON.stringify({ type: "session.header", schemaVersion: 1, time: "2026-10-27T06:00:00Z" }) +
    "\n" +
    envLine("user.message", "2026-10-27T06:00:00Z", { text: "quick command" }) +
    envLine("turn.started", "2026-10-27T06:00:01Z", { turnId: "tt1" }) +
    envLine("tool.begin", "2026-10-27T06:00:02Z", {
      turnId: "tt1",
      callId: "tc1",
      name: "bash",
      args: { command: "echo early" },
    }) +
    envLine("tool.output", "2026-10-27T06:00:03Z", { turnId: "tt1", callId: "tc1", data: "early tail chunk\n" }) +
    envLine("tool.end", "2026-10-27T06:00:04Z", { turnId: "tt1", callId: "tc1", name: "bash", output: "final output" }) +
    envLine("text.delta", "2026-10-27T06:00:05Z", { text: "quick done" }) +
    envLine("turn.completed", "2026-10-27T06:00:06Z", { turnId: "tt1", stopReason: "end_turn" }) +
    envLine("user.message", "2026-10-27T06:01:00Z", { text: "chatty command" }) +
    envLine("turn.started", "2026-10-27T06:01:01Z", { turnId: "tt2" }) +
    envLine("tool.begin", "2026-10-27T06:01:02Z", {
      turnId: "tt2",
      callId: "tc2",
      name: "bash",
      args: { command: "for i in $(seq 0 249); do echo tail line $i; sleep 1; done" },
    });
  for (let i = 0; i < 250; i++) {
    tailSession += envLine("tool.output", "2026-10-27T06:01:03Z", {
      turnId: "tt2",
      callId: "tc2",
      data: `tail line ${i}\n`,
    });
  }
  await writeFile(join(sessionsDir, "tail-session.jsonl"), tailSession);
  await writeFile(
    join(sessionsDir, "tail-session.meta.json"),
    JSON.stringify({ title: "Tool tail replay" }) + "\n",
  );
  // Session rewind (#1336) fixture: two completed turns; the second ran an
  // uncovered (bash) mutation, so a code restore must be acknowledged.
  await writeFile(
    join(sessionsDir, "rewind-session.jsonl"),
    envLine("user.message", "2026-10-26T07:00:00Z", { text: "add login" }) +
      envLine("turn.started", "2026-10-26T07:00:01Z", {}) +
      envLine("text.delta", "2026-10-26T07:00:02Z", { text: "added" }) +
      envLine("turn.completed", "2026-10-26T07:00:03Z", { stopReason: "end_turn", files: [{ path: "auth/login.go", kind: "create" }] }) +
      envLine("user.message", "2026-10-26T07:01:00Z", { text: "run the migration script" }) +
      envLine("turn.started", "2026-10-26T07:01:01Z", {}) +
      envLine("text.delta", "2026-10-26T07:01:02Z", { text: "migrated" }) +
      envLine("turn.completed", "2026-10-26T07:01:03Z", {
        stopReason: "end_turn",
        files: [{ path: "db/schema.sql", kind: "update" }],
        uncovered: ["bash"],
      }),
  );
  await writeFile(join(sessionsDir, "rewind-session.meta.json"), JSON.stringify({ title: "Rewind fixture" }) + "\n");
  // Verification badge (#1373) fixture: one turn per state plus a turn
  // with no verification payload, which must render no badge.
  const verifyTurn = (n, text, verification) =>
    envLine("user.message", `2026-10-28T06:0${n}:00Z`, { text: `verify prompt ${n}` }) +
    envLine("text.delta", `2026-10-28T06:0${n}:05Z`, { text }) +
    envLine(
      "turn.completed",
      `2026-10-28T06:0${n}:10Z`,
      verification ? { stopReason: "end_turn", verification } : { stopReason: "end_turn" },
    );
  const verifySession =
    JSON.stringify({ type: "session.header", schemaVersion: 1, time: "2026-10-28T06:00:00Z" }) +
    "\n" +
    verifyTurn(1, "answer gated", { passed: true, claimed: true, verified: true, summary: "go test passed" }) +
    verifyTurn(2, "answer claimed", { passed: false, claimed: true, verified: false }) +
    verifyTurn(3, "answer failed", {
      passed: false,
      claimed: false,
      verified: true,
      checks: [{ kind: "cmd", value: "go test ./...", passed: false }],
    }) +
    verifyTurn(4, "answer ungated", null);
  await writeFile(join(sessionsDir, "verify-session.jsonl"), verifySession);
  await writeFile(
    join(sessionsDir, "verify-session.meta.json"),
    JSON.stringify({ title: "Verification replay" }) + "\n",
  );
  // Per-turn file summary (#1365) fixture: two back-to-back turns that
  // change files and one in between that changes none.
  const filesSession =
    JSON.stringify({ type: "session.header", schemaVersion: 1, time: "2026-10-29T06:00:00Z" }) +
    "\n" +
    envLine("user.message", "2026-10-29T06:00:00Z", { text: "edit three files" }) +
    envLine("text.delta", "2026-10-29T06:00:05Z", { text: "files answer one" }) +
    envLine("turn.completed", "2026-10-29T06:00:10Z", {
      stopReason: "end_turn",
      files: [
        { path: "README.md", kind: "update" },
        { path: "docs/new.md", kind: "create" },
        { path: "go.mod", kind: "update" },
      ],
    }) +
    envLine("user.message", "2026-10-29T06:01:00Z", { text: "just answer" }) +
    envLine("text.delta", "2026-10-29T06:01:05Z", { text: "files answer none" }) +
    envLine("turn.completed", "2026-10-29T06:01:10Z", { stopReason: "end_turn", files: [] }) +
    envLine("user.message", "2026-10-29T06:02:00Z", { text: "remove one" }) +
    envLine("text.delta", "2026-10-29T06:02:05Z", { text: "files answer two" }) +
    envLine("turn.completed", "2026-10-29T06:02:10Z", {
      stopReason: "end_turn",
      files: [{ path: "old.txt", kind: "delete" }],
    });
  await writeFile(join(sessionsDir, "files-session.jsonl"), filesSession);
  await writeFile(
    join(sessionsDir, "files-session.meta.json"),
    JSON.stringify({ title: "File summary replay" }) + "\n",
  );
  const found = await call("GET", "/v1/session-list/search?q=coral%20bookmark");
  if (found.status !== 200 || !found.text.includes("beta-session") || !found.text.includes("content")) {
    throw new Error(`content search ${found.status} ${found.text.slice(0, 300)}`);
  }
  await writeFile(join(home, ".strike", "config.json"), '{"sandboxAllowDegrade":true}\n').catch(() => {});

  // GitHub ubuntu-latest already has a `google-chrome` on PATH, so a conditional
  // apt install never ran (job 121365125893). That binary never opened
  // /json/list, and stdio "ignore" dropped the reason. These flags are what
  // current headless Chrome needs to bind the DevTools port in a runner:
  // /dev/shm is small, remote debugging refuses a missing allow-origins, and
  // Ozone will not start headless without an explicit platform. Stderr is kept
  // so a crash fails with the browser's own message instead of a blind timeout.
  let chromeLog = "";
  chrome = spawn(
    chromeBin(),
    [
      "--headless=new",
      // Phone layout starts at 720px wide, or a short landscape. Headless
      // Chrome's default window is small enough on some runners to hide the
      // side tabs this check reads. Pin a desktop size.
      "--window-size=1440,900",
      "--disable-gpu",
      "--no-sandbox",
      "--disable-setuid-sandbox",
      "--disable-dev-shm-usage",
      "--no-first-run",
      "--no-default-browser-check",
      "--disable-extensions",
      "--ozone-platform=headless",
      `--remote-debugging-port=${chromePort}`,
      "--remote-debugging-address=127.0.0.1",
      "--remote-allow-origins=*",
      `--user-data-dir=${chromeData}`,
      "about:blank",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  const keep = (buf) => {
    chromeLog += buf.toString();
    if (chromeLog.length > 8000) chromeLog = chromeLog.slice(-8000);
  };
  chrome.stdout.on("data", keep);
  chrome.stderr.on("data", keep);
  let target;
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (chrome.exitCode != null || chrome.signalCode) {
      throw new Error(
        `chrome exited before devtools (code=${chrome.exitCode} signal=${chrome.signalCode})\n${chromeLog.slice(-1500)}`,
      );
    }
    let port = chromePort;
    try {
      const active = await readFile(join(chromeData, "DevToolsActivePort"), "utf8");
      const n = Number(active.split("\n")[0]);
      if (n > 0) port = n;
    } catch {
      // file appears once the browser binds the port
    }
    try {
      const list = await fetch(`http://127.0.0.1:${port}/json/list`);
      if (list.ok) {
        const tabs = await list.json();
        // Chrome's bundled component extensions also expose a DevTools
        // websocket. Attaching to one of those leaves the cockpit on
        // about:blank forever (href stays chrome-extension://…).
        target = tabs.find(
          (t) =>
            t.type === "page" &&
            t.webSocketDebuggerUrl &&
            !String(t.url || "").startsWith("chrome-extension://") &&
            !String(t.url || "").startsWith("chrome://"),
        );
        if (!target) {
          await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" }).catch(() => {});
        }
        if (target) break;
      }
    } catch {
      // chrome still starting
    }
    await delay(100);
  }
  if (!target) {
    throw new Error(`chrome devtools did not open\n${chromeLog.slice(-1500)}`);
  }

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve);
    ws.addEventListener("error", () => reject(new Error("devtools websocket failed")));
  });
  let next = 0;
  const pending = new Map();
  // Page-side evidence collected for the whole run; dumped on any timeout
  // so a red run says *why* the page never reached the wanted state.
  const consoleEvents = [];
  const failedRequests = [];
  const requestUrls = new Map();
  const noteConsole = (text) => {
    if (text) consoleEvents.push(String(text).slice(0, 300));
    if (consoleEvents.length > 50) consoleEvents.shift();
  };
  const noteFailed = (text) => {
    failedRequests.push(String(text).slice(0, 300));
    if (failedRequests.length > 50) failedRequests.shift();
  };
  ws.addEventListener("message", (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
      return;
    }
    // DevTools events (no id): keep the ones that explain a stuck page.
    const p = msg.params || {};
    if (msg.method === "Runtime.consoleAPICalled" && (p.type === "error" || p.type === "warning")) {
      noteConsole(
        (p.args || []).map((a) => a.value ?? a.description ?? a.type ?? "").join(" ").trim() || p.type,
      );
    } else if (msg.method === "Runtime.exceptionThrown") {
      noteConsole(p.exceptionDetails?.exception?.description || p.exceptionDetails?.text || "uncaught exception");
    } else if (msg.method === "Log.entryAdded" && p.entry?.level === "error") {
      noteConsole(`${p.entry.text || ""} ${p.entry.url || ""}`.trim());
    } else if (msg.method === "Network.requestWillBeSent") {
      requestUrls.set(p.requestId, p.request?.url || "");
    } else if (msg.method === "Network.responseReceived" && p.response?.status >= 400) {
      noteFailed(`${p.response.status} ${p.response.url || requestUrls.get(p.requestId) || ""}`);
    } else if (msg.method === "Network.loadingFailed") {
      noteFailed(`loadingFailed ${p.errorText || ""} ${requestUrls.get(p.requestId) || ""}`.trim());
    }
  });
  const send = (method, params) =>
    new Promise((resolve) => {
      const id = ++next;
      pending.set(id, resolve);
      ws.send(JSON.stringify({ id, method, params }));
    });

  // Runtime.evaluate that surfaces page exceptions instead of swallowing
  // them into an undefined value the caller then waits on forever.
  const evaluate = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    const r = ev.result;
    if (r?.exceptionDetails) {
      const detail =
        r.exceptionDetails.exception?.description || r.exceptionDetails.text || "unknown page exception";
      throw new Error(`page evaluate failed: ${String(detail).slice(0, 400)}`);
    }
    return r?.result?.value;
  };

  const pageSnapshot = async () => {
    try {
      const raw = await evaluate(`(() => JSON.stringify({
        url: location.href,
        readyState: document.readyState,
        views: [...document.querySelectorAll("[data-view]")].map((e) => e.getAttribute("data-view")),
        navLabels: [...document.querySelectorAll("[data-nav]")].map(
          (e) => e.getAttribute("data-nav") + ":" + (e.innerText || "").trim().replace(/\\s+/g, " ").slice(0, 40),
        ),
        goalsRoot: !!document.querySelector("#goals-root"),
        goalsBtn: !!document.querySelector('button[data-view="goals"]'),
        text: (document.body && document.body.innerText || "").slice(0, 600),
      }))()`);
      return JSON.parse(raw || "{}");
    } catch (e) {
      return { snapshotError: String(e?.message || e) };
    }
  };

  let lastProbe;
  const diagnostics = async (probe) => {
    const lines = ["--- smoke diagnostics ---"];
    const shown = probe !== undefined ? probe : lastProbe;
    if (shown !== undefined) lines.push(`last probe: ${JSON.stringify(shown).slice(0, 1000)}`);
    lines.push(`page: ${JSON.stringify(await pageSnapshot()).slice(0, 1600)}`);
    if (consoleEvents.length) lines.push(`console: ${consoleEvents.slice(-10).join(" | ").slice(0, 1200)}`);
    if (failedRequests.length) {
      lines.push(`failed requests: ${failedRequests.slice(-10).join(" | ").slice(0, 1200)}`);
    }
    return lines.join("\n");
  };
  collectDiagnostics = () => diagnostics();

  // Poll `probe` (an async fn returning {ok, ...detail}) until it reports
  // ok or the deadline passes. Probe exceptions count as "not yet" — the
  // page is usually mid-navigation — but are kept for the timeout dump.
  // Callers get the probe's full detail (their guards may re-check long
  // page text); only the copy kept for diagnostics is truncated.
  const shrink = (v) => {
    if (!v || typeof v !== "object") return v;
    const out = {};
    for (const [k, val] of Object.entries(v)) {
      out[k] = typeof val === "string" && val.length > 300 ? `${val.slice(0, 300)}…` : val;
    }
    return out;
  };
  const waitFor = async (label, probe, { timeout = WAIT_MS, interval = 200 } = {}) => {
    const end = Date.now() + timeout;
    for (;;) {
      try {
        const v = await probe();
        lastProbe = shrink(v);
        if (v && v.ok) return v;
      } catch (e) {
        lastProbe = { probeError: String(e?.message || e).slice(0, 300) };
      }
      if (Date.now() >= end) {
        throw new Error(`timed out after ${timeout}ms waiting for: ${label}\n${await diagnostics()}`);
      }
      await delay(Math.min(interval, Math.max(0, end - Date.now())));
    }
  };

  // Open a palette command by its visible label. Enter runs the first hit.
  const runPalette = async (query) => {
    await evaluate(`(() => {
      if (!document.querySelector('[data-nav="palette-dialog"]')) {
        document.querySelector('[data-nav="palette"]')?.click();
      }
      return true;
    })()`);
    await waitFor(
      `palette open (${query})`,
      async () => ({
        ok: (await evaluate(`!!document.querySelector('[data-nav="palette-dialog"] input')`)) === true,
      }),
      { timeout: STEP_MS, interval: 100 },
    );
    const quoted = JSON.stringify(query);
    await evaluate(`(() => {
      const input = document.querySelector('[data-nav="palette-dialog"] input');
      const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
      proto.set.call(input, ${quoted});
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: ${quoted} }));
      return input.value;
    })()`);
    await waitFor(
      `palette row (${query})`,
      async () => {
        const text = String(
          (await evaluate(`(document.querySelector('[data-nav="palette-dialog"]') || {}).innerText || ""`)) || "",
        );
        return { ok: text.toLowerCase().includes(String(query).toLowerCase()), text: text.slice(0, 240) };
      },
      { timeout: STEP_MS, interval: 80 },
    );
    await evaluate(`(() => {
      const input = document.querySelector('[data-nav="palette-dialog"] input');
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      return true;
    })()`);
  };

  // Probe a Goals list row's own status label (#1455): the status filter
  // chips render every status word, so whole-body text cannot prove a run
  // reached a status.
  const goalRowProbe = (title, status) => async () => {
    const rows = await evaluate(`[...document.querySelectorAll(".goals-row")].map((r) => [
      r.querySelector(".goals-row-title")?.textContent.trim(),
      r.querySelector(".goals-status")?.textContent.trim(),
    ])`) || [];
    return { ok: rows.some(([t, s]) => t === title && s === status), body: JSON.stringify(rows) };
  };

  // Probe on the whole body text.
  const textProbe = (ready) => async () => {
    const body = (await evaluate(`document.body ? document.body.innerText : ""`)) || "";
    return { ok: !!ready(body), body };
  };

  await send("Page.enable");
  await send("Runtime.enable");
  await send("Network.enable");
  await send("Log.enable");
  // JSON calls send the bearer from localStorage. The live stream uses the
  // cookie set by /?token= (HttpOnly, stripped from the URL before WASM boots).
  await send("Page.addScriptToEvaluateOnNewDocument", {
    source: `try{localStorage.setItem("strike.token", ${JSON.stringify(token)});}catch(e){}`,
  });
  await send("Page.navigate", {
    url: `http://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`,
  });
  const readCockpit = async () => {
    const ev = await send("Runtime.evaluate", {
      expression: `(() => {
        const body = document.body ? document.body.innerText : "";
        const cs = getComputedStyle(document.documentElement);
        const mark = document.querySelector("[data-status]");
        return JSON.stringify({
          body,
          acid: cs.getPropertyValue("--acid").trim(),
          ground: cs.getPropertyValue("--ground").trim(),
          title: document.title,
          // #1590 A6/A7: the header prints no version/gateway text; the
          // connection state ("Connected · v…") is the brand mark's title.
          version: document.querySelector(".brand-mark")?.getAttribute("title") || "",
          status: mark ? mark.getAttribute("data-status") : "",
          chip: !!document.querySelector(".status-chip"),
        });
      })()`,
      returnByValue: true,
    });
    const raw = ev.result?.result?.value || "{}";
    return JSON.parse(raw);
  };

  const statusIds = ["idle", "running", "waiting", "blocked", "failed", "done", "stopped", "disconnected"];
  const boot = await waitFor(
    "cockpit boot (labels, theme tokens, status chip)",
    async () => {
      const parsed = await readCockpit();
      const text = parsed.body || "";
      return {
        ok:
          text.includes("Run a command") &&
          text.includes("Activity") &&
          (parsed.version || "").includes("Connected") &&
          parsed.acid === "#7c3aed" &&
          !!parsed.chip &&
          statusIds.includes(parsed.status || "") &&
          (parsed.title || "").includes("Strike"),
        ...parsed,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const text = boot.body || "";
  const acid = boot.acid || "";
  const ground = boot.ground || "";
  const title = boot.title || "";
  const statusId = boot.status || "";
  const chip = !!boot.chip;
  const version = boot.version || "";
  const want = ["Run a command", "Strike", "Activity", "Files", "Changes", "Git", "Terminal", "Approval"];
  const missingText = want.filter((s) => !text.includes(s));
  if (!version.includes("Connected")) missingText.push("Connected (brand-mark title)");
  if (missingText.length || acid !== "#7c3aed" || ground !== "#14131c" || !chip || !statusIds.includes(statusId) || !title.includes("Strike")) {
    throw new Error(
      `cockpit check failed missing=${missingText.join(",")} acid=${acid} ground=${ground} status=${statusId} chip=${chip} title=${title} version=${version} text=${text.slice(0, 400)}\n${await diagnostics()}`,
    );
  }
  // #1488: the watermark was just set to daemon now, so the away gap is
  // under the threshold: no catch-up panel (empty delta = cockpit as today).
  {
    const panel = await evaluate(`!!document.querySelector("[data-briefing]")`);
    if (panel) {
      throw new Error(`away briefing shown with no away gap\n${await diagnostics()}`);
    }
  }
  // Approvals (#1266): mode menu, Yolo confirm + Escape, and the
  // permission-rules API confirm gates. Runs while the boot session is
  // live (rule writes require a live root) and leaves the chip on Default.
  const apprEval = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    return ev.result?.result?.value || "";
  };
  const apprText = () => apprEval("document.body ? document.body.innerText : \"\"");
  const apprOpenPalette = () =>
    apprEval(`(() => {
      const open = document.querySelector('[data-nav="palette-dialog"]');
      if (open) return "open";
      const btn = document.querySelector('[data-nav="palette"]');
      if (!btn) return "no-palette";
      btn.click();
      return "clicked";
    })()`);
  // Condition-based waits with the clicks inside the probes (this file's
  // post-#1378 discipline): a click that lands mid-render is retried
  // instead of failing against a fixed deadline.
  await waitFor(
    "approval commands (modes, rules and audit, deny copy)",
    async () => {
      const t = await apprText();
      if (
        t.includes("Set approval mode: Default") &&
        t.includes("Set approval mode: Plan") &&
        t.includes("Set approval mode: Yolo") &&
        t.includes("Approval rules and audit") &&
        t.includes("Deny rules still apply")
      ) {
        return { ok: true, t };
      }
      await apprOpenPalette();
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await waitFor(
    "yolo confirm dialog",
    async () => {
      const t = await apprText();
      if (t.includes("Use Yolo for this session?")) {
        return { ok: true };
      }
      await apprOpenPalette();
      await apprEval(`document.querySelector('[data-command-id="mode-yolo"]')?.click()`);
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await apprEval(`window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await waitFor(
    "escape keeps Default",
    async () => {
      const t = await apprText();
      return { ok: t.includes("Approval · Default") && !t.includes("Use Yolo for this session?") };
    },
    { timeout: STEP_MS, interval: 250 },
  );

  // Permissions presets / diff / explain (#1504): read-only section in the
  // rules panel. A host without the Permissions service answers 501 and
  // the section must be absent; otherwise it lists the daemon's presets,
  // diffs the default pair, and labels a dry-run explain.
  const peRes = await fetch(`http://127.0.0.1:${port}/v1/permissions/presets`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const peText = await peRes.text();
  await apprOpenPalette();
  await waitFor(
    "permissions panel open for presets",
    async () => {
      const open = await apprEval(`!!document.querySelector('.approvals-panel')`);
      if (open === true) return { ok: true };
      await apprOpenPalette();
      await apprEval(`document.querySelector('[data-command-id="approvals.rules"]')?.click()`);
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  if (peRes.status === 501) {
    // The section renders only after the presets request settles with a
    // non-501, so it must never appear here; give the fetch time to land.
    await new Promise((r) => setTimeout(r, 1000));
    const shown = await apprEval(`!!document.querySelector('[data-permissions-explain]')`);
    if (shown === true) throw new Error("presets section must hide when permissions capability is unavailable");
  } else {
    if (peRes.status !== 200) throw new Error(`presets: ${peRes.status} ${peText.slice(0, 300)}`);
    const peIds = (JSON.parse(peText).presets || []).map((p) => p.ID).filter(Boolean);
    if (peIds.length < 2) throw new Error(`presets: expected at least two shipped presets: ${peText.slice(0, 300)}`);
    await waitFor(
      "presets section lists the daemon presets",
      async () => {
        const ids = JSON.parse(
          await apprEval(`JSON.stringify([...document.querySelectorAll('[data-permission-presets] [data-preset-id]')].map((li) => li.getAttribute("data-preset-id")))`),
        );
        return { ok: peIds.every((id) => ids.includes(id)) };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    await waitFor(
      "preset diff renders",
      async () => {
        const hit = await apprEval(`!!document.querySelector('[data-preset-diff]')`);
        if (hit === true) return { ok: true };
        const notice = await apprEval(`[...document.querySelectorAll('[data-permissions-explain] .notice')].map((n) => n.innerText).join(" | ")`);
        if (notice) throw new Error(`preset diff error: ${notice}`);
        await apprEval(`document.querySelector('[data-preset-diff-run]')?.click()`);
        return { ok: false };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    await apprEval(`(() => {
      const sel = document.querySelector('select[aria-label="Dry-run preset"]');
      if (sel) { sel.value = ${JSON.stringify(peIds[0])}; sel.dispatchEvent(new Event("change", { bubbles: true })); }
      return "ok";
    })()`);
    await waitFor(
      "dry-run explain result is labelled",
      async () => {
        const state = JSON.parse(
          await apprEval(`JSON.stringify((() => {
            const r = document.querySelector('[data-permission-explain-result]');
            return r ? { dry: r.getAttribute("data-dry-run"), text: r.innerText || "" } : null;
          })())`),
        );
        if (state && state.dry === "true" && state.text.includes(`Dry run under ${peIds[0]}`)) return { ok: true };
        await apprEval(`document.querySelector('[data-permission-explain-run]')?.click()`);
        return { ok: false };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  }
  await apprEval(`[...document.querySelectorAll(".approvals-panel-head button")].find((b) => (b.textContent || "").trim() === "Close")?.click()`);

  // Sandbox explain (#1532): the Permissions panel shows the daemon's real
  // GET /v1/sandbox snapshot (or the honest unavailable state on 501).
  const sbRes = await fetch(`http://127.0.0.1:${port}/v1/sandbox`, { headers: { Authorization: `Bearer ${token}` } });
  const sbSnap = sbRes.status === 200 ? await sbRes.json() : null;
  if (sbRes.status !== 200 && sbRes.status !== 501) {
    throw new Error(`GET /v1/sandbox: ${sbRes.status}`);
  }
  const sbModeLabel = { off: "Off", "read-only": "Read-only", "workspace-write": "Workspace write" };
  await waitFor(
    "sandbox section in the Permissions panel",
    async () => {
      const probe = await apprEval(`(() => {
        const sec = document.querySelector('[data-section="sandbox"]');
        if (!sec) return null;
        const state = sec.querySelector('[data-sandbox-state]');
        return {
          text: sec.innerText || "",
          state: state ? state.getAttribute('data-sandbox-state') : "",
          profile: !!sec.querySelector('details.sandbox-profile pre'),
        };
      })()`);
      if (!probe) {
        await apprOpenPalette();
        await apprEval(`document.querySelector('[data-command-id="approvals.rules"]')?.click()`);
        return { ok: false };
      }
      if (!probe.text.includes("OS sandbox")) return { ok: false, probe };
      if (!sbSnap) {
        return { ok: probe.state === "unavailable" && !probe.text.includes("Active sandbox"), probe };
      }
      const label = sbModeLabel[sbSnap.mode] || sbSnap.mode;
      // "unprobed" (serve seeds it before any sandboxed command) is unknown,
      // never rendered as "no working backend".
      const want = sbSnap.available ? "ready" : sbSnap.backend === "unprobed" ? "unprobed" : "no-backend";
      return {
        ok:
          probe.state === want &&
          probe.text.includes(label) &&
          probe.profile === !!sbSnap.explain &&
          (sbSnap.networkAllow || []).every((h) => probe.text.includes(h)),
        probe,
        snap: sbSnap,
      };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await waitFor(
    "close the Permissions panel",
    async () => {
      const open = await apprEval(`!!document.querySelector('.approvals-panel')`);
      if (!open) return { ok: true };
      await apprEval(`[...document.querySelectorAll('.approvals-panel-head button')].find((b) => (b.innerText || '').trim() === 'Close')?.click()`);
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );

  const apprAuth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const apprRuleCall = async (path, body) => {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      method: "POST",
      headers: apprAuth,
      body: JSON.stringify(body),
    });
    const text = await res.text();
    return { status: res.status, text };
  };
  const apprDenied = await apprRuleCall("/v1/permissions/rules", {
    scope: "global",
    permission: "bash",
    pattern: "smoke-deny",
    action: "deny",
    confirm: false,
  });
  if (apprDenied.status !== 200 || !apprDenied.text.includes("smoke-deny")) {
    throw new Error(`deny rule: ${apprDenied.status} ${apprDenied.text.slice(0, 300)}`);
  }
  const apprListed = await fetch(`http://127.0.0.1:${port}/v1/permissions/rules`, { headers: apprAuth });
  const apprListedText = await apprListed.text();
  if (apprListed.status !== 200 || !apprListedText.includes("smoke-deny")) {
    throw new Error(`rules list: ${apprListed.status} ${apprListedText.slice(0, 300)}`);
  }
  const apprQuiet = await apprRuleCall("/v1/permissions/rules", {
    scope: "global",
    permission: "bash",
    pattern: "smoke-allow",
    action: "allow",
    confirm: false,
  });
  if (apprQuiet.status !== 400 || !apprQuiet.text.includes("confirmation required")) {
    throw new Error(`allow without confirm: ${apprQuiet.status} ${apprQuiet.text.slice(0, 300)}`);
  }
  const apprAllowed = await apprRuleCall("/v1/permissions/rules", {
    scope: "global",
    permission: "bash",
    pattern: "smoke-allow",
    action: "allow",
    confirm: true,
  });
  if (apprAllowed.status !== 200 || !apprAllowed.text.includes("smoke-allow")) {
    throw new Error(`allow with confirm: ${apprAllowed.status} ${apprAllowed.text.slice(0, 300)}`);
  }
  const apprRules = JSON.parse(apprListedText).rules || [];
  const apprDenyIndex = apprRules.find((row) => row.pattern === "smoke-deny" && row.action === "deny");
  if (!apprDenyIndex) throw new Error(`deny index missing: ${apprListedText.slice(0, 300)}`);
  const apprKept = await apprRuleCall("/v1/permissions/rules/remove", {
    scope: "global",
    index: apprDenyIndex.index,
    confirm: false,
  });
  if (apprKept.status !== 400 || !apprKept.text.includes("confirmation required")) {
    throw new Error(`remove deny without confirm: ${apprKept.status} ${apprKept.text.slice(0, 300)}`);
  }
  const apprRemoved = await apprRuleCall("/v1/permissions/rules/remove", {
    scope: "global",
    index: apprDenyIndex.index,
    confirm: true,
  });
  if (apprRemoved.status !== 200 || apprRemoved.text.includes("smoke-deny")) {
    throw new Error(`remove deny: ${apprRemoved.status} ${apprRemoved.text.slice(0, 300)}`);
  }

  // Rule duplicate (#1433): the surviving smoke-allow row's Duplicate
  // button prefills the Add form verbatim and focuses Pattern. Prefill
  // alone creates nothing; the copy is stored only through the existing
  // Add path, dangerous-add confirm included.
  await apprOpenPalette();
  await waitFor(
    "rule duplicate button in the rules panel",
    async () => {
      const hit = await apprEval(`!!document.querySelector('[data-rule-duplicate]')`);
      if (hit === true) return { ok: true };
      await apprEval(`document.querySelector('[data-command-id="approvals.rules"]')?.click()`);
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  const dupClicked = await apprEval(`(() => {
    const row = [...document.querySelectorAll(".approvals-rule")].find((li) => (li.textContent || "").includes("smoke-allow"));
    const btn = row ? row.querySelector("[data-rule-duplicate]") : null;
    if (!btn) return "missing";
    btn.click();
    return "clicked";
  })()`);
  if (dupClicked !== "clicked") {
    throw new Error(`rule duplicate click: ${dupClicked}`);
  }
  await waitFor(
    "duplicate prefill (all four fields, pattern focused)",
    async () => {
      const state = JSON.parse(
        await apprEval(`JSON.stringify((() => {
          const pattern = document.querySelector('input[aria-label="Pattern"]');
          return {
            scope: document.querySelector('select[aria-label="Rule scope"]')?.value || "",
            permission: document.querySelector('select[aria-label="Permission name"]')?.value || "",
            pattern: pattern?.value || "",
            action: document.querySelector('select[aria-label="Rule action"]')?.value || "",
            focused: document.activeElement === pattern,
          };
        })())`),
      );
      return {
        ok:
          state.scope === "global" &&
          state.permission === "bash" &&
          state.pattern === "smoke-allow" &&
          state.action === "allow" &&
          state.focused,
      };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  const prefillListed = await fetch(`http://127.0.0.1:${port}/v1/permissions/rules`, { headers: apprAuth });
  const prefillDoc = JSON.parse(await prefillListed.text());
  if ((prefillDoc.rules || []).filter((row) => row.pattern === "smoke-allow").length !== 1) {
    throw new Error(`prefill must not create a rule: ${JSON.stringify(prefillDoc.rules)}`);
  }
  await apprEval(`(() => {
    const input = document.querySelector('input[aria-label="Pattern"]');
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    setter.call(input, "smoke-allow-copy");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    [...document.querySelectorAll(".approvals-form button")]
      .find((b) => (b.textContent || "").trim() === "Add rule")?.click();
    return "ok";
  })()`);
  await waitFor(
    "duplicated add hits the dangerous-add confirm",
    async () => {
      const t = await apprText();
      if (t.includes("Add allow rule?")) {
        await apprEval(`[...document.querySelectorAll(".approvals-dialog button")].find((b) => (b.textContent || "").trim() === "Add rule")?.click()`);
        return { ok: true };
      }
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await waitFor(
    "duplicated rule stored beside the original",
    async () => {
      const res = await fetch(`http://127.0.0.1:${port}/v1/permissions/rules`, { headers: apprAuth });
      const doc = JSON.parse(await res.text());
      const pats = (doc.rules || []).map((row) => row.pattern);
      return { ok: pats.includes("smoke-allow") && pats.includes("smoke-allow-copy") };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await apprEval(`[...document.querySelectorAll(".approvals-panel-head button")].find((b) => (b.textContent || "").trim() === "Close")?.click()`);

  const evalValue = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    if (ev.result?.exceptionDetails) {
      throw new Error(`evaluate failed: ${JSON.stringify(ev.result.exceptionDetails)}`);
    }
    return ev.result?.result?.value;
  };
  const bodyText = async () => String((await evalValue("document.body ? document.body.innerText : ''")) || "");

  // Session-ux transcript (#1591): the empty state keeps the headline and
  // Add provider. Rename, regenerate, and export are palette commands.
  await waitFor(
    "session-ux empty transcript (headline, add provider, palette commands)",
    async () => {
      const t = await bodyText();
      if (!t.includes("This session is empty.") && !t.includes("Send a message to start.")) {
        return { ok: false, t: t.slice(0, 200) };
      }
      if (!t.includes("Add provider")) return { ok: false, t: t.slice(0, 200) };
      const dialog = await evalValue(`!!document.querySelector('[data-nav="palette-dialog"]')`);
      if (!dialog) {
        await evalValue(`document.querySelector('[data-nav="palette"]')?.click()`);
        return { ok: false };
      }
      const ids = await evalValue(`JSON.stringify([...document.querySelectorAll("[data-command-id]")].map((el) => el.getAttribute("data-command-id")))`);
      const have = String(ids || "");
      return {
        ok:
          have.includes("session-rename") &&
          have.includes("transcript.regenerate") &&
          have.includes("session-export") &&
          have.includes("session-export-md"),
        have: have.slice(0, 300),
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evalValue(`document.querySelector('[data-nav="palette-dialog"]')?.closest('.palette-backdrop')?.click()`);

  // Gateway switcher (#1265): opens from the header, shows this gateway,
  // never exposes the smoke token in page text, in a visible (non-password)
  // input, or in the daemon log, and Escape closes it. The PR's original
  // checks expected the unauthenticated panel because its harness never
  // handed the browser a token; the current harness authenticates via the
  // /?token= cookie handoff, so the panel correctly reports Connected with
  // the host token, and the token lives only in the designated password
  // field (the PR's design: rotate writes the new token there). Assertions
  // use the hardened waitFor poller.
  if (text.includes(token)) {
    throw new Error("cockpit innerText contains the smoke token");
  }
  const gwState = await waitFor(
    "gateway panel (detail, token field, Connected)",
    async () => {
      await evalValue(`(() => {
        const pop = document.querySelector(".gw-pop");
        if (pop) return "open";
        const dialog = document.querySelector('[data-nav="palette-dialog"]');
        if (!dialog) {
          document.querySelector('[data-nav="palette"]')?.click();
          return "opening";
        }
        document.querySelector('[data-command-id="gateways.open"]')?.click();
        return "clicked";
      })()`);
      const parsed = JSON.parse((await evalValue(`(() => {
        const pop = document.querySelector(".gw-pop");
        const visibleInputs = [...document.querySelectorAll("input")]
          .filter((el) => el.type !== "password")
          .map((el) => el.value || "")
          .join("\\n");
        return JSON.stringify({
          open: !!pop,
          pop: pop ? pop.innerText : "",
          body: document.body ? document.body.innerText : "",
          version: document.querySelector(".brand-mark")?.getAttribute("title") || "",
          tokenField: !!pop?.querySelector('input[type="password"]'),
          visibleInputs,
        });
      })()`)) || "{}");
      return {
        ok:
          !!parsed.open &&
          parsed.tokenField &&
          (parsed.version || "").includes("Connected") &&
          (parsed.pop || "").includes("Token for this gateway"),
        ...parsed,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  if ((gwState.body || "").includes(token) || (gwState.visibleInputs || "").includes(token)) {
    throw new Error("gateway UI exposed the smoke token");
  }
  await evalValue(`(() => { const pop = document.querySelector(".gw-pop"); if (pop) pop.focus(); return !!pop; })()`);
  // Trusted key events. A synthetic KeyboardEvent is not what the panel handles.
  await send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "Escape",
    code: "Escape",
    windowsVirtualKeyCode: 27,
    nativeVirtualKeyCode: 27,
  });
  await send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "Escape",
    code: "Escape",
    windowsVirtualKeyCode: 27,
    nativeVirtualKeyCode: 27,
  });
  await waitFor(
    "gateway Escape closes the switcher",
    async () => ({ ok: (await evalValue(`!!document.querySelector(".gw-pop")`)) === false }),
    { timeout: STEP_MS, interval: 100 },
  );
  if (strikeLog.includes(token)) {
    throw new Error("daemon log contains the smoke token");
  }


  // Switch model is a palette command. The status line stays glanceable.
  // Filtering and the daemon's unknown-provider error still have to surface.
  await waitFor(
    "model status is glanceable",
    async () => {
      const t = await bodyText();
      return { ok: t.includes("Model ·"), t: t.slice(0, 240) };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await waitFor(
    "switch model palette",
    async () => {
      const open = await evalValue(`!!document.querySelector('[data-nav="palette-dialog"]')`);
      if (!open) {
        await evalValue(`document.querySelector('[data-nav="palette"]')?.click()`);
        return { ok: false };
      }
      const stepped = await evalValue(`!!document.querySelector('[data-choice-id]')`);
      if (!stepped) {
        await evalValue(`document.querySelector('[data-command-id="switch-model"]')?.click()`);
        return { ok: false };
      }
      const t = await bodyText();
      return { ok: t.includes("echo"), t: t.slice(0, 240) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Palette geometry: the dialog must lie fully inside the viewport.
  let panelRect = null;
  for (let i = 0; i < 20; i++) {
    panelRect = await evalValue(`(() => {
      const el = document.querySelector("[data-nav='palette-dialog']");
      if (!el) return null;
      const r = el.getBoundingClientRect();
      return { left: r.left, top: r.top, right: r.right, bottom: r.bottom, width: r.width, height: r.height, vw: window.innerWidth, vh: window.innerHeight };
    })()`);
    if (
      panelRect &&
      panelRect.width > 0 &&
      panelRect.height > 0 &&
      panelRect.left >= 0 &&
      panelRect.top >= 0 &&
      panelRect.right <= panelRect.vw &&
      panelRect.bottom <= panelRect.vh
    ) {
      break;
    }
    await delay(100);
  }
  if (
    !panelRect ||
    !(panelRect.width > 0) ||
    !(panelRect.height > 0) ||
    panelRect.left < 0 ||
    panelRect.top < 0 ||
    panelRect.right > panelRect.vw ||
    panelRect.bottom > panelRect.vh
  ) {
    throw new Error(`model palette outside viewport rect=${JSON.stringify(panelRect)}`);
  }
  const modelFiltered = await evalValue(`(() => {
    const el = document.querySelector("[data-nav='palette-dialog'] input");
    if (!el) return "missing";
    el.focus();
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(el, "zzz");
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return "ok";
  })()`);
  if (modelFiltered !== "ok") {
    throw new Error(`model filter input missing (${modelFiltered})`);
  }
  let empty = "";
  for (let i = 0; i < 20; i++) {
    empty = await bodyText();
    if (empty.includes("Nothing matches")) break;
    await delay(100);
  }
  if (!empty.includes("Nothing matches")) {
    throw new Error(`model filter did not empty the list: ${empty.slice(0, 400)}`);
  }
  await evalValue(`(() => {
    const el = document.querySelector("[data-nav='palette-dialog'] input");
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(el, "nope/missing");
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    return "ok";
  })()`);
  await delay(400);
  const freeform = await evalValue(`(() => {
    const chip = document.querySelector("[data-model-status]");
    const text = chip ? chip.textContent : "";
    const hint = document.body.innerText.includes("Enter uses");
    const blocked = document.body.innerText.includes('"nope/missing" is not in the model catalog');
    return JSON.stringify({ text, hint, blocked, menu: !!document.querySelector("[data-model-menu]") });
  })()`);
  const freeformParsed = JSON.parse(freeform || "{}");
  if (String(freeformParsed.text || "").includes("nope/missing") || freeformParsed.hint) {
    throw new Error(`freeform model enter was accepted: ${freeform}`);
  }
  // #1512: a typed model that is not in the catalog must block with a
  // specific reason and a path to Providers, not close silently.
  if (!freeformParsed.blocked) {
    throw new Error(`freeform model enter did not explain the rejection: ${freeform}`);
  }

  // Close the model menu (and any error note) before the navbar pass.
  // #1512: the rejected freeform pick keeps Switch model's prompt open,
  // so the first Escape steps back to the palette root and the second
  // closes the palette. Escape until the dialog is gone.
  for (let i = 0; i < 3; i++) {
    const open = await evalValue(`Boolean(document.querySelector("[data-nav='palette-dialog']"))`);
    if (!open) break;
    await send("Runtime.evaluate", {
      expression: `document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`,
    });
    await delay(250);
  }
  if (await evalValue(`Boolean(document.querySelector("[data-nav='palette-dialog']"))`)) {
    throw new Error("Escape did not close the palette after the rejected model pick");
  }
  await send("Runtime.evaluate", {
    expression: `document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`,
  });
  await delay(250);
  // Session panel (#1309): seeded sessions, folder note, and conductor chip
  // render in the rail; wait on condition (not a fixed delay) before the
  // keyboard pass below drives its cursor.
  await waitFor(
    "session panel (seeded sessions, folders, conductor)",
    textProbe((t) =>
      ["All", "Unread", "Palette polish", "Alpha session"].every((x) => t.includes(x)) &&
      !t.includes("Folders group sessions") &&
      !t.includes("j and k move. Enter opens."),
    ),
    { timeout: WAIT_MS, interval: 250 },
  );
  await waitFor(
    "session panel cursor",
    async () => {
      const id = await evalValue(
        `document.querySelector(".ss-cursor")?.getAttribute("data-session-id") || ""`,
      );
      return { ok: !!id, id };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Folder Remove confirm (#1556): folder removal now lives behind the
  // palette (declutter #1669), and its bind must open the shared confirm
  // dialog stating both halves — sessions stay (unfiled, not deleted) and
  // the grouping cannot be restored. Cancelling leaves the folder;
  // confirming removes it while its sessions stay in the list.
  const smokeFolder = await call("POST", "/v1/session-list/folders", { name: "Smoke folder" });
  if (smokeFolder.status !== 201) {
    throw new Error(`smoke folder create ${smokeFolder.status} ${smokeFolder.text}`);
  }
  // File Alpha session into the folder so Remove proves the "sessions stay,
  // become unfiled" half of the confirm copy, not just the folder vanishing.
  const smokeFolderId = JSON.parse(smokeFolder.text).id;
  const filed = await call("PATCH", "/v1/session-list/sessions/alpha-session", { folderId: smokeFolderId });
  if (filed.status !== 200) {
    throw new Error(`file alpha into smoke folder ${filed.status} ${filed.text}`);
  }
  const folderRowProbe = async () => {
    const raw = await evaluate(`(() => {
      const row = [...document.querySelectorAll(".ss-folder-row")].find((r) => r.textContent.includes("Smoke folder"));
      return JSON.stringify({ row: !!row });
    })()`);
    return JSON.parse(raw || "{}");
  };
  await waitFor(
    "smoke folder row to render",
    async () => {
      const p = await folderRowProbe();
      return { ok: p.row, ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const runFolderRemove = async () => {
    await runPalette("Remove folder");
    await waitFor(
      "palette folder choices (Smoke folder)",
      async () => {
        const text = String(
          (await evaluate(`(document.querySelector('[data-nav="palette-dialog"]') || {}).innerText || ""`)) || "",
        );
        return { ok: text.includes("Smoke folder"), text: text.slice(0, 240) };
      },
      { timeout: STEP_MS, interval: 100 },
    );
    await evaluate(`(() => {
      const input = document.querySelector('[data-nav="palette-dialog"] input');
      const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
      proto.set.call(input, "Smoke folder");
      input.dispatchEvent(new InputEvent("input", { bubbles: true, data: "Smoke folder" }));
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      return true;
    })()`);
  };
  await runFolderRemove();
  const folderDialog = await waitFor(
    "folder remove confirm dialog (sessions stay, grouping cannot be restored)",
    async () => {
      const raw = await evaluate(`(() => {
        const dialog = document.querySelector("[data-confirm='dialog']");
        return JSON.stringify({ ok: !!dialog, text: dialog ? dialog.innerText : "" });
      })()`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: p.ok && p.text.includes("Sessions stay") && p.text.includes("cannot be restored"),
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!folderDialog.text.includes("Remove folder")) {
    throw new Error(`folder confirm copy wrong: ${String(folderDialog.text).slice(0, 300)}`);
  }
  await evaluate(`document.querySelector("[data-confirm='cancel']").click()`);
  await waitFor(
    "folder confirm to close on cancel, folder intact",
    async () => {
      const p = await folderRowProbe();
      return {
        ok: p.row && (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === false,
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await runFolderRemove();
  await waitFor(
    "folder remove confirm dialog to reopen",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === true,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector("[data-confirm='accept']").click()`);
  await waitFor(
    "folder removed after confirm, sessions still listed",
    async () => {
      const p = await folderRowProbe();
      const body = (await evaluate(`document.body.innerText`)) || "";
      return { ok: !p.row && body.includes("Alpha session"), folder: p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const afterRemove = await call("GET", "/v1/session-list");
  const afterOrg = JSON.parse(afterRemove.text || "{}");
  const alphaOrg = (afterOrg.sessions || {})["alpha-session"];
  if (
    afterRemove.status !== 200 ||
    (afterOrg.folders || []).some((f) => f.id === smokeFolderId) ||
    (alphaOrg && alphaOrg.folderId)
  ) {
    throw new Error(`folder remove did not unfile alpha: ${afterRemove.status} ${afterRemove.text.slice(0, 300)}`);
  }
  const before = await send("Runtime.evaluate", {
    expression: `(() => {
      const panel = document.querySelector(".ss-panel");
      if (panel) panel.focus();
      const cur = document.querySelector(".ss-cursor");
      return cur ? cur.getAttribute("data-session-id") : "";
    })()`,
    returnByValue: true,
  });
  const beforeId = before.result?.result?.value || "";
  const key = async (type, keyName, code, vk) => {
    await send("Input.dispatchKeyEvent", {
      type,
      key: keyName,
      code,
      windowsVirtualKeyCode: vk,
      nativeVirtualKeyCode: vk,
    });
  };
  await key("keyDown", "j", "KeyJ", 74);
  await key("keyUp", "j", "KeyJ", 74);
  await delay(200);
  const after = await send("Runtime.evaluate", {
    expression: `document.querySelector(".ss-cursor")?.getAttribute("data-session-id") || ""`,
    returnByValue: true,
  });
  const afterId = after.result?.result?.value || "";
  if (!beforeId || !afterId || beforeId === afterId) {
    throw new Error(`keyboard j did not move the session cursor (${beforeId} -> ${afterId})`);
  }
  await key("keyDown", "x", "KeyX", 88);
  await key("keyUp", "x", "KeyX", 88);
  await delay(200);
  const selected = await send("Runtime.evaluate", {
    expression: `document.body.innerText.includes("1 selected") ? "yes" : "no"`,
    returnByValue: true,
  });
  if (selected.result?.result?.value !== "yes") {
    throw new Error("keyboard x did not select the cursor session");
  }
  // Session list actions (#1414/#1454/#1458): every session row carries a
  // Copy ID button (data-ss-copy-id = the row's session id) and an
  // Archive/Unarchive button, and the panel exposes the sort select.
  // Clicking Copy ID must produce honest feedback — "Session ID copied."
  // only when the browser accepted the write, otherwise the generic
  // manual-copy failure line; never silence.
  const rowActions = await send("Runtime.evaluate", {
    expression: `(() => {
      const sort = document.querySelector("[data-ss-sort]");
      const copy = document.querySelector("[data-ss-copy-id]");
      const arch = document.querySelector("[data-ss-archive-id]");
      if (!sort || !copy || !arch) return "missing";
      const row = copy.closest("[data-session-id]");
      if (!row || row.getAttribute("data-session-id") !== copy.getAttribute("data-ss-copy-id")) return "copy-hook-mismatch";
      copy.click();
      return "clicked";
    })()`,
    returnByValue: true,
  });
  if (rowActions.result?.result?.value !== "clicked") {
    throw new Error(`session row actions missing (${rowActions.result?.result?.value})`);
  }
  await waitFor(
    "copy ID feedback",
    textProbe((t) => t.includes("Session ID copied.") || t.includes("Copy didn't work in this browser.")),
    { timeout: WAIT_MS, interval: 250 },
  );
  // Needs you chip (#1451): the chip renders beside All/Unread, activates,
  // and filters the seeded (non-waiting) sessions out; All restores them.
  // Waiting-row rendering itself is covered by session_list unit tests —
  // the echo/smoke seeds never pend an approval or a question.
  const needsYou = await send("Runtime.evaluate", {
    expression: `(() => {
      const chips = [...document.querySelectorAll(".ss-chips .chip")].map((c) => c.textContent.trim());
      const chip = [...document.querySelectorAll(".ss-chips .chip")].find((c) => c.textContent.trim() === "Needs you");
      if (chip) chip.click();
      return JSON.stringify({ chips, clicked: !!chip });
    })()`,
    returnByValue: true,
  });
  const needsYouInfo = JSON.parse(needsYou.result?.result?.value || "{}");
  if (!needsYouInfo.clicked || needsYouInfo.chips?.join(",") !== "All,Unread,Needs you") {
    throw new Error(`Needs you chip missing or out of order: ${JSON.stringify(needsYouInfo)}`);
  }
  await waitFor(
    "Needs you filter empties the non-waiting list",
    textProbe((t) => t.includes("No sessions match")),
    { timeout: WAIT_MS, interval: 250 },
  );
  const needsYouActive = await evalValue(
    `document.querySelector(".ss-chips .chip.on")?.textContent.trim() || ""`,
  );
  if (needsYouActive !== "Needs you") {
    throw new Error(`Needs you chip did not stay active (on=${needsYouActive})`);
  }
  await send("Runtime.evaluate", {
    expression: `[...document.querySelectorAll(".ss-chips .chip")].find((c) => c.textContent.trim() === "All")?.click()`,
  });
  await waitFor(
    "All filter restores sessions after Needs you",
    textProbe((t) => t.includes("Palette polish") && t.includes("Alpha session")),
    { timeout: WAIT_MS, interval: 250 },
  );
  // feature:navbar — palette fuzzy match, argument error, shortcut overlay.
  const navbar = async (expression) => {
    try {
      return await evaluate(expression);
    } catch (e) {
      throw new Error(`navbar check exception: ${String(e?.message || e).slice(0, 500)}`);
    }
  };
  await navbar(`document.querySelector('[data-nav="palette"]').click()`);
  await waitFor(
    "command palette to open",
    textProbe((t) => t.includes("Open session") && t.includes("New session")),
    { timeout: STEP_MS, interval: 100 },
  );
  const palette = await navbar(`document.body.innerText`);
  if (!palette.includes("Open session") || !palette.includes("Alt+Left")) {
    throw new Error(`palette did not open text=${palette.slice(0, 500)}`);
  }
  await navbar(`(() => {
    const input = document.querySelector('[data-nav="palette-dialog"] input');
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(input, "nord");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    return true;
  })()`);
  const filtered = await waitFor(
    "palette fuzzy filter to match Theme: Nord only",
    async () => {
      const t = (await navbar(`(document.querySelector('[data-nav="palette-dialog"]') || {}).innerText || ""`)) || "";
      return { ok: t.includes("Theme: Nord") && !t.includes("New session"), text: t };
    },
    { timeout: STEP_MS, interval: 100 },
  );
  if (!filtered.text.includes("Theme: Nord") || filtered.text.includes("New session")) {
    throw new Error(`palette fuzzy failed text=${filtered.text.slice(0, 500)}`);
  }
  await navbar(`(() => {
    const input = document.querySelector('[data-nav="palette-dialog"] input');
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(input, "open session");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    return true;
  })()`);
  await delay(50);
  const typed = await navbar(`(() => {
    const input = document.querySelector('[data-nav="palette-dialog"] input');
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(input, "zzzz-no-such-session");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    return input ? input.value : "";
  })()`);
  const alertProbe = await waitFor(
    "palette no-match alert",
    async () => {
      const t = (await navbar(`(document.querySelector('[role="alert"]') || {}).innerText || ""`)) || "";
      return { ok: String(t).includes("Nothing matches"), text: String(t) };
    },
    { timeout: STEP_MS, interval: 100 },
  );
  if (!String(alertProbe.text).includes("Nothing matches")) {
    throw new Error(`palette error path failed alert=${alertProbe.text} typed=${typed}`);
  }
  await navbar(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await navbar(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await navbar(`(() => { if (document.activeElement && document.activeElement.blur) document.activeElement.blur(); return true; })()`);
  await navbar(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "?", bubbles: true }))`);
  const help = await waitFor(
    "shortcut overlay to open",
    async () => {
      const t = (await navbar(`(document.querySelector('[data-nav="help"]') || {}).innerText || ""`)) || "";
      return {
        ok: String(t).includes("Alt+Left") && String(t).includes("Focus mode"),
        text: String(t),
      };
    },
    { timeout: STEP_MS, interval: 100 },
  );
  if (!String(help.text).includes("Focus mode")) {
    throw new Error(`shortcut overlay failed text=${help.text}`);
  }
  const crumbs = await navbar(`(document.querySelector('[data-nav="crumbs"]') || {}).innerText || ""`);
  if (!String(crumbs).includes("Strike") || !String(crumbs).includes(">")) {
    throw new Error(`breadcrumb missing text=${crumbs}`);
  }
  const waitUntil = async (expression, label) => {
    let last = "";
    for (let i = 0; i < 40; i++) {
      last = await evaluate(expression);
      if (last === true) return;
      await delay(250);
    }
    throw new Error(`${label}\nlast=${String(last).slice(0, 500)}`);
  };

  await waitUntil(
    `document.body.innerText.includes("No telemetry") && document.body.innerText.includes(".strike") && document.querySelector('[data-action="onboarding-continue"]') && !document.querySelector('[data-action="onboarding-continue"]').disabled`,
    "onboarding file list",
  );
  await evaluate(`document.querySelector('[data-action="onboarding-continue"]').click()`);
  await waitUntil(
    `document.querySelector('[data-action="telemetry-note"]') == null`,
    "onboarding did not dismiss",
  );
  await evaluate(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await evaluate(`document.querySelector('[data-action="open-settings"]').click()`);
  await waitUntil(
    `document.body.innerText.includes("Appearance") && document.body.innerText.includes("Type size") && document.body.innerText.includes("Density") && !!document.querySelector('input[data-choice="lg"]')`,
    "settings page",
  );
  // #1489: Download diagnostics lives in Settings, enabled on a live
  // (echo) host, and a click produces the success confirmation. The
  // server-chosen filename is covered by the UAT download capture.
  await waitUntil(
    `!!document.querySelector('[data-action="download-diagnostics"]') && !document.querySelector('[data-action="download-diagnostics"]').disabled`,
    "diag download control enabled on a live host",
  );
  await evaluate(`document.querySelector('[data-action="download-diagnostics"]').click()`);
  await waitUntil(
    `document.body.innerText.includes("diagnostic bundle downloaded")`,
    "diag download success confirmation",
  );
  // #1493: Config sources lists the daemon's GET /v1/config-sources rows in
  // daemon order, read-only. Global rows do not depend on the session root,
  // so every one must render, in the same order, with the same exists flag.
  {
    const res = await fetch(`http://127.0.0.1:${port}/v1/config-sources`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (res.status !== 200) throw new Error(`config-sources HTTP ${res.status}`);
    const wire = (await res.json()).sources || [];
    const globals = wire.filter((s) => s.scope === "global");
    const main = globals.find((s) => s.slot === "config");
    if (!main || main.exists !== true) {
      throw new Error(`smoke seeds ~/.strike/config, so the global main config must exist: ${JSON.stringify(wire).slice(0, 600)}`);
    }
    const want = JSON.stringify(globals.map((s) => [s.display, String(!!s.exists)]));
    await waitUntil(
      `(() => {
        const want = ${JSON.stringify(want)};
        const rows = [...document.querySelectorAll('[data-config-sources] li.config-source')]
          .map((li) => [li.getAttribute("data-config-source"), li.getAttribute("data-exists")]);
        const globals = JSON.parse(want);
        let at = 0;
        for (const g of globals) {
          const i = rows.findIndex((r, k) => k >= at && r[0] === g[0] && r[1] === g[1]);
          if (i < 0) return JSON.stringify({ missing: g, rows });
          at = i + 1;
        }
        const text = document.querySelector("[data-config-sources]")?.innerText || "";
        return text.includes("Main config") && text.includes("Global") && !document.querySelector("[data-config-sources-unavailable]");
      })()`,
      "config sources render the daemon's global rows in order",
    );
  }
  // #1339: Settings > Backup. Export downloads the archive; restore reads
  // a chosen file and previews the plan without writing anything.
  await waitUntil(
    `!!document.querySelector('[data-action="backup-export"]') && !document.querySelector('[data-action="backup-export"]').disabled`,
    "backup export control",
  );
  await evaluate(`document.querySelector('[data-action="backup-export"]').click()`);
  await waitUntil(
    `!!document.querySelector('[data-backup-export-status]') && document.body.innerText.includes("Downloaded strike-backup.json")`,
    "backup export confirmation",
  );
  await evaluate(`(() => {
    const input = document.querySelector('[data-action="backup-restore-file"]');
    const dt = new DataTransfer();
    dt.items.add(new File([${JSON.stringify(backupText)}], "strike-backup.json", { type: "application/json" }));
    input.files = dt.files;
    input.dispatchEvent(new Event("change", { bubbles: true }));
    return true;
  })()`);
  await waitUntil(
    `!!document.querySelector('[data-action="backup-restore-preview"]') && !document.querySelector('[data-action="backup-restore-preview"]').disabled`,
    "backup restore file read",
  );
  await evaluate(`document.querySelector('[data-action="backup-restore-preview"]').click()`);
  await waitUntil(
    `!!document.querySelector('[data-backup-report="preview"]') && document.body.innerText.includes("Restore preview") && document.body.innerText.includes("memory:")`,
    "backup restore preview",
  );
  await evaluate(`document.querySelector('input[data-choice="lg"]').click()`);
  await evaluate(`document.querySelector('input[data-choice="compact"]').click()`);
  await waitUntil(
    `document.documentElement.getAttribute("data-font") === "lg" && document.documentElement.getAttribute("data-density") === "compact" && localStorage.getItem("strike.font") === "lg"`,
    "font and density",
  );

  // #1583: Settings → Updates shows the effective autoupdate dial, `off`
  // persists and the route answers "off" (no probe), and the row never
  // claims "up to date" without a successful check.
  await waitUntil(
    `(() => {
      const sec = document.querySelector('[data-section="updates"]');
      const notify = sec?.querySelector('input[data-autoupdate="notify"]');
      return !!notify && notify.checked && !notify.disabled && !!sec.querySelector(".update-status")?.textContent;
    })()`,
    "updates section shows the effective notify dial",
  );
  const updateRoute = async () => {
    const res = await fetch(`http://127.0.0.1:${port}/v1/update`, { headers: { Authorization: `Bearer ${token}` } });
    return { status: res.status, body: res.status === 200 ? await res.json() : null };
  };
  const updateCap = (await updateRoute()).status === 200;
  await evaluate(`document.querySelector('input[data-autoupdate="off"]').click()`);
  await waitUntil(
    `(() => {
      const off = document.querySelector('input[data-autoupdate="off"]');
      const line = document.querySelector('[data-section="updates"] .update-status')?.textContent || "";
      return off.checked && !off.disabled && (${updateCap} ? line.includes("Checks are off") : true);
    })()`,
    "autoupdate off saved",
  );
  {
    const settings = await (await fetch(`http://127.0.0.1:${port}/v1/settings`, { headers: { Authorization: `Bearer ${token}` } })).json();
    if (settings.autoupdateEffective !== "off") {
      throw new Error(`autoupdate off did not persist: ${JSON.stringify(settings.autoupdateEffective)}`);
    }
    if (updateCap) {
      const r = await updateRoute();
      if (r.body?.state !== "off") throw new Error(`update route with dial off: ${JSON.stringify(r)}`);
    }
  }
  await evaluate(`document.querySelector('input[data-autoupdate="notify"]').click()`);
  await waitUntil(
    `(() => {
      const n = document.querySelector('input[data-autoupdate="notify"]');
      const line = document.querySelector('[data-section="updates"] .update-status')?.textContent || "";
      return n.checked && !n.disabled && !line.includes("Checks are off");
    })()`,
    "autoupdate back to notify",
  );

  await waitUntil(`(() => {
    if (document.querySelector(".gw-pop")) return true;
    if (!document.querySelector('[data-nav="palette-dialog"]')) {
      document.querySelector('[data-nav="palette"]')?.click();
      return false;
    }
    document.querySelector('[data-command-id="gateways.open"]')?.click();
    return false;
  })()`, "gateways panel for a bad base");
  await evaluate(`(() => {
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    const name = document.querySelector('.gw-pop input[aria-label="Gateway name"]');
    const url = document.querySelector('.gw-pop input[aria-label="Gateway base URL"]');
    set.call(name, "bad");
    name.dispatchEvent(new Event("input", { bubbles: true }));
    set.call(url, "http://127.0.0.1:1");
    url.dispatchEvent(new Event("input", { bubbles: true }));
    [...document.querySelectorAll(".gw-pop button")].find((b) => b.textContent === "Save").click();
    return true;
  })()`);
  // #1539: the saved profile shows up as a read-only peek group in the
  // Sessions list, under a Local label, badged from a real probe — here
  // unreachable, never a fake count. The Sessions rail lives in the
  // chat view (settings was open above), so go there first; mounting
  // the list is itself one of the peek's refresh triggers.
  await evaluate(`document.querySelector('[data-view="chat"]')?.click()`);
  await waitUntil(`!!document.querySelector(".ss-panel")`, "sessions list for the peek");
  await waitUntil(`(() => {
    const group = [...document.querySelectorAll(".ss-peek [data-peek-gateway]")]
      .find((el) => (el.textContent || "").includes("bad"));
    if (!group) return false;
    return group.getAttribute("data-peek-state") === "unreachable"
      && (group.textContent || "").includes("Unreachable")
      && !(group.textContent || "").includes(" running · ")
      && !!document.querySelector(".ss-peek-local");
  })()`, "cross-gateway peek group for the saved bad gateway");
  // Manual refresh re-probes and lands on the same honest badge.
  await evaluate(`document.querySelector('[data-action="peek-refresh"]')?.click()`);
  await waitUntil(`(() => {
    const group = [...document.querySelectorAll(".ss-peek [data-peek-gateway]")]
      .find((el) => (el.textContent || "").includes("bad"));
    return !!group && group.getAttribute("data-peek-state") === "unreachable";
  })()`, "peek refresh keeps the unreachable badge");
  await waitUntil(`(() => {
    const hit = [...document.querySelectorAll("[data-command-id]")].some((el) => (el.textContent || "").includes("Switch to gateway: bad"));
    if (hit) return true;
    if (!document.querySelector('[data-nav="palette-dialog"]')) {
      document.querySelector('[data-nav="help"]')?.closest(".palette-backdrop")?.click();
      document.querySelector('[data-nav="palette"]')?.click();
    }
    return false;
  })()`, "bad gateway command");
  await evaluate(`[...document.querySelectorAll("[data-command-id]")].find((el) => (el.textContent || "").includes("Switch to gateway: bad"))?.click()`);
  // #1523: switching to an unreachable gateway is probe-first — the
  // switch is refused, the form shows the error, and the working
  // gateway stays Connected instead of being stranded as Unreachable.
  await waitUntil(`(() => {
    const pop = document.querySelector(".gw-pop");
    if (!pop) return false;
    const note = pop.querySelector(".gw-form-note");
    // The panel detail line is the working gateway's live describe
    // (host · loopback · auth on · host token · grants); the header
    // gateway chip is gone (#1590 A6), so connected-ness is read here:
    // the working gateway's detail never flips to "Could not reach
    // this daemon".
    const txt = pop.innerText || "";
    return !!note && (note.textContent || "").includes("Could not reach")
      && txt.includes("loopback") && !txt.includes("Could not reach this daemon");
  })()`, "guarded switch keeps the working gateway connected with a form error");
  await evaluate(`document.querySelector(".gw-back")?.click()`);
  await evaluate(`document.querySelector('[data-view="chat"]')?.click()`);
  // The settings error path below still needs a genuinely unreachable
  // base: persist one directly (the pre-#1523 stranded state a user
  // could be left in) and reload into it.
  await evaluate(`(() => { localStorage.setItem("strike.base", "http://127.0.0.1:1"); location.reload(); return true; })()`);
  // The gateway status chip is no longer rendered in the header (#1590 A6):
  // gateway health lives in the Gateways panel, so read "Unreachable" there.
  await waitUntil(`(() => {
    if (document.querySelector(".gw-pop")) return true;
    if (!document.querySelector('[data-nav="palette-dialog"]')) {
      document.querySelector('[data-nav="palette"]')?.click();
      return false;
    }
    document.querySelector('[data-command-id="gateways.open"]')?.click();
    return false;
  })()`, "gateways panel for bad gateway status");
  await waitUntil(`document.body.innerText.includes("Could not reach this daemon")`, "bad gateway is unreachable");
  await evaluate(`document.querySelector('[data-action="open-settings"]').click()`);
  await runPalette("Data locations");
  await waitUntil(
    `document.body.innerText.includes("Could not read the file list") && document.querySelector('[data-action="reload-files"]') != null`,
    "settings error path",
  );
  await evaluate(`document.querySelector('[data-action="reload-files"]').click()`);
  await waitUntil(
    `document.body.innerText.includes("Could not read the file list")`,
    "retry while the daemon URL is still wrong",
  );
  await waitUntil(`(() => {
    if ([...document.querySelectorAll("[data-command-id]")].some((el) => (el.textContent || "").includes("Switch to gateway: This gateway"))) return true;
    if (!document.querySelector('[data-nav="palette-dialog"]')) {
      document.querySelector('[data-nav="palette"]')?.click();
    }
    return false;
  })()`, "this gateway command");
  await evaluate(`[...document.querySelectorAll("[data-command-id]")].find((el) => (el.textContent || "").includes("Switch to gateway: This gateway"))?.click()`);
  await evaluate(`document.querySelector('[data-view="chat"]')?.click()`);
  // "Connected" was the header gateway chip / version text, both removed by
  // #1590 (A6/A7): read it from the Gateways panel detail instead.
  await waitUntil(`(() => {
    if (document.querySelector(".gw-pop")) return true;
    if (!document.querySelector('[data-nav="palette-dialog"]')) {
      document.querySelector('[data-nav="palette"]')?.click();
      return false;
    }
    document.querySelector('[data-command-id="gateways.open"]')?.click();
    return false;
  })()`, "gateways panel for reconnected status");
  await waitUntil(`(() => { const pop = document.querySelector(".gw-pop"); return !!pop && pop.innerText.includes("loopback") && !pop.innerText.includes("Could not reach this daemon"); })()`, "this gateway is connected");
  await evaluate(`document.querySelector('[data-action="open-settings"]').click()`);
  await runPalette("Data locations");
  await waitUntil(
    `document.body.innerText.includes(".strike") && !document.body.innerText.includes("Could not read the file list") && !!document.querySelector('input[data-choice="lg"]')`,
    "settings recovered",
  );
  await editorBrowserCheck({
    send,
    port,
    token,
    repoDir: process.cwd(),
  });
  await agentPickerCheck({ send });
  await mdReviewBrowserCheck({
    send,
    port,
    token,
    repoDir: process.cwd(),
  });

  const beforeSelection = await send("Runtime.evaluate", {
    expression: `(() => {
      const shell = document.querySelector(".shell");
      return JSON.stringify({
        layout: document.documentElement.getAttribute("data-layout") || "",
        cls: shell ? shell.className : "",
        attach: !!document.querySelector("[data-chat-attach]"),
      });
    })()`,
    returnByValue: true,
  });
  const beforeState = JSON.parse(beforeSelection.result?.result?.value || "{}");
  if (beforeState.layout === "conversation" || String(beforeState.cls).includes("layout-conversation")) {
    throw new Error(`cockpit is not the default layout: ${JSON.stringify(beforeState)}`);
  }
  if (!beforeState.attach) throw new Error("attach control missing in cockpit");

  await send("Runtime.evaluate", {
    expression: `document.querySelector('[data-layout="conversation"]')?.click()`,
  });
  let chat = {};
  for (let i = 0; i < 20; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `(() => {
        const shell = document.querySelector(".shell");
        const bar = document.querySelector(".chat-mode-bar");
        const shown = bar ? getComputedStyle(bar).display : "";
        return JSON.stringify({
          cls: shell ? shell.className : "",
          bar: shown,
          sessions: !!document.querySelector("[data-chat-sessions]"),
          panel: !!document.querySelector("[data-chat-panel]"),
        });
      })()`,
      returnByValue: true,
    });
    chat = JSON.parse(ev.result?.result?.value || "{}");
    if (String(chat.cls).includes("layout-conversation") && chat.bar === "flex") break;
    await delay(100);
  }
  if (!String(chat.cls).includes("layout-conversation") || chat.bar !== "flex" || !chat.sessions || !chat.panel) {
    throw new Error(`conversation layout failed: ${JSON.stringify(chat)}`);
  }
  // Drawer content must lay out at full width, not crushed into a flex row:
  // assert a content element inside each open drawer is >200px wide (the
  // drawer class alone passed while the sessions card rendered ~35px wide).
  const drawerMeasure = async (drawerClass, drawerSel, contentSel, fallbackSel) => {
    let measured = {};
    for (let i = 0; i < 20; i++) {
      const ev = await send("Runtime.evaluate", {
        expression: `(() => {
          const shell = document.querySelector(".shell");
          const drawer = document.querySelector(${JSON.stringify(drawerSel)});
          const content = drawer ? drawer.querySelector(${JSON.stringify(contentSel)}) || drawer.querySelector(${JSON.stringify(fallbackSel)}) : null;
          return JSON.stringify({
            cls: shell ? shell.className : "",
            direction: drawer ? getComputedStyle(drawer).flexDirection : "",
            drawerWidth: drawer ? drawer.getBoundingClientRect().width : 0,
            contentWidth: content ? content.getBoundingClientRect().width : 0,
          });
        })()`,
        returnByValue: true,
      });
      measured = JSON.parse(ev.result?.result?.value || "{}");
      if (
        String(measured.cls).includes(drawerClass) &&
        measured.direction === "column" &&
        measured.drawerWidth > 200 &&
        measured.contentWidth > 200
      ) {
        break;
      }
      await delay(100);
    }
    if (
      !String(measured.cls).includes(drawerClass) ||
      measured.direction !== "column" ||
      measured.drawerWidth <= 200 ||
      measured.contentWidth <= 200
    ) {
      throw new Error(`conversation drawer content crushed: ${JSON.stringify(measured)}`);
    }
    return measured;
  };
  await send("Runtime.evaluate", {
    expression: `document.querySelector("[data-chat-sessions]")?.click()`,
  });
  await drawerMeasure("drawer-sessions", ".shell.drawer-sessions .rail", ".session-row", ".section-head");
  await send("Runtime.evaluate", {
    expression: `document.querySelector("[data-chat-panel]")?.click()`,
  });
  await drawerMeasure("drawer-panel", ".shell.drawer-panel .side", ".side-tabs", ".activity");
  await send("Runtime.evaluate", {
    expression: `document.querySelector("[data-chat-panel]")?.click()`,
  });
  await send("Runtime.evaluate", {
    expression: `document.querySelector('[data-layout="cockpit"]')?.click()`,
  });

  const png = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==";
  const secretName = "notes-sk-abcdefghijklmnopqrstuvwxyz.png";
  const uploaded = await fetch(`http://127.0.0.1:${port}/v1/attachments`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ name: secretName, mime: "image/png", data: png }),
  });
  const uploadedBody = await uploaded.text();
  if (uploaded.status !== 200 || uploadedBody.includes("sk-abcdefghijklmnopqrstuvwxyz") || !uploadedBody.includes("[REDACTED_API_KEY]")) {
    throw new Error(`attachment ingest failed ${uploaded.status} ${uploadedBody.slice(0, 300)}`);
  }
  const rejected = await fetch(`http://127.0.0.1:${port}/v1/attachments`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ data: "" }),
  });
  if (rejected.status !== 400) {
    throw new Error(`empty attachment status ${rejected.status}`);
  }
  // Sent attachments in the transcript (#1389): GET serves the stored
  // blob with its stored MIME, and a user.input carrying an image + PDF
  // renders both chips under the "you" bubble after a reload, with the
  // image thumbnail loaded from the GET route.
  const pngRef = JSON.parse(uploadedBody).ref;
  const pdfB64 = Buffer.from("%PDF-1.4\n1 0 obj\n<<>>\nendobj\ntrailer\n<<>>\n%%EOF\n").toString("base64");
  const pdfUp = await fetch(`http://127.0.0.1:${port}/v1/attachments`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({ name: "smoke-spec.pdf", mime: "application/pdf", data: pdfB64 }),
  });
  const pdfBody = await pdfUp.text();
  if (pdfUp.status !== 200) throw new Error(`pdf ingest failed ${pdfUp.status} ${pdfBody.slice(0, 300)}`);
  const pdfRef = JSON.parse(pdfBody).ref;
  const gotPng = await fetch(`http://127.0.0.1:${port}/v1/attachments/${encodeURIComponent(pngRef)}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (gotPng.status !== 200 || gotPng.headers.get("content-type") !== "image/png") {
    throw new Error(`attachment GET ${gotPng.status} ${gotPng.headers.get("content-type")}`);
  }
  const noAuth = await fetch(`http://127.0.0.1:${port}/v1/attachments/${encodeURIComponent(pngRef)}`);
  if (noAuth.status !== 401) throw new Error(`attachment GET without token = ${noAuth.status}`);
  const missingAtt = await fetch(`http://127.0.0.1:${port}/v1/attachments/att%3Asha256%3A${"0".repeat(64)}`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (missingAtt.status !== 404) throw new Error(`attachment GET unknown ref = ${missingAtt.status}`);
  const sentWithFiles = await fetch(`http://127.0.0.1:${port}/v1/ops`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      type: "user.input",
      data: {
        text: "transcript attachments smoke 1389",
        images: [
          { ref: pngRef, mime: "image/png", name: "smoke-shot.png" },
          { ref: pdfRef, mime: "application/pdf", name: "smoke-spec.pdf" },
        ],
      },
    }),
  });
  if (!sentWithFiles.ok) throw new Error(`user.input with attachments ${sentWithFiles.status} ${await sentWithFiles.text()}`);
  // Attachment honesty (#1561): on the echo provider the engine drops
  // every attachment, so the composer must say so before send (warn,
  // not block). Removing the file clears the warning.
  await evaluate(`(() => {
    const input = document.querySelector('input[data-chat-attach="true"]');
    const bytes = Uint8Array.from(atob(${JSON.stringify(png)}), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], "honesty-shot.png", { type: "image/png" }));
    input.files = dt.files;
    input.dispatchEvent(new Event("change", { bubbles: true }));
    return true;
  })()`);
  await waitUntil(
    `(() => { const w = document.querySelector("[data-attach-warning]"); return !!w && w.offsetParent !== null && w.innerText.includes("honesty-shot.png") && w.innerText.includes("echo provider does not accept attachments"); })()`,
    "composer warns that echo drops the attachment",
  );
  await evaluate(`(() => { const b = [...document.querySelectorAll('.attach-chip button[aria-label="Remove attachment"]')].find((x) => x.closest(".attach-chip")?.innerText.includes("honesty-shot.png")); b?.click(); return !!b; })()`);
  await waitUntil(`!document.querySelector("[data-attach-warning]")`, "attachment warning clears with the file");
  // Office + media classification (#1390): a docx with its real browser
  // MIME is accepted as a document, an mp4 header as video, a plain zip
  // stays an archive, and text renamed to .docx is rejected honestly.
  const crc32 = (buf) => {
    let c = ~0;
    for (const b of buf) {
      c ^= b;
      for (let k = 0; k < 8; k++) c = (c >>> 1) ^ (0xedb88320 & -(c & 1));
    }
    return ~c >>> 0;
  };
  const zipStored = (entries) => {
    const locals = [];
    const centrals = [];
    let offset = 0;
    for (const [name, body] of entries) {
      const nameBuf = Buffer.from(name);
      const data = Buffer.from(body);
      const crc = crc32(data);
      const local = Buffer.alloc(30);
      local.writeUInt32LE(0x04034b50, 0);
      local.writeUInt16LE(20, 4);
      local.writeUInt32LE(crc, 14);
      local.writeUInt32LE(data.length, 18);
      local.writeUInt32LE(data.length, 22);
      local.writeUInt16LE(nameBuf.length, 26);
      locals.push(local, nameBuf, data);
      const central = Buffer.alloc(46);
      central.writeUInt32LE(0x02014b50, 0);
      central.writeUInt16LE(20, 4);
      central.writeUInt16LE(20, 6);
      central.writeUInt32LE(crc, 16);
      central.writeUInt32LE(data.length, 20);
      central.writeUInt32LE(data.length, 24);
      central.writeUInt16LE(nameBuf.length, 28);
      central.writeUInt32LE(offset, 42);
      centrals.push(central, nameBuf);
      offset += 30 + nameBuf.length + data.length;
    }
    const cd = Buffer.concat(centrals);
    const end = Buffer.alloc(22);
    end.writeUInt32LE(0x06054b50, 0);
    end.writeUInt16LE(entries.length, 8);
    end.writeUInt16LE(entries.length, 10);
    end.writeUInt32LE(cd.length, 12);
    end.writeUInt32LE(offset, 16);
    return Buffer.concat([...locals, cd, end]);
  };
  const docxMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document";
  const ingest = async (name, mime, buf) => {
    const res = await fetch(`http://127.0.0.1:${port}/v1/attachments`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ name, mime, data: Buffer.from(buf).toString("base64") }),
    });
    return { status: res.status, body: await res.text() };
  };
  const docxFixture = zipStored([["[Content_Types].xml", "<Types/>"], ["word/document.xml", "<w:document/>"]]);
  const mp4Fixture = Buffer.concat([Buffer.from("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00", "latin1"), Buffer.alloc(32)]);
  const classified = [
    ["smoke.docx", docxMIME, docxFixture, 200, "document"],
    ["smoke.docx", "", docxFixture, 200, "document"],
    ["smoke.zip", "application/zip", zipStored([["README.md", "hi"]]), 200, "archive"],
    ["smoke.mp4", "video/mp4", mp4Fixture, 200, "video"],
    ["fake.docx", docxMIME, Buffer.from("just text, not a document\n"), 400, ""],
  ];
  for (const [name, mime, buf, wantStatus, wantKind] of classified) {
    const got = await ingest(name, mime, buf);
    const kind = got.status === 200 ? JSON.parse(got.body).kind : "";
    if (got.status !== wantStatus || kind !== wantKind) {
      throw new Error(`classify ${name} (${mime || "no mime"}): ${got.status} kind=${kind} body=${got.body.slice(0, 200)}`);
    }
  }
  // Composer: an mp4 chip gets a <video controls> preview; removing it clears it.
  await evaluate(`(() => {
    const input = document.querySelector('input[data-chat-attach="true"]');
    const bytes = Uint8Array.from(atob(${JSON.stringify(mp4Fixture.toString("base64"))}), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], "smoke-clip.mp4", { type: "video/mp4" }));
    input.files = dt.files;
    input.dispatchEvent(new Event("change", { bubbles: true }));
    return true;
  })()`);
  await waitUntil(
    `(() => { const chip = [...document.querySelectorAll(".attach-chip")].find((c) => c.innerText.includes("smoke-clip.mp4")); const v = chip && chip.querySelector("video.attach-media"); return !!v && v.controls && v.src.startsWith("blob:"); })()`,
    "mp4 composer chip shows video controls",
  );
  await evaluate(`(() => { const chip = [...document.querySelectorAll(".attach-chip")].find((c) => c.innerText.includes("smoke-clip.mp4")); chip?.querySelector('button[aria-label="Remove attachment"]')?.click(); return !!chip; })()`);
  await waitUntil(`![...document.querySelectorAll(".attach-chip")].some((c) => c.innerText.includes("smoke-clip.mp4"))`, "mp4 chip removed");
  // Visual annotate (#1545): attach an image, drop a pin with a note,
  // send, and the single turn carries the coordinate block; removing an
  // image with unsent marks asks first.
  const attachPng = (name) => evaluate(`(() => {
    const input = document.querySelector('input[data-chat-attach="true"]');
    const bytes = Uint8Array.from(atob(${JSON.stringify(png)}), (c) => c.charCodeAt(0));
    const dt = new DataTransfer();
    dt.items.add(new File([bytes], ${JSON.stringify(name)}, { type: "image/png" }));
    input.files = dt.files;
    input.dispatchEvent(new Event("change", { bubbles: true }));
    return true;
  })()`);
  const chipFor = (name) => `[...document.querySelectorAll(".attach-chip")].find((c) => c.innerText.includes(${JSON.stringify(name)}))`;
  const placePin = async (name, note) => {
    await waitUntil(`!!(${chipFor(name)})?.querySelector('[data-annotate-open]')`, `annotate action on ${name}`);
    await evaluate(`(${chipFor(name)}).querySelector('[data-annotate-open]').click()`);
    await waitUntil(`(() => { const img = document.querySelector('[data-annotate] .annotate-canvas img'); return !!img && img.complete && img.getBoundingClientRect().width > 0; })()`, "annotate overlay image");
    await evaluate(`(() => {
      const canvas = document.querySelector('[data-annotate] .annotate-canvas');
      const r = canvas.querySelector('img').getBoundingClientRect();
      const at = { clientX: r.left + r.width / 2, clientY: r.top + r.height / 2, button: 0, pointerId: 7, bubbles: true, isPrimary: true };
      canvas.dispatchEvent(new PointerEvent("pointerdown", at));
      canvas.dispatchEvent(new PointerEvent("pointerup", at));
      return true;
    })()`);
    await waitUntil(`document.querySelectorAll('[data-annotate-marks] li').length === 1 && document.querySelector('[data-annotate-marks]').innerText.includes("[pin x=50%, y=50%]")`, "pin listed with coordinates");
    await evaluate(`(() => {
      const input = document.querySelector('[data-annotate-marks] input');
      input.value = ${JSON.stringify(note)};
      input.dispatchEvent(new Event("change", { bubbles: true }));
      document.querySelector('[data-annotate-done]').click();
      return true;
    })()`);
    await waitUntil(`!document.querySelector('[data-annotate]') && (${chipFor(name)})?.querySelector('[data-attach-marks]')?.innerText === "1 mark"`, "overlay closed and chip shows 1 mark");
  };
  await attachPng("annotate-discard.png");
  await placePin("annotate-discard.png", "throwaway");
  await evaluate(`(${chipFor("annotate-discard.png")}).querySelector('button[aria-label="Remove attachment"]').click()`);
  await waitUntil(`(() => { const d = document.querySelector(".confirm-dialog"); return !!d && d.innerText.includes("1 mark"); })()`, "removing an annotated image asks first");
  await evaluate(`document.querySelector('[data-confirm="accept"]').click()`);
  await waitUntil(`!(${chipFor("annotate-discard.png")})`, "annotated image removed after explicit yes");
  await attachPng("annotate-shot.png");
  await placePin("annotate-shot.png", "move this button");
  await evaluate(`(() => {
    const ta = document.getElementById("composer-input");
    const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
    proto.set.call(ta, "annotate smoke 1545");
    ta.dispatchEvent(new Event("input", { bubbles: true }));
    document.getElementById("send-primary").click();
    return true;
  })()`);
  await waitUntil(
    `(() => {
      const draft = document.getElementById("composer-input")?.value || "";
      const chips = document.querySelectorAll(".attach-chip").length;
      if (draft === "" && chips === 0) return true;
      return JSON.stringify({ draft, chips, notes: [...document.querySelectorAll(".hint, .queue-note, [role=status]")].map((n) => n.innerText).filter(Boolean).slice(0, 8) });
    })()`,
    "annotated send accepted (composer cleared)",
  );
  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
  });
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitUntil(
    // Right after Page.navigate the new document may not have a body yet;
    // read it as empty so the wait retries instead of throwing.
    `(() => {
      const t = document.body?.innerText ?? "";
      const sent = [...document.querySelectorAll("[data-sent-attachments]")].some((el) => el.innerText.includes("annotate-shot-annotated.png"));
      if (sent && t.includes("annotate smoke 1545") && t.includes("Annotations on annotate-shot.png") && t.includes('[pin x=50%, y=50%] "move this button"')) return true;
      const msgs = [...document.querySelectorAll(".sx-msg")].slice(-3).map((m) => m.innerText.slice(0, 300));
      return JSON.stringify({ sent, msgs });
    })()`,
    "sent turn carries the composite and the annotation block after reload",
  );
  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
  });
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "cockpit reload after attachment checks (Run a command, Terminal)",
    textProbe((t) => t.includes("Run a command") && t.includes("Terminal")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await waitUntil(
    `(() => {
      const box = [...document.querySelectorAll("[data-sent-attachments]")].find((el) => el.innerText.includes("smoke-shot.png"));
      if (!box) return false;
      const img = box.querySelector(".sx-attach-chip img");
      return box.innerText.includes("smoke-spec.pdf") && !!img && img.src.startsWith("blob:") && img.complete && img.naturalWidth > 0;
    })()`,
    "sent image + PDF chips render after reload with a loaded thumbnail",
  );

  // Usage + Audit (#1286): open each view and confirm its controls
  // render; the Audit view must also report a chain state. Ported onto
  // the hardened script's helpers: evaluate() surfaces page exceptions,
  // and waitFor()/textProbe() poll on condition with diagnostics on
  // timeout instead of the old fixed-iteration body scrape.
  const openView = async (label, needles) => {
    await runPalette(label);
    await waitFor(
      `${label} view (${needles.join(", ")})`,
      textProbe((t) => needles.every((s) => t.includes(s))),
      { timeout: WAIT_MS, interval: 250 },
    );
  };
  // Usage export (#1481): the client-side Export controls render beside
  // the report even before any usage is seeded.
  await openView("Usage", ["Input tokens", "Daily budget (USD)", "Export JSON", "Export CSV (by day)"]);
  // Usage projection (#1388): the smoke daemon has no multi-day priced
  // history, so no estimate may render — only the absent note, if any.
  const projectionLevel = await evaluate(
    `(document.querySelector("[data-usage-projection]") || {}).dataset?.usageProjection || ""`,
  );
  if (projectionLevel !== "" && projectionLevel !== "absent") {
    throw new Error(`usage projection rendered without priced history: ${projectionLevel}`);
  }
  // Audit window (#1440): seed a chain longer than the 100-row window
  // before opening Audit, so the first render is already capped.
  const auditSeed = 105;
  let auditSeeded = 0;
  for (let i = 0; i < auditSeed; i++) {
    const res = await fetch(`http://127.0.0.1:${port}/v1/audit`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ action: "terminal", summary: `smoke audit window ${i}` }),
    });
    if (!res.ok) break;
    auditSeeded++;
  }
  await openView("Audit", ["Verify chain", "Append-only hash chain"]);
  const auditProbe = await waitFor(
    "audit chain state (Chain intact / No audit entries / Not configured)",
    textProbe(
      (t) =>
        t.includes("Chain intact") || t.includes("No audit entries") || t.includes("Not configured"),
    ),
    { timeout: STEP_MS, interval: 250 },
  );
  const auditText = auditProbe.body || "";
  if (
    !auditText.includes("Chain intact") &&
    !auditText.includes("No audit entries") &&
    !auditText.includes("Not configured")
  ) {
    throw new Error(`audit chain state missing: ${auditText.slice(0, 500)}`);
  }
  if (auditSeeded === auditSeed) {
    const auditRows = () => evaluate(`document.querySelectorAll(".ua-row.ua-audit").length`);
    const windowAttr = () =>
      evaluate(`(document.querySelector("[data-audit-window]") || {}).dataset?.auditWindow || ""`);
    const first = await waitFor(
      "audit window caps the first render at 100 rows",
      async () => {
        const rows = await auditRows();
        const attr = await windowAttr();
        return { ok: rows === 100 && attr.startsWith("100/"), rows, attr };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    const total = Number(String(first.attr || "").split("/")[1]);
    if (!(total >= auditSeed)) throw new Error(`audit window total ${first.attr} < ${auditSeed}`);
    const more = await evaluate(
      `(() => { const b = document.querySelector("[data-audit-more]"); if (!b) return "missing"; b.click(); return "clicked"; })()`,
    );
    if (more !== "clicked") throw new Error(`audit Show more ${more}`);
    const want = Math.min(total, 200);
    await waitFor(
      `audit rows beyond the window render after Show more (${want})`,
      async () => {
        const rows = await auditRows();
        return { ok: rows === want, rows };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  }

  // Memory panel (#1305): reload with the token set, open Memory, and
  // confirm the seeded entry renders — redacted, with a readable key cell.
  await evaluate(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "cockpit re-boot with Memory label after reload",
    async () => {
      const parsed = await readCockpit();
      const body = parsed.body || "";
      return {
        ok: body.includes("Run a command") && body.includes("Terminal"),
        body: body.slice(0, 300),
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // LSP diagnostics (#1404). First the real daemon: the pane must show
  // the daemon's own empty/unavailable text verbatim. Then a stubbed
  // /v1/diagnostics payload (CDP Fetch interception): entries grouped by
  // file, the editor badge counting errors only, and click-to-line
  // landing in the document viewer.
  const daemonDiagTexts = [
    "no language servers configured (add lsp.servers in config)",
    "no live language servers (see servers status; try retry)",
    "no diagnostics",
    "lsp capability unavailable on this host",
  ];
  await runPalette("Code diagnostics");
  await waitFor(
    "diagnostics pane shows the daemon's own note",
    async () => {
      const v = await evaluate(`(() => {
        const pane = document.querySelector("[data-diagnostics-pane]");
        const note = document.querySelector("[data-diag-note]");
        const err = document.querySelector("[data-diag-error] span");
        return {
          pane: !!pane,
          text: ((note && note.textContent) || (err && err.textContent) || "").trim(),
        };
      })()`);
      return { ok: v.pane && daemonDiagTexts.includes(v.text), ...v };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const diagStub = JSON.stringify({
    diagnostics: [
      { path: "cmd/strike/main.go", line: 10, character: 1, severity: "error", source: "smoke", code: "E1", message: "smoke diag error ten" },
      { path: "cmd/strike/main.go", line: 12, character: 3, severity: "warning", source: "smoke", message: "smoke diag warning twelve" },
      { path: "go.mod", line: 1, character: 1, severity: "error", source: "smoke", message: "smoke diag go mod" },
    ],
    count: 3,
  });
  const onDiagPaused = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.method !== "Fetch.requestPaused") return;
    send("Fetch.fulfillRequest", {
      requestId: msg.params.requestId,
      responseCode: 200,
      responseHeaders: [{ name: "Content-Type", value: "application/json" }],
      body: Buffer.from(diagStub).toString("base64"),
    });
  };
  ws.addEventListener("message", onDiagPaused);
  await send("Fetch.enable", { patterns: [{ urlPattern: "*/v1/diagnostics*", requestStage: "Request" }] });
  try {
    await waitFor(
      "stubbed diagnostics grouped by file",
      async () => {
        const v = await evaluate(`(() => {
          const files = [...document.querySelectorAll("[data-diag-file]")].map((el) => el.getAttribute("data-diag-file"));
          const entries = [...document.querySelectorAll(".diag-entry")].map((b) => b.getAttribute("data-diag-path") + ":" + b.getAttribute("data-diag-line"));
          if (files.length === 0) document.querySelector("[data-diag-refresh]")?.click();
          return { files, entries };
        })()`);
        return {
          ok: v.files.join(",") === "cmd/strike/main.go,go.mod" && v.entries.join(",") === "cmd/strike/main.go:10,cmd/strike/main.go:12,go.mod:1",
          ...v,
        };
      },
      { timeout: WAIT_MS, interval: 500 },
    );
    const diagClicked = await evaluate(`(() => {
      const b = document.querySelector('.diag-entry[data-diag-path="cmd/strike/main.go"][data-diag-line="10"]');
      if (!b) return "missing";
      b.click();
      return "clicked";
    })()`);
    if (diagClicked !== "clicked") {
      throw new Error(`diagnostics entry ${diagClicked}`);
    }
    await waitFor(
      "diagnostic click opens the file at its line, badge counts errors only",
      async () => {
        const v = await evaluate(`(() => {
          const doc = document.querySelector('[data-ev-root="open"]');
          const badge = document.querySelector("[data-diag-badge]");
          return {
            path: doc ? doc.getAttribute("data-ev-path") || "" : "",
            lines: doc ? doc.querySelectorAll(".ev-line").length : 0,
            badge: badge ? (badge.textContent || "").trim() : "",
          };
        })()`);
        return { ok: v.path.endsWith("cmd/strike/main.go") && v.lines >= 10 && v.badge === "1 error", ...v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
  } finally {
    await send("Fetch.disable", {});
    ws.removeEventListener("message", onDiagPaused);
  }
  // Close the document again: the viewer takes the transcript's place,
  // and later steps read transcript rows.
  await evaluate(`document.querySelector("[data-ev-back]")?.click()`);
  await waitFor(
    "diagnostics check closes the document viewer",
    async () => ({ ok: (await evaluate(`!document.querySelector('[data-ev-root="open"]')`)) === true }),
    { timeout: STEP_MS, interval: 200 },
  );

  // Notification grouping (#1826): stub GET /v1/notifications with two
  // consecutive child.finished rows for one session plus an unrelated
  // row. The centre must show one counted, unread group; opening it
  // expands the members, clears the dot, and marks both read with one
  // POST /v1/notifications/seen (captured, not sent to the daemon).
  const notifyStub = JSON.stringify({
    notifications: [
      { id: "smoke-grp-2", seq: 9002, kind: "child.finished", sessionId: "smoke-grp-s1", title: "smoke child two finished", at: "2026-11-01T10:02:00Z" },
      { id: "smoke-grp-1", seq: 9001, kind: "child.finished", sessionId: "smoke-grp-s1", title: "smoke child one finished", at: "2026-11-01T10:01:00Z" },
      { id: "smoke-grp-0", seq: 9000, kind: "approval.needed", sessionId: "smoke-grp-s2", title: "smoke approval", at: "2026-11-01T10:00:00Z" },
    ],
    prefs: { enabled: false, quiet: { enabled: false, start: "22:00", end: "07:00" }, mutedSessions: [] },
    latest: 9002,
    seen: { upTo: 0, ids: [] },
  });
  const seenPosts = [];
  const onNotifyPaused = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.method !== "Fetch.requestPaused") return;
    const url = msg.params.request?.url || "";
    const seenPost = url.includes("/v1/notifications/seen");
    if (seenPost) seenPosts.push(msg.params.request?.postData || "");
    send("Fetch.fulfillRequest", {
      requestId: msg.params.requestId,
      responseCode: 200,
      responseHeaders: [{ name: "Content-Type", value: "application/json" }],
      body: Buffer.from(seenPost ? "{}" : notifyStub).toString("base64"),
    });
  };
  ws.addEventListener("message", onNotifyPaused);
  await send("Fetch.enable", {
    patterns: [
      { urlPattern: "*/v1/notifications", requestStage: "Request" },
      { urlPattern: "*/v1/notifications/seen", requestStage: "Request" },
    ],
  });
  try {
    await evaluate(`(() => { if (!document.querySelector(".notify-panel")) document.querySelector(".notify-bell")?.click(); return true; })()`);
    const groupProbe = `(() => {
      const g = document.querySelector('.notify-panel [data-notify-group="smoke-grp-1"]');
      const open = g && g.querySelector(".notify-open");
      return {
        group: !!g,
        count: g ? g.getAttribute("data-count") : "",
        expanded: open ? open.getAttribute("aria-expanded") : "",
        dot: !!(g && g.querySelector(".notify-dot")),
        members: [...document.querySelectorAll(".notify-panel .notify-member .notify-title")].map((t) => t.textContent.trim()),
        approval: [...document.querySelectorAll(".notify-panel .notify-title")].some((t) => t.textContent.includes("smoke approval")),
      };
    })()`;
    await waitFor(
      "notification group collapsed with a count and unread dot",
      async () => {
        const v = await evaluate(groupProbe);
        return { ok: v.group && v.count === "2" && v.expanded === "false" && v.dot && v.members.length === 0 && v.approval, ...v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    await evaluate(`document.querySelector('.notify-panel [data-notify-group="smoke-grp-1"] .notify-open').click()`);
    await waitFor(
      "notification group expands and marks every member read",
      async () => {
        const v = await evaluate(groupProbe);
        const posted = seenPosts.some((b) => b.includes("smoke-grp-1") && b.includes("smoke-grp-2"));
        return {
          ok:
            v.expanded === "true" &&
            !v.dot &&
            v.members.join("|") === "smoke child two finished|smoke child one finished" &&
            posted,
          posted,
          seenPosts: seenPosts.slice(0, 3),
          ...v,
        };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
  } finally {
    await send("Fetch.disable", {});
    ws.removeEventListener("message", onNotifyPaused);
    await evaluate(`(() => { if (document.querySelector(".notify-panel")) document.querySelector(".notify-bell")?.click(); return true; })()`);
  }

  await runPalette("Memory");
  const memoryText = (
    await waitFor(
      "memory panel to show the seeded entry",
      textProbe((t) => t.includes("smoke-pref") && t.includes("smoke likes plain files")),
      { timeout: WAIT_MS, interval: 250 },
    )
  ).body;
  if (!memoryText.includes("smoke-pref") || !memoryText.includes("smoke likes plain files")) {
    throw new Error(`memory panel did not show the seeded entry text=${memoryText.slice(0, 1500)}`);
  }
  if (memoryText.includes(secret)) {
    throw new Error("memory panel rendered the raw secret");
  }
  // A memory row's key cell must stay readable at panel width: the full
  // RFC3339 timestamp once sat as a flex sibling and crushed the link
  // block to ~35px (PR #1305 review). The text probe above can pass
  // before layout settles, leaving the link rect at width 0 for a
  // frame, so poll the width like every other wait in this file
  // instead of measuring once.
  await waitFor(
    "memory row key cell readable width (>100px)",
    async () => {
      const w = await evaluate(`(() => {
        const row = [...document.querySelectorAll(".storage-row")].find((r) => r.textContent.includes("smoke-pref"));
        const link = row ? row.querySelector(".link") : null;
        return link ? link.getBoundingClientRect().width : 0;
      })()`);
      return { ok: w > 100, memKeyWidthPx: w };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Memory export (#1534): the panel must offer the Export action with
  // its honest label, and the daemon route must return the seeded
  // entry as a strike-memory.json download.
  if (!memoryText.includes("Export memory") || !memoryText.includes("unencrypted")) {
    throw new Error(`memory panel missing export action/label text=${memoryText.slice(0, 1500)}`);
  }
  const exportBtnFound = await evaluate(`(() => {
    const btn = document.querySelector('[data-action="memory-export"]');
    return !!btn && btn.textContent.includes("Export memory");
  })()`);
  if (!exportBtnFound) {
    throw new Error("memory panel export button not found in DOM");
  }
  const exportRes = await fetch(`http://127.0.0.1:${port}/v1/memory/export`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  const exportBody = await exportRes.text();
  const exportDisposition = exportRes.headers.get("content-disposition") || "";
  if (
    !exportRes.ok ||
    !exportDisposition.includes("strike-memory.json") ||
    !exportBody.includes("smoke-pref")
  ) {
    throw new Error(
      `memory export route failed status=${exportRes.status} disposition=${exportDisposition} body=${exportBody.slice(0, 500)}`,
    );
  }
  // Memory import (#1541): paste the export straight back, preview
  // (every key overwrites, none new — so later entry counts are
  // unchanged), merge, and see the daemon's imported count.
  const importDoc = JSON.parse(exportBody);
  const overlap = importDoc.entries.length;
  const importOpened = await evaluate(`(() => {
    const btn = document.querySelector('[data-action="memory-import"]');
    if (!btn || btn.disabled) return false;
    btn.click();
    return true;
  })()`);
  if (!importOpened) {
    throw new Error("memory import button missing or disabled on the Project tab");
  }
  await waitFor(
    "memory import panel",
    async () => ({
      ok: await evaluate(`(() => {
        const ta = document.querySelector('[data-memory-import-panel] textarea');
        if (!ta) return false;
        ta.value = ${JSON.stringify(JSON.stringify(importDoc))};
        ta.dispatchEvent(new Event("input", { bubbles: true }));
        const btn = document.querySelector('[data-action="memory-import-preview"]');
        if (!btn || btn.disabled) return false;
        btn.click();
        return true;
      })()`),
    }),
    { timeout: WAIT_MS, interval: 250 },
  );
  const previewText = (
    await waitFor(
      "memory import preview counts",
      textProbe((t) => t.includes(`${overlap} entries in the file: ${overlap} would overwrite existing Project keys, 0 are new`)),
      { timeout: WAIT_MS, interval: 250 },
    )
  ).body;
  if (!previewText.includes("This imports into Project memory")) {
    throw new Error(`memory import preview did not name the Project target text=${previewText.slice(0, 1500)}`);
  }
  await evaluate(`document.querySelector('[data-action="memory-import-merge"]').click()`);
  await waitFor(
    "memory import merged with the daemon's count",
    textProbe(
      (t) =>
        t.includes(`Imported ${overlap} entries into Project memory (merged)`) &&
        t.includes("smoke-pref"),
    ),
    { timeout: WAIT_MS, interval: 250 },
  );

  // Memory search/filter (#1461): value fragment narrows to a visible-vs-
  // total count, a no-match query shows the distinct filtered-empty state
  // without hiding the Save form, and clearing restores every row.
  const setSearch = async (selector, value) => {
    const raw = await evaluate(
      `(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return "missing"; el.value = ${JSON.stringify(value)}; el.dispatchEvent(new Event("input", { bubbles: true })); return "set"; })()`,
    );
    if (raw !== "set") throw new Error(`storage search input ${selector} not found`);
  };
  await setSearch(".storage-search-memory", "smoke likes");
  await waitFor(
    "memory search by value fragment to narrow to 1 of 2",
    textProbe((t) => t.includes("1 of 2 entries") && t.includes("smoke-pref")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await setSearch(".storage-search-memory", "zzz-no-memory-match");
  await waitFor(
    "memory filtered-empty state with the Save form still present",
    async () => {
      const raw = await evaluate(`(() => JSON.stringify({
        empty: !!document.querySelector("[data-memory-filtered-empty]"),
        form: !!document.querySelector(".storage-form"),
        body: document.body.innerText.includes("No memory entries match"),
        count: document.body.innerText.includes("0 of 2 entries"),
      }))()`);
      const p = JSON.parse(raw || "{}");
      return { ok: !!(p.empty && p.form && p.body && p.count), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await setSearch(".storage-search-memory", "");
  await waitFor(
    "memory search cleared to restore all rows",
    textProbe((t) => t.includes("2 of 2 entries") && t.includes("smoke-pref")),
    { timeout: WAIT_MS, interval: 250 },
  );

  // Memory Delete confirm (#1554): Delete must open the shared confirm
  // dialog naming the entry; cancelling leaves the entry, confirming
  // removes it. Runs after the Memory search checks — later checks do not use
  // smoke-pref.
  const memRowProbe = async () => {
    const raw = await evaluate(`(() => {
      const row = [...document.querySelectorAll(".storage-row")].find((r) => r.textContent.includes("smoke-pref"));
      return JSON.stringify({ row: !!row, del: !!(row && row.querySelector("[data-action='delete-memory']")) });
    })()`);
    return JSON.parse(raw || "{}");
  };
  const clickMemoryDeleteOnce = async () =>
    evaluate(`(() => {
      const row = [...document.querySelectorAll(".storage-row")].find((r) => r.textContent.includes("smoke-pref"));
      const btn =
        row &&
        (row.querySelector("[data-action='delete-memory']") ||
          [...row.querySelectorAll("button")].find((b) => b.textContent.trim() === "Delete"));
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
  // The panel re-renders its rows when the async memory/knowledge loads
  // settle, so a single-shot click can land in the empty window between
  // renders (CI run 40971001626 failed exactly there). Retry the click
  // until the button is present, like every other probe in this harness.
  const clickMemoryDelete = async () => {
    for (let attempt = 0; attempt < 50; attempt++) {
      if ((await clickMemoryDeleteOnce()) === "clicked") return "clicked";
      await delay(200);
    }
    return "missing";
  };
  if ((await clickMemoryDelete()) !== "clicked") {
    const diag = await evaluate(`(() => {
      const rows = [...document.querySelectorAll(".storage-row")];
      const row = rows.find((r) => r.textContent.includes("smoke-pref"));
      return JSON.stringify({
        rows: rows.length,
        globalBtn: !!document.querySelector("[data-action='delete-memory']"),
        rowHtml: row ? row.outerHTML.slice(0, 400) : null,
      });
    })()`).catch((e) => `diag failed: ${e}`);
    throw new Error(`memory Delete button click failed diag=${diag}`);
  }
  const memDialog = await waitFor(
    "memory delete confirm dialog (entry named, cannot be undone)",
    async () => {
      const raw = await evaluate(`(() => {
        const dialog = document.querySelector("[data-confirm='dialog']");
        const err = document.querySelector(".storage-error");
        return JSON.stringify({ ok: !!dialog, text: dialog ? dialog.innerText : "", err: err ? err.textContent : "" });
      })()`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: p.ok && p.text.includes("smoke-pref") && p.text.includes("cannot be undone"),
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!memDialog.text.includes("Delete memory")) {
    throw new Error(`memory confirm copy wrong: ${String(memDialog.text).slice(0, 300)}`);
  }
  await evaluate(`document.querySelector("[data-confirm='cancel']").click()`);
  await waitFor(
    "memory delete confirm to close on cancel, entry intact",
    async () => {
      const p = await memRowProbe();
      return {
        ok: p.row && (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === false,
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if ((await clickMemoryDelete()) !== "clicked") {
    throw new Error("memory Delete button click (confirm path) failed");
  }
  await waitFor(
    "memory delete confirm dialog to reopen",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === true,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector("[data-confirm='accept']").click()`);
  await waitFor(
    "memory entry gone after confirmed delete",
    async () => {
      const p = await memRowProbe();
      return { ok: !p.row, ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // Knowledge search/filter (#1469): same shape over path/note/author.
  await runPalette("Knowledge");
  await waitFor(
    "knowledge panel to show the seeded pin",
    textProbe((t) => t.includes("docs/smoke-pin.md") && t.includes("smoke pin note")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await setSearch(".storage-search-knowledge", "pin note");
  await waitFor(
    "knowledge search by note fragment to narrow to 1 of 1",
    textProbe((t) => t.includes("1 of 1 pins") && t.includes("docs/smoke-pin.md")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await setSearch(".storage-search-knowledge", "zzz-no-pin-match");
  await waitFor(
    "knowledge filtered-empty state with the Pin form still present",
    async () => {
      const raw = await evaluate(`(() => JSON.stringify({
        empty: !!document.querySelector("[data-knowledge-filtered-empty]"),
        form: !!document.querySelector(".storage-form"),
        body: document.body.innerText.includes("No pins match"),
        count: document.body.innerText.includes("0 of 1 pins"),
      }))()`);
      const p = JSON.parse(raw || "{}");
      return { ok: !!(p.empty && p.form && p.body && p.count), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await setSearch(".storage-search-knowledge", "");
  await waitFor(
    "knowledge search cleared to restore the pin",
    textProbe((t) => t.includes("1 of 1 pins") && t.includes("docs/smoke-pin.md")),
    { timeout: WAIT_MS, interval: 250 },
  );

  // Decisions tab (#1485): the seeded ledger renders read-only. Default is
  // the active slice; Include history adds superseded/invalidated rows
  // (distinguished by data-ledger-status); a kind chip narrows through
  // the daemon; and the supersede chain is navigable old → new.
  const ledgerBoot = await (
    await fetch(`http://127.0.0.1:${port}/v1/bootstrap`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (!ledgerBoot.capabilities?.ledger) {
    throw new Error(`bootstrap does not advertise the ledger capability: ${JSON.stringify(ledgerBoot.capabilities)}`);
  }
  const ledgerRows = async () =>
    (await evaluate(`[...document.querySelectorAll("[data-ledger] .ledger-row")].map((r) => [
      r.dataset.ledgerId,
      r.dataset.ledgerStatus,
    ])`)) || [];
  await runPalette("Decisions");
  await waitFor(
    "decisions tab shows only the active slice",
    async () => {
      const rows = await ledgerRows();
      const ids = rows.map((r) => r[0]).sort().join(",");
      return { ok: ids === "led_smoke_fixture,led_smoke_new", body: JSON.stringify(rows) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-ledger-view="history"]').click()`);
  await waitFor(
    "include history adds superseded and invalidated rows",
    async () => {
      const rows = await ledgerRows();
      const st = Object.fromEntries(rows);
      return {
        ok:
          rows.length === 4 &&
          st.led_smoke_old === "superseded" &&
          st.led_smoke_net === "invalidated" &&
          st.led_smoke_new === "active",
        body: JSON.stringify(rows),
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-ledger-kind="assumption"]').click()`);
  await waitFor(
    "kind filter narrows history to the assumption",
    async () => {
      const rows = await ledgerRows();
      return { ok: rows.length === 1 && rows[0][0] === "led_smoke_net", body: JSON.stringify(rows) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-ledger-kind=""]').click()`);
  await waitFor(
    "all kinds restores the history rows",
    async () => ({ ok: (await ledgerRows()).length === 4 }),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-ledger-id="led_smoke_old"] .link').click()`);
  await waitFor(
    "superseded entry detail names its replacement",
    async () => {
      const d = await evaluate(`(() => {
        const el = document.querySelector('[data-ledger-detail="led_smoke_old"]');
        return el ? { text: el.innerText, chain: !!el.querySelector('[data-ledger-chain="superseded-by"]') } : null;
      })()`);
      return { ok: !!d && d.chain && d.text.includes("led_smoke_new"), body: JSON.stringify(d) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-ledger-chain="superseded-by"]').click()`);
  await waitFor(
    "following the chain opens the current decision",
    async () => {
      const d = await evaluate(`(() => {
        const el = document.querySelector('[data-ledger-detail="led_smoke_new"]');
        return el ? { status: el.dataset.ledgerStatus, text: el.innerText, back: !!el.querySelector('[data-ledger-chain="supersedes"]') } : null;
      })()`);
      return {
        ok: !!d && d.status === "active" && d.back && d.text.includes("Smoke config is JSONC") && d.text.includes("docs/config.md"),
        body: JSON.stringify(d),
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // Context doctor (#1537): the statusline meter opens the breakdown, the
  // engine answers inspect.prompt over the live stream, and the dialog lists
  // real layers with token figures labelled as estimates.
  await evaluate(`document.querySelector('[data-context-doctor="open"]').click()`);
  await waitFor(
    "context doctor lists the live session's layers",
    async () => {
      const raw = await evaluate(`(() => {
        const d = document.querySelector('[data-context-doctor="dialog"]');
        if (!d) return JSON.stringify({ open: false });
        return JSON.stringify({
          open: true,
          layers: d.querySelectorAll("[data-context-layer]").length,
          scope: d.querySelector("[data-context-doctor-scope]")?.dataset.contextDoctorScope || "",
          error: d.querySelector("[data-context-doctor-error]")?.innerText || "",
          text: d.innerText.slice(0, 600),
        });
      })()`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: !!(p.open && p.layers > 0 && p.scope && !p.error && p.text.includes("chars/4") && p.text.includes("~")),
        ...p,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-context-doctor="refresh"]').click()`);
  await waitFor(
    "context doctor refresh settles with layers and no error",
    async () => {
      const raw = await evaluate(`(() => {
        const d = document.querySelector('[data-context-doctor="dialog"]');
        return JSON.stringify({
          layers: d ? d.querySelectorAll("[data-context-layer]").length : 0,
          pending: !!d?.querySelector('[data-context-doctor="refresh"]')?.disabled,
          error: d?.querySelector("[data-context-doctor-error]")?.innerText || "",
        });
      })()`);
      const p = JSON.parse(raw || "{}");
      return { ok: p.layers > 0 && !p.pending && !p.error, ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-context-doctor="close"]').click()`);
  await waitFor(
    "context doctor closes",
    async () => ({ ok: (await evaluate(`!!document.querySelector('[data-context-doctor="dialog"]')`)) === false }),
    { timeout: STEP_MS, interval: 200 },
  );

  // Lessons capture (#1376): "Remember this" on a user message opens a
  // pre-filled draft; Cancel stores nothing; Save stores a tagged lesson
  // through /v1/memory. The entry is removed again so later memory counts
  // are unaffected.
  const lessonKey = "lesson-smoke-capture";
  const memoryKeys = async () => {
    const r = await fetch(`http://127.0.0.1:${port}/v1/memory?scope=project`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const j = await r.json();
    return (j.entries || []).map((e) => ({ key: e.key, tags: e.tags || [], value: e.value }));
  };
  const openLessonDraft = async () => {
    const clicked = await evaluate(`(() => {
      const btn = [...document.querySelectorAll('[data-role="you"] [data-action="lesson-remember"]')].pop();
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
    if (clicked !== "clicked") throw new Error(`no Remember this action on a user message (${clicked})`);
    await waitFor(
      "lesson draft opens pre-filled from the message",
      async () => {
        const raw = await evaluate(`(() => {
          const d = document.querySelector('[data-lesson="draft"]');
          if (!d) return JSON.stringify({ open: false });
          return JSON.stringify({
            open: true,
            value: d.querySelector('[data-lesson="value"]').value,
            key: d.querySelector('[data-lesson="key"]').value,
            scope: d.querySelector('[data-lesson-scope="project"]').getAttribute("aria-pressed"),
          });
        })()`);
        const p = JSON.parse(raw || "{}");
        return { ok: !!(p.open && p.value.trim() && p.key.startsWith("lesson-") && p.scope === "true"), ...p };
      },
      { timeout: STEP_MS, interval: 200 },
    );
  };
  const lessonBefore = (await memoryKeys()).length;
  await openLessonDraft();
  await evaluate(`document.querySelector('[data-lesson="cancel"]').click()`);
  await waitFor(
    "lesson draft cancel closes without storing",
    async () => ({
      ok:
        (await evaluate(`!!document.querySelector('[data-lesson="draft"]')`)) === false &&
        (await memoryKeys()).length === lessonBefore,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await openLessonDraft();
  await evaluate(`(() => {
    const el = document.querySelector('[data-lesson="key"]');
    el.value = ${JSON.stringify(lessonKey)};
    el.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector('[data-lesson="save"]').click();
    return true;
  })()`);
  await waitFor(
    "saved lesson is stored with the lesson tag and the draft closes",
    async () => {
      const rows = await memoryKeys();
      const row = rows.find((r) => r.key === lessonKey);
      const open = await evaluate(`!!document.querySelector('[data-lesson="draft"]')`);
      return { ok: !!row && row.tags.includes("lesson") && !!row.value && open === false, body: JSON.stringify(row) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await fetch(`http://127.0.0.1:${port}/v1/memory/${lessonKey}?scope=project`, {
    method: "DELETE",
    headers: { Authorization: `Bearer ${token}` },
  });
  // Local issues (#1500): create → listed under Open → close → listed under
  // Closed → still closed after the panel remounts (fresh daemon read) and
  // in the daemon's own list. An empty title is refused inline.
  const issuesBoot = await (
    await fetch(`http://127.0.0.1:${port}/v1/bootstrap`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (!issuesBoot.capabilities?.issues) {
    throw new Error(`bootstrap does not advertise the issues capability: ${JSON.stringify(issuesBoot.capabilities)}`);
  }
  const issueRows = async () =>
    (await evaluate(`[...document.querySelectorAll("[data-local-issues] .local-issue-row")].map((r) => [
      r.dataset.issueId,
      r.dataset.issueStatus,
      r.innerText,
    ])`)) || [];
  const smokeIssueTitle = "smoke local issue";
  await runPalette("Local issues");
  await waitFor(
    "issues panel mounts with the create form",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-local-issues] [data-issues-form]")`)) === true,
    }),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-action="issues-create"]').click()`);
  await waitFor(
    "empty title is refused inline",
    async () => {
      const t = await evaluate(`document.querySelector("[data-issues-action-error]")?.innerText || ""`);
      return { ok: String(t).includes("Title is required"), body: t };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  const fillIssue = await evaluate(`(() => {
    const set = (sel, value) => {
      const el = document.querySelector(sel);
      if (!el) return false;
      el.value = value;
      el.dispatchEvent(new Event("input", { bubbles: true }));
      return true;
    };
    if (!set("[data-issues-title]", ${JSON.stringify(smokeIssueTitle)})) return "missing title";
    if (!set("[data-issues-body]", "filed by webui-smoke")) return "missing body";
    document.querySelector('[data-action="issues-create"]').click();
    return "sent";
  })()`);
  if (fillIssue !== "sent") throw new Error(`issues create form: ${fillIssue}`);
  let smokeIssueId = "";
  await waitFor(
    "created issue appears under Open",
    async () => {
      const rows = await issueRows();
      const row = rows.find((r) => String(r[2]).includes(smokeIssueTitle));
      if (row) smokeIssueId = row[0];
      return { ok: !!row && row[1] === "open", body: JSON.stringify(rows) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-issue-id="${smokeIssueId}"] .link').click()`);
  await waitFor(
    "issue body expands",
    async () => {
      const t = await evaluate(`document.querySelector('[data-issue-body="${smokeIssueId}"]')?.innerText || ""`);
      return { ok: String(t).includes("filed by webui-smoke"), body: t };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector('[data-issue-id="${smokeIssueId}"] [data-action="issues-close"]').click()`);
  await waitFor(
    "closed issue leaves the Open filter",
    async () => {
      const rows = await issueRows();
      return { ok: !rows.some((r) => r[0] === smokeIssueId), body: JSON.stringify(rows) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-issues-filter="closed"]').click()`);
  await waitFor(
    "closed issue listed under Closed",
    async () => {
      const rows = await issueRows();
      return { ok: rows.some((r) => r[0] === smokeIssueId && r[1] === "closed"), body: JSON.stringify(rows) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Remount: leave the tab and come back; the panel re-reads the daemon.
  await runPalette("Memory");
  await waitFor(
    "issues panel unmounts on another tab",
    async () => ({ ok: (await evaluate(`!!document.querySelector("[data-local-issues]")`)) === false }),
    { timeout: WAIT_MS, interval: 250 },
  );
  await runPalette("Local issues");
  await waitFor(
    "issues panel remounts",
    async () => ({ ok: (await evaluate(`!!document.querySelector('[data-issues-filter="closed"]')`)) === true }),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('[data-issues-filter="closed"]').click()`);
  await waitFor(
    "closed issue persists after the panel remounts",
    async () => {
      const rows = await issueRows();
      return { ok: rows.some((r) => r[0] === smokeIssueId && r[1] === "closed"), body: JSON.stringify(rows) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const persisted = await (
    await fetch(`http://127.0.0.1:${port}/v1/issues?status=closed`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (!(persisted.issues || []).some((i) => String(i.ID ?? i.id) === smokeIssueId && (i.Status ?? i.status) === "closed")) {
    throw new Error(`daemon does not list issue ${smokeIssueId} as closed: ${JSON.stringify(persisted)}`);
  }

  // Dashboards (#1628): a page started from a template renders a measured
  // block with a daemon value, an unmeasured metric as a dash (never 0),
  // and an agent-written block with its "agent wrote this" marker.
  {
    const dashAuth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
    const roots = await (await fetch(`http://127.0.0.1:${port}/v1/roots`, { headers: dashAuth })).json();
    const dashRoot = roots.activeId || (roots.roots && roots.roots[0] && roots.roots[0].id) || "";
    if (!dashRoot) {
      throw new Error(`dashboards: no live root to own the page: ${JSON.stringify(roots).slice(0, 200)}`);
    }
    const dashNoAuth = await fetch(`http://127.0.0.1:${port}/v1/dashboards?root=${encodeURIComponent(dashRoot)}`);
    if (dashNoAuth.status !== 401) {
      throw new Error(`dashboards without token = ${dashNoAuth.status}`);
    }
    const made = await fetch(`http://127.0.0.1:${port}/v1/dashboards?root=${encodeURIComponent(dashRoot)}`, {
      method: "POST",
      headers: dashAuth,
      body: JSON.stringify({ template: "project_report", title: "Smoke report" }),
    });
    if (made.status !== 201) {
      throw new Error(`dashboard create = ${made.status} ${await made.text()}`);
    }
    const dashPage = await made.json();
    const total = (dashPage.blocks || []).find((b) => b.metric === "sessions.total");
    const cost = (dashPage.blocks || []).find((b) => b.metric === "cost.usd");
    if (!total || !total.measured || typeof total.value !== "number" || !cost || cost.measured || "value" in cost) {
      throw new Error(`dashboard measured blocks = ${JSON.stringify(dashPage.blocks).slice(0, 400)}`);
    }
    await runPalette("Dashboards");
    await waitFor(
      "dashboards index lists the new page",
      async () => {
        const raw = await evaluate(`(() => {
          const row = document.querySelector('[data-dashboard="${dashPage.id}"] button');
          if (row && !document.querySelector('[data-dashboard-page="${dashPage.id}"]')) row.click();
          return JSON.stringify({ root: !!document.querySelector("#dashboards-root"), row: !!row });
        })()`);
        const p = JSON.parse(raw || "{}");
        return { ok: !!(p.root && p.row), ...p };
      },
      { timeout: WAIT_MS, interval: 300 },
    );
    await waitFor(
      "dashboard page shows a measured value, a dash for unmeasured cost, and the agent marker",
      async () => {
        const raw = await evaluate(`(() => {
          const page = document.querySelector('[data-dashboard-page="${dashPage.id}"]');
          if (!page) {
            document.querySelector('[data-dashboard="${dashPage.id}"] button')?.click();
            return JSON.stringify({ page: false });
          }
          const vals = [...page.querySelectorAll(".dash-value")].map((e) => ({ m: e.dataset.measured, t: e.textContent.trim(), k: e.title }));
          const agent = page.querySelector('[data-provenance="agent"] .dash-mark');
          return JSON.stringify({ page: true, vals, mark: agent ? agent.textContent.trim() : "" });
        })()`);
        const p = JSON.parse(raw || "{}");
        const vals = p.vals || [];
        const measured = vals.find((v) => v.k === "sessions.total");
        const dash = vals.find((v) => v.k === "cost.usd");
        return {
          ok: !!(p.page && measured && measured.m === "true" && /^\d+$/.test(measured.t) && dash && dash.m === "false" && dash.t === "\u2014" && p.mark === "agent wrote this"),
          ...p,
        };
      },
      { timeout: WAIT_MS, interval: 300 },
    );
  }

  // Ideas inbox (#1323): a producer deposits a suggestion over the authed
  // API, the palette opens the Ideas page, the row renders with its why and
  // evidence, and Dismiss records it daemon-side so the list empties.
  {
    const ideasUrl = `http://127.0.0.1:${port}/v1/suggestions`;
    const ideaAuth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
    const idea = {
      title: "Smoke idea: tidy the README",
      why: "The smoke run noticed a stale section.",
      prompt: "Tidy the README",
      source: "smoke.producer",
      kind: "docs",
      evidenceKey: "smoke:readme",
      evidence: [{ label: "Smoke evidence", url: "https://example.com/smoke" }],
    };
    const ideaNoAuth = await fetch(ideasUrl, { method: "POST", body: JSON.stringify(idea) });
    if (ideaNoAuth.status !== 401) {
      throw new Error(`suggestions create without token = ${ideaNoAuth.status}`);
    }
    const created = await fetch(ideasUrl, { method: "POST", headers: ideaAuth, body: JSON.stringify(idea) });
    if (created.status !== 201) {
      throw new Error(`suggestions create = ${created.status} ${await created.text()}`);
    }
    const ideaId = (await created.json()).suggestion.id;
    const dup = await fetch(ideasUrl, { method: "POST", headers: ideaAuth, body: JSON.stringify(idea) });
    if (dup.status !== 200 || (await dup.json()).created !== false) {
      throw new Error(`suggestions dedupe = ${dup.status}`);
    }
    await runPalette("Ideas");
    await waitFor(
      "ideas page lists the deposited suggestion with its why and evidence",
      async () => {
        const raw = await evaluate(`(() => {
          const row = document.querySelector('[data-idea="${ideaId}"]');
          const badge = document.querySelector('[data-nav="header"] [data-ideas-badge]');
          return JSON.stringify({
            root: !!document.querySelector("#ideas-root"),
            row: !!row,
            text: row ? row.innerText : "",
            badge: badge ? badge.innerText : "",
            quiet: !!document.querySelector("[data-ideas-quiet]"),
          });
        })()`);
        const p = JSON.parse(raw || "{}");
        // #1926: the open-count badge lives in the shell header, not the rail.
        return {
          ok: !!(p.root && p.row && p.text.includes("tidy the README") && p.text.includes("stale section") && p.text.includes("Smoke evidence") && p.badge.includes("1") && p.quiet),
          ...p,
        };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    await waitFor(
      "ideas Dismiss empties the list",
      async () => {
        const raw = await evaluate(`(() => {
          const btn = document.querySelector('[data-idea="${ideaId}"] .ideas-dismiss');
          if (btn && !btn.disabled) btn.click();
          return JSON.stringify({
            empty: !!document.querySelector("[data-ideas-empty]"),
            badge: !!document.querySelector('[data-nav="header"] [data-ideas-badge]'),
          });
        })()`);
        const p = JSON.parse(raw || "{}");
        return { ok: !!p.empty && !p.badge, ...p };
      },
      { timeout: WAIT_MS, interval: 400 },
    );
    const ideasAfter = await (await fetch(ideasUrl, { headers: ideaAuth })).json();
    const stored = (ideasAfter.suggestions || []).find((x) => x.id === ideaId);
    if (!stored || stored.state !== "dismissed" || ideasAfter.open !== 0) {
      throw new Error(`dismissed idea state = ${JSON.stringify(ideasAfter).slice(0, 400)}`);
    }
    // #1926: quiet hours are store-enforced; reserved producer sources are
    // refused over HTTP.
    const prefsUrl = `${ideasUrl}/prefs`;
    const allDay = { mutedSources: [], mutedKinds: [], quietHours: { enabled: true, start: "00:00", end: "00:00" } };
    const quietSet = await fetch(prefsUrl, { method: "PUT", headers: ideaAuth, body: JSON.stringify(allDay) });
    if (quietSet.status !== 200) {
      throw new Error(`suggestions quiet hours = ${quietSet.status} ${await quietSet.text()}`);
    }
    const quietIdea = { ...idea, evidenceKey: "smoke:quiet" };
    const held = await fetch(ideasUrl, { method: "POST", headers: ideaAuth, body: JSON.stringify(quietIdea) });
    if (held.status !== 409) {
      throw new Error(`suggestions create in quiet hours = ${held.status}`);
    }
    allDay.quietHours.enabled = false;
    await fetch(prefsUrl, { method: "PUT", headers: ideaAuth, body: JSON.stringify(allDay) });
    const reserved = await fetch(ideasUrl, { method: "POST", headers: ideaAuth, body: JSON.stringify({ ...idea, source: "scheduler" }) });
    if (reserved.status !== 400) {
      throw new Error(`suggestions create as reserved producer = ${reserved.status}`);
    }
  }

  // Goals + Autopilot (#1300, #1593). The view is palette-only. A bundle
  // that predates Goals has no palette command and fails here.
  await runPalette("Goals");
  await waitFor(
    "goals pane (root, form, verification field, autopilot) after opening Goals",
    async () => {
      const raw = await evaluate(`(() => {
        const root = document.querySelector("#goals-root");
        return JSON.stringify({
          root: !!root,
          form: !!document.querySelector("#goals-form"),
          verify: !!document.getElementById("goals-verify"),
          run: !!document.getElementById("goals-run"),
          autopilot: root ? root.innerText.includes("Autopilot") : false,
        });
      })()`);
      const p = JSON.parse(raw || "{}");
      return { ok: !!(p.root && p.form && p.verify && p.run && p.autopilot), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const fillGoalForm = async (desc, verify, turns, fails) =>
    evaluate(`(() => {
      const set = (id, value) => {
        const el = document.getElementById(id);
        el.value = value;
        el.dispatchEvent(new Event("input", { bubbles: true }));
      };
      set("goals-desc", ${JSON.stringify(desc)});
      set("goals-verify", ${JSON.stringify(verify)});
      set("goals-turns", ${JSON.stringify(turns)});
      set("goals-fails", ${JSON.stringify(fails)});
      document.getElementById("goals-run").click();
      return "sent";
    })()`);
  if ((await fillGoalForm("smoke pass", "true", "8", "3")) !== "sent") {
    throw new Error("goals form submit (pass case) did not run");
  }
  await waitFor(
    "passing goal run to reach Done",
    goalRowProbe("smoke pass", "Done"),
    { timeout: WAIT_MS, interval: 250 },
  );
  // Duplicate a goal (#1415): the detail Duplicate opens the create form
  // prefilled from the spec; nothing is created until submit, and the
  // submitted copy is a fresh goal.
  const goalsBeforeDup = await (
    await fetch(`http://127.0.0.1:${port}/v1/goals`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  await evaluate(`document.getElementById("goals-duplicate").click()`);
  await waitFor(
    "duplicate goal form prefilled with the original spec",
    async () => {
      const raw = await evaluate(`JSON.stringify({
        desc: document.getElementById("goals-desc")?.value,
        verify: document.getElementById("goals-verify")?.value,
        turns: document.getElementById("goals-turns")?.value,
      })`);
      const p = JSON.parse(raw || "{}");
      return { ok: p.desc === "smoke pass" && p.verify === "cmd: true" && p.turns === "8", ...p };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  const goalsAfterDupOpen = await (
    await fetch(`http://127.0.0.1:${port}/v1/goals`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (goalsAfterDupOpen.goals.length !== goalsBeforeDup.goals.length) {
    throw new Error("duplicate goal was created before submit");
  }
  await evaluate(`(() => {
    const el = document.getElementById("goals-desc");
    el.value = "smoke pass copy";
    el.dispatchEvent(new Event("input", { bubbles: true }));
    document.getElementById("goals-run").click();
    return "sent";
  })()`);
  await waitFor(
    "duplicated goal run to reach Done",
    goalRowProbe("smoke pass copy", "Done"),
    { timeout: WAIT_MS, interval: 250 },
  );
  const goalsAfterDupSubmit = await (
    await fetch(`http://127.0.0.1:${port}/v1/goals`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (goalsAfterDupSubmit.goals.length !== goalsBeforeDup.goals.length + 1) {
    throw new Error(`duplicate goal submit count=${goalsAfterDupSubmit.goals.length} want=${goalsBeforeDup.goals.length + 1}`);
  }
  await evaluate(`document.querySelector(".goals-list .secondary").click()`);
  await waitFor(
    "goal form to reappear after picking the listed goal",
    async () => ({ ok: (await evaluate(`!!document.getElementById("goals-desc")`)) === true }),
    { timeout: STEP_MS, interval: 250 },
  );
  if ((await fillGoalForm("smoke fail", "false", "20", "2")) !== "sent") {
    throw new Error("goals form submit (fail case) did not run");
  }
  await waitFor(
    "failing goal run to surface Needs you / verification failed",
    async () => {
      // Row label or the detail escalation banner — never the filter chip.
      const row = await goalRowProbe("smoke fail", "Needs you")();
      const banner = (await evaluate(`!!document.getElementById("goals-escalate")`)) === true;
      const body = await textProbe((b) => b.includes("verification failed"))();
      return { ok: (row.ok || banner) && body.ok, body: row.body };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Goal Abort confirm (#1559): Abort is terminal, so it must open the
  // shared confirm dialog; cancelling leaves the goal in Needs you,
  // confirming moves it to Aborted.
  const clickGoalAbortOnce = async () =>
    evaluate(`(() => {
      const btn = document.querySelector("[data-action='abort-goal']");
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
  // Same re-render race as the Memory Delete click above: retry until
  // the button is present instead of failing on a single-shot probe.
  const clickGoalAbort = async () => {
    for (let attempt = 0; attempt < 50; attempt++) {
      if ((await clickGoalAbortOnce()) === "clicked") return "clicked";
      await delay(200);
    }
    return "missing";
  };
  if ((await clickGoalAbort()) !== "clicked") {
    throw new Error("goal Abort button click failed");
  }
  const goalDialog = await waitFor(
    "goal abort confirm dialog (cannot be resumed)",
    async () => {
      const raw = await evaluate(`(() => {
        const dialog = document.querySelector("[data-confirm='dialog']");
        return JSON.stringify({ ok: !!dialog, text: dialog ? dialog.innerText : "" });
      })()`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: p.ok && p.text.includes("cannot be resumed or run again"),
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!goalDialog.text.includes("Abort goal")) {
    throw new Error(`goal abort confirm copy wrong: ${String(goalDialog.text).slice(0, 300)}`);
  }
  await evaluate(`document.querySelector("[data-confirm='cancel']").click()`);
  await waitFor(
    "goal abort confirm to close on cancel, goal still Needs you",
    async () => {
      // Row label, not body text: the "Needs you" filter chip is always on screen (#1455).
      const row = await goalRowProbe("smoke fail", "Needs you")();
      return {
        ok:
          (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === false &&
          row.ok,
        body: row.body,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if ((await clickGoalAbort()) !== "clicked") {
    throw new Error("goal Abort button click (confirm path) failed");
  }
  await waitFor(
    "goal abort confirm dialog to reopen",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === true,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector("[data-confirm='accept']").click()`);
  await waitFor(
    "goal in Aborted state after confirmed abort",
    // Row label, not body text: the "Aborted" filter chip is always on screen (#1455).
    goalRowProbe("smoke fail", "Aborted"),
    { timeout: WAIT_MS, interval: 250 },
  );
  // Goals list filter (#1455): status chip + description search narrow the
  // list with AND semantics; a no-match shows its own empty state, and
  // Clear filters restores every row.
  await evaluate(`document.querySelector('[data-goal-filter="done"]').click()`);
  await waitFor(
    "Done status chip narrows the goals list",
    goalRowProbe("smoke pass", "Done"),
    { timeout: STEP_MS, interval: 250 },
  );
  const doneRows = await evaluate(`[...document.querySelectorAll(".goals-row .goals-status")].map((s) => s.textContent.trim())`);
  if (!doneRows.length || doneRows.some((s) => s !== "Done")) {
    throw new Error(`Done filter left non-Done rows: ${JSON.stringify(doneRows)}`);
  }
  await evaluate(`(() => {
    const el = document.getElementById("goals-search");
    el.value = "zzz-no-such-goal";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  })()`);
  await waitFor(
    "goals no-match empty state and count",
    async () => {
      const r = await evaluate(`({
        empty: document.body.innerText.includes("No goals match — clear filters"),
        count: document.querySelector("[data-goals-count]")?.textContent.trim() || "",
        rows: document.querySelectorAll(".goals-row").length,
      })`);
      return { ok: r.empty && r.rows === 0 && /^Showing 0 of \d+ goals$/.test(r.count), body: JSON.stringify(r) };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`document.querySelector("[data-goals-clear]").click()`);
  await waitFor(
    "Clear filters restores every goal row",
    async () => {
      const r = await evaluate(`({
        rows: document.querySelectorAll(".goals-row").length,
        search: document.getElementById("goals-search")?.value,
        on: document.querySelector("[data-goal-filter].on")?.dataset.goalFilter,
      })`);
      return { ok: r.rows >= 3 && r.search === "" && r.on === "all", body: JSON.stringify(r) };
    },
    { timeout: STEP_MS, interval: 250 },
  );

  const clicked = await send("Runtime.evaluate", {
    expression: `(() => {
      const btn = document.querySelector('[data-feature="notifications"]');
      if (!btn) return "missing-bell";
      btn.click();
      return "clicked";
    })()`,
    returnByValue: true,
  });
  if (clicked.result?.result?.value !== "clicked") {
    throw new Error(`notification bell missing: ${clicked.result?.result?.value}`);
  }
  let centre = "";
  for (let i = 0; i < 20; i++) {
    const opened = await send("Runtime.evaluate", {
      expression: "document.body ? document.body.innerText : ''",
      returnByValue: true,
    });
    centre = opened.result?.result?.value || "";
    if (
      centre.includes("Mark all read") &&
      (centre.includes("Nothing yet") ||
        centre.includes("Could not load notifications") ||
        centre.includes("Alt+N opens this panel"))
    ) {
      break;
    }
    await delay(100);
  }
  if (
    !centre.includes("Mark all read") ||
    (!centre.includes("Nothing yet") &&
      !centre.includes("Could not load notifications") &&
      !centre.includes("Alt+N opens this panel"))
  ) {
    throw new Error(`notification centre did not open: ${String(centre).slice(0, 500)}`);
  }
  await send("Runtime.evaluate", {
    expression: `document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));`,
    returnByValue: true,
  });
  await delay(100);
  await send("Runtime.evaluate", {
    expression: `document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "n", altKey: true, bubbles: true }));`,
    returnByValue: true,
  });
  let keyed = "closed";
  for (let i = 0; i < 20; i++) {
    const again = await send("Runtime.evaluate", {
      expression: "document.body && document.body.innerText.includes('Mark all read') ? 'open' : 'closed'",
      returnByValue: true,
    });
    keyed = again.result?.result?.value || "closed";
    if (keyed === "open") break;
    await delay(100);
  }
  if (keyed !== "open") {
    throw new Error(`Alt+N did not reopen notifications: ${keyed}`);
  }
  await evaluate(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.navigate", {
    url: `http://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`,
  });

  const booted = await waitFor(
    "side chat shell to boot (session rows + composer + activity affordances)",
    async () => {
      const raw = await evaluate(`(() => JSON.stringify({
        rows: document.querySelectorAll(".session-row").length,
        composer: !!document.querySelector(".side-chat-open-activity"),
        activity: !!document.querySelector(".side-chat-open-activity"),
        text: (document.body && document.body.innerText || "").slice(0, 240),
      }))()`);
      const p = JSON.parse(raw || "{}");
      return { ok: p.rows > 0 && !!p.composer && !!p.activity, ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  if (!(booted.rows > 0 && booted.composer && booted.activity)) {
    throw new Error(`side chat shell did not boot ${JSON.stringify(booted)}`);
  }

  // The seed carries an outbox client message id (#1325); the retry below
  // must be acknowledged as a duplicate and never start a second turn.
  const seedInit = {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
      "X-Strike-Client-Message-Id": "smoke-seed-1325",
    },
    body: JSON.stringify({ type: "user.input", data: { text: "read the makefile" } }),
  };
  const seeded = await fetch(`http://127.0.0.1:${port}/v1/ops`, seedInit);
  if (!seeded.ok) {
    throw new Error(`seed user.input ${seeded.status} ${await seeded.text()}`);
  }
  const seedRetry = await fetch(`http://127.0.0.1:${port}/v1/ops`, seedInit);
  const seedRetryBody = await seedRetry.text();
  if (seedRetry.status !== 200 || !seedRetryBody.includes('"duplicate":true')) {
    throw new Error(`outbox retry was not deduped: ${seedRetry.status} ${seedRetryBody}`);
  }

  // #1565: share link create -> open with no credential -> revoke -> dead end.
  {
    const authed = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
    const st = await (await fetch(`http://127.0.0.1:${port}/v1/status`, { headers: authed })).json();
    const sid = st.sessionId;
    if (!sid) throw new Error(`share smoke: no live session id ${JSON.stringify(st)}`);
    const sharesUrl = `http://127.0.0.1:${port}/v1/sessions/${encodeURIComponent(sid)}/shares`;
    // The session log is written by the engine; give the seed a moment.
    let minted;
    for (let i = 0; i < 50; i++) {
      minted = await fetch(sharesUrl, { method: "POST", headers: authed, body: JSON.stringify({ ttlSeconds: 3600 }) });
      if (minted.status !== 404) break;
      await delay(200);
    }
    if (minted.status !== 201) throw new Error(`share mint ${minted.status} ${await minted.text()}`);
    const grant = await minted.json();
    if (!grant.path || !grant.path.startsWith("/#share=") || grant.path.includes("?")) {
      throw new Error(`share link not fragment-carried: ${grant.path}`);
    }
    const redeem = () =>
      fetch(`http://127.0.0.1:${port}/share/v1/transcript`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ token: grant.token, after: 0 }),
      });
    let opened;
    let openedBody = "";
    for (let i = 0; i < 50; i++) {
      opened = await redeem();
      openedBody = await opened.text();
      if (opened.status !== 200 || openedBody.includes("read the makefile")) break;
      await delay(200);
    }
    if (opened.status !== 200 || !openedBody.includes("read the makefile")) {
      throw new Error(`share redeem ${opened.status} ${openedBody.slice(0, 300)}`);
    }
    const asGateway = await fetch(`http://127.0.0.1:${port}/v1/sessions`, {
      headers: { Authorization: `Bearer ${grant.token}` },
    });
    if (asGateway.status !== 401) throw new Error(`share token opened /v1/sessions: ${asGateway.status}`);
    const revoked = await fetch(`${sharesUrl}/${encodeURIComponent(grant.id)}`, { method: "DELETE", headers: authed });
    if (revoked.status !== 204) throw new Error(`share revoke ${revoked.status}`);
    const dead = await redeem();
    if (dead.status !== 404) throw new Error(`revoked share still opens: ${dead.status}`);
  }

  await evaluate(`document.querySelector(".side-chat-open-activity").click()`);

  const panel = await waitFor(
    "side chat panel (Read-only note + Ask button)",
    async () => {
      const raw = await evaluate(`(() => {
        const el = document.querySelector(".side-chat");
        if (!el) return "{}";
        return JSON.stringify({
          text: el.innerText,
          buttons: [...el.querySelectorAll("button")].map((b) => b.textContent.trim()),
        });
      })()`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: (p.text || "").includes("Read-only") && (p.buttons || []).includes("Ask"),
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!(panel.text || "").includes("Read-only") || !(panel.buttons || []).includes("Ask")) {
    throw new Error(`side chat panel missing ${JSON.stringify(panel)}`);
  }
  if ((panel.buttons || []).some((b) => b.startsWith("Approval"))) {
    throw new Error(`side chat rendered an approval control ${JSON.stringify(panel.buttons)}`);
  }

  const ask = async (text) => {
    const value = await evaluate(`(() => {
      const input = document.getElementById("side-chat-input");
      if (!input) return "missing input";
      const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
      proto.set.call(input, ${JSON.stringify(text)});
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      return "sent";
    })()`);
    if (value !== "sent") {
      throw new Error(`side chat keyboard send failed: ${String(value)}`);
    }
  };

  const panelText = async () => {
    const t = await evaluate(
      `document.querySelector(".side-chat") ? document.querySelector(".side-chat").innerText : ""`,
    );
    return t || "";
  };

  await ask("what is the agent doing?");
  // The question shows on a pending card while the side turn runs (#1363),
  // so wait for the settled answer before the next ask.
  const answered = await waitFor(
    "side chat to echo the asked question",
    async () => {
      const t = await panelText();
      const settled = await evaluate(
        `document.querySelectorAll("[data-side-chat-answer]").length > 0 && !document.querySelector("[data-side-chat-pending]")`,
      );
      return { ok: t.includes("what is the agent doing?") && settled === true, text: t };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  if (!answered.text.includes("what is the agent doing?")) {
    throw new Error(`side chat answer missing ${answered.text.slice(0, 800)}`);
  }

  await ask("run true");
  const refused = await waitFor(
    "side chat to refuse a tool call",
    async () => {
      const t = await panelText();
      return { ok: t.toLowerCase().includes("refused a tool call"), text: t };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!refused.text.toLowerCase().includes("refused a tool call")) {
    throw new Error(`side chat did not refuse a tool call ${refused.text.slice(0, 500)}`);
  }

  await evaluate(`window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await waitFor(
    "Escape to close the side chat",
    async () => ({ ok: (await evaluate(`!!document.querySelector(".side-chat")`)) === false }),
    { timeout: STEP_MS, interval: 100 },
  );

  const still = await evaluate(`document.body.innerText.includes("Approval")`);
  if (still !== true) {
    throw new Error("main composer lost its approval chip");
  }

  // #1384: read replies aloud. The speech engine is mocked at the browser
  // boundary (CI Chrome has no reliable voices): an on-device voice reads
  // the cleaned reply and marks the line playing until the engine ends;
  // with no voices the control explains instead of pretending to play.
  await evaluate(`(() => {
    const spoken = [];
    window.__ttsSpoken = spoken;
    window.__ttsVoices = [{ name: "Smoke Voice", lang: "en-US", localService: true, default: true, voiceURI: "smoke-voice" }];
    window.SpeechSynthesisUtterance = function (text) { this.text = text; };
    const fake = {
      current: null,
      getVoices() { return window.__ttsVoices; },
      speak(u) { spoken.push(u.text); this.current = u; },
      cancel() { const u = this.current; this.current = null; if (u && u.onerror) u.onerror({ error: "canceled" }); },
      pause() {}, resume() {},
    };
    Object.defineProperty(window, "speechSynthesis", { configurable: true, value: fake });
    return true;
  })()`);
  await waitUntil(
    `!!document.querySelector('article[data-role="strike"] [data-action="read-aloud"]')`,
    "read-aloud control on an assistant reply",
  );
  const ttsRoles = await evaluate(`JSON.stringify([...document.querySelectorAll('[data-action="read-aloud"]')].map((b) => b.closest("article")?.getAttribute("data-role")))`);
  if (JSON.parse(ttsRoles).some((r) => r !== "strike")) {
    throw new Error(`read-aloud on non-assistant lines: ${ttsRoles}`);
  }
  await evaluate(`document.querySelector('article[data-role="strike"] [data-action="read-aloud"]').focus()`);
  await evaluate(`document.activeElement.click()`);
  await waitUntil(
    `window.__ttsSpoken.length === 1 && window.__ttsSpoken[0].length > 0 && !window.__ttsSpoken[0].includes("\u0060\u0060\u0060") && !!document.querySelector('article[data-role="strike"] .sx-tts[data-tts-playing="true"]') && document.activeElement.textContent.includes("Stop")`,
    "read-aloud plays and marks the line",
  );
  await evaluate(`(() => { const u = window.speechSynthesis.current; window.speechSynthesis.current = null; u.onend({}); return true; })()`);
  await waitUntil(
    `!document.querySelector('.sx-tts[data-tts-playing="true"]') && document.querySelector('article[data-role="strike"] [data-action="read-aloud"]').textContent.includes("Read aloud")`,
    "read-aloud clears when the engine finishes",
  );
  await evaluate(`window.__ttsVoices = []; document.querySelector('article[data-role="strike"] [data-action="read-aloud"]').click()`);
  await waitUntil(
    `!!document.querySelector('[data-tts-note]') && document.querySelector('[data-tts-note]').textContent.includes("no speech voice") && window.__ttsSpoken.length === 1`,
    "read-aloud explains when no voice is available",
  );

  // Mermaid diagrams (#1368). A seeded message with a flowchart (one
  // label carrying HTML/script markup), a sequence diagram and a broken
  // block: the valid ones render as SVG with the markup as inert text, the
  // broken one falls back to its source with the note, and the source
  // toggle and zoom controls round-trip.
  const fence = "```";
  const mermaidText = [
    "Mermaid smoke",
    "",
    `${fence}mermaid`,
    "flowchart TD",
    "  A[Start] --> B{Ok?}",
    "  B -->|yes| C[\"<img src=x onerror=alert(1)><b>inert</b>\"]",
    fence,
    "",
    `${fence}mermaid`,
    "sequenceDiagram",
    "  Alice->>Bob: ping",
    "  Bob-->>Alice: pong",
    fence,
    "",
    `${fence}mermaid`,
    "flowchart TD",
    "  A ?? B",
    fence,
  ].join("\n");
  const mermaidSeed = await fetch(`http://127.0.0.1:${port}/v1/ops`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ type: "user.input", data: { text: mermaidText } }),
  });
  if (!mermaidSeed.ok) {
    throw new Error(`seed mermaid user.input ${mermaidSeed.status} ${await mermaidSeed.text()}`);
  }
  const mermaidProbe = async () => {
    const raw = await evaluate(`(() => {
      const flow = document.querySelector('[data-mermaid="flowchart"]');
      const seq = document.querySelector('[data-mermaid="sequence"]');
      const failed = document.querySelector("[data-mermaid-failed]");
      const svg = flow ? flow.querySelector("svg") : null;
      return JSON.stringify({
        flow: !!svg,
        flowText: svg ? svg.textContent : "",
        injected: flow ? !!flow.querySelector("svg img, svg b, svg script, svg foreignObject") : false,
        seq: seq ? (seq.querySelector("svg") || {}).textContent || "" : "",
        failNote: failed ? (failed.querySelector(".mmd-fail-note") || {}).textContent || "" : "",
        failSource: failed ? !!failed.querySelector("pre.sx-code") : false,
        canvasHidden: flow ? flow.querySelector(".mmd-canvas").hidden : null,
        sourceHidden: flow ? flow.querySelector(".mmd-source").hidden : null,
        width: svg ? svg.getAttribute("width") : "",
        fit: flow ? flow.querySelector(".mmd-canvas").classList.contains("mmd-z2") : null,
        shown: svg ? Math.round(svg.getBoundingClientRect().width) : 0,
        styleAttr: svg ? svg.getAttribute("style") : null,
      });
    })()`);
    return JSON.parse(raw || "{}");
  };
  const mermaidShown = await waitFor(
    "mermaid flowchart, sequence and fallback in the transcript",
    async () => {
      const p = await mermaidProbe();
      return {
        ok: p.flow && p.flowText.includes("Start") && p.flowText.includes("<b>inert</b>") && !p.injected
          && p.seq.includes("ping") && p.seq.includes("pong")
          && p.failNote.includes("Diagram didn't parse") && p.failSource
          && p.canvasHidden === false && p.sourceHidden === true,
        ...p,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  if (!mermaidShown.flow || mermaidShown.injected) {
    throw new Error(`mermaid render check failed ${JSON.stringify(mermaidShown)}`);
  }
  await evaluate(`document.querySelector('[data-mermaid="flowchart"] [data-mermaid-toggle]').click()`);
  await waitFor(
    "mermaid source toggle shows the code block",
    async () => {
      const p = await mermaidProbe();
      return { ok: p.canvasHidden === true && p.sourceHidden === false, ...p };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector('[data-mermaid="flowchart"] [data-mermaid-toggle]').click()`);
  await waitFor(
    "mermaid toggle back to the diagram",
    async () => {
      const p = await mermaidProbe();
      return { ok: p.canvasHidden === false && p.sourceHidden === true && p.fit === true && p.styleAttr === null, ...p };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  const fitWidth = (await mermaidProbe()).shown;
  // The message row can re-render when its turn finishes (elapsed/usage
  // land), which remounts the block at Fit; click again while still at
  // Fit so the check measures zoom, not that remount.
  await waitFor(
    "mermaid zoom in sizes the diagram explicitly",
    async () => {
      let p = await mermaidProbe();
      if (p.fit) {
        await evaluate(`document.querySelector('[data-mermaid="flowchart"] [data-mermaid-zoom="in"]').click()`);
        p = await mermaidProbe();
      }
      return { ok: p.fit === false && p.shown > fitWidth, ...p };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector('[data-mermaid="flowchart"] [data-mermaid-zoom="fit"]').click()`);

  // Find in transcript (#1342). The seed above sent "read the makefile"
  // to the open session, so the transcript holds that word in the user
  // bubble and in the echo reply. Keyboard path only: Ctrl+F opens,
  // typing counts and highlights, Enter steps, a zero-match query is
  // reported plainly, and Escape closes and clears the highlights.
  await evaluate(
    `window.dispatchEvent(new KeyboardEvent("keydown", { key: "f", ctrlKey: true, bubbles: true, cancelable: true }))`,
  );
  await waitFor(
    "find bar to open on Ctrl+F",
    async () => ({ ok: (await evaluate(`!!document.getElementById("tf-search")`)) === true }),
    { timeout: STEP_MS, interval: 200 },
  );
  const tfType = async (value) => {
    const sent = await evaluate(`(() => {
      const el = document.getElementById("tf-search");
      if (!el) return "missing";
      const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
      proto.set.call(el, ${JSON.stringify(value)});
      el.dispatchEvent(new Event("input", { bubbles: true }));
      return "typed";
    })()`);
    if (sent !== "typed") {
      throw new Error(`find type failed: ${sent}`);
    }
  };
  const tfProbe = async () => {
    const raw = await evaluate(`(() => JSON.stringify({
      count: document.querySelector("[data-tf-count]") ? document.querySelector("[data-tf-count]").textContent.trim() : "",
      marks: document.querySelectorAll(".tf-mark").length,
      current: document.querySelectorAll(".tf-mark.tf-current").length,
      bar: !!document.getElementById("tf-search"),
    }))()`);
    return JSON.parse(raw || "{}");
  };
  await tfType("makefile");
  const tfFound = await waitFor(
    "find count and highlights for a transcript word",
    async () => {
      const p = await tfProbe();
      const m = /^(\d+)\/(\d+)$/.exec(p.count || "");
      return {
        ok: !!m && Number(m[2]) >= 1 && p.marks === Number(m[2]) && p.current === 1,
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  const tfTotal = Number(/^(\d+)\/(\d+)$/.exec(tfFound.count)[2]);
  if (tfTotal > 1) {
    await evaluate(
      `document.getElementById("tf-search").dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }))`,
    );
    await waitFor(
      "Enter to step to the next match",
      async () => {
        const p = await tfProbe();
        return { ok: p.count === `2/${tfTotal}` && p.current === 1, ...p };
      },
      { timeout: STEP_MS, interval: 200 },
    );
  }
  // Find options (#1409/#1419): both toggle chips exist, start
  // unpressed, and flip aria-pressed when clicked — in the transcript
  // bar here; the editor bar carries the same chips (host-tested
  // matcher, UAT covers its counts).
  const tfChips = await evaluate(`(() => JSON.stringify({
    aa: !!document.querySelector("[data-tf-match-case]"),
    ww: !!document.querySelector("[data-tf-whole-word]"),
    aaPressed: document.querySelector("[data-tf-match-case]")?.getAttribute("aria-pressed"),
    wwPressed: document.querySelector("[data-tf-whole-word]")?.getAttribute("aria-pressed"),
  }))()`);
  const chips = JSON.parse(tfChips || "{}");
  if (!chips.aa || !chips.ww || chips.aaPressed !== "false" || chips.wwPressed !== "false") {
    throw new Error(`find toggle chips missing or not default-off: ${tfChips}`);
  }
  await evaluate(`document.querySelector("[data-tf-match-case]").click()`);
  await evaluate(`document.querySelector("[data-tf-whole-word]").click()`);
  const tfChipsOn = await evaluate(`(() => JSON.stringify({
    aaPressed: document.querySelector("[data-tf-match-case]")?.getAttribute("aria-pressed"),
    wwPressed: document.querySelector("[data-tf-whole-word]")?.getAttribute("aria-pressed"),
  }))()`);
  const chipsOn = JSON.parse(tfChipsOn || "{}");
  if (chipsOn.aaPressed !== "true" || chipsOn.wwPressed !== "true") {
    throw new Error(`find toggle chips did not press: ${tfChipsOn}`);
  }
  await evaluate(`document.querySelector("[data-tf-match-case]").click()`);
  await evaluate(`document.querySelector("[data-tf-whole-word]").click()`);
  await tfType("zz-no-match-zz");
  await waitFor(
    "zero matches reported plainly, with no marks",
    async () => {
      const p = await tfProbe();
      return { ok: p.count === "No matches" && p.marks === 0, ...p };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(
    `document.getElementById("tf-search").dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }))`,
  );
  await waitFor(
    "Escape to close the find bar and clear highlights",
    async () => {
      const p = await tfProbe();
      return { ok: p.bar === false && p.marks === 0, ...p };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  // Git and Worktrees side tabs (#1270): click each tab and wait for its
  // panel's own marker — "Staged" section for Git, the displayed
  // session.childIsolation value for Worktrees. Condition-based waits,
  // like the rest of this hardened script. This runs while the boot live
  // session is still open: the panels read the live session's repo, and
  // the session-delete section below removes that session, after which
  // the git endpoints have no live CWD to read.
  const clickSideTab = async (label, needle) => {
    const clicked = await evaluate(`(() => {
      const btn = [...document.querySelectorAll(".side-tabs button")].find((b) => (b.textContent || "").includes(${JSON.stringify(label)}));
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
    if (clicked !== "clicked") {
      throw new Error(`side tab ${label} not found\n${await diagnostics()}`);
    }
    await waitFor(
      `${label} panel (${needle})`,
      textProbe((t) => t.includes(needle)),
      { timeout: WAIT_MS, interval: 250 },
    );
  };
  // Stage all / Unstage all (#1426): seed several untracked files BEFORE
  // the panel first loads, but only when the repo is completely clean —
  // Stage all would otherwise stage a developer's unrelated work when
  // this script runs in a dirty checkout. In CI the checkout is clean,
  // so the full batch flow below runs.
  const stageAllFiles = [
    "smoke-stage-all-1.txt",
    "smoke-stage-all-2.txt",
    "smoke-stage-all-3.txt",
  ];
  const gitStatusPaths = async () => {
    const res = await fetch(`http://127.0.0.1:${port}/v1/git/status`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) throw new Error(`git status HTTP ${res.status}`);
    const body = await res.json();
    const paths = (list) => (list || []).map((f) => f.path);
    return {
      staged: paths(body.staged),
      unstaged: paths(body.unstaged),
      untracked: paths(body.untracked),
    };
  };
  let stageAllSeeded = false;
  try {
    const before = await gitStatusPaths();
    if (
      before.staged.length === 0 &&
      before.unstaged.length === 0 &&
      before.untracked.length === 0
    ) {
      for (const name of stageAllFiles) {
        await writeFile(join(process.cwd(), name), `stage-all smoke ${name}\n`);
      }
      stageAllSeeded = true;
    }
  } catch {
    stageAllSeeded = false;
  }
  // #1341: Preview tab. A real loopback fixture server is picked by
  // typing its port. The pane labels the proxied destination, the
  // daemon proxies the fixture under the pane's sandboxed path, and a
  // port with no listener is reported in the pane, not left blank.
  {
    const fixture = createServer((req, res) => {
      res.writeHead(200, { "Content-Type": "text/html" });
      res.end("<h1>smoke preview fixture</h1>");
    });
    await new Promise((resolve) => fixture.listen(0, "127.0.0.1", resolve));
    const fixturePort = fixture.address().port;
    const closedServer = createServer();
    await new Promise((resolve) => closedServer.listen(0, "127.0.0.1", resolve));
    const closedPort = closedServer.address().port;
    await new Promise((resolve) => closedServer.close(resolve));
    try {
      await clickSideTab("Preview", "Listening on this gateway");
      const pickPort = async (n) => {
        await evaluate(`(() => {
          const input = document.querySelector('[data-preview-manual]');
          const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set;
          set.call(input, ${JSON.stringify(String(n))});
          input.dispatchEvent(new Event("input", { bubbles: true }));
          document.querySelector('[data-action="preview-open"]').click();
          return true;
        })()`);
      };
      await pickPort(fixturePort);
      await waitUntil(
        `!!document.querySelector('[data-preview-destination="127.0.0.1:${fixturePort}"]') && !!document.querySelector('iframe.preview-frame')`,
        "preview frame for the fixture port",
      );
      const frame = await evaluate(`JSON.stringify({
        src: document.querySelector('iframe.preview-frame').getAttribute("src"),
        sandbox: document.querySelector('iframe.preview-frame').getAttribute("sandbox"),
      })`);
      const { src, sandbox } = JSON.parse(frame);
      if (!sandbox || sandbox.includes("allow-same-origin") || !src.includes("/preview/")) {
        throw new Error(`preview frame attributes src=${src} sandbox=${sandbox}`);
      }
      const proxied = await fetch(new URL(src, `http://127.0.0.1:${port}`));
      const proxiedText = await proxied.text();
      const proxiedCSP = proxied.headers.get("content-security-policy") || "";
      if (!proxied.ok || !proxiedText.includes("smoke preview fixture") || !proxiedCSP.includes("sandbox")) {
        throw new Error(`preview proxy status=${proxied.status} csp=${proxiedCSP} body=${proxiedText.slice(0, 200)}`);
      }
      await pickPort(closedPort);
      await waitUntil(
        `!!document.querySelector('[data-preview-unreachable]') && document.body.innerText.includes("127.0.0.1:${closedPort}") && !document.querySelector('iframe.preview-frame')`,
        "preview reports a port with no listener",
      );
      await evaluate(`document.querySelector('[data-action="preview-stop"]').click()`);
      await waitUntil(`!!document.querySelector('[data-preview-empty]')`, "preview stopped");
    } finally {
      await new Promise((resolve) => fixture.close(resolve));
    }
  }
  await clickSideTab("Git", "Staged");
  // Pre-push review (#1487): the preview route sizes the diff and names the
  // read-only reviewer profile, a start without confirm never spawns (400),
  // and the Git tab's "Review this branch" panel renders the same state
  // with its cost notice. No reviewer is spawned here.
  {
    const revHeaders = { Authorization: `Bearer ${token}` };
    const revRes = await fetch(`http://127.0.0.1:${port}/v1/git/review`, { headers: revHeaders });
    if (!revRes.ok) {
      throw new Error(`branch review preview: HTTP ${revRes.status} ${await revRes.text()}`);
    }
    const rev = await revRes.json();
    if (!["ok", "empty", "refuse"].includes(rev.state) || rev.agent !== "branch-reviewer" || rev.turns !== 1) {
      throw new Error(`branch review preview shape: ${JSON.stringify(rev).slice(0, 300)}`);
    }
    const revStart = await fetch(`http://127.0.0.1:${port}/v1/git/review`, {
      method: "POST",
      headers: { ...revHeaders, "Content-Type": "application/json" },
      body: "{}",
    });
    if (revStart.status !== 400) {
      throw new Error(`branch review start without confirm: HTTP ${revStart.status} ${await revStart.text()}`);
    }
    await evaluate(`(() => { document.querySelector("[data-review-open]").click(); return true; })()`);
    await waitFor(
      `branch review notice renders state ${rev.state}`,
      async () => {
        const raw = await evaluate(`JSON.stringify((() => {
          const n = document.querySelector("[data-review-notice]");
          if (!n) return null;
          const run = n.querySelector("[data-review-run]");
          return {
            state: n.getAttribute("data-review-state"),
            size: (n.querySelector("[data-review-size]") || {}).textContent || "",
            runDisabled: run ? run.disabled : null,
          };
        })())`);
        const got = JSON.parse(raw || "null");
        const ok =
          !!got &&
          got.state === rev.state &&
          got.size.startsWith("Diff:") &&
          got.runDisabled === (rev.state !== "ok");
        return { ok, got };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    await evaluate(`(() => { document.querySelector("[data-review-open]").click(); return true; })()`);
    // Security mode (#1620): the toggle re-sizes with two turns (reviewer +
    // verifier) and names the verifier; the suppressions route answers for
    // the current branch (CI's detached checkout gets a plain 409). No
    // reviewer or verifier is spawned here.
    {
      const secRes = await fetch(`http://127.0.0.1:${port}/v1/git/review?mode=security`, { headers: revHeaders });
      const sec = await secRes.json();
      if (!secRes.ok || sec.mode !== "security" || sec.turns !== 2) {
        throw new Error(`security review preview: HTTP ${secRes.status} ${JSON.stringify(sec).slice(0, 300)}`);
      }
      const supRes = await fetch(`http://127.0.0.1:${port}/v1/git/review-suppressions`, { headers: revHeaders });
      if (![200, 409].includes(supRes.status)) {
        throw new Error(`review suppressions: HTTP ${supRes.status} ${await supRes.text()}`);
      }
      const noReason = await fetch(`http://127.0.0.1:${port}/v1/git/review-suppressions`, {
        method: "POST",
        headers: { ...revHeaders, "Content-Type": "application/json" },
        body: JSON.stringify({ path: "a.go", cwe: "CWE-89", line: 1, reason: " " }),
      });
      if (![400, 409].includes(noReason.status)) {
        throw new Error(`suppression without a reason accepted: HTTP ${noReason.status}`);
      }
      await evaluate(`(() => { document.querySelector("[data-review-open]").click(); return true; })()`);
      await evaluate(`(() => { document.querySelector("[data-review-mode-security]").click(); return true; })()`);
      await waitFor(
        "security review notice names the verifier",
        async () => {
          const raw = await evaluate(`JSON.stringify((() => {
            const n = document.querySelector("[data-review-notice]");
            return n ? { text: n.textContent || "", state: n.getAttribute("data-review-state") } : null;
          })())`);
          const got = JSON.parse(raw || "null");
          return { ok: !!got && got.text.includes("independent verifier") && got.text.includes("2 model turns") && got.state === sec.state, got };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      await evaluate(`(() => { document.querySelector("[data-review-mode-general]").click(); return true; })()`);
      await evaluate(`(() => { document.querySelector("[data-review-open]").click(); return true; })()`);
    }
  }
  // Ship to PR (#1340): the preview route answers with an honest state
  // (CI checks out a detached merge commit, so "blocked" with a detached
  // blocker is expected there), a create without confirmPush never pushes
  // or creates (409, or 200 only when it found an already-open PR), and
  // the Git tab's "Create pull request" panel renders that preview.
  {
    const shipHeaders = { Authorization: `Bearer ${token}` };
    const shipRes = await fetch(`http://127.0.0.1:${port}/v1/git/pr`, { headers: shipHeaders });
    if (!shipRes.ok) {
      throw new Error(`ship-to-pr preview: HTTP ${shipRes.status} ${await shipRes.text()}`);
    }
    const ship = await shipRes.json();
    if (!["ok", "blocked"].includes(ship.state) || !Array.isArray(ship.blockers)) {
      throw new Error(`ship-to-pr preview shape: ${JSON.stringify(ship).slice(0, 300)}`);
    }
    const shipCreate = await fetch(`http://127.0.0.1:${port}/v1/git/pr`, {
      method: "POST",
      headers: { ...shipHeaders, "Content-Type": "application/json" },
      body: JSON.stringify({ title: "smoke must not ship" }),
    });
    const shipCreateText = await shipCreate.text();
    const foundExisting = shipCreate.status === 200 && shipCreateText.includes('"existing":true');
    if (shipCreate.status !== 409 && !foundExisting) {
      throw new Error(`ship-to-pr create without confirmPush: HTTP ${shipCreate.status} ${shipCreateText.slice(0, 300)}`);
    }
    await evaluate(`(() => { document.querySelector("[data-ship-pr-open]").click(); return true; })()`);
    await waitFor(
      `ship-to-pr panel renders preview state ${ship.state}`,
      async () => {
        const raw = await evaluate(`JSON.stringify((() => {
          const panel = document.querySelector("[data-ship-pr-panel]");
          if (!panel) return null;
          const create = panel.querySelector("[data-ship-pr-create]");
          return {
            branch: !!panel.querySelector("[data-ship-pr-branch]"),
            blockers: (panel.querySelector("[data-ship-pr-blockers]") || {}).textContent || "",
            existing: !!panel.querySelector("[data-ship-pr-existing]"),
            createDisabled: create ? create.disabled : null,
            error: (panel.querySelector("[data-ship-pr-error]") || {}).textContent || "",
          };
        })())`);
        const got = JSON.parse(raw || "null");
        let ok = !!got && got.branch && !got.error;
        if (ok && ship.state === "blocked" && !ship.existing) {
          ok = got.blockers.length > 0 && got.createDisabled !== false;
        }
        if (ok && ship.state === "ok" && !ship.existing) {
          // Push/dirty confirmations start unticked, so create starts disabled
          // whenever a push is needed.
          ok = ship.needsPush ? got.createDisabled === true : got.createDisabled !== null;
        }
        return { ok, got };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    await evaluate(`(() => { document.querySelector("[data-ship-pr-open]").click(); return true; })()`);
  }
  // Git panel small wins (#1427/#1432/#1435): the path filter exists,
  // an active no-match query flips every section's empty text to
  // "No files match.", and the log rows carry full-hash copy hooks.
  await waitFor(
    "git path filter input (data-git-filter)",
    async () => {
      const n = await evaluate(`document.querySelectorAll("[data-git-filter]").length`);
      return { ok: n === 1, n };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const el = document.querySelector("[data-git-filter]");
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    setter.call(el, "zz-no-match-zz");
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return true;
  })()`);
  await waitFor(
    "active git filter shows No files match",
    textProbe((t) => t.includes("No files match.")),
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const el = document.querySelector("[data-git-filter]");
    const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    setter.call(el, "");
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return true;
  })()`);
  const logRes = await fetch(`http://127.0.0.1:${port}/v1/git/log?limit=30`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!logRes.ok) {
    throw new Error(`git log route failed: HTTP ${logRes.status} (copy-hash/show-more unchecked)`);
  }
  {
    const logBody = await logRes.json();
    const commitCount = (logBody.commits || []).length;
    if (commitCount > 0) {
      await evaluate(`(() => {
        const panel = document.getElementById("git-panel");
        panel.focus();
        panel.dispatchEvent(new KeyboardEvent("keydown", { key: "l", bubbles: true, cancelable: true }));
        return true;
      })()`);
      await waitFor(
        "git log copy-hash hooks carry full 40-char hashes",
        async () => {
          const raw = await evaluate(`JSON.stringify([...document.querySelectorAll("[data-git-copy-hash]")].map((b) => b.getAttribute("data-git-copy-hash") || ""))`);
          const hashes = JSON.parse(raw || "[]");
          return { ok: hashes.length > 0 && hashes.every((h) => h.length === 40), count: hashes.length };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      if (commitCount === 30) {
        await waitFor(
          "git log Show more hook at a full 30 page",
          async () => {
            const n = await evaluate(`document.querySelectorAll("[data-git-log-more]").length`);
            return { ok: n === 1, n };
          },
          { timeout: STEP_MS, interval: 250 },
        );
      }
      // Toggle the log back closed so later Git-tab probes see the
      // default panel.
      await evaluate(`(() => {
        const panel = document.getElementById("git-panel");
        panel.focus();
        panel.dispatchEvent(new KeyboardEvent("keydown", { key: "l", bubbles: true, cancelable: true }));
        return true;
      })()`);
    }
  }
  // Commit detail (#1484): the daemon show route returns the real
  // commit, and clicking a log row renders its header, files and patch.
  const showLogRes = await fetch(`http://127.0.0.1:${port}/v1/git/log?limit=1`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!showLogRes.ok) {
    throw new Error(`git log route failed: HTTP ${showLogRes.status} (commit detail unchecked)`);
  }
  const headCommit = ((await showLogRes.json()).commits || [])[0];
  if (headCommit) {
    const showRes = await fetch(
      `http://127.0.0.1:${port}/v1/git/show?ref=${encodeURIComponent(headCommit.hash)}`,
      { headers: { Authorization: `Bearer ${token}` } },
    );
    if (!showRes.ok) {
      throw new Error(`git show route: HTTP ${showRes.status} ${await showRes.text()}`);
    }
    const shown = await showRes.json();
    if (shown.hash !== headCommit.hash || !Array.isArray(shown.files)) {
      throw new Error(`git show payload mismatch: ${JSON.stringify(shown).slice(0, 300)}`);
    }
    const badRef = await fetch(`http://127.0.0.1:${port}/v1/git/show?ref=--output%3D%2Ftmp%2Fx`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (badRef.status !== 400) {
      throw new Error(`git show accepted an option-looking ref: HTTP ${badRef.status}`);
    }
    const toggleLog = `(() => {
      const panel = document.getElementById("git-panel");
      panel.focus();
      panel.dispatchEvent(new KeyboardEvent("keydown", { key: "l", bubbles: true, cancelable: true }));
      return true;
    })()`;
    await evaluate(toggleLog);
    await waitFor(
      "git log rows are clickable commit buttons",
      async () => {
        const n = await evaluate(`document.querySelectorAll("[data-git-commit]").length`);
        return { ok: n > 0, n };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    await evaluate(`(() => { document.querySelector("[data-git-commit]").click(); return true; })()`);
    await waitFor(
      "git commit detail renders header, files and patch",
      async () => {
        const raw = await evaluate(`JSON.stringify((() => {
          const d = document.querySelector("[data-git-commit-detail]");
          if (!d) return null;
          return {
            hash: d.getAttribute("data-git-commit-detail"),
            text: d.textContent || "",
            patch: !!d.querySelector("pre.git-diff"),
            files: d.querySelectorAll(".git-commit-files li").length,
          };
        })())`);
        const got = JSON.parse(raw || "null");
        const ok = !!got &&
          got.hash === headCommit.hash &&
          got.text.includes(headCommit.hash) &&
          got.patch &&
          got.files === shown.files.length;
        return { ok, got: got && { ...got, text: got.text.slice(0, 120) } };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    // Clicking the open commit closes the detail.
    await evaluate(`(() => { document.querySelector("[data-git-commit]").click(); return true; })()`);
    await waitFor(
      "clicking the open commit closes the detail",
      async () => {
        const n = await evaluate(`document.querySelectorAll("[data-git-commit-detail]").length`);
        return { ok: n === 0, n };
      },
      { timeout: STEP_MS, interval: 250 },
    );
    // Enter on a focused commit row must keep the button's native
    // activation: the panel's Enter-opens-diff shortcut may not swallow
    // it (a synthetic keydown cannot click, so assert it stays
    // un-prevented).
    const enterPrevented = await evaluate(`(() => {
      const b = document.querySelector("[data-git-commit]");
      b.focus();
      const ev = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });
      b.dispatchEvent(ev);
      return ev.defaultPrevented;
    })()`);
    if (enterPrevented !== false) {
      throw new Error(`Enter on a commit row was swallowed by the panel shortcut (defaultPrevented=${enterPrevented})`);
    }
    await evaluate(toggleLog);
  }
  // Branch changes (#1450): the panel's Branch view must agree with the
  // daemon route — a summary for "ok", the named empty state otherwise
  // (CI checks out a detached merge commit, so "detached" is expected
  // there).
  const bcRes = await fetch(`http://127.0.0.1:${port}/v1/git/branch-changes`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!bcRes.ok) {
    throw new Error(`branch-changes route: HTTP ${bcRes.status} ${await bcRes.text()}`);
  }
  const bcBody = await bcRes.json();
  const bcBad = await fetch(`http://127.0.0.1:${port}/v1/git/branch-changes?base=-p`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (bcBad.status !== 400) {
    throw new Error(`branch-changes accepted an option-looking base: HTTP ${bcBad.status}`);
  }
  await evaluate(`(() => { document.querySelector("[data-git-branch-toggle]").click(); return true; })()`);
  await waitFor(
    `git Branch view renders daemon state ${bcBody.state}`,
    async () => {
      const raw = await evaluate(`JSON.stringify((() => {
        const root = document.querySelector("[data-git-branch-changes]");
        if (!root) return null;
        const st = root.querySelector("[data-git-branch-state]");
        return {
          state: st ? st.getAttribute("data-git-branch-state") : "ok",
          summary: !!root.querySelector("[data-git-branch-summary]"),
          picker: !!root.querySelector("[data-git-branch-base]"),
        };
      })())`);
      const got = JSON.parse(raw || "null");
      const ok = !!got && got.picker && got.state === bcBody.state && (bcBody.state !== "ok" || got.summary);
      return { ok, got };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`(() => { document.querySelector("[data-git-branch-toggle]").click(); return true; })()`);
  // Branch switcher regression (#1314): the rendered <select> value must
  // equal the daemon's current branch once the options exist. The defect
  // was prop:value being applied before the options rendered, so the
  // browser fell back to the first option and displayed a branch the repo
  // was not on. Only checked when the daemon reports a current branch
  // (a detached checkout has none); option count is reported, not
  // required, since a fresh CI checkout may carry a single branch.
  const branchesRes = await fetch(`http://127.0.0.1:${port}/v1/git/branches`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  if (!branchesRes.ok) {
    throw new Error(`branch-select regression: branches HTTP ${branchesRes.status}`);
  }
  const branchesBody = await branchesRes.json();
  const currentBranch =
    branchesBody.current ||
    (branchesBody.branches || []).find((b) => b.current)?.name ||
    "";
  if (currentBranch) {
    await waitFor(
      "branch select value equals current branch",
      async () => {
        const raw = await evaluate(
          `(() => { const s = document.querySelector(".git-branch"); return s ? s.textContent : ""; })()`,
        );
        const shown = String(raw || "").trim();
        return { ok: shown === currentBranch || shown.endsWith(currentBranch), shown, currentBranch };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  }
  // Git diff expand (#1428): the seeded untracked file's diff is ~505 lines.
  // The panel must render only the first 400 with a "Show all N lines"
  // button (data-git-diff-expand); clicking it renders the tail in place;
  // re-opening the same diff starts capped again (per-diff reset).
  const diffProbe = async () => {
    const raw = await evaluate(`(() => {
      const pre = document.querySelector(".git-diff");
      const btn = document.querySelector("[data-git-diff-expand]");
      return JSON.stringify({
        lines: pre ? pre.children.length : -1,
        btn: btn ? (btn.textContent || "").trim() : "",
        hasTail: pre ? pre.textContent.includes("smoke-diff-line-500") : false,
        has401: pre ? pre.textContent.includes("smoke-diff-line-396") : false,
      });
    })()`);
    return JSON.parse(raw || "{}");
  };
  const clickSeedRow = async () => {
    // Rows load async after the Git tab opens; poll for the seed row like
    // every other wait in this script instead of a single-shot query
    // (a one-shot query raced the status load on CI: "seed row missing").
    await waitFor(
      "git diff expand seed row (smoke-git-diff-1428.txt)",
      async () => {
        const v = await evaluate(`(() => {
          const b = [...document.querySelectorAll(".git-file")].find((x) => (x.textContent || "").includes("smoke-git-diff-1428.txt"));
          if (!b) return "missing";
          b.click();
          return "clicked";
        })()`);
        return { ok: v === "clicked", v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
  };
  await clickSeedRow();
  await waitFor(
    "git diff capped at 400 lines with a Show all N lines button",
    async () => {
      const p = await diffProbe();
      return { ok: p.lines === 400 && p.btn.startsWith("Show all ") && p.btn.endsWith(" lines") && !p.has401, ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const expandClicked = await evaluate(
    `(() => { const b = document.querySelector("[data-git-diff-expand]"); if (!b) return "missing"; b.click(); return "clicked"; })()`,
  );
  if (expandClicked !== "clicked") {
    throw new Error(`git diff expand: button ${expandClicked}`);
  }
  await waitFor(
    "expanded git diff renders lines past 400 through the tail",
    async () => {
      const p = await diffProbe();
      return { ok: p.lines > 400 && p.hasTail && p.btn === "", ...p };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await clickSeedRow();
  await waitFor(
    "re-opened git diff starts capped again (per-diff reset)",
    async () => {
      const p = await diffProbe();
      return { ok: p.lines === 400 && p.btn.startsWith("Show all "), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  if (stageAllSeeded) {
    const batchButtons = async () => {
      const raw = await evaluate(`(() => {
        const s = document.querySelector("[data-git-stage-all]");
        const u = document.querySelector("[data-git-unstage-all]");
        return JSON.stringify({
          stage: s ? s.disabled : null,
          unstage: u ? u.disabled : null,
        });
      })()`);
      return JSON.parse(raw || "{}");
    };
    const clickBatch = async (selector) => {
      const v = await evaluate(`(() => {
        const b = document.querySelector(${JSON.stringify(selector)});
        if (!b) return "missing";
        if (b.disabled) return "disabled";
        b.click();
        return "clicked";
      })()`);
      if (v !== "clicked") throw new Error(`stage-all smoke: ${selector} -> ${v}`);
    };
    try {
      await waitFor(
        "Stage all enabled and Unstage all disabled with only untracked files",
        async () => {
          const b = await batchButtons();
          return { ok: b.stage === false && b.unstage === true, ...b };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      await clickBatch("[data-git-stage-all]");
      await waitFor(
        "Stage all moves every seeded file to Staged in one batch",
        async () => {
          const s = await gitStatusPaths();
          return {
            ok: stageAllFiles.every((f) => s.staged.includes(f)),
            ...s,
          };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      await waitFor(
        "panel flips the batch buttons after Stage all",
        async () => {
          const b = await batchButtons();
          return { ok: b.stage === true && b.unstage === false, ...b };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      await clickBatch("[data-git-unstage-all]");
      await waitFor(
        "Unstage all moves every seeded file back to Untracked",
        async () => {
          const s = await gitStatusPaths();
          return {
            ok:
              stageAllFiles.every((f) => s.untracked.includes(f)) &&
              !stageAllFiles.some((f) => s.staged.includes(f)),
            ...s,
          };
        },
        { timeout: STEP_MS, interval: 250 },
      );
    } finally {
      // Restore the clean repo even when an assertion above fails:
      // unstage any seeded file still staged, then delete the seeds.
      try {
        const s = await gitStatusPaths();
        const stillStaged = stageAllFiles.filter((f) => s.staged.includes(f));
        if (stillStaged.length > 0) {
          await fetch(`http://127.0.0.1:${port}/v1/git/unstage`, {
            method: "POST",
            headers: auth,
            body: JSON.stringify({ paths: stillStaged }),
          });
        }
      } catch {
        // best-effort cleanup only; the assertion failure already reports
      }
      for (const name of stageAllFiles) {
        await rm(join(process.cwd(), name), { force: true });
      }
    }
  }
  // Stash (#1471): the list always comes from the daemon; on a clean
  // repo, seed one untracked file, stash it from the panel (with the
  // include-untracked opt-in and a message), see the row, Apply it back,
  // then drop it via the API and delete the seed.
  const stashFile = "smoke-stash-1471.txt";
  const stashMsg = "smoke-stash-1471";
  const stashListApi = async () => {
    const r = await fetch(`http://127.0.0.1:${port}/v1/git/stash`, { headers: auth });
    if (!r.ok) throw new Error(`git stash list HTTP ${r.status}`);
    return (await r.json()).stashes || [];
  };
  const stashBefore = await stashListApi();
  const stashStatus = await gitStatusPaths();
  const stashClean =
    stashStatus.staged.length === 0 &&
    stashStatus.unstaged.length === 0 &&
    stashStatus.untracked.length === 0;
  if (!stashClean) {
    console.log("webui-smoke: repo not clean; stash create/apply check skipped");
  } else {
    let created = null;
    try {
      await writeFile(join(process.cwd(), stashFile), "stash smoke\n");
      await waitUntil(`(() => {
        const cmd = document.querySelector('[data-command-id="git.refresh"]');
        if (cmd) { cmd.click(); return true; }
        if (!document.querySelector('[data-nav="palette-dialog"]')) {
          document.querySelector('[data-nav="palette"]')?.click();
        }
        return false;
      })()`, "git.refresh via palette");
      await evaluate(`(() => {
        const msg = document.querySelector("[data-git-stash-message]");
        const setter = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
        setter.call(msg, ${JSON.stringify(stashMsg)});
        msg.dispatchEvent(new Event("input", { bubbles: true }));
        const box = document.querySelector("[data-git-stash-untracked]");
        if (!box.checked) { box.checked = true; box.dispatchEvent(new Event("change", { bubbles: true })); }
        return true;
      })()`);
      await waitFor(
        "Stash changes enabled for an untracked-only tree with the opt-in",
        async () => {
          const v = await evaluate(`(() => {
            const b = document.querySelector("[data-git-stash-push]");
            if (!b) return "missing";
            if (b.disabled) return "disabled";
            b.click();
            return "clicked";
          })()`);
          return { ok: v === "clicked", v };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      await waitFor(
        "panel stash appears in the daemon list and the panel",
        async () => {
          const list = await stashListApi();
          const top = list[0];
          if (list.length === stashBefore.length + 1 && top && top.message.includes(stashMsg)) {
            created = top;
          }
          const shown = created
            ? await evaluate(`!!document.querySelector('[data-git-stash-row="${created.hash}"]')`)
            : false;
          return { ok: !!created && shown === true, count: list.length };
        },
        { timeout: STEP_MS, interval: 250 },
      );
      const stashedAway = await gitStatusPaths();
      if (stashedAway.untracked.includes(stashFile)) {
        throw new Error("stash smoke: seeded file still present after Stash changes");
      }
      await evaluate(`(() => { document.querySelector('[data-git-stash-row="${created.hash}"] [data-git-stash-apply]').click(); return true; })()`);
      await waitFor(
        "Apply restores the stashed file and keeps the stash",
        async () => {
          const s = await gitStatusPaths();
          const list = await stashListApi();
          return { ok: s.untracked.includes(stashFile) && list.some((x) => x.hash === created.hash) };
        },
        { timeout: STEP_MS, interval: 250 },
      );
    } finally {
      try {
        if (created) {
          const list = await stashListApi();
          const cur = list.find((x) => x.hash === created.hash);
          if (cur) {
            await fetch(`http://127.0.0.1:${port}/v1/git/stash/drop`, {
              method: "POST",
              headers: auth,
              body: JSON.stringify({ index: cur.index, hash: cur.hash, confirm: "drop-stash" }),
            });
          }
        }
      } catch {
        // best-effort cleanup only
      }
      await rm(join(process.cwd(), stashFile), { force: true });
    }
  }
  // Review queue (#1324): seed two separate edits in a tracked file,
  // accept nothing, reject the first hunk (two taps: Reject, then
  // "Revert this hunk?") and require it reverted on disk, comment on the
  // second hunk, then Send review and require exactly one transcript
  // turn labelled "Review". Runs only when the seed file is unmodified
  // so a developer's own edits are never touched; restored in finally.
  const reviewSeed = join(process.cwd(), "AGENTS.md");
  const reviewOriginal = await readFile(reviewSeed, "utf8");
  const reviewStatus = await gitStatusPaths();
  if (reviewStatus.unstaged.includes("AGENTS.md") || reviewStatus.staged.includes("AGENTS.md")) {
    console.log("review queue smoke: skipped (AGENTS.md already modified)");
  } else {
    try {
      const seeded = reviewOriginal.split("\n");
      seeded[0] = "# strike-cli (smoke-review-first-1324)";
      seeded.push("smoke-review-second-1324");
      await writeFile(reviewSeed, seeded.join("\n"));
      await clickSideTab("Review", "Review queue");
      const reviewProbe = async () => {
        const raw = await evaluate(`(() => {
          const file = document.querySelector('[data-rq-file="AGENTS.md"]');
          const hunks = file ? [...file.querySelectorAll("[data-rq-hunk]")] : [];
          const send = document.querySelector(".rq-send-btn");
          return JSON.stringify({
            hunks: hunks.length,
            first: hunks[0] ? hunks[0].textContent.includes("smoke-review-first-1324") : false,
            summary: (document.querySelector("[data-rq-summary]") || {}).textContent || "",
            sendDisabled: send ? send.disabled : null,
            note: (document.querySelector(".rq-note") || {}).textContent || "",
          });
        })()`);
        return JSON.parse(raw || "{}");
      };
      const reviewClick = async (expr, label) => {
        await waitFor(
          label,
          async () => {
            const v = await evaluate(`(() => { const b = ${expr}; if (!b) return "missing"; if (b.disabled) return "disabled"; b.click(); return "clicked"; })()`);
            return { ok: v === "clicked", v };
          },
          { timeout: WAIT_MS, interval: 250 },
        );
      };
      await waitFor(
        "review queue lists two AGENTS.md hunks, send disabled",
        async () => {
          const p = await reviewProbe();
          return { ok: p.hunks === 2 && p.first && p.sendDisabled === true && p.summary.includes("0 comments"), ...p };
        },
        { timeout: WAIT_MS, interval: 250 },
      );
      const firstHunk = `document.querySelector('[data-rq-file="AGENTS.md"] [data-rq-hunk] .rq-reject')`;
      await reviewClick(firstHunk, "review reject (first tap)");
      await waitFor(
        "review reject asks for confirmation",
        async () => {
          const label = await evaluate(`(${firstHunk} || {}).textContent || ""`);
          return { ok: String(label).includes("Revert this hunk?"), label };
        },
        { timeout: STEP_MS, interval: 200 },
      );
      await reviewClick(firstHunk, "review reject (confirm)");
      await waitFor(
        "rejected hunk reverted on disk and listed",
        async () => {
          const disk = await readFile(reviewSeed, "utf8");
          const p = await reviewProbe();
          return {
            ok: !disk.includes("smoke-review-first-1324") && disk.includes("smoke-review-second-1324") && p.hunks === 1 && p.summary.includes("1 rejected hunk"),
            ...p,
          };
        },
        { timeout: WAIT_MS, interval: 250 },
      );
      await reviewClick(`document.querySelector('[data-rq-file="AGENTS.md"] .rq-comment-open')`, "review comment open");
      await waitFor(
        "review comment composer opens with a line range",
        async () => {
          const v = await evaluate(`(() => {
            const ta = document.querySelector(".rq-composer textarea");
            const start = document.querySelector('.rq-composer input[aria-label="Start line"]');
            if (!ta || !start || !start.value) return "missing";
            ta.value = "smoke review comment 1324";
            ta.dispatchEvent(new Event("input", { bubbles: true }));
            return "typed";
          })()`);
          return { ok: v === "typed", v };
        },
        { timeout: STEP_MS, interval: 200 },
      );
      await reviewClick(`document.querySelector(".rq-composer .rq-save")`, "review add comment");
      await waitFor(
        "review comment listed and send enabled",
        async () => {
          const p = await reviewProbe();
          const listed = await evaluate(`[...document.querySelectorAll(".rq-comment")].some((c) => c.textContent.includes("smoke review comment 1324"))`);
          return { ok: listed === true && p.summary.includes("1 comment") && p.sendDisabled === false, listed, ...p };
        },
        { timeout: WAIT_MS, interval: 250 },
      );
      const reviewTurns = async () =>
        Number(
          await evaluate(
            `[...document.querySelectorAll('article[data-role="you"] .sx-who')].filter((e) => (e.textContent || "").trim() === "Review").length`,
          ),
        );
      const turnsBefore = await reviewTurns();
      await reviewClick(`document.querySelector(".rq-send-btn")`, "review send");
      await waitFor(
        "review arrives as one transcript turn labelled Review and the queue clears",
        async () => {
          const turns = await reviewTurns();
          const p = await reviewProbe();
          return { ok: turns === turnsBefore + 1 && p.summary.includes("0 comments · 0 rejected hunks"), turns, turnsBefore, ...p };
        },
        { timeout: WAIT_MS, interval: 250 },
      );
      // Let the echo turn the review started finish before later sections.
      await waitFor(
        "review turn completes",
        async () => {
          const res = await fetch(`http://127.0.0.1:${port}/v1/review`, { headers: { Authorization: `Bearer ${token}` } });
          const body = res.ok ? await res.json() : {};
          return { ok: res.ok && body.busy === false, status: res.status, busy: body.busy };
        },
        { timeout: WAIT_MS, interval: 250 },
      );
    } finally {
      await writeFile(reviewSeed, reviewOriginal);
    }
    await clickSideTab("Git", "Staged");
  }
  await waitUntil(`(() => {
    if ((document.body.innerText || "").includes("session.childIsolation")) return true;
    const cmd = document.querySelector('[data-command-id="panel-worktrees"]');
    if (cmd) { cmd.click(); return false; }
    if (!document.querySelector('[data-nav="palette-dialog"]')) {
      document.querySelector('[data-nav="palette"]')?.click();
    }
    return false;
  })()`, "worktrees panel via palette");

  // Single ask surface (#1424 review, #1319 Q3): ONE pending permission
  // ask must render EXACTLY ONE decision UI across both ask containers
  // — the ApprovalCards stream (.approvals-card) and the composer
  // transcript-asks bar (.composer-ask, removed). Seed a model-tool ask
  // via the ops API ("run echo ..." makes the echo provider call bash),
  // count both containers, then resolve through the card's Deny and
  // require both containers to clear.
  const singleAskSeed = await fetch(`http://127.0.0.1:${port}/v1/ops`, {
    method: "POST",
    headers: apprAuth,
    body: JSON.stringify({ type: "user.input", data: { text: "run echo single-ask-smoke-1424" } }),
  });
  if (!singleAskSeed.ok) {
    throw new Error(`single-ask seed ${singleAskSeed.status} ${await singleAskSeed.text()}`);
  }
  const askCounts = async () => {
    const raw = await evaluate(`(() => JSON.stringify({
      cards: document.querySelectorAll(".approvals-card").length,
      composerAsks: document.querySelectorAll(".composer-ask").length,
      composerBar: document.querySelectorAll('[data-composer-triggers="asks"]').length,
    }))()`);
    return JSON.parse(raw || "{}");
  };
  const singleAsk = await waitFor(
    "one pending ask renders exactly one ask row (ApprovalCards only, no composer bar)",
    async () => {
      const c = await askCounts();
      return { ok: c.cards + c.composerAsks === 1 && c.cards === 1 && c.composerBar === 0, ...c };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  if (singleAsk.cards !== 1 || singleAsk.composerAsks !== 0) {
    throw new Error(`single-ask count wrong: ${JSON.stringify(singleAsk)}`);
  }
  const singleAskDenied = await evaluate(`(() => {
    const b = [...document.querySelectorAll(".approvals-card button")].find((x) => (x.textContent || "").includes("Deny"));
    if (!b) return "no-deny";
    b.click();
    return "denied";
  })()`);
  if (singleAskDenied !== "denied") {
    throw new Error(`single-ask Deny button missing (${singleAskDenied})`);
  }
  await waitFor(
    "single ask clears from both containers after Deny",
    async () => {
      const c = await askCounts();
      return { ok: c.cards + c.composerAsks === 0, ...c };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  // Session rewind (#1336): the prompt's Rewind… action opens a preview
  // fed by the daemon; the bash warning blocks a code restore until it is
  // acknowledged; Cancel changes nothing; a conversation-only rewind
  // drops the turn on the daemon and puts the prompt back in the composer
  // without sending it.
  {
    const rewindPoints = async () => {
      const res = await fetch(`http://127.0.0.1:${port}/v1/sessions/rewind-session/rewind_points`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      return ((await res.json()).points || []).length;
    };
    if ((await rewindPoints()) !== 2) throw new Error("rewind fixture should start with 2 turns");
    const rowRW = `[data-session-id="rewind-session"]`;
    await waitFor(
      "rewind fixture row",
      async () => ({ ok: (await evaluate(`!!document.querySelector(${JSON.stringify(rowRW)})`)) === true }),
      { timeout: STEP_MS, interval: 250 },
    );
    await evaluate(`document.querySelector(${JSON.stringify(rowRW)}).querySelector(".ss-main").click()`);
    const rewindBtn = `[data-rewind-keep="8"]`;
    await waitFor(
      "Rewind… action on the second prompt",
      async () => ({ ok: (await evaluate(`!!document.querySelector(${JSON.stringify(rewindBtn)})`)) === true }),
      { timeout: WAIT_MS, interval: 250 },
    );
    const rwDialog = async () =>
      JSON.parse(
        (await evaluate(`(() => {
          const d = document.querySelector("[data-rewind='dialog']");
          const c = document.querySelector("[data-rewind='confirm']");
          return JSON.stringify({
            open: !!d,
            text: d ? d.innerText : "",
            confirmDisabled: c ? c.disabled : null,
            blocked: document.querySelector("[data-rewind='blocked']")?.innerText || "",
          });
        })()`)) || "{}",
      );
    await evaluate(`document.querySelector(${JSON.stringify(rewindBtn)}).click()`);
    const shown = await waitFor(
      "rewind preview with the uncovered bash warning",
      async () => {
        const d = await rwDialog();
        return { ok: d.open && d.text.includes("Rewind to before turn 2?") && d.text.includes("bash"), ...d };
      },
      { timeout: WAIT_MS, interval: 200 },
    );
    if (!shown.text.includes("db/schema.sql") || !shown.text.includes("Restore code only")) {
      throw new Error(`rewind preview incomplete: ${shown.text.slice(0, 400)}`);
    }
    if (shown.confirmDisabled !== true || !shown.blocked.includes("Acknowledge")) {
      throw new Error(`uncovered warning did not block the code restore: ${JSON.stringify(shown).slice(0, 400)}`);
    }
    await evaluate(`document.querySelector("[data-rewind='cancel']").click()`);
    await waitFor("rewind dialog closes on cancel", async () => ({ ok: !(await rwDialog()).open }), {
      timeout: STEP_MS,
      interval: 200,
    });
    if ((await rewindPoints()) !== 2) throw new Error("cancelled rewind changed the session");
    await evaluate(`document.querySelector(${JSON.stringify(rewindBtn)}).click()`);
    await waitFor(
      "rewind preview reopens",
      async () => ({ ok: (await rwDialog()).text.includes("Rewind to before turn 2?") }),
      { timeout: WAIT_MS, interval: 200 },
    );
    await evaluate(`document.querySelector("[data-rewind-mode='conversation'] input").click()`);
    await waitFor(
      "conversation-only rewind is not blocked",
      async () => ({ ok: (await rwDialog()).confirmDisabled === false }),
      { timeout: STEP_MS, interval: 200 },
    );
    await evaluate(`document.querySelector("[data-rewind='confirm']").click()`);
    await waitFor(
      "rewind drops the turn and returns the prompt to the composer",
      async () => {
        const points = await rewindPoints();
        const composer = (await evaluate(`document.querySelector("#composer-input")?.value || ""`)) || "";
        return { ok: points === 1 && composer.includes("run the migration script"), points, composer };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    await evaluate(`(() => {
      const el = document.querySelector("#composer-input");
      const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
      proto.set.call(el, "");
      el.dispatchEvent(new InputEvent("input", { bubbles: true }));
    })()`);
  }

  // Global transcript search (#1355): the palette opens the overlay; a
  // query with no matches says so; a content match in a session shows a
  // highlighted snippet; picking it opens that session and hands the
  // query to find-in-transcript.
  {
    const api = await fetch(`http://127.0.0.1:${port}/v1/search?q=coral%20bookmark`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const body = await api.json();
    if (!api.ok || !(body.hits || []).some((h) => h.id === "beta-session")) {
      throw new Error(`global search API ${api.status} ${JSON.stringify(body).slice(0, 300)}`);
    }
    await runPalette("Search all sessions");
    // The command takes an optional query, so the first Enter opens its
    // argument prompt; a second Enter submits it empty and opens the overlay.
    await evaluate(`(() => {
      const input = document.querySelector('[data-nav="palette-dialog"] input');
      input?.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      return true;
    })()`);
    await waitFor(
      "global search overlay",
      async () => ({ ok: (await evaluate(`!!document.querySelector("#gs-input")`)) === true }),
      { timeout: STEP_MS, interval: 150 },
    );
    const gsSearch = async (q) => {
      await evaluate(`(() => {
        const input = document.querySelector("#gs-input");
        const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
        proto.set.call(input, ${JSON.stringify(q)});
        input.dispatchEvent(new InputEvent("input", { bubbles: true }));
        document.querySelector("[data-gs-submit]").click();
        return true;
      })()`);
    };
    await gsSearch("zzqx nothing matches this");
    await waitFor(
      "global search no-results state",
      async () => {
        const t = (await evaluate(`document.querySelector("[data-gs-status]")?.innerText || ""`)) || "";
        return { ok: t.startsWith("No matches in"), t };
      },
      { timeout: WAIT_MS, interval: 200 },
    );
    await gsSearch("coral bookmark");
    const hitInfo = await waitFor(
      "global search hit with highlighted snippet",
      async () => {
        const raw = await evaluate(`(() => {
          const hit = document.querySelector('[data-gs-hit="beta-session"]');
          return JSON.stringify({ found: !!hit, mark: hit?.querySelector("mark")?.textContent || "" });
        })()`);
        const v = JSON.parse(raw || "{}");
        return { ok: v.found && v.mark.toLowerCase() === "coral bookmark", ...v };
      },
      { timeout: WAIT_MS, interval: 200 },
    );
    if (!hitInfo.found) throw new Error("global search hit missing");
    await evaluate(`document.querySelector('[data-gs-hit="beta-session"]').click()`);
    await waitFor(
      "global search opens the session and hands off to find-in-transcript",
      async () => {
        const raw = await evaluate(`(() => JSON.stringify({
          overlay: !!document.querySelector("#gs-input"),
          selected: document.querySelector('[data-session-id][aria-selected="true"]')?.getAttribute("data-session-id") || "",
          find: document.querySelector("#tf-search")?.value || "",
        }))()`);
        const v = JSON.parse(raw || "{}");
        return { ok: !v.overlay && v.selected === "beta-session" && v.find === "coral bookmark", ...v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    await evaluate(`document.querySelector("[data-tf-close]")?.click()`);
  }
  // Search in files (#1406): seed a fixture dir under the daemon's work
  // dir, search from the Files-pane box, and check: matches grouped by
  // file with marked text, the no-results state, the capped state (per-
  // file cap of 20), and click-to-line landing in the document viewer.
  // Needles are built at runtime so this script's own source never
  // matches. DOM probes use textContent so the layout cannot hide them.
  const sifDir = join(process.cwd(), "smoke-search-in-files");
  const sifTag = Date.now().toString(36);
  const sifNeedle = ["sif", "needle", sifTag].join("");
  const sifCap = ["sif", "cap", sifTag].join("");
  await rm(sifDir, { recursive: true, force: true });
  await mkdir(sifDir, { recursive: true });
  try {
    await writeFile(join(sifDir, "alpha.txt"), `one\ntwo\nfind ${sifNeedle.toUpperCase()} here\n`);
    await writeFile(
      join(sifDir, "beta.txt"),
      `${sifNeedle} top\nb2\nb3\nb4\nb5\nb6\nagain ${sifNeedle} below\n`,
    );
    await writeFile(
      join(sifDir, "capped.txt"),
      Array.from({ length: 25 }, (_, i) => `${sifCap} ${i + 1}`).join("\n") + "\n",
    );
    const sifTab = await evaluate(`(() => {
      const btn = [...document.querySelectorAll(".side-tabs button")].find((b) => (b.textContent || "").includes("Files"));
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
    if (sifTab !== "clicked") {
      throw new Error(`search-in-files: Files side tab ${sifTab}\n${await diagnostics()}`);
    }
    // Fill the box and press Enter. Re-sent on each poll until the
    // expected state shows, which also rides out the daemon's 2s file
    // index cache right after the fixture was written.
    const sifSearch = (q) =>
      evaluate(`(() => {
        const input = document.querySelector("[data-search-in-files]");
        if (!input) return "missing";
        const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
        proto.set.call(input, ${JSON.stringify(q)});
        input.dispatchEvent(new InputEvent("input", { bubbles: true }));
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
        return "sent";
      })()`);
    const sifState = `(() => {
      const files = [...document.querySelectorAll("[data-sif-file]")].map((el) => el.getAttribute("data-sif-file"));
      const hits = [...document.querySelectorAll(".sif-hit")].map((b) => b.getAttribute("data-sif-path") + ":" + b.getAttribute("data-sif-line"));
      const summary = document.querySelector(".sif-summary");
      return {
        files,
        hits,
        marks: document.querySelectorAll(".sif-hit mark.tf-mark").length,
        summary: summary ? summary.textContent || "" : "",
        capped: summary ? summary.getAttribute("data-sif-capped") : "",
      };
    })()`;
    const sifWait = (label, q, ok) =>
      waitFor(
        label,
        async () => {
          const v = await evaluate(sifState);
          if (ok(v)) return { ok: true, ...v };
          const sent = await sifSearch(q);
          return { ok: false, sent, ...v };
        },
        { timeout: WAIT_MS, interval: 750 },
      );
    const rel = (name) => `smoke-search-in-files/${name}`;
    await sifWait("search-in-files: matches grouped by file", sifNeedle, (v) =>
      v.files.length === 2 &&
      v.files.includes(rel("alpha.txt")) &&
      v.files.includes(rel("beta.txt")) &&
      v.hits.join(",") === [rel("alpha.txt") + ":3", rel("beta.txt") + ":1", rel("beta.txt") + ":7"].join(",") &&
      v.marks === 3 &&
      v.capped === "false",
    );
    await sifWait("search-in-files: no-results state", `${sifNeedle}-absent`, (v) =>
      v.hits.length === 0 && v.summary.startsWith("No matches for") && v.capped === "false",
    );
    await sifWait("search-in-files: capped state", sifCap, (v) =>
      v.hits.length === 20 && v.capped === "true" && v.summary.includes("results are partial"),
    );
    await sifWait("search-in-files: results again for click-to-line", sifNeedle, (v) => v.hits.length === 3);
    const sifClicked = await evaluate(`(() => {
      const b = document.querySelector('.sif-hit[data-sif-path=${JSON.stringify(rel("beta.txt"))}][data-sif-line="7"]');
      if (!b) return "missing";
      b.click();
      return "clicked";
    })()`);
    if (sifClicked !== "clicked") {
      throw new Error(`search-in-files: result button ${sifClicked}`);
    }
    await waitFor(
      "search-in-files: document viewer opened the file at the line",
      async () => {
        const v = await evaluate(`(() => {
          const doc = document.querySelector('[data-ev-root="open"]');
          return {
            path: doc ? doc.getAttribute("data-ev-path") || "" : "",
            lines: doc ? doc.querySelectorAll(".ev-line").length : 0,
            text: doc ? (doc.textContent || "").includes(${JSON.stringify(`again ${sifNeedle} below`)}) : false,
          };
        })()`);
        return { ok: v.path.endsWith("beta.txt") && v.lines >= 7 && v.text, ...v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
  } finally {
    await rm(sifDir, { recursive: true, force: true });
  }
  // Session delete (#1358): seed a session, close its root so it is
  // saved but not open, then delete it through the cockpit. Cancelling
  // the confirm must issue no request; confirming must remove it from
  // the daemon and the list. Deleting the open session must surface the
  // daemon's 409 as a separate force confirmation — also cancelled
  // here, so the open session survives.
  const listJSON = async (path) => {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (!res.ok) throw new Error(`session-delete list ${path} HTTP ${res.status}`);
    return res.json();
  };
  const savedIDs = async () => ((await listJSON("/v1/sessions")).sessions || []).map((s) => s.id);
  const rootIDs = async () => ((await listJSON("/v1/roots")).roots || []).map((r) => r.id);
  const createdRes = await fetch(`http://127.0.0.1:${port}/v1/roots`, {
    method: "POST",
    headers: auth,
    body: "{}",
  });
  if (!createdRes.ok) {
    throw new Error(`session-delete seed root failed ${createdRes.status} ${await createdRes.text()}`);
  }
  const sacrificial = (await createdRes.json()).id;
  if (!sacrificial) {
    throw new Error("session-delete seed root returned no id");
  }
  const closeRes = await fetch(
    `http://127.0.0.1:${port}/v1/roots/${encodeURIComponent(sacrificial)}`,
    { method: "DELETE", headers: { Authorization: `Bearer ${token}` } },
  );
  if (!closeRes.ok) {
    throw new Error(`session-delete seed close failed ${closeRes.status} ${await closeRes.text()}`);
  }
  if (!(await savedIDs()).includes(sacrificial)) {
    throw new Error("session-delete seed session is not in the saved list");
  }
  const rowSel = (id) => `[data-session-id="${id}"]`;
  const clickSel = async (sel) => {
    const v = await evaluate(
      `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return "missing"; el.click(); return "clicked"; })()`,
    );
    if (v !== "clicked") throw new Error(`session-delete click ${sel} -> ${v}`);
  };
  const dialogText = async () =>
    evaluate(
      `(() => { const el = document.querySelector("[data-confirm='dialog']"); return el ? el.innerText : ""; })()`,
    );
  const aimCursor = async (id) => {
    const clicked = await evaluate(`(() => {
      const row = document.querySelector(${JSON.stringify(`[data-session-id="${id}"]`)});
      document.querySelectorAll("[data-delete-target]").forEach((el) => el.removeAttribute("data-delete-target"));
      if (row) row.setAttribute("data-delete-target", "1");
      const box = row && row.querySelector("input.ss-check");
      if (!box) return "missing";
      const clear = document.querySelector(".ss-bulk button");
      if (clear) clear.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      if (!box.checked) {
        box.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
      }
      return "clicked";
    })()`);
    if (clicked !== "clicked") throw new Error(`session row missing for ${id} (${clicked})`);
  };
  const armDelete = async (id) => {
    await aimCursor(id);
    for (let i = 0; i < 20; i++) {
      const hit = await evaluate(`(() => {
        const cmd = document.querySelector('[data-command-id="session-delete"]');
        if (cmd) { cmd.click(); return "clicked"; }
        document.querySelector('[data-nav="palette"]')?.click();
        return "opening";
      })()`);
      if (hit === "clicked") return;
      await delay(100);
    }
    throw new Error("Delete session command missing");
  };
  await evaluate(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.navigate", {
    url: "http://127.0.0.1:" + port + "/?token=" + token,
  });
  await waitFor(
    "cockpit re-boot for session delete",
    textProbe((t) => t.includes("Run a command")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await waitFor(
    "seeded session row",
    async () => ({
      ok:
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(rowSel(sacrificial))})`,
        )) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  await armDelete(sacrificial);
  const confirmText = await waitFor(
    "delete confirm dialog naming the permanence",
    async () => {
      const t = (await dialogText()) || "";
      return { ok: t.includes("Delete session") && t.includes("cannot be undone"), text: t };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!confirmText.text.includes("cannot be undone")) {
    throw new Error(`delete confirm copy wrong: ${confirmText.text.slice(0, 300)}`);
  }
  await clickSel("[data-confirm='cancel']");
  await waitFor(
    "confirm dialog to close on cancel",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === false,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  if (!(await savedIDs()).includes(sacrificial)) {
    throw new Error("cancelled delete still removed the session on the daemon");
  }
  await armDelete(sacrificial);
  await waitFor(
    "delete confirm dialog to reopen",
    async () => ({ ok: ((await dialogText()) || "").includes("cannot be undone") }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-confirm='accept']");
  await waitFor(
    "deleted session to leave the daemon list and the page",
    async () => ({
      ok:
        !(await savedIDs()).includes(sacrificial) &&
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(rowSel(sacrificial))})`,
        )) === false,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  const openIDs = await rootIDs();
  if (!openIDs.length) {
    throw new Error("session-delete 409 check found no open root");
  }
  const openID = openIDs[0];
  await waitFor(
    "open session row with a Delete button",
    async () => ({
      ok:
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(rowSel(openID))})`,
        )) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  await armDelete(openID);
  await waitFor(
    "delete confirm for the open session",
    async () => ({ ok: ((await dialogText()) || "").includes("cannot be undone") }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-confirm='accept']");
  const forceText = await waitFor(
    "force confirmation after the daemon 409",
    async () => {
      const t = (await dialogText()) || "";
      const hasForceBtn =
        (await dialogText() || "").includes("Force delete") &&
        (await evaluate(`!!document.querySelector("[data-confirm='accept']")`)) === true;
      return {
        ok: t.includes("is active") && t.includes("force required") && hasForceBtn,
        text: t,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!forceText.text.includes("Force delete")) {
    throw new Error(`force confirmation copy wrong: ${forceText.text.slice(0, 300)}`);
  }
  await clickSel("[data-confirm='cancel']");
  await waitFor(
    "force confirmation to close on cancel",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-confirm='dialog']")`)) === false,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  if (!(await rootIDs()).includes(openID)) {
    throw new Error("cancelled force delete still removed the open session");
  }

  // Force path through: reopen, confirm, then force. The open session
  // must leave the roots list, the saved list, and the page (the page
  // closes the live root after the session DELETE lands, because the
  // daemon keeps a log-less root registered otherwise).
  await armDelete(openID);
  await waitFor(
    "delete confirm to reopen for the force-through",
    async () => ({ ok: ((await dialogText()) || "").includes("cannot be undone") }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-confirm='accept']");
  await waitFor(
    "force confirmation to reopen",
    async () => ({
      ok:
        (await dialogText() || "").includes("Force delete"),
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-confirm='accept']");
  await waitFor(
    "force-deleted session to leave the daemon lists and the page",
    async () => ({
      ok:
        !(await rootIDs()).includes(openID) &&
        !(await savedIDs()).includes(openID) &&
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(rowSel(openID))})`,
        )) === false,
    }),
    { timeout: WAIT_MS, interval: 250 },
  );
  // Session restore (#1391): the viewed session is persisted like theme
  // and layout, so a reload returns to it; a saved id whose session was
  // deleted falls back to another session instead of an empty screen.
  const mkRoot = async (label) => {
    const res = await fetch(`http://127.0.0.1:${port}/v1/roots`, {
      method: "POST",
      headers: auth,
      body: "{}",
    });
    if (!res.ok) {
      throw new Error(`session-restore seed ${label} failed ${res.status} ${await res.text()}`);
    }
    const id = (await res.json()).id;
    if (!id) throw new Error(`session-restore seed ${label} returned no id`);
    return id;
  };
  const restoreFallback = await mkRoot("fallback");
  const restoreTarget = await mkRoot("target");
  const selectedID = async () =>
    evaluate(
      `(() => { const el = document.querySelector('[data-session-id][aria-selected="true"]'); return el ? el.getAttribute("data-session-id") : ""; })()`,
    );
  const savedActive = async () => evaluate(`localStorage.getItem("strike.activeSession") || ""`);
  const reloadCockpit = async (label) => {
    await send("Page.navigate", {
      url: "http://127.0.0.1:" + port + "/?token=" + token,
    });
    await waitFor(label, textProbe((t) => t.includes("Run a command")), {
      timeout: WAIT_MS,
      interval: 250,
    });
  };
  await reloadCockpit("cockpit re-boot for session restore");
  await waitFor(
    "session-restore target row",
    async () => ({
      ok:
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(rowSel(restoreTarget))})`,
        )) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  // Per-session drafts (#1367): a draft typed in session A is absent in
  // session B, back on return to A, persisted under strike.draft.<A>,
  // and still there after a reload. Clearing it removes the stored key
  // so later composer checks start from an empty draft.
  const openRow = async (id) => {
    await evaluate(
      `(() => { const row = document.querySelector(${JSON.stringify(rowSel(id))}); const btn = row && row.querySelector(".ss-main"); if (btn) btn.click(); return !!btn; })()`,
    );
    await waitFor(`session ${id} selected`, async () => ({ ok: (await selectedID()) === id }), {
      timeout: STEP_MS,
      interval: 200,
    });
  };
  const composerValue = async () =>
    evaluate(`(() => { const el = document.querySelector("#composer-input"); return el ? el.value : null; })()`);
  const typeComposer = async (value) => {
    const ok = await evaluate(`(() => {
      const el = document.querySelector("#composer-input");
      if (!el) return false;
      el.focus();
      const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
      proto.set.call(el, ${JSON.stringify(value)});
      el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, data: ${JSON.stringify(value)} }));
      return el.value === ${JSON.stringify(value)};
    })()`);
    if (ok !== true) throw new Error(`draft check: composer did not accept ${JSON.stringify(value)}`);
  };
  const storedDraft = async (id) =>
    evaluate(`localStorage.getItem(${JSON.stringify("strike.draft." + id)})`);
  const draftA = "half-written prompt for session A (#1367)";
  await waitFor(
    "draft check: session A row",
    async () => ({
      ok: (await evaluate(`!!document.querySelector(${JSON.stringify(rowSel(restoreFallback))})`)) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  await openRow(restoreFallback);
  await typeComposer(draftA);
  await waitFor(
    "draft check: A's draft persisted",
    async () => ({ ok: (await storedDraft(restoreFallback)) === draftA, stored: await storedDraft(restoreFallback) }),
    { timeout: STEP_MS, interval: 200 },
  );
  await openRow(restoreTarget);
  await waitFor(
    "draft check: session B composer is empty",
    async () => {
      const value = await composerValue();
      return { ok: value === "", value };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await openRow(restoreFallback);
  await waitFor(
    "draft check: A's draft restored on return",
    async () => {
      const value = await composerValue();
      return { ok: value === draftA, value };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await reloadCockpit("cockpit reload for draft persistence");
  await waitFor(
    "draft check: A's draft restored after reload",
    async () => {
      const selected = await selectedID();
      const value = await composerValue();
      return { ok: selected === restoreFallback && value === draftA, selected, value };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await typeComposer("");
  await waitFor(
    "draft check: cleared draft removes its stored key",
    async () => ({ ok: (await storedDraft(restoreFallback)) === null }),
    { timeout: STEP_MS, interval: 200 },
  );
  // Open session B (the target) through its row, like a user switch.
  await evaluate(
    `(() => { const row = document.querySelector(${JSON.stringify(rowSel(restoreTarget))}); const btn = row && row.querySelector(".ss-main"); if (btn) btn.click(); return !!btn; })()`,
  );
  await waitFor(
    "session B open and persisted",
    async () => ({
      ok:
        (await selectedID()) === restoreTarget &&
        (await savedActive()) === restoreTarget,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await reloadCockpit("cockpit reload after opening session B");
  await waitFor(
    "session B still viewed after reload",
    async () => ({
      ok:
        (await selectedID()) === restoreTarget &&
        (await savedActive()) === restoreTarget,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  // Stale-id path: delete B on the daemon (session log + live root,
  // the same composition the page's delete flow performs), reload, and
  // require the fallback session — never an empty screen.
  const delSession = await fetch(
    `http://127.0.0.1:${port}/v1/sessions/${encodeURIComponent(restoreTarget)}?force=true`,
    { method: "DELETE", headers: { Authorization: `Bearer ${token}` } },
  );
  if (!delSession.ok) {
    throw new Error(`session-restore delete failed ${delSession.status} ${await delSession.text()}`);
  }
  const delRoot = await fetch(
    `http://127.0.0.1:${port}/v1/roots/${encodeURIComponent(restoreTarget)}`,
    { method: "DELETE", headers: { Authorization: `Bearer ${token}` } },
  );
  // Since #1399 the session DELETE above also deregisters the live
  // root, so this follow-up close answering 404 "is not active" is the
  // documented success case (see handleSessionDelete in api.go) — the
  // root being gone either way is exactly the state this step needs.
  // Any other failure is still fatal.
  if (!delRoot.ok && delRoot.status !== 404) {
    throw new Error(`session-restore root close failed ${delRoot.status} ${await delRoot.text()}`);
  }
  await reloadCockpit("cockpit reload after deleting session B");
  await waitFor(
    "fallback session viewed after stale-id reload (no empty state)",
    async () => {
      const selected = await selectedID();
      return {
        ok:
          selected !== "" &&
          selected !== restoreTarget &&
          (await savedActive()) === selected &&
          (await rootIDs()).includes(selected),
        selected,
      };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if (!(await rootIDs()).includes(restoreFallback)) {
    throw new Error("session-restore fallback seed root vanished");
  }

  // Task runner (#1277): a plain echo section fails schema:handoff and
  // blocks the next section. Restart the daemon so the run is read back
  // from disk, then check the Tasks board (via the strict
  // [data-view="tasks"] switch) shows the Failed and Blocked rows.
  const taskApi = async (method, path, body) => {
    const res = await fetch(`http://127.0.0.1:${port}${path}`, {
      method,
      headers: authz,
      body: body == null ? undefined : JSON.stringify(body),
    });
    const text = await res.text();
    if (!res.ok) {
      throw new Error(`task runner ${method} ${path} -> ${res.status} ${text.slice(0, 300)}`);
    }
    return JSON.parse(text);
  };
  const taskRootRes = await fetch(`http://127.0.0.1:${port}/v1/roots`, {
    method: "POST",
    headers: auth,
    body: "{}",
  });
  if (!taskRootRes.ok) {
    throw new Error(`task runner seed root failed ${taskRootRes.status} ${await taskRootRes.text()}`);
  }
  const taskRoot = await taskRootRes.json();
  const taskOwner = taskRoot.sessionId || taskRoot.id;
  if (!taskOwner) throw new Error(`task runner root returned no id: ${JSON.stringify(taskRoot)}`);
  const taskPlan = await taskApi("POST", "/v1/plans", {
    ownerRoot: taskOwner,
    title: "Smoke checklist",
    sections: [
      { title: "First step", body: "plain echo body, no handoff" },
      { title: "Second step", body: "depends on the first" },
    ],
  });
  const taskPlanId = taskPlan.ID || taskPlan.id;
  if (!taskPlanId) throw new Error(`plan create returned no id: ${JSON.stringify(taskPlan).slice(0, 300)}`);
  const taskOpened = await taskApi("POST", `/v1/plans/${taskPlanId}/run`, {
    action: "open",
    ownerRoot: taskOwner,
  });
  const taskVersion = taskOpened.run && taskOpened.run.version;
  if (!taskVersion) throw new Error(`open run returned no version: ${JSON.stringify(taskOpened).slice(0, 300)}`);
  await taskApi("POST", `/v1/plans/${taskPlanId}/run`, {
    action: "start",
    ownerRoot: taskOwner,
    expectedVersion: taskVersion,
  });
  let taskSections = [];
  for (let i = 0; i < 60; i++) {
    const taskBoard = await taskApi("GET", `/v1/plans/${taskPlanId}/run`);
    taskSections = (taskBoard.run && taskBoard.run.sections) || [];
    if (taskSections[0]?.status === "failed" && taskSections[1]?.status === "blocked") break;
    await delay(500);
  }
  if (taskSections[0]?.status !== "failed" || taskSections[1]?.status !== "blocked") {
    throw new Error(`task run did not block dependants: ${JSON.stringify(taskSections).slice(0, 500)}`);
  }
  try {
    const exited = new Promise((resolve) => strike.once("exit", resolve));
    strike.kill("SIGTERM");
    await Promise.race([exited, delay(4000)]);
    if (strike.exitCode == null && !strike.signalCode) strike.kill("SIGKILL");
    await Promise.race([exited, delay(2000)]);
    strike = spawn(
      process.env.STRIKE_BIN || "./strike",
      ["serve", "--addr", `127.0.0.1:${port}`, "--provider", "echo", "--auth", "--token", token],
      { env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] },
    );
    strike.stdout.on("data", (b) => {
      strikeLog += b.toString();
    });
    strike.stderr.on("data", (b) => {
      strikeLog += b.toString();
    });
    await waitHealth(`http://127.0.0.1:${port}/health`);
    const taskAfter = await taskApi("GET", `/v1/plans/${taskPlanId}/run`);
    const taskAgain = (taskAfter.run && taskAfter.run.sections) || [];
    if (taskAgain[0]?.status !== "failed" || taskAgain[1]?.status !== "blocked") {
      throw new Error(`task run did not survive restart: ${JSON.stringify(taskAgain).slice(0, 500)}`);
    }
    await send("Page.navigate", {
      url: "http://127.0.0.1:" + port + "/?token=" + token,
    });
    await waitFor(
      "cockpit after task restart",
      textProbe((body) => body.includes("Run a command") && body.includes("Terminal")),
      { timeout: WAIT_MS, interval: 250 },
    );
    await runPalette("Task runner");
    await waitFor(
      "task runner board after restart (Failed and Blocked rows)",
      async () => {
        const boardText =
          (await evaluate(
            `(() => { const b = document.querySelector("#task-runner"); return b ? b.innerText : ""; })()`,
          )) || "";
        return {
          ok:
            boardText.includes("First step") &&
            boardText.includes("Second step") &&
            boardText.includes("Failed") &&
            boardText.includes("Blocked"),
          board: boardText.slice(0, 300),
        };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    // Leave the cockpit on the Chat view: the checks after this block
    // drive the chat and daemon surfaces, not the task board.
    await evaluate(
      `(() => { const btn = [...document.querySelectorAll(".segmented button")].find((b) => (b.textContent || "").trim() === "Chat"); if (btn) btn.click(); return true; })()`,
    );
    await waitFor(
      "chat view after the task runner check",
      textProbe((body) => body.includes("Run a command")),
      { timeout: WAIT_MS, interval: 250 },
    );
  } catch (err) {
    // The outer finally kills whichever daemon `strike` now points at.
    throw err;
  }
  // Turn elapsed (#1364): resume the seeded replay session and check
  // the label from the fixture above — exactly one "took 4:12", on the
  // long turn's final line; the short turn and the untimed-line turn
  // render nothing.
  // The seeded session row renders from the session-list fetch; right
  // after the daemon restart above that fetch can still be in flight (or
  // refused mid-restart), so the row is not always in the DOM yet. Poll
  // for it with the file's condition-based waitFor — clicking as soon as
  // the row appears — instead of a single shot that flakes as "missing"
  // (#1477). A row that never appears still fails with the same error.
  let elapsedClicked = "missing";
  try {
    await waitFor(
      "turn-elapsed resume row",
      async () => {
        elapsedClicked = await evaluate(`(() => {
          const row = document.querySelector('[data-session-id="elapsed-session"]');
          const btn = row && row.querySelector(".ss-main");
          if (!btn) return "missing";
          btn.click();
          return "clicked";
        })()`);
        return { ok: elapsedClicked === "clicked", clicked: elapsedClicked };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  } catch {
    // waitFor's timeout carries diagnostics; the outer catch appends a
    // fresh dump to the original "missing" failure thrown below.
  }
  if (elapsedClicked !== "clicked") {
    throw new Error(`turn-elapsed resume click -> ${elapsedClicked}`);
  }
  await waitFor(
    "turn elapsed label on the replayed long turn only",
    async () => {
      const v = await evaluate(`(() => {
        const labels = [...document.querySelectorAll(".turn-elapsed")];
        return {
          n: labels.length,
          text: labels[0] ? labels[0].textContent.trim() : "",
          onSlow: labels[0]
            ? (labels[0].closest("article")?.innerText || "").includes("slow answer")
            : false,
          body: document.body.innerText.includes("old answer"),
        };
      })()`);
      return { ok: v.n === 1 && v.text === "took 4:12" && v.onSlow && v.body, ...v };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Compaction visibility (#1359): the replay's four valid markers render
  // inline in order; the completed marker shows removed/kept with summary
  // and residue in a collapsed fold. The schema-rejected payload must not
  // break the transcript (everything after it still renders); if the
  // daemon delivers it, it shows as a title-only marker. (Observed in CI:
  // the daemon path did not deliver that line, so the minimal-marker
  // rendering itself is covered by the Rust unit tests.)
  await waitFor(
    "compaction and budget markers in the replayed transcript",
    async () => {
      const v = await evaluate(`(() => {
        const marks = [...document.querySelectorAll("article[data-compaction]")];
        const done = marks.find((m) => m.getAttribute("data-compaction") === "completed");
        const fold = done && done.querySelector("details.sx-fold");
        const last = marks[marks.length - 1];
        return {
          kinds: marks.map((m) => m.getAttribute("data-compaction")).join(","),
          titles: marks.map((m) => (m.querySelector(".sx-who")?.textContent || "").trim()).join("|"),
          counts: done ? done.innerText.includes("Removed 6 messages, kept 2 messages") : false,
          foldClosed: !!fold && !fold.open,
          foldText: fold ? fold.textContent : "",
          budget: (marks[3]?.innerText || "").includes("$4.00 of $5.00 session cost"),
          extra: marks.length - 4,
          extraTitleOnly: marks.length < 5 || !last.querySelector(".sx-body"),
          after: document.body.innerText.includes("old answer"),
        };
      })()`);
      return {
        ok:
          v.kinds.startsWith("fit,started,completed,budget") &&
          v.titles.startsWith("Context near limit|Compacting context|Context compacted|Budget warning") &&
          v.extra <= 1 &&
          v.extraTitleOnly &&
          v.counts &&
          v.foldClosed &&
          v.foldText.includes("Earlier the slow replay ran sleep 180.") &&
          v.foldText.includes("bash slept for 180s") &&
          v.budget &&
          v.after,
        ...v,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Reasoning visibility (#1372): off by default the reasoning leaves no
  // trace in the DOM, only the toggle; switched on, a labelled block sits
  // directly above the answer it preceded and stays out of the answer's
  // own text; switched off again, it is gone.
  const reasoningProbe = `(() => {
    const toggle = document.querySelector("[data-reasoning-toggle]");
    const blocks = [...document.querySelectorAll(".sx-reasoning")];
    const answer = [...document.querySelectorAll("article.sx-msg")].find((a) =>
      (a.innerText || "").includes("fast answer"),
    );
    return {
      toggle: !!toggle,
      pressed: toggle ? toggle.getAttribute("aria-pressed") : "",
      label: toggle ? toggle.textContent.trim() : "",
      blocks: blocks.length,
      text: blocks[0] ? blocks[0].innerText : "",
      aboveAnswer: !!blocks[0] && !!answer && blocks[0].nextElementSibling === answer,
      inAnswer: answer ? (answer.innerText || "").includes("weighing the quick reply") : true,
      leaked: !blocks.length && document.body.innerText.includes("weighing the quick reply"),
    };
  })()`;
  await waitFor(
    "reasoning hidden by default behind its toggle",
    async () => {
      const v = await evaluate(reasoningProbe);
      return {
        ok: v.toggle && v.pressed === "false" && v.label === "Show reasoning" && v.blocks === 0 && !v.leaked,
        ...v,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => { document.querySelector("[data-reasoning-toggle]").click(); return true; })()`);
  await waitFor(
    "reasoning block above the answer once toggled on",
    async () => {
      const v = await evaluate(reasoningProbe);
      return {
        ok:
          v.pressed === "true" &&
          v.label === "Hide reasoning" &&
          v.blocks === 1 &&
          v.text.includes("weighing the quick reply") &&
          v.text.includes("not part of the answer") &&
          v.aboveAnswer &&
          !v.inAnswer,
        ...v,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => { document.querySelector("[data-reasoning-toggle]").click(); return true; })()`);
  await waitFor(
    "reasoning gone again once toggled off",
    async () => {
      const v = await evaluate(reasoningProbe);
      return { ok: v.pressed === "false" && v.blocks === 0 && !v.leaked, ...v };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  // Turn failure (#1371): the same replay ends its two failing turns with
  // a stop marker each (error, loop_detected), in order, badged failed;
  // the loop guard's own line names the tool; clean turns get none.
  await waitFor(
    "turn failure markers on the replayed failing turns only",
    async () => {
      const v = await evaluate(`(() => {
        const marks = [...document.querySelectorAll("article[data-turn-stop]")];
        const arts = [...document.querySelectorAll("article.sx-msg")];
        const idx = (el) => arts.indexOf(el);
        const failing = arts.find((a) => (a.innerText || "").includes("half answer"));
        const guard = arts.find((a) => (a.innerText || "").includes("Loop guard"));
        return {
          reasons: marks.map((m) => m.getAttribute("data-turn-stop")).join(","),
          titles: marks.map((m) => (m.querySelector(".sx-who")?.textContent || "").trim()).join("|"),
          afterAnswer: !!failing && marks.length > 0 && idx(marks[0]) > idx(failing),
          guardText: guard ? guard.innerText : "",
          engineError: document.body.innerText.includes("provider exploded"),
        };
      })()`);
      return {
        ok:
          v.reasons === "error,loop_detected" &&
          v.titles === "Turn failed|Turn stopped by loop guard" &&
          v.afterAnswer &&
          v.guardText.includes("bash, 3 times") &&
          v.engineError,
        ...v,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // Live tool tail (#1374): resume the seeded tail session. The call that
  // ended shows no tail (tool.end replaced it); the call that never ended
  // keeps exactly the last 200 streamed lines plus the truncation hint.
  let tailClicked = "missing";
  try {
    await waitFor(
      "tool-tail resume row",
      async () => {
        tailClicked = await evaluate(`(() => {
          const row = document.querySelector('[data-session-id="tail-session"]');
          const btn = row && row.querySelector(".ss-main");
          if (!btn) return "missing";
          btn.click();
          return "clicked";
        })()`);
        return { ok: tailClicked === "clicked", clicked: tailClicked };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  } catch {
    // reported below with the last observed state
  }
  if (tailClicked !== "clicked") {
    throw new Error(`tool-tail resume click -> ${tailClicked}`);
  }
  await waitFor(
    "bounded live tool tail on the unfinished call only",
    async () => {
      const v = await evaluate(`(() => {
        const tails = [...document.querySelectorAll("[data-tool-tail]")];
        const t = tails[0];
        const pre = t ? (t.querySelector("pre")?.textContent || "") : "";
        return {
          n: tails.length,
          lines: t ? t.getAttribute("data-tool-tail") : "",
          note: Boolean(t && t.querySelector("[data-tool-tail-truncated]")),
          first: pre.split("\\n")[0],
          last: pre.trimEnd().split("\\n").pop(),
          early: document.body.innerText.includes("early tail chunk"),
          quick: document.body.innerText.includes("quick done"),
        };
      })()`);
      return {
        ok:
          v.n === 1 &&
          v.lines === "200" &&
          v.note &&
          v.first === "tail line 50" &&
          v.last === "tail line 249" &&
          !v.early &&
          v.quick,
        ...v,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // Verification badge (#1373): resume the seeded session; each gated
  // turn's final line carries the TUI label for its state, and the turn
  // without a payload carries none.
  let verifyClicked = "missing";
  try {
    await waitFor(
      "verification badge resume row",
      async () => {
        verifyClicked = await evaluate(`(() => {
          const row = document.querySelector('[data-session-id="verify-session"]');
          const btn = row && row.querySelector(".ss-main");
          if (!btn) return "missing";
          btn.click();
          return "clicked";
        })()`);
        return { ok: verifyClicked === "clicked", clicked: verifyClicked };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  } catch {
    // reported below with the last observed state
  }
  if (verifyClicked !== "clicked") {
    throw new Error(`verification badge resume click -> ${verifyClicked}`);
  }
  await waitFor(
    "verification badges per turn state",
    async () => {
      const v = await evaluate(`(() => {
        const badges = [...document.querySelectorAll(".turn-verify")].map((b) => ({
          state: b.getAttribute("data-verify"),
          text: b.textContent.trim(),
          on: (b.closest("article")?.innerText || "").split("\\n").find((l) => l.startsWith("answer")) || "",
        }));
        const ungated = [...document.querySelectorAll("article")].find((a) =>
          (a.innerText || "").includes("answer ungated"),
        );
        return {
          badges: JSON.stringify(badges),
          ungatedFound: Boolean(ungated),
          ungatedBadge: Boolean(ungated && ungated.querySelector(".turn-verify")),
        };
      })()`);
      const want = JSON.stringify([
        { state: "verified", text: "verified", on: "answer gated" },
        { state: "claimed", text: "claimed", on: "answer claimed" },
        { state: "unverified", text: "unverified", on: "answer failed" },
      ]);
      return { ok: v.badges === want && v.ungatedFound && !v.ungatedBadge, ...v };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // files.invalidated (#1380): with the Files tab open, add a file to the
  // daemon's work dir, then open the seeded replay session. The replayed
  // invalidations must produce exactly one merged, capped notice, and the
  // open Files panel must refetch (the new file appears) with no manual
  // Refresh. DOM probes use textContent so a collapsed side panel in the
  // current layout cannot hide the result.
  const invalidatedFile = "smoke-files-invalidated.tmp";
  const invalidatedPath = join(process.cwd(), invalidatedFile);
  await rm(invalidatedPath, { force: true });
  try {
    const filesTab = await evaluate(`(() => {
      const btn = [...document.querySelectorAll(".side-tabs button")].find((b) => (b.textContent || "").includes("Files"));
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
    if (filesTab !== "clicked") {
      throw new Error(`files.invalidated: Files side tab ${filesTab}\n${await diagnostics()}`);
    }
    const fileRows = `[...document.querySelectorAll(".files .file-row")].map((b) => (b.textContent || "").trim())`;
    await waitFor(
      "files.invalidated: Files panel loaded before the change",
      async () => {
        const v = await evaluate(`(() => { const rows = ${fileRows}; return { n: rows.length, has: rows.includes(${JSON.stringify(invalidatedFile)}) }; })()`);
        return { ok: v.n > 0 && !v.has, ...v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    await writeFile(invalidatedPath, "changed outside the transcript\n");
    let invalidatedClicked = "missing";
    try {
      await waitFor(
        "files.invalidated resume row",
        async () => {
          invalidatedClicked = await evaluate(`(() => {
            const row = document.querySelector('[data-session-id="invalidated-session"]');
            const btn = row && row.querySelector(".ss-main");
            if (!btn) return "missing";
            btn.click();
            return "clicked";
          })()`);
          return { ok: invalidatedClicked === "clicked", clicked: invalidatedClicked };
        },
        { timeout: STEP_MS, interval: 250 },
      );
    } catch {
      // Reported by the throw below with the last observed state.
    }
    if (invalidatedClicked !== "clicked") {
      throw new Error(`files.invalidated resume click -> ${invalidatedClicked}`);
    }
    await waitFor(
      "files.invalidated: one merged notice and a refreshed Files panel",
      async () => {
        const v = await evaluate(`(() => {
          const notices = [...document.querySelectorAll('article[data-kind="files.invalidated"]')];
          const rows = ${fileRows};
          return {
            n: notices.length,
            text: notices[0] ? (notices[0].textContent || "") : "",
            replayed: (document.body.textContent || "").includes("tree touched"),
            has: rows.includes(${JSON.stringify(invalidatedFile)}),
          };
        })()`);
        const want = "files changed — agent will re-read: src/a.go, src/b.go, src/c.go +2 more";
        return { ok: v.n === 1 && v.text.includes(want) && v.replayed && v.has, ...v };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
  } finally {
    await rm(invalidatedPath, { force: true });
  }

  // Diagnostics refresh on files.invalidated (#1894): with the
  // diagnostics pane shown, resuming a replay with an invalidation burst
  // refetches /v1/diagnostics once (the same 400 ms debounce as Files).
  await runPalette("Code diagnostics");
  await waitFor(
    "files.invalidated diagnostics: pane shown",
    async () => ({ ok: (await evaluate(`!!document.querySelector("[data-diagnostics-pane]")`)) === true }),
    { timeout: WAIT_MS, interval: 250 },
  );
  let diagFetches = 0;
  const onDiagRequest = (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.method === "Network.requestWillBeSent" && (msg.params?.request?.url || "").includes("/v1/diagnostics")) {
      diagFetches += 1;
    }
  };
  ws.addEventListener("message", onDiagRequest);
  try {
    // Let the palette-open fetch settle, then count only what follows.
    await new Promise((r) => setTimeout(r, 1000));
    diagFetches = 0;
    let diagResume = "missing";
    try {
      await waitFor(
        "files.invalidated diagnostics resume row",
        async () => {
          diagResume = await evaluate(`(() => {
            const row = document.querySelector('[data-session-id="invalidated-diag-session"]');
            const btn = row && row.querySelector(".ss-main");
            if (!btn) return "missing";
            btn.click();
            return "clicked";
          })()`);
          return { ok: diagResume === "clicked", clicked: diagResume };
        },
        { timeout: STEP_MS, interval: 250 },
      );
    } catch {
      // Reported by the throw below with the last observed state.
    }
    if (diagResume !== "clicked") {
      throw new Error(`files.invalidated diagnostics resume click -> ${diagResume}`);
    }
    await waitFor(
      "files.invalidated diagnostics: replay applied and diagnostics refetched",
      async () => {
        const replayed = (await evaluate(`(document.body.textContent || "").includes("lsp tree touched")`)) === true;
        return { ok: replayed && diagFetches >= 1, replayed, diagFetches };
      },
      { timeout: WAIT_MS, interval: 250 },
    );
    // Past the debounce window the burst must still have cost one fetch.
    await new Promise((r) => setTimeout(r, 1500));
    if (diagFetches !== 1) {
      throw new Error(`files.invalidated diagnostics: want 1 refetch for the burst, got ${diagFetches}\n${await diagnostics()}`);
    }
  } finally {
    ws.removeEventListener("message", onDiagRequest);
  }

  // Per-turn file summary (#1365): resume the seeded session; each turn
  // that changed files carries one collapsed summary on its own final
  // line, the no-change turn carries none, and expanding shows the paths
  // grouped by kind (deleted paths are not links).
  let filesClicked = "missing";
  try {
    await waitFor(
      "file summary resume row",
      async () => {
        filesClicked = await evaluate(`(() => {
          const row = document.querySelector('[data-session-id="files-session"]');
          const btn = row && row.querySelector(".ss-main");
          if (!btn) return "missing";
          btn.click();
          return "clicked";
        })()`);
        return { ok: filesClicked === "clicked", clicked: filesClicked };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  } catch {
    // reported below with the last observed state
  }
  if (filesClicked !== "clicked") {
    throw new Error(`file summary resume click -> ${filesClicked}`);
  }
  await waitFor(
    "per-turn file summaries on the right turns",
    async () => {
      const v = await evaluate(`(() => {
        const all = [...document.querySelectorAll("details.turn-files")];
        const rows = all.map((d) => ({
          label: (d.querySelector("summary")?.textContent || "").trim(),
          open: d.open,
          on: (d.closest("article")?.innerText || "").includes("files answer one")
            ? "one"
            : (d.closest("article")?.innerText || "").includes("files answer two")
              ? "two"
              : "other",
        }));
        const none = [...document.querySelectorAll("article")].find((a) =>
          (a.innerText || "").includes("files answer none"),
        );
        let groups = "";
        if (all[0]) {
          groups = [...all[0].querySelectorAll("[data-turn-files-kind]")]
            .map((g) => g.getAttribute("data-turn-files-kind") + ":" +
              [...g.querySelectorAll("a.turn-files-path")].map((a) => a.getAttribute("data-path")).join("|"))
            .join(";");
        }
        const del = all[1] ? all[1].querySelectorAll("a.turn-files-path").length : -1;
        return {
          rows: JSON.stringify(rows),
          groups,
          del,
          noneFound: Boolean(none),
          noneSummary: Boolean(none && none.querySelector(".turn-files")),
        };
      })()`);
      const want = JSON.stringify([
        { label: "3 files changed · 2 updated, 1 created", open: false, on: "one" },
        { label: "1 file changed · 1 deleted", open: false, on: "two" },
      ]);
      return {
        ok:
          v.rows === want &&
          v.groups === "updated:README.md|go.mod;created:docs/new.md" &&
          v.del === 0 &&
          v.noneFound &&
          !v.noneSummary,
        ...v,
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  // Clicking a summary path opens it in the document pane (#1269), then
  // Escape closes the pane so later steps start from the transcript.
  const filesOpen = await evaluate(`(() => {
    const a = document.querySelector('details.turn-files a.turn-files-path[data-path="README.md"]');
    if (!a) return "missing";
    a.click();
    return "clicked";
  })()`);
  if (filesOpen !== "clicked") {
    throw new Error(`file summary path click -> ${filesOpen}`);
  }
  await waitFor(
    "file summary path opens the document pane",
    async () => {
      const path = await evaluate(
        `(document.querySelector(".ev-doc .ev-path")?.textContent || "").trim()`,
      );
      return { ok: path === "README.md", path };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await waitFor(
    "document pane closes after the file summary check",
    async () => ({ ok: await evaluate(`document.querySelector(".ev-doc") == null`) }),
    { timeout: STEP_MS, interval: 250 },
  );

  const wsClicked = await send("Runtime.evaluate", {
    expression: `(() => {
      const btn = document.querySelector(".ws-open");
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`,
    returnByValue: true,
  });
  if (wsClicked.result?.result?.value !== "clicked") {
    throw new Error(`workspace picker button missing: ${wsClicked.result?.result?.value || ""}`);
  }
  let picker = "";
  for (let i = 0; i < 20; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `document.body ? document.body.innerText : ""`,
      returnByValue: true,
    });
    picker = ev.result?.result?.value || "";
    if (picker.includes("A workspace is a directory")) break;
    await delay(100);
  }
  if (!picker.includes("A workspace is a directory")) {
    throw new Error(`workspace picker did not open: ${picker.slice(0, 400)}`);
  }
  // Workspace filter (#1437): a query with no matches shows the named
  // empty state (not the generic "None yet" hint); clearing restores it.
  const wsFilterSet = await evaluate(`(() => {
    const el = document.querySelector("[data-workspace-filter]");
    if (!el) return "missing";
    el.focus();
    el.value = "zzz-no-such-workspace";
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return "ok";
  })()`);
  if (wsFilterSet !== "ok") {
    throw new Error(`workspace filter input missing (${wsFilterSet})`);
  }
  await waitFor(
    "workspace filter no-match empty state",
    textProbe((body) => body.includes('No workspaces match "zzz-no-such-workspace"')),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const el = document.querySelector("[data-workspace-filter]");
    el.value = "";
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return "ok";
  })()`);
  await waitFor(
    "workspace filter cleared restores the list",
    textProbe((body) => !body.includes("No workspaces match")),
    { timeout: WAIT_MS, interval: 250 },
  );
  // Shared filtered list (#1437, load-bearing): seed two workspaces, filter
  // to one, press Enter in the filter, and assert the daemon activated the
  // intended workspace — not unfiltered row 0.
  const wsAuth = { Authorization: `Bearer ${token}`, "Content-Type": "application/json" };
  const wsBefore = await (await fetch(`http://127.0.0.1:${port}/v1/workspaces`, { headers: wsAuth })).json();
  const seededWs = {};
  for (const name of ["smoke-ws-alpha", "smoke-ws-beta"]) {
    const dir = join(home, name);
    await mkdir(dir, { recursive: true });
    const res = await fetch(`http://127.0.0.1:${port}/v1/workspaces`, {
      method: "POST",
      headers: wsAuth,
      body: JSON.stringify({ path: dir, name }),
    });
    if (!res.ok) throw new Error(`seed workspace ${name}: HTTP ${res.status}`);
    seededWs[name] = (await res.json()).id;
  }
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "app after reload for workspace filter activation",
    // The header Menu is gone (#1590); boot is "Run a command" like the other reloads.
    textProbe((body) => body.includes("Run a command")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector(".ws-open").click()`);
  await waitFor(
    "workspace picker lists both seeded workspaces",
    textProbe((body) => body.includes("smoke-ws-alpha") && body.includes("smoke-ws-beta")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const el = document.querySelector("[data-workspace-filter]");
    el.focus();
    el.value = "ws-BETA";
    el.dispatchEvent(new Event("input", { bubbles: true }));
  })()`);
  await waitFor(
    "workspace filter narrows to the beta workspace",
    async () => {
      const r = await evaluate(`(() => {
        // The Recent/All column only — Browse may list the seeded folders.
        const col = document.querySelector(".ws-dialog .ws-cols > section");
        const t = col ? col.innerText : "";
        return { alpha: t.includes("smoke-ws-alpha"), beta: t.includes("smoke-ws-beta") };
      })()`);
      return { ok: r && !r.alpha && r.beta, body: JSON.stringify(r) };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`document.querySelector("[data-workspace-filter]").dispatchEvent(
    new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }))`);
  await waitFor(
    "Enter in the filter activates the filtered workspace",
    async () => {
      const v = await (await fetch(`http://127.0.0.1:${port}/v1/workspaces`, { headers: wsAuth })).json();
      return { ok: v.activeId === seededWs["smoke-ws-beta"], body: JSON.stringify({ activeId: v.activeId, want: seededWs["smoke-ws-beta"] }) };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  if (wsBefore.activeId && wsBefore.activeId !== seededWs["smoke-ws-beta"]) {
    await fetch(`http://127.0.0.1:${port}/v1/workspaces/${wsBefore.activeId}/activate`, {
      method: "POST",
      headers: wsAuth,
      body: "{}",
    });
  }
  // Schedule page (#1276): the top-bar switch (data-view="schedule",
  // registered by src/shell/contrib/scheduler.rs) opens the page and the
  // empty state shows; the daemon token is already in localStorage from
  // the boot script, so no reload is needed (the original PR's reload
  // loop is replaced by the shared waitFor/textProbe helpers).
  await runPalette("Schedule");
  await waitFor(
    "schedule page (empty state)",
    textProbe((body) => body.includes("Nothing is scheduled") && !body.includes("unauthorized")),
    { timeout: WAIT_MS, interval: 250 },
  );

  // Duplicate a schedule (#1462): seed one via the API, duplicate it in the
  // form, assert nothing is saved until Create, then assert the saved copy
  // exists next to the original and starts disabled.
  const seedRes = await fetch(`http://127.0.0.1:${port}/v1/schedules`, {
    method: "POST",
    headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
    body: JSON.stringify({
      name: "Smoke schedule",
      kind: "cron",
      cron: "0 9 * * 1-5",
      timezone: "UTC",
      source: "prompt",
      prompt: "review CI",
      enabled: true,
    }),
  });
  if (!seedRes.ok) {
    throw new Error(`seed schedule for duplicate check: HTTP ${seedRes.status}`);
  }
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "app after reload for schedule duplicate check",
    textProbe((body) => body.includes("Run a command")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await runPalette("Schedule");
  await waitFor(
    "schedule page with the seeded schedule",
    textProbe((body) => body.includes("Smoke schedule")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const row = [...document.querySelectorAll(".sched-row")].find((r) => r.textContent.includes("Smoke schedule"));
    if (row) row.click();
    return row ? "clicked" : "missing";
  })()`);
  await waitFor(
    "schedule duplicate action enabled after selecting a schedule",
    async () => ({
      ok: (await evaluate(`(() => { const b = document.getElementById("schedule-duplicate"); return !!b && !b.disabled; })()`)) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`document.getElementById("schedule-duplicate").click()`);
  await waitFor(
    "duplicated schedule form (copy name, disabled notice, Create)",
    async () => {
      const raw = await evaluate(`JSON.stringify({
        name: document.getElementById("schedule-name")?.value,
        text: document.body ? document.body.innerText : "",
      })`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: p.name === "Smoke schedule (copy)" && (p.text || "").includes("starts paused/disabled"),
        name: p.name,
      };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  const schedBeforeSave = await (
    await fetch(`http://127.0.0.1:${port}/v1/schedules`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (schedBeforeSave.schedules.length !== 1) {
    throw new Error(`duplicate schedule was saved before Create count=${schedBeforeSave.schedules.length}`);
  }
  await evaluate(`document.querySelector(".sched-form .primary").click()`);
  await waitFor(
    "saved schedule copy in the list next to the original",
    textProbe((body) => body.includes("Smoke schedule (copy)")),
    { timeout: WAIT_MS, interval: 250 },
  );
  const schedAfterSave = await (
    await fetch(`http://127.0.0.1:${port}/v1/schedules`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  const schedCopy = schedAfterSave.schedules.find((s) => s.name === "Smoke schedule (copy)");
  const schedOrig = schedAfterSave.schedules.find((s) => s.name === "Smoke schedule");
  if (!schedCopy || schedCopy.enabled !== false || !schedOrig || schedOrig.enabled !== true) {
    throw new Error(`schedule duplicate saved state copy=${JSON.stringify(schedCopy)} orig=${JSON.stringify(schedOrig)}`);
  }

  // Script schedules (#1777): Script is an explicit choice, the first
  // submit only shows the confirm step (full command, mode, sandbox dial)
  // and writes nothing; "Confirm and arm" creates it armed.
  const scriptCmd = "echo smoke-script-1777";
  await evaluate(`document.querySelector(".sched .section-head button.secondary").click()`);
  await evaluate(`(() => {
    const name = document.getElementById("schedule-name");
    name.value = "Smoke script";
    name.dispatchEvent(new Event("input", { bubbles: true }));
    const src = document.getElementById("schedule-source");
    src.value = "script";
    src.dispatchEvent(new Event("change", { bubbles: true }));
  })()`);
  await waitFor(
    "script command field after choosing Script",
    async () => ({ ok: (await evaluate(`!!document.getElementById("schedule-command")`)) === true }),
    { timeout: STEP_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const cmd = document.getElementById("schedule-command");
    cmd.value = ${JSON.stringify(scriptCmd)};
    cmd.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector(".sched-form .sched-actions .primary").click();
  })()`);
  await waitFor(
    "script confirm step repeats the command, mode, and sandbox dial",
    async () => {
      const raw = await evaluate(`JSON.stringify({
        cmd: (document.getElementById("schedule-confirm-command") || {}).textContent || "",
        text: (document.querySelector(".sched-confirm") || {}).innerText || "",
      })`);
      const p = JSON.parse(raw || "{}");
      return {
        ok: p.cmd === scriptCmd && (p.text || "").includes("Mode:") && (p.text || "").includes("Sandbox:"),
        ...p,
      };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  const schedBeforeArm = await (
    await fetch(`http://127.0.0.1:${port}/v1/schedules`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (schedBeforeArm.schedules.some((s) => s.name === "Smoke script")) {
    throw new Error("script schedule was written before the confirm step");
  }
  await evaluate(`document.getElementById("schedule-confirm").click()`);
  await waitFor(
    "script schedule armed notice and script badge",
    async () => {
      const raw = await evaluate(`JSON.stringify({
        text: document.body ? document.body.innerText : "",
        badge: [...document.querySelectorAll(".sched-row")].some((r) => r.textContent.includes("Smoke script") && !!r.querySelector(".sched-script-badge")),
      })`);
      const p = JSON.parse(raw || "{}");
      return { ok: (p.text || "").includes("Script schedule armed") && p.badge === true, badge: p.badge };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  const schedAfterArm = await (
    await fetch(`http://127.0.0.1:${port}/v1/schedules`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  const armed = schedAfterArm.schedules.find((s) => s.name === "Smoke script");
  if (!armed || armed.source !== "script" || armed.command !== scriptCmd || !armed.armedAt || armed.overlap !== "skip") {
    throw new Error(`script schedule armed state ${JSON.stringify(armed)}`);
  }

  // Scheduler presets (#1495): the section lists the daemon catalog, Apply
  // round-trips the checked ids through POST /v1/scheduler/presets, and the
  // selection survives a reload (it is the daemon's persisted global state).
  await waitFor(
    "scheduler presets section with the cmake preset",
    async () => {
      const raw = await evaluate(`JSON.stringify({
        text: (document.querySelector(".sched-presets") || {}).innerText || "",
        box: !!document.getElementById("sched-preset-cmake"),
      })`);
      const p = JSON.parse(raw || "{}");
      return { ok: p.box && (p.text || "").includes("CMake"), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => {
    const box = document.getElementById("sched-preset-cmake");
    if (!box.checked) box.click();
    return box.checked;
  })()`);
  await evaluate(`document.getElementById("sched-presets-apply").click()`);
  await waitFor(
    "scheduler presets applied notice",
    textProbe((body) => body.includes("Scheduler presets applied.")),
    { timeout: WAIT_MS, interval: 250 },
  );
  const presetsAfter = await (
    await fetch(`http://127.0.0.1:${port}/v1/scheduler/presets`, { headers: { Authorization: `Bearer ${token}` } })
  ).json();
  if (!(presetsAfter.global?.Presets || []).includes("cmake")) {
    throw new Error(`scheduler preset apply did not persist: ${JSON.stringify(presetsAfter.global)}`);
  }
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "app after reload for scheduler presets",
    // The header Menu is gone (#1590); boot is "Run a command" like the other reloads.
    textProbe((body) => body.includes("Run a command")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await runPalette("Schedule");
  await waitFor(
    "scheduler preset still checked after reload",
    async () => ({
      ok: (await evaluate(`(() => { const b = document.getElementById("sched-preset-cmake"); return !!b && b.checked; })()`)) === true,
    }),
    { timeout: WAIT_MS, interval: 250 },
  );

  if (strikeLog.includes(token)) {
    throw new Error("daemon log contains the smoke token");
  }

  // Back to Chat before the composer checks below: the Schedule section
  // above leaves the app on the schedule view, where the composer (and
  // its actions button) is not rendered. The view switcher is the
  // segmented "View" control whose Chat button calls set_view("chat").
  await evaluate(`(() => {
    const btn = [...document.querySelectorAll('.segmented[aria-label="View"] button')]
      .find((b) => b.textContent.trim() === "Chat");
    if (btn) { btn.click(); return "clicked"; }
    return "missing-chat-switch";
  })()`);
  await waitFor(
    "chat view (composer actions button) after leaving Schedule",
    async () => {
      const raw = await evaluate(
        `JSON.stringify({ plus: !!document.querySelector('[aria-label="Composer actions"]') })`,
      );
      const parsed = JSON.parse(raw || "{}");
      return { ok: !!parsed.plus, ...parsed };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  const composerTriggers = await send("Runtime.evaluate", {
    expression: `(() => {
      const btn = document.querySelector('[aria-label="Composer actions"]');
      if (!btn) return "missing-plus";
      btn.click();
      return "clicked";
    })()`,
    returnByValue: true,
  });
  if (composerTriggers.result?.result?.value !== "clicked") {
    throw new Error(`composer plus failed: ${composerTriggers.result?.result?.value || JSON.stringify(composerTriggers)}`);
  }
  let composerSuggest = "";
  for (let i = 0; i < 40; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `document.body.innerText`,
      returnByValue: true,
    });
    composerSuggest = ev.result?.result?.value || "";
    if (composerSuggest.includes("Mention a file")) break;
    await delay(150);
  }
  if (!composerSuggest.includes("Mention a file")) {
    throw new Error(`composer plus menu missing: ${composerSuggest.slice(0, 500)}`);
  }
  const composerTyped = await send("Runtime.evaluate", {
    expression: `(() => {
      const ta = document.querySelector('[data-feature="composer-triggers"] textarea');
      if (!ta) return "missing-textarea";
      ta.focus();
      ta.value = "/";
      ta.setSelectionRange(1, 1);
      ta.dispatchEvent(new InputEvent("input", { bubbles: true, data: "/" }));
      return "typed";
    })()`,
    returnByValue: true,
  });
  if (composerTyped.result?.result?.value !== "typed") {
    throw new Error(`composer slash type failed: ${composerTyped.result?.result?.value || JSON.stringify(composerTyped)}`);
  }
  composerSuggest = "";
  for (let i = 0; i < 40; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `document.body.innerText`,
      returnByValue: true,
    });
    composerSuggest = ev.result?.result?.value || "";
    if (composerSuggest.includes("/stop") || composerSuggest.includes("/mode") || composerSuggest.includes("Mention a file")) {
      if (composerSuggest.includes("/stop") || composerSuggest.includes("/mode")) break;
    }
    await delay(150);
  }
  if (!composerSuggest.includes("/stop") && !composerSuggest.includes("/mode")) {
    throw new Error(`composer slash popup missing: ${composerSuggest.slice(0, 500)}`);
  }
  await runPalette("Triggers");
  let triggersText = "";
  for (let i = 0; i < 25; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: "document.body ? document.body.innerText : ''",
      returnByValue: true,
    });
    triggersText = ev.result?.result?.value || "";
    if (triggersText.includes("New trigger") && triggersText.includes("Delivery log")) break;
    await delay(200);
  }
  if (!triggersText.includes("New trigger") || !triggersText.includes("Delivery log")) {
    throw new Error(`triggers page missing: ${triggersText.slice(0, 400)}`);
  }

  // #1486: create a trigger so the show-once secret and the hook URL
  // render, then Copy secret must produce a visible outcome — the copied
  // confirmation, or the manual-fallback notice when the harness denies
  // clipboard permission. A silent click is the failure this guards.
  await evaluate(`(() => {
    const el = document.querySelector("#trigger-name");
    if (!el) return false;
    const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
    proto.set.call(el, "smoke-copy");
    el.dispatchEvent(new InputEvent("input", { bubbles: true, data: "smoke-copy" }));
    return el.value === "smoke-copy";
  })()`);
  await evaluate(`(() => {
    document.querySelector(".triggers-detail form button.primary")?.click();
    return true;
  })()`);
  await waitFor(
    "trigger copy buttons",
    textProbe((t) => t.includes("Copy secret") && t.includes("Copy hook URL")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`(() => {
    [...document.querySelectorAll("button")].find((b) => b.innerText.trim() === "Copy secret")?.click();
    return true;
  })()`);
  await waitFor(
    "trigger copy outcome",
    textProbe((t) => t.includes("Signing secret copied.") || t.includes("Copy failed")),
    { timeout: WAIT_MS, interval: 250 },
  );

  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
    returnByValue: true,
  });
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "apps palette entry",
    textProbe((body) => body.includes("Run a command")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await runPalette("Apps");
  await waitFor(
    "apps launcher GitHub triage",
    textProbe((body) => body.includes("GitHub triage")),
    { timeout: WAIT_MS, interval: 250 },
  );

  // Steer / queue. Cookie handoff authenticates EventSource; the bearer in
  // localStorage authenticates JSON ops. A `run sleep` holds the turn so
  // Enter queues, the other send steers, and Escape interrupts.
  await evaluate(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.navigate", {
    url: `http://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`,
  });

  // The instance value setter does not notify Leptos. The prototype setter
  // plus a bubbling input event is the path the UAT uses, and it updates
  // the draft signal. Key handlers listen on the textarea, so a bubbling
  // keydown on that node is the keyboard path.
  const setDraft = async (value) => {
    const ok = await evaluate(`(() => {
      const el = document.querySelector("#composer-input");
      if (!el) return false;
      el.focus();
      const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
      proto.set.call(el, ${JSON.stringify(value)});
      el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, data: ${JSON.stringify(value)} }));
      return el.value === ${JSON.stringify(value)};
    })()`);
    if (ok !== true) throw new Error(`composer did not accept ${JSON.stringify(value)}`);
  };
  const press = async (key, extra = {}) => {
    const ok = await evaluate(`(() => {
      const el = document.querySelector("#composer-input");
      if (!el) return false;
      el.focus();
      const ev = new KeyboardEvent("keydown", {
        key: ${JSON.stringify(key)},
        code: ${JSON.stringify(key)},
        bubbles: true,
        cancelable: true,
        composed: true,
        ctrlKey: ${extra.ctrlKey ? "true" : "false"},
        metaKey: ${extra.metaKey ? "true" : "false"},
        altKey: ${extra.altKey ? "true" : "false"},
        shiftKey: ${extra.shiftKey ? "true" : "false"}
      });
      el.dispatchEvent(ev);
      return true;
    })()`);
    if (ok !== true) throw new Error(`composer missing for ${key}`);
  };

  await waitFor(
    "authenticated composer",
    async () => {
      const raw = await evaluate(`(() => {
        const text = document.body.innerText || "";
        const composer = !!document.querySelector("#composer-input");
        const send = !!document.querySelector("#send-primary");
        return JSON.stringify({
          ok: composer && send && /strike/i.test(text),
          composer, send,
          text: text.slice(0, 400)
        });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`(() => {
    const btn = [...document.querySelectorAll("button")].find((b) => (b.innerText || "").trim() === "New");
    if (btn) btn.click();
    return !!btn;
  })()`);
  await waitFor(
    "new live session",
    async () => {
      const roots = await (await fetch(`http://127.0.0.1:${port}/v1/roots`, {
        headers: { Authorization: `Bearer ${token}` },
      })).json();
      const id = roots.activeId || "";
      return { ok: id.includes("T") && !id.startsWith("elapsed"), root: id };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await waitFor(
    "yolo confirm from the palette",
    async () => {
      const open = await evaluate(`!!document.querySelector(".approvals-dialog")`);
      if (open) return { ok: true };
      const clicked = await evaluate(`(() => {
        const cmd = document.querySelector('[data-command-id="mode-yolo"]');
        if (cmd) { cmd.click(); return "clicked"; }
        document.querySelector('[data-nav="palette"]')?.click();
        return "opening";
      })()`);
      return { ok: false, clicked };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`(() => {
    const btn = [...document.querySelectorAll(".approvals-dialog button")].find((b) => (b.innerText || "").trim() === "Use Yolo");
    if (btn) btn.click();
    return "ok";
  })()`);
  await waitFor(
    "yolo approval",
    async () => {
      const body = (await evaluate(`document.body ? document.body.innerText : ""`)) || "";
      return { ok: body.includes("Approval · Yolo"), body: body.slice(0, 240) };
    },
    { timeout: STEP_MS, interval: 200 },
  );

  // Prompt sharpen (#1385): Alt+P sends the draft to the daemon's
  // one-shot rewrite (echo stands in for the model), shows the
  // original-vs-sharpened preview, and changes nothing until Accept.
  // Undo restores the original; Esc on the preview rejects untouched.
  const sharpenDraft = "fix the flaky thing";
  const composerText = async () =>
    evaluate(`(() => { const el = document.querySelector("#composer-input"); return el ? el.value : null; })()`);
  await setDraft(sharpenDraft);
  await press("p", { altKey: true });
  await waitFor(
    "sharpen preview with original and rewrite",
    async () => {
      const raw = await evaluate(`(() => {
        const diff = document.querySelector("[data-sharpen-diff]");
        return JSON.stringify({
          accept: !!document.querySelector("[data-sharpen-accept]"),
          diff: diff ? diff.innerText : "",
          state: document.querySelector("[data-feature=prompt-sharpen]")?.getAttribute("data-sharpen-state") || "",
        });
      })()`);
      const p = JSON.parse(raw || "{}");
      return { ok: p.accept && p.diff.includes(sharpenDraft) && p.diff.includes("You said:"), ...p };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  if ((await composerText()) !== sharpenDraft) {
    throw new Error(`sharpen changed the draft before Accept: ${JSON.stringify(await composerText())}`);
  }
  await evaluate(`document.querySelector("[data-sharpen-accept]")?.click()`);
  await waitFor(
    "accepted rewrite replaces the draft, Undo offered",
    async () => {
      const value = (await composerText()) || "";
      const undo = await evaluate(`!!document.querySelector("[data-sharpen-undo]")`);
      return { ok: value.startsWith("You said:") && value.includes(sharpenDraft) && undo, value, undo };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector("[data-sharpen-undo]")?.click()`);
  await waitFor(
    "undo restores the original draft",
    async () => {
      const value = await composerText();
      return { ok: value === sharpenDraft, value };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await press("p", { altKey: true });
  await waitFor(
    "second sharpen preview",
    async () => ({ ok: (await evaluate(`!!document.querySelector("[data-sharpen-accept]")`)) === true }),
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`(() => {
    const el = document.querySelector("[data-sharpen-accept]");
    el?.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", code: "Escape", bubbles: true, cancelable: true }));
    return true;
  })()`);
  await waitFor(
    "Esc rejects and leaves the draft byte-identical",
    async () => {
      const value = await composerText();
      const open = await evaluate(`!!document.querySelector("[data-feature=prompt-sharpen]")`);
      return { ok: !open && value === sharpenDraft, value, open };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await setDraft("");

  // Session dials (#1572 autonomy, #1569 Fast): both start at the
  // daemon-reported default, flip only after the daemon confirms the
  // op, and skip-all goes through its confirm first.
  await waitFor(
    "autonomy chip starts supervised",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-autonomy-status]')?.textContent || ""`);
      return { ok: chip.includes("Autonomy · supervised"), chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  const palettePick = async (id) => {
    await evaluate(`document.querySelector('[data-nav="palette"]')?.click()`);
    await delay(250);
    await evaluate(`document.querySelector('[data-command-id="${id}"]')?.click()`);
  };
  await palettePick("autonomy-checks");
  await waitFor(
    "autonomy flips to checks only on daemon confirmation",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-autonomy-status]')?.textContent || ""`);
      return { ok: chip.includes("Autonomy · checks"), chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await palettePick("autonomy-skip-all");
  await waitFor(
    "skip-all asks for its confirm first",
    async () => {
      const open = await evaluate(`!!document.querySelector(".approvals-dialog")`);
      const body = (await evaluate(`document.body ? document.body.innerText : ""`)) || "";
      return { ok: open && body.includes("Advance without exit gates?"), open };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`(() => {
    const btn = [...document.querySelectorAll(".approvals-dialog button")].find((b) => (b.innerText || "").trim() === "Use skip-all");
    if (btn) btn.click();
    return "ok";
  })()`);
  await waitFor(
    "skip-all chip after confirm + daemon confirmation",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-autonomy-status]')?.textContent || ""`);
      return { ok: chip.includes("Autonomy · skip-all"), chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await palettePick("autonomy-supervised");
  await waitFor(
    "autonomy restored to supervised",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-autonomy-status]')?.textContent || ""`);
      return { ok: chip.includes("Autonomy · supervised"), chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await palettePick("model.fast");
  await delay(250);
  await evaluate(`document.querySelector('[data-choice-id="on"]')?.click()`);
  await waitFor(
    "fast chip appears only on daemon confirmation",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-fast-status]')?.textContent || ""`);
      return { ok: chip.includes("Fast · on"), chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await palettePick("model.fast");
  await delay(250);
  await evaluate(`document.querySelector('[data-choice-id="off"]')?.click()`);
  await waitFor(
    "fast chip clears on daemon confirmation",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-fast-status]')?.textContent || "none"`);
      return { ok: chip === "none", chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );

  // Response style (#1369): the chip starts at Default, changes only on
  // the daemon's verbosity.selected, and says it is delivered as a
  // session instruction while a non-default style is active.
  await waitFor(
    "style chip starts at default",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-verbosity-status]')?.textContent || ""`);
      return { ok: chip === "Style · Default", chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  const pickStyle = async (id) => {
    // The chip opens the palette at the style command; pick the choice.
    await evaluate(`document.querySelector('[data-verbosity-status]')?.click()`);
    await delay(250);
    await evaluate(`document.querySelector('[data-choice-id="${id}"]')?.click()`);
  };
  await pickStyle("concise");
  await waitFor(
    "style chip shows concise via instruction after daemon confirmation",
    async () => {
      const raw = await evaluate(`JSON.stringify({
        chip: document.querySelector('[data-verbosity-status]')?.textContent || "",
        id: document.querySelector('[data-verbosity-status]')?.getAttribute("data-verbosity-status") || "",
        title: document.querySelector('[data-verbosity-status]')?.getAttribute("title") || "",
      })`);
      const st = JSON.parse(raw || "{}");
      return { ok: st.chip === "Style · Concise (instruction)" && st.id === "concise" && st.title.includes("session instruction"), ...st };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await pickStyle("default");
  await waitFor(
    "style chip back to default",
    async () => {
      const chip = await evaluate(`document.querySelector('[data-verbosity-status]')?.textContent || ""`);
      return { ok: chip === "Style · Default", chip };
    },
    { timeout: STEP_MS, interval: 200 },
  );

  await setDraft("run sleep 30");
  await press("Enter");
  await waitFor(
    "running turn",
    async () => {
      const raw = await evaluate(`(() => JSON.stringify({
        ok: !!document.querySelector("#stop-turn") && document.querySelector("#send-primary")?.innerText === "Queue",
        primary: document.querySelector("#send-primary")?.innerText || "",
        stop: !!document.querySelector("#stop-turn"),
        draft: document.querySelector("#composer-input")?.value || "",
        err: document.querySelector(".steer-error")?.textContent || "",
        hasSleep: (document.body.innerText || "").includes("run sleep")
      }))()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );

  // Mid-turn (#1572/#1569): the autonomy picker opens but every row is
  // disabled with the reason shown — the daemon rejects these ops while
  // a turn is running, and the cockpit surfaces that instead of faking it.
  await evaluate(`document.querySelector('[data-autonomy-status]')?.click()`);
  await waitFor(
    "autonomy picker disabled mid-turn",
    async () => {
      const raw = await evaluate(`(() => {
        const rows = [...document.querySelectorAll('[data-autonomy-menu] button[data-autonomy-mode]')];
        return JSON.stringify({
          menu: !!document.querySelector('[data-autonomy-menu]'),
          rows: rows.length,
          allDisabled: rows.length > 0 && rows.every((r) => r.disabled),
          busyNote: !!document.querySelector('[data-autonomy-busy]'),
        });
      })()`);
      const parsed = JSON.parse(raw || "{}");
      return { ok: parsed.menu && parsed.allDisabled && parsed.busyNote, ...parsed };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`document.querySelector('[data-autonomy-status]')?.click()`);

  await setDraft("alpha later");
  await press("Enter");
  await waitFor(
    "first queued message",
    async () => {
      const raw = await evaluate(`(() => {
        const texts = [...document.querySelectorAll(".queue-text")].map((n) => n.textContent);
        return JSON.stringify({ ok: texts.includes("alpha later"), texts });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await setDraft("beta later");
  await press("Enter");
  await waitFor(
    "queued messages",
    async () => {
      const raw = await evaluate(`(() => {
        const texts = [...document.querySelectorAll(".queue-text")].map((n) => n.textContent);
        return JSON.stringify({ ok: texts[0] === "alpha later" && texts[1] === "beta later", texts });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await waitFor(
    "queue move-up command",
    async () => {
      const clicked = await evaluate(`(() => {
        const cmd = document.querySelector('[data-command-id="queue.move-up"]');
        if (cmd) { cmd.click(); return "clicked"; }
        document.querySelector('[data-nav="palette"]')?.click();
        return "opening";
      })()`);
      return { ok: clicked === "clicked", clicked };
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await waitFor(
    "reordered queue",
    async () => {
      const raw = await evaluate(`(() => {
        const texts = [...document.querySelectorAll(".queue-text")].map((n) => n.textContent);
        return JSON.stringify({ ok: texts[0] === "beta later" && texts[1] === "alpha later", texts });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`(() => {
    const row = document.querySelector(".queue-list li");
    const edit = row && [...row.querySelectorAll("button")].find((b) => b.textContent === "Edit");
    if (edit) edit.click();
    return "ok";
  })()`);
  await waitFor(
    "queue edit field",
    async () => ({ ok: !!(await evaluate(`!!document.querySelector(".queue-edit")`)) }),
    { timeout: STEP_MS, interval: 150 },
  );
  await evaluate(`(() => {
    const el = document.querySelector(".queue-edit");
    if (!el) return false;
    el.focus();
    const proto = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value");
    proto.set.call(el, "beta edited");
    el.dispatchEvent(new InputEvent("input", { bubbles: true, composed: true, data: "beta edited" }));
    return el.value === "beta edited";
  })()`);
  await evaluate(`(() => {
    const save = [...document.querySelectorAll(".queue-actions button")].find((b) => b.textContent === "Save");
    if (save) save.click();
    return !!save;
  })()`);
  await waitFor(
    "edited queue",
    async () => {
      const raw = await evaluate(`(() => {
        const texts = [...document.querySelectorAll(".queue-text")].map((n) => n.textContent);
        return JSON.stringify({ ok: texts.includes("beta edited"), texts });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await evaluate(`(() => {
    const row = [...document.querySelectorAll(".queue-list li")].find((r) => r.innerText.includes("alpha later"));
    const cancel = row && [...row.querySelectorAll("button")].find((b) => b.textContent === "Cancel");
    if (cancel) cancel.click();
    return "ok";
  })()`);
  await waitFor(
    "cancel confirm",
    async () => {
      const raw = await evaluate(`(() => {
        const dialog = document.querySelector("[data-confirm='dialog']");
        const text = dialog ? dialog.innerText : "";
        return JSON.stringify({
          ok: !!dialog && text.includes("Remove queued message") && !!dialog.querySelector("[data-confirm='accept']"),
        });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 150 },
  );
  await evaluate(`(() => {
    const confirm = document.querySelector("[data-confirm='accept']");
    if (confirm) confirm.click();
    return "ok";
  })()`);
  await waitFor(
    "cancelled queue item",
    async () => {
      const raw = await evaluate(`(() => {
        const texts = [...document.querySelectorAll(".queue-text")].map((n) => n.textContent);
        return JSON.stringify({ ok: texts.includes("beta edited") && !texts.includes("alpha later"), texts });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );

  // Send now (#1571): the remaining queued message promotes into the
  // running turn as a steer — it leaves the FIFO at once and the steer
  // note takes over. Runs after the cancel step so the cancel coverage
  // keeps its two-item queue.
  await evaluate(`(() => {
    const row = [...document.querySelectorAll(".queue-list li")].find((r) => r.innerText.includes("beta edited"));
    const sendNow = row && [...row.querySelectorAll("button")].find((b) => b.textContent === "Send now");
    if (sendNow) sendNow.click();
    return "ok";
  })()`);
  await waitFor(
    "send now promoted queued message",
    async () => {
      const raw = await evaluate(`(() => {
        const texts = [...document.querySelectorAll(".queue-text")].map((n) => n.textContent);
        const note = document.querySelector(".steer-note")?.textContent || "";
        return JSON.stringify({ ok: texts.length === 0 && /steer/i.test(note), texts, note });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );

  await setDraft("steer left");
  await evaluate(`(() => {
    const mode = [...document.querySelectorAll(".steer-actions .chip")].find((b) => (b.textContent || "").trim() === "Steer");
    if (mode) mode.click();
    const send = document.querySelector("#send-primary");
    if (send) send.click();
    return !!(mode && send);
  })()`);
  await waitFor(
    "steer indicator",
    async () => {
      const raw = await evaluate(`(() => {
        const note = document.querySelector(".steer-note");
        const text = note ? note.textContent : "";
        return JSON.stringify({ ok: /steer|queued/i.test(text), text });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );

  await press("Escape");
  const idleProbe = async () => {
    const raw = await evaluate(`(() => {
      const stop = document.querySelector("#stop-turn");
      const queued = document.querySelector(".queue-list");
      return JSON.stringify({ ok: !stop && !queued, busy: !!stop, queued: !!queued });
    })()`);
    return JSON.parse(raw || "{}");
  };
  // "turn stopped" is an anti-hang arrival wait, not a timeliness check:
  // Escape posts an `interrupt` op, the daemon/engine must cancel the
  // running `sleep 30` tool, emit the terminal event, and the cockpit
  // must receive it and re-render before #stop-turn disappears — a full
  // daemon round trip. The predicate is unchanged (idle = no #stop-turn
  // and no queue list); only its budget class was wrong. With STEP_MS
  // (15s) it flaked 1/20 under load (#1713: probe still busy:true at
  // expiry while the whole smoke ran 54-87s vs 26-31s unloaded, load
  // ~9.9), so it uses WAIT_MS like the other daemon-backed arrival
  // waits. A genuine stop hang still fails, just at 30s. The "still
  // idle" re-check below keeps STEP_MS: its predicate is already true
  // when it starts, so it is a stability check, not an arrival wait.
  await waitFor("turn stopped", idleProbe, { timeout: WAIT_MS, interval: 200 });
  await delay(700);
  await waitFor("still idle", idleProbe, { timeout: STEP_MS, interval: 200 });

  await setDraft("too soon");
  await press("Enter", { ctrlKey: true });
  await waitFor(
    "steer while idle",
    async () => {
      const raw = await evaluate(`(() => {
        const err = document.querySelector(".steer-error");
        const text = err ? err.textContent : "";
        return JSON.stringify({ ok: text.includes("no active turn"), text });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );

  // Phone Settings (#1551): the blanket phone rule
  // `body[data-phone="1"] .segmented { display:none }` was written for the
  // header bar, but the Settings ChoiceRows reuse .segmented, so Scheme /
  // Type size / Density measured 0x0 on phone. The hide is now scoped to
  // `.page-header` in mobile.css. The desktop settings block above cannot
  // catch that — it clicks the inputs through JS, which fires even on
  // display:none controls. Switch to a phone viewport, measure the real
  // geometry at 390x844 and again at 360x800, tap one option from each
  // row at its on-screen centre, and check the header bar itself keeps
  // no visible segmented switch (the hide's original purpose).
  await send("Emulation.setDeviceMetricsOverride", {
    width: 390,
    height: 844,
    deviceScaleFactor: 2,
    mobile: true,
  });
  await waitFor(
    "phone layout engages for the settings check",
    async () => ({
      ok: (await evaluate(`document.body.getAttribute("data-phone")`)) === "1",
    }),
    { timeout: STEP_MS, interval: 150 },
  );
  await evaluate(`document.querySelector('[data-action="open-settings"]')?.click()`);
  await waitFor(
    "phone settings choice rows are visible",
    async () => {
      const raw = await evaluate(`(() => {
        const rows = [...document.querySelectorAll(".settings-page .choice-row .segmented")].map((seg) => {
          const r = seg.getBoundingClientRect();
          return { display: getComputedStyle(seg).display, w: r.width, h: r.height };
        });
        return JSON.stringify({
          ok: rows.length === 3 && rows.every((r) => r.display !== "none" && r.w >= 200 && r.h >= 40),
          rows,
        });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await waitFor(
    "phone header bar keeps no visible segmented switch",
    async () => {
      const raw = await evaluate(`(() => {
        const segs = [...document.querySelectorAll(".page-header .segmented")].map((seg) => {
          const r = seg.getBoundingClientRect();
          return { display: getComputedStyle(seg).display, w: r.width, h: r.height };
        });
        return JSON.stringify({
          ok: segs.every((s) => s.display === "none" || s.w === 0 || s.h === 0),
          segs,
        });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  const tapSettingsChoice = async (choice) => {
    const point = JSON.parse(
      (await evaluate(`(() => {
        const input = document.querySelector('.settings-page input[data-choice="${choice}"]');
        const span = input ? input.closest("label").querySelector("span") : null;
        if (!span) return "{}";
        span.scrollIntoView({ block: "center" });
        const r = span.getBoundingClientRect();
        const cx = r.x + r.width / 2;
        const cy = r.y + r.height / 2;
        const hit = document.elementFromPoint(cx, cy);
        return JSON.stringify({ cx, cy, hitOk: !!hit && (hit === span || span.contains(hit)) });
      })()`)) || "{}",
    );
    if (!point.hitOk) {
      throw new Error(`phone settings ${choice} choice is covered: ${JSON.stringify(point)}`);
    }
    await send("Input.dispatchMouseEvent", {
      type: "mousePressed",
      x: point.cx,
      y: point.cy,
      button: "left",
      clickCount: 1,
    });
    await send("Input.dispatchMouseEvent", {
      type: "mouseReleased",
      x: point.cx,
      y: point.cy,
      button: "left",
      clickCount: 1,
    });
  };
  await tapSettingsChoice("light");
  await waitFor(
    "phone tap changes the scheme",
    async () => ({
      ok: (await evaluate(`document.documentElement.getAttribute("data-scheme")`)) === "light",
    }),
    { timeout: STEP_MS, interval: 150 },
  );
  // Narrow phone (360x800, the second #1551 evidence width): the same
  // three rows keep real geometry, and Type size / Density taps — not
  // just Scheme — take effect.
  await send("Emulation.setDeviceMetricsOverride", {
    width: 360,
    height: 800,
    deviceScaleFactor: 2,
    mobile: true,
  });
  await waitFor(
    "phone settings choice rows are visible at 360",
    async () => {
      const raw = await evaluate(`(() => {
        const rows = [...document.querySelectorAll(".settings-page .choice-row .segmented")].map((seg) => {
          const r = seg.getBoundingClientRect();
          return { display: getComputedStyle(seg).display, w: r.width, h: r.height };
        });
        return JSON.stringify({
          ok: rows.length === 3 && rows.every((r) => r.display !== "none" && r.w >= 200 && r.h >= 40),
          rows,
        });
      })()`);
      return JSON.parse(raw || "{}");
    },
    { timeout: STEP_MS, interval: 200 },
  );
  await tapSettingsChoice("lg");
  await waitFor(
    "phone tap changes the type size",
    async () => ({
      ok: (await evaluate(`document.documentElement.getAttribute("data-font")`)) === "lg",
    }),
    { timeout: STEP_MS, interval: 150 },
  );
  await tapSettingsChoice("compact");
  await waitFor(
    "phone tap changes the density",
    async () => ({
      ok: (await evaluate(`document.documentElement.getAttribute("data-density")`)) === "compact",
    }),
    { timeout: STEP_MS, interval: 150 },
  );
  await send("Emulation.setDeviceMetricsOverride", {
    width: 1440,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
  });
  await evaluate(`document.querySelector('[data-view="chat"]')?.click()`);

  const mcpSecret = "sk-abcdefghijklmnopqrstuvwxyz0123456789";
  await mkdir(join(home, ".strike", "skills"), { recursive: true });
  await writeFile(
    join(home, ".strike", "skills", "smoke-note.md"),
    "---\nname: smoke-note\ndescription: seeded note\n---\nSay hello from smoke.\n",
  );
  await writeFile(join(home, ".strike", "skills", `${BROKEN_SKILL}.md`), BROKEN_SKILL_BODY);
  await seedSkillUsage(home);
  await writeFile(
    join(home, ".strike", "mcp.json"),
    JSON.stringify({
      servers: { docs: { command: "echo", args: ["ok"], env: { TOKEN: mcpSecret } } },
    }),
  );

  const evalJson = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    if (ev.result?.exceptionDetails) {
      throw new Error(`eval failed: ${JSON.stringify(ev.result.exceptionDetails).slice(0, 500)}`);
    }
    return ev.result?.result?.value;
  };
  // #1529: pick the seeded user theme from the catalog-backed picker
  // before the reload below, then prove it survived the reload.
  const themeProbe = `(() => {
    const el = document.documentElement;
    const cs = getComputedStyle(el);
    return JSON.stringify({
      theme: el.getAttribute("data-theme") || "",
      scheme: el.getAttribute("data-scheme") || "",
      acid: cs.getPropertyValue("--acid").trim().toLowerCase(),
      ground: cs.getPropertyValue("--ground").trim().toLowerCase(),
      saved: localStorage.getItem("strike.theme") || "",
      note: document.querySelector("[data-theme-catalog-note]")?.innerText || "",
    });
  })()`;
  const themePainted = async (label, wantId) => {
    let last = {};
    for (let i = 0; i < 60; i++) {
      last = JSON.parse(String(await evalJson(themeProbe)));
      const side = last.scheme === "light" ? "light" : "dark";
      const ok =
        wantId === smokeTheme.id
          ? last.theme === wantId &&
            last.saved === wantId &&
            last.acid === smokeTheme.accent[side] &&
            last.ground === smokeTheme.background[side]
          : last.theme === wantId && last.saved === wantId && last.acid !== smokeTheme.accent[side];
      if (ok) return last;
      await delay(250);
    }
    throw new Error(`${label}: ${JSON.stringify(last)}\n${await diagnostics()}`);
  };
  await evalJson(`document.querySelector('[data-action="open-settings"]')?.click()`);
  for (let i = 0; i < 60; i++) {
    const ready = await evalJson(`(() => {
      const input = document.querySelector('.settings-page input[data-theme-choice="${smokeTheme.id}"]');
      return !!input && !input.disabled && !document.querySelector("[data-theme-catalog-note]");
    })()`);
    if (ready === true) break;
    if (i === 59) {
      throw new Error(`catalog user theme missing from the settings picker: ${String(await evalJson(themeProbe))}\n${await diagnostics()}`);
    }
    await delay(250);
  }
  const pickerText = String(await evalJson(`document.querySelector(".settings-page .theme-grid")?.innerText || ""`));
  if (!pickerText.includes(smokeTheme.name) || !pickerText.includes("User theme") || !pickerText.includes("Nord")) {
    throw new Error(`theme picker should list the user theme beside bundled themes: ${pickerText.slice(0, 600)}`);
  }
  await evalJson(`document.querySelector('.settings-page input[data-theme-choice="${smokeTheme.id}"]').click()`);
  await themePainted("user theme applied from the catalog", smokeTheme.id);

  // The page polls bootstrap every 2s and may already have announced the
  // seeded broken skill; forget announced fingerprints so the reload's
  // session-start notice is deterministic (#1386).
  await evalJson(`(() => { localStorage.setItem("strike.token", ${JSON.stringify(token)}); localStorage.removeItem("strike.loadWarningsSeen"); return true; })()`);
  await send("Page.reload", { ignoreCache: true });
  for (let i = 0; i < 40; i++) {
    const body = await evalJson(`document.body ? document.body.innerText : ""`);
    // No header Menu since #1590: the Customize slot stays mounted (clipped)
    // and the palette is the launcher, so wait on "Run a command".
    const ready = await evalJson(`!!document.querySelector('[data-view="customize"]')`);
    if (String(body).includes("Run a command") && ready === true) break;
    if (i === 39) throw new Error(`reload did not show the cockpit: ${String(body).slice(0, 300)}`);
    await delay(250);
  }
  await loadWarningNoticeCheck({ send });
  await themePainted("user theme persisted across reload", smokeTheme.id);
  await evalJson(`document.querySelector('[data-action="open-settings"]')?.click()`);
  for (let i = 0; i < 60; i++) {
    const clicked = await evalJson(`(() => {
      const input = document.querySelector('.settings-page input[data-theme-choice="strike"]');
      if (!input) return false;
      input.click();
      return true;
    })()`);
    if (clicked === true) break;
    if (i === 59) throw new Error("settings picker did not return after reload");
    await delay(250);
  }
  await themePainted("back to Strike after the user theme", "strike");

  let customizeText = "";
  for (let i = 0; i < 40; i++) {
    customizeText = String(await evalJson(`(() => {
      const btn = document.querySelector('[data-view="customize"]');
      if (btn) btn.click();
      return document.body ? document.body.innerText : "";
    })()`));
    if (customizeText.includes("smoke-note") && customizeText.includes("Skills")) break;
    await delay(250);
  }
  if (!customizeText.includes("smoke-note")) {
    throw new Error(`customize catalog missing smoke-note: ${customizeText.slice(0, 500)}`);
  }
  await loadWarningCustomizeCheck({ send });
  await skillUsageCheck({ send });

  // #1570: Customize > Plugins lifecycle lists GET /v1/plugins, and remove
  // is a two-step action: arming shows a confirm, Cancel sends nothing.
  {
    const res = await fetch(`http://127.0.0.1:${port}/v1/plugins`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    if (res.status !== 200) throw new Error(`plugins HTTP ${res.status}`);
    const wireIds = ((await res.json()).plugins || []).map((p) => p.id);
    if (!wireIds.includes(smokePluginId)) {
      throw new Error(`seeded plugin missing from GET /v1/plugins: ${JSON.stringify(wireIds)}`);
    }
    let last = "";
    for (let i = 0; i < 60; i++) {
      last = String(await evalJson(`(() => {
        document.querySelector('[data-cz-section="plugins"]')?.click();
        const rows = [...document.querySelectorAll('[data-plugin-lifecycle] li[data-plugin-id]')].map((li) => li.getAttribute("data-plugin-id"));
        return JSON.stringify(rows);
      })()`));
      if (last === JSON.stringify(wireIds)) break;
      if (i === 59) throw new Error(`plugin lifecycle rows ${last} != GET /v1/plugins ${JSON.stringify(wireIds)}\n${await diagnostics()}`);
      await delay(250);
    }
    const row = `[data-plugin-lifecycle] li[data-plugin-id="${smokePluginId}"]`;
    const trustText = String(await evalJson(`document.querySelector('${row} [data-plugin-trust]')?.innerText || ""`));
    if (!trustText.includes("Passive only")) throw new Error(`passive plugin trust label: ${trustText}`);
    if (await evalJson(`!!document.querySelector('${row} [data-plugin-action="trust-preview"]')`)) {
      throw new Error("a passive-only plugin must not offer trust");
    }
    await evalJson(`document.querySelector('${row} [data-plugin-action="remove"]').click()`);
    for (let i = 0; i < 40; i++) {
      const armed = await evalJson(`!!document.querySelector('${row} [data-plugin-remove-confirm]') && !!document.querySelector('${row} [data-plugin-action="confirm"]')`);
      if (armed === true) break;
      if (i === 39) throw new Error(`remove did not arm a confirm step\n${await diagnostics()}`);
      await delay(250);
    }
    await evalJson(`document.querySelector('${row} [data-plugin-action="cancel"]').click()`);
    for (let i = 0; i < 40; i++) {
      const cleared = await evalJson(`!document.querySelector('[data-plugin-lifecycle] [data-plugin-remove-confirm]') && !!document.querySelector('${row}')`);
      if (cleared === true) break;
      if (i === 39) throw new Error("cancel did not clear the remove confirm");
      await delay(250);
    }
    const after = await fetch(`http://127.0.0.1:${port}/v1/plugins`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    const afterIds = ((await after.json()).plugins || []).map((p) => p.id);
    if (!afterIds.includes(smokePluginId)) throw new Error("cancelled remove still removed the plugin");
    await evalJson(`document.querySelector('[data-cz-section="skills"]')?.click()`);
  }

  await evalJson(`(() => {
    const tabs = document.querySelector('[aria-label="Customize sections"]');
    tabs.focus();
    tabs.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    return "sent";
  })()`);
  let agentsSelected = "";
  for (let i = 0; i < 20; i++) {
    agentsSelected = String(await evalJson(`document.querySelector('[data-cz-section="agents"]')?.getAttribute("aria-selected") || ""`));
    if (agentsSelected === "true") break;
    await delay(50);
  }
  if (agentsSelected !== "true") {
    throw new Error(`customize keyboard aria-selected=${agentsSelected}`);
  }
  await evalJson(`document.querySelector('[data-cz-section="skills"]').click()`);
  await evalJson(`document.querySelector("[data-cz-new]").click()`);
  let nameReady = false;
  for (let i = 0; i < 20; i++) {
    const value = await evalJson(`!!document.querySelector('[data-cz-field="name"]') && !!document.querySelector('[data-cz-field="body"]')`);
    if (value === true) { nameReady = true; break; }
    await delay(50);
  }
  if (!nameReady) throw new Error("new skill form did not open");
  await evalJson(`(() => {
    const set = (el, value) => {
      const proto = el.tagName === "TEXTAREA" ? HTMLTextAreaElement.prototype : HTMLInputElement.prototype;
      Object.getOwnPropertyDescriptor(proto, "value").set.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    };
    set(document.querySelector('[data-cz-field="name"]'), "mcp");
    set(document.querySelector('[data-cz-field="body"]'), "Nope.");
    document.querySelector("[data-cz-preview]").click();
    return "ok";
  })()`);
  let errText = "";
  for (let i = 0; i < 40; i++) {
    errText = String(await evalJson(`document.body ? document.body.innerText : ""`));
    if (errText.toLowerCase().includes("reserved")) break;
    await delay(250);
  }
  if (!errText.toLowerCase().includes("reserved")) {
    throw new Error(`reserved skill error missing: ${errText.slice(0, 500)}`);
  }

  await evalJson(`document.querySelector('[data-cz-item="smoke-note"]').click()`);
  let opened = false;
  for (let i = 0; i < 40; i++) {
    const value = await evalJson(`document.querySelector('[data-cz-field="description"]')?.value || ""`);
    if (value === "seeded note") { opened = true; break; }
    await delay(250);
  }
  if (!opened) throw new Error("smoke-note editor did not open");
  await evalJson(`(() => {
    const el = document.querySelector('[data-cz-field="description"]');
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value").set.call(el, "updated note");
    el.dispatchEvent(new Event("input", { bubbles: true }));
    document.querySelector("[data-cz-preview]").click();
    return "ok";
  })()`);
  let diffText = "";
  for (let i = 0; i < 40; i++) {
    diffText = String(await evalJson(`document.querySelector(".cz-diff")?.textContent || ""`));
    const save = await evalJson(`(() => { const b = document.querySelector("[data-cz-save]"); return b ? b.disabled : true; })()`);
    if (diffText.includes("updated note") && save === false) break;
    await delay(250);
  }
  if (!diffText.includes("updated note")) {
    throw new Error(`preview diff missing update: ${diffText.slice(0, 400)}`);
  }
  await evalJson(`document.querySelector("[data-cz-save]").click()`);
  let saved = "";
  for (let i = 0; i < 40; i++) {
    saved = String(await evalJson(`document.body ? document.body.innerText : ""`));
    if (saved.includes("Saved") || saved.includes("updated note")) break;
    await delay(250);
  }
  if (!saved.includes("Saved") && !saved.includes("updated note")) {
    throw new Error(`save did not land: ${saved.slice(0, 400)}`);
  }

  await evalJson(`document.querySelector('[data-cz-section="mcp"]').click()`);
  let openedMcp = false;
  for (let i = 0; i < 20; i++) {
    openedMcp = (await evalJson(`(() => {
      const b = document.querySelector('[data-cz-item="docs"]');
      if (!b) return false;
      b.click();
      return true;
    })()`)) === true;
    if (openedMcp) break;
    await delay(50);
  }
  if (!openedMcp) throw new Error("mcp server docs was not listed");
  let mcpText = "";
  for (let i = 0; i < 40; i++) {
    mcpText = String(await evalJson(`(() => {
      const bits = [document.body ? document.body.innerText : ""];
      for (const el of document.querySelectorAll("input,textarea")) bits.push(el.value || "");
      return bits.join("\\n");
    })()`));
    if (mcpText.includes("TOKEN=")) break;
    await delay(250);
  }
  if (mcpText.includes(mcpSecret)) {
    throw new Error("customize UI showed an MCP secret");
  }
  const values = String(await evalJson(`(() => [...document.querySelectorAll("input,textarea")].map((el) => el.value || "").join("\\n"))()`));
  if (values.includes(mcpSecret)) {
    throw new Error("customize field showed an MCP secret");
  }
  if (!values.includes("TOKEN") || !values.includes("••••")) {
    const extra = String(await evalJson(`document.querySelector(".cz-error")?.textContent || ""`));
    const docEv = await send("Runtime.evaluate", {
      expression: `fetch("/v1/customize/doc?kind=mcp&name=docs&scope=global",{headers:{Authorization:"Bearer "+localStorage.getItem("strike.token")}}).then(async (r) => r.status + " " + (await r.text()).slice(0, 500))`,
      awaitPromise: true,
      returnByValue: true,
    });
    const docHead = docEv.result?.result?.value || JSON.stringify(docEv).slice(0, 600);
    throw new Error(`mcp form did not mask env err=${extra} doc=${docHead} values=${values.slice(0, 300)} body=${mcpText.slice(0, 300)}`);
  }

  await seedRepeatedWorkflow({ home, port, token });
  await skillProposalsCheck({ send, repoDir: process.cwd() });

  console.log("webui-smoke ok");
} catch (err) {
  console.error(String(err));
  if (collectDiagnostics && !String(err).includes("smoke diagnostics")) {
    try {
      console.error(await collectDiagnostics());
    } catch {
      // page already gone; the original error stands
    }
  }
  if (strikeLog) console.error(strikeLog.slice(-2000));
  process.exitCode = 1;
} finally {
  // Wait for both children to actually exit before removing their files
  // (Chrome profile, daemon HOME): the old fixed 200ms sleep raced a
  // still-shutting-down Chrome and crashed with ENOTEMPTY after every
  // assertion had passed (#1477). maxRetries is the belt-and-braces for
  // a file released a beat after exit — rm retries ENOTEMPTY/EBUSY
  // instead of failing the run.
  await Promise.all([stopChild(chrome), stopChild(strike)]);
  await rm(gitDiffSeedPath, { force: true });
  await rm(home, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  await rm(chromeData, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
}
