import type { Envelope, TranscriptItem, WorkspaceState } from "./types";
import { isRootLineage, parseUndoPreview, type UndoPreview } from "./undoPreview";

export const initialState = (): WorkspaceState => ({
  items: [], seen: new Set(), status: {}, children: {}, changedFiles: [], undoStack: [],
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

function pushUndo(stack: UndoPreview[], preview: UndoPreview): UndoPreview[] {
  return [...stack, preview];
}

function popUndo(stack: UndoPreview[]): UndoPreview[] {
  if (!stack.length) return stack;
  return stack.slice(0, -1);
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
    case "permission.mode": status = { ...status, permissionMode: text(d, "mode") }; break;
    case "phase.changed": status = { ...status, phase: text(d, "phase"), workflow: text(d, "workflow") }; break;
    case "files.invalidated": changedFiles = Array.from(new Set([...changedFiles, ...((d.paths as string[]) || [])])); break;
    case "child.started": children[String(d.sessionId)] = { agent: text(d, "agent"), status: "running" }; break;
    case "child.completed": children[String(d.sessionId)] = { ...children[String(d.sessionId)], status: text(d, "status"), summary: text(d, "summary") }; break;
    case "engine.error": items = append(items, { id: `error:${items.length}`, kind: "error", text: text(d, "message") }); break;
    case "provider.retrying": items = append(items, { id: `retry:${items.length}`, kind: "system", title: "Retrying provider", text: text(d, "error") }); break;
  }
  return { ...state, seen, items, status, permission, question, children, changedFiles, undoStack };
}
