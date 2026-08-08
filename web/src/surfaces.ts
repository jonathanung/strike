/**
 * Declarative surface registry and progressive mode presets (WEBUI.3 / #1073).
 * Normative metadata: docs/web-cockpit-contract.md §2–§3.
 */
import type { Capabilities } from "./types";

export type WorkspaceMode = "chat" | "code" | "team" | "project" | "ops";

export type SurfacePlacement = "canvas" | "drawer" | "sheet" | "rail" | "chrome" | "dialog";

export type AttentionKind = "none" | "badge" | "needs-you" | "busy";

export type AttachBehavior = "hidden" | "read" | "mutate-blocked";

/** Bootstrap capability key, always-on, live-only, or team observation. */
export type SurfaceCapability =
  | "always"
  | "live"
  | "team"
  | keyof Capabilities
  | string;

export type SurfaceDef = {
  /** Stable kebab-case id — never rename without migration. */
  id: string;
  label: string;
  /** Primary home mode(s). `any` = available across modes when listed. */
  modes: WorkspaceMode[] | "any";
  capability: SurfaceCapability;
  attention: AttentionKind;
  lazyMount: boolean;
  attach: AttachBehavior;
  placement: {
    desktop: SurfacePlacement;
    tablet: SurfacePlacement;
    phone: SurfacePlacement;
  };
  /** Appears in the mode secondary nav (inspector / drawer tabs). */
  inspector?: boolean;
  /** Sort order within inspector (ascending). */
  order?: number;
  /** Per-mode sort override (Ops/Project regroup without disturbing Chat union). */
  modeOrder?: Partial<Record<WorkspaceMode, number>>;
};

export type ModePreset = {
  id: WorkspaceMode;
  label: string;
  /** Accessible description for mode control. */
  description: string;
  /** Default secondary surface when entering the mode (inspector). */
  defaultSurface?: string;
  /** Canvas primary — chat keeps transcript; others open default surface. */
  primarySurface: string;
};

export type ActivitySignals = {
  changedFiles?: number;
  teamMembers?: number;
  permissionPending?: boolean;
  questionPending?: boolean;
  contextWarning?: boolean;
  fitWarning?: boolean;
};

export const WORKSPACE_MODES: readonly WorkspaceMode[] = [
  "chat",
  "code",
  "team",
  "project",
  "ops",
] as const;

export const MODE_PRESETS: Record<WorkspaceMode, ModePreset> = {
  chat: {
    id: "chat",
    label: "Chat",
    description: "Transcript and composer",
    primarySurface: "transcript",
  },
  code: {
    id: "code",
    label: "Code",
    description: "Files, diffs, and diagnostics",
    primarySurface: "files",
    defaultSurface: "files",
  },
  team: {
    id: "team",
    label: "Team",
    description: "Agents, roster, and handoffs",
    primarySurface: "roster",
    defaultSurface: "roster",
  },
  project: {
    id: "project",
    label: "Project",
    description: "Plans, goals, issues, memory, workflows",
    primarySurface: "plans",
    defaultSurface: "plans",
  },
  ops: {
    id: "ops",
    label: "Ops",
    description: "Providers, settings, MCP, plugins, panes, diagnostics",
    primarySurface: "settings",
    defaultSurface: "settings",
  },
};

/**
 * Built-in surfaces. Adding a panel = append here + optional mount in App;
 * do not extend a tab union or scatter switch cases for visibility.
 */
export const BUILTIN_SURFACES: readonly SurfaceDef[] = [
  {
    id: "transcript",
    label: "Transcript",
    modes: ["chat"],
    capability: "always",
    attention: "none",
    lazyMount: false,
    attach: "read",
    placement: { desktop: "canvas", tablet: "canvas", phone: "canvas" },
  },
  {
    id: "composer",
    label: "Composer",
    modes: ["chat"],
    capability: "live",
    attention: "none",
    lazyMount: false,
    attach: "mutate-blocked",
    placement: { desktop: "canvas", tablet: "canvas", phone: "canvas" },
  },
  {
    id: "sessions-rail",
    label: "Sessions",
    modes: "any",
    capability: "sessions",
    attention: "badge",
    lazyMount: false,
    attach: "read",
    placement: { desktop: "rail", tablet: "sheet", phone: "sheet" },
  },
  {
    id: "runtime",
    label: "Runtime",
    modes: ["chat"],
    capability: "live",
    attention: "none",
    lazyMount: false,
    attach: "mutate-blocked",
    placement: { desktop: "chrome", tablet: "chrome", phone: "sheet" },
  },
  {
    id: "asks",
    label: "Asks",
    modes: ["chat"],
    capability: "live",
    attention: "needs-you",
    lazyMount: false,
    attach: "mutate-blocked",
    placement: { desktop: "dialog", tablet: "dialog", phone: "dialog" },
  },
  {
    id: "context",
    label: "context",
    modes: ["chat", "ops"],
    capability: "always",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 10,
    modeOrder: { ops: 280 },
  },
  {
    id: "files",
    label: "files",
    modes: ["code"],
    capability: "files",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 20,
  },
  {
    id: "diagnostics",
    label: "diagnostics",
    modes: ["code", "ops"],
    capability: "lsp",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 30,
    modeOrder: { ops: 270 },
  },
  {
    id: "roster",
    label: "agents",
    modes: ["team"],
    capability: "team",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 40,
  },
  {
    id: "timeline",
    label: "timeline",
    modes: ["ops", "team"],
    capability: "timeline",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 50,
    modeOrder: { ops: 290, team: 50 },
  },
  {
    id: "plans",
    label: "plans",
    modes: ["project"],
    capability: "plans",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 60,
  },
  {
    id: "goals",
    label: "goals",
    modes: ["project"],
    capability: "goals",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 70,
  },
  {
    id: "issues",
    label: "issues",
    modes: ["project"],
    capability: "issues",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 80,
  },
  {
    id: "memory",
    label: "memory",
    modes: ["project"],
    capability: "memory",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 90,
  },
  {
    id: "workflows",
    label: "workflows",
    modes: ["project"],
    capability: "workflows",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 100,
  },
  {
    id: "project-export",
    label: "exports",
    modes: ["project"],
    capability: "project-export",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 105,
  },
  // --- Ops mode (WEBUI.12): providers/settings first, then integrations, then observe ---
  {
    id: "settings",
    label: "settings",
    modes: ["ops"],
    capability: "settings",
    attention: "none",
    lazyMount: true,
    attach: "mutate-blocked",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 200,
  },
  {
    id: "providers",
    label: "providers",
    modes: ["ops"],
    capability: "auth",
    attention: "none",
    lazyMount: true,
    attach: "mutate-blocked",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 210,
  },
  {
    id: "auth",
    label: "auth",
    modes: ["ops"],
    capability: "auth",
    attention: "none",
    lazyMount: true,
    attach: "mutate-blocked",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 215,
  },
  {
    id: "permissions",
    label: "permissions",
    modes: ["ops"],
    capability: "permissions",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 220,
  },
  {
    id: "sandbox",
    label: "sandbox",
    modes: ["ops"],
    capability: "sandbox",
    attention: "none",
    lazyMount: true,
    attach: "mutate-blocked",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 225,
  },
  {
    id: "theme",
    label: "theme",
    modes: ["ops"],
    capability: "settings",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 230,
  },
  {
    id: "mcp",
    label: "mcp",
    modes: ["ops"],
    capability: "mcp",
    attention: "badge",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 240,
  },
  {
    id: "plugins",
    label: "plugins",
    modes: ["ops"],
    capability: "plugins",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 250,
  },
  {
    id: "panes",
    label: "panes",
    modes: ["ops"],
    capability: "panes",
    attention: "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 260,
  },
  {
    id: "diag-export",
    label: "diagnostics export",
    modes: ["ops"],
    capability: "diag",
    attention: "none",
    lazyMount: true,
    attach: "mutate-blocked",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 300,
  },
] as const;

/** Dynamic pane/1 contributions registered at runtime (WEBUI.12). */
const dynamicSurfaces = new Map<string, SurfaceDef>();

/** Register or replace a dynamic surface (plugin pane). Ids must be unique. */
export function registerDynamicSurface(def: SurfaceDef): void {
  if (!def.id || BUILTIN_SURFACES.some((s) => s.id === def.id)) {
    // Never shadow builtins; ignore malformed empty ids.
    return;
  }
  dynamicSurfaces.set(def.id, def);
}

/** Drop one dynamic surface (pane unmounted / plugin disabled). */
export function unregisterDynamicSurface(id: string): void {
  dynamicSurfaces.delete(id);
}

/** Clear all dynamic surfaces (session reset). */
export function clearDynamicSurfaces(): void {
  dynamicSurfaces.clear();
}

/** Build a bounded pane surface from host pane metadata. */
export function paneSurfaceFromInfo(pane: {
  id?: string;
  title?: string;
  loadError?: string;
}): SurfaceDef | undefined {
  const id = String(pane.id || "").trim();
  if (!id) return undefined;
  // Bound id/label; strip C0 controls. Renderer still escapes text nodes.
  const safeId = id.replace(/[^\w.:@+-]+/g, "-").slice(0, 96);
  if (!safeId) return undefined;
  const surfaceId = safeId.startsWith("pane:") ? safeId : `pane:${safeId}`;
  const rawTitle = String(pane.title || id).replace(/[\u0000-\u001f\u007f]/g, "").slice(0, 64);
  return {
    id: surfaceId,
    label: rawTitle || surfaceId,
    modes: ["ops"],
    capability: "panes",
    attention: pane.loadError ? "badge" : "none",
    lazyMount: true,
    attach: "read",
    placement: { desktop: "drawer", tablet: "drawer", phone: "sheet" },
    inspector: true,
    order: 265,
  };
}

function allSurfaces(): SurfaceDef[] {
  if (dynamicSurfaces.size === 0) return [...BUILTIN_SURFACES];
  return [...BUILTIN_SURFACES, ...dynamicSurfaces.values()];
}

const byId = () => new Map(allSurfaces().map((s) => [s.id, s]));

export function getSurface(id: string): SurfaceDef | undefined {
  return byId().get(id);
}

export function isWorkspaceMode(value: string): value is WorkspaceMode {
  return (WORKSPACE_MODES as readonly string[]).includes(value);
}

export function surfaceInMode(surface: SurfaceDef, mode: WorkspaceMode): boolean {
  if (surface.modes === "any") return true;
  return surface.modes.includes(mode);
}

/**
 * Capability gate. `team` is always allowed at the mode level (empty roster is valid).
 */
export function surfaceCapabilityAllowed(
  surface: SurfaceDef,
  caps?: Capabilities,
  opts?: { isLive?: boolean },
): boolean {
  const c = surface.capability;
  if (c === "always") return true;
  if (c === "live") return Boolean(opts?.isLive ?? caps?.live);
  if (c === "team") return true;
  if (c === "sessions") return Boolean(caps?.sessions || caps?.roots);
  // Project exports when any project data surface is available (WEBUI.12).
  if (c === "project-export") {
    return Boolean(caps?.memory || caps?.issues || caps?.plans || caps?.goals || caps?.workflows);
  }
  if (!caps) return false;
  return Boolean(caps[c]);
}

export function surfaceAttachAllowed(
  surface: SurfaceDef,
  attachOnly?: boolean,
): boolean {
  if (!attachOnly) return true;
  return surface.attach !== "hidden";
}

export type ListSurfacesOpts = {
  caps?: Capabilities;
  mode?: WorkspaceMode;
  attachOnly?: boolean;
  isLive?: boolean;
  /** Include these ids even when capability is false (deep-link unavailable). */
  forceIds?: Iterable<string>;
  /** Only inspector-eligible surfaces. */
  inspectorOnly?: boolean;
};

/**
 * Filter registry for shell chrome. Capability-false surfaces are omitted
 * unless forced via deep link (caller renders unavailable state).
 */
export function listSurfaces(opts: ListSurfacesOpts = {}): SurfaceDef[] {
  const force = new Set(opts.forceIds || []);
  return allSurfaces().filter((surface) => {
    if (opts.inspectorOnly && !surface.inspector) return false;
    if (opts.mode && !surfaceInMode(surface, opts.mode)) {
      if (!force.has(surface.id)) return false;
    }
    if (!surfaceAttachAllowed(surface, opts.attachOnly) && !force.has(surface.id)) {
      return false;
    }
    const allowed = surfaceCapabilityAllowed(surface, opts.caps, { isLive: opts.isLive });
    if (!allowed && !force.has(surface.id)) return false;
    return true;
  }).sort((a, b) => surfaceOrder(a, opts.mode) - surfaceOrder(b, opts.mode));
}

function surfaceOrder(s: SurfaceDef, mode?: WorkspaceMode): number {
  if (mode && s.modeOrder && s.modeOrder[mode] !== undefined) {
    return s.modeOrder[mode] as number;
  }
  return s.order ?? 500;
}

/** Inspector tabs for a mode (capability-gated). */
export function inspectorSurfaces(
  mode: WorkspaceMode,
  caps?: Capabilities,
  opts?: { attachOnly?: boolean; isLive?: boolean; forceIds?: Iterable<string> },
): SurfaceDef[] {
  return listSurfaces({
    mode,
    caps,
    attachOnly: opts?.attachOnly,
    isLive: opts?.isLive,
    forceIds: opts?.forceIds,
    inspectorOnly: true,
  });
}

/**
 * Chat mode shows the progressive union of inspector surfaces the host can
 * serve (legacy strip). Other modes are strict presets over the registry.
 */
export function inspectorSurfacesForShell(
  mode: WorkspaceMode,
  caps?: Capabilities,
  opts?: { attachOnly?: boolean; isLive?: boolean; forceIds?: Iterable<string> },
): SurfaceDef[] {
  if (mode !== "chat") {
    return inspectorSurfaces(mode, caps, opts);
  }
  return listSurfaces({
    caps,
    attachOnly: opts?.attachOnly,
    isLive: opts?.isLive,
    forceIds: opts?.forceIds,
    inspectorOnly: true,
  });
}

export function modeAttention(
  mode: WorkspaceMode,
  activity: ActivitySignals = {},
): AttentionKind {
  switch (mode) {
    case "code":
      if ((activity.changedFiles ?? 0) > 0) return "badge";
      return "none";
    case "team":
      if (activity.permissionPending || activity.questionPending) return "needs-you";
      if ((activity.teamMembers ?? 0) > 0) return "badge";
      return "none";
    case "chat":
      if (activity.permissionPending || activity.questionPending) return "needs-you";
      if (activity.fitWarning || activity.contextWarning) return "badge";
      return "none";
    default:
      return "none";
  }
}

/** Pick default surface when entering a mode. */
export function defaultSurfaceForMode(
  mode: WorkspaceMode,
  caps?: Capabilities,
  opts?: { attachOnly?: boolean; isLive?: boolean; preferred?: string },
): string | undefined {
  const preset = MODE_PRESETS[mode];
  const list = inspectorSurfacesForShell(mode, caps, opts);
  if (opts?.preferred) {
    const hit = list.find((s) => s.id === opts.preferred);
    if (hit) return hit.id;
    if (getSurface(opts.preferred)) return opts.preferred;
  }
  if (preset.defaultSurface) {
    const hit = list.find((s) => s.id === preset.defaultSurface);
    if (hit) return hit.id;
  }
  return list[0]?.id;
}

/**
 * Resolve mode+surface after bootstrap. Invalid mode/surface → chat safe default.
 * Capability-false but registered surface → keep id and mark unavailable.
 */
export function resolveModeSurface(input: {
  mode?: string;
  surface?: string;
  path?: string;
  pane?: string;
  agent?: string;
  caps?: Capabilities;
  attachOnly?: boolean;
  isLive?: boolean;
}): {
  mode: WorkspaceMode;
  surface?: string;
  openDrawer: boolean;
  unavailable?: { id: string; reason: string };
  openSettings?: boolean;
} {
  const modeProvided = Boolean(input.mode && isWorkspaceMode(input.mode));
  let mode: WorkspaceMode = modeProvided ? (input.mode as WorkspaceMode) : "chat";

  if (!modeProvided) {
    if (input.path) mode = "code";
    else if (input.agent) mode = "team";
    else if (input.pane) mode = "ops";
    else mode = "chat";
  }

  let surface = (input.surface || "").trim();
  if (input.pane && !surface) surface = "panes";
  if (input.path && !surface) surface = "files";
  if (input.agent && !surface) surface = "roster";

  if (surface) {
    const def = getSurface(surface);
    if (!def) {
      surface = defaultSurfaceForMode(mode, input.caps, {
        attachOnly: input.attachOnly,
        isLive: input.isLive,
      }) || "";
      return {
        mode,
        surface: surface || undefined,
        openDrawer: Boolean(surface) && mode !== "chat",
      };
    }
    if (!surfaceInMode(def, mode) && def.modes !== "any") {
      const home = Array.isArray(def.modes) ? def.modes[0] : "chat";
      if (!modeProvided) mode = home;
    }
    const capOk = surfaceCapabilityAllowed(def, input.caps, { isLive: input.isLive });
    const attachOk = surfaceAttachAllowed(def, input.attachOnly);
    if (!capOk || !attachOk) {
      return {
        mode,
        surface: def.id,
        openDrawer: Boolean(def.inspector),
        openSettings: def.id === "settings" || def.id === "theme" || def.id === "providers" || def.id === "auth",
        unavailable: {
          id: def.id,
          reason: !attachOk
            ? "hidden in attach-only"
            : `capability ${String(def.capability)} unavailable`,
        },
      };
    }
    return {
      mode,
      surface: def.id,
      openDrawer: Boolean(def.inspector) || def.placement.desktop === "drawer",
      openSettings: def.id === "settings" || def.id === "theme" || def.id === "providers" || def.id === "auth",
    };
  }

  if (mode === "chat") {
    return { mode: "chat", openDrawer: false };
  }
  const defSurface = defaultSurfaceForMode(mode, input.caps, {
    attachOnly: input.attachOnly,
    isLive: input.isLive,
  });
  if (mode === "ops" && !defSurface) {
    return { mode, surface: "settings", openDrawer: false, openSettings: true };
  }
  return {
    mode,
    surface: defSurface,
    openDrawer: Boolean(defSurface),
    openSettings: defSurface === "settings",
  };
}

/** Project mode surface ids in list-first order (WEBUI.12). */
export const PROJECT_SURFACE_IDS = [
  "plans", "goals", "issues", "memory", "workflows", "project-export",
] as const;

/** Ops mode surface ids in coherent groups (WEBUI.12). */
export const OPS_SURFACE_IDS = [
  "settings", "providers", "auth", "permissions", "sandbox", "theme",
  "mcp", "plugins", "panes",
  "diagnostics", "context", "timeline", "diag-export",
] as const;

/**
 * TUI `/loop` is superseded on web by durable Goals and Workflows controls.
 * No session-only loop engine is exposed in the cockpit (WEBUI.12 non-goal).
 */
export const LOOP_SUPERSEDED_NOTE =
  "The TUI /loop shortcut is superseded here by Goals and Workflows. Use Project → goals or workflows for durable automation.";
