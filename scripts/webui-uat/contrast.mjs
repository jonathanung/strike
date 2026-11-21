// WCAG 2.x contrast audit of the cockpit theme tokens in webui/style.css.
//
// Method: each theme block's custom properties are layered the way the
// browser resolves them (default :root, then the scheme block, then the
// named theme block), and every pair is scored with the WCAG relative
// luminance formula (sRGB, linearised, (L1 + 0.05) / (L2 + 0.05)).
//
//   node scripts/webui-uat/contrast.mjs          # print the table
//   node --test scripts/webui-uat/contrast.test.mjs

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

export const STYLE_CSS = fileURLToPath(new URL("../../webui/style.css", import.meta.url));

// Text pairs need 4.5:1 (WCAG 1.4.3). UI boundaries need 3:1 (WCAG 1.4.11).
// --border-muted is decorative (card and section separators) and exempt;
// controls draw their boundary with --rule.
export const TEXT_MIN = 4.5;
export const UI_MIN = 3;
export const PAIRS = [
  { fg: "ink", bg: "ground", min: TEXT_MIN, kind: "text" },
  { fg: "muted", bg: "ground", min: TEXT_MIN, kind: "text" },
  { fg: "ink", bg: "surface", min: TEXT_MIN, kind: "text" },
  { fg: "muted", bg: "surface", min: TEXT_MIN, kind: "text" },
  { fg: "rule", bg: "ground", min: UI_MIN, kind: "ui" },
  { fg: "rule", bg: "surface", min: UI_MIN, kind: "ui" },
  { fg: "border-focus", bg: "ground", min: UI_MIN, kind: "ui" },
];

export function parseHex(hex) {
  let h = hex.trim().replace(/^#/, "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  if (!/^[0-9a-f]{6}$/i.test(h)) throw new Error(`not a hex colour: ${hex}`);
  return [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
}

export function luminance(hex) {
  const [r, g, b] = parseHex(hex).map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function ratio(a, b) {
  const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
}

// Returns { selector: { var: value } } for every top-level :root block.
export function rootBlocks(css) {
  const out = {};
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, "");
  for (const m of clean.matchAll(/(:root[^{,]*)\{([^}]*)\}/g)) {
    const sel = m[1].trim();
    const vars = (out[sel] ??= {});
    for (const d of m[2].matchAll(/--([\w-]+)\s*:\s*([^;]+);/g)) {
      vars[d[1]] = d[2].trim();
    }
  }
  return out;
}

// Resolves every theme x scheme into its effective token set.
export function themes(css) {
  const blocks = rootBlocks(css);
  const dark = blocks[":root"];
  const light = { ...dark, ...blocks[':root[data-scheme="light"]'] };
  if (!dark) throw new Error("style.css has no :root block");
  const out = [
    { theme: "strike", scheme: "dark", tokens: dark },
    { theme: "strike", scheme: "light", tokens: light },
  ];
  for (const sel of Object.keys(blocks)) {
    const m = sel.match(/^:root\[data-theme="([\w-]+)"\](.*)$/);
    if (!m) continue;
    const scheme = m[2].includes('[data-scheme="light"]') && !m[2].includes(":not") ? "light" : "dark";
    const base = scheme === "light" ? light : dark;
    out.push({ theme: m[1], scheme, tokens: { ...base, ...blocks[sel] } });
  }
  return out;
}

export function audit(css) {
  const rows = [];
  for (const t of themes(css)) {
    for (const p of PAIRS) {
      const fg = t.tokens[p.fg];
      const bg = t.tokens[p.bg];
      const r = ratio(fg, bg);
      rows.push({ theme: t.theme, scheme: t.scheme, pair: `${p.fg}/${p.bg}`, fg, bg, ratio: r, min: p.min, kind: p.kind, pass: r >= p.min });
    }
  }
  return rows;
}

export function table(rows) {
  const lines = ["| theme | scheme | pair | fg | bg | ratio | min | ok |", "|---|---|---|---|---|---|---|---|"];
  for (const r of rows) {
    lines.push(`| ${r.theme} | ${r.scheme} | ${r.pair} | ${r.fg} | ${r.bg} | ${r.ratio.toFixed(2)}:1 | ${r.min}:1 | ${r.pass ? "pass" : "FAIL"} |`);
  }
  return lines.join("\n");
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const path = process.argv[2] ?? STYLE_CSS;
  const rows = audit(readFileSync(path, "utf8"));
  console.log(table(rows));
  const bad = rows.filter((r) => !r.pass);
  console.log(`\n${rows.length - bad.length}/${rows.length} pairs pass`);
  process.exitCode = bad.length ? 1 : 0;
}
