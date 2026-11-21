// Web side of the UI token contract (#1518). schemas/ui-tokens.json says the
// chrome is square with radiusWebPx 2; tokens_test.go guards the TUI, and
// this guards the cockpit's stylesheets so pill and rounded literals cannot
// drift back in under theme.css's hand-listed override (they did twice:
// #1159, then the Leptos rewrite).

import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync, readdirSync } from "node:fs";
import { fileURLToPath } from "node:url";

const root = (rel) => fileURLToPath(new URL(`../../${rel}`, import.meta.url));
const webuiDir = root("webui");
const sheets = readdirSync(webuiDir)
  .filter((name) => name.endsWith(".css"))
  .sort()
  .map((name) => ({ name, css: readFileSync(`${webuiDir}/${name}`, "utf8") }));
const tokens = JSON.parse(readFileSync(root("schemas/ui-tokens.json"), "utf8"));

const RADIUS_PART = /^(0|0px|var\(--radius\)|var\(--radius-sm\))$/;

// A radius value is allowed when every corner is a token or zero
// ("var(--radius) var(--radius) 0 0" for a bottom sheet is fine).
export function radiusAllowed(value) {
  const parts = value.replace(/\s*!important\s*$/, "").trim().split(/\s+/);
  return parts.length > 0 && parts.every((part) => RADIUS_PART.test(part));
}

// Every `border-radius` (and longhand) declaration with its file:line.
export function radiusDeclarations(name, css) {
  const out = [];
  const re = /(border(?:-[a-z]+)*-radius)\s*:\s*([^;}]+)/g;
  for (const m of css.matchAll(re)) {
    const line = css.slice(0, m.index).split("\n").length;
    out.push({ where: `${name}:${line}`, prop: m[1], value: m[2].trim() });
  }
  return out;
}

// Declarations that paint #FFD400 (--sharp) as text colour. Yellow is a
// fill/border/outline accent; as text it fails contrast on light grounds.
export function sharpTextDeclarations(name, css) {
  const out = [];
  const re = /(^|[;{\s])color\s*:\s*var\(--sharp\)/g;
  for (const m of css.matchAll(re)) {
    const line = css.slice(0, m.index + m[1].length).split("\n").length;
    out.push(`${name}:${line}`);
  }
  return out;
}

test("radius helpers read declarations and line numbers", () => {
  const css = ".a { border-radius: 999px; }\n.b {\n  border-top-left-radius: var(--radius);\n}";
  assert.deepEqual(radiusDeclarations("x.css", css), [
    { where: "x.css:1", prop: "border-radius", value: "999px" },
    { where: "x.css:3", prop: "border-top-left-radius", value: "var(--radius)" },
  ]);
  assert.deepEqual(sharpTextDeclarations("y.css", ".e { color: var(--sharp); }\n.f { background: var(--sharp); }"), ["y.css:1"]);
  assert.deepEqual(sharpTextDeclarations("z.css", ".g { border-color: var(--sharp); }"), []);
  for (const ok of ["0", "var(--radius)", "var(--radius-sm) !important", "var(--radius) var(--radius) 0 0"]) {
    assert.ok(radiusAllowed(ok), ok);
  }
  for (const bad of ["999px", "4px", "50%", "var(--radius) 8px", ""]) {
    assert.ok(!radiusAllowed(bad), bad);
  }
});

test("cockpit radius tokens match schemas/ui-tokens.json", () => {
  const style = sheets.find((s) => s.name === "style.css").css;
  const radius = style.match(/--radius:\s*(\d+)px/);
  const radiusSm = style.match(/--radius-sm:\s*(\d+)px/);
  assert.ok(radius && radiusSm, "style.css must define --radius and --radius-sm in px");
  assert.equal(tokens.chrome.corners, "square");
  assert.equal(Number(radius[1]), tokens.chrome.radiusWebPx, "--radius drifted from radiusWebPx");
  assert.ok(Number(radiusSm[1]) <= tokens.chrome.radiusWebPx, "--radius-sm exceeds radiusWebPx");
});

test("no stylesheet ships a literal radius (pills, rounded cards)", () => {
  const bad = sheets
    .flatMap(({ name, css }) => radiusDeclarations(name, css))
    .filter((d) => !radiusAllowed(d.value))
    .map((d) => `${d.where} ${d.prop}: ${d.value}`);
  assert.deepEqual(bad, [], "use var(--radius) / var(--radius-sm) / 0, not literals");
});

test("no stylesheet paints --sharp as text", () => {
  const bad = sheets.flatMap(({ name, css }) => sharpTextDeclarations(name, css));
  assert.deepEqual(bad, [], "#FFD400 is a fill accent; pair background: var(--sharp) with color: var(--on-sharp)");
});

test("docs/theme.md does not claim a rounded cockpit", () => {
  const doc = readFileSync(root("docs/theme.md"), "utf8");
  assert.doesNotMatch(doc, /10px panel radius|pill command field/);
});
