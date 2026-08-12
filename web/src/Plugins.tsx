import { useEffect, useState } from "react";
import {
  disablePlugin,
  enablePlugin,
  installPlugin,
  listPlugins,
  removePlugin,
  searchPlugins,
  trustPlugin,
  trustPreviewPlugin,
  untrustPlugin,
  updatePlugin,
  previewUpdatePlugin,
  type PluginCatalogHit,
  type PluginInfo,
  type PluginTrustPreview,
  type PluginUpdateReview,
} from "./pluginsApi";
import { listPanes, mountPane, unmountPane, type PaneInfo, type PaneSnapshot } from "./panesApi";
import { PaneView } from "./PaneView";

function trustClass(state?: string): string {
  switch (state) {
    case "trusted":
      return "plugin-trust trusted";
    case "stale":
      return "plugin-trust stale";
    case "none":
      return "plugin-trust none";
    default:
      return "plugin-trust passive";
  }
}

function statusClass(status?: string): string {
  switch (status) {
    case "enabled":
      return "plugin-status enabled";
    case "disabled":
      return "plugin-status disabled";
    case "invalid":
      return "plugin-status invalid";
    default:
      return "plugin-status";
  }
}

export function PluginsPanel({
  available,
  live: _live = true,
  panesAvailable = true,
  rootID,
}: {
  available: boolean;
  /** When false, destructive actions may still run (host config); reserved for UI hints. */
  live?: boolean;
  panesAvailable?: boolean;
  rootID?: string;
}) {
  void _live;
  const [items, setItems] = useState<PluginInfo[]>([]);
  const [panes, setPanes] = useState<PaneInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState("");
  const [notice, setNotice] = useState("");
  const [selected, setSelected] = useState<PluginInfo | null>(null);
  const [trustPrev, setTrustPrev] = useState<PluginTrustPreview | null>(null);
  const [updateRev, setUpdateRev] = useState<PluginUpdateReview | null>(null);
  const [installSrc, setInstallSrc] = useState("");
  const [registry, setRegistry] = useState("");
  const [searchQ, setSearchQ] = useState("");
  const [hits, setHits] = useState<PluginCatalogHit[]>([]);
  const [paneSnap, setPaneSnap] = useState<PaneSnapshot | null>(null);
  const [activePane, setActivePane] = useState("");

  const refresh = async () => {
    if (!available) return;
    setLoading(true);
    setError("");
    try {
      const res = await listPlugins();
      setItems(res.plugins || []);
      if (panesAvailable) {
        const pr = await listPanes().catch(() => ({ panes: [] as PaneInfo[] }));
        setPanes(pr.panes || []);
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, [available, panesAvailable]);

  if (!available) {
    return (
      <section className="unavailable" role="status">
        <strong>Plugins unavailable</strong>
        <p>The configured host did not provide this capability. No action was attempted.</p>
      </section>
    );
  }

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    setNotice("");
    try {
      await fn();
      await refresh();
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  const onTrust = async (p: PluginInfo) => {
    setBusy(`trust:${p.id}`);
    setNotice("");
    try {
      const prev = await trustPreviewPlugin(p.id, p.scope);
      setTrustPrev(prev);
      setSelected(p);
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  const confirmTrust = async () => {
    if (!selected || !trustPrev) return;
    await run(`trust:${selected.id}`, async () => {
      await trustPlugin(selected.id, selected.scope);
      setNotice(`Trusted ${selected.id}.`);
      setTrustPrev(null);
    });
  };

  const onUpdate = async (p: PluginInfo) => {
    setBusy(`update:${p.id}`);
    try {
      const rev = await previewUpdatePlugin(p.id, p.scope, registry || undefined);
      setUpdateRev(rev);
      setSelected(p);
    } catch (err) {
      window.alert((err as Error).message);
    } finally {
      setBusy("");
    }
  };

  const confirmUpdate = async () => {
    if (!selected || !updateRev) return;
    if (!window.confirm(`Apply update ${updateRev.oldVersion} → ${updateRev.newVersion} for ${selected.id}?`)) return;
    await run(`update:${selected.id}`, async () => {
      await updatePlugin(selected.id, selected.scope, registry || undefined, true);
      setNotice(`Updated ${selected.id}.`);
      setUpdateRev(null);
    });
  };

  const openPane = async (pane: PaneInfo) => {
    setBusy(`pane:${pane.id}`);
    setNotice("");
    try {
      if (activePane && activePane !== pane.id) {
        await unmountPane(activePane).catch(() => undefined);
      }
      const snap = await mountPane(pane.id, 40, 14, rootID);
      setPaneSnap(snap);
      setActivePane(pane.id);
    } catch (err) {
      window.alert((err as Error).message);
      setPaneSnap(null);
    } finally {
      setBusy("");
    }
  };

  const pluginPanes = (pluginId: string) => panes.filter((p) => p.pluginId === pluginId);

  return (
    <>
      <div className="plugin-head">
        <h2>Plugins</h2>
        <div className="plugin-actions">
          <button type="button" onClick={() => void refresh()} disabled={loading || Boolean(busy)}>
            Refresh
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
          <strong>Error</strong>
          <p>{error}</p>
        </section>
      )}

      <details className="plugin-install">
        <summary>Install / catalog</summary>
        <label>
          Source (path, git URL, or catalog:pkg)
          <input value={installSrc} onChange={(e) => setInstallSrc(e.target.value)} placeholder="./my-plugin or catalog:acme.pack" />
        </label>
        <label>
          Registry (catalog installs)
          <input value={registry} onChange={(e) => setRegistry(e.target.value)} placeholder="https://…" />
        </label>
        <div className="plugin-actions">
          <button
            type="button"
            disabled={!installSrc.trim() || Boolean(busy)}
            onClick={() =>
              void run("install", async () => {
                const res = await installPlugin(installSrc.trim(), "global", registry || undefined);
                setNotice(`Installed ${res.id}@${res.version || "?"}.`);
                setInstallSrc("");
              })
            }
          >
            Install
          </button>
        </div>
        <label>
          Search catalog
          <input value={searchQ} onChange={(e) => setSearchQ(e.target.value)} placeholder="query" />
        </label>
        <button
          type="button"
          disabled={!registry.trim() || Boolean(busy)}
          onClick={() =>
            void run("search", async () => {
              const res = await searchPlugins(registry.trim(), searchQ);
              setHits(res.hits || []);
            })
          }
        >
          Search
        </button>
        {hits.length > 0 && (
          <ul className="plugin-hits">
            {hits.map((h) => (
              <li key={h.id}>
                <strong>{h.id}</strong> {h.version && <span className="muted">v{h.version}</span>}
                {h.description && <p className="muted">{h.description}</p>}
                <button
                  type="button"
                  onClick={() =>
                    void run(`install:${h.id}`, async () => {
                      const res = await installPlugin(`catalog:${h.id}`, "global", registry || h.registry);
                      setNotice(`Installed ${res.id}.`);
                    })
                  }
                >
                  Install
                </button>
              </li>
            ))}
          </ul>
        )}
      </details>

      {trustPrev && (
        <section className="plugin-review" role="dialog" aria-label="Trust review">
          <h3>Trust review — {trustPrev.id}</h3>
          {trustPrev.digest && <p className="muted">digest: {trustPrev.digest}</p>}
          <ul>
            {(trustPrev.reviewLines || []).map((line, i) => (
              <li key={i}>
                <code>{line}</code>
              </li>
            ))}
          </ul>
          <div className="plugin-actions">
            <button type="button" onClick={() => void confirmTrust()} disabled={Boolean(busy)}>
              Grant trust
            </button>
            <button type="button" onClick={() => setTrustPrev(null)}>
              Cancel
            </button>
          </div>
        </section>
      )}

      {updateRev && (
        <section className="plugin-review" role="dialog" aria-label="Update review">
          <h3>
            Update {updateRev.id}: {updateRev.oldVersion} → {updateRev.newVersion}
          </h3>
          <pre className="plugin-summary">{updateRev.summary || "(no summary)"}</pre>
          {updateRev.trustInvalidated && <p className="warning-text">Trust will be invalidated.</p>}
          <div className="plugin-actions">
            <button type="button" onClick={() => void confirmUpdate()} disabled={Boolean(busy)}>
              Apply update
            </button>
            <button type="button" onClick={() => setUpdateRev(null)}>
              Cancel
            </button>
          </div>
        </section>
      )}

      {loading && !items.length ? (
        <p className="muted">Loading plugins…</p>
      ) : !items.length ? (
        <p className="muted">No plugins installed. Use Install above or <code>strike plugin install</code>.</p>
      ) : (
        <div className="plugin-list" aria-label="Installed plugins">
          {items.map((p) => (
            <article key={`${p.scope || ""}:${p.id}`} className="plugin-card">
              <header>
                <h3>{p.name || p.id}</h3>
                <span className={statusClass(p.status)}>{p.status || (p.enabled ? "enabled" : "disabled")}</span>
                <span className={trustClass(p.trustState)}>{p.trustState || "—"}</span>
              </header>
              <p className="muted">
                <code>{p.id}</code>
                {p.version ? ` @ ${p.version}` : ""}
                {p.scope ? ` · ${p.scope}` : ""}
              </p>
              {p.sourceLabel && <p className="muted">{p.sourceLabel}</p>}
              {p.loadError && <p className="error-text">{p.loadError}</p>}
              <p className="plugin-counts muted">
                agents {p.agents || 0} · skills {p.skills || 0} · workflows {p.workflows || 0} · themes {p.themes || 0} ·
                panes {p.panes || 0} · hooks {p.hooks || 0}
              </p>
              {p.capabilities && p.capabilities.length > 0 && (
                <p className="muted">caps: {p.capabilities.join(", ")}</p>
              )}
              {p.mcp && p.mcp.length > 0 && (
                <ul className="plugin-mcp">
                  {p.mcp.map((m) => (
                    <li key={m.name}>
                      MCP <code>{m.name}</code>
                      {m.command ? ` ${m.command}` : ""}
                      {m.envKeys && m.envKeys.length ? ` (env: ${m.envKeys.join(", ")})` : ""}
                    </li>
                  ))}
                </ul>
              )}
              {p.findings && p.findings.length > 0 && (
                <ul className="plugin-findings">
                  {p.findings.map((f, i) => (
                    <li key={i}>{f}</li>
                  ))}
                </ul>
              )}
              <div className="plugin-card-actions">
                {p.enabled ? (
                  <button
                    type="button"
                    disabled={Boolean(busy)}
                    onClick={() =>
                      void run(`dis:${p.id}`, async () => {
                        await disablePlugin(p.id, p.scope);
                        setNotice(`Disabled ${p.id}.`);
                      })
                    }
                  >
                    Disable
                  </button>
                ) : (
                  <button
                    type="button"
                    disabled={Boolean(busy)}
                    onClick={() =>
                      void run(`en:${p.id}`, async () => {
                        await enablePlugin(p.id, p.scope);
                        setNotice(`Enabled ${p.id}.`);
                      })
                    }
                  >
                    Enable
                  </button>
                )}
                {p.hasExecutable && p.trustState !== "trusted" && (
                  <button type="button" disabled={Boolean(busy)} onClick={() => void onTrust(p)}>
                    Trust…
                  </button>
                )}
                {p.hasExecutable && p.trustState === "trusted" && (
                  <button
                    type="button"
                    disabled={Boolean(busy)}
                    onClick={() =>
                      void run(`untrust:${p.id}`, async () => {
                        await untrustPlugin(p.id, p.scope);
                        setNotice(`Untrusted ${p.id}.`);
                      })
                    }
                  >
                    Untrust
                  </button>
                )}
                {p.updateAvailable && (
                  <button type="button" disabled={Boolean(busy)} onClick={() => void onUpdate(p)}>
                    Update {p.updateAvailable}
                  </button>
                )}
                <button
                  type="button"
                  className="danger"
                  disabled={Boolean(busy)}
                  onClick={() => {
                    if (!window.confirm(`Remove plugin “${p.id}”? This deletes the install.`)) return;
                    void run(`rm:${p.id}`, async () => {
                      await removePlugin(p.id, p.scope, true);
                      setNotice(`Removed ${p.id}.`);
                    });
                  }}
                >
                  Remove
                </button>
              </div>
              {panesAvailable && pluginPanes(p.id).length > 0 && (
                <div className="plugin-panes">
                  <strong>Panes</strong>
                  <ul>
                    {pluginPanes(p.id).map((pane) => (
                      <li key={pane.id}>
                        <button type="button" disabled={Boolean(busy)} onClick={() => void openPane(pane)}>
                          {pane.title || pane.id}
                        </button>
                        <span className="muted">
                          {" "}
                          {pane.mode}
                          {!pane.trusted ? " · blocked" : ""}
                          {pane.loadError ? ` · ${pane.loadError}` : ""}
                        </span>
                        <span className="muted"> · {pane.provenance}</span>
                      </li>
                    ))}
                  </ul>
                </div>
              )}
            </article>
          ))}
        </div>
      )}

      {paneSnap && (
        <section className="plugin-pane-host" aria-label={`Plugin pane ${paneSnap.id}`}>
          <header className="plugin-pane-head">
            <h3>
              {paneSnap.title || paneSnap.id}
              {paneSnap.status ? ` · ${paneSnap.status}` : ""}
            </h3>
            <button
              type="button"
              onClick={() => {
                if (activePane) void unmountPane(activePane).catch(() => undefined);
                setPaneSnap(null);
                setActivePane("");
              }}
            >
              Close pane
            </button>
          </header>
          {paneSnap.error ? (
            <section className="unavailable" role="alert">
              <strong>Pane error</strong>
              <p>{paneSnap.error}</p>
            </section>
          ) : (
            <div className="plugin-pane-body">
              <PaneView node={(paneSnap.view as import("./panesApi").PaneViewNode) || null} feeds={paneSnap.feeds} />
            </div>
          )}
        </section>
      )}
    </>
  );
}
