/** Web-safe slash catalog and dispatch (subset of TUI builtins). */

export { formatCostNotice } from "./cost";

export type SlashCompletion = { label: string; detail: string; insert: string };

export const WEB_SLASH_COMMANDS: SlashCompletion[] = [
  { label: "/help", detail: "List available web commands", insert: "/help" },
  { label: "/export", detail: "Download conversation as markdown", insert: "/export" },
  { label: "/compact", detail: "Compact model history", insert: "/compact" },
  { label: "/prompt", detail: "Inspect effective prompt", insert: "/prompt" },
  { label: "/context", detail: "Refresh context doctor", insert: "/context" },
  { label: "/rewind", detail: "Undo last turn — chat only or chat and files", insert: "/rewind" },
  { label: "/rewind-files", detail: "Undo last turn with file restore preview", insert: "/rewind-files" },
  { label: "/interrupt", detail: "Stop the running turn", insert: "/interrupt" },
  { label: "/queue", detail: "Focus the prompt queue browser", insert: "/queue" },
  { label: "/rename", detail: "Rename the current session", insert: "/rename " },
  { label: "/fork", detail: "Fork the current session", insert: "/fork" },
  { label: "/cost", detail: "Show session cost / context usage", insert: "/cost" },
  { label: "/copy", detail: "Copy last assistant reply", insert: "/copy" },
  { label: "/fast", detail: "Toggle fast mode (on|off)", insert: "/fast" },
  { label: "/agent", detail: "Select agent by name", insert: "/agent " },
  { label: "/effort", detail: "Set effort (low|medium|high|xhigh)", insert: "/effort " },
  { label: "/autonomy", detail: "Set autonomy (supervised|agent|checks)", insert: "/autonomy " },
  { label: "/mode", detail: "Set permission mode", insert: "/mode " },
  { label: "/model", detail: "Select model [provider/model]", insert: "/model " },
  { label: "/provider", detail: "Select provider", insert: "/provider " },
];

const KNOWN = new Set(WEB_SLASH_COMMANDS.map((c) => c.label.slice(1)));

export type SlashResult =
  | { kind: "pass" }
  | { kind: "unknown"; command: string }
  | { kind: "help" }
  | { kind: "export" }
  | { kind: "queue" }
  | { kind: "cost" }
  | { kind: "copy" }
  | { kind: "fork" }
  | { kind: "rename"; title: string }
  | { kind: "fast"; enabled?: boolean }
  | { kind: "usage"; message: string }
  | { kind: "op"; type: string; data?: unknown };

/** Resolve a composer line that may be a web slash command or skill. */
export function resolveSlash(text: string, skillNames: string[] = []): SlashResult {
  const trimmed = text.trim();
  if (!trimmed.startsWith("/")) return { kind: "pass" };

  const space = trimmed.search(/\s/);
  const raw = (space < 0 ? trimmed : trimmed.slice(0, space)).toLowerCase();
  const args = space < 0 ? "" : trimmed.slice(space + 1).trim();
  if (!raw.startsWith("/") || raw === "/") return { kind: "unknown", command: raw || "/" };
  const name = raw.slice(1);

  const skillHit = skillNames.some((s) => s.toLowerCase() === name);
  if (skillHit) return { kind: "pass" };
  if (!KNOWN.has(name)) return { kind: "unknown", command: raw };

  switch (name) {
    case "help":
      return { kind: "help" };
    case "export":
      return { kind: "export" };
    case "queue":
      return { kind: "queue" };
    case "cost":
      return { kind: "cost" };
    case "copy":
      return { kind: "copy" };
    case "fork":
      return { kind: "fork" };
    case "compact":
      return { kind: "op", type: "compact", data: { strategy: "summarize" } };
    case "prompt":
    case "context":
      return { kind: "op", type: "inspect.prompt" };
    case "rewind":
      return { kind: "op", type: "rewind", data: {} };
    case "rewind-files":
      return { kind: "op", type: "rewind", data: { restoreFiles: true } };
    case "interrupt":
      return { kind: "op", type: "interrupt" };
    case "rename":
      return { kind: "rename", title: args };
    case "fast": {
      if (!args) return { kind: "fast" };
      if (args === "on" || args === "true" || args === "1") return { kind: "fast", enabled: true };
      if (args === "off" || args === "false" || args === "0") return { kind: "fast", enabled: false };
      return { kind: "usage", message: "usage: /fast [on|off]" };
    }
    case "agent":
      if (!args) return { kind: "usage", message: "usage: /agent <name>" };
      return { kind: "op", type: "select.agent", data: { name: args } };
    case "effort":
      if (!args) return { kind: "usage", message: "usage: /effort <low|medium|high|xhigh>" };
      return { kind: "op", type: "set.effort", data: { level: args } };
    case "autonomy":
      if (!args) return { kind: "usage", message: "usage: /autonomy <supervised|agent|checks>" };
      return { kind: "op", type: "set.autonomy", data: { mode: args } };
    case "mode":
      if (!args) return { kind: "usage", message: "usage: /mode <default|plan|soft-approve|accept-edits|yolo>" };
      return { kind: "op", type: "set.permission_mode", data: { mode: args } };
    case "provider":
      if (!args) return { kind: "usage", message: "usage: /provider <name>" };
      return { kind: "op", type: "select.model", data: { provider: args } };
    case "model": {
      if (!args) return { kind: "usage", message: "usage: /model <model|provider/model>" };
      const slash = args.indexOf("/");
      if (slash > 0) {
        return {
          kind: "op",
          type: "select.model",
          data: { provider: args.slice(0, slash), model: args.slice(slash + 1) },
        };
      }
      return { kind: "op", type: "select.model", data: { model: args } };
    }
    default:
      return { kind: "unknown", command: raw };
  }
}

export function formatSlashHelp(skills: { name: string; description?: string }[] = []): string {
  const rows = [
    "Web slash commands:",
    ...WEB_SLASH_COMMANDS.map((c) => `  ${c.label.padEnd(16)} ${c.detail}`),
  ];
  if (skills.length) {
    rows.push("", "Skills:");
    for (const skill of skills) {
      rows.push(`  /${skill.name.padEnd(15)} ${skill.description || "Skill"}`);
    }
  }
  rows.push("", "Unknown /commands are rejected with feedback (not sent as prompts).");
  return rows.join("\n");
}

