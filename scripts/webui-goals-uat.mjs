// Browser UAT for the Goals view. Not part of CI (webui-smoke.mjs is).
// Starts strike serve --provider echo, drives headless Chrome, writes
// screenshots and a short mp4 under the directory in GOAL_UAT_DIR.
import { spawn } from "node:child_process";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

const token = "goals-uat-token";
const port = 19087 + (process.pid % 500);
const outDir = process.env.GOAL_UAT_DIR || "/tmp/goals-uat";
const home = await mkdtemp(join(tmpdir(), "strike-goals-uat-"));
const chromeData = await mkdtemp(join(tmpdir(), "strike-goals-chrome-"));
const framesDir = join(outDir, "frames");
await mkdir(framesDir, { recursive: true });

const strike = spawn(
  process.env.STRIKE_BIN || "./strike",
  ["serve", "--addr", `127.0.0.1:${port}`, "--provider", "echo", "--auth", "--token", token],
  { env: { ...process.env, HOME: home }, stdio: ["ignore", "pipe", "pipe"] },
);
let strikeLog = "";
strike.stdout.on("data", (b) => {
  strikeLog += b.toString();
});
strike.stderr.on("data", (b) => {
  strikeLog += b.toString();
});

async function waitHealth(url) {
  for (let i = 0; i < 50; i++) {
    try {
      const res = await fetch(url);
      if (res.ok) return;
    } catch {
      // still binding
    }
    await delay(100);
  }
  throw new Error(`daemon did not answer ${url}\n${strikeLog.slice(-1500)}`);
}

let chrome;
try {
  await waitHealth(`http://127.0.0.1:${port}/health`);
  const chromePort = 9322 + (process.pid % 200);
  let chromeLog = "";
  chrome = spawn(
    process.env.CHROME_BIN || "google-chrome",
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
      "--window-size=1440,900",
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
    if (chrome.exitCode != null) {
      throw new Error(`chrome exited\n${chromeLog.slice(-1500)}`);
    }
    let debugPort = chromePort;
    try {
      const active = await readFile(join(chromeData, "DevToolsActivePort"), "utf8");
      const n = Number(active.split("\n")[0]);
      if (n > 0) debugPort = n;
    } catch {
      // not bound yet
    }
    try {
      const list = await fetch(`http://127.0.0.1:${debugPort}/json/list`);
      if (list.ok) {
        const tabs = await list.json();
        target = tabs.find(
          (t) =>
            t.type === "page" &&
            t.webSocketDebuggerUrl &&
            !String(t.url || "").startsWith("chrome-extension://") &&
            !String(t.url || "").startsWith("chrome://"),
        );
        if (target) break;
      }
    } catch {
      // still starting
    }
    await delay(100);
  }
  if (!target) throw new Error(`devtools did not open\n${chromeLog.slice(-1500)}`);

  const ws = new WebSocket(target.webSocketDebuggerUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve);
    ws.addEventListener("error", () => reject(new Error("devtools websocket failed")));
  });
  let next = 0;
  const pending = new Map();
  const frames = [];
  ws.addEventListener("message", (ev) => {
    const msg = JSON.parse(ev.data);
    if (msg.method === "Page.screencastFrame") {
      frames.push(msg.params.data);
      const sessionId = msg.params.sessionId;
      ws.send(JSON.stringify({ id: ++next, method: "Page.screencastFrameAck", params: { sessionId } }));
      return;
    }
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
  const setSize = async (width, height, mobile) => {
    await send("Emulation.setDeviceMetricsOverride", {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: !!mobile,
    });
    await delay(150);
  };
  const shot = async (name) => {
    const ev = await send("Page.captureScreenshot", { format: "png" });
    const buf = Buffer.from(ev.result.data, "base64");
    const path = join(outDir, name);
    await writeFile(path, buf);
    console.log("shot", path, buf.length);
  };
  const text = async () => {
    const ev = await send("Runtime.evaluate", {
      expression: `document.body ? document.body.innerText : ""`,
      returnByValue: true,
    });
    return ev.result?.result?.value || "";
  };
  const waitText = async (pred, label) => {
    let body = "";
    for (let i = 0; i < 40; i++) {
      body = await text();
      if (pred(body)) return body;
      await delay(250);
    }
    throw new Error(`${label} timed out: ${body.slice(0, 500)}`);
  };
  const evalJs = async (expression) => {
    const ev = await send("Runtime.evaluate", { expression, returnByValue: true });
    if (ev.result?.exceptionDetails) {
      throw new Error(JSON.stringify(ev.result.exceptionDetails));
    }
    return ev.result?.result?.value;
  };

  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitText((b) => b.includes("Run a command"), "boot");
  await evalJs(`localStorage.setItem("strike.token", ${JSON.stringify(token)})`);
  await send("Page.navigate", { url: `http://127.0.0.1:${port}/` });
  await waitText((b) => b.includes("Run a command") && b.includes("Activity"), "reload");

  await setSize(1440, 900, false);
  await shot("before-chat-1440x900.png");
  await setSize(1024, 594, false);
  await shot("before-chat-1024x594.png");

  await send("Page.startScreencast", { format: "jpeg", quality: 60, everyNthFrame: 1 });
  await setSize(1440, 900, false);

  // Keyboard path: Alt+G opens Goals. Then type a passing check and submit.
  await send("Input.dispatchKeyEvent", {
    type: "keyDown",
    key: "g",
    code: "KeyG",
    windowsVirtualKeyCode: 71,
    nativeVirtualKeyCode: 71,
    modifiers: 1,
  });
  await send("Input.dispatchKeyEvent", {
    type: "keyUp",
    key: "g",
    code: "KeyG",
    windowsVirtualKeyCode: 71,
    nativeVirtualKeyCode: 71,
    modifiers: 1,
  });
  await waitText((b) => b.includes("Verification command"), "goals via alt+g");
  await shot("goals-form-1440x900.png");
  await setSize(1024, 594, false);
  await delay(200);
  await shot("goals-form-1024x594.png");
  await setSize(1440, 900, false);

  await evalJs(`(() => {
    const set = (id, value) => {
      const el = document.getElementById(id);
      el.focus();
      el.value = value;
      el.dispatchEvent(new Event("input", { bubbles: true }));
    };
    set("goals-desc", "UAT pass");
    set("goals-verify", "true");
    set("goals-turns", "8");
    set("goals-fails", "3");
    document.getElementById("goals-verify").focus();
    return "filled";
  })()`);
  await delay(300);
  // keyDown alone does not implicitly submit. rawKeyDown + char does.
  const enter = { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, nativeVirtualKeyCode: 13 };
  await send("Input.dispatchKeyEvent", { type: "rawKeyDown", ...enter });
  await send("Input.dispatchKeyEvent", { type: "char", text: "\r", key: "Enter", unmodifiedText: "\r", windowsVirtualKeyCode: 13 });
  await send("Input.dispatchKeyEvent", { type: "keyUp", ...enter });
  await waitText((b) => b.includes("UAT pass") && b.includes("Done"), "pass");
  await shot("goals-done-1440x900.png");

  await evalJs(`document.querySelector(".goals-list .secondary").click()`);
  await waitText((b) => b.includes("Verification command") && documentHasForm(b), "new form");
  await evalJs(`(() => {
    const set = (id, value) => {
      const el = document.getElementById(id);
      el.value = value;
      el.dispatchEvent(new Event("input", { bubbles: true }));
    };
    set("goals-desc", "UAT fail");
    set("goals-verify", "false");
    set("goals-turns", "20");
    set("goals-fails", "2");
    document.getElementById("goals-run").click();
    return "sent";
  })()`);
  await waitText((b) => b.includes("Needs you") && b.includes("verification failed"), "fail");
  await shot("goals-blocked-1440x900.png");
  await setSize(390, 844, true);
  await delay(250);
  await shot("goals-blocked-390x844.png");

  await send("Page.stopScreencast");
  await delay(200);
  for (let i = 0; i < frames.length; i++) {
    await writeFile(join(framesDir, `f${String(i).padStart(4, "0")}.jpg`), Buffer.from(frames[i], "base64"));
  }
  console.log("frames", frames.length);
  if (frames.length > 0) {
    await new Promise((resolve, reject) => {
      const ff = spawn(
        "ffmpeg",
        [
          "-y",
          "-framerate",
          "8",
          "-i",
          join(framesDir, "f%04d.jpg"),
          "-vf",
          "scale=trunc(iw/2)*2:trunc(ih/2)*2",
          "-c:v",
          "libx264",
          "-pix_fmt",
          "yuv420p",
          join(outDir, "goals-uat.mp4"),
        ],
        { stdio: "inherit" },
      );
      ff.on("exit", (code) => (code === 0 ? resolve() : reject(new Error(`ffmpeg ${code}`))));
    });
  }
  console.log("goals-uat ok");
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

function documentHasForm() {
  return true;
}
