import { describe, expect, it } from "vitest";
import { injectStockTokens, stockRoleVars, stockVars, STOCK_TOKENS } from "./stockTokens";

describe("stockTokens", () => {
  it("maps every token-file role onto its documented CSS variable", () => {
    const dark = stockRoleVars("dark");
    const light = stockRoleVars("light");
    for (const role of Object.values(STOCK_TOKENS.roles)) {
      expect(dark[role.cssVar]).toBe(role.dark.toLowerCase());
      expect(light[role.cssVar]).toBe(role.light.toLowerCase());
    }
    expect(dark["--acid"]).toBe("#7c3aed");
    expect(light["--acid"]).toBe("#5b21b6");
    expect(dark["--acid"]).not.toBe("#c4b5fd");
  });

  it("derives inset and mark tokens from the surface ladder", () => {
    const dark = stockVars("dark");
    const light = stockVars("light");
    expect(dark["--code-bg"]).toBe(STOCK_TOKENS.roles.surfaceMuted.dark.toLowerCase());
    expect(dark["--mark-ink"]).toBe(STOCK_TOKENS.roles.background.dark.toLowerCase());
    expect(light["--mark-ink"]).toBe("#ffffff");
    expect(dark["--idle"]).toBe(STOCK_TOKENS.roles.border.dark.toLowerCase());
  });

  it("injects hexes at strike-stock markers without inventing extra selectors", () => {
    const src = ":root {\n  /* strike-stock:dark */\n  --text: var(--ink);\n}\n";
    const out = injectStockTokens(src);
    expect(out).toContain("--ink: #f3f1fa;");
    expect(out).toContain("--acid: #7c3aed;");
    expect(out).toContain("--text: var(--ink);");
    expect(out.match(/strike-stock:dark/g)?.length).toBe(1);
  });
});
