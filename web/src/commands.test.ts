import { describe, expect, it } from "vitest";
import { buildCommandCatalog, fileMentionEmptyHint, filterCommands, insertMention, isFileMentionTrigger, mentionInsertCaret } from "./commands";

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
    expect(isFileMentionTrigger(" @y", 3).active).toBe(true);
    expect(isFileMentionTrigger("(@y", 3).active).toBe(false);
    const next = insertMention("see @sr", 4, 7, "src/main.go");
    expect(next).toBe("see @src/main.go ");
    expect(mentionInsertCaret(4, "src/main.go", next)).toBe("see @src/main.go ".length);
  });

  it("is cursor-aware inside the @ token and rewrites the whole mention", () => {
    const cases: { name: string; text: string; cursor: number; active: boolean; query?: string }[] = [
      { name: "at only", text: "@", cursor: 1, active: true, query: "" },
      { name: "partial", text: "@app", cursor: 4, active: true, query: "app" },
      { name: "mid token", text: "see @src/old.go extra", cursor: 8, active: true, query: "src" },
      { name: "mid line", text: "see @pkg", cursor: 8, active: true, query: "pkg" },
      { name: "email", text: "a@b.com", cursor: 3, active: false },
      { name: "second line", text: "hi\n@main", cursor: 8, active: true, query: "main" },
      { name: "after space token", text: "@a.go more", cursor: 10, active: false },
      { name: "cursor on at", text: "@app", cursor: 0, active: false },
    ];
    for (const tt of cases) {
      const got = isFileMentionTrigger(tt.text, tt.cursor);
      expect({ name: tt.name, active: got.active, query: got.active ? got.query : undefined }).toEqual({
        name: tt.name,
        active: tt.active,
        query: tt.query,
      });
    }
    expect(insertMention("see @src/old.go extra", 4, 8, "internal/frontend/tui/app.go")).toBe(
      "see @internal/frontend/tui/app.go extra",
    );
    expect(insertMention("see @src/old.go\nextra", 4, 8, "internal/frontend/tui/app.go")).toBe(
      "see @internal/frontend/tui/app.go\nextra",
    );
    expect(mentionInsertCaret(4, "internal/frontend/tui/app.go", "see @internal/frontend/tui/app.go extra")).toBe(
      "see @internal/frontend/tui/app.go ".length,
    );
  });

  it("explains zero @file hits like TUI emptyHint", () => {
    expect(fileMentionEmptyHint("")).toMatch(/no project files indexed/);
    expect(fileMentionEmptyHint("nope")).toMatch(/no files match/);
    expect(fileMentionEmptyHint("nope", true)).toMatch(/file search unavailable/);
  });
});
