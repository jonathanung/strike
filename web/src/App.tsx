import { FormEvent, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { bootstrap, createRoot, historicalConnection, liveConnection, request, resumeRoot, roots as loadRoots, sendOp, sessions as loadSessions } from "./api";
import { buildExportMarkdown, defaultExportFilename, downloadTextFile } from "./exportMarkdown";
import { clearQueue, editQueuedText, moveQueuedAt, removeQueuedAt, type QueuedPrompt } from "./queueOps";
import { initialState, reduceEvent } from "./reducer";
import { formatCostNotice, formatSlashHelp, resolveSlash, WEB_SLASH_COMMANDS } from "./slash";
import { Transcript } from "./Transcript";
import type { ActiveRoot, Bootstrap, ImageAttachment, Session, Status } from "./types";
import { PlansPanel } from "./Plans";
import { WorkflowsPanel } from "./Workflows";
import "./styles.css";

type InspectorTab = "context" | "files" | "memory" | "issues" | "plans" | "workflows";
type Completion = { label: string; detail: string; insert: string };
type ChangedFile = { path: string; added: number; deleted: number; diff: string };
type MemoryEntry = { Key?: string; key?: string; Value?: string; value?: string; Tags?: string[]; tags?: string[] };
type IssueEntry = { ID?: number; id?: number; Title?: string; title?: string; Body?: string; body?: string; Status?: string; status?: string };
const runtimeValues = { effort: ["low", "medium", "high", "xhigh"], autonomy: ["supervised", "agent", "checks"], permission: ["default", "plan", "soft-approve", "accept-edits", "yolo"] };
const slashCommands: Completion[] = WEB_SLASH_COMMANDS;

const op = (type: string, data?: unknown, rootID?: string) => sendOp(type, data, rootID).catch((error) => window.alert(error.message));

function Field({ label, value, values, disabled, onChange }: { label: string; value?: string; values: string[]; disabled?: boolean; onChange: (value: string) => void }) {
  return <label className="runtime-field"><span>{label}</span><select aria-label={label} value={value || ""} disabled={disabled} onChange={(event) => onChange(event.target.value)}><option value="">—</option>{values.map((item) => <option key={item}>{item}</option>)}</select></label>;
}

function BlockingDialog({ title, children }: { title: string; children: React.ReactNode }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => { ref.current?.showModal(); }, []);
  return <dialog ref={ref} aria-labelledby="blocking-title" onCancel={(event) => event.preventDefault()}><div className="dialog-rule" /><h2 id="blocking-title">{title}</h2>{children}</dialog>;
}

const readAttachment = (file: File) => new Promise<ImageAttachment>((resolve, reject) => {
  if (!file.type.startsWith("image/")) return reject(new Error(`${file.name}: only image attachments are supported by the engine`));
  const reader = new FileReader();
  reader.onerror = () => reject(new Error(`${file.name}: read failed`));
  reader.onload = () => resolve({ name: file.name, mime: file.type, data: String(reader.result).split(",")[1] || "" });
  reader.readAsDataURL(file);
});

export default function App() {
  const [boot, setBoot] = useState<Bootstrap>();
  const [sessions, setSessions] = useState<Session[]>([]);
  const [activeRoots, setActiveRoots] = useState<ActiveRoot[]>([]);
  const [liveID, setLiveID] = useState("");
  const [activeRootID, setActiveRootID] = useState("");
  const [selectedID, setSelectedID] = useState("");
  const [selectedIsLive, setSelectedIsLive] = useState(false);
  const [navTab, setNavTab] = useState<"active" | "history">("active");
  const [historySearch, setHistorySearch] = useState("");
  const [state, dispatch] = useReducer(reduceEvent, undefined, initialState);
  const [transport, setTransport] = useState("connecting");
  const [draft, setDraft] = useState("");
  const [queue, setQueue] = useState<QueuedPrompt[]>([]);
  const [queueEdit, setQueueEdit] = useState<{ index: number; text: string } | null>(null);
  const queueRef = useRef<HTMLOListElement>(null);
  const queueEditCancel = useRef(false);
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [inspector, setInspector] = useState<InspectorTab>("context");
  const [inspectorOpen, setInspectorOpen] = useState(true);
  const [navOpen, setNavOpen] = useState(true);
  const [navWidth, setNavWidth] = useState(240);
  const [inspectorWidth, setInspectorWidth] = useState(340);
  const [projectData, setProjectData] = useState<unknown>();
  const [projectLoading, setProjectLoading] = useState(false);
  const [expandedDiffs, setExpandedDiffs] = useState<Set<string>>(new Set());
  const [providers, setProviders] = useState<string[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [history, setHistory] = useState<string[]>([]);
  const [fast, setFast] = useState(false);
  const [runtimeOpen, setRuntimeOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const endRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const refreshSessions = () => loadSessions().then((list) => { setSessions(list.sessions || []); setLiveID(list.liveId || ""); return list; });
  const refreshRoots = () => loadRoots().then((r) => { setActiveRoots(r.roots || []); setActiveRootID(r.activeId || ""); return r; }).catch(() => {});
  useEffect(() => {
    // roots is optional (attach-only returns 503) — do not fail the whole boot.
    Promise.all([
      bootstrap(),
      loadSessions(),
      loadRoots().catch(() => ({ roots: [] as ActiveRoot[], activeId: "" })),
    ]).then(([nextBoot, list, r]) => {
      setBoot(nextBoot);
      setTransport("connected");
      if (nextBoot.status) dispatch({ type: "status", data: nextBoot.status });
      setSessions(list.sessions || []);
      const hasRoots = Boolean(nextBoot.capabilities.roots && r.roots?.length);
      setLiveID(list.liveId || nextBoot.status?.sessionId || "");
      const rootsArr = hasRoots ? (r.roots || []) : [];
      setActiveRoots(rootsArr);
      setActiveRootID(r.activeId || list.liveId || nextBoot.status?.sessionId || "");
      const firstLive = rootsArr[0]?.id || (nextBoot.capabilities.roots ? "" : list.liveId) || "";
      const firstID = firstLive || list.sessions?.[0]?.id || "";
      setSelectedID(firstID);
      setSelectedIsLive(Boolean(firstLive && firstID === firstLive));
      setNavTab(firstLive ? "active" : "history");
      if (nextBoot.capabilities.auth) request<{ providers: Array<{ Name?: string; name?: string }> }>("/v1/providers").then((v) => setProviders(v.providers.map((p) => p.Name || p.name || "").filter(Boolean))).catch(() => {});
      if (nextBoot.capabilities.history) request<{ entries: string[] }>("/v1/history").then((v) => setHistory(v.entries || [])).catch(() => {});
    }).catch((error) => setTransport(error.message));
  }, []);

  useEffect(() => {
    if (!selectedID) return;
    dispatch({ type: "workspace.reset", data: { sessionId: selectedID } });
    if (!selectedIsLive) return historicalConnection(selectedID, dispatch, (message) => setTransport(message));
    const live = liveConnection(selectedID, dispatch, setTransport);
    return () => live.close();
  }, [selectedID, selectedIsLive]);
  useEffect(() => { endRef.current?.scrollIntoView({ block: "end" }); }, [state.items]);
  useEffect(() => {
    if (state.status.busy || !queue.length || !selectedIsLive) return;
    const [next, ...rest] = queue; setQueue(rest); void op("user.input", { text: next.text, images: next.images.map(({ mime, data }) => ({ mime, data })) }, selectedID);
  }, [state.status.busy, queue, selectedIsLive, selectedID]);

  const isLive = Boolean(selectedIsLive && selectedID && !boot?.attachOnly);
  const runtimeBusy = !isLive || state.status.busy;
  const runtimeSummary = [state.status.effort, state.status.autonomy, state.status.permissionMode, fast ? "fast" : ""].filter(Boolean).join(" · ");
  const children = useMemo(() => Object.entries(state.children), [state.children]);
  const shellStyle = { "--nav-width": navOpen ? `${navWidth}px` : "0px", "--inspector-width": inspectorOpen ? `${inspectorWidth}px` : "0px" } as React.CSSProperties;
  const completions = useMemo(() => {
    const token = draft.split(/\s/).at(-1) || "";
    if (token.startsWith("/")) return [...slashCommands, ...(boot?.skills || []).map((skill) => ({ label: `/${skill.name}`, detail: skill.description || "Skill", insert: `/${skill.name}` }))].filter((item) => item.label.startsWith(token));
    return [];
  }, [draft, boot]);

  const notice = (title: string, body: string) => dispatch({ type: "local.system", time: String(Date.now()), data: { title, text: body } });
  const exportSession = () => {
    const md = buildExportMarkdown(state.items, {
      sessionId: selectedID || state.status.sessionId,
      title: sessions.find((s) => s.id === selectedID)?.title,
      provider: state.status.provider,
      model: state.status.model,
      agent: state.status.agent,
    });
    downloadTextFile(defaultExportFilename(selectedID || state.status.sessionId), md);
  };
  const execute = (text: string, attached: ImageAttachment[]) => {
    const command = text.trim();
    const skillNames = (boot?.skills || []).map((s) => s.name);
    const resolved = resolveSlash(command, skillNames);
    switch (resolved.kind) {
      case "pass":
        void op("user.input", { text: command, images: attached.map(({ mime, data }) => ({ mime, data })) }, selectedID);
        return;
      case "unknown":
        notice("Unknown command", `${resolved.command} is not available in the web cockpit. Type /help for the list.`);
        return;
      case "usage":
        notice("Usage", resolved.message);
        return;
      case "help":
        notice("Help", formatSlashHelp(boot?.skills || []));
        return;
      case "export":
        exportSession();
        return;
      case "queue":
        queueRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
        notice("Queue", queue.length ? `${queue.length} prompt(s) queued — reorder, edit, or clear below the composer.` : "Queue is empty. Prompts sent while the agent is busy land here.");
        return;
      case "cost":
        notice("Cost", formatCostNotice(state.status));
        return;
      case "copy": {
        const last = [...state.items].reverse().find((item) => item.kind === "assistant" && item.text.trim());
        if (!last) { notice("Copy", "No assistant reply to copy."); return; }
        void navigator.clipboard?.writeText(last.text).then(
          () => notice("Copy", "Last assistant reply copied to clipboard."),
          () => notice("Copy", "Clipboard unavailable in this browser."),
        );
        return;
      }
      case "fork":
        void sessionAction("fork");
        return;
      case "rename": {
        if (resolved.title) {
          if (!boot?.capabilities.sessions) { notice("Rename", "Sessions capability unavailable."); return; }
          void request(`/v1/sessions/${encodeURIComponent(selectedID)}`, { method: "PATCH", body: JSON.stringify({ title: resolved.title }) }).then(() => refreshSessions()).catch((error) => window.alert(error.message));
          return;
        }
        void sessionAction("rename");
        return;
      }
      case "fast": {
        const enabled = resolved.enabled === undefined ? !fast : resolved.enabled;
        setFast(enabled);
        void op("set.fast", { enabled }, selectedID);
        return;
      }
      case "op": {
        let data = resolved.data;
        if (resolved.type === "select.model" && data && typeof data === "object") {
          const body = { ...(data as Record<string, unknown>) };
          if (!body.provider) body.provider = state.status.provider;
          data = body;
        }
        void op(resolved.type, data, selectedID);
        return;
      }
    }
  };
  const submit = (event: FormEvent) => {
    event.preventDefault(); const text = draft.trim(); if (!text || !isLive) return;
    if (state.status.busy) setQueue((items) => [...items, { text, images }]); else execute(text, images);
    setDraft(""); setImages([]);
  };
  const selectCompletion = (item: Completion) => setDraft((value) => `${value.slice(0, value.lastIndexOf(value.split(/\s/).at(-1) || ""))}${item.insert} `);
  const attach = async (files: FileList | null) => { if (!files) return; try { const added = await Promise.all([...files].map(readAttachment)); setImages((old) => [...old, ...added]); } catch (error) { window.alert((error as Error).message); } };
  const selectProvider = async (provider: string) => {
    void op("select.model", { provider }, selectedID);
    if (boot?.capabilities.catalog) request<{ models: Array<{ ID?: string; id?: string }> }>(`/v1/models?provider=${encodeURIComponent(provider)}`).then((v) => setModels(v.models.map((m) => m.ID || m.id || "").filter(Boolean))).catch(() => setModels([]));
  };
  const inspectProject = async (tab: InspectorTab) => {
    setInspector(tab); setInspectorOpen(true); setProjectLoading(true); setProjectData(undefined);
    try {
      if (tab === "files") setProjectData(boot?.capabilities.files ? await request(`/v1/changed-files${selectedID ? `?root=${encodeURIComponent(selectedID)}` : ""}`).catch((error) => ({ error: error.message })) : undefined);
      if (tab === "memory") setProjectData(boot?.capabilities.memory ? await request("/v1/memory").catch((error) => ({ error: error.message })) : undefined);
      if (tab === "issues") setProjectData(boot?.capabilities.issues ? await request("/v1/issues").catch((error) => ({ error: error.message })) : undefined);
      if (tab === "plans" || tab === "workflows") setProjectData(undefined);
    } finally { setProjectLoading(false); }
  };
  const sessionAction = async (action: "fork" | "rename" | "delete") => {
    if (!boot?.capabilities.sessions) return;
    if (action === "fork") await request(`/v1/sessions/${encodeURIComponent(selectedID)}/fork`, { method: "POST" });
    if (action === "rename") { const title = window.prompt("Session title"); if (title === null) return; await request(`/v1/sessions/${encodeURIComponent(selectedID)}`, { method: "PATCH", body: JSON.stringify({ title }) }); }
    if (action === "delete" && window.confirm("Delete this durable session?")) await request(`/v1/sessions/${encodeURIComponent(selectedID)}`, { method: "DELETE" });
    await refreshSessions();
  };
  const handleCreateWorkspace = async () => { if (!boot?.capabilities.roots) return; try { const result = await createRoot(); await refreshRoots(); setSelectedID(result.id); setSelectedIsLive(true); setNavTab("active"); } catch (error) { window.alert((error as Error).message); } };
  const handleResume = async (id: string) => { if (!boot?.capabilities.roots) return; try { const result = await resumeRoot(id); await refreshRoots(); setSelectedID(result.id); setSelectedIsLive(true); setNavTab("active"); } catch (error) { window.alert((error as Error).message); } };
  const selectWorkspace = (id: string, isLive: boolean) => { setSelectedID(id); setSelectedIsLive(isLive); };
  const toggleDiff = (path: string) => setExpandedDiffs((old) => { const next = new Set(old); next.has(path) ? next.delete(path) : next.add(path); return next; });

  return <div className="app-shell" style={shellStyle}>
    <header><button className="icon-button" aria-label="Toggle agents panel" aria-pressed={navOpen} onClick={() => setNavOpen((open) => !open)}>☰</button><div className="wordmark"><span className="mark">S</span><strong>STRIKE</strong><small>workspace</small></div><div className="session-line"><span className={state.status.busy ? "pulse busy" : "pulse"} />{state.status.busy ? "agent working" : transport}</div><button className="icon-button" aria-label="Export markdown" title="Export markdown" onClick={() => exportSession()}>↓</button><button className="icon-button" aria-label="Open settings" onClick={() => setSettingsOpen(true)}>⚙</button><button className="icon-button" aria-label="Toggle inspector" aria-pressed={inspectorOpen} onClick={() => setInspectorOpen((open) => !open)}>◫</button></header>
    <aside className={`navigation ${navOpen ? "open" : "collapsed"}`} aria-label="Agents panel"><PanelResize label="Resize agents panel" value={navWidth} min={180} max={420} onChange={setNavWidth} />{boot?.capabilities.roots ? <><div className="aside-heading"><button className={`nav-tab ${navTab === "active" ? "active" : ""}`} onClick={() => setNavTab("active")}>ACTIVE</button><button className={`nav-tab ${navTab === "history" ? "active" : ""}`} onClick={() => setNavTab("history")}>HISTORY</button></div>{navTab === "active" && <><nav>{activeRoots.map((root) => <button key={root.id} className={root.id === selectedID && selectedIsLive ? "session active" : "session"} onClick={() => selectWorkspace(root.id, true)}><span className={root.busy ? "root-busy" : "root-idle"} />{root.agent || root.id.slice(0, 12)}{root.id === activeRootID && <small>ACTIVE</small>}{root.busy && <small>BUSY</small>}</button>)}</nav>{!boot?.attachOnly && <div className="session-actions"><button onClick={() => void handleCreateWorkspace()}>+ New workspace</button></div>}</>}{navTab === "history" && <HistoryNav sessions={sessions} activeRoots={activeRoots} selectedID={selectedID} selectedIsLive={selectedIsLive} historySearch={historySearch} setHistorySearch={setHistorySearch} selectWorkspace={selectWorkspace} handleResume={handleResume} boot={boot} sessionAction={sessionAction} />}</> : <><div className="aside-heading"><span>SESSIONS</span></div><nav>{sessions.map((session) => <button key={session.id} className={session.id === selectedID ? "session active" : "session"} onClick={() => { setSelectedID(session.id); setSelectedIsLive(session.id === liveID && !boot?.attachOnly); }}><span>{session.title || session.id.slice(0, 12)}</span>{session.id === liveID && <small>LIVE</small>}</button>)}</nav><div className="session-actions" aria-label="Session actions"><button disabled={!boot?.capabilities.sessions} onClick={() => void sessionAction("fork")}>Fork</button><button disabled={!boot?.capabilities.sessions} onClick={() => void sessionAction("rename")}>Rename</button><button disabled={!boot?.capabilities.sessions} onClick={() => void sessionAction("delete")}>Delete</button></div></>}<div className="aside-heading">CHILD AGENTS</div><div className="children">{children.length ? children.map(([id, child]) => <div key={id}><span className={`child-state ${child.status}`} />{child.agent || id.slice(0, 8)}<small>{child.status}</small></div>) : <p>None dispatched</p>}</div><div className="workspace-meta"><span>ROOT</span><code>{state.status.cwd || "unavailable"}</code><span>BUILD</span><code>{boot?.version || "…"}</code></div></aside>
    <main>
      <section className="runtime" aria-label="Runtime controls">
        <Field label="Provider" value={state.status.provider} values={providers.length ? providers : state.status.provider ? [state.status.provider] : []} disabled={runtimeBusy || !boot?.capabilities.auth} onChange={(name) => void selectProvider(name)} />
        <Field label="Model" value={state.status.model} values={models.length ? models : state.status.model ? [state.status.model] : []} disabled={runtimeBusy || !boot?.capabilities.catalog} onChange={(model) => void op("select.model", { provider: state.status.provider, model }, selectedID)} />
        <Field label="Agent" value={state.status.agent} values={boot?.agents.map((agent) => agent.name) || []} disabled={runtimeBusy} onChange={(name) => void op("select.agent", { name }, selectedID)} />
        <div className="runtime-more">
          <button type="button" className="runtime-disclosure" aria-expanded={runtimeOpen} aria-controls="runtime-secondary" onClick={() => setRuntimeOpen((open) => !open)}>
            <span>Runtime…</span>
            {!runtimeOpen && runtimeSummary && <span className="runtime-summary">{runtimeSummary}</span>}
          </button>
          {runtimeOpen && (
            <div id="runtime-secondary" className="runtime-secondary" role="group" aria-label="Secondary runtime controls">
              <Field label="Effort" value={state.status.effort} values={runtimeValues.effort} disabled={runtimeBusy} onChange={(level) => void op("set.effort", { level }, selectedID)} />
              <Field label="Autonomy" value={state.status.autonomy} values={runtimeValues.autonomy} disabled={runtimeBusy} onChange={(mode) => void op("set.autonomy", { mode }, selectedID)} />
              <Field label="Permission" value={state.status.permissionMode} values={runtimeValues.permission} disabled={runtimeBusy} onChange={(mode) => void op("set.permission_mode", { mode }, selectedID)} />
              <label className="fast-toggle"><input type="checkbox" checked={fast} disabled={runtimeBusy} onChange={(event) => { setFast(event.target.checked); void op("set.fast", { enabled: event.target.checked }, selectedID); }} />FAST</label>
            </div>
          )}
        </div>
      </section>
      <section className="transcript" aria-live="polite" aria-label="Conversation transcript">{!boot && transport !== "connecting" ? <div className="empty-state" role="alert"><span>ERROR</span><h1>{transport}</h1><p>Failed to load cockpit. Open the URL printed by <code>strike serve</code> (includes <code>?token=</code>), or pass a valid bearer token.</p></div> : !state.items.length && <div className="empty-state"><span>01 / READY</span><h1>{boot?.attachOnly ? "Inspect the record." : "Direct the work."}</h1><p>{boot?.attachOnly ? "Select a durable session from the rail." : "Describe an outcome. Strike will plan, act, and report through the live engine seam."}</p></div>}{state.items.map((item) => <Transcript key={item.id} item={item} />)}<div ref={endRef} /></section>
      <form className="composer" onSubmit={submit}><label htmlFor="prompt">Instruction {state.status.busy && "— send to queue"}</label><textarea aria-label="Instruction" id="prompt" value={draft} disabled={!isLive} placeholder={isLive ? "Describe the next outcome…  / command" : "Historical session — read only"} onPaste={(event) => void attach(event.clipboardData.files)} onDrop={(event) => { event.preventDefault(); void attach(event.dataTransfer.files); }} onDragOver={(event) => event.preventDefault()} onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !completions.length) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} />{completions.length > 0 && <div className="completion" role="listbox" aria-label="Composer completions">{completions.slice(0, 8).map((item) => <button type="button" role="option" key={item.label} onClick={() => selectCompletion(item)}><strong>{item.label}</strong><span>{item.detail}</span></button>)}</div>}{images.length > 0 && <div className="attachments">{images.map((image, index) => <button type="button" key={`${image.name}-${index}`} onClick={() => setImages((list) => list.filter((_, i) => i !== index))}>{image.name} ×</button>)}</div>}{queue.length > 0 && <div className="prompt-queue-wrap"><ol ref={queueRef} className="prompt-queue" aria-label="Queued prompts">{queue.map((item, index) => <li key={index}>{queueEdit?.index === index ? <input className="queue-edit" aria-label={`Queued prompt text ${index + 1}`} value={queueEdit.text} autoFocus onChange={(event) => setQueueEdit({ index, text: event.target.value })} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); queueEditCancel.current = false; setQueue((list) => editQueuedText(list, index, queueEdit.text)); setQueueEdit(null); } if (event.key === "Escape") { event.preventDefault(); queueEditCancel.current = true; setQueueEdit(null); } }} onBlur={() => { if (!queueEditCancel.current) setQueue((list) => editQueuedText(list, index, queueEdit.text)); queueEditCancel.current = false; setQueueEdit(null); }} /> : <span>{item.text}{item.images.length > 0 ? ` (${item.images.length} img)` : ""}</span>}<span className="queue-actions"><button type="button" aria-label={`Move queued prompt ${index + 1} up`} disabled={index === 0} onClick={() => setQueue((list) => moveQueuedAt(list, index, -1))}>↑</button><button type="button" aria-label={`Move queued prompt ${index + 1} down`} disabled={index === queue.length - 1} onClick={() => setQueue((list) => moveQueuedAt(list, index, 1))}>↓</button><button type="button" aria-label={`Edit queued prompt ${index + 1}`} onClick={() => { queueEditCancel.current = false; setQueueEdit({ index, text: item.text }); }}>✎</button><button type="button" aria-label={`Remove queued prompt ${index + 1}`} onClick={() => { setQueue((list) => removeQueuedAt(list, index)); setQueueEdit((cur) => cur?.index === index ? null : cur); }}>×</button></span></li>)}</ol><div className="queue-toolbar"><button type="button" onClick={() => { setQueue(clearQueue()); setQueueEdit(null); }}>Clear queue</button></div></div>}<div><span><kbd>↵</kbd> send · <kbd>⇧↵</kbd> newline</span><span><input ref={fileRef} hidden type="file" accept="image/*" multiple onChange={(event) => void attach(event.target.files)} /><button type="button" onClick={() => fileRef.current?.click()}>Attach</button>{history.length > 0 && <button type="button" onClick={() => setDraft(history.at(-1) || "")}>History</button>}<button type="button" onClick={() => exportSession()} disabled={!state.items.length}>Export</button>{state.status.busy && <button type="button" className="stop" onClick={() => void op("interrupt")}>Interrupt</button>}<button type="submit" disabled={!draft.trim() || !isLive}>{state.status.busy ? "Queue" : "Send"}</button></span></div></form>
    </main>
    <aside className={`inspector ${inspectorOpen ? "open" : "collapsed"}`} aria-label="Inspector"><PanelResize label="Resize inspector panel" value={inspectorWidth} min={240} max={520} onChange={setInspectorWidth} /><div className="inspector-tabs" role="tablist">{(["context", "files", "memory", "issues", "plans", "workflows"] as InspectorTab[]).map((tab) => <button role="tab" aria-selected={inspector === tab} key={tab} onClick={() => void inspectProject(tab)}>{tab}</button>)}</div><div className="inspector-body"><InspectorBody tab={inspector} boot={boot} status={state.status} data={projectData} loading={projectLoading} expandedDiffs={expandedDiffs} toggleDiff={toggleDiff} isLive={isLive} selectedID={selectedID} /></div></aside>
    {settingsOpen && <SettingsDialog boot={boot} status={state.status} providers={providers} onClose={() => setSettingsOpen(false)} />}
    {state.permission && <BlockingDialog title="Permission required"><p><strong>{String(state.permission.tool || state.permission.name || "Tool request")}</strong></p><pre>{JSON.stringify(state.permission, null, 2)}</pre><div className="dialog-actions"><button onClick={() => void op("permission.reply", { requestId: state.permission?.requestId, decision: "reject" }, selectedID)}>Reject</button><button onClick={() => void op("permission.reply", { requestId: state.permission?.requestId, decision: "always" }, selectedID)}>Allow session</button><button autoFocus onClick={() => void op("permission.reply", { requestId: state.permission?.requestId, decision: "once" }, selectedID)}>Allow once</button></div></BlockingDialog>}{state.question && <QuestionDialog question={state.question} rootID={selectedID} />}
  </div>;
}

function PanelResize({ label, value, min, max, onChange }: { label: string; value: number; min: number; max: number; onChange: (value: number) => void }) {
  return <label className="panel-resize"><span>{label}</span><input aria-label={label} type="range" min={min} max={max} value={value} onChange={(event) => onChange(Number(event.target.value))} /></label>;
}

function HistoryNav({ sessions, activeRoots, selectedID, selectedIsLive, historySearch, setHistorySearch, selectWorkspace, handleResume, boot, sessionAction }: { sessions: Session[]; activeRoots: ActiveRoot[]; selectedID: string; selectedIsLive: boolean; historySearch: string; setHistorySearch: (value: string) => void; selectWorkspace: (id: string, isLive: boolean) => void; handleResume: (id: string) => Promise<void>; boot?: Bootstrap; sessionAction: (action: "fork" | "rename" | "delete") => Promise<void> }) {
  return <><input className="history-search" type="search" placeholder="Search sessions…" aria-label="Search sessions" value={historySearch} onChange={(event) => setHistorySearch(event.target.value)} /><nav>{sessions.filter((s) => !historySearch || s.title?.toLowerCase().includes(historySearch.toLowerCase()) || s.id.toLowerCase().includes(historySearch.toLowerCase())).map((session) => { const isActiveWorkspace = activeRoots.some((r) => r.id === session.id); return <button key={session.id} className={session.id === selectedID ? "session active" : "session"} onClick={() => selectWorkspace(session.id, isActiveWorkspace)}><span>{session.title || session.id.slice(0, 12)}</span>{isActiveWorkspace && <small className="live-badge">LIVE</small>}</button>; })}</nav><div className="session-actions">{!selectedIsLive && !boot?.attachOnly && <button onClick={() => void handleResume(selectedID)}>Resume as workspace</button>}{boot?.capabilities.sessions && <><button disabled={!boot?.capabilities.sessions} onClick={() => void sessionAction("fork")}>Fork</button><button disabled={!boot?.capabilities.sessions} onClick={() => void sessionAction("rename")}>Rename</button><button disabled={!boot?.capabilities.sessions} onClick={() => void sessionAction("delete")}>Delete</button></>}</div></>;
}

function InspectorBody({ tab, boot, status, data, loading, expandedDiffs, toggleDiff, isLive, selectedID }: { tab: InspectorTab; boot?: Bootstrap; status: Status; data: unknown; loading: boolean; expandedDiffs: Set<string>; toggleDiff: (path: string) => void; isLive: boolean; selectedID: string }) {
  if (tab === "context") return <><h2>Runtime context</h2><dl><dt>Provider</dt><dd>{status.provider || "unknown"}</dd><dt>Model</dt><dd>{status.model || "unknown"}</dd><dt>Phase</dt><dd>{status.phase || "idle"}</dd><dt>Workflow</dt><dd>{status.workflow || "none"}</dd><dt>Context</dt><dd>{status.contextUsed !== undefined && status.contextLimit !== undefined ? `${status.contextUsed.toLocaleString()} / ${status.contextLimit.toLocaleString()}` : "not reported"}</dd><dt>Cost</dt><dd>not reported</dd></dl><p className="muted">Context details can expand here as the web cockpit catches up with the TUI.</p></>;
  if (tab === "workflows") {
    return <WorkflowsPanel
      available={Boolean(boot?.capabilities.workflows)}
      draftsAvailable={Boolean(boot?.capabilities.workflowDrafts)}
      live={isLive}
      rootID={selectedID}
      activeWorkflow={status.workflow}
      agents={boot?.agents.map((a) => a.name) || []}
      busy={Boolean(status.busy)}
    />;
  }
  if (tab === "plans") {
    return <PlansPanel available={Boolean(boot?.capabilities.plans)} live={isLive} rootID={selectedID} />;
  }
  if (loading) return <section className="unavailable" role="status"><strong>Loading {tab}</strong></section>;
  if (tab === "files") return <FilesPanel boot={boot} data={data} expandedDiffs={expandedDiffs} toggleDiff={toggleDiff} />;
  if (tab === "memory") return <MemoryPanel boot={boot} data={data} />;
  return <IssuesPanel boot={boot} data={data} />;
}

function FilesPanel({ boot, data, expandedDiffs, toggleDiff }: { boot?: Bootstrap; data: unknown; expandedDiffs: Set<string>; toggleDiff: (path: string) => void }) {
  if (!boot?.capabilities.files) return <CapabilityUnavailable name="Changed files" />;
  if ((data as { error?: string } | undefined)?.error) return <CapabilityError error={(data as { error: string }).error} />;
  const files = ((data as { files?: ChangedFile[] } | undefined)?.files || []);
  return <><h2>Changed files</h2>{files.length ? <div className="changed-files">{files.map((file) => <article key={file.path} className="changed-file"><button onClick={() => toggleDiff(file.path)} aria-expanded={expandedDiffs.has(file.path)}><code>{file.path}</code><span className="diff-stat"><b>+{file.added}</b><b>-{file.deleted}</b></span></button>{expandedDiffs.has(file.path) && <pre className="diff-view">{file.diff || "No textual diff available."}</pre>}</article>)}</div> : <p className="muted">No changed files reported.</p>}</>;
}

function MemoryPanel({ boot, data }: { boot?: Bootstrap; data: unknown }) {
  if (!boot?.capabilities.memory) return <CapabilityUnavailable name="Memory" />;
  if ((data as { error?: string } | undefined)?.error) return <CapabilityError error={(data as { error: string }).error} />;
  const entries = ((data as { entries?: MemoryEntry[] } | undefined)?.entries || []);
  return <><h2>Memory</h2>{entries.length ? <div className="project-list">{entries.map((entry) => { const key = entry.Key || entry.key || ""; const value = entry.Value || entry.value || ""; const tags = entry.Tags || entry.tags || []; return <article key={key}><h3>{key}</h3><p>{value}</p>{tags.length > 0 && <small>{tags.join(", ")}</small>}</article>; })}</div> : <p className="muted">No project memory entries.</p>}</>;
}

function IssuesPanel({ boot, data }: { boot?: Bootstrap; data: unknown }) {
  if (!boot?.capabilities.issues) return <CapabilityUnavailable name="Issues" />;
  if ((data as { error?: string } | undefined)?.error) return <CapabilityError error={(data as { error: string }).error} />;
  const issues = ((data as { issues?: IssueEntry[] } | undefined)?.issues || []);
  return <><h2>Issues</h2>{issues.length ? <div className="project-list">{issues.map((issue) => { const id = issue.ID ?? issue.id ?? 0; const title = issue.Title || issue.title || "Untitled issue"; const body = issue.Body || issue.body || ""; const status = issue.Status || issue.status || "open"; return <article key={id}><h3>#{id} {title}</h3><small>{status}</small>{body && <p>{body}</p>}</article>; })}</div> : <p className="muted">No project issues.</p>}</>;
}

function SettingsDialog({ boot, status, providers, onClose }: { boot?: Bootstrap; status: Status; providers: string[]; onClose: () => void }) { const ref = useRef<HTMLDialogElement>(null); const [provider, setProvider] = useState(String(status.provider || providers[0] || "")); const [key, setKey] = useState(""); useEffect(() => { ref.current?.showModal(); }, []); const save = async () => { if (boot?.capabilities.settings) await request("/v1/settings", { method: "PATCH", body: JSON.stringify({ provider: String(status.provider || ""), model: String(status.model || ""), agent: String(status.agent || ""), effort: String(status.effort || ""), mode: String(status.permissionMode || "") }) }); onClose(); }; return <dialog ref={ref} aria-labelledby="settings-title" onClose={onClose}><div className="dialog-rule" /><h2 id="settings-title">Workspace settings</h2>{boot?.capabilities.auth ? <fieldset><legend>Provider authentication</legend><label>Provider<select value={provider} onChange={(event) => setProvider(event.target.value)}>{providers.map((name) => <option key={name}>{name}</option>)}</select></label><label>API key<input value={key} onChange={(event) => setKey(event.target.value)} placeholder="Stored locally by strike" /></label><button disabled={!provider || !key} onClick={() => void request("/v1/auth/key", { method: "POST", body: JSON.stringify({ provider, key }) }).then(() => setKey(""))}>Save key</button></fieldset> : <CapabilityUnavailable name="Provider authentication" />}{boot?.capabilities.settings ? <p className="muted">Current defaults can be saved from the live runtime controls.</p> : <CapabilityUnavailable name="Saved defaults" />}<div className="dialog-actions"><button onClick={onClose}>Close</button><button onClick={() => void save()}>Save defaults</button></div></dialog>; }
function CapabilityUnavailable({ name }: { name: string }) { return <section className="unavailable" role="status"><strong>{name} unavailable</strong><p>The configured host did not provide this capability. No action was attempted.</p></section>; }
function CapabilityError({ error }: { error: string }) { return <section className="unavailable" role="status"><strong>Unable to load</strong><p>{error}</p></section>; }
function QuestionDialog({ question, rootID }: { question: Record<string, unknown>; rootID: string }) { const [answers, setAnswers] = useState<string[]>([]); const prompts = Array.isArray(question.questions) ? question.questions as Array<Record<string, unknown>> : [{ question: question.question }]; const update = (index: number, value: string) => setAnswers((old) => { const next = [...old]; next[index] = value; return next; }); return <BlockingDialog title={String(question.title || "Agent question")}>{prompts.map((prompt, index) => { const options = Array.isArray(prompt.options) ? prompt.options as Array<Record<string, unknown>> : []; return <fieldset key={index}><legend>{String(prompt.question || "A response is required to continue.")}</legend>{options.length ? options.map((option) => <label key={String(option.label)}><input type="radio" name={`question-${index}`} value={String(option.label)} checked={answers[index] === String(option.label)} onChange={(event) => update(index, event.target.value)} />{String(option.label)}<span>{String(option.description || "")}</span></label>) : <textarea aria-label={`Answer ${index + 1}`} value={answers[index] || ""} onChange={(event) => update(index, event.target.value)} />}</fieldset>; })}<div className="dialog-actions"><button autoFocus onClick={() => void op("question.reply", { requestId: question.requestId, answers }, rootID)}>Continue</button></div></BlockingDialog>; }
