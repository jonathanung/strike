import { useEffect, useState } from "react";
import { disableMCP, listMCP, retryMCP, type MCPServerStatus } from "./mcp";

function stateClass(state: string): string {
  switch (state) {
    case "up":
      return "mcp-state up";
    case "disabled":
      return "mcp-state disabled";
    case "error":
      return "mcp-state error";
    default:
      return "mcp-state down";
  }
}

export function MCPPanel({ available }: { available: boolean }) {
  const [items, setItems] = useState<MCPServerStatus[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");

  const refresh = async () => {
    if (!available) return;
    setLoading(true);
    setError("");
    try {
      const res = await listMCP();
      setItems(res.servers || []);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, [available]);

  if (!available) {
    return (
      <section className="unavailable" role="status">
        <strong>MCP unavailable</strong>
        <p>The configured host did not provide this capability. No action was attempted.</p>
      </section>
    );
  }

  const onRetry = async (name?: string) => {
    const key = name || "*";
    setBusy(key);
    setNotice("");
    try {
      await retryMCP(name);
      setNotice(name ? `Retried ${name}.` : "Retry complete for non-up servers.");
      await refresh();
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  const onDisable = async (name: string) => {
    if (!window.confirm(`Disable MCP server “${name}” and unregister its tools?`)) return;
    setBusy(name);
    setNotice("");
    try {
      await disableMCP(name);
      setNotice(`Disabled ${name}.`);
      await refresh();
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <div className="mcp-head">
        <h2>MCP servers</h2>
        <div className="mcp-actions">
          <button type="button" onClick={() => void refresh()} disabled={loading || Boolean(busy)}>
            Refresh
          </button>
          <button type="button" onClick={() => void onRetry()} disabled={loading || Boolean(busy) || !items.length}>
            Retry all
          </button>
        </div>
      </div>
      {notice && (
        <p className="muted" role="status">
          {notice}
        </p>
      )}
      {error && (
        <section className="unavailable" role="alert">
          <strong>Unable to load</strong>
          <p>{error}</p>
        </section>
      )}
      {loading && !items.length ? <p className="muted">Loading MCP status…</p> : null}
      {!loading && !items.length && !error ? (
        <p className="muted">No MCP servers configured. Add servers in ~/.strike/mcp.jsonc.</p>
      ) : null}
      <div className="mcp-list" role="list" aria-label="MCP servers">
        {items.map((server) => {
          const transport = server.transport || "stdio";
          const acting = busy === server.name || busy === "*";
          return (
            <article key={server.name} className="mcp-card" role="listitem">
              <header>
                <h3>{server.name}</h3>
                <span className={stateClass(server.state)}>{server.state}</span>
              </header>
              <p>
                <span>{transport}</span>
                {server.command ? (
                  <>
                    {" · "}
                    <code>{server.command}</code>
                  </>
                ) : null}
                {" · "}
                tools={server.toolCount ?? 0}
              </p>
              {server.error && server.state !== "disabled" ? (
                <p className="mcp-error">{server.error}</p>
              ) : null}
              {server.tools && server.tools.length > 0 ? (
                <div className="mcp-tools" aria-label={`${server.name} tools`}>
                  {server.tools.map((tool) => (
                    <code key={tool}>{tool}</code>
                  ))}
                </div>
              ) : null}
              <div className="mcp-card-actions">
                <button
                  type="button"
                  disabled={acting || server.state === "disabled"}
                  onClick={() => void onRetry(server.name)}
                >
                  Retry
                </button>
                <button
                  type="button"
                  disabled={acting || server.state === "disabled"}
                  onClick={() => void onDisable(server.name)}
                >
                  Disable
                </button>
              </div>
            </article>
          );
        })}
      </div>
    </>
  );
}
