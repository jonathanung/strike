import type { TranscriptItem } from "./types";

export type ExportMeta = {
  sessionId?: string;
  title?: string;
  provider?: string;
  model?: string;
  agent?: string;
  exported?: Date;
};

const TOOL_OUTPUT_MAX_LINES = 40;
const TOOL_OUTPUT_MAX_CHARS = 8 << 10;

/** Build a TUI-shaped markdown transcript from cockpit items. */
export function buildExportMarkdown(items: TranscriptItem[], meta: ExportMeta = {}): string {
  const lines: string[] = ["# Strike session export", ""];
  if (meta.sessionId?.trim()) lines.push(`- **Session:** \`${meta.sessionId.trim()}\``);
  if (meta.title?.trim()) lines.push(`- **Title:** ${oneLine(meta.title)}`);
  const model = [meta.provider, meta.model].filter(Boolean).join(" / ");
  if (model) lines.push(`- **Model:** ${oneLine(model)}`);
  if (meta.agent?.trim()) lines.push(`- **Agent:** ${oneLine(meta.agent)}`);
  const exported = meta.exported ?? new Date();
  lines.push(`- **Exported:** ${exported.toISOString()}`);
  lines.push("", "---", "");

  if (!items.length) {
    lines.push("_Empty transcript._", "");
    return lines.join("\n");
  }

  for (const item of items) {
    const section = itemMarkdown(item);
    if (!section) continue;
    lines.push(section.endsWith("\n") ? section.trimEnd() : section, "");
  }
  return lines.join("\n").replace(/\n{3,}/g, "\n\n");
}

export function defaultExportFilename(sessionId?: string, now = new Date()): string {
  const stamp = now.toISOString().replace(/[-:]/g, "").replace("T", "-").replace(/\.\d{3}Z$/, "");
  const short = (sessionId || "session").replace(/[^a-zA-Z0-9_-]/g, "").replace(/^-+|-+$/g, "").slice(0, 12).replace(/-+$/g, "") || "session";
  return `strike-${short}-${stamp}.md`;
}

/** Trigger a browser download for text content. */
export function downloadTextFile(filename: string, content: string, mime = "text/markdown;charset=utf-8"): void {
  const blob = new Blob([content], { type: mime });
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.rel = "noopener";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  URL.revokeObjectURL(url);
}

function itemMarkdown(item: TranscriptItem): string {
  const body = (item.text || "").replace(/\s+$/u, "");
  switch (item.kind) {
    case "user":
      return body ? `## You\n\n${body}` : "## You\n\n_Empty message._";
    case "assistant":
      return body ? `## Strike\n\n${body}` : "## Strike\n\n_Empty response._";
    case "reasoning":
      return body ? `### Thinking\n\n${fenced(body)}` : "";
    case "tool": {
      const name = sanitizeIdent(item.title || String(item.data?.name || "tool"));
      const status = toolStatus(item.data);
      const parts = [`### Tool: \`${name}\` (${status})`];
      if (body) parts.push("", fenced(summarizeOutput(body)));
      return parts.join("\n");
    }
    case "error":
      return body ? `### Error\n\n${body}` : "";
    case "system":
      return body ? `### ${oneLine(item.title || "Info")}\n\n${body}` : "";
    default:
      return "";
  }
}

function toolStatus(data?: Record<string, unknown>): string {
  if (!data) return "ok";
  if (data.isError || data.error) return "error";
  if (data.done === false) return "running";
  return "ok";
}

function summarizeOutput(out: string): string {
  let text = out.replace(/\s+$/u, "");
  if (text.length > TOOL_OUTPUT_MAX_CHARS) {
    text = `${text.slice(0, TOOL_OUTPUT_MAX_CHARS)}\n... (truncated)`;
  }
  const lines = text.split("\n");
  if (lines.length > TOOL_OUTPUT_MAX_LINES) {
    text = `${lines.slice(0, TOOL_OUTPUT_MAX_LINES).join("\n")}\n... (truncated)`;
  }
  return text;
}

function fenced(body: string): string {
  const trimmed = body.replace(/\s+$/u, "");
  let ticks = "```";
  while (trimmed.includes(ticks)) ticks += "`";
  return `${ticks}\n${trimmed}\n${ticks}`;
}

function oneLine(s: string): string {
  return s.replace(/[\r\n]+/g, " ").trim();
}

function sanitizeIdent(s: string): string {
  const cleaned = s.trim().replace(/`/g, "'").replace(/[\r\n]+/g, " ");
  return cleaned || "tool";
}
