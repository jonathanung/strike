import { request } from "./api";

export type TimelineEntry = {
  id: string;
  kind: string;
  state: string;
  sessionId?: string;
  turnId?: string;
  parentId?: string;
  name?: string;
  callId?: string;
  providerRequestId?: string;
  childSessionId?: string;
  attempt?: number;
  startedAt?: string;
  endedAt?: string;
  durationMs?: number | null;
  stopReason?: string;
  childStatus?: string;
  error?: string;
  errorCode?: string;
  inputTokens?: number | null;
  outputTokens?: number | null;
  argsPreview?: string;
  outputPreview?: string;
  truncated?: boolean;
};

export type TimelineSummary = {
  turns: number;
  tools: number;
  providers: number;
  children: number;
  permissions?: number;
  verifies?: number;
  failed: number;
  canceled: number;
  inputTokens?: number;
  outputTokens?: number;
  durationMs?: number;
};

export type TimelineTrace = {
  schemaVersion: string;
  sessionId?: string;
  exportedAt?: string;
  redacted: boolean;
  note?: string;
  summary: TimelineSummary;
  entries: TimelineEntry[];
  warnings?: string[];
};

export const fetchTimeline = (sessionID: string) =>
  request<TimelineTrace>(`/v1/sessions/${encodeURIComponent(sessionID)}/timeline`);

/** Download a redacted timeline export (JSON or JSONL) via authenticated fetch. */
export async function downloadTimeline(sessionID: string, format: "json" | "jsonl" = "json"): Promise<void> {
  const path = `/v1/sessions/${encodeURIComponent(sessionID)}/timeline/export?format=${format}`;
  const response = await fetch(path, { credentials: "same-origin" });
  if (!response.ok) {
    const err = await response.json().catch(() => null);
    throw new Error(err?.error || `${response.status} ${response.statusText}`);
  }
  const blob = await response.blob();
  const cd = response.headers.get("Content-Disposition") || "";
  const match = /filename="([^"]+)"/.exec(cd);
  const filename = match?.[1] || `strike-timeline.${format === "jsonl" ? "jsonl" : "json"}`;
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.rel = "noopener";
    document.body.appendChild(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(url);
  }
}
