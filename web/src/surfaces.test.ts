import { describe, expect, it } from "vitest";
import {
  BUILTIN_SURFACES,
  defaultSurfaceForMode,
  getSurface,
  inspectorSurfaces,
  inspectorSurfacesForShell,
  listSurfaces,
  modeAttention,
  MODE_PRESETS,
  resolveModeSurface,
  surfaceCapabilityAllowed,
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
};

describe("surface registry", () => {
  it("registers shipped inspector surfaces with stable ids", () => {
    const ids = BUILTIN_SURFACES.map((s) => s.id);
    for (const id of [
      "context", "files", "memory", "issues", "plans", "goals", "workflows",
      "mcp", "plugins", "panes", "timeline", "diagnostics", "roster", "settings",
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
    expect(project).toEqual(["plans"]);
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
    expect(project).toEqual(["plans", "goals", "issues", "memory", "workflows"]);
    expect(project).not.toContain("mcp");
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

  it("mode presets cover all workspace modes", () => {
    for (const id of WORKSPACE_MODES) {
      expect(MODE_PRESETS[id].id).toBe(id);
      expect(MODE_PRESETS[id].label.length).toBeGreaterThan(0);
    }
  });
});
