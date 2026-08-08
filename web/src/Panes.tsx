import { useEffect, useState } from "react";
import { PaneView } from "./PaneView";
import {
  listPanes,
  mountPane,
  paneInput,
  paneSnapshot,
  unmountPane,
  type PaneInfo,
  type PaneSnapshot,
} from "./panes";

export function PanesPanel({ available, focusId }: { available: boolean; focusId?: string }) {
  const [items, setItems] = useState<PaneInfo[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [active, setActive] = useState("");
  const [snap, setSnap] = useState<PaneSnapshot | null>(null);
  const [busy, setBusy] = useState(false);

  const refresh = async () => {
    if (!available) return;
    setLoading(true);
    setError("");
    try {
      const res = await listPanes();
      setItems(res.panes || []);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void refresh();
  }, [available]);


  useEffect(() => {
    if (!active) {
      setSnap(null);
      return;
    }
    let cancelled = false;
    const tick = async () => {
      try {
        const s = await paneSnapshot(active);
        if (!cancelled) setSnap(s);
      } catch {
        /* keep last */
      }
    };
    void tick();
    const id = window.setInterval(() => void tick(), 500);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [active]);

  if (!available) {
    return (
      <section className="unavailable" role="status">
        <strong>Plugin panes unavailable</strong>
        <p>The configured host did not provide this capability. No action was attempted.</p>
      </section>
    );
  }

  const open = async (p: PaneInfo) => {
    setBusy(true);
    setError("");
    try {
      const s = await mountPane(p.id);
      setActive(p.id);
      setSnap(s);
    } catch (err) {
      setError((err as Error).message);
      setActive(p.id);
      setSnap({
        id: p.id,
        mode: p.mode,
        title: p.title,
        error: p.loadError || (err as Error).message,
      });
    } finally {
      setBusy(false);
    }
  };


  useEffect(() => {
    if (!focusId || !items.length) return;
    const hit = items.find((p) => p.id === focusId);
    if (hit && active !== focusId) void open(hit);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusId, items]);

  const close = async () => {
    if (!active) return;
    setBusy(true);
    try {
      await unmountPane(active);
    } catch {
      /* ignore */
    } finally {
      setActive("");
      setSnap(null);
      setBusy(false);
    }
  };

  const onKey = async (key: string) => {
    if (!active || !snap || snap.mode !== "process") return;
    try {
      await paneInput(active, { kind: "key", key, mods: [] });
      const s = await paneSnapshot(active);
      setSnap(s);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  return (
    <>
      <div className="mcp-head">
        <h2>Plugin panes</h2>
        <div className="mcp-actions">
          <button type="button" onClick={() => void refresh()} disabled={loading || busy}>
            Refresh
          </button>
          {active && (
            <button type="button" onClick={() => void close()} disabled={busy}>
              Close pane
            </button>
          )}
        </div>
      </div>
      {error && (
        <section className="unavailable" role="alert">
          <strong>Error</strong>
          <p>{error}</p>
        </section>
      )}
      {!active && (
        <>
          {loading && !items.length ? (
            <p className="muted">Loading…</p>
          ) : items.length === 0 ? (
            <p className="muted">No enabled plugin panes. Install/enable a pane plugin via the plugins tab.</p>
          ) : (
            <div className="mcp-list">
              {items.map((p) => (
                <article key={p.id} className="mcp-card">
                  <header>
                    <h3>{p.title || p.id}</h3>
                    <span className="mcp-state">{p.mode}</span>
                  </header>
                  <p className="muted">{p.provenance}</p>
                  {p.loadError && <p className="mcp-error">{p.loadError}</p>}
                  <div className="mcp-card-actions">
                    <button
                      type="button"
                      disabled={busy || Boolean(p.loadError && p.mode === "process" && !p.trusted)}
                      onClick={() => void open(p)}
                    >
                      Open
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </>
      )}
      {active && snap && (
        <section
          className="plugin-pane-surface"
          tabIndex={0}
          aria-label={snap.title || snap.id}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              void onKey("enter");
            } else if (e.key === "Escape") {
              e.preventDefault();
              void onKey("esc");
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              void onKey("up");
            } else if (e.key === "ArrowDown") {
              e.preventDefault();
              void onKey("down");
            } else if (e.key.length === 1) {
              void onKey(e.key);
            }
          }}
        >
          <header className="plugin-pane-chrome">
            <strong>{snap.title || snap.id}</strong>
            {snap.status && <span className="muted">{snap.status}</span>}
            <span className="muted">{snap.mode}</span>
          </header>
          {snap.error ? (
            <div className="pane-error" role="alert">
              <strong>pane error</strong>
              <p>{snap.error}</p>
              <p className="muted">disable via plugins tab · trust process panes before mount</p>
            </div>
          ) : (
            <div className="plugin-pane-body">
              <PaneView node={snap.view} feeds={snap.feeds} />
            </div>
          )}
        </section>
      )}
    </>
  );
}
