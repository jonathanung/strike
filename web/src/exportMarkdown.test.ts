import { describe, expect, it, vi, afterEach } from "vitest";
import { buildExportMarkdown, defaultExportFilename, downloadTextFile } from "./exportMarkdown";
import type { TranscriptItem } from "./types";

describe("buildExportMarkdown", () => {
  it("exports header and empty body", () => {
    const md = buildExportMarkdown([], {
      sessionId: "abc",
      title: "Demo",
      provider: "echo",
      model: "echo",
      agent: "build",
      exported: new Date("2026-01-02T03:04:05.000Z"),
    });
    expect(md).toContain("# Strike session export");
    expect(md).toContain("- **Session:** `abc`");
    expect(md).toContain("- **Title:** Demo");
    expect(md).toContain("- **Model:** echo / echo");
    expect(md).toContain("- **Agent:** build");
    expect(md).toContain("- **Exported:** 2026-01-02T03:04:05.000Z");
    expect(md).toContain("_Empty transcript._");
  });

  it("maps user, assistant, reasoning, tool, error, and system items", () => {
    const items: TranscriptItem[] = [
      { id: "1", kind: "user", text: "hello" },
      { id: "2", kind: "assistant", text: "world" },
      { id: "3", kind: "reasoning", text: "think" },
      { id: "4", kind: "tool", title: "bash", text: "ok\nline2", data: { name: "bash" } },
      { id: "5", kind: "error", text: "boom" },
      { id: "6", kind: "system", title: "Help", text: "commands" },
    ];
    const md = buildExportMarkdown(items, { sessionId: "s1" });
    expect(md).toContain("## You\n\nhello");
    expect(md).toContain("## Strike\n\nworld");
    expect(md).toContain("### Thinking");
    expect(md).toContain("```\nthink\n```");
    expect(md).toContain("### Tool: `bash` (ok)");
    expect(md).toContain("### Error\n\nboom");
    expect(md).toContain("### Help\n\ncommands");
  });

  it("truncates long tool output", () => {
    const long = Array.from({ length: 50 }, (_, i) => `line ${i}`).join("\n");
    const md = buildExportMarkdown([{ id: "t", kind: "tool", title: "bash", text: long }]);
    expect(md).toContain("... (truncated)");
  });
});

describe("defaultExportFilename", () => {
  it("includes session short id and stamp", () => {
    const name = defaultExportFilename("session-xyz-long", new Date("2026-08-06T12:00:00.000Z"));
    expect(name).toMatch(/^strike-session-xyz-\d{8}-\d{6}\.md$/);
  });
});

describe("downloadTextFile", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("creates an object URL and clicks an anchor", () => {
    const click = vi.fn();
    const remove = vi.fn();
    const createElement = vi.spyOn(document, "createElement").mockImplementation(() => {
      return { click, remove, rel: "", href: "", download: "" } as unknown as HTMLAnchorElement;
    });
    const append = vi.spyOn(document.body, "appendChild").mockImplementation((n) => n);
    const createObjectURL = vi.fn(() => "blob:test");
    const revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", { createObjectURL, revokeObjectURL });

    downloadTextFile("out.md", "# hi");

    expect(createObjectURL).toHaveBeenCalled();
    expect(createElement).toHaveBeenCalledWith("a");
    expect(append).toHaveBeenCalled();
    expect(click).toHaveBeenCalled();
    expect(remove).toHaveBeenCalled();
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:test");
  });
});
