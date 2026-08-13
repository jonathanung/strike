import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

describe("pane meter/badge/role CSS", () => {
  it("uses live track/fill classes and --acid for accent", () => {
    expect(css).toMatch(/\.pane-meter-track\s*\{/);
    expect(css).toMatch(/\.pane-meter-fill\s*\{[^}]*background:\s*var\(--acid\)/);
    expect(css).not.toMatch(/\.pane-meter-bar\b/);
    expect(css).toMatch(/\.pane-role\.accent\s*\{[^}]*color:\s*var\(--acid\)/);
    expect(css).toMatch(/\.pane-badge\.tone-accent\s*\{[^}]*color:\s*var\(--acid\)/);
    const paneRules = css.match(/\.pane-[^{]*\{[^}]*\}/g) || [];
    expect(paneRules.length).toBeGreaterThan(0);
    for (const rule of paneRules) {
      expect(rule).not.toMatch(/var\(--accent\)/);
    }
  });
});
