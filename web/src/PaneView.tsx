import type { CSSProperties } from "react";
import { resolveFrom, type PaneViewNode } from "./panesApi";

/** Per-child minimum used for TUI-like too-narrow column fallback (matches prior 80px basis). */
const ROW_CELL_MIN_PX = 80;

function roleClass(style?: string): string {
  const s = (style || "body").toLowerCase();
  switch (s) {
    case "title":
      return "pane-role title";
    case "muted":
      return "pane-role muted";
    case "accent":
      return "pane-role accent";
    case "success":
      return "pane-role success";
    case "warning":
      return "pane-role warning";
    case "error":
    case "danger":
      return "pane-role error";
    default:
      return "pane-role body";
  }
}

function textOf(node: PaneViewNode, feeds?: Record<string, unknown>): string {
  if (node.textFrom) {
    const v = resolveFrom(feeds, node.textFrom);
    if (v) return v;
  }
  return node.text || "";
}

function truncateStyle(truncate?: string): CSSProperties | undefined {
  if (!truncate) return undefined;
  return { overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap", minWidth: 0 };
}

/** Closed icon set — unknown names omitted (same mapping as TUI paneIconGlyph). */
function paneIconGlyph(name?: string): string {
  switch ((name || "").trim().toLowerCase()) {
    case "check":
    case "ok":
      return "✓";
    case "warn":
    case "warning":
      return "◦";
    case "error":
    case "err":
      return "✗";
    case "agent":
      return "◆";
    case "folder":
    case "file":
      return "·";
    default:
      return "";
  }
}

function rowCellStyle(child: PaneViewNode): CSSProperties {
  const style: CSSProperties = {};
  if (child.flex != null && child.flex > 0) style.flexGrow = child.flex;
  if (child.min != null && child.min > 0) style.minWidth = `${child.min}ch`;
  return style;
}

function listItemLabel(item: NonNullable<PaneViewNode["items"]>[number]) {
  const glyph = paneIconGlyph(item.icon);
  return (
    <>
      <span>
        {glyph ? <span aria-hidden="true">{glyph} </span> : null}
        <strong>{item.label || item.id}</strong>
      </span>
      {item.detail ? <span className="muted"> {item.detail}</span> : null}
    </>
  );
}

export function PaneView({
  node,
  feeds,
  depth = 0,
  onSelect,
}: {
  node: PaneViewNode | null | undefined;
  feeds?: Record<string, unknown>;
  depth?: number;
  onSelect?: (id: string) => void;
}) {
  if (!node || depth > 16) {
    return <p className="muted">view truncated</p>;
  }
  const type = (node.type || "").toLowerCase();

  switch (type) {
    case "column":
      return (
        <div className="pane-column" style={{ gap: node.gap ? `${node.gap * 4}px` : undefined }}>
          {(node.children || []).map((c, i) => (
            <PaneView key={i} node={c} feeds={feeds} depth={depth + 1} onSelect={onSelect} />
          ))}
        </div>
      );
    case "row": {
      const children = node.children || [];
      const nChild = children.length;
      const gapPx = node.gap ? node.gap * 4 : 0;
      const wrap = Boolean(node.wrap);
      const gutter = nChild > 1 ? gapPx * (nChild - 1) : 0;
      const rowStyle: CSSProperties & { ["--stack-at"]?: string } = { gap: gapPx || undefined };
      if (!wrap && nChild > 0) {
        rowStyle["--stack-at"] = `${nChild * ROW_CELL_MIN_PX + gutter}px`;
      }
      return (
        <div className={wrap ? "pane-row wrap" : "pane-row"} style={rowStyle}>
          {children.map((c, i) => (
            <div key={i} className="pane-row-cell" style={rowCellStyle(c)}>
              <PaneView node={c} feeds={feeds} depth={depth + 1} onSelect={onSelect} />
            </div>
          ))}
        </div>
      );
    }
    case "text":
    case "markdown":
      return (
        <p className={roleClass(node.style)} style={truncateStyle(node.truncate)}>
          {textOf(node, feeds)}
        </p>
      );
    case "kv":
      return (
        <dl className="pane-kv">
          {(node.entries || []).map((e, i) => (
            <div key={i} className="pane-kv-row">
              <dt>{e.key || ""}</dt>
              <dd>{e.valueFrom ? resolveFrom(feeds, e.valueFrom) || e.value || "—" : e.value || "—"}</dd>
            </div>
          ))}
        </dl>
      );
    case "list":
      return (
        <ul className="pane-list" role={node.selectable ? "listbox" : "list"}>
          {(node.items || []).map((item, i) => {
            const selected = node.selectedId && item.id === node.selectedId;
            return (
              <li
                key={item.id || i}
                className={selected ? "selected" : undefined}
                role={node.selectable ? "option" : undefined}
                aria-selected={node.selectable ? Boolean(selected) : undefined}
              >
                {node.selectable && item.id ? (
                  <button type="button" className="pane-list-btn" onClick={() => onSelect?.(item.id!)}>
                    {listItemLabel(item)}
                  </button>
                ) : (
                  listItemLabel(item)
                )}
              </li>
            );
          })}
        </ul>
      );
    case "table":
      return (
        <table className="pane-table">
          <thead>
            <tr>
              {(node.columns || []).map((c) => (
                <th key={c.id || c.header}>{c.header || c.id}</th>
              ))}
            </tr>
          </thead>
          <tbody>
            {(node.rows || []).map((row, i) => (
              <tr key={i}>
                {(node.columns || []).map((c) => (
                  <td key={c.id || c.header}>{row.cells?.[c.id || ""] || ""}</td>
                ))}
              </tr>
            ))}
          </tbody>
        </table>
      );
    case "meter": {
      const raw = node.valueFrom ? resolveFrom(feeds, node.valueFrom) : String(node.value ?? "");
      const maxRaw = node.maxFrom ? resolveFrom(feeds, node.maxFrom) : String(node.max ?? "");
      const val = Number(raw) || 0;
      const max = Number(maxRaw) || 0;
      const pct = max > 0 ? Math.min(100, Math.round((val / max) * 100)) : 0;
      return (
        <div className="pane-meter" role="meter" aria-valuenow={val} aria-valuemin={0} aria-valuemax={max || undefined}>
          {node.label ? <span className="muted">{node.label}</span> : null}
          <div className="pane-meter-track">
            <div className={`pane-meter-fill tone-${node.tone || "accent"}`} style={{ width: `${pct}%` }} />
          </div>
          <span className="muted">{max > 0 ? `${val} / ${max}` : String(val)}</span>
        </div>
      );
    }
    case "badge":
      return <span className={`pane-badge tone-${node.tone || "accent"}`}>{node.label || textOf(node, feeds)}</span>;
    case "spacer":
      return <div className="pane-spacer" style={{ height: `${Math.min(8, Math.max(1, node.min || 1)) * 8}px` }} />;
    case "divider":
      return <hr className="pane-divider" />;
    case "empty":
      return (
        <div className="pane-empty">
          <p className="muted">{node.text || "Empty"}</p>
          {node.hint ? <p className="muted">{node.hint}</p> : null}
        </div>
      );
    default:
      return <p className="muted">unsupported node: {type || "?"}</p>;
  }
}
