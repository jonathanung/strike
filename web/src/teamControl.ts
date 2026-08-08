/**
 * Human team-control Op helpers (WEBUI.19 / #1089).
 * Builds protocol envelopes for WEBUI.18 Ops only — no internal tool names.
 */

export const TEAM_OPS = [
  "team.spawn",
  "team.message",
  "team.broadcast",
  "team.child_interrupt",
  "team.task_transition",
  "team.board_create",
  "team.board_claim",
  "team.board_complete",
] as const;

export type TeamOpName = (typeof TEAM_OPS)[number];

/** Client-generated idempotency key (UUID-ish). */
export function newIdempotencyKey(): string {
  if (typeof crypto !== "undefined" && "randomUUID" in crypto) {
    return crypto.randomUUID();
  }
  return `tc-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

export function hasTeamOp(protocolOps: string[] | undefined, name: TeamOpName): boolean {
  return Boolean(protocolOps?.includes(name));
}

export function teamControlEnabled(
  protocolOps: string[] | undefined,
  caps?: { teamControl?: boolean; team?: boolean },
  attachOnly?: boolean,
): boolean {
  if (attachOnly) return false;
  if (caps && caps.teamControl === false) return false;
  return TEAM_OPS.some((op) => hasTeamOp(protocolOps, op));
}

export type TeamControlUnavailableReason =
  | "attach-only"
  | "no-capability"
  | "no-ops"
  | undefined;

export function teamControlUnavailableReason(opts: {
  attachOnly?: boolean;
  teamControl?: boolean;
  protocolOps?: string[];
}): TeamControlUnavailableReason {
  if (opts.attachOnly) return "attach-only";
  if (opts.teamControl === false) return "no-capability";
  if (!TEAM_OPS.some((op) => hasTeamOp(opts.protocolOps, op))) return "no-ops";
  return undefined;
}

export function unavailableMessage(reason: TeamControlUnavailableReason): string {
  switch (reason) {
    case "attach-only":
      return "Attach-only / historical session — team controls are disabled.";
    case "no-capability":
      return "This server does not advertise teamControl.";
    case "no-ops":
      return "No team-control Ops are available on this connection.";
    default:
      return "";
  }
}

/** In-flight guard: one key per logical action until settled. */
export class InflightGuard {
  private keys = new Set<string>();

  tryBegin(key: string): boolean {
    if (this.keys.has(key)) return false;
    this.keys.add(key);
    return true;
  }

  end(key: string): void {
    this.keys.delete(key);
  }

  has(key: string): boolean {
    return this.keys.has(key);
  }
}

export type LocalBoardTask = {
  id: string;
  title: string;
  version: number;
  status: "pending" | "claimed" | "completed";
};

export function applyBoardCreate(
  list: LocalBoardTask[],
  taskId: string,
  title: string,
  version = 1,
): LocalBoardTask[] {
  if (list.some((t) => t.id === taskId)) return list;
  return [...list, { id: taskId, title, version, status: "pending" }];
}

export function applyBoardClaim(
  list: LocalBoardTask[],
  taskId: string,
  version: number,
): LocalBoardTask[] {
  return list.map((t) =>
    t.id === taskId ? { ...t, version: version || t.version + 1, status: "claimed" as const } : t,
  );
}

export function applyBoardComplete(
  list: LocalBoardTask[],
  taskId: string,
  version: number,
): LocalBoardTask[] {
  return list.map((t) =>
    t.id === taskId ? { ...t, version: version || t.version + 1, status: "completed" as const } : t,
  );
}
