import type {
  ChildAgent, ClientState, Envelope, FitWarning, PromptLayer, RequestAttribution,
  Status, TokenCount, TranscriptItem, WorkspaceComposer, WorkspaceSlice, WorkspaceState,
} from "./types";
import { isRootLineage, parseUndoPreview, type UndoPreview } from "./undoPreview";

export const initialState = (): WorkspaceState => ({
  items: [], seen: new Set(), status: {}, children: {}, changedFiles: [], undoStack: [],
  layers: [], pinnedKinds: [], excludedKinds: [], shedKinds: [],
});

const fingerprint = (env: Envelope) => JSON.stringify([env.type, env.time, env.data]);
const text = (data: Record<string, unknown> | undefined, key: string) => String(data?.[key] ?? "");
const correlation = (d?: Record<string, unknown>) => String(d?.turnId ?? d?.sessionId ?? "root");


/** Parse protocol.TokenCount wire shape ({n, known}) or a bare number. */
export function tokenCount(value: unknown): { n: number; known: boolean } {
  if (typeof value === "number" && Number.isFinite(value)) return { n: value, known: true };
  if (value && typeof value === "object") {
    const obj = value as { n?: unknown; known?: unknown };
    if (obj.known === true && typeof obj.n === "number" && Number.isFinite(obj.n)) return { n: obj.n, known: true };
  }
  return { n: 0, known: false };
}

function addKnown(current: number | undefined, part: { n: number; known: boolean }): number | undefined {
  if (!part.known) return current;
  return (current ?? 0) + part.n;
}

function usageSourceLabel(prev: string | undefined, source: string): string {
  const next = source.trim();
  if (!next) return prev || "";
  if (!prev || prev === next) return next;
  if (prev === "mixed (actual + estimated)") return prev;
  if ((prev === "actual" && next === "estimated") || (prev === "estimated" && next === "actual")) {
    return "mixed (actual + estimated)";
  }
  return next;
}

/** Accumulate one usage.reported payload into status totals (never invent zeros). */
export function applyUsageReported(status: Status, data: Record<string, unknown>): Status {
  const input = tokenCount(data.input);
  const output = tokenCount(data.output);
  const cacheRead = tokenCount(data.cacheRead);
  const cacheCreation = tokenCount(data.cacheCreation);
  const used = tokenCount(data.used);
  const next: Status = {
    ...status,
    usageReports: (status.usageReports ?? 0) + 1,
    inputTokens: addKnown(status.inputTokens, input),
    outputTokens: addKnown(status.outputTokens, output),
    cacheReadTokens: addKnown(status.cacheReadTokens, cacheRead),
    cacheCreationTokens: addKnown(status.cacheCreationTokens, cacheCreation),
    usageSource: usageSourceLabel(status.usageSource, text(data, "source")) || status.usageSource,
  };
  if (used.known) next.contextUsed = used.n;
  return next;
}


function append(items: TranscriptItem[], item: TranscriptItem, merge = false) {
  if (merge) {
    const last = items.at(-1);
    if (last?.kind === item.kind && last.id === item.id) return [...items.slice(0, -1), { ...last, text: last.text + item.text }];
  }
  return [...items, item];
}

function pushUndo(stack: UndoPreview[], preview: UndoPreview): UndoPreview[] {
  return [...stack, preview];
}

function popUndo(stack: UndoPreview[]): UndoPreview[] {
  if (!stack.length) return stack;
  return stack.slice(0, -1);
}

function asTokenCount(raw: unknown): TokenCount | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const o = raw as Record<string, unknown>;
  const known = Boolean(o.known);
  const n = typeof o.n === "number" ? o.n : 0;
  return { n, known };
}

function asStringList(raw: unknown): string[] {
  if (!Array.isArray(raw)) return [];
  return raw.map((v) => String(v)).filter(Boolean);
}

function asLayers(raw: unknown): PromptLayer[] {
  if (!Array.isArray(raw)) return [];
  return raw.map((item) => {
    const o = (item && typeof item === "object" ? item : {}) as Record<string, unknown>;
    return {
      kind: String(o.kind || ""),
      source: o.source !== undefined ? String(o.source) : undefined,
      mode: o.mode !== undefined ? String(o.mode) : undefined,
      chars: typeof o.chars === "number" ? o.chars : undefined,
      estTokens: typeof o.estTokens === "number" ? o.estTokens : undefined,
      pinned: Boolean(o.pinned),
      preview: o.preview !== undefined ? String(o.preview) : undefined,
    };
  }).filter((layer) => layer.kind);
}

function asAttribution(raw: unknown): RequestAttribution | undefined {
  if (!raw || typeof raw !== "object") return undefined;
  const o = raw as Record<string, unknown>;
  const attr: RequestAttribution = {
    system: asTokenCount(o.system),
    tools: asTokenCount(o.tools),
    messages: asTokenCount(o.messages),
    toolResults: asTokenCount(o.toolResults),
    total: asTokenCount(o.total),
    source: o.source !== undefined ? String(o.source) : undefined,
  };
  const has =
    attr.source ||
    attr.system?.known || attr.tools?.known || attr.messages?.known ||
    attr.toolResults?.known || attr.total?.known;
  return has ? attr : undefined;
}

function knownN(tc: unknown): number | undefined {
  const t = asTokenCount(tc);
  return t?.known ? t.n : undefined;
}

/** Toggle helpers for pin/exclude sets (immutable). */
export function setAdd(list: string[], value: string): string[] {
  return list.includes(value) ? list : [...list, value];
}
export function setRemove(list: string[], value: string): string[] {
  return list.filter((v) => v !== value);
}


const optionalText = (data: Record<string, unknown> | undefined, key: string) => {
  const value = String(data?.[key] ?? "").trim();
  return value || undefined;
};
const asRecord = (value: unknown): Record<string, unknown> | undefined =>
  value && typeof value === "object" && !Array.isArray(value) ? value as Record<string, unknown> : undefined;
const childId = (d: Record<string, unknown>) => String(d.sessionId ?? "").trim();
function mergeChild(prev: ChildAgent | undefined, next: Partial<ChildAgent> & { status?: string }): ChildAgent {
  return {
    agent: next.agent || prev?.agent,
    name: next.name || prev?.name,
    status: next.status || prev?.status || "unknown",
    summary: next.summary ?? prev?.summary,
    quality: next.quality ?? prev?.quality,
    budgetKind: next.budgetKind ?? prev?.budgetKind,
    finalization: next.finalization ?? prev?.finalization,
    prompt: next.prompt ?? prev?.prompt,
    escalateKind: next.escalateKind ?? prev?.escalateKind,
    escalateReason: next.escalateReason ?? prev?.escalateReason,
    escalateAction: next.escalateAction ?? prev?.escalateAction,
  };
}

export function reduceEvent(state: WorkspaceState, env: Envelope): WorkspaceState {
  if (env.type === "workspace.reset") return { ...initialState(), status: { sessionId: String(env.data?.sessionId || "") } };
  // Client-only notices (slash help/unknown/cost) — not part of the wire protocol.
  if (env.type === "local.system") {
    const d = env.data || {};
    return {
      ...state,
      items: append(state.items, {
        id: `system:${state.items.length}:${env.time || Date.now()}`,
        kind: "system",
        title: text(d, "title") || "Notice",
        text: text(d, "text"),
      }),
    };
  }
  const key = fingerprint(env);
  if (state.seen.has(key)) return state;
  const seen = new Set(state.seen).add(key);
  const d = env.data || {};
  let items = state.items;
  let status = state.status;
  let permission = state.permission;
  let question = state.question;
  const children = { ...state.children };
  let changedFiles = state.changedFiles;
  let undoStack = state.undoStack;
  let attribution = state.attribution;
  let layers = state.layers;
  let pinnedKinds = state.pinnedKinds;
  let excludedKinds = state.excludedKinds;
  let shedKinds = state.shedKinds;
  let fitWarning = state.fitWarning;
  let promptScope = state.promptScope;
  let systemChars = state.systemChars;
  let messageCount = state.messageCount;
  const id = correlation(d);
  switch (env.type) {
    case "status": status = { ...status, ...(d as Status) }; break;
    case "user.message": items = append(items, { id: `${id}:user:${items.length}`, kind: "user", text: text(d, "text"), data: d }); break;
    case "text.delta": items = append(items, { id: `${id}:assistant`, kind: "assistant", text: text(d, "text") }, true); break;
    case "reasoning.delta": items = append(items, { id: `${id}:reasoning`, kind: "reasoning", title: "Reasoning", text: text(d, "text") }, true); break;
    case "tool.begin": items = append(items, { id: String(d.callId), kind: "tool", title: text(d, "name"), text: JSON.stringify(d.args ?? {}, null, 2), data: d }); break;
    case "tool.output": {
      const call = String(d.callId); const index = items.findIndex((v) => v.id === call);
      if (index >= 0) items = items.map((v, i) => i === index ? { ...v, text: v.text + text(d, "data") } : v);
      break;
    }
    case "tool.end": {
      const call = String(d.callId); const index = items.findIndex((v) => v.id === call);
      const next = { id: call, kind: "tool" as const, title: text(d, "title"), text: text(d, "output"), data: d };
      items = index >= 0 ? items.map((v, i) => i === index ? next : v) : append(items, next);
      break;
    }
    case "permission.asked": permission = d; break;
    case "permission.resolved": if (!d.requestId || d.requestId === permission?.requestId) permission = undefined; break;
    case "question.asked": question = d; break;
    case "question.resolved": if (!d.requestId || d.requestId === question?.requestId) question = undefined; break;
    case "turn.started":
      // Drop stale fit banners; engine re-emits if the new turn is still tight.
      status = { ...status, busy: true };
      fitWarning = undefined;
      break;
    case "turn.completed":
      status = { ...status, busy: false };
      // Stack undo preview for /rewind path list + uncovered warn (TUI #801 / WEB.12).
      if (isRootLineage(d)) undoStack = pushUndo(undoStack, parseUndoPreview(d));
      break;
    case "session.rewound":
      if (isRootLineage(d)) undoStack = popUndo(undoStack);
      break;
    case "model.selected": status = { ...status, provider: text(d, "provider"), model: text(d, "model") }; break;
    case "agent.selected": status = { ...status, agent: text(d, "name") }; break;
    case "effort.selected": status = { ...status, effort: text(d, "level") }; break;
    case "autonomy.selected": status = { ...status, autonomy: text(d, "mode") }; break;
    case "usage.reported": status = applyUsageReported(status, d); break;
    case "permission.mode": status = { ...status, permissionMode: text(d, "mode") }; break;
    case "phase.changed": status = { ...status, phase: text(d, "phase"), workflow: text(d, "workflow") }; break;
    case "files.invalidated": changedFiles = Array.from(new Set([...changedFiles, ...((d.paths as string[]) || [])])); break;
    case "child.started": {
      const sid = childId(d);
      if (sid) {
        children[sid] = mergeChild(children[sid], {
          agent: optionalText(d, "agent"),
          name: optionalText(d, "name"),
          status: "running",
          prompt: optionalText(d, "prompt"),
        });
      }
      break;
    }
    case "child.completed": {
      const sid = childId(d);
      if (sid) {
        const handoff = asRecord(d.handoff);
        const summary = optionalText(d, "summary") || optionalText(handoff, "summary");
        children[sid] = mergeChild(children[sid], {
          name: optionalText(d, "name"),
          status: optionalText(d, "status") || "completed",
          summary,
          quality: optionalText(handoff, "quality"),
          budgetKind: optionalText(d, "budgetKind"),
          finalization: optionalText(d, "finalization"),
        });
      }
      break;
    }
    case "child.escalated": {
      const sid = childId(d);
      if (sid) {
        const action = optionalText(d, "action");
        const prev = children[sid];
        const running = !prev || prev.status === "running" || prev.status === "finalizing" || prev.status === "escalating";
        children[sid] = mergeChild(prev, {
          name: optionalText(d, "name"),
          status: running ? (action === "finalizing" ? "finalizing" : action === "interrupted" ? "interrupted" : "escalating") : prev?.status,
          escalateKind: optionalText(d, "kind"),
          escalateReason: optionalText(d, "reason"),
          escalateAction: action,
          budgetKind: optionalText(d, "kind") || prev?.budgetKind,
        });
      }
      break;
    }
    case "children.seed": {
      const list = Array.isArray(d.sessions) ? d.sessions as Array<Record<string, unknown>> : [];
      for (const row of list) {
        const sid = String(row.id ?? row.ID ?? "").trim();
        if (!sid || children[sid]) continue;
        const title = optionalText(row, "title") || optionalText(row, "Title");
        const open = Boolean(row.open ?? row.Open);
        children[sid] = { agent: title, status: open ? "running" : "unknown" };
      }
      break;
    }
    case "engine.error": items = append(items, { id: `error:${items.length}`, kind: "error", text: text(d, "message") }); break;
    case "provider.retrying": items = append(items, { id: `retry:${items.length}`, kind: "system", title: "Retrying provider", text: text(d, "error") }); break;
    case "prompt.effective": {
      layers = asLayers(d.layers);
      attribution = asAttribution(d.attribution) ?? attribution;
      pinnedKinds = asStringList(d.pinnedKinds);
      excludedKinds = asStringList(d.excludedKinds);
      shedKinds = asStringList(d.shedKinds);
      promptScope = d.fromLastStream ? "last" : "current";
      if (typeof d.systemChars === "number") systemChars = d.systemChars;
      if (typeof d.messageCount === "number") messageCount = d.messageCount;
      const total = knownN((d.attribution as Record<string, unknown> | undefined)?.total);
      if (total !== undefined) status = { ...status, contextUsed: status.contextUsed ?? total };
      break;
    }
    case "context.controls": {
      // Event confirms pin/exclude sets after SetContextControls.
      pinnedKinds = asStringList(d.pinnedKinds);
      excludedKinds = asStringList(d.excludedKinds);
      layers = layers.map((layer) => ({
        ...layer,
        pinned: pinnedKinds.includes(layer.kind),
      }));
      break;
    }
    case "context.fit_warning": {
      const level = text(d, "level") || "warn";
      const message = text(d, "message") || `context fit ${level}`;
      const fw: FitWarning = {
        level,
        message,
        estimatedTokens: typeof d.estimatedTokens === "number" ? d.estimatedTokens : undefined,
        contextLimit: typeof d.contextLimit === "number" ? d.contextLimit : undefined,
        source: d.source !== undefined ? String(d.source) : undefined,
      };
      fitWarning = fw;
      if (typeof d.contextLimit === "number" && d.contextLimit > 0) {
        status = { ...status, contextLimit: d.contextLimit };
      }
      if (typeof d.estimatedTokens === "number" && d.estimatedTokens > 0) {
        status = { ...status, contextUsed: status.contextUsed ?? d.estimatedTokens };
      }
      break;
    }
  }
  return {
    ...state, seen, items, status, permission, question, children, changedFiles, undoStack,
    attribution, layers, pinnedKinds, excludedKinds, shedKinds, fitWarning,
    promptScope, systemChars, messageCount,
  };
}

export const emptyComposer = (): WorkspaceComposer => ({
  draft: "", queue: [], images: [], fast: false,
});

export const emptySlice = (sessionId = ""): WorkspaceSlice => ({
  ...initialState(),
  status: sessionId ? { sessionId } : {},
  ...emptyComposer(),
});

export const initialClientState = (): ClientState => ({
  selectedID: "",
  byID: {},
});

function asWorkspaceState(slice: WorkspaceSlice): WorkspaceState {
  return {
    items: slice.items,
    seen: slice.seen,
    status: slice.status,
    permission: slice.permission,
    question: slice.question,
    children: slice.children,
    changedFiles: slice.changedFiles,
    undoStack: slice.undoStack,
    attribution: slice.attribution,
    layers: slice.layers,
    pinnedKinds: slice.pinnedKinds,
    excludedKinds: slice.excludedKinds,
    shedKinds: slice.shedKinds,
    fitWarning: slice.fitWarning,
    promptScope: slice.promptScope,
    systemChars: slice.systemChars,
    messageCount: slice.messageCount,
  };
}

export type ClientAction =
  | { type: "client.select"; id: string }
  | { type: "client.ensure"; id: string }
  | { type: "client.reset"; id: string }
  | { type: "client.event"; id: string; envelope: Envelope }
  | { type: "client.composer"; id: string; patch: Partial<WorkspaceComposer> }
  | { type: "client.drop"; id: string };

/** Partition engine + composer UI by workspace id. Switching never wipes peers. */
export function reduceClient(state: ClientState, action: ClientAction): ClientState {
  switch (action.type) {
    case "client.select":
      return { ...state, selectedID: action.id };
    case "client.ensure": {
      if (state.byID[action.id]) {
        return state.selectedID === action.id ? state : { ...state, selectedID: action.id };
      }
      return {
        ...state,
        selectedID: action.id,
        byID: { ...state.byID, [action.id]: emptySlice(action.id) },
      };
    }
    case "client.reset": {
      const prev = state.byID[action.id];
      const next = emptySlice(action.id);
      if (prev) {
        next.draft = prev.draft;
        next.queue = prev.queue;
        next.images = prev.images;
        next.fast = prev.fast;
      }
      return { ...state, byID: { ...state.byID, [action.id]: next } };
    }
    case "client.event": {
      const current = state.byID[action.id] || emptySlice(action.id);
      if (action.envelope.type === "workspace.reset") {
        const cleared = emptySlice(action.id);
        cleared.draft = current.draft;
        cleared.queue = current.queue;
        cleared.images = current.images;
        cleared.fast = current.fast;
        return { ...state, byID: { ...state.byID, [action.id]: cleared } };
      }
      const reduced = reduceEvent(asWorkspaceState(current), action.envelope);
      return {
        ...state,
        byID: { ...state.byID, [action.id]: { ...current, ...reduced } },
      };
    }
    case "client.composer": {
      const current = state.byID[action.id] || emptySlice(action.id);
      return {
        ...state,
        byID: { ...state.byID, [action.id]: { ...current, ...action.patch } },
      };
    }
    case "client.drop": {
      const { [action.id]: _removed, ...rest } = state.byID;
      return {
        selectedID: state.selectedID === action.id ? "" : state.selectedID,
        byID: rest,
      };
    }
  }
}

export function selectedSlice(state: ClientState): WorkspaceSlice {
  if (!state.selectedID) return emptySlice();
  return state.byID[state.selectedID] || emptySlice(state.selectedID);
}
