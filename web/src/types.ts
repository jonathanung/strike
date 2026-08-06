export type Envelope = { type: string; time?: string; data?: Record<string, unknown> };
export type Status = {
  sessionId?: string; provider?: string; model?: string; agent?: string; effort?: string;
  autonomy?: string; permissionMode?: string; phase?: string; workflow?: string; cwd?: string;
  busy?: boolean; contextUsed?: number; contextLimit?: number;
  sandbox?: string; sandboxBackend?: string; sandboxAvailable?: boolean; networkAllow?: string[];
};
export type SandboxInfo = {
  mode: string; backend?: string; available: boolean; networkAllow?: string[];
  explain?: string; defaultMode?: string; permissionMode?: string; note?: string;
  modes?: string[]; canChangeDefault?: boolean;
};
export type Capabilities = Record<string, boolean>;
export type Bootstrap = {
  version: string; authRequired: boolean; attachOnly: boolean; capabilities: Capabilities;
  status?: Status; agents: { name: string; description?: string }[];
  skills: { name: string; description: string }[];
  protocolOps: string[];
};
export type Session = { id: string; title?: string; mtime?: number; parentId?: string; forkedFrom?: string; open?: boolean };
export type TranscriptItem = {
  id: string; kind: "user" | "assistant" | "reasoning" | "tool" | "system" | "error";
  title?: string; text: string; requestId?: string; data?: Record<string, unknown>;
};
export type ImageAttachment = { name: string; mime: string; data: string };
export type { TurnFileChange, UndoPreview } from "./undoPreview";

/** Child agent row derived from child.* events (+ optional children API seed). */
export type ChildAgent = {
  agent?: string;
  name?: string;
  status: string;
  summary?: string;
  /** Handoff quality: complete | partial | unavailable (#879). */
  quality?: string;
  budgetKind?: string;
  finalization?: string;
  prompt?: string;
  escalateKind?: string;
  escalateReason?: string;
  escalateAction?: string;
};

export type WorkspaceState = {
  items: TranscriptItem[]; seen: Set<string>; status: Status;
  permission?: Record<string, unknown>; question?: Record<string, unknown>;
  children: Record<string, ChildAgent>;
  changedFiles: string[];
  /** Stack of last-turn harness previews; top is current /rewind target (TUI undoStack). */
  undoStack: import("./undoPreview").UndoPreview[];
};
export type ActiveRoot = {
  id: string; title?: string; agent?: string; busy: boolean;
  activeAt?: number; createdAt?: number; hasRecentEvent?: boolean;
  permissionPending?: boolean; questionPending?: boolean;
};
export type RootsResponse = { roots: ActiveRoot[]; activeId?: string };
export type RootCreateResult = { id: string; sessionId: string };
export type RootResumeResult = { id: string; sessionId: string; resumedId: string; wasActive: boolean };

/** Per-workspace composer + runtime mirrors kept while switching roots. */
export type WorkspaceComposer = {
  draft: string;
  queue: Array<{ text: string; images: ImageAttachment[] }>;
  images: ImageAttachment[];
  fast: boolean;
};
export type WorkspaceSlice = WorkspaceState & WorkspaceComposer;
/** Client cache keyed by workspace/root (or historical session) id. */
export type ClientState = {
  selectedID: string;
  byID: Record<string, WorkspaceSlice>;
};

