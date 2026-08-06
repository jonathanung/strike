import { request } from "./api";

export type PaneInfo = {
  id: string;
  pluginId: string;
  pluginVersion?: string;
  scope?: string;
  title?: string;
  mode: string;
  trusted: boolean;
  loadError?: string;
  provenance?: string;
  definition?: Record<string, unknown>;
};

export type PaneSnapshot = {
  id: string;
  title?: string;
  status?: string;
  error?: string;
  mode: string;
  view?: PaneViewNode | null;
  feeds?: Record<string, unknown>;
  rev?: number;
  mounted?: boolean;
};

export type PaneViewNode = {
  type?: string;
  children?: PaneViewNode[];
  gap?: number;
  wrap?: boolean;
  text?: string;
  textFrom?: string;
  style?: string;
  truncate?: string;
  entries?: Array<{ key?: string; value?: string; valueFrom?: string }>;
  items?: Array<{
    id?: string;
    label?: string;
    detail?: string;
    icon?: string;
    actions?: Record<string, unknown>;
  }>;
  selectable?: boolean;
  selectedId?: string;
  columns?: Array<{ id?: string; header?: string; width?: number }>;
  rows?: Array<{ cells?: Record<string, string> }>;
  label?: string;
  value?: unknown;
  valueFrom?: string;
  max?: unknown;
  maxFrom?: string;
  tone?: string;
  flex?: number;
  min?: number;
  hint?: string;
  icon?: string;
  actions?: Record<string, unknown>;
};

export const listPanes = () => request<{ panes: PaneInfo[] }>("/v1/panes");

export const getPane = (id: string) =>
  request<PaneInfo>(`/v1/panes/${encodeURIComponent(id)}`);

export const paneSnapshot = (id: string, rootID?: string) => {
  const q = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<PaneSnapshot>(`/v1/panes/${encodeURIComponent(id)}/snapshot${q}`);
};

export const mountPane = (id: string, width = 40, height = 14, rootID?: string) => {
  const q = rootID ? `?root=${encodeURIComponent(rootID)}` : "";
  return request<PaneSnapshot>(`/v1/panes/${encodeURIComponent(id)}/mount${q}`, {
    method: "POST",
    body: JSON.stringify({ width, height }),
  });
};

export const unmountPane = (id: string) =>
  request<{ ok: boolean }>(`/v1/panes/${encodeURIComponent(id)}/unmount`, {
    method: "POST",
    body: JSON.stringify({}),
  });

export const paneInput = (id: string, event: Record<string, unknown>) =>
  request<{ ok: boolean }>(`/v1/panes/${encodeURIComponent(id)}/input`, {
    method: "POST",
    body: JSON.stringify({ event }),
  });

export const paneResize = (id: string, width: number, height: number) =>
  request<{ ok: boolean }>(`/v1/panes/${encodeURIComponent(id)}/resize`, {
    method: "POST",
    body: JSON.stringify({ width, height }),
  });

/** Resolve valueFrom paths like "session.summary.model" against feed snapshots. */
export function resolveFrom(feeds: Record<string, unknown> | undefined, path: string | undefined): string {
  if (!path || !feeds) return "";
  const parts = path.split(".").filter(Boolean);
  if (!parts.length) return "";
  // Prefer longest matching feed key (session.summary before session).
  let feedKey = "";
  let rest: string[] = parts;
  if (parts.length >= 2 && feeds[`${parts[0]}.${parts[1]}`] !== undefined) {
    feedKey = `${parts[0]}.${parts[1]}`;
    rest = parts.slice(2);
  } else if (feeds[parts[0]] !== undefined) {
    feedKey = parts[0];
    rest = parts.slice(1);
  } else {
    return "";
  }
  let cur: unknown = feeds[feedKey];
  for (const p of rest) {
    if (cur == null || typeof cur !== "object") return "";
    cur = (cur as Record<string, unknown>)[p];
  }
  if (cur == null) return "";
  if (typeof cur === "string" || typeof cur === "number" || typeof cur === "boolean") return String(cur);
  try {
    return JSON.stringify(cur);
  } catch {
    return "";
  }
}
