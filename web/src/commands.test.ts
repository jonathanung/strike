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

  it("detects @file mentions without email false positives", () => {
    expect(isFileMentionTrigger("see @src", 8).active).toBe(true);
    expect(isFileMentionTrigger("see @src/a.go", 13).query).toBe("src/a.go");
    expect(isFileMentionTrigger("user@example.com", 16).active).toBe(false);
    expect(isFileMentionTrigger(" (@y", 4).active).toBe(true);
    const next = insertMention("see @sr", 4, 7, "src/main.go");
    expect(next).toContain("@src/main.go");
  });
});
