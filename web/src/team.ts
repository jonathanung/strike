/**
 * Observe-only multi-agent team projection (WEBUI.13 / #1081).
 * Consumes pkg/protocol envelope shapes; no browser-only event bus.
 */

export type TeamMember = {
  sessionId: string;
  name?: string;
  agent?: string;
  state: string;
  role?: string;
  parentSessionId?: string;
  depth?: number;
  objective?: string;
  lastAction?: string;
  blockReason?: string;
  terminalSummary?: string;
  queueLabel?: string;
  filesTouched?: string[];
  budget?: Record<string, unknown>;
  /** True once a terminal state was observed; terminal agents are not revived. */
  terminal?: boolean;
};

export type DelegationTask = {
  id: string;
  state: string;
  prev?: string;
  version?: number;
  sessionId?: string;
  name?: string;
  reason?: string;
  ownerSessionId?: string;
};

export type TeamMessage = {
  messageId?: string;
  from?: string;
  to?: string;
  body?: string;
  summary?: string;
  teamId?: string;
  taskId?: string;
  urgency?: string;
  kind?: string;
  time?: string;
};

export type PathOverlapNote = {
  path: string;
  sessions?: string[];
  time?: string;
};

export type ArtifactNote = {
  id: string;
  type?: string;
  version?: number;
  title?: string;
  op?: string;
  scope?: string;
};

export type LedgerNote = {
  id: string;
  kind?: string;
  status?: string;
  op?: string;
  statement?: string;
};

export type VerificationNote = {
  sessionId?: string;
  passed?: boolean;
  summary?: string;
  claimed?: boolean;
  verified?: boolean;
};

export type TeamObservation = {
  leadId?: string;
  members: Record<string, TeamMember>;
  delegations: Record<string, DelegationTask>;
  messages: TeamMessage[];
  pathOverlaps: PathOverlapNote[];
  artifacts: Record<string, ArtifactNote>;
  ledger: Record<string, LedgerNote>;
  verifications: VerificationNote[];
  /** Capability / availability for UI empty states. */
  available: boolean;
  unavailableReason?: string;
};

export const emptyTeam = (): TeamObservation => ({
  members: {},
  delegations: {},
  messages: [],
  pathOverlaps: [],
  artifacts: {},
  ledger: {},
  verifications: [],
  available: true,
});

const TERMINAL = new Set(["completed", "failed", "canceled", "cancelled", "blocked"]);

const asRecord = (v: unknown): Record<string, unknown> | undefined =>
  v && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;

const str = (d: Record<string, unknown> | undefined, key: string): string | undefined => {
  const v = d?.[key];
  if (v === undefined || v === null) return undefined;
  const s = String(v).trim();
  return s || undefined;
};

const isTerminalState = (state?: string) => TERMINAL.has((state || "").toLowerCase());

/** Merge roster member without reviving a terminal agent to a live state. */
export function mergeMember(prev: TeamMember | undefined, next: Partial<TeamMember> & { sessionId: string }): TeamMember {
  const wasTerminal = Boolean(prev?.terminal || isTerminalState(prev?.state));
  let state = next.state || prev?.state || "unknown";
  let terminal = wasTerminal || isTerminalState(state);
  if (wasTerminal && next.state && !isTerminalState(next.state)) {
    // Do not revive terminal agents from stale/late roster rows.
    state = prev?.state || state;
    terminal = true;
  }
  return {
    sessionId: next.sessionId,
    name: next.name ?? prev?.name,
    agent: next.agent ?? prev?.agent,
    state,
    role: next.role ?? prev?.role,
    parentSessionId: next.parentSessionId ?? prev?.parentSessionId,
    depth: next.depth ?? prev?.depth,
    objective: next.objective ?? prev?.objective,
    lastAction: next.lastAction ?? prev?.lastAction,
    blockReason: next.blockReason ?? prev?.blockReason,
    terminalSummary: next.terminalSummary ?? prev?.terminalSummary,
    queueLabel: next.queueLabel ?? prev?.queueLabel,
    filesTouched: next.filesTouched ?? prev?.filesTouched,
    budget: next.budget ?? prev?.budget,
    terminal,
  };
}

const MAX_MESSAGES = 100;
const MAX_OVERLAPS = 50;
const MAX_VERIFICATIONS = 40;

/**
 * Project one protocol envelope into team observation state.
 * Unknown types are ignored safely.
 */
export function reduceTeamEvent(team: TeamObservation, type: string, data: Record<string, unknown> = {}, time?: string): TeamObservation {
  switch (type) {
    case "team.roster": {
      const leadId = str(data, "leadId") || team.leadId;
      const members = { ...team.members };
      const list = Array.isArray(data.members) ? data.members : [];
      for (const raw of list) {
        const row = asRecord(raw);
        if (!row) continue;
        const sessionId = str(row, "sessionId");
        if (!sessionId) continue;
        const budget = asRecord(row.budget);
        members[sessionId] = mergeMember(members[sessionId], {
          sessionId,
          name: str(row, "name"),
          agent: str(row, "agent"),
          state: str(row, "state") || "unknown",
          role: str(row, "role"),
          parentSessionId: str(row, "parentSessionId"),
          depth: typeof row.depth === "number" ? row.depth : undefined,
          objective: str(row, "objective"),
          lastAction: str(row, "lastAction"),
          blockReason: str(row, "blockReason"),
          terminalSummary: str(row, "terminalSummary"),
          queueLabel: str(row, "queueLabel"),
          filesTouched: Array.isArray(row.filesTouched) ? row.filesTouched.map(String) : undefined,
          budget: budget,
        });
      }
      return { ...team, leadId, members, available: true, unavailableReason: undefined };
    }
    case "child.started": {
      const sessionId = str(data, "sessionId");
      if (!sessionId) return team;
      const members = { ...team.members };
      members[sessionId] = mergeMember(members[sessionId], {
        sessionId,
        name: str(data, "name"),
        agent: str(data, "agent"),
        state: "running",
        objective: str(data, "prompt"),
      });
      return { ...team, members, available: true };
    }
    case "child.completed": {
      const sessionId = str(data, "sessionId");
      if (!sessionId) return team;
      const members = { ...team.members };
      const status = str(data, "status") || "completed";
      const handoff = asRecord(data.handoff);
      members[sessionId] = mergeMember(members[sessionId], {
        sessionId,
        name: str(data, "name"),
        state: status,
        terminalSummary: str(data, "summary") || str(handoff, "summary"),
        terminal: true,
      });
      let verifications = team.verifications;
      const verification = asRecord(data.verification);
      if (verification) {
        verifications = [
          ...team.verifications,
          {
            sessionId,
            passed: Boolean(verification.passed),
            claimed: Boolean(verification.claimed),
            verified: Boolean(verification.verified),
            summary: str(verification, "summary"),
          },
        ].slice(-MAX_VERIFICATIONS);
      }
      return { ...team, members, verifications, available: true };
    }
    case "child.escalated": {
      const sessionId = str(data, "sessionId");
      if (!sessionId) return team;
      const members = { ...team.members };
      const action = str(data, "action");
      const state =
        action === "finalizing" ? "finalizing" : action === "interrupted" ? "interrupted" : "escalating";
      members[sessionId] = mergeMember(members[sessionId], {
        sessionId,
        name: str(data, "name"),
        state,
        blockReason: str(data, "reason"),
      });
      return { ...team, members };
    }
    case "delegation.changed": {
      const id = str(data, "id");
      if (!id) return team;
      const delegations = { ...team.delegations };
      delegations[id] = {
        id,
        state: str(data, "state") || "unknown",
        prev: str(data, "prev"),
        version: typeof data.version === "number" ? data.version : undefined,
        sessionId: str(data, "sessionId"),
        name: str(data, "name"),
        reason: str(data, "reason"),
        ownerSessionId: str(data, "ownerSessionId"),
      };
      return { ...team, delegations, available: true };
    }
    case "agent.message": {
      const msg: TeamMessage = {
        messageId: str(data, "messageId"),
        from: str(data, "from"),
        to: str(data, "to"),
        body: str(data, "body"),
        summary: str(data, "summary"),
        teamId: str(data, "teamId"),
        taskId: str(data, "taskId"),
        urgency: str(data, "urgency"),
        kind: str(data, "kind"),
        time,
      };
      const messages = [...team.messages, msg].slice(-MAX_MESSAGES);
      return { ...team, messages };
    }
    case "path.overlap": {
      const path = str(data, "path");
      if (!path) return team;
      const sessions = Array.isArray(data.sessions)
        ? data.sessions.map(String)
        : Array.isArray(data.sessionIds)
          ? data.sessionIds.map(String)
          : undefined;
      const pathOverlaps = [...team.pathOverlaps, { path, sessions, time }].slice(-MAX_OVERLAPS);
      return { ...team, pathOverlaps };
    }
    case "artifact.updated": {
      const id = str(data, "id");
      if (!id) return team;
      const artifacts = { ...team.artifacts };
      artifacts[id] = {
        id,
        type: str(data, "type"),
        version: typeof data.version === "number" ? data.version : undefined,
        title: str(data, "title"),
        op: str(data, "op"),
        scope: str(data, "scope"),
      };
      return { ...team, artifacts };
    }
    case "ledger.updated": {
      const id = str(data, "id");
      if (!id) return team;
      const ledger = { ...team.ledger };
      ledger[id] = {
        id,
        kind: str(data, "kind"),
        status: str(data, "status"),
        op: str(data, "op"),
        statement: str(data, "statement"),
      };
      return { ...team, ledger };
    }
    case "team.unavailable": {
      return {
        ...team,
        available: false,
        unavailableReason: str(data, "reason") || "Team observation unavailable",
      };
    }
    case "workspace.reset":
      return emptyTeam();
    default:
      return team;
  }
}

/** Apply a server snapshot (late join / reload). Does not claim missing fields. */
export function applyTeamSnapshot(prev: TeamObservation, snap: Partial<TeamObservation> | null | undefined): TeamObservation {
  if (!snap) {
    return {
      ...prev,
      available: false,
      unavailableReason: prev.unavailableReason || "Team snapshot unavailable",
    };
  }
  if (snap.available === false) {
    return {
      ...emptyTeam(),
      available: false,
      unavailableReason: snap.unavailableReason || "Team dissolved or unavailable",
    };
  }
  const base = emptyTeam();
  const members: Record<string, TeamMember> = {};
  for (const [id, m] of Object.entries(snap.members || {})) {
    if (!m?.sessionId) continue;
    members[id] = mergeMember(undefined, m);
  }
  return {
    ...base,
    leadId: snap.leadId || prev.leadId,
    members: Object.keys(members).length ? members : prev.members,
    delegations: snap.delegations || prev.delegations,
    messages: Array.isArray(snap.messages) ? snap.messages.slice(-MAX_MESSAGES) : prev.messages,
    pathOverlaps: Array.isArray(snap.pathOverlaps) ? snap.pathOverlaps.slice(-MAX_OVERLAPS) : prev.pathOverlaps,
    artifacts: snap.artifacts || prev.artifacts,
    ledger: snap.ledger || prev.ledger,
    verifications: Array.isArray(snap.verifications) ? snap.verifications.slice(-MAX_VERIFICATIONS) : prev.verifications,
    available: true,
    unavailableReason: undefined,
  };
}
