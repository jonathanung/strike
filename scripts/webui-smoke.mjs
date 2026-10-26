// Headless check that the embedded Rust cockpit boots and that --auth rejects
// a missing or wrong bearer. Chrome is required (google-chrome or chromium).
import { spawn } from "node:child_process";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
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

  await writeFile(join(home, ".strike", "config.json"), '{"sandboxAllowDegrade":true}\n').catch(() => {});

  chrome = spawn(
    chromeBin(),
    [
      "--headless=new",
      "--disable-gpu",
      "--no-sandbox",
      `--remote-debugging-port=${chromePort}`,
      `--user-data-dir=${chromeData}`,
      "about:blank",
    ],
    { stdio: "ignore" },
  );
  let target;
  for (let i = 0; i < 40; i++) {
    try {
      const list = await fetch(`http://127.0.0.1:${chromePort}/json/list`);
      const tabs = await list.json();
      target = tabs.find((t) => t.webSocketDebuggerUrl);
      if (target) break;
    } catch {
      // chrome still starting
    }
    await delay(100);
  }
  if (!target) throw new Error("chrome devtools did not open");

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
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });

  let text = "";
  let acid = "";
  let ground = "";
  for (let i = 0; i < 40; i++) {
    const ev = await send("Runtime.evaluate", {
      expression: `(() => {
        const body = document.body ? document.body.innerText : "";
        const cs = getComputedStyle(document.documentElement);
        return JSON.stringify({
          body,
          acid: cs.getPropertyValue("--acid").trim(),
          ground: cs.getPropertyValue("--ground").trim(),
        });
      })()`,
      returnByValue: true,
    });
    const raw = ev.result?.result?.value || "{}";
    const parsed = JSON.parse(raw);
    text = parsed.body || "";
    acid = parsed.acid || "";
    ground = parsed.ground || "";
    if (text.includes("Run a command") && text.includes("Activity") && acid === "#7c3aed") break;
    await delay(250);
  }
  const want = ["Run a command", "Strike", "Activity", "Files", "Changes", "Approval"];
  const missingText = want.filter((s) => !text.includes(s));
  if (missingText.length || acid !== "#7c3aed" || ground !== "#14131c") {
    throw new Error(
      `cockpit check failed missing=${missingText.join(",")} acid=${acid} ground=${ground} text=${text.slice(0, 400)}`,
    );
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
