import { useCallback, useEffect, useState } from "react";
import { request } from "./api";

export type LSPServer = {
  name: string;
  command?: string;
  state: string;
  extensions?: string[];
  error?: string;
  openDocs?: number;
};

export type DiagnosticFinding = {
  path: string;
  line: number;
  character: number;
  severity: string;
  source?: string;
  code?: string;
  message: string;
};

type LSPStatusResponse = { servers: LSPServer[]; note?: string };
type DiagnosticsResponse = { diagnostics: DiagnosticFinding[]; count: number; note?: string };

export function DiagnosticsPanel({ available }: { available: boolean }) {
  const [servers, setServers] = useState<LSPServer[]>([]);
  const [findings, setFindings] = useState<DiagnosticFinding[]>([]);
  const [note, setNote] = useState("");
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");

  const refresh = useCallback(async () => {
    if (!available) return;
    setLoading(true);
    setError("");
    try {
      const [lsp, diags] = await Promise.all([
        request<LSPStatusResponse>("/v1/lsp"),
        request<DiagnosticsResponse>("/v1/diagnostics"),
      ]);
      setServers(lsp.servers || []);
      setFindings(diags.diagnostics || []);
      setNote(diags.note || lsp.note || "");
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  }, [available]);

  useEffect(() => { void refresh(); }, [refresh]);

  const retry = async (name?: string) => {
    setBusy(name ? `retry:${name}` : "retry:all");
    try {
      await request("/v1/lsp/retry", {
        method: "POST",
        body: JSON.stringify(name ? { name } : {}),
      });
      await refresh();
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  const disable = async (name: string) => {
    if (!window.confirm(`Disable language server “${name}”?`)) return;
    setBusy(`disable:${name}`);
    try {
      await request(`/v1/lsp/${encodeURIComponent(name)}/disable`, { method: "POST" });
      await refresh();
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  if (!available) {
    return (
      <section className="unavailable" role="status">
        <strong>Diagnostics unavailable</strong>
        <p>The configured host did not provide this capability. No action was attempted.</p>
      </section>
    );
  }
  if (loading && !servers.length && !findings.length && !note && !error) {
    return <section className="unavailable" role="status"><strong>Loading diagnostics</strong></section>;
  }
  if (error) {
    return <section className="unavailable" role="status"><strong>Unable to load</strong><p>{error}</p></section>;
  }

  return (
    <>
      <div className="diagnostics-heading">
        <h2>Diagnostics</h2>
        <div className="diagnostics-actions">
          <button type="button" disabled={Boolean(busy)} onClick={() => void refresh()}>Refresh</button>
          <button type="button" disabled={Boolean(busy)} onClick={() => void retry()}>
            {busy === "retry:all" ? "Retrying…" : "Retry all"}
          </button>
        </div>
      </div>

      <h3 className="diagnostics-subhead">Language servers</h3>
      {servers.length === 0 ? (
        <p className="muted" role="status">{note || "no language servers configured (add lsp.servers in config)"}</p>
      ) : (
        <div className="project-list lsp-servers">
          {servers.map((server) => (
            <article key={server.name} className="lsp-server">
              <header>
                <h3>{server.name}</h3>
                <span className={`lsp-state state-${(server.state || "down").toLowerCase()}`}>{server.state}</span>
              </header>
              <p>
                <code>{server.command || "—"}</code>
                {server.extensions && server.extensions.length > 0 && (
                  <small> {server.extensions.join(", ")}</small>
                )}
                {typeof server.openDocs === "number" && server.openDocs > 0 && (
                  <small> docs={server.openDocs}</small>
                )}
              </p>
              {server.error && server.state !== "disabled" && <p className="lsp-error">{server.error}</p>}
              <div className="diagnostics-actions">
                <button
                  type="button"
                  disabled={Boolean(busy) || server.state === "disabled"}
                  onClick={() => void retry(server.name)}
                >
                  {busy === `retry:${server.name}` ? "Retrying…" : "Retry"}
                </button>
                <button
                  type="button"
                  disabled={Boolean(busy) || server.state === "disabled"}
                  onClick={() => void disable(server.name)}
                >
                  {busy === `disable:${server.name}` ? "Disabling…" : "Disable"}
                </button>
              </div>
            </article>
          ))}
        </div>
      )}

      <h3 className="diagnostics-subhead">Findings</h3>
      {findings.length === 0 ? (
        <p className="muted" role="status">{note || "no diagnostics"}</p>
      ) : (
        <div className="project-list diagnostics-list">
          {findings.map((d, index) => (
            <article key={`${d.path}:${d.line}:${d.character}:${d.message}:${index}`} className="diagnostic-item">
              <header>
                <span className={`diag-severity sev-${(d.severity || "error").toLowerCase()}`}>{d.severity || "error"}</span>
                <code>{d.path}:{d.line}:{d.character}</code>
              </header>
              <p>{d.message || "(no message)"}</p>
              {(d.source || d.code) && (
                <small>
                  {d.source || ""}
                  {d.source && d.code ? " · " : ""}
                  {d.code || ""}
                </small>
              )}
            </article>
          ))}
        </div>
      )}
    </>
  );
}
