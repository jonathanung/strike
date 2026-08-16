import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

type TokenRole = { light: string; dark: string; cssVar: string };
type TokenFile = {
  schemaVersion: string;
  id: string;
  chrome: { mode: string; corners: string; radiusWebPx: number };
  roles: Record<string, TokenRole>;
};

function loadTokens(): TokenFile {
  const dir = dirname(fileURLToPath(import.meta.url));
  return JSON.parse(readFileSync(resolve(dir, "../../schemas/ui-tokens.json"), "utf8")) as TokenFile;
}

function loadStyles(): string {
  const dir = dirname(fileURLToPath(import.meta.url));
  return readFileSync(resolve(dir, "styles.css"), "utf8");
}

/** Extract `--name: #hex` declarations from the first matching selector block. */
function varsInBlock(source: string, selector: string): Record<string, string> {
  const start = source.indexOf(selector);
  expect(start, `missing selector ${selector}`).toBeGreaterThanOrEqual(0);
  const open = source.indexOf("{", start);
  const close = source.indexOf("}", open);
  const body = source.slice(open + 1, close);
  const out: Record<string, string> = {};
  for (const match of body.matchAll(/(--[\w-]+)\s*:\s*(#[0-9a-fA-F]{3,8})\s*;/g)) {
    out[match[1]] = match[2].toLowerCase();
  }
  return out;
}

describe("web theme parity with schemas/ui-tokens.json", () => {
  const css = loadStyles();
  const tokens = loadTokens();

  it("documents the token file and TUI Default map in the stylesheet header", () => {
    expect(css).toMatch(/schemas\/ui-tokens\.json/);
    expect(css).toMatch(/theme\.Default/);
    expect(css).toMatch(/--ink\s+Text/);
    expect(css).toMatch(/--acid\s+Accent/);
    expect(css).toMatch(/--danger\s+Danger/);
  });

  it("keeps the chrome contract (bordered, square, 2px web radius)", () => {
    expect(tokens.schemaVersion).toBe("1");
    expect(tokens.chrome.mode).toBe("bordered");
    expect(tokens.chrome.corners).toBe("square");
    expect(tokens.chrome.radiusWebPx).toBe(2);
    expect(css).toMatch(/--radius:\s*2px/);
    expect(tokens.roles.accent.dark.toLowerCase()).not.toBe("#c4b5fd");
    expect(tokens.roles.accent.light.toLowerCase()).toBe("#5b21b6");
    expect(tokens.roles.accent.dark.toLowerCase()).toBe("#7c3aed");
  });

  it("maps dark-mode CSS variables to token-file dark members", () => {
    const dark = varsInBlock(css, ":root {");
    for (const role of Object.values(tokens.roles)) {
      expect(dark[role.cssVar], role.cssVar).toBe(role.dark.toLowerCase());
    }
    expect(dark["--code-bg"]).toBe(tokens.roles.surfaceMuted.dark.toLowerCase());
    expect(dark["--mark-ink"]).toBe(tokens.roles.background.dark.toLowerCase());
  });

  it("maps light-mode CSS variables to token-file light members", () => {
    const mediaStart = css.indexOf("@media (prefers-color-scheme: light)");
    expect(mediaStart).toBeGreaterThanOrEqual(0);
    const inner = varsInBlock(css.slice(mediaStart), ":root {");
    for (const role of Object.values(tokens.roles)) {
      expect(inner[role.cssVar], role.cssVar).toBe(role.light.toLowerCase());
    }
    expect(inner["--code-bg"]).toBe(tokens.roles.surfaceMuted.light.toLowerCase());
    expect(inner["--mark-ink"]).toBe("#ffffff");
  });

  it("uses prefers-color-scheme for light and keeps color-scheme adaptive", () => {
    expect(css).toMatch(/color-scheme:\s*dark\s+light/);
    expect(css).toMatch(/@media\s*\(prefers-color-scheme:\s*light\)/);
  });

  it("supports explicit data-appearance light/dark overrides", () => {
    expect(css).toMatch(/:root\[data-appearance="light"\]/);
    expect(css).toMatch(/:root\[data-appearance="dark"\]/);
    const lightExplicit = varsInBlock(css, ':root[data-appearance="light"]');
    expect(lightExplicit["--ink"]).toBe(tokens.roles.text.light.toLowerCase());
    expect(lightExplicit["--acid"]).toBe(tokens.roles.accent.light.toLowerCase());
  });

  it("aliases leftover token names onto cockpit roles", () => {
    const root = css.slice(css.indexOf(":root {"), css.indexOf("}", css.indexOf(":root {")));
    expect(root).toMatch(/--text:\s*var\(--ink\)/);
    expect(root).toMatch(/--bg:\s*var\(--ground\)/);
    expect(root).toMatch(/--border:\s*var\(--rule\)/);
    expect(root).toMatch(/--panel:\s*var\(--surface\)/);
    expect(root).toMatch(/--accent:\s*var\(--acid\)/);
    expect(css).toMatch(/--text\/--bg\/--border\/--panel\/--accent/);
  });

  it("uses cockpit tokens for Team/Review selected tabs and child-agent cards", () => {
    expect(css).toMatch(
      /\.team-view-tabs button\.active[\s\S]*?background:\s*var\(--acid\);\s*color:\s*var\(--mark-ink\);\s*border-color:\s*var\(--ink\)/,
    );
    expect(css).toMatch(
      /\.review-tabs button\.active\s*\{\s*background:\s*var\(--acid\);\s*color:\s*var\(--mark-ink\);\s*border-color:\s*var\(--ink\)/,
    );
    expect(css).toMatch(/\.child-row:hover,\s*\.child-row\.active\s*\{\s*border-color:\s*var\(--rule\);\s*background:\s*color-mix\(in srgb,\s*var\(--surface\)/);
    expect(css).toMatch(/\.child-detail\s*\{[^}]*border:\s*1px solid var\(--rule\)/);
  });
});
