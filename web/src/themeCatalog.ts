/**
 * Host theme catalog → CSS custom properties (WEBUI.11 / #1076).
 * Portable semantic roles only; TUI glyph/chrome fields are ignored by the API.
 */
import { request } from "./api";

export type ColorPair = { light?: string; dark?: string };

export type ThemeColors = {
  text?: ColorPair;
  textMuted?: ColorPair;
  accent?: ColorPair;
  accentAlt?: ColorPair;
  highlight?: ColorPair;
  success?: ColorPair;
  warning?: ColorPair;
  error?: ColorPair;
  danger?: ColorPair;
  background?: ColorPair;
  surface?: ColorPair;
  surfaceFocus?: ColorPair;
  surfaceMuted?: ColorPair;
  border?: ColorPair;
  borderFocus?: ColorPair;
  borderMuted?: ColorPair;
  userLabel?: ColorPair;
  toolLabel?: ColorPair;
  diffAdded?: ColorPair;
  diffRemoved?: ColorPair;
  overlayScrim?: ColorPair;
};

export type ThemeInfo = {
  id?: string;
  ID?: string;
  name?: string;
  Name?: string;
  appearance?: string;
  Appearance?: string;
  provenance?: string;
  Provenance?: string;
  overrode?: string;
  Overrode?: string;
  colors?: ThemeColors;
  Colors?: ThemeColors;
};

export type ThemesResponse = {
  themes?: ThemeInfo[];
  active?: string;
};

/** CSS custom property for each portable semantic role (docs/theme.md + styles.css). */
export const ROLE_CSS: Record<keyof ThemeColors, string> = {
  text: "--ink",
  textMuted: "--muted",
  accent: "--acid",
  accentAlt: "--accent-alt",
  highlight: "--highlight",
  success: "--success",
  warning: "--warning",
  error: "--signal",
  danger: "--danger",
  background: "--ground",
  surface: "--surface",
  surfaceFocus: "--raised",
  surfaceMuted: "--surface-muted",
  border: "--rule",
  borderFocus: "--border-focus",
  borderMuted: "--border-muted",
  userLabel: "--user",
  toolLabel: "--tool",
  diffAdded: "--diff-add",
  diffRemoved: "--diff-del",
  overlayScrim: "--overlay",
};

/** Roles required for contrast/focus/status — never cleared by incomplete themes. */
export const ESSENTIAL_ROLES: (keyof ThemeColors)[] = [
  "text",
  "textMuted",
  "accent",
  "background",
  "surface",
  "border",
  "borderFocus",
  "success",
  "warning",
  "error",
  "danger",
];

const THEME_STYLE_ID = "strike-theme-override";
const PREVIEW_KEY = "strike.web.theme.preview";

export function themeId(t: ThemeInfo): string {
  return String(t.id || t.ID || "").trim();
}

export function themeName(t: ThemeInfo): string {
  return String(t.name || t.Name || themeId(t) || "theme").trim();
}

export function themeProvenance(t: ThemeInfo): string {
  return String(t.provenance || t.Provenance || "builtin").trim();
}

export function themeColors(t: ThemeInfo): ThemeColors {
  return (t.colors || t.Colors || {}) as ThemeColors;
}

/** Normalize a color token; reject non-hex / empty so invalid data cannot wipe tokens. */
export function sanitizeHex(value: string | undefined): string | undefined {
  if (!value) return undefined;
  const v = value.trim().toLowerCase();
  if (/^#[0-9a-f]{3}([0-9a-f]{3})?([0-9a-f]{2})?$/.test(v)) return v;
  return undefined;
}

export type AppearanceSide = "light" | "dark";

/**
 * Resolve which adaptive side to paint for inline overrides.
 * Explicit data-appearance wins; otherwise prefer dark (stock default) when auto.
 */
export function resolveAppearanceSide(
  appearance: "auto" | "light" | "dark",
  preferDarkWhenAuto = true,
): AppearanceSide {
  if (appearance === "light") return "light";
  if (appearance === "dark") return "dark";
  if (typeof window !== "undefined" && window.matchMedia) {
    try {
      if (window.matchMedia("(prefers-color-scheme: light)").matches) return "light";
      if (window.matchMedia("(prefers-color-scheme: dark)").matches) return "dark";
    } catch { /* ignore */ }
  }
  return preferDarkWhenAuto ? "dark" : "light";
}

/** Build CSS custom-property map for one appearance side; skips invalid/empty. */
export function colorsToCSSVars(
  colors: ThemeColors,
  side: AppearanceSide,
): Record<string, string> {
  const out: Record<string, string> = {};
  for (const [role, cssVar] of Object.entries(ROLE_CSS) as [keyof ThemeColors, string][]) {
    const pair = colors[role];
    if (!pair) continue;
    const raw = side === "light" ? pair.light : pair.dark;
    const hex = sanitizeHex(raw) || sanitizeHex(side === "light" ? pair.dark : pair.light);
    if (hex) out[cssVar] = hex;
  }
  // Derived tokens used by the shell.
  if (out["--surface-muted"]) out["--code-bg"] = out["--surface-muted"];
  if (out["--ground"]) {
    out["--mark-ink"] = side === "light" ? "#ffffff" : out["--ground"];
    out["--header-bg"] = side === "light" ? `${out["--ground"]}f0` : `${out["--ground"]}e8`;
  }
  if (out["--acid"]) out["--glow"] = `${out["--acid"]}66`;
  if (out["--rule"]) out["--idle"] = out["--rule"];
  if (out["--rule"]) out["--cap-border"] = out["--rule"];
  return out;
}

/** True when every essential role has at least one valid hex after merge with base. */
export function essentialCovered(
  applied: Record<string, string>,
  base?: Record<string, string>,
): boolean {
  const merged = { ...(base || {}), ...applied };
  for (const role of ESSENTIAL_ROLES) {
    const cssVar = ROLE_CSS[role];
    if (!merged[cssVar]) return false;
  }
  return true;
}

function ensureStyleEl(): HTMLStyleElement | null {
  if (typeof document === "undefined") return null;
  let el = document.getElementById(THEME_STYLE_ID) as HTMLStyleElement | null;
  if (!el) {
    el = document.createElement("style");
    el.id = THEME_STYLE_ID;
    document.head.appendChild(el);
  }
  return el;
}

/** Apply portable colors as :root inline overrides (preview or persisted client paint). */
export function applyThemeColors(
  colors: ThemeColors,
  appearance: "auto" | "light" | "dark",
): void {
  const el = ensureStyleEl();
  if (!el) return;
  const side = resolveAppearanceSide(appearance);
  const vars = colorsToCSSVars(colors, side);
  // Incomplete themes must not erase essentials — only set provided vars.
  if (Object.keys(vars).length === 0) {
    el.textContent = "";
    return;
  }
  const body = Object.entries(vars)
    .map(([k, v]) => `  ${k}: ${v};`)
    .join("\n");
  el.textContent = `:root {\n${body}\n}`;
}

/** Clear catalog theme overrides (revert to stylesheet + data-appearance). */
export function clearThemeColors(): void {
  const el = typeof document !== "undefined"
    ? (document.getElementById(THEME_STYLE_ID) as HTMLStyleElement | null)
    : null;
  if (el) el.textContent = "";
  try { sessionStorage.removeItem(PREVIEW_KEY); } catch { /* ignore */ }
}

export function rememberPreviewId(id: string): void {
  try { sessionStorage.setItem(PREVIEW_KEY, id); } catch { /* ignore */ }
}

export function loadPreviewId(): string {
  try { return sessionStorage.getItem(PREVIEW_KEY) || ""; } catch { return ""; }
}

export const fetchThemes = (rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<ThemesResponse>(`/v1/themes${qs}`);
};

export const fetchTheme = (id: string, rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<ThemeInfo>(`/v1/themes/${encodeURIComponent(id)}${qs}`);
};

/** Persist theme id through existing settings boundary. */
export const applyThemeDefault = (id: string) =>
  request<{ theme?: string }>("/v1/settings", {
    method: "PATCH",
    body: JSON.stringify({ theme: id }),
  });
