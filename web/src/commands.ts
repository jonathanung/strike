/**
 * Unified command catalog for palette, slash, and help (WEBUI.6 / #1078).
 */
import { WEB_SLASH_COMMANDS } from "./slash";
import type { WorkspaceMode } from "./surfaces";
import { MODE_PRESETS, WORKSPACE_MODES } from "./surfaces";

export type CommandAction =
  | { type: "mode"; mode: WorkspaceMode }
  | { type: "surface"; mode: WorkspaceMode; surface: string }
  | { type: "slash"; insert: string; run?: boolean }
  | { type: "session"; action: "export" | "fork" | "interrupt" | "help" | "settings" | "cost" }
  | { type: "notice"; title: string; body: string };

export type CatalogCommand = {
  id: string;
  label: string;
  detail: string;
  keywords?: string;
  action: CommandAction;
  /** When true, attach-only / missing capability should explain rather than run. */
  requiresLive?: boolean;
  requiresCapability?: string;
};

export function buildCommandCatalog(opts: {
  skills?: { name: string; description?: string }[];
  attachOnly?: boolean;
  capabilities?: Record<string, boolean>;
}): CatalogCommand[] {
  const out: CatalogCommand[] = [];

  for (const mode of WORKSPACE_MODES) {
    const preset = MODE_PRESETS[mode];
    out.push({
      id: `mode:${mode}`,
      label: `Mode: ${preset.label}`,
      detail: preset.description,
      keywords: `mode ${mode} ${preset.label}`,
      action: { type: "mode", mode },
    });
  }

  const surfaces: { mode: WorkspaceMode; surface: string; label: string }[] = [
    { mode: "code", surface: "files", label: "Files" },
    { mode: "team", surface: "roster", label: "Team roster" },
    { mode: "project", surface: "plans", label: "Plans" },
    { mode: "project", surface: "goals", label: "Goals" },
    { mode: "project", surface: "issues", label: "Issues" },
    { mode: "project", surface: "memory", label: "Memory" },
    { mode: "ops", surface: "mcp", label: "MCP" },
    { mode: "ops", surface: "plugins", label: "Plugins" },
    { mode: "ops", surface: "settings", label: "Settings surface" },
    { mode: "ops", surface: "timeline", label: "Timeline" },
    { mode: "chat", surface: "context", label: "Context doctor" },
  ];
  for (const s of surfaces) {
    out.push({
      id: `surface:${s.surface}`,
      label: `Open ${s.label}`,
      detail: `${MODE_PRESETS[s.mode].label} · ${s.surface}`,
      keywords: `${s.label} ${s.surface} ${s.mode}`,
      action: { type: "surface", mode: s.mode, surface: s.surface },
      requiresCapability: s.surface === "settings" ? "settings" : undefined,
    });
  }

  for (const c of WEB_SLASH_COMMANDS) {
    out.push({
      id: `slash:${c.label}`,
      label: c.label,
      detail: c.detail,
      keywords: `slash command ${c.label} ${c.detail}`,
      action: { type: "slash", insert: c.insert, run: c.insert === "/help" || c.insert === "/cost" || c.insert === "/export" },
      requiresLive: !["/help", "/export", "/cost", "/copy"].includes(c.label),
    });
  }

  for (const skill of opts.skills || []) {
    out.push({
      id: `skill:${skill.name}`,
      label: `/${skill.name}`,
      detail: skill.description || "Skill",
      keywords: `skill ${skill.name}`,
      action: { type: "slash", insert: `/${skill.name} ` },
      requiresLive: true,
    });
  }

  out.push(
    {
      id: "session:settings",
      label: "Settings",
      detail: "Open settings dialog",
      keywords: "settings preferences config",
      action: { type: "session", action: "settings" },
      requiresCapability: "settings",
    },
    {
      id: "session:export",
      label: "Export transcript",
      detail: "Download markdown export",
      action: { type: "session", action: "export" },
    },
    {
      id: "session:help",
      label: "Help",
      detail: "Show web slash commands",
      action: { type: "session", action: "help" },
    },
  );

  if (opts.attachOnly) {
    out.push({
      id: "notice:attach",
      label: "Attach-only mode",
      detail: "Mutations are disabled in this session",
      action: {
        type: "notice",
        title: "Attach-only",
        body: "This cockpit is attach-only. Observation works; prompts and settings mutations are blocked server-side.",
      },
    });
  }

  return out;
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function hasWord(hay: string, q: string): boolean {
  return new RegExp(`(?:^|\\s)${escapeRegExp(q)}(?:\\s|$)`).test(hay);
}

/** Higher is a better hit. Label/id matches beat a mode whose blurb mentions the query. */
function commandMatchScore(c: CatalogCommand, q: string): number {
  const label = c.label.toLowerCase();
  const detail = (c.detail || "").toLowerCase();
  const keywords = (c.keywords || "").toLowerCase();
  if (label === q || label === `open ${q}`) return 400;
  if (c.action.type === "surface" && c.action.surface.toLowerCase() === q) return 360;
  if (c.action.type === "mode" && c.action.mode.toLowerCase() === q) return 350;
  if (label.startsWith(`${q} `) || label.startsWith(`open ${q}`)) return 300;
  if (hasWord(label, q)) return 250;
  if (label.includes(q)) return 200;
  if (hasWord(keywords, q)) return 80;
  if (keywords.includes(q)) return 60;
  if (hasWord(detail, q)) return 30;
  if (detail.includes(q)) return 10;
  return 1;
}

export function filterCommands(commands: CatalogCommand[], query: string): CatalogCommand[] {
  const q = query.trim().toLowerCase();
  if (!q) return commands.slice(0, 50);
  const parts = q.split(/\s+/).filter(Boolean);
  return commands
    .filter((c) => {
      const hay = `${c.label} ${c.detail} ${c.keywords || ""}`.toLowerCase();
      return hay.includes(q) || parts.every((part) => hay.includes(part));
    })
    .sort((a, b) => commandMatchScore(b, q) - commandMatchScore(a, q))
    .slice(0, 50);
}

/** True when @ should open file search (not email-like). */
export function isFileMentionTrigger(text: string, cursor: number): { active: boolean; query: string; start: number } {
  const before = text.slice(0, cursor);
  const at = before.lastIndexOf("@");
  if (at < 0) return { active: false, query: "", start: -1 };
  const prev = at === 0 ? " " : before[at - 1];
  if (prev && !/[\s(]/.test(prev)) return { active: false, query: "", start: -1 };
  const frag = before.slice(at + 1);
  if (frag.includes(" ") || frag.includes("\n")) return { active: false, query: "", start: -1 };
  // Avoid bare email: require no domain-looking complete token without path sep when it has a dot mid-word only after chars
  if (/^[a-zA-Z0-9._%+-]+@[a-zA-Z0-9.-]+\.[a-zA-Z]{2,}$/.test(before.slice(Math.max(0, at - 40)))) {
    return { active: false, query: "", start: -1 };
  }
  return { active: true, query: frag, start: at };
}

export function insertMention(text: string, start: number, cursor: number, path: string): string {
  const before = text.slice(0, start);
  const after = text.slice(cursor);
  const insert = path.endsWith("/") ? path : path;
  return `${before}@${insert}${after.startsWith(" ") || after === "" ? after : ` ${after}`}`;
}
