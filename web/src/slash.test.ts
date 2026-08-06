import { describe, expect, it } from "vitest";
import { formatCostNotice, formatSlashHelp, resolveSlash, WEB_SLASH_COMMANDS } from "./slash";

describe("resolveSlash", () => {
  it("passes through non-slash text", () => {
    expect(resolveSlash("hello")).toEqual({ kind: "pass" });
  });

  it("passes through known skills as prompts", () => {
    expect(resolveSlash("/ship", ["ship"])).toEqual({ kind: "pass" });
    expect(resolveSlash("/Ship now", ["ship"])).toEqual({ kind: "pass" });
  });

  it("rejects unknown slash commands", () => {
    expect(resolveSlash("/pets")).toEqual({ kind: "unknown", command: "/pets" });
    expect(resolveSlash("/nope args")).toEqual({ kind: "unknown", command: "/nope" });
  });

  it("maps engine ops and client actions", () => {
    expect(resolveSlash("/compact")).toEqual({ kind: "op", type: "compact", data: { strategy: "summarize" } });
    expect(resolveSlash("/prompt")).toEqual({ kind: "op", type: "inspect.prompt" });
    expect(resolveSlash("/context")).toEqual({ kind: "op", type: "inspect.prompt" });
    expect(resolveSlash("/rewind")).toEqual({ kind: "op", type: "rewind", data: {} });
    expect(resolveSlash("/rewind-files")).toEqual({ kind: "op", type: "rewind", data: { restoreFiles: true } });
    expect(resolveSlash("/interrupt")).toEqual({ kind: "op", type: "interrupt" });
    expect(resolveSlash("/export")).toEqual({ kind: "export" });
    expect(resolveSlash("/help")).toEqual({ kind: "help" });
    expect(resolveSlash("/queue")).toEqual({ kind: "queue" });
    expect(resolveSlash("/cost")).toEqual({ kind: "cost" });
    expect(resolveSlash("/copy")).toEqual({ kind: "copy" });
    expect(resolveSlash("/fork")).toEqual({ kind: "fork" });
    expect(resolveSlash("/rename New title")).toEqual({ kind: "rename", title: "New title" });
    expect(resolveSlash("/fast")).toEqual({ kind: "fast" });
    expect(resolveSlash("/fast on")).toEqual({ kind: "fast", enabled: true });
    expect(resolveSlash("/fast off")).toEqual({ kind: "fast", enabled: false });
    expect(resolveSlash("/agent build")).toEqual({ kind: "op", type: "select.agent", data: { name: "build" } });
    expect(resolveSlash("/effort high")).toEqual({ kind: "op", type: "set.effort", data: { level: "high" } });
    expect(resolveSlash("/autonomy agent")).toEqual({ kind: "op", type: "set.autonomy", data: { mode: "agent" } });
    expect(resolveSlash("/mode plan")).toEqual({ kind: "op", type: "set.permission_mode", data: { mode: "plan" } });
    expect(resolveSlash("/provider echo")).toEqual({ kind: "op", type: "select.model", data: { provider: "echo" } });
    expect(resolveSlash("/model echo/fast")).toEqual({
      kind: "op",
      type: "select.model",
      data: { provider: "echo", model: "fast" },
    });
    expect(resolveSlash("/model only")).toEqual({ kind: "op", type: "select.model", data: { model: "only" } });
  });

  it("returns usage for incomplete arg commands", () => {
    expect(resolveSlash("/agent").kind).toBe("usage");
    expect(resolveSlash("/effort").kind).toBe("usage");
    expect(resolveSlash("/fast maybe").kind).toBe("usage");
  });
});

describe("formatSlashHelp", () => {
  it("lists builtins and skills", () => {
    const text = formatSlashHelp([{ name: "ship", description: "Ship it" }]);
    expect(text).toContain("/export");
    expect(text).toContain("/help");
    expect(text).toContain("/ship");
    expect(text).toContain("Ship it");
    expect(WEB_SLASH_COMMANDS.length).toBeGreaterThan(4);
  });
});

describe("formatCostNotice", () => {
  it("reports missing cost and optional context", () => {
    expect(formatCostNotice({ provider: "echo", model: "m", contextUsed: 10, contextLimit: 100 })).toContain(
      "10 / 100",
    );
    expect(formatCostNotice({})).toContain("Cost: not reported");
  });
});
