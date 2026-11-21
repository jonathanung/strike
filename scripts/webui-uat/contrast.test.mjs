import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { STYLE_CSS, TEXT_MIN, audit, ratio, rootBlocks, table, themes } from "./contrast.mjs";

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

test("accent-role text meets AA on the default Strike theme (#1337)", () => {
  // a11y.css: --acid-text is --highlight on Strike dark (where --acid as
  // text was 3.23:1) and --acid elsewhere. Non-default themes: #1516.
  const a11y = webui("a11y.css");
  assert.match(a11y, /--acid-text:\s*var\(--highlight\)/);
  assert.match(a11y, /\.chip\.on,[\s\S]*color:\s*var\(--acid-text\)/);
  const strike = themes(css).filter((t) => t.theme === "strike");
  for (const t of strike) {
    const fg = t.scheme === "dark" ? t.tokens.highlight : t.tokens.acid;
    for (const bg of ["ground", "surface", "raised"]) {
      const r = ratio(fg, t.tokens[bg]);
      assert.ok(r >= 4.5, `strike/${t.scheme} accent text ${fg} on ${bg} ${t.tokens[bg]} = ${r.toFixed(2)}`);
    }
  }
  // The pair this replaces really did fail.
  const dark = strike.find((t) => t.scheme === "dark").tokens;
  assert.ok(ratio(dark.acid, dark.ground) < 4.5);
});

// The selector a11y.css uses to set --acid-text for one theme and scheme.
function acidTextSelector(theme, scheme) {
  const light = scheme === "light" ? '[data-scheme="light"]' : ':not([data-scheme="light"])';
  return `:root[data-theme="${theme}"]${light}`;
}

function resolve(value, tokens) {
  const m = value.match(/^var\(--([\w-]+)\)$/);
  return m ? tokens[m[1]] : value;
}

test("accent-role text meets AA on every theme and scheme (#1878)", () => {
  const blocks = rootBlocks(webui("a11y.css"));
  const fallback = blocks[":root"]?.["acid-text"];
  assert.equal(fallback, "var(--acid)");
  const rows = [];
  for (const t of themes(css)) {
    const own = blocks[acidTextSelector(t.theme, t.scheme)]?.["acid-text"];
    const fg = resolve(own || fallback, t.tokens);
    for (const bg of ["ground", "surface", "raised"]) {
      const r = ratio(fg, t.tokens[bg]);
      rows.push({ theme: t.theme, scheme: t.scheme, pair: `acid-text/${bg}`, fg, bg: t.tokens[bg], ratio: r, min: TEXT_MIN, kind: "text", pass: r >= TEXT_MIN });
    }
  }
  assert.equal(rows.length, 14 * 3);
  const bad = rows.filter((r) => !r.pass);
  assert.equal(bad.length, 0, `--acid-text failures:\n${table(bad)}`);
});

test("the --acid-text check catches a theme that falls back to a failing --acid (#1878)", () => {
  const a11y = webui("a11y.css").replace(/:root\[data-theme="dracula"\]\[data-scheme="light"\] \{[^}]*\}/, "");
  const blocks = rootBlocks(a11y);
  const dracula = themes(css).find((t) => t.theme === "dracula" && t.scheme === "light");
  const own = blocks[acidTextSelector("dracula", "light")]?.["acid-text"];
  assert.equal(own, undefined);
  assert.ok(ratio(resolve("var(--acid)", dracula.tokens), dracula.tokens.raised) < TEXT_MIN);
});

test("reduced motion is honoured globally (#1337)", () => {
  const a11y = webui("a11y.css");
  assert.match(a11y, /@media \(prefers-reduced-motion: reduce\)/);
  assert.match(a11y, /animation-duration:\s*0\.01ms !important/);
  assert.match(a11y, /transition-duration:\s*0\.01ms !important/);
  const index = webui("index.html");
  assert.ok(index.indexOf("a11y.css") > 0 && index.indexOf("a11y.css") < index.indexOf("theme.css"), "a11y.css must load before theme.css");
});
