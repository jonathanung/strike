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
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
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

const strike = spawn(
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
  if (missing !== 401 || wrong !== 401 || right !== 200) {
    throw new Error(`auth statuses missing=${missing} wrong=${wrong} right=${right}`);
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
          text.includes("Run a command") &&
          text.includes("Activity") &&
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
  const want = ["Run a command", "Strike", "Activity", "Files", "Changes", "Memory", "Knowledge", "Artifacts", "Approval"];
  const missingText = want.filter((s) => !text.includes(s));
  if (missingText.length || acid !== "#7c3aed" || ground !== "#14131c" || !chip || !statusIds.includes(statusId) || !title.includes("Strike")) {
    throw new Error(
      `cockpit check failed missing=${missingText.join(",")} acid=${acid} ground=${ground} status=${statusId} chip=${chip} title=${title} text=${text.slice(0, 400)}\n${await diagnostics()}`,
    );
  }
  const evalValue = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    if (ev.result?.exceptionDetails) {
      throw new Error(`evaluate failed: ${JSON.stringify(ev.result.exceptionDetails)}`);
    }
    return ev.result?.result?.value;
  };
  const bodyText = async () => String((await evalValue("document.body ? document.body.innerText : ''")) || "");

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
  await editorBrowserCheck({
    send,
    port,
    token,
    repoDir: process.cwd(),
  });

  const before = await send("Runtime.evaluate", {
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
  const beforeState = JSON.parse(before.result?.result?.value || "{}");
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
    "cockpit reload after attachment checks (Run a command, Memory)",
    textProbe((t) => t.includes("Run a command") && t.includes("Memory")),
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
        ok: body.includes("Run a command") && body.includes("Memory"),
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
