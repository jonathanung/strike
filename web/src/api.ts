import type { Bootstrap, Envelope, RootsResponse, RootCreateResult, RootResumeResult, Session } from "./types";

// Token may arrive via ?token= before the server handoff redirect strips it and
// sets an HttpOnly cookie. After handoff, same-origin cookies authenticate
// fetch / EventSource / WebSocket without a query param.
const queryToken = new URLSearchParams(location.search).get("token") || "";
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const requestHeaders = new Headers(init.headers);
  requestHeaders.set("Content-Type", "application/json");
  if (queryToken) requestHeaders.set("Authorization", `Bearer ${queryToken}`);
  // same-origin credentials so the attach handoff cookie is always sent.
  const response = await fetch(path, { credentials: "same-origin", ...init, headers: requestHeaders });
  if (!response.ok) throw new Error((await response.json().catch(() => null))?.error || `${response.status} ${response.statusText}`);
  if (response.status === 204) return undefined as T;
  return response.json();
}
export const bootstrap = () => request<Bootstrap>("/v1/bootstrap");
export const sessions = () => request<{ sessions: Session[]; liveId?: string }>("/v1/sessions");
export const sendOp = (type: string, data?: unknown, rootID?: string) => {
  const qs = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<{ ok: boolean }>(`/v1/ops${qs}`, { method: "POST", body: JSON.stringify({ type, ...(data === undefined ? {} : { data }) }) });
};

// --- root API ---
export const roots = () => request<RootsResponse>("/v1/roots");
export const createRoot = () => request<RootCreateResult>("/v1/roots", { method: "POST" });
export const activateRoot = (id: string) => request<{ ok: boolean }>(`/v1/roots/${encodeURIComponent(id)}/activate`, { method: "POST" });
export const resumeRoot = (sessionID: string) => request<RootResumeResult>(`/v1/roots/${encodeURIComponent(sessionID)}/resume`, { method: "POST" });
export const closeRoot = (id: string) => request<{ ok: boolean }>(`/v1/roots/${encodeURIComponent(id)}`, { method: "DELETE" });

export function liveConnection(rootID: string, onEvent: (event: Envelope) => void, onState: (state: string) => void) {
  let socket: WebSocket | undefined;
  let retry = 0;
  let closed = false;
  const connect = () => {
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const token = queryToken ? `?token=${encodeURIComponent(queryToken)}` : "";
    const rootParam = rootID ? `${token ? "&" : "?"}root=${encodeURIComponent(rootID)}` : "";
    socket = new WebSocket(`${scheme}//${location.host}/v1/ws${token}${rootParam}`);
    socket.onopen = () => { retry = 0; onState("connected"); };
    socket.onmessage = (message) => { try { onEvent(JSON.parse(message.data)); } catch { onState("invalid event"); } };
    socket.onerror = () => onState("transport error");
    socket.onclose = () => { if (!closed) { onState("reconnecting"); setTimeout(connect, Math.min(500 * 2 ** retry++, 8000)); } };
  };
  connect();
  return { send: (type: string, data?: unknown) => socket?.readyState === WebSocket.OPEN && socket.send(JSON.stringify({ type, ...(data === undefined ? {} : { data }) })), close: () => { closed = true; socket?.close(); } };
}

export function historicalConnection(id: string, onEvent: (event: Envelope) => void, onError: (message: string) => void = () => {}) {
  const token = queryToken ? `?token=${encodeURIComponent(queryToken)}` : "";
  const source = new EventSource(`/v1/sessions/${encodeURIComponent(id)}/events${token}`);
  source.onmessage = (event) => { try { onEvent(JSON.parse(event.data)); } catch { onError("invalid historical event"); } };
  source.onerror = () => onError("history reconnecting");
  return () => source.close();
}

export const sessionChildren = (id: string) =>
  request<{ sessions: Array<Record<string, unknown>> }>(`/v1/sessions/${encodeURIComponent(id)}/children`);
