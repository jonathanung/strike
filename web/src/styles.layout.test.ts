import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";
import { SHELL_BREAKPOINTS } from "./shellProfile";

const css = readFileSync(path.join(path.dirname(fileURLToPath(import.meta.url)), "styles.css"), "utf8");

function rule(selector: string): string {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  const match = css.match(new RegExp(`(?:^|\\n)${escaped}\\s*\\{([^}]*)\\}`));
  expect(match, `missing rule ${selector}`).toBeTruthy();
  return match![1];
}

describe("shell flex layout tokens (#1236)", () => {
  it("defines header/gutter/pane layout tokens next to density tokens", () => {
    expect(css).toMatch(/--header-height:\s*52px/);
    expect(css).toMatch(/--gutter:\s*12px/);
    expect(css).toMatch(/--pane-min:\s*180px/);
    expect(css).toMatch(/--header-height\s*\/\s*--gutter\s*\/\s*--pane-min/);
  });

  it("sizes the desktop shell from tokens and lets columns shrink", () => {
    const shell = rule(".app-shell");
    expect(shell).toMatch(/grid-template:\s*var\(--header-height\)\s+minmax\(0,\s*1fr\)/);
    expect(shell).toMatch(/minmax\(0,\s*var\(--nav-width/);
    expect(shell).toMatch(/minmax\(0,\s*1fr\)/);
    expect(shell).toMatch(/minmax\(0,\s*var\(--inspector-width/);
    expect(shell).not.toMatch(/minmax\(420px/);
    expect(shell).toMatch(/overflow:\s*hidden/);
    expect(shell).toMatch(/max-width:\s*100vw/);
  });

  it("does not hard-code the 52px header row outside the token", () => {
    const withoutToken = css.replace(/--header-height:\s*52px;/, "");
    expect(withoutToken).not.toMatch(/grid-template[^;]*52px/);
    expect(withoutToken).not.toMatch(/min-height:\s*calc\(52px/);
    expect(css).toMatch(/min-height:\s*calc\(var\(--header-height\)/);
  });

  it("lets header chrome, wordmark, and palette shrink instead of overflowing", () => {
    expect(rule("header")).toMatch(/min-width:\s*0/);
    expect(rule("header")).toMatch(/gap:\s*var\(--gutter\)/);
    expect(rule(".wordmark")).toMatch(/min-width:\s*0/);
    expect(rule(".wordmark")).toMatch(/flex:\s*0\s+1\s+202px/);
    expect(rule(".wordmark")).toMatch(/overflow:\s*hidden/);
    expect(rule(".wordmark")).not.toMatch(/width:\s*202px/);
    expect(rule(".wordmark strong")).toMatch(/text-overflow:\s*ellipsis/);
    expect(rule(".session-line")).toMatch(/min-width:\s*0/);
    expect(rule(".session-line")).toMatch(/overflow:\s*hidden/);
    expect(rule(".mode-switch")).toMatch(/min-width:\s*0/);
    expect(rule(".palette-trigger")).toMatch(/min-width:\s*0/);
    expect(rule(".ui-btn-icon, .icon-button")).toMatch(/flex-shrink:\s*0/);
  });

  it("keeps rails, inspector, transcript, and composer shrinkable", () => {
    expect(rule(".navigation, .inspector")).toMatch(/min-width:\s*0/);
    expect(rule(".navigation")).toMatch(/min-width:\s*0/);
    expect(rule(".navigation")).toMatch(/overflow:\s*hidden/);
    expect(rule(".inspector")).toMatch(/min-width:\s*0/);
    expect(rule(".inspector")).toMatch(/overflow:\s*hidden/);
    expect(rule(".inspector-body")).toMatch(/min-width:\s*0/);
    expect(rule(".inspector-body")).toMatch(/min-height:\s*0/);
    expect(rule("main")).toMatch(/min-width:\s*0/);
    expect(rule("main")).toMatch(/overflow:\s*hidden/);
    expect(rule(".transcript")).toMatch(/min-width:\s*0/);
    expect(rule(".transcript")).toMatch(/overflow:\s*auto/);
    expect(rule(".composer")).toMatch(/min-width:\s*0/);
    expect(rule(".composer-bar")).toMatch(/flex-wrap:\s*wrap/);
    expect(rule(".composer-bar")).toMatch(/min-width:\s*0/);
    expect(rule(".composer-bar > span")).toMatch(/flex-wrap:\s*wrap/);
    expect(rule(".composer-send")).toMatch(/flex-shrink:\s*0/);
  });

  it("lets Code explorer, Team, and settings dialogs shrink or wrap", () => {
    expect(rule(".code-explorer")).toMatch(/min-width:\s*0/);
    expect(rule(".code-explorer-body")).toMatch(/minmax\(0,\s*240px\)\s+minmax\(0,\s*1fr\)/);
    expect(rule(".code-explorer-body")).toMatch(/min-width:\s*0/);
    expect(rule(".code-sidebar")).toMatch(/min-width:\s*0/);
    expect(rule(".code-search")).toMatch(/min-width:\s*0/);
    expect(rule(".code-search input")).toMatch(/min-width:\s*0/);
    expect(rule(".team-workspace")).toMatch(/min-width:\s*0/);
    expect(rule(".team-workspace-head")).toMatch(/flex-wrap:\s*wrap/);
    expect(rule(".team-workspace-head")).toMatch(/min-width:\s*0/);
    expect(rule(".team-detail header")).toMatch(/flex-wrap:\s*wrap/);
    expect(rule(".settings-dialog")).toMatch(/min-width:\s*0/);
    expect(rule(".settings-form fieldset label")).toMatch(/minmax\(0,\s*140px\)\s+minmax\(0,\s*1fr\)/);
    expect(rule(".settings-form fieldset label")).toMatch(/min-width:\s*0/);
    expect(css).toMatch(/\.settings-form fieldset label\s*\{\s*grid-template-columns:\s*minmax\(0,\s*1fr\)/);
  });

  it("keeps the phone mode bar visible after the desktop hide rule", () => {
    expect(rule('.app-shell[data-shell="phone"]')).toMatch(/grid-template:\s*auto\s+minmax\(0,\s*1fr\)\s+auto/);
    expect(css).toMatch(/@media \(max-width: 599px\)[\s\S]*\.palette-trigger kbd\s*\{\s*display:\s*none/);
    const hide = css.lastIndexOf(".mode-bottom-bar { display: none; }");
    const show = css.lastIndexOf('.app-shell[data-shell="phone"] .mode-bottom-bar');
    expect(hide).toBeGreaterThan(-1);
    expect(show).toBeGreaterThan(hide);
    expect(rule('.app-shell[data-shell="phone"] .mode-bottom-bar')).toMatch(/display:\s*flex/);
  });

  it("keeps desktop 1280 and phone 360 on opposite sides of the shell breakpoints", () => {
    expect(1280).toBeGreaterThan(SHELL_BREAKPOINTS.tabletMax);
    expect(360).toBeLessThanOrEqual(SHELL_BREAKPOINTS.phoneMax);
  });
});
