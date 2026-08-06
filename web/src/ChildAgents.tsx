import type { ChildAgent } from "./types";

// WEB.11 (#937): list + handoff detail only. Full multi-agent graph / team board
// visualizer remains #523 — do not grow this panel into a visualizer.

export type ChildEntry = [string, ChildAgent];

function label(id: string, child: ChildAgent) {
  return child.name || child.agent || id.slice(0, 8);
}

function qualityClass(quality?: string) {
  switch (quality) {
    case "complete": return "quality complete";
    case "partial": return "quality partial";
    case "unavailable": return "quality unavailable";
    default: return "quality";
  }
}

export function ChildAgentsPanel({
  children,
  selectedId,
  onSelect,
  onOpenTranscript,
}: {
  children: ChildEntry[];
  selectedId?: string;
  onSelect: (id: string | undefined) => void;
  onOpenTranscript?: (id: string) => void;
}) {
  const selected = selectedId ? children.find(([id]) => id === selectedId) : undefined;
  // Density contract: hide empty chrome (main App.test + progressive disclosure).
  if (!children.length) return null;
  return (
    <div className="child-agents" aria-label="Child agents">
      <div className="aside-heading">CHILD AGENTS</div>
      <div className="children" role="list">
        {children.map(([id, child]) => {
          const active = id === selectedId;
          return (
            <button
              type="button"
              role="listitem"
              key={id}
              className={active ? "child-row active" : "child-row"}
              aria-pressed={active}
              aria-label={`Child ${label(id, child)}`}
              onClick={() => onSelect(active ? undefined : id)}
            >
              <span className={`child-state ${child.status}`} aria-hidden />
              <span className="child-label">{label(id, child)}</span>
              <span className="child-meta">
                <small>{child.status}</small>
                {child.quality ? <small className={qualityClass(child.quality)}>{child.quality}</small> : null}
              </span>
            </button>
          );
        })}
      </div>
      {selected && (
        <ChildDetail
          id={selected[0]}
          child={selected[1]}
          onClose={() => onSelect(undefined)}
          onOpenTranscript={onOpenTranscript}
        />
      )}
    </div>
  );
}

export function ChildDetail({
  id,
  child,
  onClose,
  onOpenTranscript,
}: {
  id: string;
  child: ChildAgent;
  onClose: () => void;
  onOpenTranscript?: (id: string) => void;
}) {
  const budgetStop = child.budgetKind
    ? [child.budgetKind, child.finalization && child.finalization !== "none" ? `finalization ${child.finalization}` : ""].filter(Boolean).join(" · ")
    : child.escalateReason || undefined;
  return (
    <section className="child-detail" aria-label="Child handoff detail">
      <header>
        <h3>{label(id, child)}</h3>
        <button type="button" className="icon-button" aria-label="Close child detail" onClick={onClose}>×</button>
      </header>
      <dl>
        <dt>Status</dt>
        <dd>{child.status}</dd>
        {child.quality ? <><dt>Handoff quality</dt><dd><span className={qualityClass(child.quality)}>{child.quality}</span></dd></> : null}
        {child.summary ? <><dt>Summary</dt><dd>{child.summary}</dd></> : null}
        {budgetStop ? <><dt>Budget / stop</dt><dd>{budgetStop}</dd></> : null}
        {child.escalateAction && !child.budgetKind ? <><dt>Escalation</dt><dd>{child.escalateAction}{child.escalateKind ? ` (${child.escalateKind})` : ""}</dd></> : null}
        {child.prompt ? <><dt>Prompt</dt><dd className="child-prompt">{child.prompt}</dd></> : null}
        <dt>Session</dt>
        <dd><code>{id}</code></dd>
      </dl>
      {onOpenTranscript && (
        <div className="child-detail-actions">
          <button type="button" onClick={() => onOpenTranscript(id)}>Open transcript (RO)</button>
        </div>
      )}
      <p className="muted child-detail-note">List/detail only — full agent visualizer is tracked in #523.</p>
    </section>
  );
}
