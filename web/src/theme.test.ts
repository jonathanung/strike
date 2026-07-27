import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

// Stock strike palette from internal/tui/theme.Default() (theme.go).
// Web CSS variables must stay aligned with these adaptive pairs.
const TUI_DEFAULT = {
  text: { light: "#1a1820", dark: "#eceaf4" },
  textMuted: { light: "#5a5868", dark: "#a09eb0" },
  accent: { light: "#6d43d6", dark: "#b39dff" },
  accentAlt: { light: "#0b7285", dark: "#5cd0e8" },
  highlight: { light: "#4c1d95", dark: "#f4f1ff" },
  success: { light: "#1f8a4c", dark: "#5edb92" },
  warning: { light: "#b7791f", dark: "#f5c451" },
  error: { light: "#c23b3b", dark: "#ff8087" },
  background: { light: "#ffffff", dark: "#1c1b22" },
  surface: { light: "#f3f1f8", dark: "#252430" },
  surfaceFocus: { light: "#ebe6f8", dark: "#2f2c3c" },
  surfaceMuted: { light: "#f7f6fb", dark: "#21202a" },
  border: { light: "#b8b6c6", dark: "#4a4858" },
  borderFocus: { light: "#6d43d6", dark: "#b39dff" },
  borderMuted: { light: "#d8d6e2", dark: "#323040" },
  userLabel: { light: "#0b7285", dark: "#5cd0e8" },
  toolLabel: { light: "#3f51b5", dark: "#9db2ff" },
  diffAdded: { light: "#1f8a4c", dark: "#5edb92" },
  diffRemoved: { light: "#c23b3b", dark: "#ff8087" },
  overlayScrim: { light: "#a8a6b4", dark: "#6a6878" },
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
    expect(css).toMatch(/mirrors internal\/tui\/theme\.Default/);
    expect(css).toMatch(/--ink\s+Text/);
    expect(css).toMatch(/--acid\s+Accent/);
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
});
