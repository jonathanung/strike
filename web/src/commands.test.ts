import { describe, expect, it } from "vitest";
import { buildCommandCatalog, filterCommands, insertMention, isFileMentionTrigger } from "./commands";

describe("commands catalog", () => {
  it("includes modes and slash from one catalog", () => {
    const cat = buildCommandCatalog({ skills: [{ name: "review", description: "Review" }] });
    expect(cat.some((c) => c.id === "mode:chat")).toBe(true);
    expect(cat.some((c) => c.label === "/help")).toBe(true);
    expect(cat.some((c) => c.id === "skill:review")).toBe(true);
    expect(filterCommands(cat, "help").some((c) => c.label === "/help")).toBe(true);
  });

  it("ranks Open MCP above Mode: Ops when the query is mcp", () => {
    const cat = buildCommandCatalog({});
    const ops = cat.find((c) => c.id === "mode:ops");
    const mcp = cat.find((c) => c.id === "surface:mcp");
    expect(ops?.detail.toLowerCase()).toContain("mcp");
    expect(mcp?.label).toBe("Open MCP");
    const ranked = filterCommands(cat, "mcp");
    expect(ranked[0]?.id).toBe("surface:mcp");
    expect(ranked[0]?.label).toBe("Open MCP");
    const opsIdx = ranked.findIndex((c) => c.id === "mode:ops");
    expect(opsIdx).toBeGreaterThan(0);
  });

  it("ranks the Settings dialog above a mode whose blurb mentions settings", () => {
    const cat = buildCommandCatalog({});
    const ranked = filterCommands(cat, "settings");
    expect(ranked[0]?.id).toBe("session:settings");
  });

  it("detects @file mentions without email false positives", () => {
    expect(isFileMentionTrigger("see @src", 8).active).toBe(true);
    expect(isFileMentionTrigger("see @src/a.go", 13).query).toBe("src/a.go");
    expect(isFileMentionTrigger("user@example.com", 16).active).toBe(false);
    expect(isFileMentionTrigger(" (@y", 4).active).toBe(true);
    const next = insertMention("see @sr", 4, 7, "src/main.go");
    expect(next).toContain("@src/main.go");
  });
});
