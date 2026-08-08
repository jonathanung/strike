import type { Bootstrap, Envelope, RootsResponse, RootCreateResult, RootResumeResult, SandboxInfo, Session } from "./types";

// Auth is cookie (attach handoff) or Bearer set by the caller. Opening
// /attach?token=… sets an HttpOnly cookie and redirects; same-origin
// fetch / EventSource / WebSocket then send the cookie automatically.
// Query-string tokens are not accepted on /v1/* (they leak via logs/Referer).
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const requestHeaders = new Headers(init.headers);
  requestHeaders.set("Content-Type", "application/json");
  // same-origin credentials so the attach handoff cookie is always sent.
  const response = await fetch(path, { credentials: "same-origin", ...init, headers: requestHeaders });
  if (!response.ok) throw new Error((await response.json().catch(() => null))?.error || `${response.status} ${response.statusText}`);
  if (response.status === 204) return undefined as T;
  return response.json();
}
export const bootstrap = () => request<Bootstrap>("/v1/bootstrap");
export const sessions = () => request<{ sessions: Session[]; liveId?: string }>("/v1/sessions");
/** Observe-only multi-agent snapshot for late join / reload (WEBUI.13). */
export const fetchTeam = (rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<Record<string, unknown>>(`/v1/team${qs}`);
};
export const getSandbox = (rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<SandboxInfo>(`/v1/sandbox${qs}`);
};
export const patchSandbox = (mode: string, iKnow = false, rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<SandboxInfo>(`/v1/sandbox${qs}`, { method: "PATCH", body: JSON.stringify({ mode, iKnow }) });
};
/** Structured outcome from POST /v1/ops (team-control Ops return extra fields). */
export type OpResult = {
  ok: boolean;
  childSessionId?: string;
  name?: string;
  delegationId?: string;
  taskId?: string;
  messageId?: string;
  version?: number;
  alreadyTerminal?: boolean;
  error?: string;
  code?: string;
  currentVersion?: number;
};

export class OpError extends Error {
  code?: string;
  currentVersion?: number;
  constructor(message: string, code?: string, currentVersion?: number) {
    super(message);
    this.name = "OpError";
    this.code = code;
    this.currentVersion = currentVersion;
  }
}

export const sendOp = async (type: string, data?: unknown, rootID?: string): Promise<OpResult> => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  const response = await fetch(`/v1/ops${qs}`, {
    method: "POST",
    credentials: "same-origin",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ type, ...(data === undefined ? {} : { data }) }),
  });
  const body = (await response.json().catch(() => null)) as OpResult | null;
  if (!response.ok) {
    throw new OpError(
      body?.error || `${response.status} ${response.statusText}`,
      body?.code,
      body?.currentVersion,
    );
  }
  return body || { ok: true };
};

/** Download a redacted prompt/config diagnostic bundle (live host only). */
export async function downloadDiagnostics(rootID?: string): Promise<void> {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  const response = await fetch(`/v1/diag${qs}`, { credentials: "same-origin" });
  if (!response.ok) {
    const err = await response.json().catch(() => null) as { error?: string } | null;
    throw new Error(err?.error || `${response.status} ${response.statusText}`);
  }
  const blob = await response.blob();
  const cd = response.headers.get("Content-Disposition") || "";
  const match = /filename="?([^";]+)"?/i.exec(cd);
  const filename = match?.[1] || `strike-diag-${Date.now()}.json`;
  const url = URL.createObjectURL(blob);
  try {
    const anchor = document.createElement("a");
    anchor.href = url;
    anchor.download = filename;
    anchor.rel = "noopener";
    document.body.appendChild(anchor);
    anchor.click();
    anchor.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}

// --- root API ---
export const roots = () => request<RootsResponse>("/v1/roots");
export const createRoot = () => request<RootCreateResult>("/v1/roots", { method: "POST" });
export const activateRoot = (id: string) => request<{ ok: boolean }>(`/v1/roots/${encodeURIComponent(id)}/activate`, { method: "POST" });
export const resumeRoot = (sessionID: string) => request<RootResumeResult>(`/v1/roots/${encodeURIComponent(sessionID)}/resume`, { method: "POST" });
export const closeRoot = (id: string) => request<{ ok: boolean }>(`/v1/roots/${encodeURIComponent(id)}`, { method: "DELETE" });

// --- memory / issues (project-scoped; mutations blocked in attach-only) ---
export type MemoryEntryDTO = { Key?: string; key?: string; Value?: string; value?: string; Tags?: string[]; tags?: string[] };
export type IssueDTO = { ID?: number; id?: number; Title?: string; title?: string; Body?: string; body?: string; Status?: string; status?: string };

export const listMemory = (tag = "") => request<{ entries: MemoryEntryDTO[] }>(`/v1/memory${tag ? `?tag=${encodeURIComponent(tag)}` : ""}`);
export const putMemory = (key: string, value: string, tags: string[] = []) =>
  request<MemoryEntryDTO>(`/v1/memory/${encodeURIComponent(key)}`, { method: "PUT", body: JSON.stringify({ value, tags }) });
export const deleteMemory = (key: string) => request<void>(`/v1/memory/${encodeURIComponent(key)}`, { method: "DELETE" });
export const listIssues = (status = "") => request<{ issues: IssueDTO[] }>(`/v1/issues${status ? `?status=${encodeURIComponent(status)}` : ""}`);
export const createIssue = (title: string, body = "") =>
  request<IssueDTO>("/v1/issues", { method: "POST", body: JSON.stringify({ title, body }) });
export const closeIssue = (id: number) => request<IssueDTO>(`/v1/issues/${id}/close`, { method: "POST", body: JSON.stringify({}) });

/** Download portable export (same format as TUI). Uses cookie auth. */
export async function downloadExport(path: string, filename: string): Promise<void> {
  const response = await fetch(path, { credentials: "same-origin" });
  if (!response.ok) {
    const err = await response.json().catch(() => null);
    throw new Error(err?.error || `${response.status} ${response.statusText}`);
  }
  const blob = await response.blob();
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = url;
  anchor.download = filename;
  anchor.click();
  URL.revokeObjectURL(url);
}
export const exportMemory = () => downloadExport("/v1/memory/export", "strike-memory.json");
export const exportIssues = () => downloadExport("/v1/issues/export", "strike-issues.json");

export function liveConnection(rootID: string, onEvent: (event: Envelope) => void, onState: (state: string) => void) {
  let socket: WebSocket | undefined;
  let retry = 0;
  let closed = false;
  const connect = () => {
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    // Cookie auth only — browsers send the attach handoff cookie on same-origin WS.
    const rootParam = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
    socket = new WebSocket(`${scheme}//${location.host}/v1/ws${rootParam}`);
    socket.onopen = () => { retry = 0; onState("connected"); };
    socket.onmessage = (message) => { try { onEvent(JSON.parse(message.data)); } catch { onState("invalid event"); } };
    socket.onerror = () => onState("transport error");
    socket.onclose = () => { if (!closed) { onState("reconnecting"); setTimeout(connect, Math.min(500 * 2 ** retry++, 8000)); } };
  };
  connect();
  return { send: (type: string, data?: unknown) => socket?.readyState === WebSocket.OPEN && socket.send(JSON.stringify({ type, ...(data === undefined ? {} : { data }) })), close: () => {
      closed = true;
      if (socket) {
        socket.onmessage = null;
        socket.onerror = null;
        socket.onclose = null;
        socket.close();
      }
    } };
}

export function historicalConnection(id: string, onEvent: (event: Envelope) => void, onError: (message: string) => void = () => {}) {
  // EventSource is same-origin and sends the attach handoff cookie automatically.
  const source = new EventSource(`/v1/sessions/${encodeURIComponent(id)}/events`);
  source.onmessage = (event) => { try { onEvent(JSON.parse(event.data)); } catch { onError("invalid historical event"); } };
  source.onerror = () => onError("history reconnecting");
  return () => source.close();
}

export const sessionChildren = (id: string) =>
  request<{ sessions: Array<Record<string, unknown>> }>(`/v1/sessions/${encodeURIComponent(id)}/children`);
