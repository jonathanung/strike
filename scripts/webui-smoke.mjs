// Headless check that the embedded Rust cockpit boots and that --auth rejects
// a missing or wrong bearer. Chrome is required (google-chrome or chromium).
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
  ws.addEventListener("message", (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
    }
  });
  const send = (method, params) =>
    new Promise((resolve) => {
      const id = ++next;
      pending.set(id, resolve);
      ws.send(JSON.stringify({ id, method, params }));
    });

  await send("Page.enable");
  await send("Runtime.enable");
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

  let text = "";
  let acid = "";
  let ground = "";
  let title = "";
  let statusId = "";
  let chip = false;
  const statusIds = ["idle", "running", "waiting", "blocked", "failed", "done", "stopped", "disconnected"];
  for (let i = 0; i < 40; i++) {
    const parsed = await readCockpit();
    text = parsed.body || "";
    acid = parsed.acid || "";
    ground = parsed.ground || "";
    title = parsed.title || "";
    statusId = parsed.status || "";
    chip = !!parsed.chip;
    if (
      text.includes("Run a command") &&
      text.includes("Activity") &&
      acid === "#7c3aed" &&
      chip &&
      statusIds.includes(statusId) &&
      title.includes("Strike")
    ) {
      break;
    }
    await delay(250);
  }
  const want = ["Run a command", "Strike", "Activity", "Files", "Changes", "Memory", "Knowledge", "Artifacts", "Approval"];
  const missingText = want.filter((s) => !text.includes(s));
  if (missingText.length || acid !== "#7c3aed" || ground !== "#14131c" || !chip || !statusIds.includes(statusId) || !title.includes("Strike")) {
    throw new Error(
      `cockpit check failed missing=${missingText.join(",")} acid=${acid} ground=${ground} status=${statusId} chip=${chip} title=${title} text=${text.slice(0, 400)}`,
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
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    const value = ev.result?.result?.value;
    if (ev.result?.exceptionDetails) {
      throw new Error(`navbar check exception: ${JSON.stringify(ev.result.exceptionDetails).slice(0, 500)}`);
    }
    return value;
  };
  await navbar(`document.querySelector('[data-nav="palette"]').click()`);
  let palette = "";
  for (let i = 0; i < 20; i++) {
    palette = await navbar(`document.body.innerText`);
    if (palette.includes("Open session") && palette.includes("New session")) break;
    await delay(100);
  }
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
  let filtered = "";
  for (let i = 0; i < 20; i++) {
    filtered = await navbar(`document.querySelector('[data-nav="palette-dialog"]').innerText`);
    if (filtered.includes("Theme: Nord") && !filtered.includes("New session")) break;
    await delay(50);
  }
  if (!filtered.includes("Theme: Nord") || filtered.includes("New session")) {
    throw new Error(`palette fuzzy failed text=${filtered.slice(0, 500)}`);
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
  let alertText = "";
  for (let i = 0; i < 20; i++) {
    alertText = await navbar(`(document.querySelector('[role="alert"]') || {}).innerText || ""`);
    if (String(alertText).includes("Nothing matches")) break;
    await delay(50);
  }
  if (!String(alertText).includes("Nothing matches")) {
    throw new Error(`palette error path failed alert=${alertText} typed=${typed}`);
  }
  await navbar(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await navbar(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);
  await navbar(`(() => { if (document.activeElement && document.activeElement.blur) document.activeElement.blur(); return true; })()`);
  await navbar(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "?", bubbles: true }))`);
  let help = "";
  for (let i = 0; i < 20; i++) {
    help = await navbar(`(document.querySelector('[data-nav="help"]') || {}).innerText || ""`);
    if (String(help).includes("Alt+Left") && String(help).includes("Focus mode")) break;
    await delay(50);
  }
  if (!String(help).includes("Focus mode")) {
    throw new Error(`shortcut overlay failed text=${help}`);
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
  const openView = async (label, needles) => {
    await send("Runtime.evaluate", {
      expression: `(() => {
        const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === ${JSON.stringify(label)});
        if (!btn) return "missing";
        btn.click();
        return "ok";
      })()`,
      returnByValue: true,
    });
    let body = "";
    for (let i = 0; i < 30; i++) {
      const ev = await send("Runtime.evaluate", {
        expression: `document.body ? document.body.innerText : ""`,
        returnByValue: true,
      });
      body = ev.result?.result?.value || "";
      if (needles.every((s) => body.includes(s))) return;
      await delay(200);
    }
    throw new Error(`${label} view missing ${needles.join(", ")} text=${body.slice(0, 500)}`);
  };
  await openView("Usage", ["Input tokens", "Daily budget (USD)"]);
  await openView("Audit", ["Verify chain", "Export"]);
  const verified = await send("Runtime.evaluate", {
    expression: `document.body ? document.body.innerText : ""`,
    returnByValue: true,
  });
  const auditText = verified.result?.result?.value || "";
  if (!auditText.includes("Chain intact") && !auditText.includes("No audit entries") && !auditText.includes("Not configured")) {
    throw new Error(`audit chain state missing: ${auditText.slice(0, 500)}`);
  }

  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
  });
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  for (let i = 0; i < 40; i++) {
    const parsed = await readCockpit();
    text = parsed.body || "";
    if (text.includes("Run a command") && text.includes("Memory")) break;
    await delay(250);
  }

  await send("Runtime.evaluate", {
    expression: `(() => {
      const btn = [...document.querySelectorAll("button")].find((b) => b.textContent.trim() === "Memory");
      if (!btn) return "missing";
      btn.click();
      return "clicked";
    })()`,
    returnByValue: true,
  });
  let memoryText = "";
  for (let i = 0; i < 40; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `document.body ? document.body.innerText : ""`,
      returnByValue: true,
    });
    memoryText = ev.result?.result?.value || "";
    if (memoryText.includes("smoke-pref") && memoryText.includes("smoke likes plain files")) break;
    await delay(250);
  }
  if (!memoryText.includes("smoke-pref") || !memoryText.includes("smoke likes plain files")) {
    throw new Error(`memory panel did not show the seeded entry text=${memoryText.slice(0, 1500)}`);
  }
  if (memoryText.includes(secret)) {
    throw new Error("memory panel rendered the raw secret");
  }
  // A memory row's key cell must stay readable at panel width: the full
  // RFC3339 timestamp once sat as a flex sibling and crushed the link
  // block to ~35px (PR #1305 review).
  const memKeyWidth = await send("Runtime.evaluate", {
    expression: `(() => {
      const row = [...document.querySelectorAll(".storage-row")].find((r) => r.textContent.includes("smoke-pref"));
      const link = row ? row.querySelector(".link") : null;
      return link ? link.getBoundingClientRect().width : 0;
    })()`,
    returnByValue: true,
  });
  const memKeyWidthPx = memKeyWidth.result?.result?.value || 0;
  if (!(memKeyWidthPx > 100)) {
    throw new Error(`memory row key cell too narrow width=${memKeyWidthPx}`);
  }
  const goalCheck = async (expression, ready) => {
    let body = "";
    for (let i = 0; i < 40; i++) {
      const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
      body = ev.result?.result?.value || "";
      if (ready(body)) return body;
      await delay(250);
    }
    throw new Error(`goals check timed out: ${body.slice(0, 400)}`);
  };
  await goalCheck(
    `document.body ? document.body.innerText : ""`,
    (body) => body.includes("Run a command") && body.includes("Goals"),
  );
  await send("Runtime.evaluate", {
    expression: `document.querySelector('[data-view="goals"]').click()`,
  });
  await goalCheck(
    `document.body ? document.body.innerText : ""`,
    (body) => body.includes("Verification command") && body.includes("Autopilot"),
  );
  await send("Runtime.evaluate", {
    expression: `(() => {
      const set = (id, value) => {
        const el = document.getElementById(id);
        el.value = value;
        el.dispatchEvent(new Event("input", { bubbles: true }));
      };
      set("goals-desc", "smoke pass");
      set("goals-verify", "true");
      set("goals-turns", "8");
      set("goals-fails", "3");
      document.getElementById("goals-run").click();
      return "sent";
    })()`,
  });
  await goalCheck(
    `document.body ? document.body.innerText : ""`,
    (body) => body.includes("smoke pass") && body.includes("Done"),
  );
  await send("Runtime.evaluate", {
    expression: `document.querySelector(".goals-list .secondary").click()`,
  });
  await goalCheck(
    `document.getElementById("goals-desc") ? "form" : ""`,
    (body) => body === "form",
  );
  await send("Runtime.evaluate", {
    expression: `(() => {
      const set = (id, value) => {
        const el = document.getElementById(id);
        el.value = value;
        el.dispatchEvent(new Event("input", { bubbles: true }));
      };
      set("goals-desc", "smoke fail");
      set("goals-verify", "false");
      set("goals-turns", "20");
      set("goals-fails", "2");
      document.getElementById("goals-run").click();
      return "sent";
    })()`,
  });
  await goalCheck(
    `document.body ? document.body.innerText : ""`,
    (body) => body.includes("Needs you") && body.includes("verification failed"),
  );

  await send("Runtime.evaluate", {
    expression: `localStorage.setItem("strike.token", ${JSON.stringify(token)})`,
    returnByValue: true,
  });
  await send("Page.navigate", {
    url: `http://127.0.0.1:${port}/?token=${encodeURIComponent(token)}`,
  });

  let booted = {};
  for (let i = 0; i < 40; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `(() => JSON.stringify({
        rows: document.querySelectorAll(".session-row").length,
        composer: !!document.querySelector(".side-chat-open-composer"),
        activity: !!document.querySelector(".side-chat-open-activity"),
        text: (document.body && document.body.innerText || "").slice(0, 240),
      }))()`,
      returnByValue: true,
    });
    booted = JSON.parse(ev.result?.result?.value || "{}");
    if (booted.rows > 0 && booted.composer && booted.activity) break;
    await delay(250);
  }
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

  await send("Runtime.evaluate", {
    expression: `document.querySelector(".side-chat-open-composer").click()`,
    returnByValue: true,
  });

  let panel = {};
  for (let i = 0; i < 20; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `(() => {
        const el = document.querySelector(".side-chat");
        if (!el) return "{}";
        return JSON.stringify({
          text: el.innerText,
          buttons: [...el.querySelectorAll("button")].map((b) => b.textContent.trim()),
        });
      })()`,
      returnByValue: true,
    });
    panel = JSON.parse(ev.result?.result?.value || "{}");
    if ((panel.text || "").includes("Read-only") && (panel.buttons || []).includes("Ask")) break;
    await delay(200);
  }
  if (!(panel.text || "").includes("Read-only") || !(panel.buttons || []).includes("Ask")) {
    throw new Error(`side chat panel missing ${JSON.stringify(panel)}`);
  }
  if ((panel.buttons || []).some((b) => b.startsWith("Approval"))) {
    throw new Error(`side chat rendered an approval control ${JSON.stringify(panel.buttons)}`);
  }

  const ask = async (text) => {
    const ev = await send("Runtime.evaluate", {
      expression: `(() => {
        const input = document.getElementById("side-chat-input");
        if (!input) return "missing input";
        const proto = Object.getOwnPropertyDescriptor(window.HTMLTextAreaElement.prototype, "value");
        proto.set.call(input, ${JSON.stringify(text)});
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
        return "sent";
      })()`,
      returnByValue: true,
    });
    if (ev.result?.result?.value !== "sent") {
      throw new Error(`side chat keyboard send failed ${JSON.stringify(ev.result)}`);
    }
  };

  const panelText = async () => {
    const ev = await send("Runtime.evaluate", {
      expression: `document.querySelector(".side-chat") ? document.querySelector(".side-chat").innerText : ""`,
      returnByValue: true,
    });
    return ev.result?.result?.value || "";
  };

  await ask("what is the agent doing?");
  let answered = "";
  for (let i = 0; i < 40; i++) {
    answered = await panelText();
    if (answered.includes("what is the agent doing?")) break;
    await delay(250);
  }
  if (!answered.includes("what is the agent doing?")) {
    throw new Error(`side chat answer missing ${answered.slice(0, 800)}`);
  }

  await ask("run true");
  let refused = "";
  for (let i = 0; i < 30; i++) {
    refused = await panelText();
    if (refused.toLowerCase().includes("refused a tool call")) break;
    await delay(200);
  }
  if (!refused.toLowerCase().includes("refused a tool call")) {
    throw new Error(`side chat did not refuse a tool call ${refused.slice(0, 500)}`);
  }

  await send("Runtime.evaluate", {
    expression: `window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`,
    returnByValue: true,
  });
  let closed = false;
  for (let i = 0; i < 15; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `!!document.querySelector(".side-chat")`,
      returnByValue: true,
    });
    closed = ev.result?.result?.value === false;
    if (closed) break;
    await delay(100);
  }
  if (!closed) throw new Error("Escape did not close side chat");

  const still = await send("Runtime.evaluate", {
    expression: `document.body.innerText.includes("Approval")`,
    returnByValue: true,
  });
  if (still.result?.result?.value !== true) {
    throw new Error("main composer lost its approval chip");
  }
  console.log("webui-smoke ok");
} catch (err) {
  console.error(String(err));
  if (strikeLog) console.error(strikeLog.slice(-2000));
  process.exitCode = 1;
} finally {
  if (chrome) chrome.kill("SIGTERM");
  strike.kill("SIGTERM");
  await delay(200);
  await rm(home, { recursive: true, force: true });
  await rm(chromeData, { recursive: true, force: true });
}
