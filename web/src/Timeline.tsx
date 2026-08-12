import { useCallback, useEffect, useState } from "react";
import { downloadTimeline, fetchTimeline, type TimelineEntry, type TimelineTrace } from "./timelineApi";

function formatDuration(ms?: number | null): string {
  if (ms == null || Number.isNaN(ms)) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`;
  return `${(ms / 60_000).toFixed(1)}m`;
}

function shortID(id?: string): string {
  const s = (id || "").trim();
  if (!s) return "—";
  return s.length <= 10 ? s : s.slice(0, 10);
}

function entryLabel(e: TimelineEntry): string {
  return e.name ? `${e.kind}:${e.name}` : e.kind;
}

function entryID(e: TimelineEntry): string {
  switch (e.kind) {
    case "turn":
      return shortID(e.turnId);
    case "tool":
      return shortID(e.callId);
    case "provider":
      return shortID(e.providerRequestId);
    case "child":
      return shortID(e.childSessionId);
    case "permission":
      return shortID(e.callId);
    case "verify":
      return shortID(e.turnId || e.id);
    default:
      return shortID(e.id);
  }
}

export function TimelinePanel({
  available,
  sessionID,
}: {
  available: boolean;
  sessionID: string;
}) {
  const [trace, setTrace] = useState<TimelineTrace | undefined>();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [exporting, setExporting] = useState<"json" | "jsonl" | "">("");

  const load = useCallback(async () => {
    if (!available || !sessionID) return;
    setLoading(true);
    setError("");
    try {
      setTrace(await fetchTimeline(sessionID));
    } catch (err) {
      setTrace(undefined);
      setError((err as Error).message || "timeline unavailable");
    } finally {
      setLoading(false);
    }
  }, [available, sessionID]);

  // Drop stale spans and reload when the selected session changes.
  useEffect(() => {
    setTrace(undefined);
    setError("");
    if (!available || !sessionID) return;
    void load();
  }, [available, sessionID, load]);

  const onExport = async (format: "json" | "jsonl") => {
    if (!sessionID) return;
    setExporting(format);
    setError("");
    try {
      await downloadTimeline(sessionID, format);
    } catch (err) {
      setError((err as Error).message || "export failed");
    } finally {
      setExporting("");
    }
  };

  if (!available) {
    return (
      <section className="unavailable" role="status">
        <strong>Timeline unavailable</strong>
        <p>The configured host did not provide this capability. No action was attempted.</p>
      </section>
    );
  }
  if (!sessionID) {
    return <p className="muted">Select a session to inspect its run timeline.</p>;
  }

  const summary = trace?.summary;
  const entries = trace?.entries || [];

  return (
    <div className="timeline-panel">
      <div className="timeline-head">
        <h2>Run timeline</h2>
        <div className="timeline-actions">
          <button type="button" onClick={() => void load()} disabled={loading}>
            {loading ? "Loading…" : trace ? "Refresh" : "Load"}
          </button>
          <button type="button" onClick={() => void onExport("json")} disabled={!sessionID || exporting !== ""}>
            {exporting === "json" ? "…" : "Export JSON"}
          </button>
          <button type="button" onClick={() => void onExport("jsonl")} disabled={!sessionID || exporting !== ""}>
            {exporting === "jsonl" ? "…" : "Export JSONL"}
          </button>
        </div>
      </div>
      <p className="muted">
        Collapsed harness spans (turns/tools/provider/children). Complements the transcript; exports are secret-redacted.
      </p>
      {error && (
        <section className="unavailable" role="status">
          <strong>Unable to load</strong>
          <p>{error}</p>
        </section>
      )}
      {trace && (
        <>
          <dl className="timeline-summary" aria-label="Timeline summary">
            <div>
              <dt>Turns</dt>
              <dd>{summary?.turns ?? 0}</dd>
            </div>
            <div>
              <dt>Tools</dt>
              <dd>{summary?.tools ?? 0}</dd>
            </div>
            <div>
              <dt>Failed</dt>
              <dd>{summary?.failed ?? 0}</dd>
            </div>
            <div>
              <dt>Duration</dt>
              <dd>{formatDuration(summary?.durationMs) || "—"}</dd>
            </div>
            <div>
              <dt>Redacted</dt>
              <dd>{trace.redacted ? "yes" : "no"}</dd>
            </div>
          </dl>
          {entries.length === 0 ? (
            <p className="muted">No timeline entries yet — run a turn first.</p>
          ) : (
            <ol className="timeline-list" aria-label="Timeline entries">
              {entries.map((e) => (
                <li key={e.id} className={`timeline-entry state-${e.state || "unknown"}`}>
                  <span className={`timeline-state state-${e.state || "unknown"}`}>{e.state || "?"}</span>
                  <span className="timeline-label" title={entryLabel(e)}>
                    {entryLabel(e)}
                  </span>
                  <code className="timeline-id">{entryID(e)}</code>
                  <span className="timeline-dur">{formatDuration(e.durationMs)}</span>
                  {e.errorCode && <span className="timeline-code">{e.errorCode}</span>}
                  {e.error && <span className="timeline-err" title={e.error}>{e.error}</span>}
                </li>
              ))}
            </ol>
          )}
        </>
      )}
      {!trace && !loading && !error && (
        <p className="muted">Load the collapsed run timeline for the selected session.</p>
      )}
    </div>
  );
}
