import { FormEvent, useEffect, useMemo, useReducer, useRef, useState } from "react";
import { activateRoot, bootstrap, closeRoot, createRoot, fetchTeam, historicalConnection, liveConnection, request, resumeRoot, roots as loadRoots, sendOp, sessions as loadSessions, sessionChildren, getSandbox, patchSandbox, downloadDiagnostics, closeIssue, createIssue, deleteMemory, exportIssues, exportMemory, putMemory } from "./api";
import { ChildAgentsPanel } from "./ChildAgents";
import { TeamWorkspace } from "./Team";
import { CodeExplorer } from "./CodeExplorer";
import { buildExportMarkdown, defaultExportFilename, downloadTextFile } from "./exportMarkdown";
import { clearQueue, editQueuedText, moveQueuedAt, removeQueuedAt, type QueuedPrompt } from "./queueOps";
import { initialClientState, reduceClient, selectedSlice, setAdd, setRemove } from "./reducer";
import { buildCommandCatalog, insertMention, isFileMentionTrigger, type CatalogCommand } from "./commands";
import { CommandPalette } from "./CommandPalette";
import { formatSlashHelp, resolveSlash, WEB_SLASH_COMMANDS } from "./slash";
import { applyAppearance, loadAppearance, SettingsDialog } from "./Settings";
import { formatCostLabel, formatCostNotice } from "./cost";
import { TranscriptList } from "./Transcript";
import type {
  SandboxInfo, ActiveRoot, Bootstrap, Capabilities, FitWarning, ImageAttachment,
  RequestAttribution, Session, Status, TokenCount, WorkspaceState,
} from "./types";
import { LAYER_KINDS } from "./types";
import { MCPPanel } from "./MCP";
import { PluginsPanel } from "./Plugins";
import { PanesPanel } from "./Panes";
import { TimelinePanel } from "./Timeline";
import { DiagnosticsPanel } from "./Diagnostics";
import { PlansPanel } from "./Plans";
import {
  defaultRestoreFiles,
  emptyUndoPreview,
  filesChoiceDetail,
  formatUndoPreviewLines,
  type UndoPreview,
} from "./undoPreview";
import { GoalsPanel } from "./Goals";
import { WorkflowsPanel } from "./Workflows";
import { Tabs } from "./ui";
import {
  deepLinkWorkspaceID,
  resolveDeepLink,
  writeDeepLinkToLocation,
} from "./deepLink";
import {
  defaultSurfaceForMode,
  getSurface,
  inspectorSurfacesForShell,
  modeAttention,
  MODE_PRESETS,
  WORKSPACE_MODES,
  type ActivitySignals,
  type WorkspaceMode,
} from "./surfaces";
import {
  modesInBottomBar,
  shellProfileFromWidth,
  type ShellProfile,
} from "./shellProfile";
import "./styles.css";

type Completion = { label: string; detail: string; insert: string };
type ChangedFile = { path: string; added: number; deleted: number; diff: string };
type MemoryEntry = { Key?: string; key?: string; Value?: string; value?: string; Tags?: string[]; tags?: string[] };
type IssueEntry = { ID?: number; id?: number; Title?: string; title?: string; Body?: string; body?: string; Status?: string; status?: string };
type UndoDialogState = { preferFiles: boolean };
const runtimeValues = { effort: ["low", "medium", "high", "xhigh"], autonomy: ["supervised", "agent", "checks", "skip-all"], permission: ["default", "plan", "soft-approve", "accept-edits", "yolo"], sandbox: ["off", "read-only", "workspace-write"] };

const THINK_STORAGE_KEY = "strike.web.showThinking";
const readShowThinking = (): boolean => {
  try {
    const raw = sessionStorage.getItem(THINK_STORAGE_KEY);
    if (raw === null) return true;
    return raw === "1" || raw === "true";
  } catch {
    return true;
  }
};
const writeShowThinking = (value: boolean) => {
  try { sessionStorage.setItem(THINK_STORAGE_KEY, value ? "1" : "0"); } catch { /* private mode */ }
};

/** Format session token/cost chrome for the context inspector. */
export function formatContextLabel(status: Status): string {
  if (status.contextUsed !== undefined && status.contextLimit !== undefined) {
    return `${status.contextUsed.toLocaleString()} / ${status.contextLimit.toLocaleString()}`;
  }
  if (status.contextUsed !== undefined) return `${status.contextUsed.toLocaleString()} used`;
  return "not reported";
}

export { formatCostLabel } from "./cost";
const slashCommands: Completion[] = WEB_SLASH_COMMANDS;

const op = (type: string, data?: unknown, rootID?: string) => sendOp(type, data, rootID).catch((error) => window.alert(error.message));

const shortID = (id: string) => id.slice(0, 12);
/** Lightweight multi-root attention poll interval (documented for #919). */
const ROOTS_POLL_MS = 2000;
const rootNeedsYou = (root: ActiveRoot) => Boolean(root.permissionPending || root.questionPending);
const rootTitle = (root: ActiveRoot, sessions: Session[]) => root.title || sessions.find((s) => s.id === root.id)?.title || shortID(root.id);
const relativeActivity = (ms?: number) => {
  if (!ms) return "";
  const tms = ms < 1e12 ? ms * 1000 : ms;
  const sec = Math.max(0, Math.floor((Date.now() - tms) / 1000));
  if (sec < 60) return "just now";
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
};
const deepLinkID = () => {
  try {
    return deepLinkWorkspaceID(location.search);
  } catch {
    return "";
  }
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
  const [selectedIsLive, setSelectedIsLive] = useState(false);
  const [navTab, setNavTab] = useState<"active" | "history">("active");
  const [historySearch, setHistorySearch] = useState("");
  const [client, dispatch] = useReducer(reduceClient, undefined, initialClientState);
  const selectedID = client.selectedID;
  const state = selectedSlice(client);
  const draft = state.draft;
  const queue = state.queue;
  const images = state.images;
  const fast = state.fast;
  const [transport, setTransport] = useState("connecting");
  const [queueEdit, setQueueEdit] = useState<{ index: number; text: string } | null>(null);
  const queueRef = useRef<HTMLOListElement>(null);
  const queueEditCancel = useRef(false);
  const [undoDialog, setUndoDialog] = useState<UndoDialogState | null>(null);
  const [workspaceMode, setWorkspaceMode] = useState<WorkspaceMode>("chat");
  const [inspector, setInspector] = useState<string>("context");
  const [inspectorOpen, setInspectorOpen] = useState(false);
  const [surfaceEntity, setSurfaceEntity] = useState("");
  const [surfaceUnavailable, setSurfaceUnavailable] = useState<{ id: string; reason: string } | undefined>();
  const deepLinkApplied = useRef(false);
  const [shellProfile, setShellProfile] = useState<ShellProfile>(() =>
    typeof window !== "undefined" ? shellProfileFromWidth(window.innerWidth) : "desktop",
  );
  const [navOpen, setNavOpen] = useState(() =>
    typeof window !== "undefined" ? shellProfileFromWidth(window.innerWidth) === "desktop" : true,
  );
  const [navWidth, setNavWidth] = useState(240);
  const [inspectorWidth, setInspectorWidth] = useState(340);
  const [projectData, setProjectData] = useState<unknown>();
  const [projectLoading, setProjectLoading] = useState(false);
  const [expandedDiffs, setExpandedDiffs] = useState<Set<string>>(new Set());
  const [providers, setProviders] = useState<string[]>([]);
  const [models, setModels] = useState<string[]>([]);
  const [history, setHistory] = useState<string[]>([]);
  const [historyBrowse, setHistoryBrowse] = useState<{ index: number; draft: string } | null>(null);
  const [paletteOpen, setPaletteOpen] = useState(false);
  const [fileHits, setFileHits] = useState<string[]>([]);
  const [mention, setMention] = useState<{ start: number; query: string } | null>(null);
  const [onboardingHint, setOnboardingHint] = useState(() => {
    try { return localStorage.getItem("strike.web.onboard.v1") !== "1"; } catch { return false; }
  });
  const [runtimeOpen, setRuntimeOpen] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const promptRef = useRef<HTMLTextAreaElement>(null);
  const paletteInvoker = useRef<HTMLElement | null>(null);
  const [showThinking, setShowThinking] = useState(readShowThinking);
  const [modelRates, setModelRates] = useState<{ inputPerM: number; outputPerM: number; hasCost: boolean; context?: number }>();
  const [sandboxInfo, setSandboxInfo] = useState<SandboxInfo>();
  const [sandboxExplainOpen, setSandboxExplainOpen] = useState(false);
  const [selectedChildId, setSelectedChildId] = useState<string>();
  const endRef = useRef<HTMLDivElement>(null);
  const transcriptRef = useRef<HTMLElement>(null);
  const stickToBottomRef = useRef(true);
  const [showJumpLatest, setShowJumpLatest] = useState(false);
  const [liveStatusAnn, setLiveStatusAnn] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const setDraft = (value: string | ((prev: string) => string)) => {
    if (!selectedID) return;
    const next = typeof value === "function" ? value(draft) : value;
    dispatch({ type: "client.composer", id: selectedID, patch: { draft: next } });
  };
  const setImages = (value: ImageAttachment[] | ((prev: ImageAttachment[]) => ImageAttachment[])) => {
    if (!selectedID) return;
    const next = typeof value === "function" ? value(images) : value;
    dispatch({ type: "client.composer", id: selectedID, patch: { images: next } });
  };
  const setQueue = (value: typeof queue | ((prev: typeof queue) => typeof queue)) => {
    if (!selectedID) return;
    const next = typeof value === "function" ? value(queue) : value;
    dispatch({ type: "client.composer", id: selectedID, patch: { queue: next } });
  };
  const setFast = (value: boolean | ((prev: boolean) => boolean)) => {
    if (!selectedID) return;
    const next = typeof value === "function" ? value(fast) : value;
    dispatch({ type: "client.composer", id: selectedID, patch: { fast: next } });
  };

  const refreshSessions = () => loadSessions().then((list) => { setSessions(list.sessions || []); setLiveID(list.liveId || ""); return list; });
  const refreshRoots = () => loadRoots().then((r) => { setActiveRoots(r.roots || []); setActiveRootID(r.activeId || ""); return r; }).catch(() => undefined);
  useEffect(() => { applyAppearance(loadAppearance()); }, []);
  useEffect(() => {
    const sync = () => {
      const next = shellProfileFromWidth(window.innerWidth);
      setShellProfile((prev) => {
        if (prev !== next) {
          setNavOpen(next === "desktop");
          if (next !== "desktop") setInspectorOpen(false);
        }
        return next;
      });
    };
    sync();
    window.addEventListener("resize", sync);
    return () => window.removeEventListener("resize", sync);
  }, []);
  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        paletteInvoker.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
        setPaletteOpen(true);
        return;
      }
      if (event.key !== "Escape") return;
      if (document.querySelector("dialog[open]")) return;
      if (paletteOpen) {
        event.preventDefault();
        setPaletteOpen(false);
        return;
      }
      if (inspectorOpen) {
        event.preventDefault();
        setInspectorOpen(false);
        return;
      }
      if (navOpen && shellProfile !== "desktop") {
        event.preventDefault();
        setNavOpen(false);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [inspectorOpen, navOpen, shellProfile, paletteOpen]);
  useEffect(() => {
    // roots is optional (attach-only returns 503) — do not fail the whole boot.
    Promise.all([
      bootstrap(),
      loadSessions(),
      loadRoots().catch(() => ({ roots: [] as ActiveRoot[], activeId: "" })),
    ]).then(([nextBoot, list, r]) => {
      setBoot(nextBoot);
      setTransport("connected");
      setSessions(list.sessions || []);
      const hasRoots = Boolean(nextBoot.capabilities.roots && r.roots?.length);
      setLiveID(list.liveId || nextBoot.status?.sessionId || "");
      const rootsArr = hasRoots ? (r.roots || []) : [];
      setActiveRoots(rootsArr);
      setActiveRootID(r.activeId || list.liveId || nextBoot.status?.sessionId || "");
      const sessionsArr = list.sessions || [];
      const want = deepLinkID();
      const liveIDs = new Set(rootsArr.map((r) => r.id));
      let firstLive = rootsArr[0]?.id || (nextBoot.capabilities.roots ? "" : list.liveId) || "";
      let firstID = firstLive || sessionsArr[0]?.id || "";
      let pickLive = Boolean(firstLive && firstID === firstLive);
      if (want) {
        if (liveIDs.has(want) || (!nextBoot.capabilities.roots && want === (list.liveId || ""))) {
          firstID = want; firstLive = want; pickLive = true;
        } else if (sessionsArr.some((s) => s.id === want)) {
          firstID = want; pickLive = false;
        }
      }
      if (firstID) {
        dispatch({ type: "client.ensure", id: firstID });
        if (nextBoot.status && pickLive) dispatch({ type: "client.event", id: firstID, envelope: { type: "status", data: nextBoot.status } });
      }
      setSelectedIsLive(pickLive);
      setNavTab(pickLive ? "active" : "history");
      if (pickLive && want && nextBoot.capabilities.roots && !nextBoot.attachOnly) {
        void activateRoot(want).then(() => refreshRoots()).catch(() => {});
      }
      if (nextBoot.capabilities.auth) request<{ providers: Array<{ Name?: string; name?: string }> }>("/v1/providers").then((v) => setProviders(v.providers.map((p) => p.Name || p.name || "").filter(Boolean))).catch(() => {});
      if (nextBoot.capabilities.history) request<{ entries: string[] }>("/v1/history").then((v) => setHistory(v.entries || [])).catch(() => {});
      if (nextBoot.capabilities.sandbox) getSandbox().then(setSandboxInfo).catch(() => {});
    }).catch((error) => setTransport(error.message));
  }, []);

  // Background attention: poll GET /v1/roots (busy / hasRecentEvent / pending asks).
  // Does not open secondary WS streams — selected transcript stays isolated.
  useEffect(() => {
    if (!boot?.capabilities.roots || boot.attachOnly) return;
    const tick = () => { void refreshRoots(); };
    const id = window.setInterval(tick, ROOTS_POLL_MS);
    return () => window.clearInterval(id);
  }, [boot?.capabilities.roots, boot?.attachOnly]);

  useEffect(() => {
    if (!selectedID) return;
    dispatch({ type: "client.ensure", id: selectedID });
    setSelectedChildId(undefined);
    const id = selectedID;
    // One WS (or SSE) for the *viewed* root only. Background attention (#919) may
    // add multiplexed subscriptions later without changing this viewed-root path.
    if (!selectedIsLive) {
      return historicalConnection(id, (envelope) => dispatch({ type: "client.event", id, envelope }), (message) => setTransport(message));
    }
    const live = liveConnection(id, (envelope) => dispatch({ type: "client.event", id, envelope }), setTransport);
    return () => live.close();
  }, [selectedID, selectedIsLive]);
  useEffect(() => {
    if (!selectedID || !boot?.capabilities.sessions) return;
    let cancelled = false;
    void sessionChildren(selectedID)
      .then((res) => {
        if (!cancelled) dispatch({ type: "client.event", id: selectedID, envelope: { type: "children.seed", time: `seed:${selectedID}`, data: { sessions: res.sessions || [] } } });
      })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [selectedID, boot?.capabilities.sessions]);
  useEffect(() => {
    const el = transcriptRef.current;
    if (!el) return;
    const onScroll = () => {
      const dist = el.scrollHeight - el.scrollTop - el.clientHeight;
      const atBottom = dist < 80;
      stickToBottomRef.current = atBottom;
      setShowJumpLatest(!atBottom && state.items.length > 0);
    };
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => el.removeEventListener("scroll", onScroll);
  }, [state.items.length]);
  useEffect(() => {
    if (!stickToBottomRef.current) {
      setShowJumpLatest(state.items.length > 0);
      return;
    }
    endRef.current?.scrollIntoView({ block: "end" });
    setShowJumpLatest(false);
  }, [state.items]);
  // Batched status announcements (not per-token transcript floods).
  useEffect(() => {
    const bits: string[] = [];
    if (state.status.busy) bits.push("Agent working");
    else if (state.items.length) bits.push("Agent idle");
    if (state.permission) bits.push("Permission required");
    if (state.question) bits.push("Question pending");
    const next = bits.join(". ");
    const t = window.setTimeout(() => setLiveStatusAnn(next), 400);
    return () => window.clearTimeout(t);
  }, [state.status.busy, state.permission, state.question, state.items.length]);
  const openFileRef = (path: string, _line?: number) => {
    setSurfaceUnavailable(undefined);
    setSurfaceEntity(path);
    writeDeepLinkToLocation({ mode: "code", surface: "files", entity: path });
    setWorkspaceMode("code");
    setInspector("files");
    setInspectorOpen(true);
    void inspectProject("files");
  };
  const jumpToLatest = () => {
    stickToBottomRef.current = true;
    setShowJumpLatest(false);
    endRef.current?.scrollIntoView({ block: "end", behavior: "smooth" });
  };
  useEffect(() => {
    if (!selectedID || state.status.busy || !queue.length || !selectedIsLive) return;
    const [next, ...rest] = queue;
    dispatch({ type: "client.composer", id: selectedID, patch: { queue: rest } });
    void op("user.input", { text: next.text, images: next.images.map(({ mime, data }) => ({ mime, data })) }, selectedID);
  }, [state.status.busy, queue, selectedIsLive, selectedID]);

  const isLive = Boolean(selectedIsLive && selectedID && !boot?.attachOnly);
  const runtimeBusy = !isLive || state.status.busy;
  const runtimeSummary = [state.status.effort, state.status.autonomy, state.status.permissionMode, fast ? "fast" : ""].filter(Boolean).join(" · ");
  const children = useMemo(() => Object.entries(state.children), [state.children]);
  const inspectorTabDefs = useMemo(
    () => inspectorSurfacesForShell(workspaceMode, boot?.capabilities, {
      attachOnly: boot?.attachOnly,
      isLive: selectedIsLive,
      forceIds: surfaceUnavailable ? [surfaceUnavailable.id] : [],
    }),
    [boot, workspaceMode, selectedIsLive, surfaceUnavailable],
  );
  const inspectorTabs = useMemo(() => inspectorTabDefs.map((s) => s.id), [inspectorTabDefs]);
  const activitySignals: ActivitySignals = useMemo(() => ({
    changedFiles: state.changedFiles?.length || 0,
    teamMembers: Math.max(
      Object.keys(state.children || {}).length,
      Object.keys(state.team?.members || {}).length,
    ),
    permissionPending: Boolean(state.permission) || activeRoots.some((r) => r.permissionPending),
    questionPending: Boolean(state.question) || activeRoots.some((r) => r.questionPending),
    fitWarning: Boolean(state.fitWarning),
  }), [state.changedFiles, state.children, state.permission, state.question, state.fitWarning, activeRoots]);
  const activeInspector = inspectorTabs.includes(inspector)
    ? inspector
    : (inspectorTabs[0] || "");
  const shellStyle = {
    "--nav-width": shellProfile === "desktop" ? (navOpen ? `${navWidth}px` : "0px") : `${navWidth}px`,
    "--inspector-width": shellProfile === "desktop" ? (inspectorOpen ? `${inspectorWidth}px` : "0px") : `${inspectorWidth}px`,
  } as React.CSSProperties;
  const phoneModes = modesInBottomBar(shellProfile);
  const overlayOpen = shellProfile !== "desktop" && (navOpen || inspectorOpen);
  const catalog = useMemo(
    () => buildCommandCatalog({ skills: boot?.skills, attachOnly: boot?.attachOnly, capabilities: boot?.capabilities }),
    [boot],
  );
  const completions = useMemo(() => {
    if (mention && fileHits.length) {
      return fileHits.map((path) => ({ label: path, detail: "workspace file", insert: path }));
    }
    const token = draft.split(/\s/).at(-1) || "";
    if (token.startsWith("/")) {
      return [...slashCommands, ...(boot?.skills || []).map((skill) => ({ label: `/${skill.name}`, detail: skill.description || "Skill", insert: `/${skill.name}` }))].filter((item) => item.label.startsWith(token));
    }
    return [];
  }, [draft, boot, mention, fileHits]);

  useEffect(() => {
    if (!mention || !boot?.capabilities.files) {
      setFileHits([]);
      return;
    }
    const q = mention.query;
    const handle = window.setTimeout(() => {
      void request<{ paths: string[] }>(`/v1/files/search?q=${encodeURIComponent(q)}&limit=12`)
        .then((res) => setFileHits(res.paths || []))
        .catch(() => setFileHits([]));
    }, 120);
    return () => window.clearTimeout(handle);
  }, [mention, boot?.capabilities.files]);

  const enqueueHistory = (prompt: string) => {
    if (!boot?.capabilities.history || !prompt.trim()) return;
    void request("/v1/history", { method: "POST", body: JSON.stringify({ prompt }) })
      .then(() => request<{ entries: string[] }>("/v1/history"))
      .then((v) => setHistory(v.entries || []))
      .catch(() => {});
  };

  const notice = (title: string, body: string) => { if (!selectedID) return; dispatch({ type: "client.event", id: selectedID, envelope: { type: "local.system", time: String(Date.now()), data: { title, text: body } } }); };
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
        enqueueHistory(command);
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
        notice("Cost", formatCostNotice(state.status, modelRates));
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
    if (state.status.busy) {
      enqueueHistory(text);
      setQueue((items) => [...items, { text, images }]);
    } else execute(text, images);
    setHistoryBrowse(null);
    setDraft(""); setImages([]);
    setMention(null);
  };
  const selectCompletion = (item: Completion) => {
    if (mention) {
      const cursor = promptRef.current?.selectionStart ?? draft.length;
      setDraft(insertMention(draft, mention.start, cursor, item.insert));
      setMention(null);
      setFileHits([]);
      return;
    }
    setDraft((value) => `${value.slice(0, value.lastIndexOf(value.split(/\s/).at(-1) || ""))}${item.insert} `);
  };
  const runCatalogCommand = (cmd: CatalogCommand) => {
    const a = cmd.action;
    if (cmd.requiresLive && (!isLive || boot?.attachOnly)) {
      notice("Unavailable", boot?.attachOnly ? "Attach-only: this action requires a live workspace." : "Not available without a live session.");
      return;
    }
    if (cmd.requiresCapability && boot && !boot.capabilities[cmd.requiresCapability]) {
      notice("Unavailable", `${cmd.requiresCapability} capability is not available on this host.`);
      return;
    }
    switch (a.type) {
      case "mode":
        setMode(a.mode, { openDrawer: a.mode !== "chat" });
        return;
      case "surface":
        setMode(a.mode, { openDrawer: true, surface: a.surface });
        return;
      case "slash":
        if (a.run) {
          execute(a.insert.trim(), []);
        } else {
          setDraft(a.insert);
          promptRef.current?.focus();
        }
        return;
      case "session":
        if (a.action === "export") exportSession();
        else if (a.action === "help") notice("Help", formatSlashHelp(boot?.skills || []));
        else if (a.action === "settings") setSettingsOpen(true);
        else if (a.action === "cost") notice("Cost", formatCostNotice(state.status));
        else if (a.action === "fork") void sessionAction("fork");
        else if (a.action === "interrupt") void op("interrupt");
        return;
      case "notice":
        notice(a.title, a.body);
        return;
    }
  };
  const onComposerKeyDown = (event: React.KeyboardEvent<HTMLTextAreaElement>) => {
    const el = event.currentTarget;
    if (event.key === "Enter" && !event.shiftKey && !completions.length) {
      event.preventDefault();
      el.form?.requestSubmit();
      return;
    }
    // History browse when draft empty or already browsing (TUI parity).
    const atStart = el.selectionStart === 0 && el.selectionEnd === 0;
    if ((event.key === "ArrowUp" || event.key === "ArrowDown") && history.length && (historyBrowse || (draft === "" && atStart))) {
      event.preventDefault();
      if (event.key === "ArrowUp") {
        if (!historyBrowse) {
          setHistoryBrowse({ index: history.length - 1, draft });
          setDraft(history[history.length - 1] || "");
        } else {
          const next = Math.max(0, historyBrowse.index - 1);
          setHistoryBrowse({ ...historyBrowse, index: next });
          setDraft(history[next] || "");
        }
      } else if (historyBrowse) {
        if (historyBrowse.index >= history.length - 1) {
          setDraft(historyBrowse.draft);
          setHistoryBrowse(null);
        } else {
          const next = historyBrowse.index + 1;
          setHistoryBrowse({ ...historyBrowse, index: next });
          setDraft(history[next] || "");
        }
      }
      return;
    }
  };
  const onComposerChange = (value: string) => {
    if (historyBrowse) setHistoryBrowse(null);
    setDraft(value);
    const cursor = promptRef.current?.selectionStart ?? value.length;
    const trig = isFileMentionTrigger(value, cursor);
    setMention(trig.active ? { start: trig.start, query: trig.query } : null);
  };
  const attach = async (files: FileList | null) => { if (!files) return; try { const added = await Promise.all([...files].map(readAttachment)); setImages((old) => [...old, ...added]); } catch (error) { window.alert((error as Error).message); } };
  const selectProvider = async (provider: string) => {
    void op("select.model", { provider }, selectedID);
    if (boot?.capabilities.catalog) {
      request<{ models: Array<Record<string, unknown>> }>(`/v1/models?provider=${encodeURIComponent(provider)}`)
        .then((v) => {
          const list = v.models || [];
          setModels(list.map((m) => String(m.ID || m.id || "")).filter(Boolean));
          const current = state.status.model;
          const match = list.find((m) => String(m.ID || m.id || "") === current) || list[0];
          if (match) {
            setModelRates({
              inputPerM: Number(match.InputCost ?? match.inputCost ?? 0),
              outputPerM: Number(match.OutputCost ?? match.outputCost ?? 0),
              hasCost: Boolean(match.HasCost ?? match.hasCost),
              context: Number(match.Context ?? match.context ?? 0) || undefined,
            });
          } else setModelRates(undefined);
        })
        .catch(() => { setModels([]); setModelRates(undefined); });
    }
  };

  useEffect(() => {
    if (!boot?.capabilities.catalog || !state.status.provider) return;
    request<{ models: Array<Record<string, unknown>> }>(`/v1/models?provider=${encodeURIComponent(state.status.provider)}`)
      .then((v) => {
        const list = v.models || [];
        setModels(list.map((m) => String(m.ID || m.id || "")).filter(Boolean));
        const match = list.find((m) => String(m.ID || m.id || "") === state.status.model);
        if (!match) return;
        setModelRates({
          inputPerM: Number(match.InputCost ?? match.inputCost ?? 0),
          outputPerM: Number(match.OutputCost ?? match.outputCost ?? 0),
          hasCost: Boolean(match.HasCost ?? match.hasCost),
          context: Number(match.Context ?? match.context ?? 0) || undefined,
        });
      })
      .catch(() => {});
  }, [boot?.capabilities.catalog, state.status.provider, state.status.model]);
  const inspectProject = async (tab: string, opts?: { open?: boolean; mode?: WorkspaceMode }) => {
    setInspector(tab);
    if (tab === "settings") {
      setSettingsOpen(true);
      if (opts?.open !== false) setInspectorOpen(false);
      setSurfaceUnavailable(undefined);
      return;
    }
    const def = getSurface(tab);
    const unavailable = (() => {
      if (!def || !boot) return undefined;
      if (def.capability === "always" || def.capability === "team" || def.capability === "live" || def.capability === "sessions") {
        return undefined;
      }
      if (def.capability === "lsp" && !boot.capabilities.lsp) {
        return { id: tab, reason: "capability lsp unavailable" };
      }
      if (typeof def.capability === "string" && def.capability !== "lsp" && !boot.capabilities[def.capability]) {
        return { id: tab, reason: `capability ${def.capability} unavailable` };
      }
      return undefined;
    })();
    setSurfaceUnavailable(unavailable);
    if (opts?.open !== false) setInspectorOpen(true);
    setProjectLoading(true); setProjectData(undefined);
    try {
      if (unavailable) return;
      if (tab === "files") setProjectData(boot?.capabilities.files ? await request(`/v1/changed-files${selectedID ? `?root=${encodeURIComponent(selectedID)}` : ""}`).catch((error) => ({ error: error.message })) : undefined);
      else if (tab === "memory") setProjectData(boot?.capabilities.memory ? await request("/v1/memory").catch((error) => ({ error: error.message })) : undefined);
      else if (tab === "issues") setProjectData(boot?.capabilities.issues ? await request("/v1/issues").catch((error) => ({ error: error.message })) : undefined);
      else setProjectData(undefined);
    } finally { setProjectLoading(false); }
  };

  const setMode = (mode: WorkspaceMode, opts?: { surface?: string; openDrawer?: boolean; entity?: string }) => {
    setWorkspaceMode(mode);
    if (opts?.entity !== undefined) setSurfaceEntity(opts.entity);
    if (mode === "chat" && !opts?.surface) {
      // Stay on chat; do not force drawer.
      if (opts?.openDrawer === false || opts?.openDrawer === undefined) {
        /* keep inspector as-is unless explicitly opening a surface */
      }
      writeDeepLinkToLocation({
        mode: "chat",
        surface: "",
        entity: opts?.entity || "",
      });
      return;
    }
    const surface = opts?.surface || defaultSurfaceForMode(mode, boot?.capabilities, {
      attachOnly: boot?.attachOnly,
      isLive: selectedIsLive,
    });
    if (surface) {
      void inspectProject(surface, { open: opts?.openDrawer !== false, mode });
    } else if (mode === "ops") {
      setSettingsOpen(true);
    }
    writeDeepLinkToLocation({
      mode,
      surface: surface || "",
      entity: opts?.entity || surfaceEntity,
    });
  };

  // Apply additive deep-link mode/surface once bootstrap is ready. Root/session handled above.
  useEffect(() => {
    if (!boot || deepLinkApplied.current) return;
    deepLinkApplied.current = true;
    const resolved = resolveDeepLink(location.search, boot.capabilities, {
      attachOnly: boot.attachOnly,
      isLive: selectedIsLive || Boolean(boot.capabilities.live),
    });
    setWorkspaceMode(resolved.mode);
    if (resolved.entity) setSurfaceEntity(resolved.entity);
    if (resolved.agent) setSelectedChildId(resolved.agent);
    if (resolved.unavailable) setSurfaceUnavailable(resolved.unavailable);
    if (resolved.openSettings) setSettingsOpen(true);
    if (resolved.surface) {
      void inspectProject(resolved.surface, { open: resolved.openDrawer, mode: resolved.mode });
    } else {
      // Prefer files when present, else first available tab; hydrate without forcing open (#912).
      const tabs = inspectorSurfacesForShell("chat", boot.capabilities, { attachOnly: boot.attachOnly });
      if (tabs.length) {
        const preferred = tabs.find((t) => t.id === "files")?.id || tabs[0].id;
        void inspectProject(preferred, { open: false, mode: "chat" });
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- one-shot when boot lands
  }, [boot]);
  const sessionAction = async (action: "fork" | "rename" | "delete") => {
    if (!boot?.capabilities.sessions || !selectedID) return;
    const id = selectedID;
    try {
      if (action === "fork") {
        // Default: stay on current selection; new fork appears in HISTORY (contract #916).
        await request(`/v1/sessions/${encodeURIComponent(id)}/fork`, { method: "POST" });
        await Promise.all([refreshSessions(), refreshRoots()]);
        return;
      }
      if (action === "rename") {
        const title = window.prompt("Session title");
        if (title === null) return;
        await request(`/v1/sessions/${encodeURIComponent(id)}`, { method: "PATCH", body: JSON.stringify({ title }) });
      }
      if (action === "delete") {
        if (!window.confirm("Delete this durable session?")) return;
        await request(`/v1/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
        dispatch({ type: "client.drop", id });
        const list = await refreshSessions();
        await refreshRoots();
        const next = (list?.sessions || []).find((s) => s.id !== id)?.id || "";
        if (next) {
          dispatch({ type: "client.ensure", id: next });
          const live = Boolean((list?.liveId && next === list.liveId) || activeRoots.some((r) => r.id === next));
          setSelectedIsLive(live);
          setNavTab(live ? "active" : "history");
        }
        return;
      }
      await Promise.all([refreshSessions(), refreshRoots()]);
    } catch (error) {
      window.alert((error as Error).message);
    }
  };
  const handleCreateWorkspace = async () => { if (!boot?.capabilities.roots || boot.attachOnly) return; try { const result = await createRoot(); await refreshRoots(); dispatch({ type: "client.ensure", id: result.id }); setSelectedIsLive(true); setNavTab("active"); } catch (error) { window.alert((error as Error).message); } };
  const handleResume = async (id: string) => { if (!boot?.capabilities.roots || boot.attachOnly) return; try { const result = await resumeRoot(id); await refreshRoots(); dispatch({ type: "client.ensure", id: result.id }); setSelectedIsLive(true); setNavTab("active"); } catch (error) { window.alert((error as Error).message); } };
  const selectWorkspace = async (id: string, isLive: boolean) => {
    dispatch({ type: "client.ensure", id });
    setSelectedIsLive(isLive);
    setModelRates(undefined);
    setSurfaceEntity("");
    setSelectedChildId(undefined);
    if (!isLive || !boot?.capabilities.roots || boot.attachOnly) return;
    try {
      await activateRoot(id);
      await refreshRoots();
      if (boot?.capabilities.team) {
        const snap = await fetchTeam(id).catch(() => null);
        if (snap) {
          dispatch({ type: "client.event", id, envelope: { type: "team.snapshot", time: `snap:${id}`, data: snap } });
        }
      }
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
        dispatch({ type: "client.ensure", id: nextLive });
        setSelectedIsLive(true);
        setNavTab("active");
      } else {
        const first = sessionsResult?.sessions?.[0]?.id || "";
        if (first) dispatch({ type: "client.ensure", id: first });
        setSelectedIsLive(false);
        setNavTab("history");
      }
    } catch (error) {
      window.alert((error as Error).message);
    }
  };
  const cycleWorkspace = (delta: number) => {
    if (!boot?.capabilities.roots) {
      if (!sessions.length) return;
      const idx = Math.max(0, sessions.findIndex((s) => s.id === selectedID));
      const next = sessions[(idx + delta + sessions.length) % sessions.length];
      if (next) {
        dispatch({ type: "client.ensure", id: next.id });
        setSelectedIsLive(next.id === liveID && !boot?.attachOnly);
      }
      return;
    }
    if (navTab === "active" && activeRoots.length) {
      const idx = Math.max(0, activeRoots.findIndex((r) => r.id === selectedID && selectedIsLive));
      const next = activeRoots[(idx + delta + activeRoots.length) % activeRoots.length];
      if (next) void selectWorkspace(next.id, true);
      return;
    }
    if (sessions.length) {
      const idx = Math.max(0, sessions.findIndex((s) => s.id === selectedID));
      const next = sessions[(idx + delta + sessions.length) % sessions.length];
      if (next) {
        const live = activeRoots.some((r) => r.id === next.id);
        void selectWorkspace(next.id, live);
      }
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
  const needsYouRoots = activeRoots.filter((r) => rootNeedsYou(r) && !(r.id === selectedID && selectedIsLive));
  const attentionCount = needsYouRoots.length;
  const headerAttention = attentionCount > 0
    ? `${attentionCount} need${attentionCount === 1 ? "s" : ""} you`
    : "";

  const renderModeButtons = (className: string) => (
    <nav className={className} aria-label="Workspace mode">
      {WORKSPACE_MODES.map((mode) => {
        const preset = MODE_PRESETS[mode];
        const attention = modeAttention(mode, activitySignals);
        const selected = workspaceMode === mode;
        return (
          <button
            key={mode}
            type="button"
            className={selected ? "mode-btn active" : "mode-btn"}
            aria-pressed={selected}
            aria-current={selected ? "page" : undefined}
            aria-label={`${preset.label}: ${preset.description}${attention === "needs-you" ? ", needs attention" : attention === "badge" ? ", activity" : ""}`}
            onClick={() => {
              if (phoneModes) setNavOpen(false);
              setMode(mode, { openDrawer: mode !== "chat" });
            }}
          >
            <span>{preset.label}</span>
            {attention !== "none" && <span className={attention === "needs-you" ? "mode-attn needs-you" : "mode-attn"} aria-hidden />}
          </button>
        );
      })}
    </nav>
  );

  return <div className="app-shell" style={shellStyle} data-shell={shellProfile}>
    <header>
      <button
        className="icon-button"
        aria-label="Toggle agents panel"
        aria-pressed={navOpen}
        onClick={() => {
          setInspectorOpen(false);
          setNavOpen((open) => !open);
        }}
      >☰</button>
      <div className="wordmark"><span className="mark">S</span><strong>STRIKE</strong><small>workspace</small></div>
      <button
        type="button"
        className="palette-trigger"
        aria-label="Open command palette"
        onClick={() => {
          paletteInvoker.current = document.activeElement instanceof HTMLElement ? document.activeElement : null;
          setPaletteOpen(true);
        }}
      >
        Commands <kbd>⌘K</kbd>
      </button>
      {!phoneModes && renderModeButtons("mode-switch header-mode-switch")}
      <div className="session-line" aria-live="polite">
        <span className={state.status.busy ? "pulse busy" : "pulse"} />
        {!phoneModes && (state.status.busy ? "agent working" : transport)}
        {headerAttention && (
          <button
            type="button"
            className="attention-summary"
            aria-label={headerAttention}
            onClick={() => {
              const target = needsYouRoots[0];
              if (target) void selectWorkspace(target.id, true);
            }}
          >{headerAttention}</button>
        )}
      </div>
      <button className="icon-button" aria-label="Export markdown" title="Export markdown" onClick={() => exportSession()}>↓</button>
      <button className="icon-button" aria-label="Open settings" onClick={() => setSettingsOpen(true)}>⚙</button>
      <button
        className="icon-button"
        aria-label="Toggle inspector"
        aria-pressed={inspectorOpen}
        onClick={() => {
          if (shellProfile !== "desktop") setNavOpen(false);
          setInspectorOpen((open) => !open);
        }}
      >◫</button>
    </header>
    {overlayOpen && (
      <button
        type="button"
        className="shell-backdrop visible"
        aria-label="Close panel"
        onClick={() => {
          setNavOpen(false);
          setInspectorOpen(false);
        }}
      />
    )}
    <aside className={`navigation ${navOpen ? "open" : "collapsed"}`} aria-label="Agents panel" tabIndex={0} onKeyDown={(event) => {
      if (event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement) return;
      if (event.key === "ArrowDown" || event.key === "j") { event.preventDefault(); cycleWorkspace(1); }
      if (event.key === "ArrowUp" || event.key === "k") { event.preventDefault(); cycleWorkspace(-1); }
    }}><PanelResize label="Resize agents panel" value={navWidth} min={180} max={420} onChange={setNavWidth} side="nav" />{boot?.capabilities.roots ? <><div className="aside-heading"><button className={`nav-tab ${navTab === "active" ? "active" : ""}`} onClick={() => setNavTab("active")}>ACTIVE</button><button className={`nav-tab ${navTab === "history" ? "active" : ""}`} onClick={() => setNavTab("history")}>HISTORY</button></div>{navTab === "active" && <><nav>{activeRoots.map((root) => {
                  const label = rootTitle(root, sessions);
                  const activity = relativeActivity(root.activeAt);
                  const needsYou = rootNeedsYou(root);
                  return <button key={root.id} type="button" className={root.id === selectedID && selectedIsLive ? "session active" : "session"} onClick={() => void selectWorkspace(root.id, true)} title={root.id} aria-label={`${label}${needsYou ? ", needs attention" : ""}`}>
                    <span className={needsYou ? "root-attention" : root.busy ? "root-busy" : "root-idle"} aria-hidden />
                    <span className="session-main"><span className="session-title">{label}</span><span className="session-meta">{root.agent || "—"}{activity ? ` · ${activity}` : ""}{root.hasRecentEvent && !root.busy && !needsYou ? " · recent" : ""}</span></span>
                    <span className="session-flags">{root.id === activeRootID && <small>ACTIVE</small>}{needsYou ? <small className="needs-you">NEEDS YOU</small> : <small>{root.busy ? "BUSY" : "IDLE"}</small>}</span>
                  </button>;
                })}</nav>{!boot?.attachOnly && <div className="session-actions"><button type="button" onClick={() => void handleCreateWorkspace()}>+ New workspace</button><button type="button" disabled={!selectedIsLive || !selectedID} onClick={() => void handleCloseWorkspace()}>Close workspace</button></div>}</>}{navTab === "history" && <HistoryNav sessions={sessions} activeRoots={activeRoots} selectedID={selectedID} selectedIsLive={selectedIsLive} historySearch={historySearch} setHistorySearch={setHistorySearch} selectWorkspace={selectWorkspace} handleResume={handleResume} boot={boot} sessionAction={sessionAction} />}</> : <><div className="aside-heading"><span>SESSIONS</span></div><nav>{sessions.map((session) => {
                  const live = session.id === liveID && !boot?.attachOnly;
                  const age = relativeActivity(session.mtime);
                  return <button key={session.id} type="button" className={session.id === selectedID ? "session active history-row" : "session history-row"} onClick={() => { dispatch({ type: "client.ensure", id: session.id }); setSelectedIsLive(live); }} title={session.id}>
                    <span className="session-main"><span className="session-title">{session.title || shortID(session.id)}</span><span className="session-meta">{[shortID(session.id), age].filter(Boolean).join(" · ")}</span></span>
                    <span className="session-flags">{live && <small className="live-badge">LIVE</small>}</span>
                  </button>;
                })}</nav>{boot?.capabilities.sessions && selectedID && <div className="session-actions" aria-label="Session actions"><SessionMenu onAction={(action) => void sessionAction(action)} /></div>}</>}<ChildAgentsPanel children={children} selectedId={selectedChildId} onSelect={setSelectedChildId} onOpenTranscript={openChildTranscript} /><details className="workspace-meta"><summary>Workspace</summary><span>ROOT</span><code>{state.status.cwd || "unavailable"}</code><span>BUILD</span><code>{boot?.version || "…"}</code></details></aside>
    <main>
      <div className="runtime-stack">
      <section className="runtime" aria-label="Runtime controls">
        <Field label="Provider" value={state.status.provider} values={providers.length ? providers : state.status.provider ? [state.status.provider] : []} disabled={runtimeBusy || !boot?.capabilities.auth} onChange={(name) => void selectProvider(name)} />
        <Field label="Model" value={state.status.model} values={models.length ? models : state.status.model ? [state.status.model] : []} disabled={runtimeBusy || !boot?.capabilities.catalog} onChange={(model) => void op("select.model", { provider: state.status.provider, model }, selectedID)} />
        <Field label="Agent" value={state.status.agent} values={(boot?.agents || []).map((agent) => agent.name)} disabled={runtimeBusy} onChange={(name) => void op("select.agent", { name }, selectedID)} />
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
              {boot?.capabilities.sandbox && <Field label="Sandbox" value={state.status.sandbox || sandboxInfo?.mode} values={sandboxInfo?.modes || runtimeValues.sandbox} disabled={!isLive || state.status.busy || !sandboxInfo?.canChangeDefault} onChange={(mode) => void changeSandboxDefault(mode)} />}{boot?.capabilities.sandbox && <button type="button" className="runtime-explain" onClick={() => void openSandboxExplain()}>Explain</button>}<label className="fast-toggle"><input type="checkbox" checked={fast} disabled={runtimeBusy} onChange={(event) => { setFast(event.target.checked); void op("set.fast", { enabled: event.target.checked }, selectedID); }} />FAST</label><label className="fast-toggle"><input type="checkbox" aria-label="Show thinking" checked={showThinking} onChange={(event) => { const next = event.target.checked; setShowThinking(next); writeShowThinking(next); }} />THINK</label>
            </div>
          )}
        </div>
      </section>
      <RuntimeStatus status={state.status} modelRates={modelRates} />
      </div>
      <div className="sr-only" aria-live="polite" aria-atomic="true">{liveStatusAnn}</div>
      <section ref={transcriptRef} className="transcript" aria-label="Conversation transcript">{!boot && transport !== "connecting" ? <div className="empty-state" role="alert"><span>ERROR</span><h1>{transport}</h1><p>Failed to load cockpit. Open the URL printed by <code>strike serve</code> (one-time <code>?token=</code> handoff sets a cookie), or send <code>Authorization: Bearer</code>.</p></div> : !state.items.length && <div className="empty-state"><span>01 / READY</span><h1>{boot?.attachOnly ? "Inspect the record." : "Direct the work."}</h1><p>{boot?.attachOnly ? "Select a durable session from the rail." : "Describe an outcome. Strike will plan, act, and report through the live engine seam."}</p></div>}<TranscriptList items={state.items} showThinking={showThinking} onFileRef={openFileRef} /><div ref={endRef} />{showJumpLatest && <button type="button" className="jump-latest" onClick={jumpToLatest}>New activity — jump to latest</button>}</section>
{onboardingHint && (
        <div className="onboard-hint" role="status">
          <p>Tip: <kbd>⌘K</kbd> command palette · <code>/</code> slash commands · <code>@</code> mention files</p>
          <button type="button" onClick={() => { try { localStorage.setItem("strike.web.onboard.v1", "1"); } catch { /* */ } setOnboardingHint(false); }}>Dismiss</button>
        </div>
      )}
      <form className="composer" onSubmit={submit}><label htmlFor="prompt">Instruction {state.status.busy && "— send to queue"}</label><textarea ref={promptRef} aria-label="Instruction" id="prompt" value={draft} disabled={!isLive} placeholder={isLive ? "Describe the next outcome…  / command · @file" : "Historical session — read only"} onPaste={(event) => void attach(event.clipboardData.files)} onDrop={(event) => { event.preventDefault(); void attach(event.dataTransfer.files); }} onDragOver={(event) => event.preventDefault()} onChange={(event) => onComposerChange(event.target.value)} onKeyDown={onComposerKeyDown} />{completions.length > 0 && <div className="completion" role="listbox" aria-label="Composer completions">{completions.slice(0, 8).map((item) => <button type="button" role="option" key={item.label} onClick={() => selectCompletion(item)}><strong>{item.label}</strong><span>{item.detail}</span></button>)}</div>}{images.length > 0 && <div className="attachments">{images.map((image, index) => <button type="button" key={`${image.name}-${index}`} onClick={() => setImages((list) => list.filter((_, i) => i !== index))}>{image.name} ×</button>)}</div>}{queue.length > 0 && <div className="prompt-queue-wrap"><ol ref={queueRef} className="prompt-queue" aria-label="Queued prompts">{queue.map((item, index) => <li key={index}>{queueEdit?.index === index ? <input className="queue-edit" aria-label={`Queued prompt text ${index + 1}`} value={queueEdit.text} autoFocus onChange={(event) => setQueueEdit({ index, text: event.target.value })} onKeyDown={(event) => { if (event.key === "Enter") { event.preventDefault(); queueEditCancel.current = false; setQueue((list) => editQueuedText(list, index, queueEdit.text)); setQueueEdit(null); } if (event.key === "Escape") { event.preventDefault(); queueEditCancel.current = true; setQueueEdit(null); } }} onBlur={() => { if (!queueEditCancel.current) setQueue((list) => editQueuedText(list, index, queueEdit.text)); queueEditCancel.current = false; setQueueEdit(null); }} /> : <span>{item.text}{item.images.length > 0 ? ` (${item.images.length} img)` : ""}</span>}<span className="queue-actions"><button type="button" aria-label={`Move queued prompt ${index + 1} up`} disabled={index === 0} onClick={() => setQueue((list) => moveQueuedAt(list, index, -1))}>↑</button><button type="button" aria-label={`Move queued prompt ${index + 1} down`} disabled={index === queue.length - 1} onClick={() => setQueue((list) => moveQueuedAt(list, index, 1))}>↓</button><button type="button" aria-label={`Edit queued prompt ${index + 1}`} onClick={() => { queueEditCancel.current = false; setQueueEdit({ index, text: item.text }); }}>✎</button><button type="button" aria-label={`Remove queued prompt ${index + 1}`} onClick={() => { setQueue((list) => removeQueuedAt(list, index)); setQueueEdit((cur) => cur?.index === index ? null : cur); }}>×</button></span></li>)}</ol><div className="queue-toolbar"><button type="button" onClick={() => { setQueue(clearQueue()); setQueueEdit(null); }}>Clear queue</button></div></div>}<div><span><kbd>↵</kbd> send · <kbd>⇧↵</kbd> newline · <kbd>↑</kbd> history</span><span><input ref={fileRef} hidden type="file" accept="image/*" multiple onChange={(event) => void attach(event.target.files)} /><button type="button" onClick={() => fileRef.current?.click()}>Attach</button>{history.length > 0 && <button type="button" onClick={() => setDraft(history.at(-1) || "")}>History</button>}<button type="button" onClick={() => exportSession()} disabled={!state.items.length}>Export</button>{state.status.busy && <button type="button" className="stop" onClick={() => void op("interrupt")}>Interrupt</button>}<button type="submit" disabled={!draft.trim() || !isLive}>{state.status.busy ? "Queue" : "Send"}</button></span></div></form>
      <CommandPalette
        open={paletteOpen}
        commands={catalog}
        onClose={() => {
          setPaletteOpen(false);
          const prev = paletteInvoker.current;
          if (prev && document.contains(prev)) {
            try { prev.focus(); } catch { /* */ }
          }
        }}
        onRun={runCatalogCommand}
      />
    </main>
    <aside className={`inspector ${inspectorOpen ? "open" : "collapsed"}`} aria-label="Inspector">
      <PanelResize label="Resize inspector panel" value={inspectorWidth} min={240} max={520} onChange={setInspectorWidth} side="inspector" />
      <Tabs
        className="inspector-tabs"
        label={`${MODE_PRESETS[workspaceMode].label} surfaces`}
        value={activeInspector}
        items={inspectorTabDefs.map((tab) => ({ id: tab.id, label: tab.label }))}
        onChange={(id) => {
          setSurfaceUnavailable(undefined);
          writeDeepLinkToLocation({ mode: workspaceMode, surface: id, entity: surfaceEntity });
          void inspectProject(id);
        }}
        panelIdPrefix="inspector-panel"
      />
      <div className="inspector-body" id={activeInspector ? `inspector-panel-${activeInspector}` : undefined} role="tabpanel">
        {inspectorTabs.length ? (
          <InspectorBody
            tab={activeInspector}
            boot={boot}
            workspace={state}
            data={projectData}
            loading={projectLoading}
            expandedDiffs={expandedDiffs}
            toggleDiff={toggleDiff}
            isLive={isLive}
            selectedID={selectedID}
            onRefresh={() => void inspectProject(activeInspector)}
            sandbox={sandboxInfo}
            onExplainSandbox={() => void openSandboxExplain()}
            unavailable={surfaceUnavailable?.id === activeInspector ? surfaceUnavailable : undefined}
            childrenEntries={children}
            selectedChildId={selectedChildId}
            onSelectChild={setSelectedChildId}
            onOpenChildTranscript={openChildTranscript}
            entity={surfaceEntity}
          />
        ) : (
          <p className="muted">No surfaces available for {MODE_PRESETS[workspaceMode].label} on this host.</p>
        )}
      </div>
    </aside>
    {settingsOpen && <SettingsDialog boot={boot} status={state.status} providers={providers} rootID={selectedID} isLive={isLive} onClose={() => setSettingsOpen(false)} />}
    {sandboxExplainOpen && sandboxInfo && <SandboxExplainDialog info={sandboxInfo} status={state.status} onClose={() => setSandboxExplainOpen(false)} />}
{undoDialog && <UndoPreviewDialog preview={lastUndoPreview} preferFiles={undoDialog.preferFiles} onCancel={() => setUndoDialog(null)} onConfirm={confirmUndo} />}
    {state.permission && <PermissionDialog permission={state.permission} rootID={selectedID} canExplain={Boolean(boot?.capabilities.permissions)} />}
    {state.question && <QuestionDialog question={state.question} rootID={selectedID} />}
    {phoneModes && renderModeButtons("mode-bottom-bar")}
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
  const filtered = sessions.filter((s) => !historySearch || s.title?.toLowerCase().includes(historySearch.toLowerCase()) || s.id.toLowerCase().includes(historySearch.toLowerCase()));
  return <><input className="history-search" type="search" placeholder="Search sessions…" aria-label="Search sessions" value={historySearch} onChange={(event) => setHistorySearch(event.target.value)} /><nav aria-label="Session history">{filtered.map((session) => {
    const isActiveWorkspace = activeRoots.some((r) => r.id === session.id);
    const age = relativeActivity(session.mtime);
    const forkHint = session.forkedFrom ? `fork of ${shortID(session.forkedFrom)}` : "";
    const meta = [shortID(session.id), age, forkHint].filter(Boolean).join(" · ");
    return <button key={session.id} type="button" className={session.id === selectedID ? "session active history-row" : "session history-row"} onClick={() => void selectWorkspace(session.id, isActiveWorkspace)} title={session.id}>
      <span className="session-main"><span className="session-title">{session.title || shortID(session.id)}</span><span className="session-meta">{meta}</span></span>
      <span className="session-flags">{isActiveWorkspace && <small className="live-badge">LIVE</small>}</span>
    </button>;
  })}</nav><div className="session-actions">{!selectedIsLive && !boot?.attachOnly && <button type="button" disabled={!hasSelection || !boot?.capabilities.roots} onClick={() => void handleResume(selectedID)}>Resume as workspace</button>}{canSessions && hasSelection && <SessionMenu onAction={(action) => void sessionAction(action)} />}</div></>;
}

function RuntimeStatus({ status, modelRates }: { status: Status; modelRates?: { inputPerM: number; outputPerM: number; hasCost: boolean; context?: number } }) {
  const contextStatus = status.contextLimit === undefined && modelRates?.context
    ? { ...status, contextLimit: modelRates.context }
    : status;
  const bits: string[] = [];
  if (status.agent) bits.push(`Agent ${status.agent}`);
  if (status.effort) bits.push(`Effort ${status.effort}`);
  if (status.autonomy) bits.push(`Autonomy ${status.autonomy}`);
  if (status.permissionMode) bits.push(`Permission ${status.permissionMode}`);
  if (status.phase) bits.push(`Phase ${status.phase}`);
  if (status.workflow) bits.push(`Workflow ${status.workflow}`);
  if (status.cwd) bits.push(`CWD ${status.cwd}`);
  const ctx = formatContextLabel(contextStatus);
  if (ctx !== "not reported") bits.push(`Context ${ctx}`);
  const cost = formatCostLabel(status, modelRates);
  if (cost !== "not reported") bits.push(`Cost ${cost}`);
  if (!bits.length) return null;
  return <div className="runtime-status" aria-label="Session status">{bits.map((bit) => <span key={bit}>{bit}</span>)}</div>;
}

function formatTok(tc?: TokenCount): string {
  if (!tc?.known) return "—";
  return `~${(tc.n ?? 0).toLocaleString()}`;
}

function contextPair(status: Status): string {
  if (status.contextUsed !== undefined && status.contextLimit !== undefined) {
    return `${status.contextUsed.toLocaleString()} / ${status.contextLimit.toLocaleString()}`;
  }
  if (status.contextUsed !== undefined) return `${status.contextUsed.toLocaleString()} / —`;
  if (status.contextLimit !== undefined) return `— / ${status.contextLimit.toLocaleString()}`;
  return "not reported";
}

function ContextDoctor({ workspace, isLive, selectedID }: { workspace: WorkspaceState; isLive: boolean; selectedID: string }) {
  const {
    status, attribution, layers, pinnedKinds, excludedKinds, shedKinds,
    fitWarning, promptScope, systemChars, messageCount,
  } = workspace;
  const applyControls = (nextPin: string[], nextExcl: string[]) => {
    if (!isLive) return;
    void op("context.controls", {
      pinKinds: nextPin, setPin: true,
      excludeKinds: nextExcl, setExclude: true,
    }, selectedID);
  };
  const pin = (kind: string) => applyControls(setAdd(pinnedKinds, kind), setRemove(excludedKinds, kind));
  const unpin = (kind: string) => applyControls(setRemove(pinnedKinds, kind), excludedKinds);
  const exclude = (kind: string) => applyControls(setRemove(pinnedKinds, kind), setAdd(excludedKinds, kind));
  const include = (kind: string) => applyControls(pinnedKinds, setRemove(excludedKinds, kind));
  const clearControls = () => applyControls([], []);
  const refresh = () => { if (isLive) void op("inspect.prompt", undefined, selectedID); };

  const controlKinds = Array.from(new Set([
    ...LAYER_KINDS,
    ...pinnedKinds,
    ...excludedKinds,
    ...layers.map((l) => l.kind),
  ])).filter(Boolean);

  return <>
    <div className="context-doctor-head">
      <h2>Context doctor</h2>
      <div className="context-doctor-actions">
        <button type="button" disabled={!isLive} onClick={refresh}>Refresh</button>
        <button type="button" disabled={!isLive || (!pinnedKinds.length && !excludedKinds.length)} onClick={clearControls}>Clear pin/exclude</button>
      </div>
    </div>
    {fitWarning && <FitWarningBanner warning={fitWarning} />}
    <dl>
      <dt>Provider</dt><dd>{status.provider || "unknown"}</dd>
      <dt>Model</dt><dd>{status.model || "unknown"}</dd>
      <dt>Phase</dt><dd>{status.phase || "idle"}</dd>
      <dt>Workflow</dt><dd>{status.workflow || "none"}</dd>
      <dt>Context</dt><dd>{contextPair(status)}</dd>
      {status.usageSource && <><dt>Usage source</dt><dd>{status.usageSource}</dd></>}
      {promptScope && <><dt>Prompt scope</dt><dd>{promptScope === "last" ? "last request" : "current composition"}</dd></>}
      {systemChars !== undefined && <><dt>System chars</dt><dd>{systemChars.toLocaleString()}</dd></>}
      {messageCount !== undefined && <><dt>History</dt><dd>{messageCount.toLocaleString()} msgs</dd></>}
    </dl>

    <AttributionTable attribution={attribution} />

    {(pinnedKinds.length > 0 || excludedKinds.length > 0 || shedKinds.length > 0) && (
      <section className="context-sets" aria-label="Context control sets">
        <h3>Controls</h3>
        {pinnedKinds.length > 0 && <p><strong>Pinned</strong> {pinnedKinds.join(", ")}</p>}
        {excludedKinds.length > 0 && <p><strong>Excluded</strong> {excludedKinds.join(", ")}</p>}
        {shedKinds.length > 0 && <p><strong>Shed</strong> {shedKinds.join(", ")}</p>}
      </section>
    )}

    <section className="context-layers" aria-label="Prompt layers">
      <h3>Layers</h3>
      {layers.length === 0 ? (
        <p className="muted">{isLive ? "No layer breakdown yet. Refresh to inspect the effective prompt." : "No layer breakdown in this session log."}</p>
      ) : (
        <table className="context-table">
          <thead><tr><th>Kind</th><th>~tok</th><th>Chars</th><th>Source</th><th>Actions</th></tr></thead>
          <tbody>
            {layers.map((layer) => {
              const isPinned = layer.pinned || pinnedKinds.includes(layer.kind);
              const isExcluded = excludedKinds.includes(layer.kind);
              const est = layer.estTokens ?? (layer.chars ? Math.ceil(layer.chars / 4) : undefined);
              return <tr key={`${layer.kind}:${layer.source || ""}`}>
                <td><code>{layer.kind}</code>{isPinned ? " · pin" : ""}{isExcluded ? " · excl" : ""}</td>
                <td>{est !== undefined ? `~${est.toLocaleString()}` : "—"}</td>
                <td>{layer.chars !== undefined ? layer.chars.toLocaleString() : "—"}</td>
                <td className="muted">{layer.source || "—"}</td>
                <td className="context-layer-actions">
                  {isPinned
                    ? <button type="button" disabled={!isLive} onClick={() => unpin(layer.kind)}>Unpin</button>
                    : <button type="button" disabled={!isLive || isExcluded} onClick={() => pin(layer.kind)}>Pin</button>}
                  {isExcluded
                    ? <button type="button" disabled={!isLive} onClick={() => include(layer.kind)}>Include</button>
                    : <button type="button" disabled={!isLive} onClick={() => exclude(layer.kind)}>Exclude</button>}
                </td>
              </tr>;
            })}
          </tbody>
        </table>
      )}
    </section>

    {isLive && (
      <section className="context-kind-picker" aria-label="Pin or exclude layer kinds">
        <h3>Layer kinds</h3>
        <p className="muted">Pin retains a kind under fit pressure; exclude omits it from composition.</p>
        <div className="context-kind-list">
          {controlKinds.map((kind) => {
            const isPinned = pinnedKinds.includes(kind);
            const isExcluded = excludedKinds.includes(kind);
            return <div key={kind} className="context-kind-row">
              <code>{kind}</code>
              <span>
                {isPinned
                  ? <button type="button" onClick={() => unpin(kind)}>Unpin</button>
                  : <button type="button" disabled={isExcluded} onClick={() => pin(kind)}>Pin</button>}
                {isExcluded
                  ? <button type="button" onClick={() => include(kind)}>Include</button>
                  : <button type="button" onClick={() => exclude(kind)}>Exclude</button>}
              </span>
            </div>;
          })}
        </div>
      </section>
    )}
  </>;
}

function FitWarningBanner({ warning }: { warning: FitWarning }) {
  const tone = warning.level === "critical" ? "critical" : "warn";
  return <div className={`context-fit-warning ${tone}`} role="alert" aria-label="Context fit warning">
    <strong>{warning.level === "critical" ? "Critical fit" : "Fit warning"}</strong>
    <p>{warning.message}</p>
    {(warning.estimatedTokens !== undefined || warning.contextLimit !== undefined) && (
      <small>
        {warning.estimatedTokens !== undefined ? `~${warning.estimatedTokens.toLocaleString()} tok` : "—"}
        {" / "}
        {warning.contextLimit !== undefined ? warning.contextLimit.toLocaleString() : "—"}
        {warning.source ? ` (${warning.source})` : ""}
      </small>
    )}
  </div>;
}

function AttributionTable({ attribution }: { attribution?: RequestAttribution }) {
  if (!attribution) return null;
  const rows: Array<[string, TokenCount | undefined]> = [
    ["system", attribution.system],
    ["tools", attribution.tools],
    ["messages", attribution.messages],
    ["tool_results", attribution.toolResults],
    ["total", attribution.total],
  ];
  return <section className="context-attribution" aria-label="Token breakdown by source">
    <h3>Tokens by source</h3>
    <table className="context-table">
      <thead><tr><th>Source</th><th>~Tokens</th></tr></thead>
      <tbody>
        {rows.map(([label, tc]) => <tr key={label}><td>{label}</td><td>{formatTok(tc)}</td></tr>)}
      </tbody>
    </table>
    <p className="muted">Local ~4 chars/token estimates{attribution.source ? ` · source ${attribution.source}` : ""}. Not provider-measured billing.</p>
  </section>;
}

function InspectorBody({ tab, boot, workspace, data, loading, expandedDiffs, toggleDiff, isLive, selectedID, onRefresh, sandbox, onExplainSandbox, unavailable, childrenEntries, selectedChildId, onSelectChild, onOpenChildTranscript, entity }: {
  tab: string;
  boot?: Bootstrap;
  workspace: WorkspaceState;
  data: unknown;
  loading: boolean;
  expandedDiffs: Set<string>;
  toggleDiff: (path: string) => void;
  isLive: boolean;
  selectedID: string;
  onRefresh?: () => void;
  sandbox?: SandboxInfo;
  onExplainSandbox?: () => void;
  unavailable?: { id: string; reason: string };
  childrenEntries?: import("./ChildAgents").ChildEntry[];
  selectedChildId?: string;
  onSelectChild?: (id: string | undefined) => void;
  onOpenChildTranscript?: (id: string) => void;
  entity?: string;
}) {
  const status = workspace.status;
  if (unavailable) {
    const label = getSurface(tab)?.label || tab;
    return (
      <section className="unavailable" role="status">
        <strong>{label} unavailable</strong>
        <p>{unavailable.reason}. The surface was opened from a deep link or explicit navigation; no action was attempted.</p>
      </section>
    );
  }
  if (tab === "roster") {
    return (
      <TeamWorkspace
        team={workspace.team}
        selectedId={selectedChildId || entity}
        onSelect={onSelectChild || (() => {})}
        onOpenTranscript={onOpenChildTranscript}
        readOnly={!isLive || Boolean(boot?.attachOnly)}
        compact={typeof window !== "undefined" && window.innerWidth < 720}
        protocolOps={boot?.protocolOps}
        teamControl={boot?.capabilities?.teamControl}
        agents={(boot?.agents || []).map((a) => a.name)}
        rootSessionId={selectedID}
        sendOp={isLive && !boot?.attachOnly ? (type, data) => sendOp(type, data, selectedID) : undefined}
      />
    );
  }
  if (tab === "context") return <ContextDoctor workspace={workspace} isLive={isLive} selectedID={selectedID} />;
  if (tab === "workflows") {
    return <WorkflowsPanel
      available={Boolean(boot?.capabilities.workflows)}
      draftsAvailable={Boolean(boot?.capabilities.workflowDrafts)}
      live={isLive}
      rootID={selectedID}
      activeWorkflow={status.workflow}
      agents={(boot?.agents || []).map((a) => a.name)}
      busy={Boolean(status.busy)}
    />;
  }
  if (tab === "goals") {
    return <GoalsPanel available={Boolean(boot?.capabilities.goals)} live={isLive} />;
  }
  if (tab === "plans") {
    return <PlansPanel available={Boolean(boot?.capabilities.plans)} live={isLive} rootID={selectedID} />;
  }
  if (tab === "mcp") {
    return <MCPPanel available={Boolean(boot?.capabilities.mcp)} />;
  }
  if (tab === "plugins") {
    return (
      <PluginsPanel
        available={Boolean(boot?.capabilities.plugins)}
        live={isLive && !boot?.attachOnly}
        panesAvailable={Boolean(boot?.capabilities.panes)}
        rootID={selectedID}
      />
    );
  }
  if (tab === "panes") {
    return <PanesPanel available={Boolean(boot?.capabilities.panes)} />;
  }
  if (tab === "timeline") {
    return <TimelinePanel available={Boolean(boot?.capabilities.timeline)} sessionID={selectedID} />;
  }
  if (tab === "diagnostics") {
    return <DiagnosticsPanel available={Boolean(boot?.capabilities.lsp)} />;
  }
  if (loading) return <section className="unavailable" role="status"><strong>Loading {tab}</strong></section>;
  if (tab === "files") {
    const files = ((data as { files?: ChangedFile[] } | undefined)?.files || []);
    const err = (data as { error?: string } | undefined)?.error;
    return (
      <CodeExplorer
        available={Boolean(boot?.capabilities.files)}
        rootID={selectedID}
        entity={entity}
        readOnly={Boolean(boot?.attachOnly)}
        changedFiles={err ? [] : files}
        expandedDiffs={expandedDiffs}
        toggleDiff={toggleDiff}
        onOpenPath={(path, line) => {
          // Keep deep link in sync for shareable Code mode URLs.
          writeDeepLinkToLocation({
            mode: "code",
            surface: "files",
            entity: line ? `${path}:${line}` : path,
            root: selectedID || undefined,
          });
        }}
      />
    );
  }
  if (tab === "memory") return <MemoryPanel boot={boot} data={data} onRefresh={onRefresh || (() => {})} />;
  if (tab === "issues") return <IssuesPanel boot={boot} data={data} onRefresh={onRefresh || (() => {})} />;
  const label = getSurface(tab)?.label || tab || "surface";
  return (
    <section className="unavailable" role="status">
      <strong>{label}</strong>
      <p>This surface is registered but has no mount in this build yet.</p>
    </section>
  );
}

function FilesPanel({ boot, data, expandedDiffs, toggleDiff }: { boot?: Bootstrap; data: unknown; expandedDiffs: Set<string>; toggleDiff: (path: string) => void }) {
  if (!boot?.capabilities.files) return <CapabilityUnavailable name="Changed files" />;
  if ((data as { error?: string } | undefined)?.error) return <CapabilityError error={(data as { error: string }).error} />;
  const files = ((data as { files?: ChangedFile[] } | undefined)?.files || []);
  return <><h2>Changed files</h2>{files.length ? <div className="changed-files">{files.map((file) => <article key={file.path} className="changed-file"><button onClick={() => toggleDiff(file.path)} aria-expanded={expandedDiffs.has(file.path)}><code>{file.path}</code><span className="diff-stat"><b>+{file.added}</b><b>-{file.deleted}</b></span></button>{expandedDiffs.has(file.path) && <pre className="diff-view">{file.diff || "No textual diff available."}</pre>}</article>)}</div> : <p className="muted">No changed files reported.</p>}</>;
}

function MemoryPanel({ boot, data, onRefresh }: { boot?: Bootstrap; data: unknown; onRefresh: () => void }) {
  const canWrite = Boolean(boot?.capabilities.memory && !boot.attachOnly);
  const [key, setKey] = useState("");
  const [value, setValue] = useState("");
  const [tags, setTags] = useState("");
  const [busy, setBusy] = useState(false);
  const [editKey, setEditKey] = useState<string | null>(null);
  if (!boot?.capabilities.memory) return <CapabilityUnavailable name="Memory" />;
  if ((data as { error?: string } | undefined)?.error) return <CapabilityError error={(data as { error: string }).error} />;
  const entries = ((data as { entries?: MemoryEntry[] } | undefined)?.entries || []);
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    try { await action(); onRefresh(); } catch (error) { window.alert((error as Error).message); } finally { setBusy(false); }
  };
  const startEdit = (entry: MemoryEntry) => {
    const nextKey = entry.Key || entry.key || "";
    setEditKey(nextKey);
    setKey(nextKey);
    setValue(entry.Value || entry.value || "");
    setTags((entry.Tags || entry.tags || []).join(", "));
  };
  const save = () => {
    const nextKey = key.trim();
    if (!nextKey) return window.alert("Key is required");
    const tagList = tags.split(",").map((t) => t.trim()).filter(Boolean);
    void run(async () => {
      await putMemory(nextKey, value, tagList);
      setKey(""); setValue(""); setTags(""); setEditKey(null);
    });
  };
  return <>
    <div className="panel-heading">
      <h2>Memory</h2>
      <div className="panel-actions">
        <button type="button" disabled={busy} aria-label="Export memory" onClick={() => void run(() => exportMemory())}>Export</button>
      </div>
    </div>
    {canWrite && <form className="project-form" aria-label={editKey ? "Edit memory entry" : "Add memory entry"} onSubmit={(event) => { event.preventDefault(); save(); }}>
      <label>Key<input aria-label="Memory key" value={key} disabled={busy || Boolean(editKey)} onChange={(event) => setKey(event.target.value)} placeholder="convention" /></label>
      <label>Value<textarea aria-label="Memory value" value={value} disabled={busy} onChange={(event) => setValue(event.target.value)} placeholder="Prefer table-driven tests" rows={3} /></label>
      <label>Tags<input aria-label="Memory tags" value={tags} disabled={busy} onChange={(event) => setTags(event.target.value)} placeholder="project, style" /></label>
      <div className="panel-actions">
        {editKey && <button type="button" disabled={busy} onClick={() => { setEditKey(null); setKey(""); setValue(""); setTags(""); }}>Cancel</button>}
        <button type="submit" disabled={busy}>{editKey ? "Save" : "Add"}</button>
      </div>
    </form>}
    {!canWrite && <p className="muted">Attach-only mode is read-only. Export remains available.</p>}
    {entries.length ? <div className="project-list">{entries.map((entry) => {
      const entryKey = entry.Key || entry.key || "";
      const entryValue = entry.Value || entry.value || "";
      const entryTags = entry.Tags || entry.tags || [];
      return <article key={entryKey}>
        <h3>{entryKey}</h3>
        <p>{entryValue}</p>
        {entryTags.length > 0 && <small>{entryTags.join(", ")}</small>}
        {canWrite && <div className="panel-actions">
          <button type="button" disabled={busy} onClick={() => startEdit(entry)}>Edit</button>
          <button type="button" disabled={busy} onClick={() => { if (window.confirm(`Delete memory key “${entryKey}”?`)) void run(() => deleteMemory(entryKey)); }}>Delete</button>
        </div>}
      </article>;
    })}</div> : <p className="muted">No project memory entries.</p>}
  </>;
}

function IssuesPanel({ boot, data, onRefresh }: { boot?: Bootstrap; data: unknown; onRefresh: () => void }) {
  const canWrite = Boolean(boot?.capabilities.issues && !boot.attachOnly);
  const [title, setTitle] = useState("");
  const [body, setBody] = useState("");
  const [busy, setBusy] = useState(false);
  if (!boot?.capabilities.issues) return <CapabilityUnavailable name="Issues" />;
  if ((data as { error?: string } | undefined)?.error) return <CapabilityError error={(data as { error: string }).error} />;
  const issues = ((data as { issues?: IssueEntry[] } | undefined)?.issues || []);
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    try { await action(); onRefresh(); } catch (error) { window.alert((error as Error).message); } finally { setBusy(false); }
  };
  return <>
    <div className="panel-heading">
      <h2>Issues</h2>
      <div className="panel-actions">
        <button type="button" disabled={busy} aria-label="Export issues" onClick={() => void run(() => exportIssues())}>Export</button>
      </div>
    </div>
    {canWrite && <form className="project-form" aria-label="Add issue" onSubmit={(event) => {
      event.preventDefault();
      const nextTitle = title.trim();
      if (!nextTitle) return window.alert("Title is required");
      void run(async () => { await createIssue(nextTitle, body); setTitle(""); setBody(""); });
    }}>
      <label>Title<input aria-label="Issue title" value={title} disabled={busy} onChange={(event) => setTitle(event.target.value)} placeholder="Fix inspector resize" /></label>
      <label>Body<textarea aria-label="Issue body" value={body} disabled={busy} onChange={(event) => setBody(event.target.value)} placeholder="Optional details" rows={3} /></label>
      <div className="panel-actions"><button type="submit" disabled={busy}>Add</button></div>
    </form>}
    {!canWrite && <p className="muted">Attach-only mode is read-only. Export remains available.</p>}
    {issues.length ? <div className="project-list">{issues.map((issue) => {
      const id = issue.ID ?? issue.id ?? 0;
      const issueTitle = issue.Title || issue.title || "Untitled issue";
      const issueBody = issue.Body || issue.body || "";
      const status = issue.Status || issue.status || "open";
      return <article key={id}>
        <h3>#{id} {issueTitle}</h3>
        <small>{status}</small>
        {issueBody && <p>{issueBody}</p>}
        {canWrite && status === "open" && <div className="panel-actions">
          <button type="button" disabled={busy} onClick={() => void run(() => closeIssue(id))}>Close</button>
        </div>}
      </article>;
    })}</div> : <p className="muted">No project issues.</p>}
  </>;
}

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
