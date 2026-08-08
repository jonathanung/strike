/**
 * Observe-first Team workspace (WEBUI.14 / #1083).
 * Driven by WEBUI.13 team observation state — no human mutations.
 */
import { useMemo, useState } from "react";
import type {
  DelegationTask,
  TeamMember,
  TeamMessage,
  TeamObservation,
  VerificationNote,
} from "./team";
import { ListRow, StatusBadge, StatusDot, statusKindFrom } from "./ui";

export type TeamFilter = "all" | "working" | "needs-you" | "blocked" | "completed" | "failed" | "hidden";

const FILTERS: { id: TeamFilter; label: string }[] = [
  { id: "all", label: "All" },
  { id: "working", label: "Working" },
  { id: "needs-you", label: "Needs you" },
  { id: "blocked", label: "Blocked" },
  { id: "completed", label: "Completed" },
  { id: "failed", label: "Failed" },
  { id: "hidden", label: "Terminal" },
];

const BOARD_COLUMNS = ["queued", "starting", "working", "blocked", "needs_attention", "completed", "failed", "canceled"] as const;

function memberLabel(m: TeamMember): string {
  return m.name || m.agent || m.sessionId.slice(0, 8);
}

function normalizeState(state?: string): string {
  return (state || "unknown").toLowerCase().replace(/-/g, "_");
}

function matchesFilter(m: TeamMember, filter: TeamFilter, leadId?: string): boolean {
  const s = normalizeState(m.state);
  const terminal = Boolean(m.terminal) || ["completed", "failed", "canceled", "cancelled"].includes(s);
  switch (filter) {
    case "all":
      return !terminal;
    case "working":
      return ["running", "working", "starting", "finalizing"].includes(s);
    case "needs-you":
      return s === "needs_attention" || s === "escalating" || Boolean(m.blockReason && s.includes("need"));
    case "blocked":
      return s === "blocked" || Boolean(m.blockReason);
    case "completed":
      return s === "completed";
    case "failed":
      return s === "failed" || s === "interrupted";
    case "hidden":
      return terminal;
    default:
      return true;
  }
}

/** Stable roster order: lead first, then non-terminal by name, then terminal. */
export function orderedMembers(team: TeamObservation): TeamMember[] {
  const list = Object.values(team.members);
  const lead = team.leadId;
  return list.sort((a, b) => {
    if (lead && a.sessionId === lead) return -1;
    if (lead && b.sessionId === lead) return 1;
    const at = Boolean(a.terminal);
    const bt = Boolean(b.terminal);
    if (at !== bt) return at ? 1 : -1;
    return memberLabel(a).localeCompare(memberLabel(b));
  });
}

export type TeamAttentionItem = {
  id: string;
  kind: "escalation" | "blocked" | "verification" | "conflict" | "message" | "needs-you";
  label: string;
  detail?: string;
  sessionId?: string;
};

export function teamAttentionItems(team: TeamObservation): TeamAttentionItem[] {
  const items: TeamAttentionItem[] = [];
  for (const m of Object.values(team.members)) {
    const s = normalizeState(m.state);
    if (s === "escalating" || s === "needs_attention") {
      items.push({
        id: `esc-${m.sessionId}`,
        kind: "escalation",
        label: `${memberLabel(m)} needs attention`,
        detail: m.blockReason || m.lastAction,
        sessionId: m.sessionId,
      });
    }
    if (s === "blocked" || m.blockReason) {
      items.push({
        id: `blk-${m.sessionId}`,
        kind: "blocked",
        label: `${memberLabel(m)} blocked`,
        detail: m.blockReason,
        sessionId: m.sessionId,
      });
    }
  }
  for (const v of team.verifications) {
    if (v.passed === false) {
      items.push({
        id: `ver-${v.sessionId || items.length}`,
        kind: "verification",
        label: "Verification failed",
        detail: v.summary,
        sessionId: v.sessionId,
      });
    }
  }
  for (const o of team.pathOverlaps) {
    items.push({
      id: `ov-${o.path}`,
      kind: "conflict",
      label: `Path overlap: ${o.path}`,
      detail: o.sessions?.join(", "),
    });
  }
  for (const msg of team.messages) {
    if (msg.urgency === "high" || msg.urgency === "blocker" || msg.kind === "escalation") {
      items.push({
        id: `msg-${msg.messageId || msg.time || items.length}`,
        kind: "message",
        label: msg.summary || msg.body || "Urgent team message",
        detail: msg.from ? `from ${msg.from}` : undefined,
        sessionId: msg.to,
      });
    }
  }
  return items.slice(0, 40);
}

function budgetLabel(m: TeamMember): string {
  const b = m.budget;
  if (!b || typeof b !== "object") return "unknown";
  const parts: string[] = [];
  for (const key of ["costUSD", "tokens", "turns", "maxCostUSD", "maxTokens", "stopReason"]) {
    if (b[key] !== undefined && b[key] !== null && b[key] !== "") parts.push(`${key}=${String(b[key])}`);
  }
  return parts.length ? parts.join(" · ") : "unknown";
}

function MemberDetail({
  member,
  isLead,
  onOpenTranscript,
  onClose,
  verification,
}: {
  member: TeamMember;
  isLead: boolean;
  onOpenTranscript?: (id: string) => void;
  onClose: () => void;
  verification?: VerificationNote;
}) {
  const kind = statusKindFrom(member.state);
  return (
    <section className="team-detail" aria-label="Agent detail">
      <header>
        <h3>{memberLabel(member)}{isLead ? " (lead)" : ""}</h3>
        <button type="button" className="icon-btn" aria-label="Close agent detail" onClick={onClose}>×</button>
      </header>
      <dl>
        <dt>Status</dt>
        <dd><StatusBadge kind={kind} label={member.state || kind} /></dd>
        {member.agent ? <><dt>Agent</dt><dd>{member.agent}</dd></> : null}
        {member.objective ? <><dt>Objective</dt><dd>{member.objective}</dd></> : null}
        {member.lastAction ? <><dt>Latest action</dt><dd>{member.lastAction}</dd></> : null}
        {member.queueLabel ? <><dt>Queue</dt><dd>{member.queueLabel}</dd></> : null}
        {member.blockReason ? <><dt>Blocked</dt><dd>{member.blockReason}</dd></> : null}
        {member.terminalSummary ? <><dt>Summary</dt><dd>{member.terminalSummary}</dd></> : null}
        <dt>Budget / stop</dt>
        <dd>{budgetLabel(member)}</dd>
        {verification ? (
          <>
            <dt>Verification</dt>
            <dd>
              {verification.passed === true ? "passed" : verification.passed === false ? "failed" : "unknown"}
              {verification.summary ? ` — ${verification.summary}` : ""}
            </dd>
          </>
        ) : (
          <><dt>Verification</dt><dd>unknown</dd></>
        )}
        {member.filesTouched?.length ? (
          <><dt>Files</dt><dd><ul className="team-files">{member.filesTouched.map((f) => <li key={f}><code>{f}</code></li>)}</ul></dd></>
        ) : null}
        <dt>Session</dt>
        <dd><code>{member.sessionId}</code></dd>
      </dl>
      {onOpenTranscript && (
        <div className="team-detail-actions">
          <button type="button" onClick={() => onOpenTranscript(member.sessionId)}>Open transcript (RO)</button>
        </div>
      )}
      <p className="muted">Observe-only — human controls require WEBUI.17–19.</p>
    </section>
  );
}

function Board({
  delegations,
  members,
  onSelectOwner,
}: {
  delegations: Record<string, DelegationTask>;
  members: Record<string, TeamMember>;
  onSelectOwner?: (sessionId: string) => void;
}) {
  const byCol = useMemo(() => {
    const map = new Map<string, DelegationTask[]>();
    for (const col of BOARD_COLUMNS) map.set(col, []);
    map.set("other", []);
    for (const d of Object.values(delegations)) {
      const s = normalizeState(d.state);
      const col = BOARD_COLUMNS.includes(s as typeof BOARD_COLUMNS[number]) ? s : "other";
      map.get(col)!.push(d);
    }
    return map;
  }, [delegations]);

  const nonEmpty = [...byCol.entries()].filter(([, rows]) => rows.length > 0);
  if (!nonEmpty.length) {
    return <p className="muted">No delegation / team-task rows yet.</p>;
  }

  return (
    <div className="team-board" aria-label="Task board">
      {nonEmpty.map(([col, rows]) => (
        <section key={col} className="team-board-col" aria-label={`${col} column`}>
          <h4>{col.replace(/_/g, " ")} <span className="muted">{rows.length}</span></h4>
          <ul>
            {rows.map((d) => {
              const owner = d.ownerSessionId || d.sessionId;
              const ownerLabel = owner && members[owner] ? memberLabel(members[owner]) : owner?.slice(0, 8);
              return (
                <li key={d.id}>
                  <button
                    type="button"
                    className="team-board-card"
                    onClick={() => owner && onSelectOwner?.(owner)}
                  >
                    <strong>{d.name || d.id.slice(0, 10)}</strong>
                    <span className="muted">{ownerLabel || "unassigned"}</span>
                    {d.reason ? <span className="team-board-reason">{d.reason}</span> : null}
                    {d.version !== undefined ? <small>v{d.version}</small> : null}
                  </button>
                </li>
              );
            })}
          </ul>
        </section>
      ))}
    </div>
  );
}

function Messages({ messages }: { messages: TeamMessage[] }) {
  if (!messages.length) return <p className="muted">No team messages yet.</p>;
  return (
    <ul className="team-messages" aria-label="Recent team messages">
      {[...messages].reverse().slice(0, 20).map((m, i) => (
        <li key={m.messageId || `${m.time}-${i}`}>
          <strong>{m.from || "?"} → {m.to || "*"}</strong>
          {m.urgency && m.urgency !== "normal" ? <StatusBadge kind="needs-you" label={m.urgency} /> : null}
          <span>{m.summary || m.body || "(empty)"}</span>
        </li>
      ))}
    </ul>
  );
}

export function TeamWorkspace({
  team,
  selectedId,
  onSelect,
  onOpenTranscript,
  readOnly,
  compact,
}: {
  team: TeamObservation;
  selectedId?: string;
  onSelect: (id: string | undefined) => void;
  onOpenTranscript?: (id: string) => void;
  readOnly?: boolean;
  /** Phone: list-first density */
  compact?: boolean;
}) {
  const [filter, setFilter] = useState<TeamFilter>("all");
  const [view, setView] = useState<"roster" | "board" | "attention" | "messages">("roster");
  const members = useMemo(() => orderedMembers(team), [team]);
  const filtered = useMemo(
    () => members.filter((m) => matchesFilter(m, filter, team.leadId)),
    [members, filter, team.leadId],
  );
  const attention = useMemo(() => teamAttentionItems(team), [team]);
  const selected = selectedId ? team.members[selectedId] : undefined;
  const verification = selected
    ? [...team.verifications].reverse().find((v) => v.sessionId === selected.sessionId)
    : undefined;

  if (!team.available) {
    return (
      <section className="team-workspace" aria-label="Team workspace">
        <h2>Team</h2>
        <p className="muted" role="status">{team.unavailableReason || "Team observation unavailable"}</p>
        {readOnly ? <p className="muted">Read-only — historical / attach-only best replayable state when present.</p> : null}
      </section>
    );
  }

  const hasActivity = members.length > 0 || Object.keys(team.delegations).length > 0;

  return (
    <section className={`team-workspace ${compact ? "compact" : ""}`} aria-label="Team workspace">
      <header className="team-workspace-head">
        <h2>Team</h2>
        {readOnly ? <StatusBadge kind="idle" label="read-only" /> : null}
      </header>

      {attention.length > 0 && (
        <div className="team-attention" aria-label="Team attention">
          <button type="button" className="team-attention-entry" onClick={() => setView("attention")}>
            Attention · {attention.length}
          </button>
          {view === "attention" && (
            <ul>
              {attention.map((a) => (
                <li key={a.id}>
                  <button
                    type="button"
                    onClick={() => {
                      if (a.sessionId) onSelect(a.sessionId);
                    }}
                  >
                    <StatusDot kind={a.kind === "verification" || a.kind === "conflict" ? "failed" : "needs-you"} label={a.kind} />
                    <span>
                      <strong>{a.label}</strong>
                      {a.detail ? <small>{a.detail}</small> : null}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}

      <div className="team-view-tabs" role="tablist" aria-label="Team views">
        {(["roster", "board", "messages"] as const).map((v) => (
          <button
            key={v}
            type="button"
            role="tab"
            aria-selected={view === v}
            className={view === v ? "active" : ""}
            onClick={() => setView(v)}
          >
            {v}
          </button>
        ))}
      </div>

      {!hasActivity ? (
        <p className="muted">No child or team activity yet. Team stays available for explicit navigation.</p>
      ) : null}

      {view === "roster" && (
        <>
          <div className="team-filters" role="toolbar" aria-label="Roster filters">
            {FILTERS.map((f) => (
              <button
                key={f.id}
                type="button"
                aria-pressed={filter === f.id}
                className={filter === f.id ? "active" : ""}
                onClick={() => setFilter(f.id)}
              >
                {f.label}
              </button>
            ))}
          </div>
          <ul className="team-roster" role="list" aria-label="Agent roster">
            {filtered.map((m) => {
              const active = m.sessionId === selectedId;
              const isLead = m.sessionId === team.leadId || m.role === "lead";
              const kind = statusKindFrom(m.state);
              return (
                <li key={m.sessionId}>
                  <ListRow
                    active={active}
                    onClick={() => onSelect(active ? undefined : m.sessionId)}
                    leading={<StatusDot kind={kind} label={m.state || kind} />}
                    title={`${memberLabel(m)}${isLead ? " · lead" : ""}`}
                    meta={[m.state, m.lastAction || m.queueLabel || m.blockReason, m.filesTouched?.length ? `${m.filesTouched.length} files` : ""]
                      .filter(Boolean)
                      .join(" · ")}
                  />
                </li>
              );
            })}
          </ul>
          {!filtered.length && hasActivity ? <p className="muted">No agents match this filter.</p> : null}
        </>
      )}

      {view === "board" && (
        <Board
          delegations={team.delegations}
          members={team.members}
          onSelectOwner={(id) => {
            onSelect(id);
            setView("roster");
          }}
        />
      )}

      {view === "messages" && <Messages messages={team.messages} />}

      {selected && (
        <MemberDetail
          member={selected}
          isLead={selected.sessionId === team.leadId}
          onClose={() => onSelect(undefined)}
          onOpenTranscript={onOpenTranscript}
          verification={verification}
        />
      )}
    </section>
  );
}
