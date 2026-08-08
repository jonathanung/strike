/**
 * Responsive shell profiles (WEBUI.4 / #1074).
 * Breakpoints align with docs/web-cockpit-contract.md §4.
 */
export type ShellProfile = "desktop" | "tablet" | "phone";

/** CSS px thresholds (max-width media queries use these). */
export const SHELL_BREAKPOINTS = {
  /** Phone layout at this width and below. */
  phoneMax: 599,
  /** Tablet layout above phone and at/below this width. */
  tabletMax: 1023,
} as const;

export function shellProfileFromWidth(width: number): ShellProfile {
  if (!Number.isFinite(width) || width <= 0) return "desktop";
  if (width <= SHELL_BREAKPOINTS.phoneMax) return "phone";
  if (width <= SHELL_BREAKPOINTS.tabletMax) return "tablet";
  return "desktop";
}

/** Whether the rail is an overlay/sheet rather than a persistent column. */
export function railIsOverlay(profile: ShellProfile): boolean {
  return profile === "tablet" || profile === "phone";
}

/** Whether the inspector is a drawer/sheet overlay. */
export function inspectorIsOverlay(profile: ShellProfile): boolean {
  return profile === "tablet" || profile === "phone";
}

/** Phone uses bottom mode bar; desktop/tablet keep header modes. */
export function modesInBottomBar(profile: ShellProfile): boolean {
  return profile === "phone";
}

/** Minimum primary control target (CSS px). WCAG 2.5.5 enhanced prefers 44; never below 24. */
export const TOUCH_TARGET_MIN = 44;
export const TOUCH_TARGET_FLOOR = 24;
