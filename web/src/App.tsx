import { FormEvent, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { activateRoot, bootstrap, closeRoot, createRoot, historicalConnection, liveConnection, request, resumeRoot, roots as loadRoots, sendOp, sessions as loadSessions, sessionChildren, getSandbox, patchSandbox } from "./api";
import { ChildAgentsPanel } from "./ChildAgents";
import { buildExportMarkdown, defaultExportFilename, downloadTextFile } from "./exportMarkdown";
import { clearQueue, editQueuedText, moveQueuedAt, removeQueuedAt, type QueuedPrompt } from "./queueOps";
import { initialState, reduceEvent } from "./reducer";
import { formatCostNotice, formatSlashHelp, resolveSlash, WEB_SLASH_COMMANDS } from "./slash";
import { Transcript } from "./Transcript";
import type { SandboxInfo,  ActiveRoot, Bootstrap, Capabilities, ImageAttachment, Session, Status } from "./types";
import { MCPPanel } from "./MCP";
import { PlansPanel } from "./Plans";
import {
  defaultRestoreFiles,
  emptyUndoPreview,
  filesChoiceDetail,
  formatUndoPreviewLines,
  type UndoPreview,
} from "./undoPreview";
import { WorkflowsPanel } from "./Workflows";
import "./styles.css";

type InspectorTab = "files" | "memory" | "issues" | "plans" | "workflows" | "mcp";
type Completion = { label: string; detail: string; insert: string };
type ChangedFile = { path: string; added: number; deleted: number; diff: string };
type MemoryEntry = { Key?: string; key?: string; Value?: string; value?: string; Tags?: string[]; tags?: string[] };
type IssueEntry = { ID?: number; id?: number; Title?: string; title?: string; Body?: string; body?: string; Status?: string; status?: string };
const inspectorTabOrder: InspectorTab[] = ["files", "memory", "issues", "plans", "workflows", "mcp"];
const availableInspectorTabs = (caps?: Capabilities): InspectorTab[] =>
  inspectorTabOrder.filter((tab) => Boolean(caps?.[tab]));
type UndoDialogState = { preferFiles: boolean };
const runtimeValues = { effort: ["low", "medium", "high", "xhigh"], autonomy: ["supervised", "agent", "checks"], permission: ["default", "plan", "soft-approve", "accept-edits", "yolo"], sandbox: ["off", "read-only", "workspace-write"] };
const slashCommands: Completion[] = WEB_SLASH_COMMANDS;

const op = (type: string, data?: unknown, rootID?: string) => sendOp(type, data, rootID).catch((error) => window.alert(error.message));

const shortID = (id: string) => id.slice(0, 12);
const rootTitle = (root: ActiveRoot, sessions: Session[]) => root.title || sessions.find((s) => s.id === root.id)?.title || shortID(root.id);
const relativeActivity = (ms?: number) => {
  if (!ms) return "";
  const sec = Math.max(0, Math.floor((Date.now() - ms) / 1000));
  if (sec < 60) return "just now";
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
};

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
  const [undoDialog, setUndoDialog] = useState<UndoDialogState | null>(null);
  const [images, setImages] = useState<ImageAttachment[]>([]);
  const [inspector, setInspector] = useState<InspectorTab>("files");
  const [inspectorOpen, setInspectorOpen] = useState(false);
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
  const [sandboxInfo, setSandboxInfo] = useState<SandboxInfo>();
  const [sandboxExplainOpen, setSandboxExplainOpen] = useState(false);
  const [selectedChildId, setSelectedChildId] = useState<string>();
  const endRef = useRef<HTMLDivElement>(null);
  const fileRef = useRef<HTMLInputElement>(null);

  const refreshSessions = () => loadSessions().then((list) => { setSessions(list.sessions || []); setLiveID(list.liveId || ""); return list; });
  const refreshRoots = () => loadRoots().then((r) => { setActiveRoots(r.roots || []); setActiveRootID(r.activeId || ""); return r; }).catch(() => undefined);
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
      if (nextBoot.capabilities.sandbox) getSandbox().then(setSandboxInfo).catch(() => {});
    }).catch((error) => setTransport(error.message));
  }, []);

  useEffect(() => {
    if (!selectedID) return;
    dispatch({ type: "workspace.reset", data: { sessionId: selectedID } });
    setSelectedChildId(undefined);
    if (!selectedIsLive) return historicalConnection(selectedID, dispatch, (message) => setTransport(message));
    const live = liveConnection(selectedID, dispatch, setTransport);
    return () => live.close();
  }, [selectedID, selectedIsLive]);
  useEffect(() => {
    if (!selectedID || !boot?.capabilities.sessions) return;
    let cancelled = false;
    void sessionChildren(selectedID)
      .then((res) => {
        if (!cancelled) dispatch({ type: "children.seed", time: `seed:${selectedID}`, data: { sessions: res.sessions || [] } });
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [selectedID, boot?.capabilities.sessions]);
  useEffect(() => { endRef.current?.scrollIntoView({ block: "end" }); }, [state.items]);
  useEffect(() => {
    if (state.status.busy || !queue.length || !selectedIsLive) return;
    const [next, ...rest] = queue; setQueue(rest); void op("user.input", { text: next.text, images: next.images.map(({ mime, data }) => ({ mime, data })) }, selectedID);
  }, [state.status.busy, queue, selectedIsLive, selectedID]);

  const isLive = Boolean(selectedIsLive && selectedID && !boot?.attachOnly);
  const runtimeBusy = !isLive || state.status.busy;
  const runtimeSummary = [state.status.effort, state.status.autonomy, state.status.permissionMode, fast ? "fast" : ""].filter(Boolean).join(" · ");
  const children = useMemo(() => Object.entries(state.children), [state.children]);
  const inspectorTabs = useMemo(() => availableInspectorTabs(boot?.capabilities), [boot]);
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
        // Preview before destructive rewind (TUI /undo parity — WEB.12).
        if (resolved.type === "rewind") {
          const restore = Boolean(data && typeof data === "object" && (data as Record<string, unknown>).restoreFiles);
          setUndoDialog({ preferFiles: restore });
          return;
        }
        void op(resolved.type, data, selectedID);
        return;
      }
    }
  };
  const lastUndoPreview = state.undoStack.at(-1) || emptyUndoPreview();
  const confirmUndo = (restoreFiles: boolean) => {
    setUndoDialog(null);
    void op("rewind", restoreFiles ? { restoreFiles: true } : {}, selectedID);
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
  const inspectProject = async (tab: InspectorTab, opts?: { open?: boolean }) => {
    setInspector(tab);
    if (opts?.open !== false) setInspectorOpen(true);
    setProjectLoading(true); setProjectData(undefined);
    try {
      if (tab === "files") setProjectData(boot?.capabilities.files ? await request(`/v1/changed-files${selectedID ? `?root=${encodeURIComponent(selectedID)}` : ""}`).catch((error) => ({ error: error.message })) : undefined);
      if (tab === "memory") setProjectData(boot?.capabilities.memory ? await request("/v1/memory").catch((error) => ({ error: error.message })) : undefined);
      if (tab === "issues") setProjectData(boot?.capabilities.issues ? await request("/v1/issues").catch((error) => ({ error: error.message })) : undefined);
      if (tab === "plans" || tab === "workflows" || tab === "mcp") setProjectData(undefined);
    } finally { setProjectLoading(false); }
  };
  // Prefer files, else first capability-backed tab; hydrate data without forcing inspector open (#912 density).
  useEffect(() => {
    if (!boot) return;
    const tabs = availableInspectorTabs(boot.capabilities);
    if (!tabs.length) return;
    const tab = tabs.includes(inspector) ? inspector : tabs[0];
    void inspectProject(tab, { open: false });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- one-shot when boot lands
  }, [boot]);
  const sessionAction = async (action: "fork" | "rename" | "delete") => {
    if (!boot?.capabilities.sessions || !selectedID) return;
    if (action === "fork") await request(`/v1/sessions/${encodeURIComponent(selectedID)}/fork`, { method: "POST" });
    if (action === "rename") { const title = window.prompt("Session title"); if (title === null) return; await request(`/v1/sessions/${encodeURIComponent(selectedID)}`, { method: "PATCH", body: JSON.stringify({ title }) }); }
    if (action === "delete" && window.confirm("Delete this durable session?")) await request(`/v1/sessions/${encodeURIComponent(selectedID)}`, { method: "DELETE" });
    await Promise.all([refreshSessions(), refreshRoots()]);
  };
  const handleCreateWorkspace = async () => { if (!boot?.capabilities.roots || boot.attachOnly) return; try { const result = await createRoot(); await refreshRoots(); setSelectedID(result.id); setSelectedIsLive(true); setNavTab("active"); } catch (error) { window.alert((error as Error).message); } };
  const handleResume = async (id: string) => { if (!boot?.capabilities.roots || boot.attachOnly) return; try { const result = await resumeRoot(id); await refreshRoots(); setSelectedID(result.id); setSelectedIsLive(true); setNavTab("active"); } catch (error) { window.alert((error as Error).message); } };
  const selectWorkspace = async (id: string, isLive: boolean) => {
    setSelectedID(id);
    setSelectedIsLive(isLive);
    if (!isLive || !boot?.capabilities.roots || boot.attachOnly) return;
    try {
      await activateRoot(id);
      await refreshRoots();
    } catch (error) {
      window.alert((error as Error).message);
    }
  };
  const handleCloseWorkspace = async () => {
    if (!boot?.capabilities.roots || boot.attachOnly || !selectedIsLive || !selectedID) return;
    const root = activeRoots.find((r) => r.id === selectedID);
    const prompt = root?.busy ? "This workspace is busy. Close/stop it anyway?" : "Close this workspace?";
    if (!window.confirm(prompt)) return;
    try {
      await closeRoot(selectedID);
      const [rootsResult, sessionsResult] = await Promise.all([refreshRoots(), refreshSessions()]);
      const nextLive = rootsResult?.activeId || rootsResult?.roots?.[0]?.id || "";
      if (nextLive) {
        setSelectedID(nextLive);
        setSelectedIsLive(true);
        setNavTab("active");
      } else {
        const first = sessionsResult?.sessions?.[0]?.id || "";
        setSelectedID(first);
        setSelectedIsLive(false);
        setNavTab("history");
      }
    } catch (error) {
      window.alert((error as Error).message);
    }
  };
  const openChildTranscript = (id: string) => { setSelectedChildId(undefined); void selectWorkspace(id, false); setNavTab("history"); };
  const changeSandboxDefault = async (mode: string) => {
    if (!boot?.capabilities.sandbox || !sandboxInfo?.canChangeDefault) return;
    const perm = String(state.status.permissionMode || sandboxInfo.permissionMode || "");
    let iKnow = false;
    if (mode === "off" && perm === "yolo") {
      iKnow = window.confirm("permissionMode is yolo and sandbox off disables OS isolation. Continue only if you understand the risk (equivalent to --i-know).");
      if (!iKnow) return;
    } else if (mode !== (sandboxInfo.defaultMode || state.status.sandbox)) {
      if (!window.confirm(`Save sandbox default "${mode}" for new sessions? Active session stays "${state.status.sandbox || sandboxInfo.mode || "workspace-write"}".`)) return;
    }
    try {
      const next = await patchSandbox(mode, iKnow, selectedID || undefined);
      setSandboxInfo(next);
      window.alert(`Sandbox default saved as ${next.defaultMode || mode}. Active session mode is unchanged until a new session starts.`);
    } catch (error) {
      window.alert((error as Error).message);
    }
  };
  const openSandboxExplain = async () => {
    if (!boot?.capabilities.sandbox) return;
    try {
      const next = await getSandbox(selectedID || undefined);
      setSandboxInfo(next);
      setSandboxExplainOpen(true);
    } catch (error) {
      window.alert((error as Error).message);
    }
  };
  const toggleDiff = (path: string) => setExpandedDiffs((old) => { const next = new Set(old); next.has(path) ? next.delete(path) : next.add(path); return next; });

  return <div className="app-shell" style={shellStyle}>
    <header><button className="icon-button" aria-label="Toggle agents panel" aria-pressed={navOpen} onClick={() => setNavOpen((open) => !open)}>☰</button><div className="wordmark"><span className="mark">S</span><strong>STRIKE</strong><small>workspace</small></div><div className="session-line"><span className={state.status.busy ? "pulse busy" : "pulse"} />{state.status.busy ? "agent working" : transport}</div><button className="icon-button" aria-label="Export markdown" title="Export markdown" onClick={() => exportSession()}>↓</button><button className="icon-button" aria-label="Open settings" onClick={() => setSettingsOpen(true)}>⚙</button><button className="icon-button" aria-label="Toggle inspector" aria-pressed={inspectorOpen} onClick={() => setInspectorOpen((open) => !open)}>◫</button></header>
    <aside className={`navigation ${navOpen ? "open" : "collapsed"}`} aria-label="Agents panel"><PanelResize label="Resize agents panel" value={navWidth} min={180} max={420} onChange={setNavWidth} side="nav" />{boot?.capabilities.roots ? <><div className="aside-heading"><button className={`nav-tab ${navTab === "active" ? "active" : ""}`} onClick={() => setNavTab("active")}>ACTIVE</button><button className={`nav-tab ${navTab === "history" ? "active" : ""}`} onClick={() => setNavTab("history")}>HISTORY</button></div>{navTab === "active" && <><nav>{activeRoots.map((root) => {
                  const label = rootTitle(root, sessions);
                  const activity = relativeActivity(root.activeAt);
                  return <button key={root.id} type="button" className={root.id === selectedID && selectedIsLive ? "session active" : "session"} onClick={() => void selectWorkspace(root.id, true)} title={root.id}>
                    <span className={root.busy ? "root-busy" : "root-idle"} aria-hidden />
                    <span className="session-main"><span className="session-title">{label}</span><span className="session-meta">{root.agent || "—"}{activity ? ` · ${activity}` : ""}</span></span>
                    <span className="session-flags">{root.id === activeRootID && <small>ACTIVE</small>}<small>{root.busy ? "BUSY" : "IDLE"}</small></span>
                  </button>;
                })}</nav>{!boot?.attachOnly && <div className="session-actions"><button type="button" onClick={() => void handleCreateWorkspace()}>+ New workspace</button><button type="button" disabled={!selectedIsLive || !selectedID} onClick={() => void handleCloseWorkspace()}>Close workspace</button></div>}</>}{navTab === "history" && <HistoryNav sessions={sessions} activeRoots={activeRoots} selectedID={selectedID} selectedIsLive={selectedIsLive} historySearch={historySearch} setHistorySearch={setHistorySearch} selectWorkspace={selectWorkspace} handleResume={handleResume} boot={boot} sessionAction={sessionAction} />}</> : <><div className="aside-heading"><span>SESSIONS</span></div><nav>{sessions.map((session) => <button key={session.id} className={session.id === selectedID ? "session active" : "session"} onClick={() => { setSelectedID(session.id); setSelectedIsLive(session.id === liveID && !boot?.attachOnly); }}><span>{session.title || session.id.slice(0, 12)}</span>{session.id === liveID && <small>LIVE</small>}</button>)}</nav>{boot?.capabilities.sessions && selectedID && <div className="session-actions" aria-label="Session actions"><SessionMenu onAction={(action) => void sessionAction(action)} /></div>}</>}<ChildAgentsPanel children={children} selectedId={selectedChildId} onSelect={setSelectedChildId} onOpenTranscript={openChildTranscript} /><details className="workspace-meta"><summary>Workspace</summary><span>ROOT</span><code>{state.status.cwd || "unavailable"}</code><span>BUILD</span><code>{boot?.version || "…"}</code></details></aside>
    <main>
      <div className="runtime-stack">
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
              {boot?.capabilities.sandbox && <Field label="Sandbox" value={state.status.sandbox || sandboxInfo?.mode} values={sandboxInfo?.modes || runtimeValues.sandbox} disabled={!isLive || state.status.busy || !sandboxInfo?.canChangeDefault} onChange={(mode) => void changeSandboxDefault(mode)} />}{boot?.capabilities.sandbox && <button type="button" className="runtime-explain" onClick={() => void openSandboxExplain()}>Explain</button>}<label className="fast-toggle"><input type="checkbox" checked={fast} disabled={runtimeBusy} onChange={(event) => { setFast(event.target.checked); void op("set.fast", { enabled: event.target.checked }, selectedID); }} />FAST</label>
            </div>
          )}
        </div>
      </section>
      <RuntimeStatus status={state.status} />
      </div>
      <section className="transcript" aria-live="polite" aria-label="Conversation transcript">{!boot && transport !== "connecting" ? <div className="empty-state" role="alert"><span>ERROR</span><h1>{transport}</h1><p>Failed to load cockpit. Open the URL printed by <code>strike serve</code> (includes <code>?token=</code>), or pass a valid bearer token.</p></div> : !state.items.length && <div className="empty-state"><span>01 / READY</span><h1>{boot?.attachOnly ? "Inspect the record." : "Direct the work."}</h1><p>{boot?.attachOnly ? "Select a durable session from the rail." : "Describe an outcome. Strike will plan, act, and report through the live engine seam."}</p></div>}{state.items.map((item) => <Transcript key={item.id} item={item} />)}<div ref={endRef} /></section>
      <form className="composer" onSubmit={submit}><label htmlFor="prompt">Instruction {state.status.busy && "— send to queue"}</label><textarea aria-label="Instruction" id="prompt" value={draft} disabled={!isLive} placeholder={isLive ? "Describe the next outcome…  / command" : "Historical session — read only"} onPaste={(event) => void attach(event.clipboardData.files)} onDrop={(event) => { event.preventDefault(); void attach(event.dataTransfer.files); }} onDragOver={(event) => event.preventDefault()} onChange={(event) => setDraft(event.target.value)} onKeyDown={(event) => { if (event.key === "Enter" && !event.shiftKey && !completions.length) { event.preventDefault(); event.currentTarget.form?.requestSubmit(); } }} />{completions.length > 0 && <div className="completion" role="listbox" aria-label="Composer completions">{completions.slice(0, 8).map((item) => <button type="button" role="option" key={item.label} onClick={() => selectCompletion(item)}><strong>{item.label}</strong><span>{item.detail}</span></button>)}</div>}{images.length > 0 && <div className="attachments">{images.map((image, index) => <button type="button" key={`${image.name}-${index}`} onClick={() => setImages((list) => list.filter((_, i) => i !== index))}>{image.name} ×</button>)}</div>}{queue.length > 0 && <div className="prompt-queue-wrap"><ol ref={queueRef} className="prompt-queue" aria-label="Queued prompts">{queue.map((item, index) => <li key={index}>{queueEdit?.index === index ? <input className="queue-edit" aria-label={`Queued prompt text ${index + 1}`} value={queueEdit.text} autoFocus onChange={(event) => setQueueEdit({ index, text: event.target.value })} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); queueEditCancel.current = false; setQueue((list) => editQueuedText(list, index, queueEdit.text)); setQueueEdit(null); } if (event.key === "Escape") { event.preventDefault(); queueEditCancel.current = true; setQueueEdit(null); } }} onBlur={() => { if (!queueEditCancel.current) setQueue((list) => editQueuedText(list, index, queueEdit.text)); queueEditCancel.current = false; setQueueEdit(null); }} /> : <span>{item.text}{item.images.length > 0 ? ` (${item.images.length} img)` : ""}</span>}<span className="queue-actions"><button type="button" aria-label={`Move queued prompt ${index + 1} up`} disabled={index === 0} onClick={() => setQueue((list) => moveQueuedAt(list, index, -1))}>↑</button><button type="button" aria-label={`Move queued prompt ${index + 1} down`} disabled={index === queue.length - 1} onClick={() => setQueue((list) => moveQueuedAt(list, index, 1))}>↓</button><button type="button" aria-label={`Edit queued prompt ${index + 1}`} onClick={() => { queueEditCancel.current = false; setQueueEdit({ index, text: item.text }); }}>✎</button><button type="button" aria-label={`Remove queued prompt ${index + 1}`} onClick={() => { setQueue((list) => removeQueuedAt(list, index)); setQueueEdit((cur) => cur?.index === index ? null : cur); }}>×</button></span></li>)}</ol><div className="queue-toolbar"><button type="button" onClick={() => { setQueue(clearQueue()); setQueueEdit(null); }}>Clear queue</button></div></div>}<div><span><kbd>↵</kbd> send · <kbd>⇧↵</kbd> newline</span><span><input ref={fileRef} hidden type="file" accept="image/*" multiple onChange={(event) => void attach(event.target.files)} /><button type="button" onClick={() => fileRef.current?.click()}>Attach</button>{history.length > 0 && <button type="button" onClick={() => setDraft(history.at(-1) || "")}>History</button>}<button type="button" onClick={() => exportSession()} disabled={!state.items.length}>Export</button>{state.status.busy && <button type="button" className="stop" onClick={() => void op("interrupt")}>Interrupt</button>}<button type="submit" disabled={!draft.trim() || !isLive}>{state.status.busy ? "Queue" : "Send"}</button></span></div></form>
    </main>
    <aside className={`inspector ${inspectorOpen ? "open" : "collapsed"}`} aria-label="Inspector"><PanelResize label="Resize inspector panel" value={inspectorWidth} min={240} max={520} onChange={setInspectorWidth} side="inspector" /><div className="inspector-tabs" role="tablist">{inspectorTabs.map((tab) => <button role="tab" aria-selected={inspector === tab} key={tab} onClick={() => void inspectProject(tab)}>{tab}</button>)}</div><div className="inspector-body">{inspectorTabs.length ? <InspectorBody tab={inspectorTabs.includes(inspector) ? inspector : inspectorTabs[0]} boot={boot} status={state.status} data={projectData} loading={projectLoading} expandedDiffs={expandedDiffs} toggleDiff={toggleDiff} isLive={isLive} selectedID={selectedID} sandbox={sandboxInfo} onExplainSandbox={() => void openSandboxExplain()} /> : <p className="muted">No inspector panels available for this host.</p>}</div></aside>
    {settingsOpen && <SettingsDialog boot={boot} status={state.status} providers={providers} sandbox={sandboxInfo} onSandboxChange={(mode) => void changeSandboxDefault(mode)} onClose={() => setSettingsOpen(false)} />}
    {sandboxExplainOpen && sandboxInfo && <SandboxExplainDialog info={sandboxInfo} status={state.status} onClose={() => setSandboxExplainOpen(false)} />}
{undoDialog && <UndoPreviewDialog preview={lastUndoPreview} preferFiles={undoDialog.preferFiles} onCancel={() => setUndoDialog(null)} onConfirm={confirmUndo} />}
    {state.permission && <PermissionDialog permission={state.permission} rootID={selectedID} canExplain={Boolean(boot?.capabilities.permissions)} />}
    {state.question && <QuestionDialog question={state.question} rootID={selectedID} />}
  </div>;
}

type PermissionExplain = {
  Permission?: string; permission?: string;
  Pattern?: string; pattern?: string;
  Action?: string; action?: string;
  Layer?: string; layer?: string;
  Summary?: string; summary?: string;
};

function permissionName(data: Record<string, unknown>): string {
  return String(data.permission || data.tool || data.name || "tool");
}

function permissionPatterns(data: Record<string, unknown>): string[] {
  const raw = data.patterns;
  if (Array.isArray(raw)) return raw.map((p) => String(p)).filter(Boolean);
  if (typeof raw === "string" && raw.trim()) return [raw];
  return [];
}


function SandboxExplainDialog({ info, status, onClose }: { info: SandboxInfo; status: Status; onClose: () => void }) {
  const ref = useRef<HTMLDialogElement>(null);
  useEffect(() => { ref.current?.showModal(); }, []);
  const network = status.networkAllow || info.networkAllow || [];
  return <dialog ref={ref} className="sandbox-explain-dialog" aria-labelledby="sandbox-explain-title" onClose={onClose}><div className="dialog-rule" /><h2 id="sandbox-explain-title">Sandbox explain</h2><dl className="sandbox-summary"><dt>Active mode</dt><dd>{status.sandbox || info.mode}</dd><dt>Default mode</dt><dd>{info.defaultMode || "—"}</dd><dt>Backend</dt><dd>{status.sandboxBackend || info.backend || (info.available ? "available" : "unavailable")}</dd><dt>Network allow</dt><dd>{network.length ? network.join(", ") : "(none — unrestricted public)"}</dd><dt>Permission mode</dt><dd>{status.permissionMode || info.permissionMode || "—"}</dd></dl><pre className="sandbox-explain">{info.explain || "No profile text."}</pre>{info.note && <p className="muted">{info.note}</p>}<div className="dialog-actions"><button autoFocus onClick={onClose}>Close</button></div></dialog>;
}
function PermissionDialog({ permission, rootID, canExplain }: { permission: Record<string, unknown>; rootID: string; canExplain: boolean }) {
  const name = permissionName(permission);
  const patterns = permissionPatterns(permission);
  const sample = patterns[0] || "*";
  const [explain, setExplain] = useState<PermissionExplain | null>(null);
  const [explainError, setExplainError] = useState("");
  const [explainLoading, setExplainLoading] = useState(false);
  const reply = (decision: "once" | "always" | "project" | "reject") =>
    void op("permission.reply", { requestId: permission.requestId, decision }, rootID);
  const loadExplain = async () => {
    if (!canExplain || explainLoading) return;
    setExplainLoading(true);
    setExplainError("");
    try {
      const qs = new URLSearchParams({ permission: name, pattern: sample });
      setExplain(await request<PermissionExplain>(`/v1/permissions/explain?${qs}`));
    } catch (error) {
      setExplainError((error as Error).message);
      setExplain(null);
    } finally {
      setExplainLoading(false);
    }
  };
  const summary = explain?.Summary || explain?.summary || "";
  const action = explain?.Action || explain?.action || "";
  const layer = explain?.Layer || explain?.layer || "";
  return (
    <BlockingDialog title="Permission required">
      <p className="permission-tool"><strong>{name}</strong></p>
      {patterns.length > 0 ? (
        <ul className="permission-patterns" aria-label="Permission patterns">
          {patterns.map((pattern) => <li key={pattern}><code>{pattern}</code></li>)}
        </ul>
      ) : (
        <p className="muted">No pattern detail provided for this request.</p>
      )}
      {canExplain && (
        <div className="permission-explain">
          <button type="button" onClick={() => void loadExplain()} disabled={explainLoading}>
            {explainLoading ? "Explaining…" : "Why is this asked?"}
          </button>
          {explainError && <p className="permission-explain-error" role="status">{explainError}</p>}
          {summary && (
            <pre className="permission-explain-body" aria-label="Permission explanation">
              {summary}
              {(action || layer) && `\n\nEffective: ${action || "unknown"}${layer ? ` (${layer})` : ""}`}
            </pre>
          )}
        </div>
      )}
      <div className="dialog-actions">
        <button type="button" onClick={() => reply("reject")}>Reject</button>
        <button type="button" onClick={() => reply("project")}>Allow for project</button>
        <button type="button" onClick={() => reply("always")}>Allow session</button>
        <button type="button" autoFocus onClick={() => reply("once")}>Allow once</button>
      </div>
    </BlockingDialog>
  );
}

function PanelResize({ label, value, min, max, onChange, side }: { label: string; value: number; min: number; max: number; onChange: (value: number) => void; side: "nav" | "inspector" }) {
  const drag = useRef<{ x: number; width: number } | null>(null);
  const clamp = (next: number) => Math.min(max, Math.max(min, Math.round(next)));
  const onPointerDown = (event: React.PointerEvent<HTMLDivElement>) => {
    event.preventDefault();
    drag.current = { x: event.clientX, width: value };
    event.currentTarget.setPointerCapture(event.pointerId);
  };
  const onPointerMove = (event: React.PointerEvent<HTMLDivElement>) => {
    if (!drag.current) return;
    const delta = event.clientX - drag.current.x;
    onChange(clamp(side === "nav" ? drag.current.width + delta : drag.current.width - delta));
  };
  const onPointerUp = (event: React.PointerEvent<HTMLDivElement>) => {
    drag.current = null;
    if (event.currentTarget.hasPointerCapture(event.pointerId)) event.currentTarget.releasePointerCapture(event.pointerId);
  };
  const onKeyDown = (event: React.KeyboardEvent<HTMLDivElement>) => {
    const step = event.shiftKey ? 20 : 10;
    if (event.key === "ArrowLeft") { event.preventDefault(); onChange(clamp(value - step)); }
    if (event.key === "ArrowRight") { event.preventDefault(); onChange(clamp(value + step)); }
    if (event.key === "Home") { event.preventDefault(); onChange(min); }
    if (event.key === "End") { event.preventDefault(); onChange(max); }
  };
  return <div className={`panel-resize panel-resize-${side}`}>
    <div className="panel-resize-handle" role="separator" aria-orientation="vertical" aria-label={label} aria-valuenow={value} aria-valuemin={min} aria-valuemax={max} tabIndex={0} onPointerDown={onPointerDown} onPointerMove={onPointerMove} onPointerUp={onPointerUp} onPointerCancel={onPointerUp} onKeyDown={onKeyDown} />
  </div>;
}

function SessionMenu({ onAction }: { onAction: (action: "fork" | "rename" | "delete") => void }) {
  return <details className="session-overflow"><summary>Session…</summary><div className="session-overflow-menu" role="menu"><button type="button" role="menuitem" onClick={() => onAction("fork")}>Fork</button><button type="button" role="menuitem" onClick={() => onAction("rename")}>Rename</button><button type="button" role="menuitem" onClick={() => onAction("delete")}>Delete</button></div></details>;
}


function HistoryNav({ sessions, activeRoots, selectedID, selectedIsLive, historySearch, setHistorySearch, selectWorkspace, handleResume, boot, sessionAction }: { sessions: Session[]; activeRoots: ActiveRoot[]; selectedID: string; selectedIsLive: boolean; historySearch: string; setHistorySearch: (value: string) => void; selectWorkspace: (id: string, isLive: boolean) => void | Promise<void>; handleResume: (id: string) => Promise<void>; boot?: Bootstrap; sessionAction: (action: "fork" | "rename" | "delete") => Promise<void> }) {
  const canSessions = Boolean(boot?.capabilities.sessions);
  const hasSelection = Boolean(selectedID);
  return <><input className="history-search" type="search" placeholder="Search sessions…" aria-label="Search sessions" value={historySearch} onChange={(event) => setHistorySearch(event.target.value)} /><nav>{sessions.filter((s) => !historySearch || s.title?.toLowerCase().includes(historySearch.toLowerCase()) || s.id.toLowerCase().includes(historySearch.toLowerCase())).map((session) => { const isActiveWorkspace = activeRoots.some((r) => r.id === session.id); return <button key={session.id} type="button" className={session.id === selectedID ? "session active" : "session"} onClick={() => void selectWorkspace(session.id, isActiveWorkspace)} title={session.id}><span className="session-title">{session.title || shortID(session.id)}</span>{isActiveWorkspace && <small className="live-badge">LIVE</small>}</button>; })}</nav><div className="session-actions">{!selectedIsLive && !boot?.attachOnly && <button type="button" disabled={!hasSelection || !boot?.capabilities.roots} onClick={() => void handleResume(selectedID)}>Resume as workspace</button>}{canSessions && hasSelection && <SessionMenu onAction={(action) => void sessionAction(action)} />}</div></>;
}

function RuntimeStatus({ status }: { status: Status }) {
  const bits: string[] = [];
  if (status.phase) bits.push(`Phase ${status.phase}`);
  if (status.workflow) bits.push(`Workflow ${status.workflow}`);
  if (status.contextUsed !== undefined && status.contextLimit !== undefined) {
    bits.push(`Context ${status.contextUsed.toLocaleString()} / ${status.contextLimit.toLocaleString()}`);
  }
  if (!bits.length) return null;
  return <div className="runtime-status" aria-label="Session status">{bits.map((bit) => <span key={bit}>{bit}</span>)}</div>;
}

function InspectorBody({ tab, boot, status, data, loading, expandedDiffs, toggleDiff, isLive, selectedID, sandbox, onExplainSandbox }: { tab: InspectorTab; boot?: Bootstrap; status: Status; data: unknown; loading: boolean; expandedDiffs: Set<string>; toggleDiff: (path: string) => void; isLive: boolean; selectedID: string; sandbox?: SandboxInfo; onExplainSandbox?: () => void }) {
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
  if (tab === "mcp") {
    return <MCPPanel available={Boolean(boot?.capabilities.mcp)} />;
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

function SettingsDialog({ boot, status, providers, sandbox: sandboxInfo, onSandboxChange, onClose }: { boot?: Bootstrap; status: Status; providers: string[]; sandbox?: SandboxInfo; onSandboxChange?: (mode: string) => void; onClose: () => void }) { const ref = useRef<HTMLDialogElement>(null); const [provider, setProvider] = useState(String(status.provider || providers[0] || "")); const [key, setKey] = useState(""); useEffect(() => { ref.current?.showModal(); }, []); const save = async () => { if (boot?.capabilities.settings) await request("/v1/settings", { method: "PATCH", body: JSON.stringify({ provider: String(status.provider || ""), model: String(status.model || ""), agent: String(status.agent || ""), effort: String(status.effort || ""), mode: String(status.permissionMode || "") }) }); onClose(); }; return <dialog ref={ref} aria-labelledby="settings-title" onClose={onClose}><div className="dialog-rule" /><h2 id="settings-title">Workspace settings</h2>{boot?.capabilities.auth ? <fieldset><legend>Provider authentication</legend><label>Provider<select value={provider} onChange={(event) => setProvider(event.target.value)}>{providers.map((name) => <option key={name}>{name}</option>)}</select></label><label>API key<input value={key} onChange={(event) => setKey(event.target.value)} placeholder="Stored locally by strike" /></label><button disabled={!provider || !key} onClick={() => void request("/v1/auth/key", { method: "POST", body: JSON.stringify({ provider, key }) }).then(() => setKey(""))}>Save key</button></fieldset> : <CapabilityUnavailable name="Provider authentication" />}{boot?.capabilities.sandbox ? <fieldset><legend>OS sandbox default</legend><label>Sandbox<select aria-label="Sandbox default" value={String(sandboxInfo?.defaultMode || status.sandbox || "workspace-write")} disabled={!sandboxInfo?.canChangeDefault} onChange={(event) => onSandboxChange?.(event.target.value)}>{(sandboxInfo?.modes || runtimeValues.sandbox).map((mode) => <option key={mode}>{mode}</option>)}</select></label><p className="muted">Active session: <strong>{status.sandbox || sandboxInfo?.mode || "unknown"}</strong>. Defaults apply to new sessions only.</p></fieldset> : null}{boot?.capabilities.settings ? <p className="muted">Current defaults can be saved from the live runtime controls.</p> : <CapabilityUnavailable name="Saved defaults" />}<div className="dialog-actions"><button onClick={onClose}>Close</button><button onClick={() => void save()}>Save defaults</button></div></dialog>; }
function CapabilityUnavailable({ name }: { name: string }) { return <section className="unavailable" role="status"><strong>{name} unavailable</strong><p>The configured host did not provide this capability. No action was attempted.</p></section>; }
function CapabilityError({ error }: { error: string }) { return <section className="unavailable" role="status"><strong>Unable to load</strong><p>{error}</p></section>; }

function QuestionDialog({ question, rootID }: { question: Record<string, unknown>; rootID: string }) { const [answers, setAnswers] = useState<string[]>([]); const prompts = Array.isArray(question.questions) ? question.questions as Array<Record<string, unknown>> : [{ question: question.question }]; const update = (index: number, value: string) => setAnswers((old) => { const next = [...old]; next[index] = value; return next; }); return <BlockingDialog title={String(question.title || "Agent question")}>{prompts.map((prompt, index) => { const options = Array.isArray(prompt.options) ? prompt.options as Array<Record<string, unknown>> : []; return <fieldset key={index}><legend>{String(prompt.question || "A response is required to continue.")}</legend>{options.length ? options.map((option) => <label key={String(option.label)}><input type="radio" name={`question-${index}`} value={String(option.label)} checked={answers[index] === String(option.label)} onChange={(event) => update(index, event.target.value)} />{String(option.label)}<span>{String(option.description || "")}</span></label>) : <textarea aria-label={`Answer ${index + 1}`} value={answers[index] || ""} onChange={(event) => update(index, event.target.value)} />}</fieldset>; })}<div className="dialog-actions"><button autoFocus onClick={() => void op("question.reply", { requestId: question.requestId, answers }, rootID)}>Continue</button></div></BlockingDialog>; }

function UndoPreviewDialog({ preview, preferFiles, onCancel, onConfirm }: {
  preview: UndoPreview;
  preferFiles: boolean;
  onCancel: () => void;
  onConfirm: (restoreFiles: boolean) => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [restoreFiles, setRestoreFiles] = useState(() => defaultRestoreFiles(preview, preferFiles));
  useEffect(() => {
    const node = ref.current;
    if (!node) return;
    if (typeof node.showModal === "function" && !node.open) {
      try { node.showModal(); } catch { node.setAttribute("open", ""); }
    } else if (!node.open) {
      node.setAttribute("open", "");
    }
  }, []);
  const lines = formatUndoPreviewLines(preview);
  const choices = [
    { id: "chat", label: "chat only", detail: "drop the last turn from history; keep disk changes", value: false },
    { id: "files", label: "chat and files", detail: filesChoiceDetail(preview), value: true },
  ] as const;
  return (
    <dialog
      ref={ref}
      className="undo-dialog"
      aria-labelledby="undo-title"
      onClose={onCancel}
      onCancel={(event) => { event.preventDefault(); onCancel(); }}
    >
      <div className="dialog-rule" />
      <h2 id="undo-title">Undo last turn</h2>
      {lines.length > 0 && (
        <div className="undo-preview" role="region" aria-label="Undo preview">
          {lines.map((line, index) => (
            <p key={index} className={line.tone === "warn" ? "undo-warn" : undefined}>{line.text}</p>
          ))}
        </div>
      )}
      <div className="undo-choices" role="radiogroup" aria-label="Undo mode">
        {choices.map((choice) => (
          <label key={choice.id} className={restoreFiles === choice.value ? "undo-choice active" : "undo-choice"}>
            <input
              type="radio"
              name="undo-mode"
              value={choice.id}
              checked={restoreFiles === choice.value}
              onChange={() => setRestoreFiles(choice.value)}
            />
            <span>
              <strong>{choice.label}</strong>
              <small>{choice.detail}</small>
            </span>
          </label>
        ))}
      </div>
      <div className="dialog-actions">
        <button type="button" onClick={onCancel}>Cancel</button>
        <button type="button" autoFocus onClick={() => onConfirm(restoreFiles)}>Confirm undo</button>
      </div>
    </dialog>
  );
}
