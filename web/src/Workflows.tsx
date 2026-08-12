import { FormEvent, useEffect, useMemo, useRef, useState } from "react";
import {
  cloneDoc, emptyPhase, formatWorkflow, getWorkflowDocument, listWorkflows,
  phaseGrants, reviewDraft, saveWorkflow, scaffoldWorkflow, startWorkflow,
  stopWorkflow, validateWorkflow,
  type WorkflowDocument, type WorkflowDraftReview, type WorkflowPermission,
  type WorkflowPhaseDocument, type WorkflowSummary,
} from "./workflowsApi";

const gates = ["agent", "user", "check"] as const;
const actions = ["allow", "ask", "deny"] as const;
const scopes = ["project", "global"] as const;

function permLabel(p: WorkflowPermission) {
  return `${p.action} ${p.permission}${p.pattern ? ` ${p.pattern}` : ""}`;
}

export function WorkflowsPanel({
  available, draftsAvailable, live, rootID, activeWorkflow, agents, busy,
}: {
  available: boolean;
  draftsAvailable: boolean;
  live: boolean;
  rootID: string;
  activeWorkflow?: string;
  agents: string[];
  busy: boolean;
}) {
  const [items, setItems] = useState<WorkflowSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const [builder, setBuilder] = useState<{ doc: WorkflowDocument; scope: string; creating: boolean } | null>(null);
  const [startTarget, setStartTarget] = useState<WorkflowSummary | null>(null);
  const [notice, setNotice] = useState("");

  const refresh = async () => {
    if (!available) return;
    setLoading(true); setError("");
    try {
      const res = await listWorkflows();
      setItems(res.workflows || []);
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void refresh(); }, [available]);

  if (!available) {
    return <section className="unavailable" role="status"><strong>Workflows unavailable</strong><p>The configured host did not provide this capability. No action was attempted.</p></section>;
  }

  const openNew = async () => {
    try {
      const doc = await scaffoldWorkflow("my-workflow");
      setBuilder({ doc, scope: "project", creating: true });
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const openEdit = async (name: string) => {
    try {
      const doc = await getWorkflowDocument(name);
      const item = items.find((w) => w.name === name);
      const scope = item?.source === "global" ? "global" : "project";
      setBuilder({ doc: cloneDoc(doc), scope, creating: false });
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  const onStop = async () => {
    if (!live || busy) return;
    try {
      await stopWorkflow(rootID);
      setNotice("Workflow stopped (session history kept).");
    } catch (err) {
      window.alert((err as Error).message);
    }
  };

  return <>
    <div className="workflow-head">
      <h2>Workflows</h2>
      <div className="workflow-actions">
        <button type="button" onClick={() => void refresh()} disabled={loading}>Refresh</button>
        <button type="button" onClick={() => void openNew()}>New</button>
        {live && <button type="button" onClick={() => void onStop()} disabled={busy || !activeWorkflow}>Stop</button>}
      </div>
    </div>
    {activeWorkflow && <p className="workflow-active" role="status">Active: <strong>{activeWorkflow}</strong></p>}
    {notice && <p className="muted" role="status">{notice}</p>}
    {error && <section className="unavailable" role="alert"><strong>Unable to load</strong><p>{error}</p></section>}
    {loading && !items.length ? <p className="muted">Loading catalog…</p> : null}
    {!loading && !items.length && !error ? <p className="muted">No workflows loaded.</p> : null}
    <div className="workflow-list">
      {items.map((item) => (
        <article key={item.name} className={`workflow-card ${item.valid ? "" : "invalid"}`}>
          <header>
            <h3>{item.name}</h3>
            <small>{item.source || "unknown"} · {item.phases?.length || 0} phase(s){item.valid ? "" : " · invalid"}</small>
          </header>
          {item.description && <p>{item.description}</p>}
          {!item.valid && item.validationError && <p className="workflow-error" role="status">{item.validationError}</p>}
          {item.phases?.[0]?.permissions?.length ? (
            <p className="workflow-grants"><span>Phase 0 grants</span>{item.phases[0].permissions.map((p, i) => <code key={i}>{permLabel(p)}</code>)}</p>
          ) : null}
          <div className="workflow-card-actions">
            <button type="button" onClick={() => void openEdit(item.name)}>Edit</button>
            <button
              type="button"
              disabled={!live || busy || !item.valid || !(item.phases?.length)}
              title={!item.valid ? "Invalid workflows cannot be activated" : undefined}
              onClick={() => setStartTarget(item)}
            >Start</button>
          </div>
        </article>
      ))}
    </div>
    {builder && (
      <WorkflowBuilderDialog
        initial={builder.doc}
        scope={builder.scope}
        creating={builder.creating}
        agents={agents}
        draftsAvailable={draftsAvailable}
        onClose={() => setBuilder(null)}
        onSaved={async (name) => { setBuilder(null); setNotice(`Saved ${name} (not activated).`); await refresh(); }}
      />
    )}
    {startTarget && (
      <WorkflowStartDialog
        summary={startTarget}
        rootID={rootID}
        onClose={() => setStartTarget(null)}
        onStarted={() => { setNotice(`Starting ${startTarget.name}…`); setStartTarget(null); }}
      />
    )}
  </>;
}

function WorkflowStartDialog({
  summary, rootID, onClose, onStarted,
}: {
  summary: WorkflowSummary; rootID: string; onClose: () => void; onStarted: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => { ref.current?.showModal(); }, []);
  const p0 = summary.phases?.[0];
  const grants = p0?.permissions || [];
  const confirm = async () => {
    setBusy(true);
    try {
      await startWorkflow(summary.name, rootID);
      onStarted();
    } catch (err) {
      window.alert((err as Error).message);
      setBusy(false);
    }
  };
  return <dialog ref={ref} className="workflow-dialog" aria-labelledby="wf-start-title" onClose={onClose} onCancel={(e) => { e.preventDefault(); onClose(); }}>
    <div className="dialog-rule" />
    <h2 id="wf-start-title">Start {summary.name}</h2>
    <p className="muted">source {summary.source || "unknown"} · {summary.phases?.length || 0} phase(s)</p>
    {summary.description && <p>{summary.description}</p>}
    <section className="workflow-review" aria-label="Phase 0 grant review">
      <h3>Phase 0 grant review</h3>
      <p>{p0 ? <>{p0.name}{p0.agent ? ` @${p0.agent}` : ""} · gate {p0.gate || "agent"}{p0.gateCommand ? ` \`${p0.gateCommand}\`` : ""}</> : "No phases"}</p>
      {grants.length ? <ul>{grants.map((g, i) => <li key={i}><code>{permLabel(g)}</code></li>)}</ul> : <p className="muted">No phase permission overrides.</p>}
      <p className="workflow-warn">Starting applies phase permissions and may pin the phase agent. Session history is kept. Confirm only after reviewing grants.</p>
    </section>
    <div className="dialog-actions">
      <button type="button" onClick={onClose} disabled={busy}>Cancel</button>
      <button type="button" autoFocus disabled={busy || !summary.valid} onClick={() => void confirm()}>Confirm start</button>
    </div>
  </dialog>;
}

function WorkflowBuilderDialog({
  initial, scope: initialScope, creating, agents, draftsAvailable, onClose, onSaved,
}: {
  initial: WorkflowDocument; scope: string; creating: boolean; agents: string[];
  draftsAvailable: boolean; onClose: () => void; onSaved: (name: string) => void | Promise<void>;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [doc, setDoc] = useState(() => cloneDoc(initial));
  const [scope, setScope] = useState(initialScope);
  const [phaseIdx, setPhaseIdx] = useState(0);
  const [status, setStatus] = useState("");
  const [statusErr, setStatusErr] = useState(false);
  const [preview, setPreview] = useState("");
  const [grants, setGrants] = useState<WorkflowPermission[]>([]);
  const [review, setReview] = useState<WorkflowDraftReview | null>(null);
  const [saving, setSaving] = useState(false);
  const baseline = useMemo(() => JSON.stringify(initial), [initial]);
  const dirty = JSON.stringify(doc) !== baseline;

  useEffect(() => { ref.current?.showModal(); }, []);

  const phase = doc.phases[phaseIdx] || emptyPhase();

  const setPhase = (next: WorkflowPhaseDocument) => {
    setDoc((old) => {
      const phases = old.phases.slice();
      phases[phaseIdx] = next;
      return { ...old, phases };
    });
  };

  const runValidate = async () => {
    try {
      const res = await validateWorkflow(doc);
      if (res.ok) {
        setStatus("Valid"); setStatusErr(false);
      } else {
        setStatus(res.error || "Invalid"); setStatusErr(true);
      }
    } catch (err) {
      setStatus((err as Error).message); setStatusErr(true);
    }
  };

  const runPreview = async () => {
    try {
      const [fmt, g] = await Promise.all([
        formatWorkflow(doc),
        phaseGrants(doc, phaseIdx),
      ]);
      setPreview(fmt.json);
      setGrants(g.grants || []);
      if (draftsAvailable) {
        const rev = await reviewDraft(fmt.json);
        setReview(rev);
        if (!rev.valid) {
          setStatus(rev.validationError || "Draft invalid"); setStatusErr(true);
        } else if (rev.hasWidening || rev.hasChecks) {
          setStatus(rev.hasWidening ? "Review widening grants before save" : "Review executable checks before save");
          setStatusErr(false);
        } else {
          setStatus("Preview ready"); setStatusErr(false);
        }
      } else {
        setReview(null);
        setStatus("Preview ready"); setStatusErr(false);
      }
    } catch (err) {
      setStatus((err as Error).message); setStatusErr(true);
    }
  };

  const onSave = async (force: boolean) => {
    setSaving(true);
    try {
      const v = await validateWorkflow(doc);
      if (!v.ok) {
        setStatus(v.error || "Cannot save invalid workflow"); setStatusErr(true); setSaving(false);
        return;
      }
      // Structured review before save when drafts capability is present.
      if (draftsAvailable) {
        const fmt = await formatWorkflow(doc);
        const rev = await reviewDraft(fmt.json);
        setReview(rev);
        setPreview(fmt.json);
        if (!rev.valid) {
          setStatus(rev.validationError || "Cannot save invalid draft"); setStatusErr(true); setSaving(false);
          return;
        }
      }
      const res = await saveWorkflow(doc, scope, force);
      if (res.activated) {
        // Server contract forbids activation on save — surface if violated.
        window.alert("Unexpected: save reported activation. Workflow was not started by the client.");
      }
      await onSaved(doc.name);
    } catch (err) {
      const message = (err as Error).message;
      if (!force && /already exists/i.test(message)) {
        if (window.confirm(`${message}\n\nOverwrite existing file?`)) {
          setSaving(false);
          await onSave(true);
          return;
        }
      }
      setStatus(message); setStatusErr(true);
    } finally {
      setSaving(false);
    }
  };

  const requestClose = () => {
    if (dirty && !window.confirm("Discard unsaved workflow edits?")) return;
    onClose();
  };

  const addPhase = () => {
    setDoc((old) => ({ ...old, phases: [...old.phases, { ...emptyPhase(), name: `phase-${old.phases.length + 1}` }] }));
    setPhaseIdx(doc.phases.length);
  };
  const removePhase = () => {
    if (doc.phases.length <= 1) return;
    setDoc((old) => {
      const phases = old.phases.filter((_, i) => i !== phaseIdx);
      return { ...old, phases };
    });
    setPhaseIdx((i) => Math.max(0, Math.min(i, doc.phases.length - 2)));
  };
  const movePhase = (dir: -1 | 1) => {
    const j = phaseIdx + dir;
    if (j < 0 || j >= doc.phases.length) return;
    setDoc((old) => {
      const phases = old.phases.slice();
      [phases[phaseIdx], phases[j]] = [phases[j], phases[phaseIdx]];
      return { ...old, phases };
    });
    setPhaseIdx(j);
  };

  const updatePerm = (index: number, patch: Partial<WorkflowPermission>) => {
    const permissions = (phase.permissions || []).slice();
    permissions[index] = { ...permissions[index], ...patch };
    setPhase({ ...phase, permissions });
  };

  return <dialog ref={ref} className="workflow-dialog wide" aria-labelledby="wf-builder-title" onClose={requestClose} onCancel={(e) => { e.preventDefault(); requestClose(); }}>
    <div className="dialog-rule" />
    <h2 id="wf-builder-title">{creating ? "New workflow" : `Edit ${initial.name}`}</h2>
    <form className="workflow-builder" onSubmit={(e: FormEvent) => { e.preventDefault(); void onSave(false); }}>
      <fieldset className="workflow-meta">
        <legend>Document</legend>
        <label>Name<input aria-label="Workflow name" value={doc.name} disabled={!creating} onChange={(e) => setDoc({ ...doc, name: e.target.value })} /></label>
        <label>Description<input aria-label="Workflow description" value={doc.description || ""} onChange={(e) => setDoc({ ...doc, description: e.target.value })} /></label>
        <label>Scope<select aria-label="Save scope" value={scope} onChange={(e) => setScope(e.target.value)}>{scopes.map((s) => <option key={s} value={s}>{s}</option>)}</select></label>
      </fieldset>

      <div className="workflow-phases" role="list" aria-label="Phases">
        {doc.phases.map((p, i) => (
          <button type="button" role="listitem" key={`${p.name}-${i}`} className={i === phaseIdx ? "active" : ""} onClick={() => setPhaseIdx(i)}>
            {i}. {p.name || "(unnamed)"}
          </button>
        ))}
        <div className="workflow-phase-tools">
          <button type="button" onClick={addPhase}>+ Phase</button>
          <button type="button" onClick={removePhase} disabled={doc.phases.length <= 1}>Remove</button>
          <button type="button" onClick={() => movePhase(-1)} disabled={phaseIdx === 0}>↑</button>
          <button type="button" onClick={() => movePhase(1)} disabled={phaseIdx >= doc.phases.length - 1}>↓</button>
        </div>
      </div>

      <fieldset className="workflow-phase-fields">
        <legend>Phase {phaseIdx}</legend>
        <label>Name<input aria-label="Phase name" value={phase.name} onChange={(e) => setPhase({ ...phase, name: e.target.value })} /></label>
        <label>Description<input aria-label="Phase description" value={phase.description || ""} onChange={(e) => setPhase({ ...phase, description: e.target.value })} /></label>
        <label>Agent<select aria-label="Phase agent" value={phase.agent || ""} onChange={(e) => setPhase({ ...phase, agent: e.target.value })}>
          <option value="">(default)</option>
          {agents.map((a) => <option key={a} value={a}>{a}</option>)}
        </select></label>
        <label>Gate<select aria-label="Phase gate" value={phase.gate || "agent"} onChange={(e) => setPhase({ ...phase, gate: e.target.value })}>
          {gates.map((g) => <option key={g} value={g}>{g}</option>)}
        </select></label>
        {(phase.gate || "agent") === "check" && (
          <label>Check command<input aria-label="Gate check command" value={phase.gateCommand || ""} onChange={(e) => setPhase({ ...phase, gateCommand: e.target.value })} placeholder="make test" /></label>
        )}
        <label>Context<textarea aria-label="Phase context" value={phase.context || ""} onChange={(e) => setPhase({ ...phase, context: e.target.value })} rows={3} /></label>
      </fieldset>

      <fieldset className="workflow-perms">
        <legend>Permissions (phase grants)</legend>
        {(phase.permissions || []).map((p, i) => (
          <div className="perm-row" key={i}>
            <input aria-label={`Permission name ${i + 1}`} value={p.permission} onChange={(e) => updatePerm(i, { permission: e.target.value })} placeholder="bash" />
            <input aria-label={`Permission pattern ${i + 1}`} value={p.pattern || ""} onChange={(e) => updatePerm(i, { pattern: e.target.value })} placeholder="*" />
            <select aria-label={`Permission action ${i + 1}`} value={p.action} onChange={(e) => updatePerm(i, { action: e.target.value })}>
              {actions.map((a) => <option key={a} value={a}>{a}</option>)}
            </select>
            <button type="button" aria-label={`Remove permission ${i + 1}`} onClick={() => setPhase({ ...phase, permissions: (phase.permissions || []).filter((_, j) => j !== i) })}>×</button>
          </div>
        ))}
        <button type="button" onClick={() => setPhase({ ...phase, permissions: [...(phase.permissions || []), { permission: "bash", pattern: "*", action: "ask" }] })}>+ Grant</button>
      </fieldset>

      {(preview || review || grants.length > 0) && (
        <section className="workflow-review" aria-label="Validation and grant preview">
          <h3>Review</h3>
          {grants.length > 0 && <p className="workflow-grants"><span>Phase {phaseIdx} grants</span>{grants.map((g, i) => <code key={i}>{permLabel(g)}</code>)}</p>}
          {review?.hasChecks && <p className="workflow-warn">Executable check gates require review before save.</p>}
          {review?.hasWidening && <p className="workflow-warn">Permission widening vs baseline — review carefully.</p>}
          {review?.phases?.map((p, i) => (
            <div key={i} className="workflow-review-phase">
              <strong>{p.name}</strong>
              {p.checkHighlighted && p.gateCommand && <code className="check-cmd">{p.gateCommand}</code>}
              {p.widening?.length ? <ul>{p.widening.map((w, j) => <li key={j}><code>{permLabel(w)}</code> (widening)</li>)}</ul> : null}
            </div>
          ))}
          {preview && <pre className="workflow-json" aria-label="Canonical JSON preview">{preview}</pre>}
        </section>
      )}

      {status && <p className={statusErr ? "workflow-error" : "muted"} role="status">{status}</p>}
      <p className="muted">Save writes the workflow file only — it never starts the workflow.</p>
      <div className="dialog-actions">
        <button type="button" onClick={requestClose} disabled={saving}>Cancel</button>
        <button type="button" onClick={() => void runValidate()} disabled={saving}>Validate</button>
        <button type="button" onClick={() => void runPreview()} disabled={saving}>Preview / review</button>
        <button type="submit" disabled={saving}>Save</button>
      </div>
    </form>
  </dialog>;
}
