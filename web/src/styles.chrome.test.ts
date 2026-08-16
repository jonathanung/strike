import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

const css = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

function rule(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`));
  expect(match, `missing rule ${selector}`).toBeTruthy();
  return match![1];
}

function radiusPx(value: string): number[] {
  const nums = [...value.matchAll(/(-?[\d.]+)px/g)].map((m) => Number(m[1]));
  return nums;
}

describe("sharp royal-purple cockpit chrome (#1235)", () => {
  it("uses the cockpit mono stack for empty-state and dialog titles", () => {
    expect(rule(".empty-state h1")).not.toMatch(/Georgia|serif/i);
    expect(rule(".ui-dialog h2")).not.toMatch(/Georgia|serif/i);
    expect(rule("dialog h2")).not.toMatch(/Georgia|serif/i);
  });

  it("documents serif as an exception for inspector and markdown headings", () => {
    expect(css).toMatch(/Inspector titles keep serif/);
    expect(css).toMatch(/Markdown headings keep serif/);
    expect(rule(".inspector h2")).toMatch(/Georgia/);
    expect(rule(".markdown h2, .markdown h3")).toMatch(/Georgia/);
  });

  it("does not paint decorative gradients on body or main", () => {
    expect(rule("body")).not.toMatch(/radial-gradient|linear-gradient/);
    expect(rule("body")).toMatch(/background:\s*var\(--ground\)/);
    expect(rule("main")).not.toMatch(/radial-gradient|linear-gradient/);
  });

  it("does not use heavy box-shadow on composer or dialog surfaces", () => {
    expect(rule(".composer")).not.toMatch(/box-shadow/);
    expect(rule(".ui-dialog")).not.toMatch(/box-shadow/);
    expect(rule("dialog")).not.toMatch(/box-shadow/);
  });

  it("uses the shared control radius on palette trigger and Team filters", () => {
    expect(rule(".ui-btn")).toMatch(/border-radius:\s*var\(--radius\)/);
    expect(rule(".palette-trigger")).toMatch(/border-radius:\s*var\(--radius\)/);
    expect(rule(".palette-trigger")).not.toMatch(/999px/);
    expect(rule(".team-view-tabs button, .team-filters button")).toMatch(/border-radius:\s*0/);
    expect(rule(".team-view-tabs button, .team-filters button")).not.toMatch(/999px/);
  });

  it("keeps chrome radii at 0–2px except circular status dots", () => {
    const decls = [...css.matchAll(/border-radius:\s*([^;]+);/g)].map((m) => m[1].trim());
    expect(decls.length).toBeGreaterThan(10);
    for (const value of decls) {
      if (value === "50%" || value === "0" || value.includes("var(--radius)")) continue;
      const px = radiusPx(value);
      expect(px.length, `unexpected radius ${value}`).toBeGreaterThan(0);
      for (const n of px) {
        expect(n, `radius ${value} exceeds 2px`).toBeLessThanOrEqual(2);
      }
    }
  });

  it("does not use glow as primary hierarchy on busy/status indicators", () => {
    expect(rule(".pulse.busy")).not.toMatch(/--glow|box-shadow/);
    expect(rule(".root-busy")).not.toMatch(/--glow|box-shadow/);
    expect(css).not.toMatch(/\.ui-status-busy[^{]*\{[^}]*--glow/);
    expect(css).not.toMatch(/box-shadow:\s*0\s+0\s+\d+px\s+var\(--glow\)/);
  });

  it("treats Chat/Code/Team/Project/Ops as a sharp segmented control", () => {
    expect(rule(".mode-switch")).toMatch(/border:\s*1px solid var\(--rule\)/);
    expect(rule(".mode-switch")).toMatch(/border-radius:\s*var\(--radius\)/);
    expect(rule(".mode-switch")).toMatch(/gap:\s*0/);
    expect(rule(".mode-btn.active")).toMatch(/background:\s*var\(--acid\)/);
    expect(rule(".mode-btn.active")).toMatch(/color:\s*var\(--mark-ink\)/);
  });

  it("uses 1px token rules on primary surfaces", () => {
    expect(rule(".composer")).toMatch(/border:\s*1px solid var\(--rule\)/);
    expect(rule(".ui-dialog")).toMatch(/border:\s*1px solid var\(--rule\)/);
    expect(rule(".ui-btn")).toMatch(/border:\s*1px solid var\(--rule\)/);
    expect(rule(".ui-btn-primary")).toMatch(/background:\s*var\(--acid\)/);
  });
});
