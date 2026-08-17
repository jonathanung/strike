import { describe, expect, it } from "vitest";
import {
  inspectorIsOverlay,
  modesInBottomBar,
  railIsOverlay,
  shellProfileFromWidth,
  SHELL_BREAKPOINTS,
} from "./shellProfile";

describe("shellProfile", () => {
  it("classifies desktop / tablet / phone by width", () => {
    expect(shellProfileFromWidth(1440)).toBe("desktop");
    expect(shellProfileFromWidth(1280)).toBe("desktop");
    expect(shellProfileFromWidth(1024)).toBe("desktop");
    expect(shellProfileFromWidth(SHELL_BREAKPOINTS.tabletMax)).toBe("tablet");
    expect(shellProfileFromWidth(900)).toBe("tablet");
    expect(shellProfileFromWidth(600)).toBe("tablet");
    expect(shellProfileFromWidth(SHELL_BREAKPOINTS.phoneMax)).toBe("phone");
    expect(shellProfileFromWidth(390)).toBe("phone");
    expect(shellProfileFromWidth(360)).toBe("phone");
    expect(shellProfileFromWidth(320)).toBe("phone");
  });

  it("treats invalid widths as desktop safe default", () => {
    expect(shellProfileFromWidth(0)).toBe("desktop");
    expect(shellProfileFromWidth(-1)).toBe("desktop");
    expect(shellProfileFromWidth(Number.NaN)).toBe("desktop");
  });

  it("maps overlay and bottom-bar behavior by profile", () => {
    expect(railIsOverlay("desktop")).toBe(false);
    expect(railIsOverlay("tablet")).toBe(true);
    expect(railIsOverlay("phone")).toBe(true);
    expect(inspectorIsOverlay("desktop")).toBe(false);
    expect(inspectorIsOverlay("phone")).toBe(true);
    expect(modesInBottomBar("desktop")).toBe(false);
    expect(modesInBottomBar("phone")).toBe(true);
  });
});
