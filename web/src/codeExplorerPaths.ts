/** Path helpers for the Code explorer (WEBUI.8). */

export type DirEntryDTO = {
  Name?: string;
  name?: string;
  IsDir?: boolean;
  isDir?: boolean;
};

export type FileContentDTO = {
  Path?: string;
  path?: string;
  Content?: string;
  content?: string;
  Notice?: string;
  notice?: string;
  Skip?: boolean;
  skip?: boolean;
};

export function entryName(e: DirEntryDTO): string {
  return e.Name || e.name || "";
}

export function entryIsDir(e: DirEntryDTO): boolean {
  return Boolean(e.IsDir ?? e.isDir);
}

export function filePath(f: FileContentDTO): string {
  return f.Path || f.path || "";
}

export function fileContent(f: FileContentDTO): string {
  return f.Content || f.content || "";
}

export function fileNotice(f: FileContentDTO): string {
  return f.Notice || f.notice || "";
}

export function fileSkipped(f: FileContentDTO): boolean {
  return Boolean(f.Skip ?? f.skip);
}

/** Join parent dir + name into a workspace-relative path (no leading ./). */
export function joinPath(dir: string, name: string): string {
  const d = dir.replace(/\\/g, "/").replace(/\/+$/, "");
  const n = name.replace(/\\/g, "/").replace(/^\/+/, "");
  if (!d || d === ".") return n;
  return `${d}/${n}`;
}

/** Parent directory of a relative path; "" for root. */
export function parentPath(path: string): string {
  const p = path.replace(/\\/g, "/").replace(/\/+$/, "");
  if (!p || p === ".") return "";
  const i = p.lastIndexOf("/");
  if (i < 0) return "";
  return p.slice(0, i);
}

/** Breadcrumb segments for path (root → leaf). */
export function breadcrumbs(path: string): { label: string; path: string }[] {
  const p = path.replace(/\\/g, "/").replace(/^\/+|\/+$/g, "");
  const out: { label: string; path: string }[] = [{ label: "root", path: "" }];
  if (!p) return out;
  let acc = "";
  for (const part of p.split("/")) {
    if (!part) continue;
    acc = acc ? `${acc}/${part}` : part;
    out.push({ label: part, path: acc });
  }
  return out;
}

export function isMarkdownPath(path: string): boolean {
  return /\.(md|mdx|markdown)$/i.test(path);
}

/** Escape HTML for safe text embedding. */
export function escapeHtml(s: string): string {
  return s
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

/**
 * Minimal Markdown → safe HTML. No raw HTML passthrough, no script, links
 * are plain text (no href) to avoid javascript: URLs.
 */
export function renderMarkdownSafe(src: string): string {
  const escaped = escapeHtml(src);
  const lines = escaped.split("\n");
  const out: string[] = [];
  let inCode = false;
  for (const line of lines) {
    if (line.startsWith("```")) {
      if (inCode) {
        out.push("</code></pre>");
        inCode = false;
      } else {
        out.push("<pre class=\"md-code\"><code>");
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      out.push(line + "\n");
      continue;
    }
    if (/^### /.test(line)) {
      out.push(`<h4>${inlineMd(line.slice(4))}</h4>`);
    } else if (/^## /.test(line)) {
      out.push(`<h3>${inlineMd(line.slice(3))}</h3>`);
    } else if (/^# /.test(line)) {
      out.push(`<h2>${inlineMd(line.slice(2))}</h2>`);
    } else if (/^[-*] /.test(line)) {
      out.push(`<li>${inlineMd(line.slice(2))}</li>`);
    } else if (line.trim() === "") {
      out.push("<br/>");
    } else {
      out.push(`<p>${inlineMd(line)}</p>`);
    }
  }
  if (inCode) out.push("</code></pre>");
  return out.join("");
}

function inlineMd(s: string): string {
  // bold / italic / inline code only (already HTML-escaped)
  return s
    .replace(/`([^`]+)`/g, "<code>$1</code>")
    .replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>")
    .replace(/\*([^*]+)\*/g, "<em>$1</em>");
}

/** Parse deep-link entity "path" or "path:line". */
export function parseFileEntity(entity?: string): { path: string; line?: number } {
  if (!entity) return { path: "" };
  const m = /^(.+):(\d+)$/.exec(entity);
  if (m) {
    const line = Number(m[2]);
    return { path: m[1], line: Number.isFinite(line) && line > 0 ? line : undefined };
  }
  return { path: entity };
}
