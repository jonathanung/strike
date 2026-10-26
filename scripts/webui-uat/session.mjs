// Browser driver shared by the cockpit pass and per-stream scenarios.
// Streams call phone(), shoot(), click(), and waitUntil() on the object
// passed to scenario(ctx). They do not open Chrome or the daemon.

import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { delay } from "./browser.mjs";
import { GOLDEN_MEAN_ABS_MAX, PHONE, phoneShotName } from "./lib.mjs";
import { compareGolden, writeBytes } from "./media.mjs";

const KEYS = {
  k: { key: "k", code: "KeyK", vk: 75 },
  Enter: { key: "Enter", code: "Enter", vk: 13 },
  Escape: { key: "Escape", code: "Escape", vk: 27 },
  ArrowLeft: { key: "ArrowLeft", code: "ArrowLeft", vk: 37 },
};

const DESK = { width: 1440, height: 900 };

export async function createSession({ page, outDir, video, goldens, updateGoldens }) {
  const failures = [];
  const shots = [];
  const log = (msg) => console.log(`uat: ${msg}`);
  const shotDir = join(outDir, "screenshots");
  const domDir = join(outDir, "dom");
  const frameDir = join(outDir, "frames");
  await mkdir(shotDir, { recursive: true });
  await mkdir(domDir, { recursive: true });
  if (video) await mkdir(frameDir, { recursive: true });
  let frame = 0;
  let lastFrame = 0;
  const check = (ok, message) => {
    if (!ok) failures.push(message);
  };

  const viewport = async (width, height) => {
    await page.send("Emulation.setDeviceMetricsOverride", {
      width,
      height,
      deviceScaleFactor: 1,
      mobile: width < 500,
      screenWidth: width,
      screenHeight: height,
    });
    page.size = { width, height };
  };

  const grabFrame = async (force = false, copies = 1) => {
    if (!video || page.size?.width !== DESK.width) return;
    const now = Date.now();
    if (!force && now - lastFrame < 400) return;
    lastFrame = now;
    const shot = await page.send("Page.captureScreenshot", { format: "jpeg", quality: 55 });
    const bytes = Buffer.from(shot.data, "base64");
    for (let i = 0; i < copies; i++) {
      frame += 1;
      await writeBytes(join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`), bytes);
    }
  };

  const bodyText = () => page.evaluate("document.body ? document.body.innerText : ''");
  const tokens = () =>
    page.evaluate(`(() => {
      const cs = getComputedStyle(document.documentElement);
      return { acid: cs.getPropertyValue("--acid").trim(), ground: cs.getPropertyValue("--ground").trim() };
    })()`);

  const waitUntil = async (expression, timeoutMs, label) => {
    const deadline = Date.now() + timeoutMs;
    let last = "";
    while (Date.now() < deadline) {
      last = await page.evaluate(expression);
      if (last) return last;
      await grabFrame();
      await delay(200);
    }
    const text = await bodyText();
    failures.push(`timed out waiting for ${label}; page starts: ${String(text).slice(0, 360)}`);
    return null;
  };

  const click = async (selector, text) => {
    const ok = await page.evaluate(`(() => {
      const want = ${JSON.stringify(text)};
      const nodes = [...document.querySelectorAll(${JSON.stringify(selector)})];
      const el = nodes.find((n) => (n.innerText || "").replace(/\\s+/g, " ").includes(want));
      if (!el) return false;
      el.click();
      return true;
    })()`);
    check(ok, `no clickable ${selector} containing ${JSON.stringify(text)}`);
    await grabFrame(true);
    return ok;
  };

  const press = async (name, modifiers = 0) => {
    const def = KEYS[name];
    if (!def) throw new Error(`unknown key ${name}`);
    const common = {
      key: def.key,
      code: def.code,
      windowsVirtualKeyCode: def.vk,
      nativeVirtualKeyCode: def.vk,
      modifiers,
    };
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...common });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", ...common });
    await delay(80);
    await grabFrame();
  };

  const saveShot = async (name) => {
    const png = await page.send("Page.captureScreenshot", { format: "png" });
    const pngPath = join(shotDir, `${name}.png`);
    await writeBytes(pngPath, Buffer.from(png.data, "base64"));
    const text = await bodyText();
    const tok = await tokens();
    const dom = {
      name,
      viewport: page.size,
      tokens: tok,
      text: String(text).slice(0, 4000),
    };
    await writeFile(join(domDir, `${name}.json`), JSON.stringify(dom, null, 2));
    shots.push(name);
    if (goldens) {
      const repoGolden = new URL(`./goldens/${name}.rgb`, import.meta.url);
      const result = await compareGolden(pngPath, repoGolden, updateGoldens);
      if (result.missing) failures.push(`golden missing for ${name} (run --update-goldens)`);
      else if (!result.updated && result.diff > GOLDEN_MEAN_ABS_MAX) {
        failures.push(`golden ${name} mean abs diff ${result.diff.toFixed(2)} > ${GOLDEN_MEAN_ABS_MAX}`);
      }
    }
    await grabFrame(true, 4);
    return pngPath;
  };

  const sizes = {
    desk1024: { width: 1024, height: 594, tag: "1024" },
    desk1440: { width: DESK.width, height: DESK.height, tag: "1440" },
    mobile: { width: PHONE.width, height: PHONE.height, tag: PHONE.tag },
  };

  const shoot = async (name, which) => {
    try {
      for (const key of which) {
        const size = sizes[key];
        if (!size) throw new Error(`unknown viewport ${key}`);
        await viewport(size.width, size.height);
        await delay(120);
        await saveShot(`${name}-${size.tag}`);
      }
    } finally {
      await viewport(DESK.width, DESK.height);
    }
  };

  // One phone-width still. Restores the 1440×900 desk so later frames stay wide.
  const phone = async (name) => {
    const shot = phoneShotName(name);
    await viewport(PHONE.width, PHONE.height);
    try {
      await delay(120);
      return await saveShot(shot);
    } finally {
      await viewport(DESK.width, DESK.height);
    }
  };

  return {
    page,
    failures,
    shots,
    frameDir,
    check,
    waitUntil,
    click,
    press,
    shoot,
    phone,
    viewport,
    bodyText,
    tokens,
    log,
    frames: () => frame,
  };
}
