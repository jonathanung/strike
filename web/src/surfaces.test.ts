import { afterEach, describe, expect, it } from "vitest";
import {
  BUILTIN_SURFACES,
  clearDynamicSurfaces,
  defaultSurfaceForMode,
  getSurface,
  inspectorSurfaces,
  inspectorSurfacesForShell,
  listSurfaces,
  LOOP_SUPERSEDED_NOTE,
  modeAttention,
  MODE_PRESETS,
  paneSurfaceFromInfo,
  registerDynamicSurface,
  resolveModeSurface,
  surfaceCapabilityAllowed,
  unregisterDynamicSurface,
  WORKSPACE_MODES,
} from "./surfaces";
import type { Capabilities } from "./types";

const fullCaps: Capabilities = {
  live: true,
  files: true,
  memory: true,
  issues: true,
  plans: true,
  goals: true,
  workflows: true,
  mcp: true,
  plugins: true,
  panes: true,
  timeline: true,
  lsp: true,
  settings: true,
  sessions: true,
  roots: true,
  diag: true,
  auth: true,
  catalog: true,
  permissions: true,
  sandbox: true,
};

describe("surface registry", () => {
  it("registers shipped inspector surfaces with stable ids", () => {
    const ids = BUILTIN_SURFACES.map((s) => s.id);
    for (const id of [
      "context", "files", "memory", "issues", "plans", "goals", "workflows",
      "mcp", "plugins", "panes", "timeline", "diagnostics", "roster", "settings",
      "providers", "auth", "permissions", "sandbox", "theme", "project-export", "diag-export",
      "transcript", "composer",
    ]) {
      expect(ids).toContain(id);
      expect(getSurface(id)?.id).toBe(id);
    }
  });

  it("filters inspector surfaces by capability", () => {
    const caps: Capabilities = { files: true, plans: true, memory: false };
    const code = inspectorSurfaces("code", caps).map((s) => s.id);
    expect(code).toContain("files");
    expect(code).not.toContain("diagnostics"); // lsp false

    const project = inspectorSurfaces("project", caps).map((s) => s.id);
    expect(project).toEqual(["plans", "project-export"]);
    expect(project).not.toContain("memory");
  });

  it("hides capability-false surfaces unless forced for deep link", () => {
    const caps: Capabilities = { plans: false, files: true };
    expect(listSurfaces({ mode: "project", caps, inspectorOnly: true })).toHaveLength(0);
    const forced = listSurfaces({
      mode: "project",
      caps,
      inspectorOnly: true,
      forceIds: ["plans"],
    });
    expect(forced.map((s) => s.id)).toEqual(["plans"]);
    expect(surfaceCapabilityAllowed(forced[0], caps)).toBe(false);
  });

  it("chat shell lists progressive union; project mode is strict", () => {
    const chat = inspectorSurfacesForShell("chat", fullCaps).map((s) => s.id);
    expect(chat).toContain("context");
    expect(chat).toContain("files");
    expect(chat).toContain("plans");
    expect(chat).toContain("mcp");

    const project = inspectorSurfacesForShell("project", fullCaps).map((s) => s.id);
    expect(project).toEqual(["plans", "goals", "issues", "memory", "workflows", "project-export"]);
    expect(project).not.toContain("mcp");
  });

  it("ops mode groups providers/settings before integrations and observe", () => {
    const ops = inspectorSurfacesForShell("ops", fullCaps).map((s) => s.id);
    expect(ops[0]).toBe("settings");
    expect(ops.indexOf("settings")).toBeLessThan(ops.indexOf("mcp"));
    expect(ops.indexOf("mcp")).toBeLessThan(ops.indexOf("plugins"));
    expect(ops.indexOf("plugins")).toBeLessThan(ops.indexOf("panes"));
    expect(ops.indexOf("panes")).toBeLessThan(ops.indexOf("diagnostics"));
    expect(ops.indexOf("diagnostics")).toBeLessThan(ops.indexOf("timeline"));
    expect(ops).toContain("providers");
    expect(ops).toContain("theme");
    expect(ops).not.toContain("plans");
  });

  it("activity disclosure badges modes without implying navigation", () => {
    expect(modeAttention("code", { changedFiles: 2 })).toBe("badge");
    expect(modeAttention("code", { changedFiles: 0 })).toBe("none");
    expect(modeAttention("team", { teamMembers: 3 })).toBe("badge");
    expect(modeAttention("team", { permissionPending: true })).toBe("needs-you");
    expect(modeAttention("chat", {})).toBe("none");
  });

  it("defaults surface per mode from preset when available", () => {
    expect(defaultSurfaceForMode("project", fullCaps)).toBe("plans");
    expect(defaultSurfaceForMode("code", fullCaps)).toBe("files");
    expect(defaultSurfaceForMode("ops", { settings: true, mcp: true })).toBe("settings");
    expect(defaultSurfaceForMode("ops", { mcp: true })).toBe("mcp");
    expect(defaultSurfaceForMode("chat", fullCaps)).toBe("context");
  });

  it("resolveModeSurface falls back to chat on invalid mode/surface", () => {
    expect(resolveModeSurface({}).mode).toBe("chat");
    expect(resolveModeSurface({ mode: "nope" }).mode).toBe("chat");
    const unknown = resolveModeSurface({ mode: "project", surface: "not-real", caps: fullCaps });
    expect(unknown.mode).toBe("project");
    expect(unknown.surface).toBe("plans");
  });

  it("resolveModeSurface marks capability-false deep links unavailable", () => {
    const r = resolveModeSurface({
      mode: "project",
      surface: "plans",
      caps: { plans: false },
    });
    expect(r.mode).toBe("project");
    expect(r.surface).toBe("plans");
    expect(r.unavailable?.id).toBe("plans");
    expect(r.openDrawer).toBe(true);
  });

  it("implies mode from path/agent/pane when mode omitted", () => {
    expect(resolveModeSurface({ path: "a.go", caps: fullCaps }).mode).toBe("code");
    expect(resolveModeSurface({ agent: "child-1", caps: fullCaps }).mode).toBe("team");
    expect(resolveModeSurface({ pane: "p1", caps: { panes: true } }).surface).toBe("panes");
  });

  it("keeps settings/theme/providers/auth in the inspector instead of opening the dialog", () => {
    for (const surface of ["settings", "theme", "providers", "auth"] as const) {
      const r = resolveModeSurface({ mode: "ops", surface, caps: fullCaps });
      expect(r.mode).toBe("ops");
      expect(r.surface).toBe(surface);
      expect(r.openDrawer).toBe(true);
      expect(r).not.toHaveProperty("openSettings", true);
    }
    const entering = resolveModeSurface({ mode: "ops", caps: fullCaps });
    expect(entering.surface).toBe("settings");
    expect(entering.openDrawer).toBe(true);
    expect(entering).not.toHaveProperty("openSettings", true);
  });

  it("mode presets cover all workspace modes", () => {
    for (const id of WORKSPACE_MODES) {
      expect(MODE_PRESETS[id].id).toBe(id);
      expect(MODE_PRESETS[id].label.length).toBeGreaterThan(0);
    }
  });
});

describe("dynamic pane surfaces (WEBUI.12)", () => {
  afterEach(() => {
    clearDynamicSurfaces();
  });

  it("registers pane contributions under chat and ops without shadowing builtins", () => {
    const def = paneSurfaceFromInfo({ id: "weather", title: "Weather" });
    expect(def?.id).toBe("pane:weather");
    expect(def?.modes).toEqual(["chat", "ops"]);
    registerDynamicSurface(def!);
    registerDynamicSurface({ ...def!, id: "settings" }); // must not shadow
    expect(getSurface("settings")?.label).toBe("settings");
    const ops = inspectorSurfaces("ops", { panes: true }).map((s) => s.id);
    expect(ops).toContain("pane:weather");
    expect(ops).toContain("panes");
    const chat = inspectorSurfaces("chat", { panes: true }).map((s) => s.id);
    expect(chat).toContain("pane:weather");
    expect(chat).not.toContain("panes");
    expect(inspectorSurfacesForShell("chat", { panes: true }).map((s) => s.id)).toContain("pane:weather");
    unregisterDynamicSurface("pane:weather");
    expect(getSurface("pane:weather")).toBeUndefined();
  });

  it("keeps chat mode when opening a pane surface from chat", () => {
    const def = paneSurfaceFromInfo({ id: "weather", title: "Weather" });
    registerDynamicSurface(def!);
    const r = resolveModeSurface({
      mode: "chat",
      surface: "pane:weather",
      caps: { panes: true },
    });
    expect(r.mode).toBe("chat");
    expect(r.surface).toBe("pane:weather");
    expect(r.openDrawer).toBe(true);
  });

  it("does not force the inspector open on cold chat load when panes exist", () => {
    const def = paneSurfaceFromInfo({ id: "weather", title: "Weather" });
    registerDynamicSurface(def!);
    const r = resolveModeSurface({ caps: { panes: true, live: true } });
    expect(r.mode).toBe("chat");
    expect(r.openDrawer).toBe(false);
  });

  it("bounds and strips control chars from pane titles", () => {
    const def = paneSurfaceFromInfo({
      id: "x",
      title: "Bad\u0000Title\u001b[31m" + "y".repeat(100),
    });
    expect(def?.label.includes("\u0000")).toBe(false);
    expect(def!.label.length).toBeLessThanOrEqual(64);
  });

  it("documents /loop as superseded by goals/workflows", () => {
    expect(LOOP_SUPERSEDED_NOTE.toLowerCase()).toContain("goals");
    expect(LOOP_SUPERSEDED_NOTE.toLowerCase()).toContain("workflows");
    expect(LOOP_SUPERSEDED_NOTE.toLowerCase()).toContain("/loop");
  });
});
