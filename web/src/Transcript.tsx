import type { ReactNode } from "react";
import type { TranscriptItem } from "./types";

const filePattern = /(?:^|\s)([\w./-]+\.[a-zA-Z0-9]+)(?=[:\s]|$)/g;

function Inline({ text }: { text: string }) {
  const parts = text.split(/(`[^`\n]+`|\*\*[^*]+\*\*|\[[^\]]+\]\([^)]+\))/g);
  return <>{parts.map((part, index) => {
    if (part.startsWith("`") && part.endsWith("`")) return <code key={index}>{part.slice(1, -1)}</code>;
    if (part.startsWith("**") && part.endsWith("**")) return <strong key={index}>{part.slice(2, -2)}</strong>;
    const link = part.match(/^\[([^\]]+)\]\(([^)]+)\)$/);
    if (link && /^(https?:|\/|#)/.test(link[2])) return <a key={index} href={link[2]} rel="noreferrer">{link[1]}</a>;
    const nodes: ReactNode[] = [];
    let last = 0;
    for (const match of part.matchAll(filePattern)) {
      const at = match.index || 0;
      nodes.push(part.slice(last, at), <span className="file-ref" key={`${index}-${at}`}>{match[0]}</span>);
      last = at + match[0].length;
    }
    nodes.push(part.slice(last));
    return <span key={index}>{nodes}</span>;
  })}</>;
}

export function Markdown({ text }: { text: string }) {
  const blocks = text.split(/(```[\s\S]*?```)/g);
  return <div className="markdown">{blocks.map((block, index) => {
    if (block.startsWith("```")) {
      const lines = block.slice(3, -3).replace(/^\n/, "").split("\n");
      const language = lines[0].match(/^[\w+-]+$/) ? lines.shift() : "text";
      return <figure className="code-block" key={index}><figcaption>{language}</figcaption><pre><code>{lines.join("\n")}</code></pre></figure>;
    }
    return block.split("\n").map((line, lineIndex) => {
      if (/^#{1,3} /.test(line)) {
        const level = line.match(/^#+/)?.[0].length || 1;
        const content = line.replace(/^#+\s/, "");
        return level === 1 ? <h2 key={lineIndex}><Inline text={content} /></h2> : <h3 key={lineIndex}><Inline text={content} /></h3>;
      }
      if (/^[-*] /.test(line)) return <div className="list-line" key={lineIndex}>— <Inline text={line.slice(2)} /></div>;
      return line ? <p key={lineIndex}><Inline text={line} /></p> : <br key={lineIndex} />;
    });
  })}</div>;
}

function Tool({ item }: { item: TranscriptItem }) {
  const raw = item.text.trim();
  let parsed: unknown;
  try { parsed = JSON.parse(raw); } catch { parsed = undefined; }
  const diff = raw.includes("@@") && (/^\+\+\+/m.test(raw) || /^---/m.test(raw));
  return <details className="tool-card"><summary><span>{item.title || "tool"}</span><small>{parsed ? "structured data" : diff ? "diff" : "output"}</small></summary>{diff ? <pre className="diff">{raw.split("\n").map((line, i) => <span className={line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : ""} key={i}>{line}{"\n"}</span>)}</pre> : <pre>{parsed ? JSON.stringify(parsed, null, 2) : raw}</pre>}</details>;
}

export function Transcript({ item }: { item: TranscriptItem }) {
  return <article className={`message ${item.kind}`}><div className="message-label">{item.title || item.kind}</div>{item.kind === "tool" ? <Tool item={item} /> : <Markdown text={item.text} />}</article>;
}
