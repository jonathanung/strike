import { readFileSync } from "node:fs";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { describe, expect, it, vi } from "vitest";
import { cleanup, render, screen, fireEvent } from "@testing-library/react";
import { afterEach } from "vitest";
import { SurfaceNav } from "./SurfaceNav";
import type { SurfaceDef } from "./surfaces";

const surfaces: SurfaceDef[] = [
  {
    id: "plans",
    label: "plans",
    modes: ["project"],
    capability: "plans",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
  },
  {
    id: "goals",
    label: "goals",
    modes: ["project"],
    capability: "goals",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
  },
];

describe("SurfaceNav", () => {
  afterEach(() => cleanup());
  it("renders tab strip on desktop", () => {
    render(
      <SurfaceNav
        modeLabel="Project"
        surfaces={surfaces}
        activeId="plans"
        profile="desktop"
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("tablist", { name: /Project surfaces/i })).toBeTruthy();
    expect(screen.getByRole("tab", { name: /plans/i })).toBeTruthy();
  });

  it("renders accessible list on phone instead of tab strip", () => {
    const onChange = vi.fn();
    render(
      <SurfaceNav
        modeLabel="Project"
        surfaces={surfaces}
        activeId="plans"
        profile="phone"
        onChange={onChange}
      />,
    );
    expect(screen.queryByRole("tablist")).toBeNull();
    expect(screen.getByRole("listbox", { name: /Project surfaces/i })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: /goals/i }));
    expect(onChange).toHaveBeenCalledWith("goals");
  });

  it("renders every inspector tab so later surfaces stay activatable", () => {
    const many: SurfaceDef[] = Array.from({ length: 24 }, (_, i) => ({
      id: i === 23 ? "mcp" : `surf-${i}`,
      label: i === 23 ? "mcp" : `surf-${i}`,
      modes: ["ops"],
      capability: "always",
      attention: "none",
      lazyMount: true,
      attach: "read",
      placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
      inspector: true,
    }));
    const onChange = vi.fn();
    render(
      <SurfaceNav
        modeLabel="Chat"
        surfaces={many}
        activeId="surf-0"
        profile="desktop"
        onChange={onChange}
      />,
    );
    const tabs = screen.getAllByRole("tab");
    expect(tabs).toHaveLength(24);
    fireEvent.click(screen.getByRole("tab", { name: "mcp" }));
    expect(onChange).toHaveBeenCalledWith("mcp");
    expect(screen.getByRole("tab", { name: "surf-0" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByRole("tab", { name: "mcp" }).getAttribute("aria-selected")).toBe("false");
  });

  it("renders a wrapping tab list on tablet, not a phone sheet", () => {
    render(
      <SurfaceNav
        modeLabel="Ops"
        surfaces={surfaces}
        activeId="goals"
        profile="tablet"
        onChange={() => {}}
      />,
    );
    expect(screen.getByRole("tablist", { name: /Ops surfaces/i })).toBeTruthy();
    expect(screen.queryByRole("listbox")).toBeNull();
    expect(screen.getByRole("tab", { name: /goals/i }).getAttribute("aria-selected")).toBe("true");
  });

  it("wraps inspector tabs instead of clipping them into a horizontal scroller", () => {
    const dir = dirname(fileURLToPath(import.meta.url));
    const css = readFileSync(resolve(dir, "styles.css"), "utf8");
    const rule = css.match(/\.inspector-tabs\s*\{[\s\S]*?\n\}/);
    expect(rule?.[0]).toBeTruthy();
    expect(rule?.[0]).toMatch(/flex-wrap:\s*wrap/);
    expect(rule?.[0]).not.toMatch(/flex-wrap:\s*nowrap/);
    expect(rule?.[0]).toMatch(/overflow-x:\s*hidden/);
    expect(rule?.[0]).not.toMatch(/overflow-x:\s*auto/);
    const sheet = css.match(/\.surface-nav-sheet\s*\{[\s\S]*?\n\}/);
    expect(sheet?.[0]).toMatch(/max-height:\s*40vh/);
    expect(css).toMatch(/\.surface-nav-list\s*\{[\s\S]*flex-direction:\s*column/);
  });
});
