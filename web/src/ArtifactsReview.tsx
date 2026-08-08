/**
 * Artifacts, ledger, handoff, and conflict review (WEBUI.16 / #1086).
 * Read-only against WEBUI.15 APIs + WEBUI.13 team observation.
 */
import { useCallback, useEffect, useMemo, useState } from "react";
import { request } from "./api";
import type { TeamObservation } from "./team";
import { StatusBadge, statusKindFrom } from "./ui";

export type ArtifactMeta = {
  id: string;
  type?: string;
  title?: string;
  version?: number;
  scope?: string;
  sessionId?: string;
  access?: string;
  ownerSession?: string;
  ownerRoot?: string;
  createdAt?: string;
  updatedAt?: string;
  expiresAt?: string | null;
  content?: string;
};

export type LedgerEntry = {
  id: string;
  kind?: string;
  statement?: string;
  confidence?: string;
  status?: string;
  scopePaths?: string[];
  scopeTaskIds?: string[];
  authorSession?: string;
  authorAgent?: string;
  supersededBy?: string;
  supersedes?: string;
  invalidateReason?: string;
  createdAt?: string;
  updatedAt?: string;
};

function qs(params: Record<string, string | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") p.set(k, v);
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function verificationLabel(v?: { passed?: boolean; claimed?: boolean; verified?: boolean; summary?: string }): {
  kind: "complete" | "failed" | "unknown" | "needs-you";
  label: string;
} {
  if (!v) return { kind: "unknown", label: "unknown" };
  if (v.verified === true || v.passed === true) return { kind: "complete", label: "verified" };
  if (v.passed === false) return { kind: "failed", label: "failed" };
  if (v.claimed && !v.verified) return { kind: "needs-you", label: "claimed (unverified)" };
  return { kind: "unknown", label: "unknown" };
}

export function ArtifactsPanel({
  available,
  rootID,
  entity,
  onOpenFile,
}: {
  available: boolean;
  rootID?: string;
  entity?: string;
  onOpenFile?: (path: string) => void;
}) {
  const [items, setItems] = useState<ArtifactMeta[]>([]);
  const [error, setError] = useState("");
  const [typeFilter, setTypeFilter] = useState("");
  const [selected, setSelected] = useState<ArtifactMeta | null>(null);
  const [version, setVersion] = useState<number | undefined>();
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    if (!available) return;
    setLoading(true);
    setError("");
    try {
      const res = await request<{ artifacts?: ArtifactMeta[] }>(
        `/v1/artifacts${qs({
          root: rootID,
          actorRoot: rootID,
          actorSession: rootID,
          type: typeFilter || undefined,
          limit: "100",
        })}`,
      );
      setItems(res.artifacts || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [available, rootID, typeFilter]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!entity || !available) return;
    // entity = artifact id or id@version
    const [id, verRaw] = entity.split("@");
    const ver = verRaw ? Number(verRaw) : undefined;
    void request<ArtifactMeta>(
      `/v1/artifacts/${encodeURIComponent(id)}${qs({
        root: rootID,
        actorRoot: rootID,
        actorSession: rootID,
        version: ver && Number.isFinite(ver) ? String(ver) : undefined,
      })}`,
    )
      .then((a) => {
        setSelected(a);
        setVersion(a.version);
      })
      .catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, [entity, available, rootID]);

  const openDetail = async (id: string, ver?: number) => {
    setError("");
    try {
      const a = await request<ArtifactMeta>(
        `/v1/artifacts/${encodeURIComponent(id)}${qs({
          root: rootID,
          actorRoot: rootID,
          actorSession: rootID,
          version: ver !== undefined ? String(ver) : undefined,
        })}`,
      );
      setSelected(a);
      setVersion(a.version);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSelected(null);
    }
  };

  if (!available) {
    return (
      <section aria-label="Artifacts">
        <h2>Artifacts</h2>
        <p className="muted" role="status">Artifacts capability unavailable.</p>
      </section>
    );
  }

  const types = useMemo(() => {
    const s = new Set<string>();
    for (const a of items) if (a.type) s.add(a.type);
    return [...s].sort();
  }, [items]);

  return (
    <section className="review-panel" aria-label="Artifacts">
      <header className="review-head">
        <h2>Artifacts</h2>
        <button type="button" onClick={() => void load()} disabled={loading}>Refresh</button>
      </header>
      <label className="review-filter">
        Type
        <select value={typeFilter} onChange={(e) => setTypeFilter(e.target.value)}>
          <option value="">All</option>
          {types.map((t) => (
            <option key={t} value={t}>{t}</option>
          ))}
        </select>
      </label>
      {error ? <p className="muted" role="alert">{error}</p> : null}
      {loading && !items.length ? <p className="muted">Loading…</p> : null}
      {!loading && !items.length && !error ? <p className="muted">No artifacts visible for this actor/root.</p> : null}
      <ul className="review-list" aria-label="Artifact list">
        {items.map((a) => (
          <li key={a.id}>
            <button type="button" className={selected?.id === a.id ? "active" : ""} onClick={() => void openDetail(a.id)}>
              <strong>{a.title || a.id}</strong>
              <span className="muted">{[a.type, a.scope, a.version !== undefined ? `v${a.version}` : "", a.ownerSession?.slice(0, 8)].filter(Boolean).join(" · ")}</span>
            </button>
          </li>
        ))}
      </ul>
      {selected ? (
        <article className="review-detail" aria-label="Artifact detail">
          <header>
            <h3>{selected.title || selected.id}</h3>
            <button type="button" className="icon-btn" aria-label="Close artifact detail" onClick={() => setSelected(null)}>×</button>
          </header>
          <dl>
            <dt>ID</dt><dd><code>{selected.id}</code></dd>
            <dt>Type</dt><dd>{selected.type || "—"}</dd>
            <dt>Version</dt>
            <dd>
              <input
                type="number"
                min={1}
                value={version ?? selected.version ?? 1}
                onChange={(e) => setVersion(Number(e.target.value))}
                aria-label="Artifact version"
              />
              <button type="button" onClick={() => void openDetail(selected.id, version)}>Load version</button>
            </dd>
            <dt>Scope</dt><dd>{selected.scope || "—"}</dd>
            <dt>Owner</dt><dd><code>{selected.ownerSession || "—"}</code></dd>
            <dt>Access</dt><dd>{selected.access || "—"}</dd>
            {selected.expiresAt ? <><dt>Expires</dt><dd>{selected.expiresAt}</dd></> : null}
          </dl>
          {selected.content !== undefined ? (
            <pre className="review-content">{selected.content || "(empty)"}</pre>
          ) : (
            <p className="muted">Content unavailable or redacted.</p>
          )}
          {onOpenFile && selected.content?.includes("/") ? (
            <p className="muted">Open paths from content via Code mode file links in the transcript.</p>
          ) : null}
        </article>
      ) : null}
    </section>
  );
}

export function LedgerPanel({
  available,
  rootID,
}: {
  available: boolean;
  rootID?: string;
}) {
  const [tab, setTab] = useState<"active" | "history">("active");
  const [items, setItems] = useState<LedgerEntry[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  const load = useCallback(async () => {
    if (!available) return;
    setLoading(true);
    setError("");
    try {
      const path = tab === "active" ? "/v1/ledger" : "/v1/ledger/history";
      const res = await request<{ entries?: LedgerEntry[] }>(
        `${path}${qs({ root: rootID, actorRoot: rootID, actorSession: rootID, limit: "100" })}`,
      );
      setItems(res.entries || []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setItems([]);
    } finally {
      setLoading(false);
    }
  }, [available, rootID, tab]);

  useEffect(() => {
    void load();
  }, [load]);

  if (!available) {
    return (
      <section aria-label="Decision ledger">
        <h2>Decisions</h2>
        <p className="muted" role="status">Ledger capability unavailable.</p>
      </section>
    );
  }

  return (
    <section className="review-panel" aria-label="Decision ledger">
      <header className="review-head">
        <h2>Decisions</h2>
        <div className="review-tabs" role="tablist">
          <button type="button" role="tab" aria-selected={tab === "active"} className={tab === "active" ? "active" : ""} onClick={() => setTab("active")}>Active</button>
          <button type="button" role="tab" aria-selected={tab === "history"} className={tab === "history" ? "active" : ""} onClick={() => setTab("history")}>History</button>
        </div>
      </header>
      {error ? <p className="muted" role="alert">{error}</p> : null}
      {loading && !items.length ? <p className="muted">Loading…</p> : null}
      {!loading && !items.length && !error ? <p className="muted">No ledger entries.</p> : null}
      <ul className="review-list" aria-label="Ledger entries">
        {items.map((e) => {
          const status = (e.status || "active").toLowerCase();
          const kind =
            status === "invalidated" || status === "superseded"
              ? "failed"
              : status === "active"
                ? "complete"
                : "unknown";
          return (
            <li key={e.id} className={`ledger-row status-${status}`}>
              <div className="ledger-row-head">
                <StatusBadge kind={kind as "complete" | "failed" | "unknown"} label={status} />
                <strong>{e.kind || "entry"}</strong>
                <code className="muted">{e.id}</code>
              </div>
              <p>{e.statement || "(no statement)"}</p>
              <div className="muted ledger-meta">
                {[e.authorAgent || e.authorSession?.slice(0, 8), e.confidence, e.supersededBy ? `→ ${e.supersededBy}` : "", e.supersedes ? `← ${e.supersedes}` : ""]
                  .filter(Boolean)
                  .join(" · ")}
              </div>
              {e.scopePaths?.length ? (
                <ul className="review-paths">{e.scopePaths.map((p) => <li key={p}><code>{p}</code></li>)}</ul>
              ) : null}
              {e.invalidateReason ? <p className="muted">Invalidated: {e.invalidateReason}</p> : null}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

export function TeamReviewPanel({
  team,
  onSelectAgent,
  onOpenFile,
}: {
  team: TeamObservation;
  onSelectAgent?: (id: string) => void;
  onOpenFile?: (path: string) => void;
}) {
  if (!team.available) {
    return (
      <section aria-label="Team review">
        <h2>Review</h2>
        <p className="muted">{team.unavailableReason || "Team observation unavailable"}</p>
      </section>
    );
  }

  const members = Object.values(team.members).filter((m) => m.terminal || m.terminalSummary);
  const overlaps = team.pathOverlaps || [];
  const verifications = team.verifications || [];

  return (
    <section className="review-panel" aria-label="Team review">
      <h2>Review</h2>
      <p className="muted">Handoffs, verification, and path conflicts from live observation (read-only).</p>

      <h3>Handoffs</h3>
      {!members.length ? <p className="muted">No terminal handoffs yet.</p> : null}
      <ul className="review-list">
        {members.map((m) => {
          const v = [...verifications].reverse().find((x) => x.sessionId === m.sessionId);
          const vl = verificationLabel(v);
          return (
            <li key={m.sessionId}>
              <button type="button" onClick={() => onSelectAgent?.(m.sessionId)}>
                <strong>{m.name || m.agent || m.sessionId.slice(0, 8)}</strong>
                <StatusBadge kind={statusKindFrom(m.state)} label={m.state || "done"} />
                <StatusBadge kind={vl.kind} label={vl.label} />
              </button>
              {m.terminalSummary ? <p>{m.terminalSummary}</p> : null}
              {m.blockReason ? <p className="muted">Blocked: {m.blockReason}</p> : null}
              <div className="muted">
                Budget: {m.budget ? JSON.stringify(m.budget).slice(0, 120) : "unknown"}
              </div>
              {v?.summary ? <p className="muted">Verification: {v.summary}</p> : null}
              {m.filesTouched?.length ? (
                <ul className="review-paths">
                  {m.filesTouched.map((p) => (
                    <li key={p}>
                      <button type="button" className="linkish" onClick={() => onOpenFile?.(p)}>
                        <code>{p}</code>
                      </button>
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          );
        })}
      </ul>

      <h3>Path overlaps</h3>
      {!overlaps.length ? <p className="muted">No path conflicts observed.</p> : null}
      <ul className="review-list">
        {overlaps.map((o) => (
          <li key={o.path}>
            <button type="button" className="linkish" onClick={() => onOpenFile?.(o.path)}>
              <code>{o.path}</code>
            </button>
            <span className="muted">{(o.sessions || []).join(", ") || "unknown agents"}</span>
          </li>
        ))}
      </ul>
    </section>
  );
}
