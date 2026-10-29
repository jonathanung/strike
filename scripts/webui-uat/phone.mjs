// Phone-width cockpit pass. Chrome device metrics emulate a phone (touch,
// devicePixelRatio 3). There is no on-screen keyboard and no notch in this
// VM: the keyboard path drives __strikeSetViewport, which is the same box
// the visualViewport listener writes. Safe-area insets are asserted as CSS
// rules; computed padding stays 0 until a real phone.

import { mkdir } from "node:fs/promises";
import { join } from "node:path";

import { delay } from "./browser.mjs";
import { encodeVideo, writeBytes } from "./media.mjs";

const PHONE = { width: 390, height: 844, tag: "390" };
const NARROW = { width: 360, height: 740, tag: "360" };
const LANDSCAPE = { width: 844, height: 390, tag: "landscape" };

export function phoneRecorder(page, outDir) {
  const frameDir = join(outDir, "frames-phone");
  let frame = 0;
  let recording = false;
  return {
    frameDir,
    async start() {
      recording = true;
      await mkdir(frameDir, { recursive: true });
    },
    async grab(copies = 1) {
      if (!recording) return;
      const shot = await page.send("Page.captureScreenshot", { format: "jpeg", quality: 60 });
      const bytes = Buffer.from(shot.data, "base64");
      for (let i = 0; i < copies; i++) {
        frame += 1;
        await writeBytes(join(frameDir, `frame-${String(frame).padStart(5, "0")}.jpg`), bytes);
      }
    },
    async finish(enabled) {
      recording = false;
      if (!enabled || frame === 0) return "";
      const videoPath = join(outDir, "uat-phone.mp4");
      await encodeVideo(frameDir, videoPath, 4);
      return videoPath;
    },
  };
}

async function setSize(page, size, dpr = 3) {
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: size.width,
    height: size.height,
    deviceScaleFactor: dpr,
    mobile: size.width < 900,
    screenWidth: size.width,
    screenHeight: size.height,
  });
  page.size = { width: size.width, height: size.height };
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: true, maxTouchPoints: 1 }).catch(() => {});
  await page.evaluate(`window.dispatchEvent(new Event("resize"))`);
}

async function restoreDesk(page) {
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: false }).catch(() => {});
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: 1440,
    height: 900,
    deviceScaleFactor: 1,
    mobile: false,
    screenWidth: 1440,
    screenHeight: 900,
  });
  page.size = { width: 1440, height: 900 };
  await page.evaluate(`window.dispatchEvent(new Event("resize"))`);
}

async function shot(page, recorder, shotDir, shots, name) {
  await delay(180);
  const png = await page.send("Page.captureScreenshot", { format: "png" });
  await writeBytes(join(shotDir, `${name}.png`), Buffer.from(png.data, "base64"));
  shots.push(name);
  await recorder.grab(3);
}

async function clickNav(page, label) {
  const ok = await page.evaluate(`(() => {
    const want = ${JSON.stringify(label)};
    const el = [...document.querySelectorAll(".phone-nav button")].find((n) => (n.innerText || "").trim() === want);
    if (!el) return false;
    el.click();
    return true;
  })()`);
  await delay(150);
  return ok;
}

function minControl(page, selector) {
  return page.evaluate(`(() => {
    const el = document.querySelector(${JSON.stringify(selector)});
    if (!el) return 0;
    return el.getBoundingClientRect().height;
  })()`);
}

function chipBox(page, label) {
  return page.evaluate(`(() => {
    const want = ${JSON.stringify(label)};
    const el = [...document.querySelectorAll(".composer-bar button")].find((n) => (n.innerText || "").trim() === want || (want === "Send" && n.classList.contains("send")));
    if (!el) return null;
    const r = el.getBoundingClientRect();
    return { right: r.right, height: r.height, inner: window.innerWidth };
  })()`);
}

function onScreen(box, label, check) {
  check(Boolean(box), `${label} is missing`);
  if (!box) return;
  check(box.height >= 44, `${label} height ${box.height} < 44`);
  check(box.right <= box.inner + 1, `${label} right ${box.right} past viewport ${box.inner}`);
}

export async function runPhoneScreens({ page, outDir, video, check, shots }) {
  const recorder = phoneRecorder(page, outDir);
  if (video) await recorder.start();
  const shotDir = join(outDir, "screenshots");
  await mkdir(shotDir, { recursive: true });
  const log = (msg) => console.log(`uat: ${msg}`);

  await setSize(page, PHONE);
  await delay(200);
  const phone = await page.evaluate(`document.body && document.body.dataset.phone === "1"`);
  check(phone, "390×844 did not set data-phone=1");
  const nav = await page.evaluate(`Boolean(document.querySelector(".phone-nav")) && getComputedStyle(document.querySelector(".phone-nav")).display !== "none"`);
  check(nav, "phone nav is not visible at 390×844");
  const safe = await page.evaluate(`(() => {
    for (const sheet of document.styleSheets) {
      let rules;
      try { rules = sheet.cssRules; } catch { continue; }
      for (const rule of rules) {
        if (rule.cssText && rule.cssText.includes("safe-area-inset-bottom") && rule.cssText.includes("safe-area-inset-top")) return true;
      }
    }
    return false;
  })()`);
  check(safe, "mobile CSS is missing safe-area inset rules");
  const manifest = await page.evaluate(`document.querySelector('link[rel="manifest"]')?.href || ""`);
  check(manifest.includes("manifest.webmanifest"), "manifest link missing");
  const theme = await page.evaluate(`document.querySelector('meta[name="theme-color"]')?.content || ""`);
  check(theme === "#14131c", `theme-color ${theme} is not the dark ground token #14131c`);
  for (const label of ["Send", "Steer", "Queue"]) {
    onScreen(await chipBox(page, label), `${label} at 390`, check);
  }
  const composer = await page.evaluate(`(() => {
    const el = document.querySelector(".composer");
    if (!el) return false;
    const r = el.getBoundingClientRect();
    return r.bottom <= window.innerHeight + 2 && r.top < window.innerHeight;
  })()`);
  check(composer, "composer is outside the 390×844 viewport");

  // Phone header (#1490 slice 2): one compact row (<=56px), a >=44px
  // overflow button, and no floating action buttons left in the bar.
  // The phone header button opens the command palette as a full-height sheet.
  const header = await page.evaluate(`(() => {
    const el = document.querySelector(".page-header");
    if (!el) return null;
    const more = document.querySelector("[data-nav='phone-more']");
    const mr = more ? more.getBoundingClientRect() : null;
    return {
      height: el.getBoundingClientRect().height,
      moreH: mr ? mr.height : 0,
      moreW: mr ? mr.width : 0,
      floating: Boolean(document.querySelector(".page-header .command-pill, .page-header .gw-btn, .page-header .gq-open")),
    };
  })()`);
  check(header && header.height > 0 && header.height <= 56, `phone header height ${header && header.height} not within 56px`);
  check(header && header.moreH >= 44 && header.moreW >= 44, `phone overflow button ${header && header.moreW}x${header && header.moreH} under 44px`);
  check(header && !header.floating, "phone header still renders floating action buttons inline");
  await page.evaluate(`document.querySelector("[data-nav='phone-more']")?.click()`);
  await delay(200);
  const sheet = await page.evaluate(`(() => {
    const el = document.querySelector("[data-nav='palette-dialog']");
    if (!el) return null;
    const row = el.querySelector(".palette-row");
    const box = el.getBoundingClientRect();
    const rowBox = row ? row.getBoundingClientRect() : null;
    return {
      text: el.innerText || "",
      height: box.height,
      row: rowBox ? rowBox.height : 0,
    };
  })()`);
  check(sheet && sheet.text.includes("Open session"), "phone palette is missing Open session");
  check(sheet && sheet.height >= 600, `phone palette is not full height (${sheet && sheet.height})`);
  check(sheet && sheet.row >= 44, `phone palette row is under 44px (${sheet && sheet.row})`);
  await shot(page, recorder, shotDir, shots, "phone-header-menu-390");
  await page.evaluate(`document.querySelector(".palette-backdrop")?.click()`);
  await delay(120);
  const sheetClosed = await page.evaluate(
    `!document.querySelector("[data-nav='palette-dialog']")`,
  );
  check(sheetClosed, "phone palette did not close on backdrop tap");
  await shot(page, recorder, shotDir, shots, "phone-chat-390");

  // Sheet slot lifecycle (#1527): the sheet must hold the ONLY gateway
  // switcher and notification bell at phone widths, open or closed —
  // a second mount leaks the switcher's 5s probe timer (each tick
  // traps on disposed signals) and an Alt+W listener per cycle, and
  // strands the palette's notification handle on a disposed bell.
  await page.evaluate(`(() => {
    if (!window.__trapLog) {
      window.__trapLog = [];
      window.addEventListener("error", (e) => {
        window.__trapLog.push(String((e && e.message) || "error"));
      });
    }
    return true;
  })()`);
  const slotCounts = () =>
    page.evaluate(`(() => ({
      bells: document.querySelectorAll(".notify-bell").length,
      gwBtns: document.querySelectorAll(".gw-btn").length,
    }))()`);
  const closedCounts = await slotCounts();
  check(
    closedCounts.bells === 1 && closedCounts.gwBtns === 1,
    `sheet closed: expected 1 bell and 1 Gateways button, got ${closedCounts.bells}/${closedCounts.gwBtns}`,
  );
  for (let i = 0; i < 5; i++) {
    await page.evaluate(`document.querySelector("[data-nav='phone-more']")?.click()`);
    await delay(120);
    const openCounts = await slotCounts();
    check(
      openCounts.bells === 1 && openCounts.gwBtns === 1,
      `sheet open ${i + 1}: expected 1 bell and 1 Gateways button, got ${openCounts.bells}/${openCounts.gwBtns}`,
    );
    await page.evaluate(`document.querySelector(".palette-backdrop")?.click()`);
    await delay(120);
  }
  // Alt+W parity: each press must toggle the switcher exactly once.
  // Leaked listeners toggle the shared open signal again, so after an
  // odd number of cycles a press would land back where it started.
  const expanded0 = await page.evaluate(
    `document.querySelector(".gw-btn")?.getAttribute("aria-expanded") || "missing"`,
  );
  check(expanded0 !== "missing", "Gateways button missing after sheet cycles");
  const altW = () =>
    page.evaluate(
      `document.body.dispatchEvent(new KeyboardEvent("keydown", { key: "w", altKey: true, bubbles: true, cancelable: true }))`,
    );
  await altW();
  await delay(150);
  const expanded1 = await page.evaluate(
    `document.querySelector(".gw-btn")?.getAttribute("aria-expanded") || "missing"`,
  );
  check(
    expanded1 !== "missing" && expanded1 !== expanded0,
    `Alt+W did not toggle the gateway switcher exactly once (${expanded0} -> ${expanded1})`,
  );
  await shot(page, recorder, shotDir, shots, "phone-sheet-gateways-390");
  await altW();
  await delay(150);
  const expanded2 = await page.evaluate(
    `document.querySelector(".gw-btn")?.getAttribute("aria-expanded") || "missing"`,
  );
  check(expanded2 === expanded0, `second Alt+W did not toggle back (${expanded1} -> ${expanded2})`);
  await page.evaluate(`document.querySelector(".palette-backdrop")?.click()`);
  await delay(120);

  // The palette's Notifications command must still reach the live bell
  // after the cycles (its handle is cleared when a bell unmounts, never
  // left pointing at a disposed one).
  await page.evaluate(
    `document.dispatchEvent(new KeyboardEvent("keydown", { key: "k", ctrlKey: true, bubbles: true, cancelable: true }))`,
  );
  await delay(250);
  const paletteOpen = await page.evaluate(`Boolean(document.querySelector(".palette input"))`);
  check(paletteOpen, "command palette did not open on the phone");
  if (paletteOpen) {
    await page.evaluate(`document.querySelector(".palette input")?.focus()`);
    await page.send("Input.insertText", { text: "Notifications" });
    await delay(300);
    const ran = await page.evaluate(`(() => {
      const row = document.querySelector('[data-command-id="notifications.open"]');
      if (!row) return false;
      row.click();
      return true;
    })()`);
    check(ran, "palette has no Notifications command row");
    await delay(300);
    const centre = await page.evaluate(`(() => {
      const panel = document.querySelector(".notify-panel");
      if (!panel) return { open: false, text: "" };
      const r = panel.getBoundingClientRect();
      return {
        open: getComputedStyle(panel).display !== "none" && r.width > 0 && r.height > 0,
        text: (panel.innerText || "").slice(0, 80),
      };
    })()`);
    check(centre.open, `palette Notifications did not open the centre after sheet cycles (${JSON.stringify(centre)})`);
    await shot(page, recorder, shotDir, shots, "phone-notify-centre-390");
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
    await delay(150);
  }

  // A leaked probe timer traps every 5s. Give any timer leaked by the
  // cycles above a full period to fire before counting traps.
  await delay(6000);
  const traps = await page.evaluate(
    `(window.__trapLog || []).filter((m) => m.includes("unreachable") || m.includes("RuntimeError")).length`,
  );
  check(traps === 0, `${traps} wasm trap(s) after sheet open/close cycles (leaked probe timer)`);

  await setSize(page, NARROW);
  await delay(120);
  const narrow = await page.evaluate(`document.body && document.body.dataset.phone === "1"`);
  check(narrow, "360×740 did not set data-phone=1");
  for (const label of ["Send", "Steer", "Queue"]) {
    onScreen(await chipBox(page, label), `${label} at 360`, check);
  }
  const header360 = await page.evaluate(`(() => {
    const el = document.querySelector(".page-header");
    return el ? el.getBoundingClientRect().height : 0;
  })()`);
  check(header360 > 0 && header360 <= 56, `phone header height ${header360} at 360 not within 56px`);
  await shot(page, recorder, shotDir, shots, "phone-chat-360");
  await setSize(page, PHONE);

  check(await clickNav(page, "Sessions"), "no Sessions phone-nav button");
  const sessions = await page.evaluate(`Boolean(document.querySelector(".phone-pane-sessions .rail")) && getComputedStyle(document.querySelector(".rail")).display !== "none"`);
  check(sessions, "sessions pane did not show the rail");
  const pinH = await minControl(page, ".pin");
  check(pinH === 0 || pinH >= 44, `pin height ${pinH} is neither absent nor 44px`);
  const filterH = await minControl(page, ".ss-chips .chip");
  check(filterH >= 44, `session filter chip height ${filterH} < 44`);
  const searchH = await minControl(page, ".ss-search");
  check(searchH >= 44, `session search height ${searchH} < 44`);
  await shot(page, recorder, shotDir, shots, "phone-sessions-390");

  check(await clickNav(page, "Chat"), "no Chat phone-nav button");
  const opened = await page.evaluate(`(() => {
    const btn = [...document.querySelectorAll(".composer-bar button")].find((n) => (n.innerText || "").includes("Approval"));
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  check(opened, "approval chip missing on the phone composer");
  await delay(150);
  const menu = await page.evaluate(`Boolean(document.querySelector(".composer .menu")) && (document.querySelector(".composer .menu").innerText || "").includes("Yolo")`);
  check(menu, "approval menu did not open on the phone");
  await shot(page, recorder, shotDir, shots, "phone-approval-390");
  await page.evaluate(`(() => {
    if (!document.querySelector(".composer .menu")) return;
    const chip = [...document.querySelectorAll(".composer-bar button")].find((n) => (n.innerText || "").includes("Approval"));
    chip?.click();
  })()`);
  await delay(100);

  const model = await page.evaluate(`(() => {
    const el = document.querySelector("[data-model-chip]");
    if (!el) return { ok: false, text: "", h: 0 };
    const rect = el.getBoundingClientRect();
    return { ok: rect.width > 0 && rect.height > 0 && (el.textContent || "").includes("Model"), text: el.textContent || "", h: rect.height };
  })()`);
  check(model && model.ok, `model status missing on the phone composer (${JSON.stringify(model)})`);
  await page.evaluate(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "m", altKey: true, bubbles: true, cancelable: true }))`);
  await delay(250);
  const modelOpen = await page.evaluate(`Boolean(document.querySelector("[data-model-menu]"))`);
  check(modelOpen, "model menu did not open on the phone");
  await shot(page, recorder, shotDir, shots, "phone-model-390");
  await page.evaluate(`document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }))`);

  // Keyboard path: shrink the visual viewport the way iOS does when the
  // keyboard opens. Headless Chrome has no software keyboard.
  await page.evaluate(`document.querySelector("textarea")?.focus()`);
  const hooked = await page.evaluate(`typeof window.__strikeSetViewport === "function"`);
  check(hooked, "__strikeSetViewport is not installed");
  if (hooked) {
    await page.evaluate(`window.__strikeSetViewport(320, 0)`);
    await delay(80);
    const visible = await page.evaluate(`(() => {
      const el = document.querySelector(".composer");
      if (!el) return false;
      const r = el.getBoundingClientRect();
      const top = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--vv-top")) || 0;
      const height = parseFloat(getComputedStyle(document.documentElement).getPropertyValue("--vv-height")) || 0;
      return height > 0 && r.bottom <= top + height + 4 && r.top >= top - 4;
    })()`);
    check(visible, "composer is outside the simulated keyboard viewport");
    await shot(page, recorder, shotDir, shots, "phone-keyboard-390");
    await page.evaluate(`window.__strikeSetViewport(844, 0)`);
  }

  const before = await page.evaluate(`document.documentElement.dataset.resume || "0"`);
  await page.evaluate(`window.dispatchEvent(new Event("strike-resume"))`);
  await delay(200);
  const after = await page.evaluate(`document.documentElement.dataset.resume || "0"`);
  check(Number(after) > Number(before), "strike-resume did not bump data-resume");

  check(await clickNav(page, "Activity"), "no Activity phone-nav button");
  const activity = await page.evaluate(`Boolean(document.querySelector(".activity")) && getComputedStyle(document.querySelector(".side")).display !== "none"`);
  check(activity, "activity pane hid the side panel");
  await shot(page, recorder, shotDir, shots, "phone-activity-390");

  check(await clickNav(page, "Files"), "no Files phone-nav button");
  await delay(300);
  const files = await page.evaluate(`document.body && (document.body.innerText.includes("Working directory") || document.body.innerText.includes("Click or drag") || document.body.innerText.includes("Open reads"))`);
  check(files, "files pane did not show the working directory");
  await shot(page, recorder, shotDir, shots, "phone-files-390");
  const openedFile = await page.evaluate(`(() => {
    const btn = document.querySelector(".ev-open");
    if (!btn) return false;
    btn.click();
    return true;
  })()`);
  if (openedFile) {
    await delay(400);
    check(await clickNav(page, "Chat"), "chat nav missing after opening a file");
    const doc = await page.evaluate(`Boolean(document.querySelector(".ev-doc"))`);
    check(doc, "Open did not show the document pane on the chat column");
    await shot(page, recorder, shotDir, shots, "phone-editor-390");
    await page.evaluate(`document.querySelector(".ev-doc button") && [...document.querySelectorAll("button")].find((b) => (b.innerText||"").trim()==="Close")?.click()`);
    await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
    await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });
    await delay(150);
  } else {
    log("phone: no file Open button (empty directory)");
  }

  check(await clickNav(page, "Files"), "files nav missing before Changes");
  await delay(200);
  await page.evaluate(`(() => { const b = [...document.querySelectorAll(".side-tabs button")].find((n) => (n.innerText||"").includes("Changes")); if (b) { b.scrollIntoView({block:"nearest"}); b.click(); } })()`);
  await delay(300);
  const changesInfo = await page.evaluate(`(() => {
    const shell = document.querySelector(".shell");
    const cls = shell ? shell.className : "";
    const side = document.querySelector(".side");
    const sideDisplay = side ? getComputedStyle(side).display : "missing";
    const pane = document.querySelector(".changes");
    const text = ((pane && pane.innerText) || "").replace(/\\s+/g, " ").trim().slice(0, 160);
    const ok = cls.includes("phone-pane-files") && side && sideDisplay !== "none" && pane && getComputedStyle(pane).display !== "none" && text.length > 0;
    return { ok, detail: "class=" + cls + " side=" + sideDisplay + " text=" + JSON.stringify(text) };
  })()`);
  check(changesInfo.ok, `Changes tab is not visible on the phone files pane (${changesInfo.detail})`);
  await shot(page, recorder, shotDir, shots, "phone-changes-390");

  check(await clickNav(page, "Daemon"), "no Daemon phone-nav button");
  const daemon = await page.evaluate(`document.body && (document.body.innerText.includes("Gateways") || document.body.innerText.includes("Base URL"))`);
  check(daemon, "daemon pane did not show the gateway form");
  const daemonH = await minControl(page, ".daemon input");
  check(daemonH >= 44, `daemon input height ${daemonH} < 44`);
  await shot(page, recorder, shotDir, shots, "phone-daemon-390");
  check(await clickNav(page, "Chat"), "could not return to chat");

  await setSize(page, LANDSCAPE);
  await delay(150);
  const land = await page.evaluate(`document.body && document.body.dataset.phone === "1"`);
  check(land, "844×390 landscape did not set data-phone=1");
  await shot(page, recorder, shotDir, shots, "phone-landscape-844");

  await restoreDesk(page);
  await delay(150);
  const desk = await page.evaluate(`document.body && document.body.dataset.phone !== "1"`);
  check(desk, "restoring 1440×900 left data-phone=1");
  log("phone screens done");
  return recorder;
}

export async function capturePhoneFrame(page, recorder, outDir, shots, name) {
  if (!recorder) return;
  const shotDir = join(outDir, "screenshots");
  const prev = page.size;
  await setSize(page, PHONE);
  await delay(160);
  await shot(page, recorder, shotDir, shots, name);
  await page.send("Emulation.setDeviceMetricsOverride", {
    width: prev?.width || 1440,
    height: prev?.height || 900,
    deviceScaleFactor: 1,
    mobile: false,
    screenWidth: prev?.width || 1440,
    screenHeight: prev?.height || 900,
  });
  page.size = { width: prev?.width || 1440, height: prev?.height || 900 };
  await page.send("Emulation.setTouchEmulationEnabled", { enabled: false }).catch(() => {});
  await page.evaluate(`window.dispatchEvent(new Event("resize"))`);
}
