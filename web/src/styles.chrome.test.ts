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

describe("flat cockpit chrome (#1159)", () => {
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
    expect(rule(".ui-btn")).toMatch(/border-radius:\s*2px/);
    expect(rule(".palette-trigger")).toMatch(/border-radius:\s*2px/);
    expect(rule(".palette-trigger")).not.toMatch(/999px/);
    expect(rule(".team-view-tabs button, .team-filters button")).toMatch(/border-radius:\s*2px/);
    expect(rule(".team-view-tabs button, .team-filters button")).not.toMatch(/999px/);
  });
});
