import { memo, useCallback, useMemo, useState, type ReactNode, type RefObject } from "react";
import type { TranscriptItem } from "./types";
import { VirtualList } from "./VirtualList";

const filePattern = /(?:^|[\s(`])([\w./-]+\.[a-zA-Z0-9]+)(?::(\d+))?(?=[:\s)`]|$)/g;
const EXPLORATION_TOOLS = new Set(["read", "glob", "grep", "toolsearch", "definition", "references", "symbols"]);
const DIFF_TOOLS = new Set(["edit", "write", "apply_patch", "apply-patch", "notebook_edit"]);
const MAX_TOOL_TAIL = 40;
const MAX_SUMMARY_CHARS = 4000;
const MAX_MARKDOWN_CHARS = 12_000;
const MAX_CODE_BLOCK_LINES = 400;
const MAX_DIFF_LINES = 800;

export type FileRefHandler = (path: string, line?: number) => void;

function Inline({ text, onFileRef }: { text: string; onFileRef?: FileRefHandler }) {
  const parts = text.split(/(`[^`\n]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\))/g);
  return <>{parts.map((part, index) => {
    if (part.startsWith("`") && part.endsWith("`")) return <code key={index}>{part.slice(1, -1)}</code>;
    if (part.startsWith("**") && part.endsWith("**")) return <strong key={index}>{part.slice(2, -2)}</strong>;
    const link = part.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
    if (link && /^(https?:|\/|#)/.test(link[2])) return <a key={index} href={link[2]} rel="noreferrer">{link[1]}</a>;
    const nodes: ReactNode[] = [];
    let last = 0;
    const re = new RegExp(filePattern.source, filePattern.flags);
    for (const match of part.matchAll(re)) {
      const at = match.index || 0;
      const full = match[0];
      const path = match[1];
      const line = match[2] ? Number(match[2]) : undefined;
      const lead = full.slice(0, full.indexOf(path));
      nodes.push(part.slice(last, at), lead);
      if (onFileRef) {
        nodes.push(
          <button type="button" className="file-ref" key={`${index}-${at}`} onClick={() => onFileRef(path, line)}>
            {path}{line ? `:${line}` : ""}
          </button>,
        );
      } else {
        nodes.push(<span className="file-ref" key={`${index}-${at}`}>{path}{line ? `:${line}` : ""}</span>);
      }
      last = at + full.length;
    }
    nodes.push(part.slice(last));
    return <span key={index}>{nodes}</span>;
  })}</>;
}

function CopyButton({ text, label = "Copy" }: { text: string; label?: string }) {
  const [done, setDone] = useState(false);
  return (
    <button
      type="button"
      className="copy-btn"
      onClick={() => {
        void navigator.clipboard?.writeText(text).then(() => {
          setDone(true);
          window.setTimeout(() => setDone(false), 1200);
        });
      }}
    >
      {done ? "Copied" : label}
    </button>
  );
}

export function Markdown({ text, onFileRef }: { text: string; onFileRef?: FileRefHandler }) {
  const { text: bounded, truncated } = boundText(text, MAX_MARKDOWN_CHARS);
  const blocks = bounded.split(/(```[\s\S]*?```)/g);
  return <div className="markdown">{blocks.map((block, index) => {
    if (block.startsWith("```")) {
      const lines = block.slice(3, -3).replace(/^\n/, "").split("\n");
      const language = lines[0].match(/^[\w+-]+$/) ? lines.shift() : "text";
      let codeLines = lines;
      let codeTrunc = false;
      if (codeLines.length > MAX_CODE_BLOCK_LINES) {
        codeTrunc = true;
        codeLines = [
          ...codeLines.slice(0, Math.floor(MAX_CODE_BLOCK_LINES / 2)),
          `… ${lines.length - MAX_CODE_BLOCK_LINES} lines omitted …`,
          ...codeLines.slice(-Math.floor(MAX_CODE_BLOCK_LINES / 2)),
        ];
      }
      const code = codeLines.join("\n");
      const fullCode = lines.join("\n");
      return (
        <figure className="code-block" key={index}>
          <figcaption>
            <span>{language}{codeTrunc ? " · bounded" : ""}</span>
            <CopyButton text={fullCode} />
          </figcaption>
          <pre><code>{code}</code></pre>
        </figure>
      );
    }
    return block.split("\n").map((line, lineIndex) => {
      if (/^#{1,3} /.test(line)) {
        const level = line.match(/^#+/)?.[0].length || 1;
        const content = line.replace(/^#+\s/, "");
        return level === 1
          ? <h2 key={lineIndex}><Inline text={content} onFileRef={onFileRef} /></h2>
          : <h3 key={lineIndex}><Inline text={content} onFileRef={onFileRef} /></h3>;
      }
      if (/^[-*] /.test(line)) return <div className="list-line" key={lineIndex}>— <Inline text={line.slice(2)} onFileRef={onFileRef} /></div>;
      return line ? <p key={lineIndex}><Inline text={line} onFileRef={onFileRef} /></p> : <br key={lineIndex} />;
    });
  })}{truncated && <p className="muted">Message truncated for display; full text remains in session memory.</p>}</div>;
}

export function DiffViewer({
  text,
  path,
  onFileRef,
}: {
  text: string;
  path?: string;
  onFileRef?: FileRefHandler;
}) {
  const [wrap, setWrap] = useState(false);
  const lines = text.split("\n");
  const bounded = lines.length > MAX_DIFF_LINES ? [...lines.slice(0, Math.floor(MAX_DIFF_LINES / 2)), `… ${lines.length - MAX_DIFF_LINES} lines omitted …`, ...lines.slice(-Math.floor(MAX_DIFF_LINES / 2))] : lines;
  return (
    <div className="diff-viewer">
      <div className="diff-toolbar">
        {path ? (
          onFileRef
            ? <button type="button" className="file-ref" onClick={() => onFileRef(path)}>{path}</button>
            : <span className="file-ref">{path}</span>
        ) : <span>diff</span>}
        <label className="diff-wrap">
          <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} />
          Wrap
        </label>
        <CopyButton text={text} />
      </div>
      <pre className={wrap ? "diff wrap" : "diff"}>
        {bounded.map((line, i) => {
          const cls = line.startsWith("+") && !line.startsWith("+++")
            ? "add"
            : line.startsWith("-") && !line.startsWith("---")
              ? "del"
              : line.startsWith("@@")
                ? "hunk"
                : "";
          return <span className={cls} key={i}>{line}{"\n"}</span>;
        })}
      </pre>
    </div>
  );
}

function toolName(item: TranscriptItem): string {
  const fromTitle = (item.title || "").replace(/^tool\s*[·:.-]\s*/i, "").trim();
  if (fromTitle) return fromTitle.split(/\s+/)[0].toLowerCase();
  const d = item.data || {};
  const n = d.name || d.tool || d.toolName;
  return typeof n === "string" ? n.toLowerCase() : "tool";
}

function toolState(item: TranscriptItem): { label: string; kind: "running" | "done" | "error" | "denied" } {
  const d = item.data || {};
  const status = String(d.status || d.state || "").toLowerCase();
  if (status === "denied" || status === "permission_denied") return { label: "denied", kind: "denied" };
  if (status === "error" || status === "failed" || item.kind === "error") return { label: "error", kind: "error" };
  if (status === "running" || status === "in_progress" || d.running === true) return { label: "running", kind: "running" };
  if (item.text.trim()) return { label: "done", kind: "done" };
  return { label: status || "done", kind: status === "running" ? "running" : "done" };
}

function durationLabel(item: TranscriptItem): string | undefined {
  const d = item.data || {};
  const ms = typeof d.durationMs === "number" ? d.durationMs
    : typeof d.duration_ms === "number" ? d.duration_ms
      : typeof d.elapsedMs === "number" ? d.elapsedMs : undefined;
  if (ms === undefined || ms < 0) return undefined;
  if (ms < 1000) return `${Math.round(ms)}ms`;
  return `${(ms / 1000).toFixed(ms >= 10_000 ? 0 : 1)}s`;
}

function extractDiffPath(raw: string): string | undefined {
  const m = raw.match(/^\+\+\+\s+(?:b\/)?(.+)$/m) || raw.match(/^---\s+(?:a\/)?(.+)$/m);
  return m?.[1]?.trim() || undefined;
}

function isDiffText(raw: string): boolean {
  return raw.includes("@@") && (/^\+\+\+/m.test(raw) || /^---/m.test(raw));
}

function boundText(raw: string, limit = MAX_SUMMARY_CHARS): { text: string; truncated: boolean } {
  if (raw.length <= limit) return { text: raw, truncated: false };
  return {
    text: `${raw.slice(0, limit)}\n… truncated (${raw.length.toLocaleString()} chars; full text retained in session memory)`,
    truncated: true,
  };
}

function liveTail(raw: string): string {
  const lines = raw.split("\n");
  if (lines.length <= MAX_TOOL_TAIL) return raw;
  return `… ${lines.length - MAX_TOOL_TAIL} earlier lines …\n${lines.slice(-MAX_TOOL_TAIL).join("\n")}`;
}

function SemanticEvent({ item, onFileRef }: { item: TranscriptItem; onFileRef?: FileRefHandler }) {
  const d = item.data || {};
  const kind = String(d.eventKind || d.kind || item.title || "").toLowerCase();
  if (kind.includes("verification") || item.title?.toLowerCase().includes("verification")) {
    const passed = d.passed === true || d.verified === true;
    return (
      <article className="message system semantic-event">
        <div className="message-label">Verification</div>
        <p>{passed ? "Passed" : d.passed === false ? "Failed" : "Completed"}{d.summary ? ` — ${String(d.summary)}` : item.text ? ` — ${item.text}` : ""}</p>
      </article>
    );
  }
  if (kind.includes("overlap") || kind.includes("path.overlap")) {
    const path = String(d.path || "");
    return (
      <article className="message system semantic-event">
        <div className="message-label">Path overlap</div>
        <p>
          {onFileRef && path
            ? <button type="button" className="file-ref" onClick={() => onFileRef(path)}>{path}</button>
            : path || "paths"}
          {Array.isArray(d.sessions) ? ` · ${d.sessions.length} sessions` : ""}
        </p>
      </article>
    );
  }
  if (kind.includes("denied") || kind.includes("permission")) {
    return (
      <article className="message system semantic-event">
        <div className="message-label">Permission denied</div>
        <p>{item.text || String(d.message || d.permission || "Permission denied")}</p>
      </article>
    );
  }
  if (kind.includes("handoff") || kind.includes("agent.message")) {
    return (
      <article className="message system semantic-event">
        <div className="message-label">{kind.includes("handoff") ? "Handoff" : "Agent message"}</div>
        <p>
          {d.from ? `${String(d.from)} → ` : ""}{d.to ? String(d.to) : ""}
          {d.summary || d.body || item.text ? ` — ${String(d.summary || d.body || item.text)}` : ""}
        </p>
      </article>
    );
  }
  return null;
}

export function ToolCard({ item, onFileRef }: { item: TranscriptItem; onFileRef?: FileRefHandler }) {
  const name = toolName(item);
  const state = toolState(item);
  const duration = durationLabel(item);
  const raw = item.text.trim();
  const { text: bounded, truncated } = boundText(raw);
  let parsed: unknown;
  try { parsed = JSON.parse(raw); } catch { parsed = undefined; }
  const diff = DIFF_TOOLS.has(name) || isDiffText(raw);
  const path = typeof (item.data || {}).path === "string"
    ? String((item.data || {}).path)
    : extractDiffPath(raw);
  const args = (item.data || {}).args ?? (item.data || {}).input;
  const tail = state.kind === "running" ? liveTail(bounded) : bounded;

  return (
    <details className={`tool-card state-${state.kind}`}>
      <summary>
        <span className="tool-name">{name}</span>
        <span className={`tool-state tool-state-${state.kind}`}>{state.label}</span>
        {duration && <small className="tool-duration">{duration}</small>}
        <small>{diff ? "diff" : parsed ? "structured" : truncated ? "bounded" : "output"}</small>
      </summary>
      {args !== undefined && (
        <div className="tool-args">
          <strong>Arguments</strong>
          <pre>{typeof args === "string" ? args : JSON.stringify(args, null, 2)}</pre>
        </div>
      )}
      {diff ? (
        <DiffViewer text={tail} path={path} onFileRef={onFileRef} />
      ) : (
        <pre className="tool-output">{parsed ? JSON.stringify(parsed, null, 2) : tail}</pre>
      )}
    </details>
  );
}

export type TranscriptGroup =
  | { kind: "single"; item: TranscriptItem }
  | { kind: "exploration"; items: TranscriptItem[]; id: string };

/** Collapse consecutive read/glob/grep tool cards into one exploration group. */
export function groupTranscriptItems(items: TranscriptItem[]): TranscriptGroup[] {
  const out: TranscriptGroup[] = [];
  let i = 0;
  while (i < items.length) {
    const item = items[i];
    if (item.kind === "tool" && EXPLORATION_TOOLS.has(toolName(item))) {
      const batch: TranscriptItem[] = [];
      while (i < items.length && items[i].kind === "tool" && EXPLORATION_TOOLS.has(toolName(items[i]))) {
        batch.push(items[i]);
        i += 1;
      }
      if (batch.length >= 2) {
        out.push({ kind: "exploration", items: batch, id: `explore-${batch[0].id}` });
      } else {
        out.push({ kind: "single", item: batch[0] });
      }
      continue;
    }
    out.push({ kind: "single", item });
    i += 1;
  }
  return out;
}

function ExplorationGroup({ items, onFileRef }: { items: TranscriptItem[]; onFileRef?: FileRefHandler }) {
  const names = useMemo(() => {
    const counts = new Map<string, number>();
    for (const it of items) {
      const n = toolName(it);
      counts.set(n, (counts.get(n) || 0) + 1);
    }
    return [...counts.entries()].map(([n, c]) => (c > 1 ? `${n}×${c}` : n)).join(", ");
  }, [items]);
  return (
    <details className="exploration-group">
      <summary>
        <span>Exploration</span>
        <small>{items.length} steps · {names}</small>
      </summary>
      <div className="exploration-body">
        {items.map((item) => <ToolCard key={item.id} item={item} onFileRef={onFileRef} />)}
      </div>
    </details>
  );
}

function isSemanticSystem(item: TranscriptItem): boolean {
  if (item.kind !== "system" && item.kind !== "tool") return false;
  const t = `${item.title || ""} ${JSON.stringify(item.data || {})}`.toLowerCase();
  return /verification|path\.overlap|overlap|permission|denied|handoff|agent\.message/.test(t);
}

export function Transcript({
  item,
  showThinking = true,
  onFileRef,
}: {
  item: TranscriptItem;
  showThinking?: boolean;
  onFileRef?: FileRefHandler;
}) {
  if (item.kind === "reasoning" && !showThinking) return null;
  if (isSemanticSystem(item)) {
    const semantic = <SemanticEvent item={item} onFileRef={onFileRef} />;
    if (semantic) return semantic;
  }
  if (item.kind === "tool") {
    return (
      <article className={`message tool`}>
        <div className="message-label">{item.title || toolName(item)}</div>
        <ToolCard item={item} onFileRef={onFileRef} />
      </article>
    );
  }
  return (
    <article className={`message ${item.kind}`}>
      <div className="message-label">{item.title || item.kind}</div>
      <Markdown text={item.text} onFileRef={onFileRef} />
    </article>
  );
}

export type TranscriptListProps = {
  items: TranscriptItem[];
  showThinking?: boolean;
  onFileRef?: FileRefHandler;
  /** Scroll container for windowing (transcript section). */
  scrollRef?: RefObject<HTMLElement | null>;
  /** Stick to bottom while the user is following the live tail. */
  stickToBottom?: boolean;
  /** Enable window virtualization (default true when items are large). */
  virtualize?: boolean;
};

function renderGroup(
  g: TranscriptGroup,
  showThinking: boolean,
  onFileRef?: FileRefHandler,
) {
  if (g.kind === "exploration") {
    return (
      <article className="message tool exploration" aria-label={`Exploration ${g.items.length} steps`}>
        <div className="message-label">Exploration</div>
        <ExplorationGroup items={g.items} onFileRef={onFileRef} />
      </article>
    );
  }
  return <Transcript item={g.item} showThinking={showThinking} onFileRef={onFileRef} />;
}

function TranscriptListImpl({
  items,
  showThinking = true,
  onFileRef,
  scrollRef,
  stickToBottom = false,
  virtualize,
}: TranscriptListProps) {
  const groups = useMemo(() => groupTranscriptItems(items), [items]);
  const shouldVirtualize = virtualize ?? groups.length > 40;
  const itemKey = useCallback((g: TranscriptGroup) => (g.kind === "exploration" ? g.id : g.item.id), []);
  const renderItem = useCallback(
    (g: TranscriptGroup) => renderGroup(g, showThinking, onFileRef),
    [showThinking, onFileRef],
  );

  if (!shouldVirtualize) {
    return (
      <div className="transcript-list" role="log" aria-label="Conversation messages" data-virtual-mounted={groups.length} data-virtual-total={groups.length}>
        {groups.map((g) => (
          <div key={itemKey(g)}>{renderGroup(g, showThinking, onFileRef)}</div>
        ))}
      </div>
    );
  }

  return (
    <VirtualList
      className="transcript-list"
      items={groups}
      itemKey={itemKey}
      renderItem={renderItem}
      scrollRef={scrollRef}
      stickToBottom={stickToBottom}
      estimateHeight={120}
      overscan={8}
      maxMounted={80}
      aria-label="Conversation messages"
    />
  );
}

/** Memoized so team/roster updates do not rerender the transcript DOM. */
export const TranscriptList = memo(TranscriptListImpl);


export { formatCostNotice } from "./cost";
