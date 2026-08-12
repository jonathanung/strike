/**
 * Team workspace: observe-first (WEBUI.14) + safe human controls (WEBUI.19).
 * Mutations go only through public team.* Ops (WEBUI.18) via sendOp.
 */
import { useCallback, useMemo, useRef, useState, type ReactNode } from "react";
import { OpError, type OpResult } from "./api";
import type {
  DelegationTask,
  TeamMember,
  TeamMessage,
  TeamObservation,
  VerificationNote,
} from "./teamModel";
import {
  applyBoardClaim,
  applyBoardComplete,
  applyBoardCreate,
  hasTeamOp,
  newIdempotencyKey,
  teamControlUnavailableReason,
  unavailableMessage,
  type LocalBoardTask,
  type TeamOpName,
} from "./teamControl";
import { ListRow, StatusBadge, StatusDot, statusKindFrom } from "./ui";

export type TeamSendOp = (type: string, data?: unknown) => Promise<OpResult>;

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
  controls,
}: {
  member: TeamMember;
  isLead: boolean;
  onOpenTranscript?: (id: string) => void;
  onClose: () => void;
  verification?: VerificationNote;
  controls?: ReactNode;
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
      {controls}
    </section>
  );
}

function Board({
  delegations,
  members,
  onSelectOwner,
  onTransition,
  canTransition,
  busy,
}: {
  delegations: Record<string, DelegationTask>;
  members: Record<string, TeamMember>;
  onSelectOwner?: (sessionId: string) => void;
  onTransition?: (d: DelegationTask, toState: string) => void;
  canTransition?: boolean;
  busy?: boolean;
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
                  <div className="team-board-card">
                    <button
                      type="button"
                      className="team-board-card-main"
                      onClick={() => owner && onSelectOwner?.(owner)}
                    >
                      <strong>{d.name || d.id.slice(0, 10)}</strong>
                      <span className="muted">{ownerLabel || "unassigned"}</span>
                      {d.reason ? <span className="team-board-reason">{d.reason}</span> : null}
                      {d.version !== undefined ? <small>v{d.version}</small> : null}
                    </button>
                    {canTransition && onTransition && !["done", "completed", "failed", "canceled", "cancelled"].includes(normalizeState(d.state)) ? (
                      <div className="team-board-actions">
                        <button type="button" disabled={busy} onClick={() => onTransition(d, "blocked")}>Block</button>
                        <button type="button" disabled={busy} onClick={() => {
                          if (window.confirm(`Mark ${d.name || d.id} completed?`)) onTransition(d, "completed");
                        }}>Complete</button>
                        <button type="button" disabled={busy} onClick={() => {
                          if (window.confirm(`Cancel ${d.name || d.id}?`)) onTransition(d, "canceled");
                        }}>Cancel</button>
                      </div>
                    ) : null}
                  </div>
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

function formatOpError(err: unknown): string {
  if (err instanceof OpError) {
    if (err.code === "conflict" && err.currentVersion !== undefined) {
      return `Conflict (current v${err.currentVersion}). Refresh and retry.`;
    }
    if (err.code === "idempotency_conflict") return "Duplicate action with different payload.";
    if (err.code === "attach_only") return "Attach-only — mutations disabled.";
    if (err.code === "permission_denied") return "Permission denied.";
    return err.message || err.code || "Operation failed";
  }
  return err instanceof Error ? err.message : String(err);
}

export function TeamWorkspace({
  team,
  selectedId,
  onSelect,
  onOpenTranscript,
  readOnly,
  compact,
  protocolOps,
  teamControl,
  agents,
  rootSessionId,
  sendOp,
}: {
  team: TeamObservation;
  selectedId?: string;
  onSelect: (id: string | undefined) => void;
  onOpenTranscript?: (id: string) => void;
  readOnly?: boolean;
  /** Phone: list-first density */
  compact?: boolean;
  protocolOps?: string[];
  teamControl?: boolean;
  agents?: string[];
  rootSessionId?: string;
  sendOp?: TeamSendOp;
}) {
  const [filter, setFilter] = useState<TeamFilter>("all");
  const [view, setView] = useState<"roster" | "board" | "attention" | "messages" | "controls">("roster");
  const [statusMsg, setStatusMsg] = useState<string>("");
  const [busy, setBusy] = useState(false);
  const [objective, setObjective] = useState("");
  const [spawnAgent, setSpawnAgent] = useState(agents?.[0] || "build");
  const [spawnName, setSpawnName] = useState("");
  const [showAdvancedSpawn, setShowAdvancedSpawn] = useState(false);
  const [maxTurns, setMaxTurns] = useState("");
  const [msgBody, setMsgBody] = useState("");
  const [msgTo, setMsgTo] = useState("");
  const [boardTitle, setBoardTitle] = useState("");
  const [localBoard, setLocalBoard] = useState<LocalBoardTask[]>([]);
  const inflight = useRef(new Set<string>());

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

  const unavailable = teamControlUnavailableReason({
    attachOnly: readOnly,
    teamControl,
    protocolOps,
  });
  const controlsOn = Boolean(sendOp && !unavailable && !readOnly);

  const can = useCallback(
    (op: TeamOpName) => controlsOn && hasTeamOp(protocolOps, op),
    [controlsOn, protocolOps],
  );

  const runOp = useCallback(
    async (logicalKey: string, type: TeamOpName, data: Record<string, unknown>) => {
      if (!sendOp || !can(type)) {
        setStatusMsg(unavailableMessage(unavailable) || "Control unavailable");
        return null;
      }
      if (inflight.current.has(logicalKey)) {
        setStatusMsg("Already in flight — wait for the previous request.");
        return null;
      }
      inflight.current.add(logicalKey);
      setBusy(true);
      setStatusMsg("");
      const idempotencyKey = newIdempotencyKey();
      const payload = {
        ...data,
        idempotencyKey,
        ...(rootSessionId ? { rootSessionId } : {}),
      };
      try {
        const res = await sendOp(type, payload);
        setStatusMsg(res.ok ? "OK" : res.error || "done");
        return res;
      } catch (err) {
        setStatusMsg(formatOpError(err));
        return null;
      } finally {
        inflight.current.delete(logicalKey);
        setBusy(false);
      }
    },
    [sendOp, can, unavailable, rootSessionId],
  );

  const onSpawn = async () => {
    const obj = objective.trim();
    if (!obj) {
      setStatusMsg("Objective is required");
      return;
    }
    const data: Record<string, unknown> = { objective: obj, agent: spawnAgent || undefined };
    if (spawnName.trim()) data.name = spawnName.trim();
    if (showAdvancedSpawn && maxTurns.trim()) {
      const n = Number(maxTurns);
      if (Number.isFinite(n) && n > 0) data.budget = { maxTurns: n };
    }
    const res = await runOp("spawn", "team.spawn", data);
    if (res?.ok) {
      setObjective("");
      setSpawnName("");
      setStatusMsg(res.childSessionId ? `Spawned ${res.childSessionId.slice(0, 8)}` : "Spawned");
    }
  };

  const onMessage = async (broadcast: boolean) => {
    const body = msgBody.trim();
    if (!body) {
      setStatusMsg("Message body is required");
      return;
    }
    if (broadcast) {
      await runOp("broadcast", "team.broadcast", { body });
    } else {
      const to = (msgTo || selectedId || "").trim();
      if (!to) {
        setStatusMsg("Select a target agent");
        return;
      }
      await runOp(`msg-${to}`, "team.message", { to, body, kind: "message" });
    }
    setMsgBody("");
  };

  const onInterrupt = async (childSessionId: string) => {
    const m = team.members[childSessionId];
    const label = m ? memberLabel(m) : childSessionId.slice(0, 8);
    if (!window.confirm(`Interrupt ${label}? Active work will be canceled.`)) return;
    await runOp(`intr-${childSessionId}`, "team.child_interrupt", {
      childSessionId,
      reason: "human interrupt",
    });
  };

  const onTransition = async (d: DelegationTask, toState: string) => {
    const res = await runOp(`tr-${d.id}-${toState}`, "team.task_transition", {
      delegationId: d.id,
      expectedVersion: d.version ?? 0,
      toState,
    });
    if (!res && statusMsg.includes("Conflict")) {
      setStatusMsg((s) => `${s} — reload team snapshot.`);
    }
  };

  const onBoardCreate = async () => {
    const title = boardTitle.trim();
    if (!title) {
      setStatusMsg("Board title is required");
      return;
    }
    const res = await runOp("board-create", "team.board_create", { title });
    if (res?.ok && res.taskId) {
      setLocalBoard((list) => applyBoardCreate(list, res.taskId!, title, res.version || 1));
      setBoardTitle("");
    }
  };

  const onBoardClaim = async (t: LocalBoardTask) => {
    const res = await runOp(`claim-${t.id}`, "team.board_claim", {
      taskId: t.id,
      expectedVersion: t.version,
    });
    if (res?.ok) {
      setLocalBoard((list) => applyBoardClaim(list, t.id, res.version || t.version + 1));
    }
  };

  const onBoardComplete = async (t: LocalBoardTask) => {
    if (!window.confirm(`Complete board task ${t.title}?`)) return;
    const res = await runOp(`complete-${t.id}`, "team.board_complete", {
      taskId: t.id,
      expectedVersion: t.version,
    });
    if (res?.ok) {
      setLocalBoard((list) => applyBoardComplete(list, t.id, res.version || t.version + 1));
    }
  };

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
  const tabs = (["roster", "board", "messages", ...(controlsOn || unavailable ? ["controls"] as const : [])] as const);

  const memberControls = selected && !selected.terminal && selected.sessionId !== team.leadId && can("team.child_interrupt") ? (
    <div className="team-detail-actions team-controls-inline" aria-label="Agent controls">
      <button type="button" disabled={busy} onClick={() => void onInterrupt(selected.sessionId)}>
        Interrupt
      </button>
      {can("team.message") ? (
        <button
          type="button"
          disabled={busy}
          onClick={() => {
            setMsgTo(selected.sessionId);
            setView("controls");
          }}
        >
          Message
        </button>
      ) : null}
    </div>
  ) : null;

  return (
    <section className={`team-workspace ${compact ? "compact" : ""}`} aria-label="Team workspace">
      <header className="team-workspace-head">
        <h2>Team</h2>
        {readOnly ? <StatusBadge kind="idle" label="read-only" /> : null}
        {controlsOn ? <StatusBadge kind="busy" label="controls" /> : null}
      </header>

      {statusMsg ? (
        <p className="team-control-status" role="status">{statusMsg}</p>
      ) : null}

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
        {tabs.map((v) => (
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
          canTransition={can("team.task_transition")}
          busy={busy}
          onTransition={(d, to) => void onTransition(d, to)}
          onSelectOwner={(id) => {
            onSelect(id);
            setView("roster");
          }}
        />
      )}

      {view === "messages" && <Messages messages={team.messages} />}

      {view === "controls" && (
        <section className="team-controls" aria-label="Team controls">
          {unavailable ? (
            <p className="muted" role="status">{unavailableMessage(unavailable)}</p>
          ) : (
            <>
              {can("team.spawn") && (
                <fieldset className="team-control-block">
                  <legend>Spawn agent</legend>
                  <label>
                    Objective
                    <textarea
                      value={objective}
                      onChange={(e) => setObjective(e.target.value)}
                      rows={compact ? 2 : 3}
                      placeholder="What should the child do?"
                      disabled={busy}
                    />
                  </label>
                  <label>
                    Agent
                    <select value={spawnAgent} onChange={(e) => setSpawnAgent(e.target.value)} disabled={busy}>
                      {(agents?.length ? agents : ["build", "explore", "general"]).map((a) => (
                        <option key={a} value={a}>{a}</option>
                      ))}
                    </select>
                  </label>
                  <button type="button" className="linkish" onClick={() => setShowAdvancedSpawn((v) => !v)}>
                    {showAdvancedSpawn ? "Hide advanced" : "Advanced…"}
                  </button>
                  {showAdvancedSpawn && (
                    <div className="team-control-advanced">
                      <label>
                        Alias
                        <input value={spawnName} onChange={(e) => setSpawnName(e.target.value)} disabled={busy} />
                      </label>
                      <label>
                        Max turns
                        <input value={maxTurns} onChange={(e) => setMaxTurns(e.target.value)} inputMode="numeric" disabled={busy} />
                      </label>
                    </div>
                  )}
                  <button type="button" className="primary" disabled={busy || !objective.trim()} onClick={() => void onSpawn()}>
                    Spawn
                  </button>
                </fieldset>
              )}

              {(can("team.message") || can("team.broadcast")) && (
                <fieldset className="team-control-block">
                  <legend>Message</legend>
                  <label>
                    To (session id)
                    <input
                      value={msgTo}
                      onChange={(e) => setMsgTo(e.target.value)}
                      placeholder={selectedId || "child session id"}
                      disabled={busy}
                      list="team-member-ids"
                    />
                    <datalist id="team-member-ids">
                      {members.map((m) => (
                        <option key={m.sessionId} value={m.sessionId}>{memberLabel(m)}</option>
                      ))}
                    </datalist>
                  </label>
                  <label>
                    Body
                    <textarea value={msgBody} onChange={(e) => setMsgBody(e.target.value)} rows={2} disabled={busy} />
                  </label>
                  <div className="team-control-row">
                    {can("team.message") ? (
                      <button type="button" disabled={busy || !msgBody.trim()} onClick={() => void onMessage(false)}>
                        Send
                      </button>
                    ) : null}
                    {can("team.broadcast") ? (
                      <button type="button" disabled={busy || !msgBody.trim()} onClick={() => void onMessage(true)}>
                        Broadcast
                      </button>
                    ) : null}
                  </div>
                </fieldset>
              )}

              {can("team.board_create") && (
                <fieldset className="team-control-block">
                  <legend>Board task</legend>
                  <label>
                    Title
                    <input value={boardTitle} onChange={(e) => setBoardTitle(e.target.value)} disabled={busy} />
                  </label>
                  <button type="button" disabled={busy || !boardTitle.trim()} onClick={() => void onBoardCreate()}>
                    Create
                  </button>
                  {localBoard.length > 0 && (
                    <ul className="team-local-board" aria-label="Local board tasks">
                      {localBoard.map((t) => (
                        <li key={t.id}>
                          <strong>{t.title}</strong>
                          <span className="muted">{t.status} · v{t.version} · {t.id}</span>
                          <div className="team-control-row">
                            {can("team.board_claim") && t.status === "pending" ? (
                              <button type="button" disabled={busy} onClick={() => void onBoardClaim(t)}>Claim</button>
                            ) : null}
                            {can("team.board_complete") && t.status !== "completed" ? (
                              <button type="button" disabled={busy} onClick={() => void onBoardComplete(t)}>Complete</button>
                            ) : null}
                          </div>
                        </li>
                      ))}
                    </ul>
                  )}
                </fieldset>
              )}

              <p className="muted">
                Root <code>{rootSessionId || team.leadId || "—"}</code>. Outcomes apply via live events; no second client store.
              </p>
            </>
          )}
        </section>
      )}

      {selected && (
        <MemberDetail
          member={selected}
          isLead={selected.sessionId === team.leadId}
          onClose={() => onSelect(undefined)}
          onOpenTranscript={onOpenTranscript}
          verification={verification}
          controls={memberControls}
        />
      )}
    </section>
  );
}
