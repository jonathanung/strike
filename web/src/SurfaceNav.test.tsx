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
  });
});
