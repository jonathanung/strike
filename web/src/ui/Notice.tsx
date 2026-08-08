import type { ReactNode } from "react";

export type NoticeTone = "info" | "success" | "warning" | "error" | "unavailable";

export function Notice({
  tone = "info",
  title,
  children,
  className = "",
}: {
  tone?: NoticeTone;
  title?: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  const role = tone === "error" ? "alert" : "status";
  return (
    <section
      className={`ui-notice ui-notice-${tone} ${tone === "unavailable" ? "unavailable" : ""} ${className}`.trim()}
      role={role}
      data-tone={tone}
    >
      {title ? <strong className="ui-notice-title">{title}</strong> : null}
      {children ? <div className="ui-notice-body">{children}</div> : null}
    </section>
  );
}

export function CapabilityUnavailable({ name }: { name: string }) {
  return (
    <Notice tone="unavailable" title={`${name} unavailable`}>
      <p>The configured host did not provide this capability. No action was attempted.</p>
    </Notice>
  );
}

export function EmptyState({
  kicker,
  title,
  children,
  className = "",
}: {
  kicker?: ReactNode;
  title: ReactNode;
  children?: ReactNode;
  className?: string;
}) {
  return (
    <div className={`empty-state ui-empty ${className}`.trim()}>
      {kicker ? <span>{kicker}</span> : null}
      <h1>{title}</h1>
      {children ? <div className="ui-empty-body">{children}</div> : null}
    </div>
  );
}

export function LoadingState({ label = "Loading…" }: { label?: string }) {
  return (
    <Notice tone="info" title={label} className="ui-loading">
      <span className="ui-loading-indicator" aria-hidden />
    </Notice>
  );
}

export function ErrorState({ title = "Error", children }: { title?: string; children?: ReactNode }) {
  return (
    <Notice tone="error" title={title}>
      {children}
    </Notice>
  );
}
