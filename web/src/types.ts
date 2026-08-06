export type Envelope = { type: string; time?: string; data?: Record<string, unknown> };
export type Status = {
  sessionId?: string; provider?: string; model?: string; agent?: string; effort?: string;
  autonomy?: string; permissionMode?: string; phase?: string; workflow?: string; cwd?: string;
  busy?: boolean; contextUsed?: number; contextLimit?: number;
};
export type Capabilities = Record<string, boolean>;
export type Bootstrap = {
  version: string; authRequired: boolean; attachOnly: boolean; capabilities: Capabilities;
  status?: Status; agents: { name: string; description?: string }[];
  skills: { name: string; description: string }[];
  protocolOps: string[];
};
export type Session = { id: string; title?: string; mtime?: number; parentId?: string; open?: boolean };
export type TranscriptItem = {
  id: string; kind: "user" | "assistant" | "reasoning" | "tool" | "system" | "error";
  title?: string; text: string; requestId?: string; data?: Record<string, unknown>;
};
export type ImageAttachment = { name: string; mime: string; data: string };
export type { TurnFileChange, UndoPreview } from "./undoPreview";

export type WorkspaceState = {
  items: TranscriptItem[]; seen: Set<string>; status: Status;
  permission?: Record<string, unknown>; question?: Record<string, unknown>;
  children: Record<string, { agent?: string; status: string; summary?: string }>;
  changedFiles: string[];
  /** Stack of last-turn harness previews; top is current /rewind target (TUI undoStack). */
  undoStack: import("./undoPreview").UndoPreview[];
};
export type ActiveRoot = {
  id: string; title?: string; agent?: string; busy: boolean;
  activeAt?: number; createdAt?: number; hasRecentEvent?: boolean;
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
