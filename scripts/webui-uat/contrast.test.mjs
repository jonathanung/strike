import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { STYLE_CSS, audit, ratio, table, themes } from "./contrast.mjs";

const css = readFileSync(STYLE_CSS, "utf8");
const webui = (name) => readFileSync(fileURLToPath(new URL(`../../webui/${name}`, import.meta.url)), "utf8");

test("ratio follows the WCAG relative luminance formula", () => {
  assert.equal(ratio("#000000", "#ffffff").toFixed(2), "21.00");
  assert.equal(ratio("#fff", "#fff").toFixed(2), "1.00");
  // Figures from the #1516 audit.
  assert.equal(ratio("#4c566a", "#2e3440").toFixed(2), "1.69");
  assert.equal(ratio("#4f4d63", "#14131c").toFixed(2), "2.26");
  assert.equal(ratio("#f3f1fa", "#14131c").toFixed(2), "16.47");
  assert.throws(() => ratio("purple", "#fff"), /hex/);
});

test("every bundled theme is resolved in both schemes", () => {
  const seen = themes(css).map((t) => `${t.theme}/${t.scheme}`).sort();
  const want = ["strike", "nord", "dracula", "gruvbox", "monokai", "tokyo-night", "catppuccin"]
    .flatMap((t) => [`${t}/dark`, `${t}/light`])
    .sort();
  assert.deepEqual(seen, want);
});

test("theme text and UI boundary tokens meet WCAG AA", () => {
  const rows = audit(css);
  const bad = rows.filter((r) => !r.pass);
  assert.equal(bad.length, 0, `contrast failures:\n${table(bad)}`);
});

test("the audit catches a regression", () => {
  // Nord's shipped muted (#1516) on its ground must fail.
  const broken = css.replace(
    /(:root\[data-theme="nord"\]:not\(\[data-scheme="light"\]\) \{[^}]*--muted: )#[0-9a-f]{6}/i,
    "$1#4c566a",
  );
  assert.notEqual(broken, css);
  const nord = audit(broken).filter((r) => r.theme === "nord" && r.scheme === "dark" && r.pair === "muted/ground");
  assert.equal(nord.length, 1);
  assert.equal(nord[0].pass, false);
});

test("text controls draw their boundary with --rule, not decorative --border-muted", () => {
  const controls = [
    ["style.css", ".composer textarea {"],
    ["style.css", ".daemon input, .search, .daemon select, select.mode, .palette input {"],
    ["style.css", ".model-menu-panel input {"],
    ["goals.css", ".goals-field input, .goals-field textarea {"],
    ["editor.css", ".ev-search {"],
    ["transcript-find.css", ".tf-search {"],
  ];
  for (const [file, selector] of controls) {
    const sheet = webui(file);
    const at = sheet.indexOf(selector);
    assert.ok(at >= 0, `${file}: missing ${selector}`);
    const body = sheet.slice(at, sheet.indexOf("}", at));
    assert.match(body, /border:[^;]*var\(--rule\)/, `${file} ${selector} border`);
  }
});

test("chat view exposes a main landmark, an h1 and a named composer", () => {
  const src = webui("src/main.rs");
  assert.match(src, /<main class="chat" aria-labelledby="chat-heading">/);
  assert.match(src, /<h1 id="chat-heading" class="sr-only">/);
  assert.match(src, /id="composer-input"\s+aria-label="Message this session"/);
  assert.match(css, /\.sr-only \{/);
});
