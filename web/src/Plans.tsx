import { FormEvent, useEffect, useState } from "react";
import {
  addPlanSection, createPlan, getPlan, listPlans, planID, planOwner, planSections,
  planStatus, planTitle, planVersion, reopenPlan, sectionBody, sectionID, sectionTitle,
  setPlanStatus, updatePlanSection, updatePlanTitle,
  type Plan, type PlanMeta, type PlanSection,
} from "./plans";

export function PlansPanel({
  available, live, rootID,
}: {
  available: boolean;
  live: boolean;
  rootID: string;
}) {
  const [items, setItems] = useState<PlanMeta[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [selected, setSelected] = useState<Plan | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [notice, setNotice] = useState("");
  const [creating, setCreating] = useState(false);
  const [newTitle, setNewTitle] = useState("");

  const refresh = async () => {
    if (!available) return;
    setLoading(true); setError("");
    try {
      const res = await listPlans();
      setItems(res.plans || []);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void refresh(); }, [available]);

  if (!available) {
    return <section className="unavailable" role="status"><strong>Plans unavailable</strong><p>The configured host did not provide this capability. No action was attempted.</p></section>;
  }

  const openPlan = async (id: string) => {
    setDetailLoading(true); setError(""); setNotice("");
    try {
      const plan = await getPlan(id);
      setSelected(plan);
    } catch (err) {
      setError((err as Error).message);
      setSelected(null);
    } finally {
      setDetailLoading(false);
    }
  };

  const canMutate = live && Boolean(rootID) && selected && planOwner(selected) === rootID && planStatus(selected) !== "closed";
  const isOwner = Boolean(selected && rootID && planOwner(selected) === rootID);
  const isClosed = selected ? planStatus(selected) === "closed" : false;

  const onCreate = async (event: FormEvent) => {
    event.preventDefault();
    if (!live || !rootID) {
      window.alert("Select a live workspace root to create a plan.");
      return;
    }
    const title = newTitle.trim();
    if (!title) return;
    try {
      const plan = await createPlan(rootID, title);
      setNewTitle(""); setCreating(false);
      setNotice(`Created ${planTitle(plan)}`);
      await refresh();
      setSelected(plan);
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const applyPlan = async (next: Plan, label: string) => {
    setSelected(next);
    setNotice(label);
    await refresh();
  };

  const onRename = async () => {
    if (!selected || !canMutate) return;
    const title = window.prompt("Plan title", planTitle(selected));
    if (title === null) return;
    const trimmed = title.trim();
    if (!trimmed) return;
    try {
      const next = await updatePlanTitle(planID(selected), rootID, trimmed, planVersion(selected));
      await applyPlan(next, "Title updated");
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const onStatus = async (status: "approved" | "closed") => {
    if (!selected || !isOwner || isClosed) return;
    try {
      const next = await setPlanStatus(planID(selected), rootID, status, planVersion(selected));
      await applyPlan(next, `Status → ${status}`);
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const onReopen = async () => {
    if (!selected || !isOwner || !isClosed) return;
    try {
      const next = await reopenPlan(planID(selected), rootID, planVersion(selected));
      await applyPlan(next, "Reopened as draft");
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const onAddSection = async () => {
    if (!selected || !canMutate) return;
    const title = window.prompt("Section title");
    if (title === null) return;
    const trimmed = title.trim();
    if (!trimmed) return;
    const body = window.prompt("Section body (optional)", "") ?? "";
    try {
      const next = await addPlanSection(planID(selected), rootID, trimmed, body, planVersion(selected));
      await applyPlan(next, "Section added");
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const onEditSection = async (sec: PlanSection) => {
    if (!selected || !canMutate) return;
    const title = window.prompt("Section title", sectionTitle(sec));
    if (title === null) return;
    const body = window.prompt("Section body", sectionBody(sec));
    if (body === null) return;
    try {
      const next = await updatePlanSection(
        planID(selected), sectionID(sec), rootID,
        { title: title.trim() || sectionTitle(sec), body },
        planVersion(selected),
      );
      await applyPlan(next, "Section updated");
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  if (selected) {
    const sections = planSections(selected);
    return <div className="plans-panel">
      <div className="workflow-head">
        <h2>{planTitle(selected)}</h2>
        <div className="workflow-actions">
          <button type="button" onClick={() => { setSelected(null); setNotice(""); }}>Back</button>
          <button type="button" onClick={() => void openPlan(planID(selected))} disabled={detailLoading}>Refresh</button>
        </div>
      </div>
      <p className="plans-meta">
        <small>{planStatus(selected)}</small>
        <span>v{planVersion(selected)}</span>
        <span title={planOwner(selected)}>owner {planOwner(selected).slice(0, 12) || "—"}</span>
      </p>
      {!isOwner && live && <p className="muted" role="status">Read-only: this plan is owned by another root.</p>}
      {isClosed && isOwner && <p className="muted" role="status">Closed — reopen to edit content.</p>}
      {!live && <p className="muted" role="status">Historical session — read only.</p>}
      {notice && <p className="muted" role="status">{notice}</p>}
      {error && <section className="unavailable" role="alert"><strong>Unable to load</strong><p>{error}</p></section>}
      <div className="workflow-card-actions plans-actions">
        {canMutate && <button type="button" onClick={() => void onRename()}>Edit title</button>}
        {canMutate && planStatus(selected) === "draft" && <button type="button" onClick={() => void onStatus("approved")}>Approve</button>}
        {canMutate && planStatus(selected) === "approved" && <button type="button" onClick={() => void onStatus("closed")}>Close</button>}
        {canMutate && planStatus(selected) === "draft" && <button type="button" onClick={() => void onStatus("closed")}>Close</button>}
        {isOwner && isClosed && live && <button type="button" onClick={() => void onReopen()}>Reopen</button>}
        {canMutate && <button type="button" onClick={() => void onAddSection()}>Add section</button>}
      </div>
      <div className="project-list plans-sections">
        {sections.length ? sections.map((sec) => {
          const sid = sectionID(sec);
          const delegate = sec.DelegateStatus || sec.delegateStatus;
          return <article key={sid || sectionTitle(sec)}>
            <header className="plans-section-head">
              <h3>{sectionTitle(sec) || sid || "Section"}</h3>
              {canMutate && <button type="button" onClick={() => void onEditSection(sec)}>Edit</button>}
            </header>
            {delegate && <small>delegate: {delegate}{sec.DelegateDetail || sec.delegateDetail ? ` — ${sec.DelegateDetail || sec.delegateDetail}` : ""}</small>}
            {sectionBody(sec) ? <p>{sectionBody(sec)}</p> : <p className="muted">Empty section body.</p>}
          </article>;
        }) : <p className="muted">No sections yet.</p>}
      </div>
    </div>;
  }

  return <div className="plans-panel">
    <div className="workflow-head">
      <h2>Plans</h2>
      <div className="workflow-actions">
        <button type="button" onClick={() => void refresh()} disabled={loading}>Refresh</button>
        {live && <button type="button" onClick={() => setCreating((v) => !v)} disabled={!rootID}>New</button>}
      </div>
    </div>
    {notice && <p className="muted" role="status">{notice}</p>}
    {error && <section className="unavailable" role="alert"><strong>Unable to load</strong><p>{error}</p></section>}
    {creating && <form className="plans-create" onSubmit={(e) => void onCreate(e)}>
      <label>Title<input aria-label="New plan title" value={newTitle} onChange={(e) => setNewTitle(e.target.value)} placeholder="Plan title" autoFocus /></label>
      <button type="submit" disabled={!newTitle.trim()}>Create</button>
    </form>}
    {loading && !items.length ? <p className="muted">Loading plans…</p> : null}
    {!loading && !items.length && !error ? <p className="muted">No project plans.</p> : null}
    <div className="workflow-list">
      {items.map((item) => {
        const id = planID(item);
        return <article key={id} className="workflow-card">
          <header>
            <h3>{planTitle(item)}</h3>
            <small>{planStatus(item)}</small>
          </header>
          <p>{item.SectionCount ?? item.sectionCount ?? 0} sections · v{planVersion(item)}</p>
          <div className="workflow-card-actions">
            <button type="button" onClick={() => void openPlan(id)}>Open</button>
          </div>
        </article>;
      })}
    </div>
  </div>;
}
