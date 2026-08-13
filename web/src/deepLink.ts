/**
 * Additive cockpit deep-link grammar (WEBUI.3 / #1073).
 * Extends existing ?root= / ?session= without a client router.
 * See docs/web-cockpit-contract.md §5.
 */
import {
  isWorkspaceMode,
  resolveModeSurface,
  type WorkspaceMode,
} from "./surfaces";
import type { Capabilities } from "./types";

export type DeepLinkState = {
  root: string;
  session: string;
  mode: WorkspaceMode;
  surface: string;
  entity: string;
  path: string;
  pane: string;
  agent: string;
};

export type ParsedDeepLink = {
  /** Raw query fields (empty string when absent). */
  raw: DeepLinkState;
  /** Whether `mode` was present in the query. */
  modePresent: boolean;
  /** Raw mode string from query (may be invalid). */
  modeRaw: string;
  /** Workspace id preference: root wins over session. */
  workspaceID: string;
};

function trim(value: string | null | undefined): string {
  return (value || "").trim();
}

function paramsFrom(search: string): URLSearchParams {
  const s = search.startsWith("?") || search.startsWith("#") ? search.slice(1) : search;
  return new URLSearchParams(s);
}

/** Parse location.search (with or without leading ?). */
export function parseDeepLink(search: string): ParsedDeepLink {
  const q = paramsFrom(search);
  const root = trim(q.get("root"));
  const session = trim(q.get("session"));
  const modeRaw = trim(q.get("mode")).toLowerCase();
  const modePresent = modeRaw !== "";
  const mode: WorkspaceMode = isWorkspaceMode(modeRaw) ? modeRaw : "chat";
  const raw: DeepLinkState = {
    root,
    session,
    mode,
    surface: trim(q.get("surface")),
    entity: trim(q.get("entity")),
    path: trim(q.get("path")),
    pane: trim(q.get("pane")),
    agent: trim(q.get("agent")),
  };
  // root wins when both present (contract §5.1).
  const workspaceID = root || session;
  return { raw, modePresent, modeRaw, workspaceID };
}

export type DeepLinkWrite = Partial<{
  root: string;
  session: string;
  mode: WorkspaceMode | "";
  surface: string;
  entity: string;
  path: string;
  pane: string;
  agent: string;
}>;

/**
 * Serialize deep-link fields into a query string (no leading ?).
 * Omits chat defaults and empty values. Preserves unknown keys from `baseSearch`.
 */
export function serializeDeepLink(
  state: DeepLinkWrite,
  baseSearch: string = "",
): string {
  const q = paramsFrom(baseSearch);
  // Drop token if present — server handoff owns auth cookies.
  q.delete("token");

  const setOrDel = (key: string, value: string | undefined, skip?: boolean) => {
    if (skip || !value) q.delete(key);
    else q.set(key, value);
  };

  if ("root" in state) setOrDel("root", state.root || "");
  if ("session" in state) setOrDel("session", state.session || "");
  if ("mode" in state) {
    const mode = state.mode || "";
    setOrDel("mode", mode, !mode || mode === "chat");
  }
  if ("surface" in state) setOrDel("surface", state.surface || "");
  if ("entity" in state) setOrDel("entity", state.entity || "");
  if ("path" in state) setOrDel("path", state.path || "");
  if ("pane" in state) setOrDel("pane", state.pane || "");
  if ("agent" in state) setOrDel("agent", state.agent || "");

  return q.toString();
}

/** Apply replaceState for mode/surface without reloading. */
export function writeDeepLinkToLocation(state: DeepLinkWrite): void {
  try {
    const qs = serializeDeepLink(state, location.search);
    const next = `${location.pathname}${qs ? `?${qs}` : ""}${location.hash || ""}`;
    const cur = `${location.pathname}${location.search}${location.hash || ""}`;
    if (next !== cur) {
      history.replaceState(null, "", next);
    }
  } catch {
    /* ignore (file:// or sandboxed) */
  }
}

export type ResolvedDeepLink = {
  workspaceID: string;
  mode: WorkspaceMode;
  surface?: string;
  entity: string;
  path: string;
  pane: string;
  agent: string;
  openDrawer: boolean;
  unavailable?: { id: string; reason: string };
};

/** Full resolution after bootstrap capabilities are known. */
export function resolveDeepLink(
  search: string,
  caps?: Capabilities,
  opts?: { attachOnly?: boolean; isLive?: boolean },
): ResolvedDeepLink {
  const parsed = parseDeepLink(search);
  const { raw, modePresent, modeRaw } = parsed;

  // Invalid explicit mode → treat as chat (safe default).
  let modeArg: string | undefined;
  if (modePresent) {
    modeArg = isWorkspaceMode(modeRaw) ? modeRaw : "chat";
  }

  const resolved = resolveModeSurface({
    mode: modeArg,
    surface: raw.surface,
    path: raw.path,
    pane: raw.pane,
    agent: raw.agent,
    caps,
    attachOnly: opts?.attachOnly,
    isLive: opts?.isLive,
  });

  return {
    workspaceID: parsed.workspaceID,
    mode: resolved.mode,
    surface: resolved.surface,
    entity: raw.entity,
    path: raw.path,
    pane: raw.pane,
    agent: raw.agent,
    openDrawer: resolved.openDrawer,
    unavailable: resolved.unavailable,
  };
}

/** Workspace id from search only (boot path helper). */
export function deepLinkWorkspaceID(
  search: string = typeof location !== "undefined" ? location.search : "",
): string {
  return parseDeepLink(search).workspaceID;
}
