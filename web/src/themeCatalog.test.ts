import { afterEach, describe, expect, it } from "vitest";
import {
  ROLE_CSS,
  applyThemeColors,
  clearThemeColors,
  colorsToCSSVars,
  essentialCovered,
  sanitizeHex,
  themeId,
  themeName,
  themeProvenance,
  type ThemeColors,
} from "./themeCatalog";

const stockDark: ThemeColors = {
  text: { light: "#1a1528", dark: "#f3f1fa" },
  textMuted: { light: "#5c586e", dark: "#9b99b0" },
  accent: { light: "#6d28d9", dark: "#c4b5fd" },
  background: { light: "#ffffff", dark: "#14131c" },
  surface: { light: "#f3eef9", dark: "#232230" },
  border: { light: "#c4bfd4", dark: "#4f4d63" },
  borderFocus: { light: "#6d28d9", dark: "#c4b5fd" },
  success: { light: "#15803d", dark: "#4ade80" },
  warning: { light: "#b45309", dark: "#fbbf24" },
  error: { light: "#e11d48", dark: "#fb7185" },
  danger: { light: "#ea580c", dark: "#fb923c" },
};

afterEach(() => {
  clearThemeColors();
});

describe("themeCatalog portable mapping", () => {
  it("maps semantic roles to documented CSS variables", () => {
    expect(ROLE_CSS.text).toBe("--ink");
    expect(ROLE_CSS.accent).toBe("--acid");
    expect(ROLE_CSS.error).toBe("--signal");
    expect(ROLE_CSS.background).toBe("--ground");
    expect(ROLE_CSS.danger).toBe("--danger");
  });

  it("sanitizes hex and rejects injection", () => {
    expect(sanitizeHex("#c4b5fd")).toBe("#c4b5fd");
    expect(sanitizeHex("#FFF")).toBe("#fff");
    expect(sanitizeHex("red")).toBeUndefined();
    expect(sanitizeHex("url(javascript:alert(1))")).toBeUndefined();
    expect(sanitizeHex("")).toBeUndefined();
  });

  it("colorsToCSSVars uses the requested appearance side", () => {
    const dark = colorsToCSSVars(stockDark, "dark");
    expect(dark["--ink"]).toBe("#f3f1fa");
    expect(dark["--acid"]).toBe("#c4b5fd");
    expect(dark["--ground"]).toBe("#14131c");
    const light = colorsToCSSVars(stockDark, "light");
    expect(light["--ink"]).toBe("#1a1528");
    expect(light["--acid"]).toBe("#6d28d9");
  });

  it("falls back to the other side when one hex is missing", () => {
    const partial: ThemeColors = { text: { dark: "#abcdef" } };
    const light = colorsToCSSVars(partial, "light");
    expect(light["--ink"]).toBe("#abcdef");
  });

  it("ignores invalid colors so essentials stay coverable", () => {
    const bad: ThemeColors = {
      text: { dark: "not-a-color" },
      accent: { dark: "#c4b5fd" },
    };
    const vars = colorsToCSSVars(bad, "dark");
    expect(vars["--ink"]).toBeUndefined();
    expect(vars["--acid"]).toBe("#c4b5fd");
    expect(essentialCovered(vars, colorsToCSSVars(stockDark, "dark"))).toBe(true);
  });

  it("default-theme parity: stock dark covers essentials", () => {
    const vars = colorsToCSSVars(stockDark, "dark");
    expect(essentialCovered(vars)).toBe(true);
  });

  it("theme metadata helpers prefer wire casing", () => {
    expect(themeId({ ID: "nord" })).toBe("nord");
    expect(themeName({ Name: "Nord", id: "nord" })).toBe("Nord");
    expect(themeProvenance({ Provenance: "builtin" })).toBe("builtin");
    expect(themeProvenance({})).toBe("builtin");
  });

  it("applyThemeColors writes override stylesheet and clear removes it", () => {
    applyThemeColors(stockDark, "dark");
    const el = document.getElementById("strike-theme-override");
    expect(el?.textContent).toContain("--ink: #f3f1fa");
    expect(el?.textContent).toContain("--acid: #c4b5fd");
    clearThemeColors();
    expect(document.getElementById("strike-theme-override")?.textContent || "").toBe("");
  });

  it("incomplete theme does not emit empty essential wipe", () => {
    applyThemeColors({ accent: { dark: "#88c0d0" } }, "dark");
    const css = document.getElementById("strike-theme-override")?.textContent || "";
    expect(css).toContain("--acid: #88c0d0");
    // Only provided roles — browser keeps stylesheet essentials underneath.
    expect(css).not.toMatch(/--ink:\s*;/);
  });
});
