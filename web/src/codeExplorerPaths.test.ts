import { describe, expect, it } from "vitest";
import {
  breadcrumbs,
  escapeHtml,
  isMarkdownPath,
  joinPath,
  parentPath,
  parseFileEntity,
  renderMarkdownSafe,
} from "./codeExplorerPaths";

describe("codeExplorer helpers", () => {
  it("joins and parents paths", () => {
    expect(joinPath("", "a.go")).toBe("a.go");
    expect(joinPath("pkg", "x.go")).toBe("pkg/x.go");
    expect(parentPath("pkg/x.go")).toBe("pkg");
    expect(parentPath("a.go")).toBe("");
  });

  it("builds breadcrumbs", () => {
    expect(breadcrumbs("a/b/c")).toEqual([
      { label: "root", path: "" },
      { label: "a", path: "a" },
      { label: "b", path: "a/b" },
      { label: "c", path: "a/b/c" },
    ]);
  });

  it("parses file entities with optional line", () => {
    expect(parseFileEntity("pkg/a.go:42")).toEqual({ path: "pkg/a.go", line: 42 });
    expect(parseFileEntity("pkg/a.go")).toEqual({ path: "pkg/a.go" });
  });

  it("detects markdown and escapes HTML in renderer", () => {
    expect(isMarkdownPath("README.md")).toBe(true);
    expect(isMarkdownPath("a.go")).toBe(false);
    expect(escapeHtml("<script>")).toBe("&lt;script&gt;");
    const html = renderMarkdownSafe("# Hi\n\n<script>alert(1)</script>\n\n**bold**");
    expect(html).toContain("<h2>");
    expect(html).toContain("&lt;script&gt;");
    expect(html).not.toContain("<script>");
    expect(html).toContain("<strong>bold</strong>");
  });
});
