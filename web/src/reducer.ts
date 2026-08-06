import type { ClientState, Envelope, TranscriptItem, WorkspaceComposer, WorkspaceSlice, WorkspaceState } from "./types";

export const initialState = (): WorkspaceState => ({
  items: [], seen: new Set(), status: {}, children: {}, changedFiles: [],
});

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

const fingerprint = (env: Envelope) => JSON.stringify([env.type, env.time, env.data]);
const text = (data: Record<string, unknown> | undefined, key: string) => String(data?.[key] ?? "");
const correlation = (d?: Record<string, unknown>) => String(d?.turnId ?? d?.sessionId ?? "root");

function append(items: TranscriptItem[], item: TranscriptItem, merge = false) {
  if (merge) {
    const last = items.at(-1);
    if (last?.kind === item.kind && last.id === item.id) return [...items.slice(0, -1), { ...last, text: last.text + item.text }];
  }
  return [...items, item];
}

function asWorkspaceState(slice: WorkspaceSlice): WorkspaceState {
  return {
    items: slice.items,
    seen: slice.seen,
    status: slice.status,
    permission: slice.permission,
    question: slice.question,
    children: slice.children,
    changedFiles: slice.changedFiles,
  };
}

export function reduceEvent(state: WorkspaceState, env: Envelope): WorkspaceState {
  if (env.type === "workspace.reset") return { ...initialState(), status: { sessionId: String(env.data?.sessionId || "") } };
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
  const id = correlation(d);
  switch (env.type) {
    case "status": status = d; break;
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
    case "turn.started": status = { ...status, busy: true }; break;
    case "turn.completed": status = { ...status, busy: false }; break;
    case "model.selected": status = { ...status, provider: text(d, "provider"), model: text(d, "model") }; break;
    case "agent.selected": status = { ...status, agent: text(d, "name") }; break;
    case "effort.selected": status = { ...status, effort: text(d, "level") }; break;
    case "autonomy.selected": status = { ...status, autonomy: text(d, "mode") }; break;
    case "permission.mode": status = { ...status, permissionMode: text(d, "mode") }; break;
    case "phase.changed": status = { ...status, phase: text(d, "phase"), workflow: text(d, "workflow") }; break;
    case "files.invalidated": changedFiles = Array.from(new Set([...changedFiles, ...((d.paths as string[]) || [])])); break;
    case "child.started": children[String(d.sessionId)] = { agent: text(d, "agent"), status: "running" }; break;
    case "child.completed": children[String(d.sessionId)] = { ...children[String(d.sessionId)], status: text(d, "status"), summary: text(d, "summary") }; break;
    case "engine.error": items = append(items, { id: `error:${items.length}`, kind: "error", text: text(d, "message") }); break;
    case "provider.retrying": items = append(items, { id: `retry:${items.length}`, kind: "system", title: "Retrying provider", text: text(d, "error") }); break;
  }
  return { ...state, seen, items, status, permission, question, children, changedFiles };
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
      // Preserve in-progress composer when reconnecting the same root.
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
