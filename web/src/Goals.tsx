import { FormEvent, useEffect, useState } from "react";
import {
  abortGoal, canAbort, canPause, canResume, canRun, getGoal, goalLog, listGoals,
  pauseGoal, resumeGoal, runGoal, setGoal,
  type Goal, type GoalIteration,
} from "./goals";

export function GoalsPanel({ available, live }: { available: boolean; live: boolean }) {
  const [items, setItems] = useState<Goal[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Goal | null>(null);
  const [log, setLog] = useState<GoalIteration[]>([]);
  const [busyID, setBusyID] = useState("");
  const [creating, setCreating] = useState(false);
  const [notice, setNotice] = useState("");

  const refresh = async () => {
    if (!available) return;
    setLoading(true); setError("");
    try {
      const res = await listGoals();
      const next = res.goals || [];
      setItems(next);
      if (selected) {
        const fresh = next.find((g) => g.id === selected.id);
        if (fresh) setSelected(fresh);
      }
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void refresh(); }, [available]);

  if (!available) {
    return <section className="unavailable" role="status"><strong>Goals unavailable</strong><p>The configured host did not provide this capability. No action was attempted.</p></section>;
  }

  const openDetail = async (id: string) => {
    setError(""); setNotice("");
    try {
      const [g, logRes] = await Promise.all([getGoal(id), goalLog(id)]);
      setSelected(g);
      setLog(logRes.iterations || []);
    } catch (err) {
      setError((err as Error).message);
    }
  };

  const control = async (id: string, action: "run" | "pause" | "resume" | "abort") => {
    if (!live) return;
    setBusyID(id); setError(""); setNotice("");
    try {
      let g: Goal;
      if (action === "run") g = await runGoal(id);
      else if (action === "pause") g = await pauseGoal(id);
      else if (action === "resume") g = await resumeGoal(id);
      else g = await abortGoal(id);
      setNotice(`${action}: ${g.status}${g.failReason ? ` — ${g.failReason}` : ""}`);
      if (selected?.id === id) {
        setSelected(g);
        const logRes = await goalLog(id).catch(() => ({ iterations: [] as GoalIteration[] }));
        setLog(logRes.iterations || []);
      }
      await refresh();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusyID("");
    }
  };

  return <>
    <div className="workflow-head">
      <h2>Goals</h2>
      <div className="workflow-actions">
        <button type="button" onClick={() => void refresh()} disabled={loading}>Refresh</button>
        <button type="button" onClick={() => setCreating(true)}>New</button>
      </div>
    </div>
    {!live && <p className="muted" role="status">Attach-only: list and inspect only. Run/pause/resume/abort require a live session.</p>}
    {notice && <p className="muted" role="status">{notice}</p>}
    {error && <section className="unavailable" role="alert"><strong>Unable to load</strong><p>{error}</p></section>}
    {loading && !items.length ? <p className="muted">Loading goals…</p> : null}
    {!loading && !items.length && !error ? <p className="muted">No goals yet. Create one to drive the loop harness.</p> : null}
    <div className="workflow-list">
      {items.map((item) => (
        <article key={item.id} className="workflow-card">
          <header>
            <h3>{item.description || item.id}</h3>
            <small>{item.status} · iter {item.lastIteration}/{item.maxIterations} · ${item.costUsd.toFixed(4)}</small>
          </header>
          {item.criteria?.length ? (
            <p className="workflow-grants">
              <span>Criteria</span>
              {item.criteria.map((c, i) => (
                <code key={i}>{c.satisfied ? "OK" : "…"} {c.check || c.description}</code>
              ))}
            </p>
          ) : null}
          {item.failReason && <p className="workflow-error" role="status">{item.failReason}</p>}
          <div className="workflow-card-actions">
            <button type="button" onClick={() => void openDetail(item.id)}>Details</button>
            <button type="button" disabled={!live || busyID === item.id || !canRun(item.status)} onClick={() => void control(item.id, "run")}>Run</button>
            <button type="button" disabled={!live || busyID === item.id || !canPause(item.status)} onClick={() => void control(item.id, "pause")}>Pause</button>
            <button type="button" disabled={!live || busyID === item.id || !canResume(item.status)} onClick={() => void control(item.id, "resume")}>Resume</button>
            <button type="button" disabled={!live || busyID === item.id || !canAbort(item.status)} onClick={() => void control(item.id, "abort")}>Abort</button>
          </div>
        </article>
      ))}
    </div>
    {selected && <GoalDetail goal={selected} log={log} onClose={() => { setSelected(null); setLog([]); }} live={live} busy={busyID === selected.id} onControl={(action) => void control(selected.id, action)} />}
    {creating && <GoalCreateDialog onClose={() => setCreating(false)} onCreated={async (g) => { setCreating(false); setNotice(`Created ${g.id}`); await refresh(); await openDetail(g.id); }} />}
  </>;
}

function GoalDetail({
  goal, log, onClose, live, busy, onControl,
}: {
  goal: Goal;
  log: GoalIteration[];
  onClose: () => void;
  live: boolean;
  busy: boolean;
  onControl: (action: "run" | "pause" | "resume" | "abort") => void;
}) {
  return <dialog className="workflow-dialog" open aria-labelledby="goal-detail-title">
    <div className="dialog-rule" />
    <h2 id="goal-detail-title">{goal.description}</h2>
    <dl className="goal-meta">
      <dt>ID</dt><dd><code>{goal.id}</code></dd>
      <dt>Status</dt><dd>{goal.status}</dd>
      <dt>Iteration</dt><dd>{goal.lastIteration} / {goal.maxIterations}</dd>
      <dt>Cost</dt><dd>${goal.costUsd.toFixed(4)}{goal.maxCostUsd > 0 ? ` / $${goal.maxCostUsd.toFixed(2)}` : ""}</dd>
      {goal.allowedTools?.length ? <><dt>Tools</dt><dd>{goal.allowedTools.join(", ")}</dd></> : null}
      {goal.failReason ? <><dt>Reason</dt><dd className="workflow-error">{goal.failReason}</dd></> : null}
    </dl>
    <h3>Criteria</h3>
    <ul className="goal-criteria">
      {(goal.criteria || []).map((c, i) => (
        <li key={i}><strong>{c.satisfied ? "OK" : "FAIL"}</strong> {c.check || c.description}</li>
      ))}
    </ul>
    <h3>Iteration log</h3>
    {log.length ? (
      <ol className="goal-log">
        {log.map((it) => (
          <li key={it.n}>
            <strong>#{it.n}</strong>
            <span>${it.costUsd.toFixed(4)}</span>
            <p>{it.summary || it.plan || "(empty)"}</p>
          </li>
        ))}
      </ol>
    ) : <p className="muted">No iterations committed yet.</p>}
    <div className="dialog-actions">
      <button type="button" onClick={onClose}>Close</button>
      <button type="button" disabled={!live || busy || !canRun(goal.status)} onClick={() => onControl("run")}>Run</button>
      <button type="button" disabled={!live || busy || !canPause(goal.status)} onClick={() => onControl("pause")}>Pause</button>
      <button type="button" disabled={!live || busy || !canResume(goal.status)} onClick={() => onControl("resume")}>Resume</button>
      <button type="button" disabled={!live || busy || !canAbort(goal.status)} onClick={() => onControl("abort")}>Abort</button>
    </div>
  </dialog>;
}

function GoalCreateDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (g: Goal) => void | Promise<void> }) {
  const [description, setDescription] = useState("");
  const [criteriaText, setCriteriaText] = useState("cmd: true");
  const [maxIterations, setMaxIterations] = useState("5");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    const criteria = criteriaText.split("\n").map((s) => s.trim()).filter(Boolean);
    if (!description.trim() || !criteria.length) {
      setError("Description and at least one criterion are required.");
      return;
    }
    setSaving(true); setError("");
    try {
      const max = Number(maxIterations);
      const g = await setGoal({
        description: description.trim(),
        criteria,
        maxIterations: Number.isFinite(max) && max > 0 ? max : undefined,
      });
      await onCreated(g);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return <dialog className="workflow-dialog" open aria-labelledby="goal-create-title" onCancel={(e) => { e.preventDefault(); onClose(); }}>
    <div className="dialog-rule" />
    <h2 id="goal-create-title">New goal</h2>
    <form className="workflow-builder" onSubmit={(e) => void submit(e)}>
      <fieldset>
        <legend>Definition</legend>
        <label>Description<input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="What should succeed?" required /></label>
        <label>Criteria<textarea value={criteriaText} onChange={(e) => setCriteriaText(e.target.value)} rows={4} placeholder={"cmd: pytest -q\ncmd: go test ./..."} aria-label="Criteria (one CheckSpec per line)" /></label>
        <label>Max iterations<input type="number" min={1} max={10000} value={maxIterations} onChange={(e) => setMaxIterations(e.target.value)} /></label>
      </fieldset>
      <p className="muted">Criteria use CheckSpec form: <code>cmd:</code>, <code>predicate:</code>, or <code>judge:</code>. Empty tool allowlist = evaluate-only (no side effects).</p>
      {error && <p className="workflow-error" role="alert">{error}</p>}
      <div className="dialog-actions">
        <button type="button" onClick={onClose}>Cancel</button>
        <button type="submit" disabled={saving}>{saving ? "Saving…" : "Create"}</button>
      </div>
    </form>
  </dialog>;
}
