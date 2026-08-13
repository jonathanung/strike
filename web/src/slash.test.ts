import { describe, expect, it } from "vitest";
import { formatCostNotice, formatSlashHelp, leadingSlashToken, matchSlashCompletions, orderedSubsequence, resolveSlash, WEB_SLASH_COMMANDS } from "./slash";

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

describe("slash completion matching (TUI parity)", () => {
  const catalog = [
    { label: "/praline" },
    { label: "/provider" },
    { label: "/project" },
    { label: "/paper" },
    { label: "/PR" },
  ];

  it("ranks exact, then prefix, then ordered subsequence", () => {
    expect(matchSlashCompletions(catalog, "/Pr").map((c) => c.label)).toEqual([
      "/PR",
      "/praline",
      "/provider",
      "/project",
      "/paper",
    ]);
    expect(matchSlashCompletions(catalog, "/zzz")).toEqual([]);
  });

  it("matches /he as prefix and /hlp as ordered subsequence of /help", () => {
    const labels = WEB_SLASH_COMMANDS.map((c) => ({ label: c.label }));
    expect(matchSlashCompletions(labels, "he").some((c) => c.label === "/help")).toBe(true);
    expect(matchSlashCompletions(labels, "hlp").some((c) => c.label === "/help")).toBe(true);
    expect(orderedSubsequence("help", "hlp")).toBe(true);
    expect(orderedSubsequence("help", "he")).toBe(true);
    expect(orderedSubsequence("help", "hpq")).toBe(false);
  });

  it("opens only on line 0 while the cursor is inside the first token", () => {
    const cases: { name: string; value: string; cursor: number; open: boolean; query?: string; end?: number }[] = [
      { name: "token middle", value: "/provider argument\nlater", cursor: 3, open: true, query: "pr", end: 9 },
      { name: "token end", value: "/pr argument", cursor: 3, open: true, query: "pr", end: 3 },
      { name: "cursor after token", value: "/pr argument", cursor: 4, open: false },
      { name: "cursor at initial slash", value: "/pr", cursor: 0, open: false },
      { name: "not leading", value: "x/pr", cursor: 4, open: false },
      { name: "later line", value: "first\n/pr", cursor: 9, open: false },
      { name: "bare slash", value: "/", cursor: 1, open: true, query: "", end: 1 },
    ];
    for (const tt of cases) {
      const got = leadingSlashToken(tt.value, tt.cursor);
      expect({ name: tt.name, open: got !== null }).toEqual({ name: tt.name, open: tt.open });
      if (tt.open) {
        expect({ name: tt.name, query: got?.query, end: got?.end }).toEqual({
          name: tt.name,
          query: tt.query,
          end: tt.end,
        });
      }
    }
  });
});
