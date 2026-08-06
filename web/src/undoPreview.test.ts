import { describe, expect, it } from "vitest";
import {
  defaultRestoreFiles,
  emptyUndoPreview,
  filesChoiceDetail,
  formatUndoPreviewLines,
  hasRestorableFiles,
  hasUncovered,
  isRootLineage,
  parseUndoPreview,
} from "./undoPreview";

describe("undoPreview", () => {
  it("parses turn.completed harness fields", () => {
    const preview = parseUndoPreview({
      files: [{ path: "a.go", kind: "update" }, { path: "b.go", kind: "create" }, { path: "  " }],
      checkpointSkipped: 2,
      uncovered: ["bash", ""],
    });
    expect(preview.files).toEqual([
      { path: "a.go", kind: "update" },
      { path: "b.go", kind: "create" },
    ]);
    expect(preview.skipped).toBe(2);
    expect(preview.uncovered).toEqual(["bash"]);
  });

  it("treats missing fields as empty preview", () => {
    expect(parseUndoPreview(undefined)).toEqual(emptyUndoPreview());
    expect(hasRestorableFiles(emptyUndoPreview())).toBe(false);
    expect(hasUncovered(emptyUndoPreview())).toBe(false);
    expect(formatUndoPreviewLines(emptyUndoPreview())).toEqual([]);
  });

  it("filters child-lineage turns from the undo stack", () => {
    expect(isRootLineage({})).toBe(true);
    expect(isRootLineage({ parentSessionId: "p" })).toBe(false);
    expect(isRootLineage({ depth: 1 })).toBe(false);
    expect(isRootLineage({ depth: 0 })).toBe(true);
  });

  it("formats paths, skipped counts, and uncovered warnings", () => {
    const lines = formatUndoPreviewLines({
      files: [
        { path: "a.go", kind: "update" },
        { path: "b.go", kind: "create" },
      ],
      skipped: 1,
      uncovered: ["bash"],
    });
    expect(lines.map((l) => l.text)).toEqual([
      "Paths to restore (2):",
      "  update  a.go",
      "  create  b.go",
      "Checkpoint skipped: 1 path(s) (oversized/unreadable).",
      "Warning: uncovered mutations (bash) — shell/other changes are NOT restored.",
    ]);
    expect(lines.at(-1)?.tone).toBe("warn");
  });

  it("truncates long path lists", () => {
    const files = Array.from({ length: 15 }, (_, i) => ({ path: `f${i}.go`, kind: "update" }));
    const lines = formatUndoPreviewLines({ files, skipped: 0, uncovered: [] }, 12);
    expect(lines.some((l) => l.text.includes("… and 3 more"))).toBe(true);
  });

  it("aligns choice detail and default selection with TUI", () => {
    expect(filesChoiceDetail({ files: [{ path: "x" }], skipped: 0, uncovered: [] }))
      .toBe("drop the last turn and restore 1 path(s)");
    expect(filesChoiceDetail({ files: [], skipped: 2, uncovered: [] }))
      .toBe("drop the last turn; checkpointed paths were not capturable");
    expect(filesChoiceDetail({ files: [], skipped: 0, uncovered: ["bash"] }))
      .toBe("drop the last turn; no harness file snapshots (uncovered mutations)");

    expect(defaultRestoreFiles(emptyUndoPreview(), false)).toBe(false);
    expect(defaultRestoreFiles({ files: [{ path: "a" }], skipped: 0, uncovered: [] }, false)).toBe(true);
    expect(defaultRestoreFiles(emptyUndoPreview(), true)).toBe(true);
  });
});
