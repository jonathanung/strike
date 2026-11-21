// Accessibility pass over the cockpit (#1878), run from the smoke cockpit
// pass in scenario.mjs:
//   - every dialog it opens traps Tab and returns focus to its opener
//     (asserted on document.activeElement before and after);
//   - the core loop — open session, approve, send — done with the keyboard
//     only, with the approval announced in the assertive live region;
//   - a dependency-free DOM scan of the main surfaces. Warn mode: findings
//     go to a11y-scan.json / a11y-scan.md in the UAT artifacts and the log,
//     never to failures (see docs/webui-planned.md for the path to blocking).

import { mkdir, writeFile } from "node:fs/promises";
import { join } from "node:path";

import { delay } from "./browser.mjs";

const SHIFT = 8;
const CTRL = 2;
const OPENER = "data-uat-opener";

// Pure: true when `active` is the expected element per the in-page probe.
export function focusReport(probe) {
  return {
    inside: Boolean(probe && probe.inside),
    first: Boolean(probe && probe.first),
    last: Boolean(probe && probe.last),
    count: probe ? probe.count : 0,
  };
}

const FOCUSABLE = `a[href], button:not([disabled]), input:not([disabled]):not([type="hidden"]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"]), [contenteditable="true"]`;

function probeExpr(dialog) {
  return `(() => {
    const d = document.querySelector(${JSON.stringify(dialog)});
    if (!d) return null;
    const items = [...d.querySelectorAll(${JSON.stringify(FOCUSABLE)})].filter((el) => {
      const r = el.getBoundingClientRect();
      return r.width > 0 && r.height > 0;
    });
    const a = document.activeElement;
    return {
      count: items.length,
      inside: Boolean(a && d.contains(a)),
      first: items.length > 0 && a === items[0],
      last: items.length > 0 && a === items[items.length - 1],
    };
  })()`;
}

function focusEdgeExpr(dialog, edge) {
  return `(() => {
    const d = document.querySelector(${JSON.stringify(dialog)});
    if (!d) return false;
    const items = [...d.querySelectorAll(${JSON.stringify(FOCUSABLE)})].filter((el) => {
      const r = el.getBoundingClientRect();
      return r.width > 0 && r.height > 0;
    });
    const el = ${edge === "last" ? "items[items.length - 1]" : "items[0]"};
    if (!el) return false;
    el.focus();
    return document.activeElement === el;
  })()`;
}

const MARK_OPENER = `(() => {
  document.querySelectorAll("[${OPENER}]").forEach((el) => el.removeAttribute("${OPENER}"));
  const el = document.querySelector('[data-nav="palette"]');
  if (!el) return false;
  el.setAttribute("${OPENER}", "1");
  el.focus();
  return document.activeElement === el;
})()`;

const OPENER_FOCUSED = `document.activeElement?.getAttribute("${OPENER}") === "1"`;

// Open a dialog from a known opener, check initial focus, Tab wrap both
// ways and containment, close it, and check focus came back.
async function dialogFocus(ctx, { name, dialog, open, close }) {
  const { page, check, waitUntil, press } = ctx;
  const marked = await page.evaluate(MARK_OPENER);
  check(marked, `${name}: could not focus the opener`);
  if (!marked) return;
  await open();
  const shown = await waitUntil(`Boolean(document.querySelector(${JSON.stringify(dialog)}))`, 4000, `${name} open`);
  check(Boolean(shown), `${name}: dialog did not open`);
  if (!shown) return;
  const settled = await waitUntil(`Boolean(${probeExpr(dialog)}?.inside)`, 2000, `${name} initial focus`);
  check(Boolean(settled), `${name}: focus did not move into the dialog`);
  const start = focusReport(await page.evaluate(probeExpr(dialog)));
  if (start.count >= 2) {
    await page.evaluate(focusEdgeExpr(dialog, "last"));
    await press("Tab");
    check(focusReport(await page.evaluate(probeExpr(dialog))).first, `${name}: Tab from the last control did not wrap to the first`);
    await press("Tab", SHIFT);
    check(focusReport(await page.evaluate(probeExpr(dialog))).last, `${name}: Shift+Tab from the first control did not wrap to the last`);
  }
  for (let i = 0; i < Math.min(start.count + 1, 6); i++) {
    await press("Tab");
    check(focusReport(await page.evaluate(probeExpr(dialog))).inside, `${name}: Tab ${i + 1} left the dialog`);
  }
  await close();
  const gone = await waitUntil(`!document.querySelector(${JSON.stringify(dialog)})`, 4000, `${name} closed`);
  check(Boolean(gone), `${name}: dialog did not close`);
  const back = await waitUntil(OPENER_FOCUSED, 3000, `${name} focus return`);
  const where = await page.evaluate(`(() => { const a = document.activeElement; return a ? a.tagName + "." + a.className : "none"; })()`);
  check(Boolean(back), `${name}: focus did not return to the opener (active: ${where})`);
}

async function setInput(page, selector, text) {
  return page.evaluate(`(() => {
    const input = document.querySelector(${JSON.stringify(selector)});
    if (!input) return false;
    input.focus();
    const proto = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value");
    proto.set.call(input, ${JSON.stringify(text)});
    input.dispatchEvent(new Event("input", { bubbles: true }));
    return input.value === ${JSON.stringify(text)};
  })()`);
}

export async function dialogFocusPass(ctx) {
  const { page, press, log } = ctx;
  log("a11y dialogs");
  await dialogFocus(ctx, {
    name: "command palette",
    dialog: "[data-nav='palette-dialog']",
    open: () => press("k", CTRL),
    close: () => press("Escape"),
  });
  await dialogFocus(ctx, {
    name: "keyboard help",
    dialog: "[data-nav='help']",
    open: () => press("?"),
    close: () => press("?"),
  });
  await dialogFocus(ctx, {
    name: "confirm dialog",
    dialog: "[data-confirm='dialog']",
    open: async () => {
      await press("k", CTRL);
      await delay(200);
      await setInput(page, "[data-nav='palette-dialog'] input", "rotate");
      await delay(200);
      await page.evaluate(`document.querySelector('[data-command-id="gateway.rotate"]')?.focus()`);
      await press("Enter");
    },
    close: () => press("Escape"),
  });
}

// Press Tab (or Shift+Tab) until `match` (an in-page expression over `a`,
// the active element) holds, up to `max` presses.
async function tabTo(ctx, match, { shift = false, max = 80 } = {}) {
  const { page, press } = ctx;
  const expr = `(() => { const a = document.activeElement; return Boolean(a && (${match})); })()`;
  for (let i = 0; i < max; i++) {
    if (await page.evaluate(expr)) return true;
    await press("Tab", shift ? SHIFT : 0);
  }
  return page.evaluate(expr);
}

const KBD_COMMAND = "run echo uat-keyboard-loop";

export async function keyboardLoop(ctx) {
  const { page, check, waitUntil, press, log, shoot } = ctx;
  log("a11y keyboard loop");
  await page.evaluate(`document.activeElement?.blur()`);

  // Approval mode back to Default so `run` asks, through the palette.
  await press("k", CTRL);
  const pal = await waitUntil(`Boolean(document.querySelector("[data-nav='palette-dialog'] input"))`, 4000, "palette for mode");
  check(Boolean(pal), "keyboard loop: palette did not open");
  if (!pal) return;
  await page.send("Input.insertText", { text: "approval: default" });
  await delay(200);
  await press("Enter");
  const mode = await waitUntil(`document.body.innerText.includes("Approval · Default")`, 4000, "approval mode Default");
  check(Boolean(mode), "keyboard loop: palette did not set Approval · Default");

  // Open session: Tab to the active session row and press Enter.
  const row = await tabTo(ctx, `a.tagName === "BUTTON" && a.closest(".session-row.on")`, { max: 150 });
  check(row, "keyboard loop: active session row not reachable with Tab");
  if (row) {
    await press("Enter");
    await delay(300);
  }

  // Send: Tab to the composer, type, Enter.
  const composer = await tabTo(ctx, `a.id === "composer-input"`, { max: 150 });
  check(composer, "keyboard loop: composer not reachable with Tab");
  if (!composer) return;
  await page.send("Input.insertText", { text: KBD_COMMAND });
  await press("Enter");

  // Approve: the ask is announced assertively; reach Allow once by keyboard.
  const card = await waitUntil(`Boolean(document.querySelector(".approvals-actions button"))`, 20000, "approval card");
  check(Boolean(card), "keyboard loop: approval card did not appear");
  if (!card) return;
  const announced = await waitUntil(
    `/Approval requested/.test(document.querySelector("#a11y-assertive")?.textContent || "")`,
    4000,
    "approval announcement",
  );
  check(Boolean(announced), "keyboard loop: approval was not announced in the assertive live region");
  const allow = `a.matches(".approvals-actions button") && /^Allow once/.test((a.innerText || "").trim())`;
  let reached = await tabTo(ctx, allow, { shift: true, max: 40 });
  if (!reached) reached = await tabTo(ctx, allow, { max: 60 });
  check(reached, "keyboard loop: Allow once not reachable with Tab / Shift+Tab");
  if (!reached) return;
  await shoot("a11y-approval-focus", ["desk1024", "desk1440"]);
  await press("Enter");
  const done = await waitUntil(
    `(() => {
      const t = document.querySelector(".transcript")?.innerText || "";
      return !document.querySelector(".approvals-actions button") && t.includes("uat-keyboard-loop") && /The tool call (succeeded|failed)/.test(t);
    })()`,
    20000,
    "approved tool result",
  );
  check(Boolean(done), "keyboard loop: approving with Enter did not run the tool");
  const ended = await waitUntil(
    `/Turn (finished|ended)/.test(document.querySelector("#a11y-polite")?.textContent || "")`,
    20000,
    "keyboard loop turn end",
  );
  check(Boolean(ended), "keyboard loop: turn end was not announced");
}

// In-page scan. Rules are a small, honest subset of WCAG A/AA checks that
// need no library: names for controls, labels for fields, alt text, unique
// ids, no focusable content under aria-hidden, no positive tabindex.
export const SCAN_EXPR = `(() => {
  const out = [];
  const visible = (el) => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0;
  };
  const text = (el) => (el ? (el.innerText || el.textContent || "").replace(/\\s+/g, " ").trim() : "");
  const describe = (el) => {
    const id = el.id ? "#" + el.id : "";
    const cls = typeof el.className === "string" && el.className.trim() ? "." + el.className.trim().split(/\\s+/).slice(0, 2).join(".") : "";
    return el.tagName.toLowerCase() + id + cls;
  };
  const name = (el) => {
    const label = (el.getAttribute("aria-label") || "").trim();
    if (label) return label;
    const by = el.getAttribute("aria-labelledby");
    if (by) {
      const t = by.split(/\\s+/).map((id) => text(document.getElementById(id))).join(" ").trim();
      if (t) return t;
    }
    if (el.labels && el.labels.length) {
      const t = [...el.labels].map(text).join(" ").trim();
      if (t) return t;
    }
    const own = text(el);
    if (own && !["INPUT", "TEXTAREA", "SELECT"].includes(el.tagName)) return own;
    const alt = [...el.querySelectorAll("img[alt], svg[aria-label]")].map((i) => i.getAttribute("alt") || i.getAttribute("aria-label")).join(" ").trim();
    if (alt) return alt;
    return (el.getAttribute("title") || el.getAttribute("placeholder") || "").trim();
  };
  const add = (rule, impact, el, help) => out.push({ rule, impact, target: describe(el), help });
  for (const el of document.querySelectorAll('button, a[href], [role="button"], [role="link"], [role="tab"], [role="menuitem"], [role="switch"], [role="checkbox"]')) {
    if (visible(el) && !name(el)) add("control-name", "serious", el, "Interactive control has no accessible name.");
  }
  for (const el of document.querySelectorAll('input:not([type="hidden"]), textarea, select')) {
    if (visible(el) && !name(el)) add("field-label", "serious", el, "Form field has no label, aria-label, title or placeholder.");
  }
  for (const el of document.querySelectorAll("img")) {
    if (visible(el) && !el.hasAttribute("alt") && el.getAttribute("role") !== "presentation") add("image-alt", "serious", el, "Image has no alt attribute.");
  }
  const ids = new Map();
  for (const el of document.querySelectorAll("[id]")) ids.set(el.id, (ids.get(el.id) || 0) + 1);
  for (const [id, n] of ids) {
    if (n > 1) out.push({ rule: "duplicate-id", impact: "moderate", target: "#" + id, help: "id is used " + n + " times." });
  }
  for (const el of document.querySelectorAll('[aria-hidden="true"] a[href], [aria-hidden="true"] button, [aria-hidden="true"] input, [aria-hidden="true"] textarea, [aria-hidden="true"] select, [aria-hidden="true"] [tabindex]:not([tabindex="-1"])')) {
    if (visible(el) && !el.disabled) add("hidden-focusable", "serious", el, "Focusable element inside aria-hidden content.");
  }
  for (const el of document.querySelectorAll("[tabindex]")) {
    if (Number(el.getAttribute("tabindex")) > 0) add("positive-tabindex", "moderate", el, "Positive tabindex breaks the natural Tab order.");
  }
  if (!document.documentElement.getAttribute("lang")) out.push({ rule: "html-lang", impact: "serious", target: "html", help: "Document has no lang." });
  return out;
})()`;

// Pure: group findings by rule for the report.
export function summarize(surfaces) {
  const byRule = {};
  let total = 0;
  for (const s of surfaces) {
    for (const f of s.findings) {
      byRule[f.rule] = (byRule[f.rule] || 0) + 1;
      total++;
    }
  }
  return { total, byRule };
}

export function scanMarkdown(surfaces) {
  const { total, byRule } = summarize(surfaces);
  const lines = [
    "# webui a11y scan (warn mode)",
    "",
    `${total} finding(s). Warn mode: findings do not fail CI yet (#1878).`,
    "",
    "| rule | count |",
    "|---|---|",
    ...Object.entries(byRule).sort().map(([rule, n]) => `| ${rule} | ${n} |`),
  ];
  for (const s of surfaces) {
    lines.push("", `## ${s.name} (${s.findings.length})`, "");
    for (const f of s.findings.slice(0, 200)) lines.push(`- **${f.rule}** (${f.impact}) \`${f.target}\` — ${f.help}`);
  }
  return lines.join("\n") + "\n";
}

export async function a11yScan(ctx, outDir) {
  const { page, check, waitUntil, press, log } = ctx;
  log("a11y scan");
  const surfaces = [];
  const scan = async (name) => {
    try {
      const findings = await page.evaluate(SCAN_EXPR);
      surfaces.push({ name, findings: Array.isArray(findings) ? findings : [] });
      check(Array.isArray(findings), `a11y scan of ${name} returned no result`);
    } catch (err) {
      check(false, `a11y scan of ${name} threw: ${err?.message || err}`);
    }
  };
  await scan("chat");
  await press("k", CTRL);
  if (await waitUntil(`Boolean(document.querySelector("[data-nav='palette-dialog']"))`, 4000, "palette for scan")) {
    await scan("command palette");
    await press("Escape");
    await delay(150);
  }
  const navs = await page.evaluate(`[...document.querySelectorAll('[data-nav="primary"] button')].map((b) => (b.innerText || "").trim()).filter(Boolean)`);
  for (const label of navs.slice(0, 6)) {
    await page.evaluate(`[...document.querySelectorAll('[data-nav="primary"] button')].find((b) => (b.innerText || "").trim() === ${JSON.stringify(label)})?.click()`);
    await delay(400);
    await scan(`view: ${label}`);
  }
  await page.evaluate(`document.querySelector('[data-nav="primary"] button')?.click()`);
  await mkdir(outDir, { recursive: true });
  const { total, byRule } = summarize(surfaces);
  await writeFile(join(outDir, "a11y-scan.json"), JSON.stringify({ mode: "warn", total, byRule, surfaces }, null, 2));
  await writeFile(join(outDir, "a11y-scan.md"), scanMarkdown(surfaces));
  log(`a11y scan (warn): ${total} finding(s) ${JSON.stringify(byRule)}`);
}
