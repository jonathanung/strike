import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Stock strike palette from internal/frontend/tui/theme.Default() (theme.go) — E13.8.
// Web CSS variables must stay aligned with these adaptive pairs.
const TUI_DEFAULT = {
  text: { light: "#1a1528", dark: "#f3f1fa" },
  textMuted: { light: "#5c586e", dark: "#9b99b0" },
  accent: { light: "#6d28d9", dark: "#c4b5fd" },
  accentAlt: { light: "#0e7490", dark: "#22d3ee" },
  highlight: { light: "#5b21b6", dark: "#f5f3ff" },
  success: { light: "#15803d", dark: "#4ade80" },
  warning: { light: "#b45309", dark: "#fbbf24" },
  error: { light: "#e11d48", dark: "#fb7185" },
  danger: { light: "#ea580c", dark: "#fb923c" },
  background: { light: "#ffffff", dark: "#14131c" },
  surface: { light: "#f3eef9", dark: "#232230" },
  surfaceFocus: { light: "#e9e0f7", dark: "#2e2c3e" },
  surfaceMuted: { light: "#f8f5fc", dark: "#1a1924" },
  border: { light: "#c4bfd4", dark: "#4f4d63" },
  borderFocus: { light: "#6d28d9", dark: "#c4b5fd" },
  borderMuted: { light: "#ddd8ea", dark: "#2c2a3a" },
  userLabel: { light: "#0e7490", dark: "#22d3ee" },
  toolLabel: { light: "#2563eb", dark: "#7dd3fc" },
  diffAdded: { light: "#15803d", dark: "#4ade80" },
  diffRemoved: { light: "#e11d48", dark: "#fb7185" },
  overlayScrim: { light: "#a8a3b8", dark: "#7c7a90" },
} as const;

const CSS_ROLE: Record<keyof typeof TUI_DEFAULT, string> = {
  text: "--ink",
  textMuted: "--muted",
  accent: "--acid",
  accentAlt: "--accent-alt",
  highlight: "--highlight",
  success: "--success",
  warning: "--warning",
  error: "--signal",
  danger: "--danger",
  background: "--ground",
  surface: "--surface",
  surfaceFocus: "--raised",
  surfaceMuted: "--surface-muted",
  border: "--rule",
  borderFocus: "--border-focus",
  borderMuted: "--border-muted",
  userLabel: "--user",
  toolLabel: "--tool",
  diffAdded: "--diff-add",
  diffRemoved: "--diff-del",
  overlayScrim: "--overlay",
};

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

describe("web theme parity with TUI Default()", () => {
  const css = loadStyles();

  it("documents the TUI token map in the stylesheet header", () => {
    expect(css).toMatch(/mirrors internal\/frontend\/tui\/theme\.Default/);
    expect(css).toMatch(/--ink\s+Text/);
    expect(css).toMatch(/--acid\s+Accent/);
    expect(css).toMatch(/--danger\s+Danger/);
  });

  it("maps dark-mode CSS variables to TUI Default dark members", () => {
    const dark = varsInBlock(css, ":root {");
    for (const [role, cssVar] of Object.entries(CSS_ROLE) as [keyof typeof TUI_DEFAULT, string][]) {
      expect(dark[cssVar], cssVar).toBe(TUI_DEFAULT[role].dark.toLowerCase());
    }
    expect(dark["--code-bg"]).toBe(TUI_DEFAULT.surfaceMuted.dark.toLowerCase());
    expect(dark["--mark-ink"]).toBe(TUI_DEFAULT.background.dark.toLowerCase());
  });

  it("maps light-mode CSS variables to TUI Default light members", () => {
    const mediaStart = css.indexOf("@media (prefers-color-scheme: light)");
    expect(mediaStart).toBeGreaterThanOrEqual(0);
    const inner = varsInBlock(css.slice(mediaStart), ":root {");
    for (const [role, cssVar] of Object.entries(CSS_ROLE) as [keyof typeof TUI_DEFAULT, string][]) {
      expect(inner[cssVar], cssVar).toBe(TUI_DEFAULT[role].light.toLowerCase());
    }
    expect(inner["--code-bg"]).toBe(TUI_DEFAULT.surfaceMuted.light.toLowerCase());
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
    expect(lightExplicit["--ink"]).toBe(TUI_DEFAULT.text.light.toLowerCase());
    expect(lightExplicit["--acid"]).toBe(TUI_DEFAULT.accent.light.toLowerCase());
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
