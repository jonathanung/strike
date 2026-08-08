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
});
