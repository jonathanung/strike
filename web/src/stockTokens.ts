/**
 * Stock web CSS variables derived from schemas/ui-tokens.json.
 * :root hexes are injected from this module — do not hand-copy role hexes.
 */
import tokens from "../../schemas/ui-tokens.json";

export type AppearanceSide = "light" | "dark";

export type TokenRole = { light: string; dark: string; cssVar: string };
export type TokenFile = {
  schemaVersion: string;
  id: string;
  chrome: { mode: string; corners: string; radiusWebPx: number };
  roles: Record<string, TokenRole>;
};

export const STOCK_TOKENS = tokens as TokenFile;

const STOCK_MARKER = /\/\*\s*strike-stock:(dark|light)\s*\*\//g;

export function stockRoleVars(side: AppearanceSide): Record<string, string> {
  const out: Record<string, string> = {};
  for (const role of Object.values(STOCK_TOKENS.roles)) {
    out[role.cssVar] = role[side].toLowerCase();
  }
  return out;
}

/** Role hexes plus derived cockpit tokens (code inset, mark ink, idle, etc.). */
export function stockVars(side: AppearanceSide): Record<string, string> {
  const out = stockRoleVars(side);
  const ground = out["--ground"];
  const acid = out["--acid"];
  const rule = out["--rule"];
  const text = out["--ink"];
  out["--code-bg"] = out["--surface-muted"];
  out["--mark-ink"] = side === "light" ? "#ffffff" : ground;
  out["--header-bg"] = side === "light" ? `${ground}f0` : `${ground}e8`;
  out["--glow"] = `${acid}66`;
  out["--idle"] = rule;
  out["--cap-border"] = rule;
  out["--shadow"] = side === "light" ? `${text}33` : "#00000088";
  out["--diff-add-bg"] = side === "light" ? "#e8f5ec" : "#1a2a22";
  out["--diff-del-bg"] = side === "light" ? "#f8e8e8" : "#2a1a1c";
  out["--unavailable-bg"] = side === "light" ? "#f8e8e8" : "#2a1a1c";
  return out;
}

export function emitStockDeclarations(side: AppearanceSide, indent = "  "): string {
  return Object.entries(stockVars(side))
    .map(([name, value]) => `${indent}${name}: ${value};`)
    .join("\n");
}

/** Replace `/* strike-stock:dark|light *\/` markers with token declarations. */
export function injectStockTokens(css: string): string {
  return css.replace(STOCK_MARKER, (_match, side: string) => {
    const appearance = side === "light" ? "light" : "dark";
    return `/* strike-stock:${appearance} */\n${emitStockDeclarations(appearance)}`;
  });
}
