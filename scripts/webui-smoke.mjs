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
import { spawn } from "node:child_process";
import { editorBrowserCheck } from "./webui-editor-check.mjs";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const token = "webui-smoke-token";
const port = 18087 + (process.pid % 1000);
const home = await mkdtemp(join(tmpdir(), "strike-webui-smoke-"));
const chromeData = await mkdtemp(join(tmpdir(), "strike-webui-chrome-"));
const chromePort = 9222 + (process.pid % 200);

// Render/boot waits get a deadline sized for shared CI runners; shorter
// interactive steps get a smaller one. Both are overridable for local runs.
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
    envLine("text.delta", "2026-10-26T06:10:10Z", { text: "fast answer" }) +
    envLine("turn.completed", "2026-10-26T06:10:20Z", { stopReason: "end_turn" }) +
    envLine("user.message", "2026-10-26T06:20:00Z", { text: "old log turn" }) +
    envLine("tool.begin", null, { callId: "c9", name: "bash", args: { command: "sleep 240" } }) +
    envLine("tool.end", "2026-10-26T06:24:00Z", { callId: "c9", name: "bash", output: "done" }) +
    envLine("text.delta", "2026-10-26T06:24:30Z", { text: "old answer" }) +
    envLine("turn.completed", "2026-10-26T06:25:00Z", { stopReason: "end_turn" });
  await writeFile(join(sessionsDir, "elapsed-session.jsonl"), elapsedSession);
  await writeFile(
    join(sessionsDir, "elapsed-session.meta.json"),
    JSON.stringify({ title: "Elapsed replay" }) + "\n",
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
          status: mark ? mark.getAttribute("data-status") : "",
          chip: !!document.querySelector(".status-chip-hit"),
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
          text.includes("Menu") &&
          text.includes("Activity") &&
          text.includes("Connected") &&
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
  const want = ["Menu", "Strike", "Activity", "Files", "Changes", "Git", "Worktrees", "Memory", "Knowledge", "Artifacts", "Terminal", "Approval", "Connected"];
  const missingText = want.filter((s) => !text.includes(s));
  if (missingText.length || acid !== "#7c3aed" || ground !== "#14131c" || !chip || !statusIds.includes(statusId) || !title.includes("Strike")) {
    throw new Error(
      `cockpit check failed missing=${missingText.join(",")} acid=${acid} ground=${ground} status=${statusId} chip=${chip} title=${title} text=${text.slice(0, 400)}\n${await diagnostics()}`,
    );
  }
  // Approvals (#1266): mode menu, Yolo confirm + Escape, and the
  // permission-rules API confirm gates. Runs while the boot session is
  // live (rule writes require a live root) and leaves the chip on Default.
  const apprEval = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    return ev.result?.result?.value || "";
  };
  const apprText = () => apprEval("document.body ? document.body.innerText : \"\"");
  const apprClickChip = () =>
    apprEval(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => (b.textContent || "").includes("Approval"));
      if (!btn) return "no-chip";
      btn.click();
      return "clicked";
    })()`);
  // Condition-based waits with the clicks inside the probes (this file's
  // post-#1378 discipline): a click that lands mid-render is retried
  // instead of failing against a fixed deadline.
  await waitFor(
    "approval menu (modes, Rules and audit, deny copy)",
    async () => {
      const t = await apprText();
      if (t.includes("Rules and audit") && t.includes("Deny rules still apply") && t.includes("Yolo")) {
        return { ok: true, t };
      }
      await apprClickChip();
      return { ok: false };
    },
    { timeout: STEP_MS, interval: 250 },
  );
  const apprMenu = await apprText();
  if (!apprMenu.includes("Default") || !apprMenu.includes("Plan") || !apprMenu.includes("Yolo") || !apprMenu.includes("Deny rules still apply")) {
    throw new Error(`approval menu missing copy: ${apprMenu.slice(0, 500)}`);
  }
  await waitFor(
    "yolo confirm dialog",
    async () => {
      const t = await apprText();
      if (t.includes("Use Yolo for this session?")) {
        return { ok: true };
      }
      const r = await apprEval(`(() => {
        const btn = [...document.querySelectorAll("button")].find((b) => (b.textContent || "").trim().startsWith("Yolo"));
        if (!btn) return "no-yolo";
        btn.click();
        return "clicked";
      })()`);
      if (r !== "clicked") {
        await apprClickChip();
      }
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

  const evalValue = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    if (ev.result?.exceptionDetails) {
      throw new Error(`evaluate failed: ${JSON.stringify(ev.result.exceptionDetails)}`);
    }
    return ev.result?.result?.value;
  };
  const bodyText = async () => String((await evalValue("document.body ? document.body.innerText : ''")) || "");

  // Session-ux transcript (#1310): an empty transcript teaches with
  // suggestion cards whose detail line is the prompt that gets sent, and
  // the pane header carries Rename / Regenerate / Export.
  await waitFor(
    "session-ux empty transcript (suggestion cards, rename, export)",
    async () => {
      const t = await bodyText();
      return {
        ok:
          t.includes("See what is here") &&
          t.includes("Summarize the working directory. Do not change files.") &&
          t.includes("Rename") &&
          t.includes("Regenerate") &&
          t.includes("Export"),
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

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
  const gwClicked = await evalValue(`(() => {
    const more = document.querySelector('[data-nav="more"]');
    if (more) more.click();
    const btn = [...document.querySelectorAll("button")].find((b) => (b.textContent || "").includes("Gateways"));
    if (!btn) return "missing";
    btn.click();
    return "ok";
  })()`);
  if (gwClicked !== "ok") {
    throw new Error("gateway switcher button missing");
  }
  const gwState = await waitFor(
    "gateway switcher panel (This gateway, Reconnect, Connected)",
    async () => {
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
          visibleInputs,
        });
      })()`)) || "{}");
      return {
        ok:
          !!parsed.open &&
          (parsed.pop || "").includes("This gateway") &&
          (parsed.pop || "").includes("Reconnect") &&
          (parsed.pop || "").includes("Connected"),
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


  // Model menu: filter, honest "not reported" capabilities, keyboard, and a
  // real select.model error from the daemon (unknown provider).
  await send("Runtime.evaluate", {
    expression: `document.querySelector("[data-model-chip]")?.click()`,
  });
  let menu = "";
  for (let i = 0; i < 40; i++) {
    menu = await bodyText();
    if (menu.includes("Filter models") && menu.includes("echo") && menu.includes("not reported")) break;
    await delay(250);
  }
  const menuWant = [
    "Filter models",
    "echo",
    "Capabilities not reported",
    "Context window not reported",
    "Reasoning support is not reported",
  ];
  const menuMissing = menuWant.filter((s) => !menu.includes(s));
  if (menuMissing.length) {
    throw new Error(`model menu missing=${menuMissing.join("|")} text=${menu.slice(0, 500)}`);
  }
  // Model-menu panel geometry: the panel must lie fully inside the
  // viewport. innerText assertions alone missed PR #1303's CSP defect —
  // placement was set via a style attribute that `style-src 'self'`
  // (no 'unsafe-inline') makes full Chrome ignore, leaving the panel
  // off-viewport while its text still read correctly.
  let panelRect = null;
  for (let i = 0; i < 20; i++) {
    panelRect = await evalValue(`(() => {
      const el = document.querySelector("[data-model-menu]");
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
    throw new Error(`model menu panel outside viewport rect=${JSON.stringify(panelRect)}`);
  }
  const modelFiltered = await evalValue(`(() => {
    const el = document.querySelector("[data-model-filter]");
    if (!el) return "missing";
    el.focus();
    el.value = "zzz";
    el.dispatchEvent(new Event("input", { bubbles: true }));
    return "ok";
  })()`);
  if (modelFiltered !== "ok") {
    throw new Error(`model filter input missing (${modelFiltered})`);
  }
  let empty = "";
  for (let i = 0; i < 20; i++) {
    empty = await bodyText();
    if (empty.includes("No models match")) break;
    await delay(100);
  }
  if (!empty.includes("No models match")) {
    throw new Error(`model filter did not empty the list: ${empty.slice(0, 400)}`);
  }
  await evalValue(`(() => {
    const el = document.querySelector("[data-model-filter]");
    el.value = "nope/missing";
    el.dispatchEvent(new Event("input", { bubbles: true }));
    el.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
    return "ok";
  })()`);
  let failed = "";
  for (let i = 0; i < 40; i++) {
    failed = await bodyText();
    if (failed.includes("unknown provider")) break;
    await delay(250);
  }
  if (!failed.includes("unknown provider")) {
    throw new Error(`model error path missing daemon message: ${failed.slice(0, 500)}`);
  }

  // Close the model menu (and any error note) before the navbar pass.
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
      ["Conductor", "Archived", "Folders group sessions", "Palette polish", "Alpha session"].every((x) =>
        t.includes(x),
      ),
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
  await evaluate(`document.querySelector('[data-action="open-settings"]').click()`);
  await waitUntil(
    `document.body.innerText.includes("Keyboard") && document.body.innerText.includes(".strike") && document.body.innerText.includes("Open model menu")`,
    "settings page",
  );
  await evaluate(`document.querySelector('input[data-choice="lg"]').click()`);
  await evaluate(`document.querySelector('input[data-choice="compact"]').click()`);
  await waitUntil(
    `document.documentElement.getAttribute("data-font") === "lg" && document.documentElement.getAttribute("data-density") === "compact" && localStorage.getItem("strike.font") === "lg"`,
    "font and density",
  );

  await evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent === "Daemon").click()`);
  await evaluate(`(() => {
    const input = document.querySelector(".daemon input");
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(input, "http://127.0.0.1:1");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    [...document.querySelectorAll(".daemon button")].find((b) => b.textContent === "Connect").click();
    return true;
  })()`);
  await evaluate(`document.querySelector('[data-action="open-settings"]').click()`);
  await waitUntil(
    `document.body.innerText.includes("Could not read the file list") && document.querySelector('[data-action="reload-files"]') != null`,
    "settings error path",
  );
  await evaluate(`document.querySelector('[data-action="reload-files"]').click()`);
  await waitUntil(
    `document.body.innerText.includes("Could not read the file list")`,
    "retry while the daemon URL is still wrong",
  );
  await evaluate(`[...document.querySelectorAll("button")].find((b) => b.textContent === "Daemon").click()`);
  await evaluate(`(() => {
    const input = document.querySelector(".daemon input");
    const set = Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value").set;
    set.call(input, "");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    [...document.querySelectorAll(".daemon button")].find((b) => b.textContent === "Connect").click();
    return true;
  })()`);
  await evaluate(`document.querySelector('[data-action="open-settings"]').click()`);
  await waitUntil(
    `document.body.innerText.includes(".strike") && !document.body.innerText.includes("Could not read the file list")`,
    "settings recovered",
  );
  await editorBrowserCheck({
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
  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
  });
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "cockpit reload after attachment checks (Menu, Memory)",
    textProbe((t) => t.includes("Menu") && t.includes("Memory")),
    { timeout: WAIT_MS, interval: 250 },
  );

  // Usage + Audit (#1286): open each view and confirm its controls
  // render; the Audit view must also report a chain state. Ported onto
  // the hardened script's helpers: evaluate() surfaces page exceptions,
  // and waitFor()/textProbe() poll on condition with diagnostics on
  // timeout instead of the old fixed-iteration body scrape.
  const openView = async (label, needles) => {
    const clicked = await evaluate(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === ${JSON.stringify(label)});
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`);
    if (clicked !== "clicked") {
      throw new Error(`${label} nav button not found\n${await diagnostics()}`);
    }
    await waitFor(
      `${label} view (${needles.join(", ")})`,
      textProbe((t) => needles.every((s) => t.includes(s))),
      { timeout: WAIT_MS, interval: 250 },
    );
  };
  await openView("Usage", ["Input tokens", "Daily budget (USD)"]);
  await openView("Audit", ["Verify chain", "Export"]);
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
        ok: body.includes("Menu") && body.includes("Memory"),
        body: body.slice(0, 300),
      };
    },
    { timeout: WAIT_MS, interval: 250 },
  );

  const memClick = await evaluate(`(() => {
    const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Memory");
    if (!btn) return "missing";
    btn.click();
    return "clicked";
  })()`);
  if (memClick !== "clicked") {
    throw new Error(`memory nav button not found after reload\n${await diagnostics()}`);
  }
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
  // block to ~35px (PR #1305 review).
  const memKeyWidthPx = await evaluate(`(() => {
    const row = [...document.querySelectorAll(".storage-row")].find((r) => r.textContent.includes("smoke-pref"));
    const link = row ? row.querySelector(".link") : null;
    return link ? link.getBoundingClientRect().width : 0;
  })()`);
  if (!(memKeyWidthPx > 100)) {
    throw new Error(`memory row key cell too narrow width=${memKeyWidthPx}`);
  }

  // Goals + Autopilot (#1300). Require the real Goals nav button in the
  // DOM — not a body-text scrape. A bundle that predates the Goals
  // feature has no such button and fails here with diagnostics, however
  // long the wait; that is the point of the check.
  await waitFor(
    "goals nav button + command pill",
    async () => {
      const raw = await evaluate(`(() => JSON.stringify({
        goalsBtn: !!document.querySelector('button[data-view="goals"]'),
        pill: !!document.querySelector('[data-nav="palette"]'),
        views: [...document.querySelectorAll("[data-view]")].map((e) => e.getAttribute("data-view")),
      }))()`);
      const p = JSON.parse(raw || "{}");
      return { ok: !!(p.goalsBtn && p.pill), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('button[data-view="goals"]').click()`);
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
    textProbe((body) => body.includes("smoke pass") && body.includes("Done")),
    { timeout: WAIT_MS, interval: 250 },
  );
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
    textProbe((body) => body.includes("Needs you") && body.includes("verification failed")),
    { timeout: WAIT_MS, interval: 250 },
  );

  const clicked = await send("Runtime.evaluate", {
    expression: `(() => {
      const more = document.querySelector('[data-nav="more"]');
      if (more && more.getAttribute("aria-expanded") !== "true") more.click();
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
        composer: !!document.querySelector(".side-chat-open-composer"),
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

  const seeded = await fetch(`http://127.0.0.1:${port}/v1/ops`, {
    method: "POST",
    headers: {
      Authorization: `Bearer ${token}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ type: "user.input", data: { text: "read the makefile" } }),
  });
  if (!seeded.ok) {
    throw new Error(`seed user.input ${seeded.status} ${await seeded.text()}`);
  }

  await evaluate(`document.querySelector(".side-chat-open-composer").click()`);

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
  const answered = await waitFor(
    "side chat to echo the asked question",
    async () => {
      const t = await panelText();
      return { ok: t.includes("what is the agent doing?"), text: t };
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
  await clickSideTab("Git", "Staged");
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
          `(() => { const s = document.querySelector('select[aria-label="Branch"]'); return s ? JSON.stringify({ value: s.value, options: s.options.length }) : null; })()`,
        );
        const parsed = raw ? JSON.parse(raw) : {};
        return { ok: parsed.value === currentBranch && parsed.options >= 1, ...parsed, currentBranch };
      },
      { timeout: STEP_MS, interval: 250 },
    );
  }
  await clickSideTab("Worktrees", "session.childIsolation");

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
  const deleteBtnSel = (id) => `[data-session-delete="${id}"]`;
  const clickSel = async (sel) => {
    const v = await evaluate(
      `(() => { const el = document.querySelector(${JSON.stringify(sel)}); if (!el) return "missing"; el.click(); return "clicked"; })()`,
    );
    if (v !== "clicked") throw new Error(`session-delete click ${sel} -> ${v}`);
  };
  const dialogText = async () =>
    evaluate(
      `(() => { const el = document.querySelector("[data-session-delete-dialog]"); return el ? el.innerText : ""; })()`,
    );
  await evaluate(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.navigate", {
    url: "http://127.0.0.1:" + port + "/?token=" + token,
  });
  await waitFor(
    "cockpit re-boot for session delete",
    textProbe((t) => t.includes("Menu")),
    { timeout: WAIT_MS, interval: 250 },
  );
  await waitFor(
    "seeded session row with a Delete button",
    async () => ({
      ok:
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(deleteBtnSel(sacrificial))})`,
        )) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  await clickSel(deleteBtnSel(sacrificial));
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
  await clickSel("[data-session-delete-cancel]");
  await waitFor(
    "confirm dialog to close on cancel",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-session-delete-dialog]")`)) === false,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  if (!(await savedIDs()).includes(sacrificial)) {
    throw new Error("cancelled delete still removed the session on the daemon");
  }
  await clickSel(deleteBtnSel(sacrificial));
  await waitFor(
    "delete confirm dialog to reopen",
    async () => ({ ok: ((await dialogText()) || "").includes("cannot be undone") }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-session-delete-confirm]");
  await waitFor(
    "deleted session to leave the daemon list and the page",
    async () => ({
      ok:
        !(await savedIDs()).includes(sacrificial) &&
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(deleteBtnSel(sacrificial))})`,
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
          `!!document.querySelector(${JSON.stringify(deleteBtnSel(openID))})`,
        )) === true,
    }),
    { timeout: STEP_MS, interval: 250 },
  );
  await clickSel(deleteBtnSel(openID));
  await waitFor(
    "delete confirm for the open session",
    async () => ({ ok: ((await dialogText()) || "").includes("cannot be undone") }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-session-delete-confirm]");
  const forceText = await waitFor(
    "force confirmation after the daemon 409",
    async () => {
      const t = (await dialogText()) || "";
      const hasForceBtn =
        (await evaluate(`!!document.querySelector("[data-session-delete-force]")`)) === true;
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
  await clickSel("[data-session-delete-cancel]");
  await waitFor(
    "force confirmation to close on cancel",
    async () => ({
      ok: (await evaluate(`!!document.querySelector("[data-session-delete-dialog]")`)) === false,
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
  await clickSel(deleteBtnSel(openID));
  await waitFor(
    "delete confirm to reopen for the force-through",
    async () => ({ ok: ((await dialogText()) || "").includes("cannot be undone") }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-session-delete-confirm]");
  await waitFor(
    "force confirmation to reopen",
    async () => ({
      ok:
        (await evaluate(`!!document.querySelector("[data-session-delete-force]")`)) === true,
    }),
    { timeout: STEP_MS, interval: 200 },
  );
  await clickSel("[data-session-delete-force]");
  await waitFor(
    "force-deleted session to leave the daemon lists and the page",
    async () => ({
      ok:
        !(await rootIDs()).includes(openID) &&
        !(await savedIDs()).includes(openID) &&
        (await evaluate(
          `!!document.querySelector(${JSON.stringify(deleteBtnSel(openID))})`,
        )) === false,
    }),
    { timeout: WAIT_MS, interval: 250 },
  );
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
      "task runner board after restart (Failed and Blocked rows)",
      async () => {
        const clicked = await evaluate(
          `(() => { const btn = document.querySelector('button[data-view="tasks"]'); if (!btn) return false; btn.click(); return true; })()`,
        );
        const boardText =
          (await evaluate(
            `(() => { const b = document.querySelector("#task-runner"); return b ? b.innerText : ""; })()`,
          )) || "";
        return {
          ok:
            clicked === true &&
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
      textProbe((body) => body.includes("Menu")),
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
  const elapsedClicked = await evaluate(`(() => {
    const row = document.querySelector('[data-session-id="elapsed-session"]');
    const btn = row && row.querySelector(".ss-main");
    if (!btn) return "missing";
    btn.click();
    return "clicked";
  })()`);
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
  // Schedule page (#1276): the top-bar switch (data-view="schedule",
  // registered by src/shell/contrib/scheduler.rs) opens the page and the
  // empty state shows; the daemon token is already in localStorage from
  // the boot script, so no reload is needed (the original PR's reload
  // loop is replaced by the shared waitFor/textProbe helpers).
  await evaluate(`document.querySelector('button[data-view="schedule"]').click()`);
  await waitFor(
    "schedule page (empty state)",
    textProbe((body) => body.includes("Nothing is scheduled") && !body.includes("unauthorized")),
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
  await send("Runtime.evaluate", {
    expression: `(() => {
      const b = [...document.querySelectorAll("button")].find((el) => (el.textContent || "").trim() === "Triggers");
      if (!b) return "missing";
      b.click();
      return "ok";
    })()`,
    returnByValue: true,
  });
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

  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
    returnByValue: true,
  });
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitFor(
    "apps nav button",
    async () => {
      const raw = await evaluate(`(() => {
        const btn = document.querySelector('button[data-view="apps"]');
        return JSON.stringify({
          btn: !!btn,
          label: btn ? btn.textContent.trim() : "",
        });
      })()`);
      const p = JSON.parse(raw || "{}");
      return { ok: !!(p.btn && p.label === "Apps"), ...p };
    },
    { timeout: WAIT_MS, interval: 250 },
  );
  await evaluate(`document.querySelector('button[data-view="apps"]').click()`);
  await waitFor(
    "apps launcher GitHub triage",
    textProbe((body) => body.includes("GitHub triage")),
    { timeout: WAIT_MS, interval: 250 },
  );


  const mcpSecret = "sk-abcdefghijklmnopqrstuvwxyz0123456789";
  await mkdir(join(home, ".strike", "skills"), { recursive: true });
  await writeFile(
    join(home, ".strike", "skills", "smoke-note.md"),
    "---\nname: smoke-note\ndescription: seeded note\n---\nSay hello from smoke.\n",
  );
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
  await evalJson(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.reload", { ignoreCache: true });
  for (let i = 0; i < 40; i++) {
    const body = await evalJson(`document.body ? document.body.innerText : ""`);
    if (String(body).includes("Run a command") && String(body).includes("Customize")) break;
    if (i === 39) throw new Error(`reload did not show the cockpit: ${String(body).slice(0, 300)}`);
    await delay(250);
  }

  let customizeText = "";
  for (let i = 0; i < 40; i++) {
    customizeText = String(await evalJson(`(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Customize");
      if (btn) btn.click();
      return document.body ? document.body.innerText : "";
    })()`));
    if (customizeText.includes("smoke-note") && customizeText.includes("Skills")) break;
    await delay(250);
  }
  if (!customizeText.includes("smoke-note")) {
    throw new Error(`customize catalog missing smoke-note: ${customizeText.slice(0, 500)}`);
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
  if (chrome) chrome.kill("SIGTERM");
  strike.kill("SIGTERM");
  await delay(200);
  await rm(home, { recursive: true, force: true });
  await rm(chromeData, { recursive: true, force: true });
}
