import type { Bootstrap, Envelope, Session } from "./types";

const queryToken = new URLSearchParams(location.search).get("token") || "";
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const requestHeaders = new Headers(init.headers);
  requestHeaders.set("Content-Type", "application/json");
  if (queryToken) requestHeaders.set("Authorization", `Bearer ${queryToken}`);
  const response = await fetch(path, { ...init, headers: requestHeaders });
  if (!response.ok) throw new Error((await response.json().catch(() => null))?.error || `${response.status} ${response.statusText}`);
  if (response.status === 204) return undefined as T;
  return response.json();
}
export const bootstrap = () => request<Bootstrap>("/v1/bootstrap");
export const sessions = () => request<{ sessions: Session[]; liveId?: string }>("/v1/sessions");
export const sendOp = (type: string, data?: unknown) => request<{ ok: boolean }>("/v1/ops", { method: "POST", body: JSON.stringify({ type, ...(data === undefined ? {} : { data }) }) });

export function liveConnection(onEvent: (event: Envelope) => void, onState: (state: string) => void) {
  let socket: WebSocket | undefined;
  let retry = 0;
  let closed = false;
  const connect = () => {
    const scheme = location.protocol === "https:" ? "wss:" : "ws:";
    const token = queryToken ? `?token=${encodeURIComponent(queryToken)}` : "";
    socket = new WebSocket(`${scheme}//${location.host}/v1/ws${token}`);
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
