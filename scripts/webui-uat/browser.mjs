// Headless Chrome (CDP) and the strike serve process.
// Chrome flags match scripts/webui-smoke.mjs: /dev/shm is small, remote
// debugging refuses a missing allow-origins, and Ozone will not start
// headless without an explicit platform.

import { spawn } from "node:child_process";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { createServer } from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

export { delay };

export function chromeBin() {
  return process.env.CHROME_BIN || "google-chrome";
}

export function freePort() {
  return new Promise((resolve, reject) => {
    const server = createServer();
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

export async function waitHealth(url, tries = 80) {
  for (let i = 0; i < tries; i++) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      // still binding, or already down
    }
    await delay(100);
  }
  throw new Error(`daemon did not answer ${url}`);
}

export async function waitDown(url, tries = 50) {
  for (let i = 0; i < tries; i++) {
    try {
      await fetch(url, { signal: AbortSignal.timeout(300) });
    } catch {
      return;
    }
    await delay(100);
  }
  throw new Error(`daemon still answering ${url}`);
}

export function startStrike({ bin, port, home, cwd }) {
  const proc = spawn(
    bin,
    ["serve", "--addr", `127.0.0.1:${port}`, "--provider", "echo"],
    { cwd, env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] },
  );
  let log = "";
  const keep = (buf) => {
    log += buf.toString();
    if (log.length > 16000) log = log.slice(-16000);
  };
  proc.stdout.on("data", keep);
  proc.stderr.on("data", keep);
  proc.log = () => log;
  return proc;
}

export async function stopStrike(proc) {
  if (!proc || proc.exitCode != null || proc.signalCode) return;
  proc.kill("SIGTERM");
  const exited = new Promise((resolve) => proc.once("exit", resolve));
  const done = await Promise.race([exited, delay(4000).then(() => "timeout")]);
  if (done === "timeout" && proc.exitCode == null) proc.kill("SIGKILL");
  await Promise.race([exited, delay(2000)]);
}

export async function launchChrome() {
  const userDataDir = await mkdtemp(join(tmpdir(), "strike-webui-uat-chrome-"));
  const port = await freePort();
  let log = "";
  const proc = spawn(
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
      `--remote-debugging-port=${port}`,
      "--remote-debugging-address=127.0.0.1",
      "--remote-allow-origins=*",
      `--user-data-dir=${userDataDir}`,
      "--hide-scrollbars",
      "about:blank",
    ],
    { stdio: ["ignore", "pipe", "pipe"] },
  );
  const keep = (buf) => {
    log += buf.toString();
    if (log.length > 8000) log = log.slice(-8000);
  };
  proc.stdout.on("data", keep);
  proc.stderr.on("data", keep);
  proc.userDataDir = userDataDir;
  proc.debugPort = port;
  proc.log = () => log;
  return proc;
}

async function devtoolsPort(chrome) {
  try {
    const active = await readFile(join(chrome.userDataDir, "DevToolsActivePort"), "utf8");
    const n = Number(active.split("\n")[0]);
    if (n > 0) return n;
  } catch {
    // file appears once the browser binds the port
  }
  return chrome.debugPort;
}

export async function waitForPage(chrome) {
  const deadline = Date.now() + 20000;
  while (Date.now() < deadline) {
    if (chrome.exitCode != null || chrome.signalCode) {
      throw new Error(
        `chrome exited before devtools (code=${chrome.exitCode} signal=${chrome.signalCode})\n${chrome.log().slice(-1500)}`,
      );
    }
    const port = await devtoolsPort(chrome);
    try {
      const list = await fetch(`http://127.0.0.1:${port}/json/list`);
      if (list.ok) {
        const tabs = await list.json();
        const target = tabs.find(
          (t) =>
            t.type === "page" &&
            t.webSocketDebuggerUrl &&
            !String(t.url || "").startsWith("chrome-extension://") &&
            !String(t.url || "").startsWith("chrome://"),
        );
        if (target) return target;
        await fetch(`http://127.0.0.1:${port}/json/new?about:blank`, { method: "PUT" }).catch(() => {});
      }
    } catch {
      // chrome still starting
    }
    await delay(100);
  }
  throw new Error(`chrome devtools did not open\n${chrome.log().slice(-1500)}`);
}

export function attachCDP(wsUrl) {
  const ws = new WebSocket(wsUrl);
  let next = 0;
  const pending = new Map();
  const opened = new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve);
    ws.addEventListener("error", () => reject(new Error("devtools websocket failed")));
  });
  ws.addEventListener("message", (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.id && pending.has(msg.id)) {
      pending.get(msg.id)(msg);
      pending.delete(msg.id);
    }
  });
  const send = (method, params, timeoutMs = 20000) =>
    new Promise((resolve, reject) => {
      const id = ++next;
      const timer = setTimeout(() => {
        pending.delete(id);
        reject(new Error(`cdp timeout ${method}`));
      }, timeoutMs);
      pending.set(id, (msg) => {
        clearTimeout(timer);
        if (msg.error) reject(new Error(`${method}: ${JSON.stringify(msg.error)}`));
        else resolve(msg.result || {});
      });
      ws.send(JSON.stringify({ id, method, params }));
    });

  async function evaluate(expression) {
    const result = await send("Runtime.evaluate", {
      expression,
      returnByValue: true,
      awaitPromise: true,
    });
    if (result.exceptionDetails) {
      const details = result.exceptionDetails;
      const text = details.exception?.description || details.text || JSON.stringify(details);
      throw new Error(text);
    }
    return result.result?.value;
  }

  return {
    ws,
    opened,
    send,
    evaluate,
    async close() {
      ws.close();
    },
  };
}

export async function removeDir(dir) {
  if (dir) await rm(dir, { recursive: true, force: true });
}
