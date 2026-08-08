import type { ReactNode } from "react";

/** Semantic run/agent status — never color-only. */
export type StatusKind =
  | "idle"
  | "busy"
  | "blocked"
  | "needs-you"
  | "failed"
  | "complete"
  | "canceled"
  | "unknown";

const LABELS: Record<StatusKind, string> = {
  idle: "Idle",
  busy: "Busy",
  blocked: "Blocked",
  "needs-you": "Needs you",
  failed: "Failed",
  complete: "Complete",
  canceled: "Canceled",
  unknown: "Unknown",
};

/** Map common engine/child status strings onto StatusKind. */
export function statusKindFrom(raw?: string): StatusKind {
  const s = (raw || "").toLowerCase();
  if (!s || s === "idle" || s === "ready") return "idle";
  if (s === "running" || s === "busy" || s === "streaming" || s === "working") return "busy";
  if (s === "blocked" || s === "finalizing" || s === "escalating" || s === "waiting") return "blocked";
  if (s === "needs-you" || s === "needs_you" || s === "permission" || s === "question") return "needs-you";
  if (s === "failed" || s === "error" || s === "interrupted") return "failed";
  if (s === "completed" || s === "complete" || s === "done" || s === "success") return "complete";
  if (s === "canceled" || s === "cancelled") return "canceled";
  return "unknown";
}

export function StatusBadge({
  kind,
  label,
  className = "",
}: {
  kind: StatusKind;
  label?: ReactNode;
  className?: string;
}) {
  const text = label ?? LABELS[kind];
  return (
    <span className={`ui-status ui-status-${kind} ${className}`.trim()} data-status={kind}>
      <span className="ui-status-dot" aria-hidden />
      <span className="ui-status-label">{text}</span>
    </span>
  );
}

export function StatusDot({
  kind,
  label,
  className = "",
}: {
  kind: StatusKind;
  /** Accessible name when the visual is icon-only. */
  label?: string;
  className?: string;
}) {
  const text = label ?? LABELS[kind];
  return (
    <span
      className={`ui-status-dot ui-status-dot-alone ui-status-${kind} ${className}`.trim()}
      data-status={kind}
      role="img"
      aria-label={text}
      title={text}
    />
  );
}
